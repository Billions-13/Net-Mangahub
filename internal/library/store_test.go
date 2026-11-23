package library

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mangahub/internal/database"
)

func TestStoreLifecycleAndPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backend.db")
	db, err := database.OpenBackend(file)
	if err != nil {
		t.Fatal(err)
	}

	result, err := db.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, "bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	empty, err := store.List(ctx, alice)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty library: %v, %#v", err, empty)
	}

	added, err := store.Add(ctx, alice, "one-piece", "plan-to-read")
	if err != nil || added.Version != 1 || added.CurrentChapter != 0 {
		t.Fatalf("add: %#v, %v", added, err)
	}
	if _, err := store.Add(ctx, alice, "one-piece", "reading"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate must conflict, got %v", err)
	}
	if _, err := store.Add(ctx, alice, "naruto", "invalid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status must fail, got %v", err)
	}
	if _, err := store.Get(ctx, bob, "one-piece"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bob must not access Alice's library: %v", err)
	}

	v1 := int64(1)
	reading := "reading"
	updated, changed, err := store.UpdateProgress(ctx, alice, "one-piece", 25, &reading, &v1)
	if err != nil || !changed || updated.Version != 2 || updated.CurrentChapter != 25 || updated.Status != "reading" {
		t.Fatalf("update: %#v, changed=%v, err=%v", updated, changed, err)
	}
	if _, _, err := store.UpdateProgress(ctx, alice, "one-piece", 26, nil, &v1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
	v2 := int64(2)
	same, changed, err := store.UpdateProgress(ctx, alice, "one-piece", 25, &reading, &v2)
	if err != nil || changed || same.Version != 2 {
		t.Fatalf("no-op changed version: %#v, %v, %v", same, changed, err)
	}
	if _, _, err := store.UpdateProgress(ctx, bob, "one-piece", 2, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bob must not update Alice's entry: %v", err)
	}
	if _, _, err := store.UpdateProgress(ctx, alice, "one-piece", -1, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative chapter allowed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := database.OpenBackend(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store, err = NewStore(reopened)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Get(ctx, alice, "one-piece")
	if err != nil || persisted.CurrentChapter != 25 || persisted.Version != 2 {
		t.Fatalf("progress not persisted: %#v %v", persisted, err)
	}
}

func TestNewStoreRequiresDB(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("nil DB accepted")
	}
}
