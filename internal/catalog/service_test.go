package catalog

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"mangahub/internal/database"
	"mangahub/proto/mangapb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestCatalogGRPC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := database.OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inserted, err := database.SeedCatalog(ctx, db)
	if err != nil || inserted < 30 {
		t.Fatalf("seed catalog: %d inserted, error: %v", inserted, err)
	}

	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	mangapb.RegisterCatalogServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := mangapb.NewCatalogServiceClient(conn)

	found, err := client.SearchManga(ctx, &mangapb.SearchMangaRequest{Query: "One Piece"})
	if err != nil || len(found.GetManga()) == 0 {
		t.Fatalf("search One Piece: %v", err)
	}
	if found.GetManga()[0].GetId() != "one-piece" {
		t.Fatalf("unexpected search result: %s", found.GetManga()[0].GetId())
	}

	empty, err := client.SearchManga(ctx, &mangapb.SearchMangaRequest{Query: "no-such-manga-xyz"})
	if err != nil || len(empty.GetManga()) != 0 {
		t.Fatalf("expected empty search results: %v", err)
	}

	get, err := client.GetManga(ctx, &mangapb.GetMangaRequest{Id: "one-piece"})
	if err != nil || get.GetManga() == nil || len(get.GetManga().GetGenres()) == 0 {
		t.Fatalf("get manga details: %v", err)
	}
	newTotal := get.GetManga().GetTotalChapters() + 1
	updated, err := client.UpdateChapters(ctx, &mangapb.UpdateChaptersRequest{
		Id: "one-piece", TotalChapters: newTotal,
	})
	if err != nil || updated.GetManga().GetTotalChapters() != newTotal {
		t.Fatalf("update chapters: %v", err)
	}

	persisted, err := client.GetManga(ctx, &mangapb.GetMangaRequest{Id: "one-piece"})
	if err != nil || persisted.GetManga().GetTotalChapters() != newTotal {
		t.Fatalf("chapter update did not persist: %v", err)
	}

	assertCode := func(err error, want codes.Code) {
		t.Helper()
		if status.Code(err) != want {
			t.Fatalf("want gRPC code %v, got %v: %v", want, status.Code(err), err)
		}
	}
	_, err = client.GetManga(ctx, &mangapb.GetMangaRequest{Id: "unknown-id"})
	assertCode(err, codes.NotFound)
	_, err = client.GetManga(ctx, &mangapb.GetMangaRequest{Id: ""})
	assertCode(err, codes.InvalidArgument)
	_, err = client.UpdateChapters(ctx, &mangapb.UpdateChaptersRequest{Id: "one-piece", TotalChapters: -1})
	assertCode(err, codes.InvalidArgument)
	_, err = client.UpdateChapters(ctx, &mangapb.UpdateChaptersRequest{Id: "one-piece", TotalChapters: newTotal - 1})
	assertCode(err, codes.FailedPrecondition)
}
