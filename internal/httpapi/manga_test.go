package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mangahub/internal/catalog"
	"mangahub/internal/database"
	"mangahub/proto/mangapb"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// A real gRPC server and a real, temporary SQLite catalog are used here.
// No existing server, .env, or fixed port is required to run these tests.
func testMangaRouter(t *testing.T) (*gin.Engine, *grpc.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := database.OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inserted, err := database.SeedCatalog(ctx, db)
	if err != nil || inserted < 30 {
		t.Fatalf("seed catalog: count=%d, err=%v", inserted, err)
	}

	service, err := catalog.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })

	server := grpc.NewServer()
	mangapb.RegisterCatalogServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	handler, err := NewMangaHandler(mangapb.NewCatalogServiceClient(conn), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler.RegisterRoutes(router)
	return router, server
}

func mangaRequest(t *testing.T, router http.Handler, path string, want int) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("GET %s: HTTP %d (wanted %d): %s", path, w.Code, want, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response for %s: %v", path, err)
	}
	return body
}

func TestMangaRESTSearchDetailsAndErrors(t *testing.T) {
	router, _ := testMangaRouter(t)

	body := mangaRequest(t, router, "/api/manga?q="+url.QueryEscape("One Piece"), http.StatusOK)
	var found []Manga
	if err := json.Unmarshal(body["manga"], &found); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != "one-piece" || len(found[0].Genres) == 0 {
		t.Fatalf("unexpected search result: %#v", found)
	}

	body = mangaRequest(t, router, "/api/manga?q=nonexistent-manga-xyz", http.StatusOK)
	if err := json.Unmarshal(body["manga"], &found); err != nil || len(found) != 0 || found == nil {
		t.Fatalf("expected JSON empty array: %s, error=%v", body["manga"], err)
	}

	body = mangaRequest(t, router, "/api/manga", http.StatusOK)
	if err := json.Unmarshal(body["manga"], &found); err != nil || len(found) < 30 {
		t.Fatalf("expected at least 30 manga, found %d, error=%v", len(found), err)
	}

	body = mangaRequest(t, router, "/api/manga/one-piece", http.StatusOK)
	var details Manga
	if err := json.Unmarshal(body["manga"], &details); err != nil {
		t.Fatal(err)
	}
	if details.ID != "one-piece" || details.Title != "One Piece" || details.TotalChapters <= 0 || details.Description == "" {
		t.Fatalf("incomplete manga details: %#v", details)
	}
	mangaRequest(t, router, "/api/manga/no-such-id", http.StatusNotFound)
	mangaRequest(t, router, "/api/manga?q="+strings.Repeat("x", 101), http.StatusBadRequest)
	mangaRequest(t, router, "/api/manga/"+strings.Repeat("x", 129), http.StatusBadRequest)

	// Literal user input must not turn into a SQL wildcard query.
	body = mangaRequest(t, router, "/api/manga?q=%25", http.StatusOK)
	if err := json.Unmarshal(body["manga"], &found); err != nil || len(found) != 0 {
		t.Fatalf("LIKE wildcard was not escaped: %#v, %v", found, err)
	}
}

func TestMangaRESTCatalogStopped(t *testing.T) {
	router, server := testMangaRouter(t)
	// The initial request ensures the gRPC client establishes a connection.
	mangaRequest(t, router, "/api/manga/one-piece", http.StatusOK)
	server.Stop()

	started := time.Now()
	mangaRequest(t, router, "/api/manga?q=One", http.StatusServiceUnavailable)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("stopped catalog request hung for %s", elapsed)
	}
}

func TestMangaRESTConstructorValidation(t *testing.T) {
	if _, err := NewMangaHandler(nil, time.Second); err == nil {
		t.Fatal("missing Catalog client should fail")
	}
	// A valid client is covered in the test fixture above.
	router, _ := testMangaRouter(t)
	mangaRequest(t, router, "/api/manga?q=One", http.StatusOK)
}
