// file: internal/itunes/service/playlist_sync_test.go
// version: 2.0.0

package itunesservice

import (
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func TestMigrateSmartPlaylists_NilLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ps := newPlaylistSync(nil, nil)
	res := ps.MigrateSmartPlaylists(nil, PlaylistImportOptions{})
	imported, skipped := res.Imported, res.Skipped
	if imported != 0 || skipped != 0 {
		t.Errorf("nil library: imported=%d skipped=%d, want 0/0", imported, skipped)
	}
}

func TestMigrateSmartPlaylists_SkipsNonSmart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	lib := &itunes.ITLLibrary{
		Playlists: []itunes.ITLPlaylist{
			{
				Title:   "Not Smart",
				IsSmart: false,
			},
		},
	}

	ps := newPlaylistSync(store, nil)
	res := ps.MigrateSmartPlaylists(lib, PlaylistImportOptions{})
	imported, skipped := res.Imported, res.Skipped
	if imported != 0 || skipped != 0 {
		t.Errorf("non-smart: imported=%d skipped=%d, want 0/0", imported, skipped)
	}
}

func TestMigrateSmartPlaylists_SkipsAlreadyImported(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	var pid [8]byte
	pid[0] = 0xAA
	pidHex := "aa00000000000000"

	_, _ = store.CreateUserPlaylist(&database.UserPlaylist{
		Name:               "Already Imported",
		Type:               database.UserPlaylistTypeSmart,
		ITunesPersistentID: pidHex,
	})

	lib := &itunes.ITLLibrary{
		Playlists: []itunes.ITLPlaylist{
			{
				Title:         "Already Imported",
				IsSmart:       true,
				PersistentID:  pid,
				SmartCriteria: []byte{0x01, 0x02, 0x03, 0x04},
			},
		},
	}

	ps := newPlaylistSync(store, nil)
	res := ps.MigrateSmartPlaylists(lib, PlaylistImportOptions{})
	imported, skipped := res.Imported, res.Skipped
	if imported != 0 {
		t.Errorf("expected 0 imported (already exists), got %d", imported)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
}

func TestPushDirty_NoDirty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	pushed := newPlaylistSync(store, nil).PushDirty()
	if pushed != 0 {
		t.Errorf("expected 0 pushed with no dirty playlists, got %d", pushed)
	}
}
