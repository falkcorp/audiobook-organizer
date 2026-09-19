// file: internal/deluge/import_durability_unknown_test.go
// version: 1.0.0
// guid: c43b21d1-60e1-474a-8095-186a3ee936b2
// last-edited: 2026-09-19

package deluge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// durUnknownDelugStore applies the repoint (records it) and reports that its
// fsync failed.
type durUnknownDelugStore struct{ fakeDelugStore }

func (f *durUnknownDelugStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.updated = file
	return fmt.Errorf("%w: injected fsync failure", database.ErrBookFileDurabilityUnknown)
}

// If the repoint's durability is unknown, Deluge must NOT be told to move the
// torrent's storage: were the WAL record lost, the row would revert to the
// source path — which Deluge would already have moved away.
func TestImportToLibrary_DurabilityUnknownSkipsMoveStorage(t *testing.T) {
	var moves atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "core.move_storage" {
			moves.Add(1)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
	}))
	defer srv.Close()
	client, err := New(srv.URL, "deluge")
	if err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg := &config.Config{RootDir: root, DelugeMoveEnabled: true}
	store := &durUnknownDelugStore{}
	bf := &database.BookFile{ID: "id-du", FilePath: src, DelugeHash: "abc123"}

	newPath, err := ImportToLibrary(cfg, client, store, bf, nil)
	if err != nil {
		t.Fatalf("an applied repoint must not fail the import: %v", err)
	}
	if newPath == "" || bf.FilePath != newPath {
		t.Fatalf("row not left repointed: newPath=%q FilePath=%q", newPath, bf.FilePath)
	}
	if n := moves.Load(); n != 0 {
		t.Fatalf("MoveStorage called %d time(s) for a repoint of unknown durability", n)
	}
}
