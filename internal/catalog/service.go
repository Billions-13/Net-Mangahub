package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"mangahub/proto/mangapb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const mangaFields = "id, title, author, genres, status, total_chapters, description"

// Service implements the CatalogService gRPC contract using catalog SQLite.
type Service struct {
	mangapb.UnimplementedCatalogServiceServer
	db *sql.DB
}

func NewService(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, errors.New("catalog database cannot be nil")
	}
	return &Service{db: db}, nil
}

func (s *Service) SearchManga(ctx context.Context, req *mangapb.SearchMangaRequest) (*mangapb.SearchMangaResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	query := strings.TrimSpace(req.GetQuery())
	if len(query) > 100 {
		return nil, status.Error(codes.InvalidArgument, "query is too long")
	}

	// Escape LIKE wildcard characters so searches match literal user input.
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
SELECT `+mangaFields+` FROM manga
WHERE title LIKE ? ESCAPE '\'
   OR author LIKE ? ESCAPE '\'
   OR genres LIKE ? ESCAPE '\'
   OR id LIKE ? ESCAPE '\'
ORDER BY title COLLATE NOCASE
LIMIT 100`, pattern, pattern, pattern, pattern)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()

	result := &mangapb.SearchMangaResponse{Manga: make([]*mangapb.Manga, 0)}
	for rows.Next() {
		m, err := scanManga(rows)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "invalid catalog record: %v", err)
		}
		result.Manga = append(result.Manga, m)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}

func (s *Service) GetManga(ctx context.Context, req *mangapb.GetMangaRequest) (*mangapb.GetMangaResponse, error) {
	if req == nil || strings.TrimSpace(req.GetId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "manga ID is required")
	}
	id := strings.TrimSpace(req.GetId())
	m, err := scanManga(s.db.QueryRowContext(ctx,
		"SELECT "+mangaFields+" FROM manga WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "manga not found")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return &mangapb.GetMangaResponse{Manga: m}, nil
}

func (s *Service) UpdateChapters(ctx context.Context, req *mangapb.UpdateChaptersRequest) (*mangapb.UpdateChaptersResponse, error) {
	if req == nil || strings.TrimSpace(req.GetId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "manga ID is required")
	}
	if req.GetTotalChapters() < 0 {
		return nil, status.Error(codes.InvalidArgument, "total chapters cannot be negative")
	}
	id := strings.TrimSpace(req.GetId())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback()

	m, err := scanManga(tx.QueryRowContext(ctx,
		"SELECT "+mangaFields+" FROM manga WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "manga not found")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if req.GetTotalChapters() < m.GetTotalChapters() {
		return nil, status.Error(codes.FailedPrecondition, "total chapters cannot decrease")
	}

	if req.GetTotalChapters() != m.GetTotalChapters() {
		if _, err := tx.ExecContext(ctx,
			"UPDATE manga SET total_chapters = ? WHERE id = ?",
			req.GetTotalChapters(), id); err != nil {
			return nil, databaseError(ctx, err)
		}
		m.TotalChapters = req.GetTotalChapters()
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &mangapb.UpdateChaptersResponse{Manga: m}, nil
}

// scanManga converts a catalog SQLite record into the protobuf message.
func scanManga(row interface{ Scan(...any) error }) (*mangapb.Manga, error) {
	m := &mangapb.Manga{}
	var rawGenres string
	var chapters int64
	if err := row.Scan(&m.Id, &m.Title, &m.Author, &rawGenres,
		&m.Status, &chapters, &m.Description); err != nil {
		return nil, err
	}
	if chapters < 0 || chapters > math.MaxInt32 {
		return nil, fmt.Errorf("invalid chapter count for %s", m.Id)
	}
	m.TotalChapters = int32(chapters)
	if err := json.Unmarshal([]byte(rawGenres), &m.Genres); err != nil {
		return nil, fmt.Errorf("decode genres for %s: %w", m.Id, err)
	}
	return m, nil
}

func databaseError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "catalog operation timed out")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return status.Error(codes.Canceled, "catalog request canceled")
	}
	return status.Errorf(codes.Unavailable, "catalog database unavailable: %v", err)
}
