// file: internal/database/readonly_close_test.go
// version: 1.0.1
// guid: 0d7c4e19-6a2b-4f83-9e51-b8a3c62f1d47
// last-edited: 2026-11-09

package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestReadOnlyBooksStoreClosesClean (R1-25: the CLI's read-only offline
// open, diagnostics query and dedup-bench): a books store over a read-only
// core open reads, refuses a write with storage.ErrReadOnly, and closes with
// no error. Its shutdown WAL barrier is refused like every write, and a
// handle that committed nothing has nothing to make durable, so the close
// must not report the refusal as a failed sync.
func TestReadOnlyBooksStoreClosesClean(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "wayne-manor.pebble")
	s, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAuthor("Alfred Pennyworth"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	build, err := standaloneBuildID()
	if err != nil {
		t.Fatal(err)
	}
	// A read-only open runs no migration step; the observer fails the test
	// if one ever does.
	opened, err := storage.Open(ctx, storage.OpenOptions{
		Path: dir, ReadOnly: true, MustExist: true, Build: build,
		Owners: []storage.OwnerSpec{{Owner: BooksOwner, Enabled: true,
			Spec: BooksSpec(func() error { t.Error("a read-only open ran a books migration step"); return nil })}},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := opened.Offline()
	if err != nil {
		t.Fatal(errors.Join(err, opened.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("core DB close: %v", err)
		}
	})
	lr, ok := db.Legacy(BooksOwner)
	if !ok {
		t.Fatal("no legacy range for books")
	}
	ro, err := NewBooksStore(lr)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := ro.GetAuthorByName("Alfred Pennyworth"); err != nil || a == nil {
		t.Fatalf("read through the read-only store: %v, %v", a, err)
	}
	if _, err := ro.CreateAuthor("Jason Todd"); !errors.Is(err, storage.ErrReadOnly) {
		t.Fatalf("a write through the read-only store = %v, want storage.ErrReadOnly", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("closing a read-only books store: %v", err)
	}
}
