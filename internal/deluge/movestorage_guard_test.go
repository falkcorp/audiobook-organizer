// file: internal/deluge/movestorage_guard_test.go
// version: 1.1.0
// guid: 9e3a7c15-4d28-4b6f-8a01-c5f2d7b30e64
// last-edited: 2026-10-10
//
// D57 regression: with deluge_move_enabled=false (the copy model), nothing
// that holds a torrent hash may send core.move_storage to Deluge.

package deluge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const guardTestHash = "aabbccddeeff00112233445566778899aabbccdd"

type guardBookStore struct {
	versions []database.BookVersion
	book     *database.Book
}

func (g *guardBookStore) GetBookVersionsByBookID(string) ([]database.BookVersion, error) {
	return g.versions, nil
}
func (g *guardBookStore) GetBookByID(string) (*database.Book, error) { return g.book, nil }

// newGuardClient wires a fake Deluge that counts core.move_storage requests
// and installs it as the global client.
func newGuardClient(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()
	var moves atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "core.move_storage" {
			moves.Add(1)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "deluge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetGlobalClientForTest(client))
	return client, &moves
}

func setMoveEnabled(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = on
	t.Cleanup(func() { config.AppConfig.DelugeMoveEnabled = prev })
}

func TestMoveStorageGuard_DisabledSendsNothing(t *testing.T) {
	client, moves := newGuardClient(t)
	setMoveEnabled(t, false)

	active := database.BookVersion{ID: "v1", BookID: "b1", Status: database.BookVersionStatusActive, TorrentHash: guardTestHash}
	alt := database.BookVersion{ID: "v2", BookID: "b1", Status: database.BookVersionStatusAlt, TorrentHash: guardTestHash}
	st := &guardBookStore{
		versions: []database.BookVersion{active},
		book:     &database.Book{ID: "b1", FilePath: "/library/example/book.m4b"},
	}

	NotifyDelugeAfterOrganize(st, "b1", "/library/example/book.m4b")
	NotifyDelugeAfterVersionSwap(st, &alt, &active, "/library/example/book.m4b")

	// ImportToLibraryWith with a DelugeHash row and a live client.
	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootDir: t.TempDir(), DelugeMoveEnabled: false}
	bf := &database.BookFile{ID: "id-guard", FilePath: src, DelugeHash: guardTestHash}
	if _, err := ImportToLibraryWith(cfg, client, &fakeDelugStore{}, bf, ImportOptions{}); err != nil {
		t.Fatalf("import failed: %v", err)
	}

	if n := moves.Load(); n != 0 {
		t.Fatalf("core.move_storage sent %d time(s) while deluge_move_enabled=false", n)
	}
}

// Positive control: the same fake does see a move when the flag is on, so the
// zero above cannot be the fake failing to count.
func TestMoveStorageGuard_EnabledControlSeesTheMove(t *testing.T) {
	_, moves := newGuardClient(t)
	setMoveEnabled(t, true)

	st := &guardBookStore{versions: []database.BookVersion{
		{ID: "v1", BookID: "b1", Status: database.BookVersionStatusActive, TorrentHash: guardTestHash},
	}}
	NotifyDelugeAfterOrganize(st, "b1", "/library/example/book.m4b")

	if n := moves.Load(); n != 1 {
		t.Fatalf("control: want 1 move_storage with the flag on, got %d", n)
	}
}

// Positive control for the ImportToLibraryWith leg: with the flag on, the same
// call shape as the disabled test above does send exactly one move, so its
// zero is the guard holding and not the copy path never reaching the client.
func TestMoveStorageGuard_EnabledControlImportSeesTheMove(t *testing.T) {
	client, moves := newGuardClient(t)
	setMoveEnabled(t, true)

	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootDir: t.TempDir(), DelugeMoveEnabled: true}
	bf := &database.BookFile{ID: "id-guard", FilePath: src, DelugeHash: guardTestHash}
	if _, err := ImportToLibraryWith(cfg, client, &fakeDelugStore{}, bf, ImportOptions{}); err != nil {
		t.Fatalf("import failed: %v", err)
	}

	if n := moves.Load(); n != 1 {
		t.Fatalf("control: want 1 move_storage from ImportToLibraryWith with the flag on, got %d", n)
	}
}
