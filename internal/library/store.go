package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotFound = errors.New("library entry not found")
	ErrConflict = errors.New("library entry already exists or was updated elsewhere")
	ErrInvalid  = errors.New("invalid library entry")
)

// Entry is stored in backend SQLite. User identity is determined by a verified JWT.
type Entry struct {
	MangaID        string `json:"manga_id"`
	Status         string `json:"status"`
	CurrentChapter int32  `json:"current_chapter"`
	Version        int64  `json:"version"`
	UpdatedAt      string `json:"updated_at"`
}

// ProgressEvent is emitted only AFTER a committed progress update.
// The TCP server can implement ProgressNotifier without modifying this package.
type ProgressEvent struct {
	UserID  int64  `json:"-"`
	MangaID string `json:"manga_id"`
	Chapter int32  `json:"chapter"`
	Version int64  `json:"version"`
}

type ProgressNotifier interface {
	PublishProgress(ProgressEvent)
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("backend SQLite database is required")
	}
	return &Store{db: db}, nil
}

func ValidStatus(value string) bool {
	switch value {
	case "plan-to-read", "reading", "completed":
		return true
	default:
		return false
	}
}

func validIdentity(userID int64, mangaID string) bool {
	return userID > 0 && strings.TrimSpace(mangaID) == mangaID && mangaID != "" && len(mangaID) <= 128
}

const entryColumns = `manga_id, status, current_chapter, version, updated_at`

// List returns only the authenticated user's own library rows.
func (s *Store) List(ctx context.Context, userID int64) ([]Entry, error) {
	if userID <= 0 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+entryColumns+`
        FROM user_library WHERE user_id = ?
        ORDER BY updated_at DESC, manga_id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list library: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.MangaID, &entry.Status, &entry.CurrentChapter, &entry.Version, &entry.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan library entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library: %w", err)
	}
	return entries, nil
}

// Get never allows one user to access another user's library entry.
func (s *Store) Get(ctx context.Context, userID int64, mangaID string) (Entry, error) {
	if !validIdentity(userID, mangaID) {
		return Entry{}, ErrInvalid
	}
	return readEntry(ctx, s.db, userID, mangaID)
}

type entryQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readEntry(ctx context.Context, q entryQuery, userID int64, mangaID string) (Entry, error) {
	var entry Entry
	err := q.QueryRowContext(ctx, `SELECT `+entryColumns+`
        FROM user_library WHERE user_id = ? AND manga_id = ?`, userID, mangaID).
		Scan(&entry.MangaID, &entry.Status, &entry.CurrentChapter, &entry.Version, &entry.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("read library entry: %w", err)
	}
	return entry, nil
}

// Add creates a unique (user_id, manga_id) row, initially at chapter zero.
func (s *Store) Add(ctx context.Context, userID int64, mangaID, status string) (Entry, error) {
	if !validIdentity(userID, mangaID) || !ValidStatus(status) {
		return Entry{}, ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_library
        (user_id, manga_id, status, current_chapter, version)
        VALUES (?, ?, ?, 0, 1)`, userID, mangaID, status)
	if err != nil {
		// SQLite primary-key uniqueness is the normal duplicate case.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "primary key constraint") {
			return Entry{}, ErrConflict
		}
		return Entry{}, fmt.Errorf("add library entry: %w", err)
	}
	return s.Get(ctx, userID, mangaID)
}

// UpdateProgress updates chapter and optionally status in one transaction.
// expectedVersion is optional optimistic concurrency protection. An unchanged
// request is a no-op: it neither increments version nor publishes a TCP event.
// changed is true only if the update committed successfully.
func (s *Store) UpdateProgress(
	ctx context.Context,
	userID int64,
	mangaID string,
	chapter int32,
	nextStatus *string,
	expectedVersion *int64,
) (entry Entry, changed bool, err error) {
	if !validIdentity(userID, mangaID) || chapter < 0 ||
		(nextStatus != nil && !ValidStatus(*nextStatus)) ||
		(expectedVersion != nil && *expectedVersion < 1) {
		return Entry{}, false, ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, false, fmt.Errorf("begin progress transaction: %w", err)
	}
	defer tx.Rollback()

	previous, err := readEntry(ctx, tx, userID, mangaID)
	if err != nil {
		return Entry{}, false, err
	}
	if expectedVersion != nil && previous.Version != *expectedVersion {
		return Entry{}, false, ErrConflict
	}
	status := previous.Status
	if nextStatus != nil {
		status = *nextStatus
	}
	if chapter == previous.CurrentChapter && status == previous.Status {
		return previous, false, nil
	}

	result, err := tx.ExecContext(ctx, `UPDATE user_library
        SET current_chapter = ?, status = ?, version = version + 1,
            updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
        WHERE user_id = ? AND manga_id = ? AND version = ?`,
		chapter, status, userID, mangaID, previous.Version)
	if err != nil {
		return Entry{}, false, fmt.Errorf("write progress: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Entry{}, false, fmt.Errorf("inspect progress update: %w", err)
	}
	if rows != 1 {
		return Entry{}, false, ErrConflict
	}
	updated, err := readEntry(ctx, tx, userID, mangaID)
	if err != nil {
		return Entry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, false, fmt.Errorf("commit progress: %w", err)
	}
	return updated, true, nil
}
