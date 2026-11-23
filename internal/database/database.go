package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const backendSchema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (
		strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	),
	CHECK (length(trim(username)) > 0),
	CHECK (length(password_hash) > 0)
);

CREATE TABLE IF NOT EXISTS user_library (
	user_id INTEGER NOT NULL,
	manga_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'plan-to-read',
	current_chapter INTEGER NOT NULL DEFAULT 0,
	version INTEGER NOT NULL DEFAULT 1,
	updated_at TEXT NOT NULL DEFAULT (
		strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	),

	PRIMARY KEY (user_id, manga_id),

	FOREIGN KEY (user_id)
		REFERENCES users(id)
		ON DELETE CASCADE,

	CHECK (
		status IN (
			'plan-to-read',
			'reading',
			'completed'
		)
	),
	CHECK (current_chapter >= 0),
	CHECK (version >= 1),
	CHECK (length(trim(manga_id)) > 0)
);

CREATE INDEX IF NOT EXISTS idx_library_user_status
ON user_library(user_id, status);
`

const catalogSchema = `
CREATE TABLE IF NOT EXISTS manga (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	author TEXT NOT NULL,
	genres TEXT NOT NULL,
	status TEXT NOT NULL,
	total_chapters INTEGER NOT NULL DEFAULT 0,
	description TEXT NOT NULL,

	CHECK (length(trim(id)) > 0),
	CHECK (length(trim(title)) > 0),
	CHECK (length(trim(author)) > 0),
	CHECK (length(trim(genres)) > 0),
	CHECK (length(trim(status)) > 0),
	CHECK (total_chapters >= 0)
);

CREATE INDEX IF NOT EXISTS idx_manga_title
ON manga(title COLLATE NOCASE);

CREATE INDEX IF NOT EXISTS idx_manga_status
ON manga(status);
`

// OpenBackend opens the backend database and creates its tables.
func OpenBackend(path string) (*sql.DB, error) {
	return openDatabase(path, backendSchema)
}

// OpenCatalog opens the catalog database and creates its tables.
func OpenCatalog(path string) (*sql.DB, error) {
	return openDatabase(path, catalogSchema)
}

// openDatabase initializes a persistent SQLite database.
func openDatabase(path, schema string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path cannot be empty")
	}

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf(
			"create database directory: %w",
			err,
		)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}

	// Keep SQLite connection settings consistent.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}

	settings := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
	}

	for _, statement := range settings {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf(
				"configure SQLite: %w",
				err,
			)
		}
	}

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf(
			"initialize database schema: %w",
			err,
		)
	}

	return db, nil
}
