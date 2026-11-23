package database

import (
	"path/filepath"
	"testing"
)

func TestBackendDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.db")

	db, err := OpenBackend(path)
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}

	result, err := db.Exec(
		`INSERT INTO users (username, password_hash)
		 VALUES (?, ?)`,
		"alice",
		"hashed-password",
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(
		`INSERT INTO user_library
		 (user_id, manga_id, status, current_chapter)
		 VALUES (?, ?, ?, ?)`,
		userID,
		"one-piece",
		"reading",
		10,
	)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}

	// Duplicate library entries must be rejected.
	_, err = db.Exec(
		`INSERT INTO user_library (user_id, manga_id)
		 VALUES (?, ?)`,
		userID,
		"one-piece",
	)
	if err == nil {
		t.Fatal("duplicate library entry was accepted")
	}

	// Invalid reading status must be rejected.
	_, err = db.Exec(
		`INSERT INTO user_library
		 (user_id, manga_id, status)
		 VALUES (?, ?, ?)`,
		userID,
		"naruto",
		"invalid",
	)
	if err == nil {
		t.Fatal("invalid status was accepted")
	}

	// A library entry must belong to an existing user.
	_, err = db.Exec(
		`INSERT INTO user_library (user_id, manga_id)
		 VALUES (?, ?)`,
		999999,
		"bleach",
	)
	if err == nil {
		t.Fatal("invalid user reference was accepted")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify persistence after reopening the database.
	db, err = OpenBackend(path)
	if err != nil {
		t.Fatalf("reopen backend: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow(
		`SELECT COUNT(*) FROM user_library
		 WHERE user_id = ? AND manga_id = ?`,
		userID,
		"one-piece",
	).Scan(&count)

	if err != nil || count != 1 {
		t.Fatalf("library data was not persisted: %v", err)
	}
}

func TestCatalogDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")

	db, err := OpenCatalog(path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO manga
		 (id, title, author, genres, status,
		  total_chapters, description)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"one-piece",
		"One Piece",
		"Eiichiro Oda",
		`["Action","Adventure"]`,
		"ongoing",
		100,
		"Pirate adventure manga.",
	)
	if err != nil {
		t.Fatalf("insert manga: %v", err)
	}

	// Duplicate manga IDs must be rejected.
	_, err = db.Exec(
		`INSERT INTO manga
		 (id, title, author, genres, status,
		  total_chapters, description)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"one-piece",
		"Duplicate",
		"Test Author",
		`["Action"]`,
		"ongoing",
		1,
		"Duplicate test.",
	)
	if err == nil {
		t.Fatal("duplicate manga ID was accepted")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenCatalog(path)
	if err != nil {
		t.Fatalf("reopen catalog: %v", err)
	}
	defer db.Close()

	var title string
	err = db.QueryRow(
		"SELECT title FROM manga WHERE id = ?",
		"one-piece",
	).Scan(&title)

	if err != nil {
		t.Fatalf("query manga: %v", err)
	}

	if title != "One Piece" {
		t.Fatalf("unexpected manga title: %s", title)
	}

	// Catalog must not contain backend-only tables.
	var count int
	err = db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE type = 'table' AND name = 'users'`,
	).Scan(&count)

	if err != nil || count != 0 {
		t.Fatal("catalog contains backend user table")
	}
}
