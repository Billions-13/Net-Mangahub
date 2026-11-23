package database

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed manga_seed.json
var mangaSeedFile embed.FS

type seedManga struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Genres        []string `json:"genres"`
	Status        string   `json:"status"`
	TotalChapters int      `json:"total_chapters"`
	Description   string   `json:"description"`
}

// SeedCatalog inserts the bundled manga catalog only when the catalog is empty.
// It returns the number of records inserted. Repeated calls never overwrite data.
func SeedCatalog(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("catalog database is nil")
	}

	raw, err := mangaSeedFile.ReadFile("manga_seed.json")
	if err != nil {
		return 0, fmt.Errorf("read bundled manga seed: %w", err)
	}

	var mangas []seedManga
	if err := json.Unmarshal(raw, &mangas); err != nil {
		return 0, fmt.Errorf("decode manga seed: %w", err)
	}
	if len(mangas) < 30 {
		return 0, fmt.Errorf("manga seed must contain at least 30 entries")
	}

	seen := make(map[string]struct{}, len(mangas))
	genresSeen := make(map[string]struct{})
	for _, m := range mangas {
		if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Title) == "" ||
			strings.TrimSpace(m.Author) == "" || strings.TrimSpace(m.Description) == "" ||
			len(m.Genres) == 0 || m.TotalChapters < 0 {
			return 0, fmt.Errorf("invalid manga seed entry: %q", m.ID)
		}
		if m.Status != "ongoing" && m.Status != "completed" {
			return 0, fmt.Errorf("invalid status for manga %q", m.ID)
		}
		if _, exists := seen[m.ID]; exists {
			return 0, fmt.Errorf("duplicate manga seed ID: %q", m.ID)
		}
		seen[m.ID] = struct{}{}
		for _, genre := range m.Genres {
			genre = strings.TrimSpace(genre)
			if genre == "" {
				return 0, fmt.Errorf("empty genre for manga %q", m.ID)
			}
			genresSeen[genre] = struct{}{}
		}
	}
	if len(genresSeen) < 4 {
		return 0, fmt.Errorf("manga seed must cover at least 4 genres")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin catalog seed transaction: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM manga").Scan(&count); err != nil {
		return 0, fmt.Errorf("count catalog rows: %w", err)
	}
	if count > 0 {
		return 0, nil
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO manga
        (id, title, author, genres, status, total_chapters, description)
        VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare manga insert: %w", err)
	}
	defer stmt.Close()

	for _, m := range mangas {
		genresJSON, err := json.Marshal(m.Genres)
		if err != nil {
			return 0, fmt.Errorf("encode genres for %q: %w", m.ID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			m.ID, m.Title, m.Author, string(genresJSON),
			m.Status, m.TotalChapters, m.Description,
		); err != nil {
			return 0, fmt.Errorf("insert manga %q: %w", m.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit manga seed: %w", err)
	}
	return len(mangas), nil
}
