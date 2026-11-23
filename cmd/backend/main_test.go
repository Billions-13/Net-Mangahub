package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mangahub/internal/catalog"
	"mangahub/internal/config"
	"mangahub/internal/database"
	tcpserver "mangahub/internal/tcp/server"
	"mangahub/proto/mangapb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const integrationJWTSecret = "mangahub-integration-tests-secret-very-long"

type integrationEntry struct {
	MangaID        string `json:"manga_id"`
	Status         string `json:"status"`
	CurrentChapter int32  `json:"current_chapter"`
	Version        int64  `json:"version"`
}

type integrationFrame struct {
	Type    string `json:"type"`
	UserID  int64  `json:"user_id"`
	MangaID string `json:"manga_id"`
	Chapter int32  `json:"chapter"`
	Version int64  `json:"version"`
}

// HTTP + gRPC + SQLite + authenticated TCP are exercised together here.
// All ports and databases are temporary; no .env or running servers are needed.
func TestBackendEndToEnd(t *testing.T) {
	root := t.TempDir()
	backendPath := filepath.Join(root, "backend.db")
	catalogPath := filepath.Join(root, "catalog.db")

	backendDB, err := database.OpenBackend(backendPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backendDB.Close() })

	catalogDB, err := database.OpenCatalog(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogDB.Close() })

	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 10*time.Second)
	inserted, err := database.SeedCatalog(seedCtx, catalogDB)
	cancelSeed()
	if err != nil || inserted < 30 {
		t.Fatalf("seed catalog: inserted=%d, error=%v", inserted, err)
	}

	catalogService, err := catalog.NewService(catalogDB)
	if err != nil {
		t.Fatal(err)
	}
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	mangapb.RegisterCatalogServiceServer(grpcServer, catalogService)
	var stopGRPC sync.Once
	stopCatalog := func() { stopGRPC.Do(grpcServer.Stop) }
	go func() { _ = grpcServer.Serve(grpcListener) }()
	t.Cleanup(func() {
		stopCatalog()
		_ = grpcListener.Close()
	})

	grpcConn, err := grpc.NewClient(
		"passthrough:///"+grpcListener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grpcConn.Close() })
	grpcClient := mangapb.NewCatalogServiceClient(grpcConn)

	progressServer, err := tcpserver.NewServer(backendDB, integrationJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpDone := make(chan error, 1)
	go func() { tcpDone <- progressServer.Serve(tcpListener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := progressServer.Shutdown(ctx); err != nil {
			t.Errorf("stop TCP server: %v", err)
		}
		if err := <-tcpDone; err != nil {
			t.Errorf("TCP server exited with error: %v", err)
		}
	})

	cfg := config.Config{JWTSecret: integrationJWTSecret, JWTTTL: time.Hour}
	router, stopExtensions, err := buildRouter(backendDB, grpcClient, cfg, progressServer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stopExtensions(context.Background()); err != nil {
			t.Errorf("stop extensions: %v", err)
		}
	})
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL
	client := &http.Client{Timeout: 7 * time.Second}

	// Health and the real Catalog gRPC lookup must work.
	apiRequest(t, client, base, "GET", "/api/health", "", nil, http.StatusOK)
	found := apiRequest(t, client, base, "GET", "/api/manga?q=One%20Piece", "", nil, http.StatusOK)
	var mangas []struct {
		ID string `json:"id"`
	}
	decodePart(t, found, "manga", &mangas)
	if len(mangas) != 1 || mangas[0].ID != "one-piece" {
		t.Fatalf("incorrect manga search results: %+v", mangas)
	}

	alice := registerAndLogin(t, client, base, "alice_e2e")
	bob := registerAndLogin(t, client, base, "bob_e2e")
	apiRequest(t, client, base, "POST", "/api/auth/register", "", map[string]string{
		"username": "alice_e2e", "password": "DemoPass123!",
	}, http.StatusConflict)
	apiRequest(t, client, base, "POST", "/api/auth/login", "", map[string]string{
		"username": "alice_e2e", "password": "wrong-password",
	}, http.StatusUnauthorized)
	apiRequest(t, client, base, "GET", "/api/library", "", nil, http.StatusUnauthorized)
	apiRequest(t, client, base, "GET", "/api/library", "invalid.jwt", nil, http.StatusUnauthorized)

	// Two TCP sessions for Alice and one for Bob authenticate using HTTP JWTs.
	aliceTCP1 := connectTCP(t, tcpListener.Addr().String(), alice)
	aliceTCP2 := connectTCP(t, tcpListener.Addr().String(), alice)
	bobTCP := connectTCP(t, tcpListener.Addr().String(), bob)
	defer aliceTCP1.Close()
	defer aliceTCP2.Close()
	defer bobTCP.Close()

	created := apiRequest(t, client, base, "POST", "/api/library", alice, map[string]string{
		"manga_id": "one-piece", "status": "reading",
	}, http.StatusCreated)
	var entry integrationEntry
	decodePart(t, created, "entry", &entry)
	if entry.MangaID != "one-piece" || entry.CurrentChapter != 0 || entry.Version != 1 {
		t.Fatalf("unexpected initial library entry: %+v", entry)
	}
	apiRequest(t, client, base, "POST", "/api/library", alice, map[string]string{
		"manga_id": "one-piece", "status": "reading",
	}, http.StatusConflict)

	// Bob must never read Alice's private library.
	bobList := apiRequest(t, client, base, "GET", "/api/library", bob, nil, http.StatusOK)
	var bobEntries []integrationEntry
	decodePart(t, bobList, "library", &bobEntries)
	if len(bobEntries) != 0 {
		t.Fatalf("Bob can see Alice's library: %+v", bobEntries)
	}
	apiRequest(t, client, base, "PUT", "/api/library/one-piece/progress", bob,
		map[string]any{"current_chapter": 25}, http.StatusNotFound)

	updated := apiRequest(t, client, base, "PUT", "/api/library/one-piece/progress", alice,
		map[string]any{"current_chapter": 25, "status": "reading", "version": 1}, http.StatusOK)
	decodePart(t, updated, "entry", &entry)
	if entry.CurrentChapter != 25 || entry.Version != 2 {
		t.Fatalf("wrong progress/version: %+v", entry)
	}
	for _, conn := range []net.Conn{aliceTCP1, aliceTCP2} {
		frame := readTCPFrame(t, conn, 2*time.Second)
		if frame.Type != "progress_updated" || frame.MangaID != "one-piece" || frame.Chapter != 25 || frame.Version != 2 {
			t.Fatalf("wrong TCP progress notification: %+v", frame)
		}
	}
	_ = bobTCP.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var oneByte [1]byte
	if _, err := bobTCP.Read(oneByte[:]); err == nil {
		t.Fatal("Bob received Alice's TCP event")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("Bob's TCP session closed unexpectedly: %v", err)
	}

	// Reject invalid chapters and stale versions without overwriting the row.
	apiRequest(t, client, base, "PUT", "/api/library/one-piece/progress", alice,
		map[string]any{"current_chapter": 999999, "version": 2}, http.StatusBadRequest)
	apiRequest(t, client, base, "PUT", "/api/library/one-piece/progress", alice,
		map[string]any{"current_chapter": 26, "version": 1}, http.StatusConflict)
	aliceList := apiRequest(t, client, base, "GET", "/api/library", alice, nil, http.StatusOK)
	var aliceEntries []integrationEntry
	decodePart(t, aliceList, "library", &aliceEntries)
	if len(aliceEntries) != 1 || aliceEntries[0].CurrentChapter != 25 || aliceEntries[0].Version != 2 {
		t.Fatalf("invalid request changed stored progress: %+v", aliceEntries)
	}

	// Opening a new SQLite handle confirms data is on disk, not in a Go map.
	reopened, err := database.OpenBackend(backendPath)
	if err != nil {
		t.Fatal(err)
	}
	var savedChapter int
	if err := reopened.QueryRow(`SELECT current_chapter FROM user_library WHERE manga_id = ?`,
		"one-piece").Scan(&savedChapter); err != nil {
		_ = reopened.Close()
		t.Fatal(err)
	}
	_ = reopened.Close()
	if savedChapter != 25 {
		t.Fatalf("SQLite persistence failed: chapter=%d", savedChapter)
	}

	// Stopping Catalog must yield an HTTP 503, not an endless request.
	stopCatalog()
	apiRequest(t, client, base, "GET", "/api/manga?q=One", "", nil, http.StatusServiceUnavailable)
}

func registerAndLogin(t *testing.T, client *http.Client, base, username string) string {
	t.Helper()
	credentials := map[string]string{"username": username, "password": "DemoPass123!"}
	apiRequest(t, client, base, "POST", "/api/auth/register", "", credentials, http.StatusCreated)
	login := apiRequest(t, client, base, "POST", "/api/auth/login", "", credentials, http.StatusOK)
	var token string
	decodePart(t, login, "access_token", &token)
	if token == "" {
		t.Fatal("login response did not include access_token")
	}
	return token
}

func apiRequest(t *testing.T, client *http.Client, base, method, path, jwt string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, base+path, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: status=%d, want=%d, body=%s", method, path, response.StatusCode, want, payload)
	}
	result := make(map[string]json.RawMessage)
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("%s %s returned invalid JSON: %v", method, path, err)
	}
	return result
}

func decodePart(t *testing.T, response map[string]json.RawMessage, key string, target any) {
	t.Helper()
	part, ok := response[key]
	if !ok {
		t.Fatalf("response missing JSON key %q", key)
	}
	if err := json.Unmarshal(part, target); err != nil {
		t.Fatalf("decode %q: %v", key, err)
	}
}

func connectTCP(t *testing.T, address, token string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "{\"type\":\"auth\",\"token\":%q}\n", token)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	frame := readTCPFrame(t, conn, 2*time.Second)
	if frame.Type != "authenticated" || frame.UserID <= 0 {
		_ = conn.Close()
		t.Fatalf("TCP login failed: %+v", frame)
	}
	return conn
}

func readTCPFrame(t *testing.T, conn net.Conn, timeout time.Duration) integrationFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read TCP frame: %v", err)
	}
	var frame integrationFrame
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("decode TCP frame: %v", err)
	}
	return frame
}
