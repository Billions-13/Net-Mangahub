package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"mangahub/internal/auth"
	"mangahub/internal/library"
	"mangahub/proto/mangapb"

	"github.com/gin-gonic/gin"
)

// LibraryHandler owns private HTTP library operations, not Catalog SQLite.
// The catalog is consulted over gRPC for chapter limits and valid manga IDs.
type LibraryHandler struct {
	store    *library.Store
	catalog  mangapb.CatalogServiceClient
	timeout  time.Duration
	notifier library.ProgressNotifier
}

// Pass nil for notifier until the TCP progress server has been constructed.
// The backend will inject that server here when wiring all components.
func NewLibraryHandler(db *sql.DB, catalog mangapb.CatalogServiceClient, timeout time.Duration, notifier library.ProgressNotifier) (*LibraryHandler, error) {
	store, err := library.NewStore(db)
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("library API: catalog gRPC client is required")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("library API: timeout must be between 1ns and 30 seconds")
	}
	return &LibraryHandler{store: store, catalog: catalog, timeout: timeout, notifier: notifier}, nil
}

// RegisterRoutes requires the JWT middleware from auth.Handler.RequireAuth().
// No client-supplied user_id is ever trusted.
func (h *LibraryHandler) RegisterRoutes(router gin.IRouter, requireAuth gin.HandlerFunc) {
	router.GET("/api/library", requireAuth, h.List)
	router.POST("/api/library", requireAuth, h.Add)
	router.PUT("/api/library/:mangaId/progress", requireAuth, h.UpdateProgress)
}

func (h *LibraryHandler) List(c *gin.Context) {
	userID, ok := auth.CurrentUserID(c)
	if !ok {
		apiError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	entries, err := h.store.List(c.Request.Context(), userID)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "library database unavailable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"library": entries})
}

type addLibraryRequest struct {
	MangaID string `json:"manga_id"`
	Status  string `json:"status"`
}

func (h *LibraryHandler) Add(c *gin.Context) {
	userID, ok := auth.CurrentUserID(c)
	if !ok {
		apiError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	var input addLibraryRequest
	if !readStrictLibraryJSON(c, &input) {
		return
	}
	if !validLibraryMangaID(input.MangaID) {
		apiError(c, http.StatusBadRequest, "invalid manga_id")
		return
	}
	if input.Status == "" {
		input.Status = "plan-to-read"
	}
	if !library.ValidStatus(input.Status) {
		apiError(c, http.StatusBadRequest, "invalid reading status")
		return
	}
	if _, err := h.fetchCatalogManga(c.Request.Context(), input.MangaID); err != nil {
		catalogError(c, err)
		return
	}
	entry, err := h.store.Add(c.Request.Context(), userID, input.MangaID, input.Status)
	switch {
	case errors.Is(err, library.ErrConflict):
		apiError(c, http.StatusConflict, "manga already exists in your library")
	case errors.Is(err, library.ErrInvalid):
		apiError(c, http.StatusBadRequest, "invalid library entry")
	case err != nil:
		apiError(c, http.StatusServiceUnavailable, "library database unavailable")
	default:
		c.JSON(http.StatusCreated, gin.H{"entry": entry})
	}
}

type updateProgressRequest struct {
	CurrentChapter *int32  `json:"current_chapter"`
	Status         *string `json:"status"`
	Version        *int64  `json:"version"`
}

func (h *LibraryHandler) UpdateProgress(c *gin.Context) {
	userID, ok := auth.CurrentUserID(c)
	if !ok {
		apiError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	mangaID := c.Param("mangaId")
	if !validLibraryMangaID(mangaID) {
		apiError(c, http.StatusBadRequest, "invalid manga ID")
		return
	}
	var input updateProgressRequest
	if !readStrictLibraryJSON(c, &input) {
		return
	}
	if input.CurrentChapter == nil || *input.CurrentChapter < 0 ||
		(input.Status != nil && !library.ValidStatus(*input.Status)) ||
		(input.Version != nil && *input.Version < 1) {
		apiError(c, http.StatusBadRequest, "invalid chapter, status, or version")
		return
	}

	// A missing library entry is not an implicit add operation.
	if _, err := h.store.Get(c.Request.Context(), userID, mangaID); err != nil {
		if errors.Is(err, library.ErrNotFound) {
			apiError(c, http.StatusNotFound, "manga not found in your library")
		} else {
			apiError(c, http.StatusServiceUnavailable, "library database unavailable")
		}
		return
	}

	manga, err := h.fetchCatalogManga(c.Request.Context(), mangaID)
	if err != nil {
		catalogError(c, err)
		return
	}
	if *input.CurrentChapter > manga.GetTotalChapters() {
		apiError(c, http.StatusBadRequest, "current_chapter exceeds total_chapters")
		return
	}

	entry, changed, err := h.store.UpdateProgress(c.Request.Context(), userID,
		mangaID, *input.CurrentChapter, input.Status, input.Version)
	switch {
	case errors.Is(err, library.ErrNotFound):
		apiError(c, http.StatusNotFound, "manga not found in your library")
	case errors.Is(err, library.ErrConflict):
		apiError(c, http.StatusConflict, "progress version conflict")
	case errors.Is(err, library.ErrInvalid):
		apiError(c, http.StatusBadRequest, "invalid progress update")
	case err != nil:
		apiError(c, http.StatusServiceUnavailable, "library database unavailable")
	default:
		// A committed update must reach the progress notifier exactly once.
		if changed && h.notifier != nil {
			h.notifier.PublishProgress(library.ProgressEvent{
				UserID: userID, MangaID: mangaID,
				Chapter: entry.CurrentChapter, Version: entry.Version,
			})
		}
		c.JSON(http.StatusOK, gin.H{"entry": entry})
	}
}

func (h *LibraryHandler) fetchCatalogManga(ctx context.Context, mangaID string) (*mangapb.Manga, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	response, err := h.catalog.GetManga(rpcCtx, &mangapb.GetMangaRequest{Id: mangaID})
	if err != nil {
		return nil, err
	}
	if response == nil || response.GetManga() == nil {
		return nil, errors.New("invalid catalog response")
	}
	return response.GetManga(), nil
}

func validLibraryMangaID(id string) bool {
	return id != "" && id == strings.TrimSpace(id) && len(id) <= 128 && !strings.ContainsAny(id, "/\\")
}

// Strict parsing also rejects user_id and other unexpected fields, oversized
// requests, and trailing JSON objects. Request bodies are limited to 16 KiB.
func readStrictLibraryJSON(c *gin.Context, out any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		apiError(c, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	var additional any
	if err := decoder.Decode(&additional); !errors.Is(err, io.EOF) {
		apiError(c, http.StatusBadRequest, "body must contain one JSON object")
		return false
	}
	return true
}
