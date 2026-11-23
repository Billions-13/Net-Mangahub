package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	issuer         = "mangahub"
	userIDContext  = "authenticated_user_id"
	maxRequestSize = 16 << 10 // 16 KiB
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

// User is safe to include in an API response; password hashes are never returned.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type credentials struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Handler owns registration, login, and JWT authorization for the HTTP backend.
type Handler struct {
	db        *sql.DB
	jwtSecret []byte
	jwtTTL    time.Duration
}

// NewHandler validates dependencies once at backend startup.
func NewHandler(db *sql.DB, jwtSecret string, jwtTTL time.Duration) (*Handler, error) {
	if db == nil {
		return nil, errors.New("auth: backend database is required")
	}
	if len(jwtSecret) < 32 || strings.HasPrefix(jwtSecret, "CHANGE_ME") {
		return nil, errors.New("auth: JWT_SECRET must have at least 32 characters and cannot be a placeholder")
	}
	if jwtTTL <= 0 || jwtTTL > 30*24*time.Hour {
		return nil, errors.New("auth: JWT duration must be greater than zero and at most 30 days")
	}
	return &Handler{db: db, jwtSecret: []byte(jwtSecret), jwtTTL: jwtTTL}, nil
}

// RegisterRoutes adds only public auth endpoints. Protected APIs should use
// RequireAuth() as middleware; no user ID from a client request is trusted.
func (h *Handler) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/auth/register", h.Register)
	router.POST("/api/auth/login", h.Login)
}

// Register creates a user. The client must log in separately to receive a JWT.
func (h *Handler) Register(c *gin.Context) {
	var req credentials
	if !readCredentials(c, &req) {
		return
	}
	if !usernamePattern.MatchString(req.Username) {
		respondError(c, http.StatusBadRequest, "username must contain 3-32 ASCII letters, numbers, or underscores")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		respondError(c, http.StatusBadRequest, "password must contain 8-72 bytes")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not register user")
		return
	}
	result, err := h.db.ExecContext(c.Request.Context(),
		"INSERT INTO users (username, password_hash) VALUES (?, ?)", req.Username, string(hash))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
			respondError(c, http.StatusConflict, "username already exists")
			return
		}
		respondError(c, http.StatusInternalServerError, "could not register user")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not read user ID")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": User{ID: id, Username: req.Username}})
}

// Login validates the stored password hash and issues a short-lived signed JWT.
func (h *Handler) Login(c *gin.Context) {
	var req credentials
	if !readCredentials(c, &req) {
		return
	}
	if !usernamePattern.MatchString(req.Username) || len(req.Password) > 72 {
		respondError(c, http.StatusUnauthorized, "invalid username or password")
		return
	}

	var user User
	var hash string
	err := h.db.QueryRowContext(c.Request.Context(),
		"SELECT id, username, password_hash FROM users WHERE username = ?", req.Username).
		Scan(&user.ID, &user.Username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		respondError(c, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not log in")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		respondError(c, http.StatusUnauthorized, "invalid username or password")
		return
	}

	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   strconv.FormatInt(user.ID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(h.jwtTTL)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(h.jwtSecret)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not create access token")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token": signed,
		"token_type":   "Bearer",
		"expires_in":   int64(h.jwtTTL / time.Second),
		"user":         user,
	})
}

// RequireAuth verifies the token signature, expiry, issuer, and user identity.
// Middleware places a trusted user ID in Gin context for private API handlers.
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			respondError(c, http.StatusUnauthorized, "Bearer token required")
			c.Abort()
			return
		}

		claims := new(jwt.RegisteredClaims)
		parsed, err := jwt.ParseWithClaims(parts[1], claims,
			func(_ *jwt.Token) (any, error) { return h.jwtSecret, nil },
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
		)
		if err != nil || !parsed.Valid {
			respondError(c, http.StatusUnauthorized, "invalid or expired token")
			c.Abort()
			return
		}
		userID, err := strconv.ParseInt(claims.Subject, 10, 64)
		if err != nil || userID <= 0 {
			respondError(c, http.StatusUnauthorized, "invalid token subject")
			c.Abort()
			return
		}

		// A removed user must not retain access until a token expires.
		var exists int
		err = h.db.QueryRowContext(c.Request.Context(),
			"SELECT 1 FROM users WHERE id = ?", userID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			respondError(c, http.StatusUnauthorized, "user no longer exists")
			c.Abort()
			return
		}
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "authentication database unavailable")
			c.Abort()
			return
		}
		c.Set(userIDContext, userID)
		c.Next()
	}
}

// CurrentUserID returns the verified user ID from RequireAuth middleware.
// Library endpoints must use this value, never a user_id from request JSON.
func CurrentUserID(c *gin.Context) (int64, bool) {
	value, ok := c.Get(userIDContext)
	if !ok {
		return 0, false
	}
	id, ok := value.(int64)
	return id, ok && id > 0
}

func readCredentials(c *gin.Context, req *credentials) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestSize)
	if err := c.ShouldBindJSON(req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid JSON or missing username/password")
		return false
	}
	return true
}

func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}
