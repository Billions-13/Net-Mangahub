package database

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSeedCatalogAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := OpenCatalog(path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	inserted, err := SeedCatalog(context.Background(), db)
	if err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	if inserted < 30 {
		t.Fatalf("expected at least 30 manga, got %d", inserted)
	}

	rows, err := db.Query(`SELECT id, title, author, genres, status,
        total_chapters, description FROM manga`)
	if err != nil {
		t.Fatalf("query manga: %v", err)
	}
	distinctGenres := make(map[string]bool)
	count := 0
	for rows.Next() {
		var id, title, author, genresRaw, status, description string
		var chapters int
		if err := rows.Scan(&id, &title, &author, &genresRaw,
			&status, &chapters, &description); err != nil {
			t.Fatalf("read manga: %v", err)
		}
		if id == "" || title == "" || author == "" ||
			description == "" || chapters < 0 ||
			(status != "ongoing" && status != "completed") {
			t.Fatalf("invalid manga record: %q", id)
		}
		var genres []string
		if err := json.Unmarshal([]byte(genresRaw), &genres); err != nil {
			t.Fatalf("invalid genres for %q: %v", id, err)
		}
		for _, genre := range genres {
			distinctGenres[genre] = true
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate manga: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close manga rows: %v", err)
	}
	if count != inserted || len(distinctGenres) < 4 {
		t.Fatalf("want %d manga and 4+ genres, got %d and %d",
			inserted, count, len(distinctGenres))
	}

	if _, err := db.Exec(`UPDATE manga SET total_chapters = ? WHERE id = ?`,
		1200, "one-piece"); err != nil {
		t.Fatalf("update test chapter count: %v", err)
	}
	secondInsert, err := SeedCatalog(context.Background(), db)
	if err != nil || secondInsert != 0 {
		t.Fatalf("second seed should insert 0, got %d: %v", secondInsert, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close catalog: %v", err)
	}

	db, err = OpenCatalog(path)
	if err != nil {
		t.Fatalf("reopen catalog: %v", err)
	}
	defer db.Close()

	var chapters int
	if err := db.QueryRow(`SELECT total_chapters FROM manga WHERE id = ?`,
		"one-piece").Scan(&chapters); err != nil {
		t.Fatalf("read persisted manga: %v", err)
	}
	if chapters != 1200 {
		t.Fatalf("seed overwrote edited manga; got %d chapters", chapters)
	}
}

func TestSeedCatalogDoesNotModifyNonEmptyCatalog(t *testing.T) {
	db, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO manga
        (id, title, author, genres, status, total_chapters, description)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"custom-manga", "Custom Manga", "Student", `["Comedy"]`,
		"ongoing", 1, "User-created catalog item")
	if err != nil {
		t.Fatal(err)
	}

	inserted, err := SeedCatalog(context.Background(), db)
	if err != nil || inserted != 0 {
		t.Fatalf("seed should skip non-empty catalog: %d: %v", inserted, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM manga").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("existing catalog changed: count=%d", count)
	}
}
