// file: internal/deluge/inplace_unloaded_test.go
// version: 1.1.0
// guid: 3d0b6c52-7a41-4e98-8f15-c2e9a04b7d63
// last-edited: 2026-10-11
//
// An in-place import fails closed while the Deluge protected-path list has
// never loaded: the cache answers "not protected" for every seeding file then,
// so marking on that answer could mark a torrent's own file as the library
// copy. The organizer's rename guard refuses on the same condition.

package deluge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// flakyDeluge is a Deluge endpoint that is down (HTTP 503) until up is set,
// then lists one torrent seeding from savePath.
type flakyDeluge struct {
	up       atomic.Bool
	savePath string
}

func (f *flakyDeluge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.up.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var result any
	switch req.Method {
	case "auth.login":
		result = true
	case "core.get_torrents_status":
		result = map[string]any{"0123456789abcdef0123456789abcdef01234567": map[string]any{
			"hash": "0123456789abcdef0123456789abcdef01234567", "save_path": f.savePath,
		}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
}

// newUnloadedCache is a ProtectedPathCache over a configured Deluge client
// that cannot be reached: Loaded() is false until the returned endpoint's up
// flag is set.
func newUnloadedCache(t *testing.T, savePath string, extra []string) (*ProtectedPathCache, *flakyDeluge) {
	t.Helper()
	fd := &flakyDeluge{savePath: savePath}
	srv := httptest.NewServer(fd)
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	c := NewProtectedPathCache(client, extra)
	if c.Loaded() {
		t.Fatal("fixture: the cache loaded from a down Deluge")
	}
	return c, fd
}

func TestInPlaceProtection_UnloadedListFailsClosed(t *testing.T) {
	const dir = "/library/Example Author"
	const path = dir + "/01.mp3"
	cache, fd := newUnloadedCache(t, "/elsewhere", []string{"/music/iTunes"})

	t.Run("configured but never loaded is protected", func(t *testing.T) {
		if got := InPlaceProtection(&config.Config{}, cache, path); got != ProtectedListNotLoaded {
			t.Errorf("got %q, want %q", got, ProtectedListNotLoaded)
		}
	})
	t.Run("a static prefix is still named first", func(t *testing.T) {
		got := InPlaceProtection(&config.Config{ProtectedPaths: []string{dir}}, cache, path)
		if got == "" || got == ProtectedListNotLoaded {
			t.Errorf("got %q, want the static prefix named", got)
		}
	})
	t.Run("nil checker (Deluge not configured) is not blocked", func(t *testing.T) {
		if got := InPlaceProtection(&config.Config{}, nil, path); got != "" {
			t.Errorf("got %q, want none", got)
		}
		var unset *ProtectedPathCache
		if got := InPlaceProtection(&config.Config{}, unset, path); got != "" {
			t.Errorf("typed-nil cache: got %q, want none", got)
		}
	})
	t.Run("cache with no client is loaded and not blocked", func(t *testing.T) {
		if got := InPlaceProtection(&config.Config{}, NewProtectedPathCache(nil, nil), path); got != "" {
			t.Errorf("got %q, want none", got)
		}
	})
	t.Run("recovers once Deluge answers", func(t *testing.T) {
		fd.up.Store(true)
		if got := InPlaceProtection(&config.Config{}, cache, path); got != "" {
			t.Errorf("got %q after the list loaded, want none", got)
		}
		if got := InPlaceProtection(&config.Config{}, cache, "/elsewhere/a.mp3"); got == "" {
			t.Error("a loaded list must protect its save path")
		}
	})
}

func TestMarkImportedInPlace_UnloadedListLeftPending(t *testing.T) {
	const path = "/library/Example Author/Example Book/01.mp3"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cache, fd := newUnloadedCache(t, "/elsewhere", nil)
	row := database.BookFile{ID: "bf-1", BookID: "book-1", FilePath: path, DelugeHash: "abc"}
	s := &modifyOnlyStore{row: &row}

	// Not InPlaceProtected: Deluge being down is no verdict on the file, and
	// the importers report it as its own status.
	got, err := MarkImportedInPlace(s, &config.Config{}, cache, "book-1", "bf-1", path, now)
	if err != nil || got != InPlaceListNotLoaded {
		t.Fatalf("got %v, %v; want InPlaceListNotLoaded, nil", got, err)
	}
	if s.calls != 0 || s.wrote || s.row.ImportedFromDelugeAt != nil {
		t.Errorf("calls=%d wrote=%v imported_at=%v; want the row untouched", s.calls, s.wrote, s.row.ImportedFromDelugeAt)
	}

	// A static prefix needs no list: it is still named protected.
	staticCfg := &config.Config{ProtectedPaths: []string{"/library/Example Author"}}
	if got, err := MarkImportedInPlace(s, staticCfg, cache, "book-1", "bf-1", path, now); err != nil || got != InPlaceProtected {
		t.Errorf("static prefix with the list unloaded: got %v, %v; want InPlaceProtected", got, err)
	}

	// The next run, with Deluge back, marks it.
	fd.up.Store(true)
	if got, err := MarkImportedInPlace(s, &config.Config{}, cache, "book-1", "bf-1", path, now); err != nil || got != InPlaceMarked {
		t.Errorf("after the list loaded: got %v, %v; want InPlaceMarked", got, err)
	}
}

// ImportToLibraryWith's source-is-destination branch shares the helper: the
// row stays pending and the error says why, wrapping both
// tagger.ErrProtectedListNotLoaded and tagger.ErrProtectedPathWrite.
func TestImportToLibrary_SourceIsDestination_UnloadedListRefused(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Example Author", "book.m4b")
	writeFile(t, src)
	cache, fd := newUnloadedCache(t, "/elsewhere", nil)
	store := &fakeDelugStore{}
	bf := &database.BookFile{ID: "id-same", FilePath: src}

	newPath, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store, bf, cache)
	if !errors.Is(err, tagger.ErrProtectedListNotLoaded) || !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want one wrapping ErrProtectedListNotLoaded and ErrProtectedPathWrite", err)
	}
	if newPath != "" {
		t.Errorf("newPath = %q, want empty", newPath)
	}
	if store.updated != nil || bf.ImportedFromDelugeAt != nil {
		t.Errorf("row written while the list was unloaded: updated=%+v imported_at=%v", store.updated, bf.ImportedFromDelugeAt)
	}

	fd.up.Store(true)
	if newPath, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store, bf, cache); err != nil || newPath != src {
		t.Errorf("after the list loaded: got %q, %v; want %q, nil", newPath, err, src)
	}
}
