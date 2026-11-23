package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mangahub/internal/database"
	"mangahub/internal/library"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "tcp-test-secret-at-least-32-characters-long"

type tcpFixture struct {
	t      *testing.T
	db     *sql.DB
	server *Server
	addr   string
}

func newFixture(t *testing.T) *tcpFixture {
	t.Helper()
	db, err := database.OpenBackend(filepath.Join(t.TempDir(), "backend.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := db.Exec(`INSERT INTO users(username, password_hash) VALUES (?, ?)`, name, "test-hash"); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewServer(db, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- s.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-finished; err != nil {
			t.Errorf("serve: %v", err)
		}
		_ = db.Close()
	})
	return &tcpFixture{t: t, db: db, server: s, addr: listener.Addr().String()}
}

func createToken(t *testing.T, userID int64, expires time.Time, secret string) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Issuer: "mangahub", Subject: fmt.Sprint(userID),
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(expires),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

type tcpClient struct {
	conn net.Conn
	read *bufio.Reader
}

func (f *tcpFixture) connect() *tcpClient {
	f.t.Helper()
	conn, err := net.DialTimeout("tcp", f.addr, time.Second)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	return &tcpClient{conn: conn, read: bufio.NewReader(conn)}
}

func (c *tcpClient) write(t *testing.T, data string) {
	t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.conn.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

func (c *tcpClient) frame(t *testing.T, timeout time.Duration) outputFrame {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := c.read.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read TCP frame: %v", err)
	}
	var out outputFrame
	if err := json.Unmarshal(line, &out); err != nil {
		t.Fatalf("decode TCP frame %q: %v", line, err)
	}
	return out
}

func (c *tcpClient) auth(t *testing.T, token string) {
	t.Helper()
	c.write(t, `{"type":"auth","token":"`+token+`"}`+"\n")
	frame := c.frame(t, time.Second)
	if frame.Type != "authenticated" || frame.UserID <= 0 {
		t.Fatalf("expected authentication acknowledgment, got %+v", frame)
	}
}

func TestTCPProgressIsolationAndFraming(t *testing.T) {
	f := newFixture(t)
	alice := createToken(t, 1, time.Now().Add(time.Hour), testSecret)
	bob := createToken(t, 2, time.Now().Add(time.Hour), testSecret)
	c1 := f.connect()
	c2 := f.connect()
	c3 := f.connect()

	// Test fragmented TCP frames: auth JSON can arrive in two writes.
	msg := `{"type":"auth","token":"` + alice + `"}` + "\n"
	c1.write(t, msg[:12])
	c1.write(t, msg[12:])
	if got := c1.frame(t, time.Second); got.Type != "authenticated" || got.UserID != 1 {
		t.Fatalf("fragmented auth frame: %+v", got)
	}
	c2.auth(t, alice)
	c3.auth(t, bob)

	// Test combined TCP messages in one write (one JSON value per line).
	c1.write(t, "{\"type\":\"ping\"}\n{\"type\":\"ping\"}\n")
	if got := c1.frame(t, time.Second); got.Type != "pong" {
		t.Fatalf("first ping: %+v", got)
	}
	if got := c1.frame(t, time.Second); got.Type != "pong" {
		t.Fatalf("second ping: %+v", got)
	}

	f.server.PublishProgress(library.ProgressEvent{UserID: 1, MangaID: "one-piece", Chapter: 25, Version: 2})
	for _, client := range []*tcpClient{c1, c2} {
		got := client.frame(t, time.Second)
		if got.Type != "progress_updated" || got.MangaID != "one-piece" || got.Chapter != 25 || got.Version != 2 {
			t.Fatalf("unexpected Alice notification: %+v", got)
		}
	}
	_ = c3.conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err := c3.read.ReadBytes('\n'); err == nil {
		t.Fatal("Bob received Alice's private progress event")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("Bob connection unexpectedly closed: %v", err)
	}

	// A disconnected client must not prevent notifications to the others.
	_ = c2.conn.Close()
	f.server.PublishProgress(library.ProgressEvent{UserID: 1, MangaID: "naruto", Chapter: 9, Version: 3})
	if got := c1.frame(t, time.Second); got.MangaID != "naruto" || got.Version != 3 {
		t.Fatalf("notification after disconnect: %+v", got)
	}
}

func TestTCPRejectsInvalidAuthentication(t *testing.T) {
	f := newFixture(t)
	for name, first := range map[string]string{
		"missing auth": `{"type":"ping"}` + "\n",
		"invalid JWT":  `{"type":"auth","token":"broken"}` + "\n",
		"wrong key":    `{"type":"auth","token":"` + createToken(t, 1, time.Now().Add(time.Hour), "incorrect-secret-key-123456789123456789") + `"}` + "\n",
		"expired":      `{"type":"auth","token":"` + createToken(t, 1, time.Now().Add(-time.Hour), testSecret) + `"}` + "\n",
		"unknown user": `{"type":"auth","token":"` + createToken(t, 99, time.Now().Add(time.Hour), testSecret) + `"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := f.connect()
			c.write(t, first)
			got := c.frame(t, time.Second)
			if got.Type != "error" {
				t.Fatalf("expected error frame, got %+v", got)
			}
		})
	}
}

func TestTCPConstructorAndConcurrentPublish(t *testing.T) {
	if _, err := NewServer(nil, testSecret); err == nil {
		t.Fatal("nil database accepted")
	}
	f := newFixture(t)
	if _, err := NewServer(f.db, "short"); err == nil {
		t.Fatal("short signing secret accepted")
	}
	client := f.connect()
	client.auth(t, createToken(t, 1, time.Now().Add(time.Hour), testSecret))
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			f.server.PublishProgress(library.ProgressEvent{UserID: 1, MangaID: "one-piece", Chapter: int32(n), Version: int64(n + 1)})
		}(i)
	}
	group.Wait()
	for i := 0; i < 12; i++ {
		if got := client.frame(t, time.Second); got.Type != "progress_updated" {
			t.Fatalf("missing concurrent event: %+v", got)
		}
	}
}

func TestTCPFrameValidation(t *testing.T) {
	f := newFixture(t)
	client := f.connect()
	client.auth(t, createToken(t, 1, time.Now().Add(time.Hour), testSecret))
	client.write(t, `{"type":"made-up"}`+"\n")
	if got := client.frame(t, time.Second); got.Type != "error" {
		t.Fatalf("unexpected response to unsupported frame: %+v", got)
	}
	other := f.connect()
	other.write(t, `{"type":"auth","token":"`+strings.Repeat("x", maxLineBytes+50)+`"}`+"\n")
	_ = other.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := other.read.ReadBytes('\n'); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}
