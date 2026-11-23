package auth

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mangahub/internal/database"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const testJWTSecret = "local-test-secret-must-be-at-least-32-bytes-long"

func newTestRouter(t *testing.T, db *sql.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewHandler(db, testJWTSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler.RegisterRoutes(router)
	router.GET("/api/private/whoami", handler.RequireAuth(), func(c *gin.Context) {
		id, ok := CurrentUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": id})
	})
	return router
}

func callAPI(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func mustStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("HTTP %d, expected %d: %s", response.Code, want, response.Body.String())
	}
}

func testLogin(t *testing.T, router http.Handler, name, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": name, "password": password})
	response := callAPI(t, router, http.MethodPost, "/api/auth/login", string(body), "")
	mustStatus(t, response, http.StatusOK)
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" || result.TokenType != "Bearer" || result.ExpiresIn != 3600 {
		t.Fatalf("unexpected login response: %s", response.Body.String())
	}
	return result.AccessToken
}

func TestRegisterLoginJWTAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.db")
	db, err := database.OpenBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router := newTestRouter(t, db)

	registerAlice := `{"username":"alice","password":"S3curePass123"}`
	response := callAPI(t, router, http.MethodPost, "/api/auth/register", registerAlice, "")
	mustStatus(t, response, http.StatusCreated)
	if bytes.Contains(response.Body.Bytes(), []byte("S3curePass123")) {
		t.Fatal("registration response leaked a password")
	}

	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE username = ?", "alice").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == "S3curePass123" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("S3curePass123")) != nil {
		t.Fatal("password was not securely hashed")
	}

	response = callAPI(t, router, http.MethodPost, "/api/auth/register", `{"username":"ALICE","password":"OtherPass123"}`, "")
	mustStatus(t, response, http.StatusConflict)
	response = callAPI(t, router, http.MethodPost, "/api/auth/register", `{"username":"a!","password":"OtherPass123"}`, "")
	mustStatus(t, response, http.StatusBadRequest)
	response = callAPI(t, router, http.MethodPost, "/api/auth/register", `{"username":"charlie","password":"short"}`, "")
	mustStatus(t, response, http.StatusBadRequest)

	response = callAPI(t, router, http.MethodPost, "/api/auth/login", `{"username":"alice","password":"wrongPassword"}`, "")
	mustStatus(t, response, http.StatusUnauthorized)
	response = callAPI(t, router, http.MethodPost, "/api/auth/login", `{"username":"unknown","password":"S3curePass123"}`, "")
	mustStatus(t, response, http.StatusUnauthorized)

	aliceToken := testLogin(t, router, "alice", "S3curePass123")
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", "")
	mustStatus(t, response, http.StatusUnauthorized)
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", "not-a-jwt")
	mustStatus(t, response, http.StatusUnauthorized)
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", aliceToken)
	mustStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `"user_id":1`) {
		t.Fatalf("unexpected user ID: %s", response.Body.String())
	}

	response = callAPI(t, router, http.MethodPost, "/api/auth/register", `{"username":"bob","password":"AnotherPass123"}`, "")
	mustStatus(t, response, http.StatusCreated)
	bobToken := testLogin(t, router, "bob", "AnotherPass123")
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", bobToken)
	mustStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `"user_id":2`) {
		t.Fatalf("Bob was incorrectly authenticated as Alice: %s", response.Body.String())
	}

	// Tokens with a forged signature or expired claims must not pass.
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: issuer, Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	forgedString, err := forged.SignedString([]byte("wrong-secret-key-wrong-secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", forgedString)
	mustStatus(t, response, http.StatusUnauthorized)

	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: issuer, Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	})
	expiredString, err := expired.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	response = callAPI(t, router, http.MethodGet, "/api/private/whoami", "", expiredString)
	mustStatus(t, response, http.StatusUnauthorized)

	// Closing and reopening the database must preserve registered users.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.OpenBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router = newTestRouter(t, db)
	_ = testLogin(t, router, "alice", "S3curePass123")
}

func TestInvalidConfigurationAndLargeRequest(t *testing.T) {
	if _, err := NewHandler(nil, testJWTSecret, time.Hour); err == nil {
		t.Fatal("nil database must be rejected")
	}
	path := filepath.Join(t.TempDir(), "backend.db")
	db, err := database.OpenBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewHandler(db, "short", time.Hour); err == nil {
		t.Fatal("short JWT secret must be rejected")
	}
	if _, err := NewHandler(db, testJWTSecret, 0); err == nil {
		t.Fatal("zero JWT TTL must be rejected")
	}
	router := newTestRouter(t, db)
	largePayload := fmt.Sprintf(`{"username":"alice","password":"%s"}`, strings.Repeat("x", 20_000))
	response := callAPI(t, router, http.MethodPost, "/api/auth/register", largePayload, "")
	mustStatus(t, response, http.StatusBadRequest)
}
