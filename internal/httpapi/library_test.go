package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mangahub/internal/auth"
	"mangahub/internal/catalog"
	"mangahub/internal/database"
	"mangahub/internal/library"
	"mangahub/proto/mangapb"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const libraryTestSecret = "library-test-secret-at-least-32-characters-long"

type eventRecorder struct{ events []library.ProgressEvent }

func (r *eventRecorder) PublishProgress(e library.ProgressEvent) { r.events = append(r.events, e) }

type libraryFixture struct {
	router  *gin.Engine
	backend *sql.DB
	events  *eventRecorder
	server  *grpc.Server
}

func newLibraryFixture(t *testing.T) *libraryFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	backend, err := database.OpenBackend(filepath.Join(t.TempDir(), "backend.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	catdb, err := database.OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catdb.Close() })
	count, err := database.SeedCatalog(context.Background(), catdb)
	if err != nil || count < 30 {
		t.Fatalf("seed: %d, %v", count, err)
	}
	svc, err := catalog.NewService(catdb)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	mangapb.RegisterCatalogServiceServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })

	conn, err := grpc.NewClient("passthrough:///library-bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := mangapb.NewCatalogServiceClient(conn)
	ah, err := auth.NewHandler(backend, libraryTestSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	events := new(eventRecorder)
	handler, err := NewLibraryHandler(backend, client, 2*time.Second, events)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	ah.RegisterRoutes(router)
	handler.RegisterRoutes(router, ah.RequireAuth())
	return &libraryFixture{router: router, backend: backend, events: events, server: server}
}

func libRequest(t *testing.T, f *libraryFixture, method, path, body, token string, want int) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &content); err != nil {
		t.Fatalf("response JSON: %s, %v", w.Body.String(), err)
	}
	return content
}

func libRegisterAndLogin(t *testing.T, f *libraryFixture, username string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"SafePass123!"}`
	libRequest(t, f, "POST", "/api/auth/register", body, "", http.StatusCreated)
	response := libRequest(t, f, "POST", "/api/auth/login", body, "", http.StatusOK)
	var token string
	if err := json.Unmarshal(response["access_token"], &token); err != nil || token == "" {
		t.Fatalf("missing JWT: %v", err)
	}
	return token
}

func libEntry(t *testing.T, response map[string]json.RawMessage) library.Entry {
	t.Helper()
	var entry library.Entry
	if err := json.Unmarshal(response["entry"], &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestLibraryAPIAndUserIsolation(t *testing.T) {
	f := newLibraryFixture(t)
	alice := libRegisterAndLogin(t, f, "alice")
	bob := libRegisterAndLogin(t, f, "bob")
	libRequest(t, f, "GET", "/api/library", "", "", http.StatusUnauthorized)
	empty := libRequest(t, f, "GET", "/api/library", "", alice, http.StatusOK)
	if string(empty["library"]) != "[]" {
		t.Fatalf("expected empty array: %s", empty["library"])
	}

	response := libRequest(t, f, "POST", "/api/library", `{"manga_id":"one-piece","status":"reading"}`, alice, http.StatusCreated)
	entry := libEntry(t, response)
	if entry.MangaID != "one-piece" || entry.CurrentChapter != 0 || entry.Version != 1 || entry.Status != "reading" {
		t.Fatalf("invalid new entry: %#v", entry)
	}
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"one-piece"}`, alice, http.StatusConflict)
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"unknown-xyz"}`, alice, http.StatusNotFound)
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"naruto","user_id":2}`, alice, http.StatusBadRequest)

	own := libRequest(t, f, "GET", "/api/library", "", alice, http.StatusOK)
	var entries []library.Entry
	if err := json.Unmarshal(own["library"], &entries); err != nil || len(entries) != 1 {
		t.Fatalf("Alice list: %s, %v", own["library"], err)
	}
	other := libRequest(t, f, "GET", "/api/library", "", bob, http.StatusOK)
	if string(other["library"]) != "[]" {
		t.Fatalf("Bob saw Alice's data: %s", other["library"])
	}

	update := libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":25,"status":"reading","version":1}`, alice, http.StatusOK)
	changed := libEntry(t, update)
	if changed.CurrentChapter != 25 || changed.Version != 2 || changed.Status != "reading" {
		t.Fatalf("invalid progress: %#v", changed)
	}
	if len(f.events.events) != 1 || f.events.events[0].Chapter != 25 || f.events.events[0].Version != 2 || f.events.events[0].UserID <= 0 {
		t.Fatalf("no progress event after commit: %#v", f.events.events)
	}
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":26,"version":1}`, alice, http.StatusConflict)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":999999}`, alice, http.StatusBadRequest)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":-1}`, alice, http.StatusBadRequest)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":25,"version":2}`, alice, http.StatusOK)
	if len(f.events.events) != 1 {
		t.Fatalf("event published for failed/no-op update: %#v", f.events.events)
	}
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":1}`, bob, http.StatusNotFound)
	other = libRequest(t, f, "GET", "/api/library", "", bob, http.StatusOK)
	if string(other["library"]) != "[]" {
		t.Fatalf("Bob's data changed: %s", other["library"])
	}
}

func TestLibraryAPIErrorsAndCatalogUnavailable(t *testing.T) {
	f := newLibraryFixture(t)
	alice := libRegisterAndLogin(t, f, "alice")
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"one-piece","status":"invalid"}`, alice, http.StatusBadRequest)
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"one-piece"}{"x":1}`, alice, http.StatusBadRequest)
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"one-piece"}`, alice, http.StatusCreated)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"status":"reading"}`, alice, http.StatusBadRequest)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":1,"user_id":1}`, alice, http.StatusBadRequest)

	// A stopped gRPC Catalog must produce a controlled error, not a hang.
	f.server.Stop()
	start := time.Now()
	libRequest(t, f, "POST", "/api/library", `{"manga_id":"naruto"}`, alice, http.StatusServiceUnavailable)
	libRequest(t, f, "PUT", "/api/library/one-piece/progress", `{"current_chapter":1}`, alice, http.StatusServiceUnavailable)
	if time.Since(start) > 5*time.Second {
		t.Fatal("catalog failure caused a slow request")
	}
	// Reading already-stored library entries should remain possible.
	libRequest(t, f, "GET", "/api/library", "", alice, http.StatusOK)
}

func TestLibraryHandlerConstructorValidation(t *testing.T) {
	db, err := database.OpenBackend(filepath.Join(t.TempDir(), "backend.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewLibraryHandler(nil, nil, time.Second, nil); err == nil {
		t.Fatal("accepted nil DB")
	}
	if _, err := NewLibraryHandler(db, nil, time.Second, nil); err == nil {
		t.Fatal("accepted nil catalog")
	}
}
