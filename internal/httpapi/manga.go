package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"mangahub/proto/mangapb"

	"context"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Manga is the stable JSON representation returned by the public Manga API.
// The Catalog gRPC service, rather than the HTTP backend, owns manga records.
type Manga struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Genres        []string `json:"genres"`
	Status        string   `json:"status"`
	TotalChapters int32    `json:"total_chapters"`
	Description   string   `json:"description"`
}

// MangaHandler translates HTTP requests to real CatalogService gRPC calls.
type MangaHandler struct {
	catalog mangapb.CatalogServiceClient
	timeout time.Duration
}

// NewMangaHandler validates its dependencies. Pass a gRPC client connected to
// the independent Catalog process and a timeout such as 2*time.Second.
func NewMangaHandler(client mangapb.CatalogServiceClient, timeout time.Duration) (*MangaHandler, error) {
	if client == nil {
		return nil, errors.New("manga API: catalog gRPC client is required")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("manga API: timeout must be between 1ns and 30 seconds")
	}
	return &MangaHandler{catalog: client, timeout: timeout}, nil
}

// RegisterRoutes adds read-only public API endpoints. Authentication is not
// required to browse manga; private Library operations will require JWT.
func (h *MangaHandler) RegisterRoutes(router gin.IRouter) {
	router.GET("/api/manga", h.SearchManga)
	router.GET("/api/manga/:id", h.GetManga)
}

// SearchManga responds with {"manga": [...]} (an empty array if no matches).
// With no q parameter the Catalog service returns its first 100 manga.
func (h *MangaHandler) SearchManga(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if len(query) > 100 {
		apiError(c, http.StatusBadRequest, "query must not exceed 100 bytes")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	resp, err := h.catalog.SearchManga(ctx, &mangapb.SearchMangaRequest{Query: query})
	if err != nil {
		catalogError(c, err)
		return
	}
	if resp == nil {
		apiError(c, http.StatusServiceUnavailable, "catalog service unavailable")
		return
	}

	result := make([]Manga, 0, len(resp.GetManga()))
	for _, manga := range resp.GetManga() {
		if manga == nil {
			apiError(c, http.StatusServiceUnavailable, "invalid response from catalog service")
			return
		}
		result = append(result, mangaJSON(manga))
	}
	c.JSON(http.StatusOK, gin.H{"manga": result})
}

// GetManga responds with {"manga": {...}} for one catalog manga ID.
func (h *MangaHandler) GetManga(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" || len(id) > 128 {
		apiError(c, http.StatusBadRequest, "invalid manga ID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	resp, err := h.catalog.GetManga(ctx, &mangapb.GetMangaRequest{Id: id})
	if err != nil {
		catalogError(c, err)
		return
	}
	if resp == nil || resp.GetManga() == nil {
		apiError(c, http.StatusServiceUnavailable, "invalid response from catalog service")
		return
	}
	c.JSON(http.StatusOK, gin.H{"manga": mangaJSON(resp.GetManga())})
}

func mangaJSON(src *mangapb.Manga) Manga {
	genres := append([]string{}, src.GetGenres()...)
	return Manga{
		ID:            src.GetId(),
		Title:         src.GetTitle(),
		Author:        src.GetAuthor(),
		Genres:        genres,
		Status:        src.GetStatus(),
		TotalChapters: src.GetTotalChapters(),
		Description:   src.GetDescription(),
	}
}

// catalogError maps gRPC statuses without exposing internal database details.
func catalogError(c *gin.Context, err error) {
	switch status.Code(err) {
	case codes.InvalidArgument:
		apiError(c, http.StatusBadRequest, "invalid manga request")
	case codes.NotFound:
		apiError(c, http.StatusNotFound, "manga not found")
	case codes.PermissionDenied:
		apiError(c, http.StatusForbidden, "access denied")
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		apiError(c, http.StatusServiceUnavailable, "catalog service unavailable")
	default:
		apiError(c, http.StatusServiceUnavailable, "catalog request failed")
	}
}

func apiError(c *gin.Context, httpStatus int, message string) {
	c.JSON(httpStatus, gin.H{"error": message})
}
