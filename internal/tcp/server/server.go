package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"mangahub/internal/library"

	"github.com/golang-jwt/jwt/v5"
)

const (
	maxLineBytes  = 8 * 1024
	queueCapacity = 32
	authTimeout   = 10 * time.Second
	writeTimeout  = 5 * time.Second
	lookupTimeout = 3 * time.Second
	tokenIssuer   = "mangahub" // Must match internal/auth.
)

// Server is a newline-delimited JSON TCP push server. Clients authenticate
// with the same JWT that was issued by the HTTP login endpoint.
// A Server instance is also a library.ProgressNotifier.
type Server struct {
	db     *sql.DB
	secret []byte

	mu       sync.RWMutex
	listener net.Listener
	started  bool
	closed   bool
	clients  map[*session]struct{}
	byUser   map[int64]map[*session]struct{}
	wg       sync.WaitGroup
}

type session struct {
	conn   net.Conn
	userID int64
	out    chan []byte
	done   chan struct{}
	once   sync.Once
}

func (c *session) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

type inputFrame struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
}

type outputFrame struct {
	Type    string `json:"type"`
	UserID  int64  `json:"user_id,omitempty"`
	Error   string `json:"error,omitempty"`
	MangaID string `json:"manga_id,omitempty"`
	Chapter int32  `json:"chapter,omitempty"`
	Version int64  `json:"version,omitempty"`
}

// NewServer rejects a missing database or unsafe JWT signing configuration.
// Pass the same backend SQLite DB and JWT secret used by the auth handler.
func NewServer(db *sql.DB, jwtSecret string) (*Server, error) {
	if db == nil {
		return nil, errors.New("tcp: backend database is required")
	}
	if len(jwtSecret) < 32 || strings.HasPrefix(jwtSecret, "CHANGE_ME") {
		return nil, errors.New("tcp: JWT secret must be at least 32 characters and not a placeholder")
	}
	return &Server{
		db:      db,
		secret:  []byte(jwtSecret),
		clients: make(map[*session]struct{}),
		byUser:  make(map[int64]map[*session]struct{}),
	}, nil
}

// Serve accepts TCP clients until the listener is closed. Listen on the
// configured TCP_ADDR (default :9090) in cmd/backend. Shutdown is safe to call
// concurrently with Serve. This method blocks while the server is running.
func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("tcp: listener is required")
	}
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return errors.New("tcp: server is already started or closed")
	}
	s.started = true
	s.listener = listener
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.RLock()
			closed := s.closed
			s.mu.RUnlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Temporary() {
				continue
			}
			return fmt.Errorf("tcp: accept: %w", err)
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.handleConnection(conn)
		}()
	}
}

// Shutdown closes the listener and connected clients, then waits for active
// client goroutines to finish or the supplied context to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
	}
	listener := s.listener
	clients := make([]*session, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, c := range clients {
		c.close()
	}

	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PublishProgress implements library.ProgressNotifier. It is safe to call
// from concurrent HTTP handlers; a slow TCP client cannot block an HTTP
// request. Events are sent only to sessions with the matching verified user ID.
// An origin session is not identifiable from ProgressEvent, so it may also
// receive the notification; the TUI can de-duplicate by manga_id + version.
func (s *Server) PublishProgress(event library.ProgressEvent) {
	if event.UserID <= 0 || event.MangaID == "" || event.Version <= 0 || event.Chapter < 0 {
		return
	}
	data, err := json.Marshal(outputFrame{
		Type: "progress_updated", MangaID: event.MangaID,
		Chapter: event.Chapter, Version: event.Version,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	for c := range s.byUser[event.UserID] {
		select {
		case <-c.done:
		case c.out <- data:
		default:
			// Disconnect a congested client rather than blocking publishers.
			c.close()
		}
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024), maxLineBytes)

	if !scanner.Scan() {
		return
	}
	first, err := decodeFrame(scanner.Bytes())
	if err != nil || first.Type != "auth" || first.Token == "" {
		_ = writeFrame(conn, outputFrame{Type: "error", Error: "authentication required"})
		return
	}
	userID, err := s.validateToken(first.Token)
	if err != nil {
		_ = writeFrame(conn, outputFrame{Type: "error", Error: "invalid or expired token"})
		return
	}
	if err := writeFrame(conn, outputFrame{Type: "authenticated", UserID: userID}); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	c := &session{conn: conn, userID: userID, out: make(chan []byte, queueCapacity), done: make(chan struct{})}
	if !s.register(c) {
		return
	}
	defer s.unregister(c)

	writerFinished := make(chan struct{})
	go func() {
		defer close(writerFinished)
		c.writeLoop()
	}()
	defer func() {
		c.close()
		<-writerFinished
	}()

	for scanner.Scan() {
		frame, err := decodeFrame(scanner.Bytes())
		if err != nil {
			s.enqueue(c, outputFrame{Type: "error", Error: "invalid JSON frame"})
			return
		}
		switch frame.Type {
		case "ping":
			s.enqueue(c, outputFrame{Type: "pong"})
		default:
			s.enqueue(c, outputFrame{Type: "error", Error: "unsupported message type"})
			return
		}
	}
}

func (s *Server) register(c *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.clients[c] = struct{}{}
	if s.byUser[c.userID] == nil {
		s.byUser[c.userID] = make(map[*session]struct{})
	}
	s.byUser[c.userID][c] = struct{}{}
	return true
}

func (s *Server) unregister(c *session) {
	c.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, c)
	delete(s.byUser[c.userID], c)
	if len(s.byUser[c.userID]) == 0 {
		delete(s.byUser, c.userID)
	}
}

func (s *Server) enqueue(c *session, msg outputFrame) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	select {
	case <-c.done:
	case c.out <- data:
	default:
		c.close()
	}
}

func (c *session) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := c.conn.Write(msg); err != nil {
				c.close()
				return
			}
		}
	}
}

func writeFrame(conn net.Conn, frame outputFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = conn.Write(append(data, '\n'))
	return err
}

func decodeFrame(line []byte) (inputFrame, error) {
	var value inputFrame
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return inputFrame{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return inputFrame{}, errors.New("multiple JSON values")
	}
	return value, nil
}

// validateToken uses the same JWT issuer, algorithm, and subject convention
// as internal/auth. It checks the database to reject removed accounts.
func (s *Server) validateToken(raw string) (int64, error) {
	claims := new(jwt.RegisteredClaims)
	parsed, err := jwt.ParseWithClaims(raw, claims,
		func(_ *jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return 0, errors.New("invalid JWT")
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || userID <= 0 {
		return 0, errors.New("invalid user identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM users WHERE id = ?", userID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("user unavailable: %w", err)
	}
	return userID, nil
}
