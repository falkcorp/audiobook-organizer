// file: internal/itunes/service/playlist_sync_test.go
// version: 2.1.0
// guid: 502086aa-eba7-403d-96e5-b82be1dbaecb
// last-edited: 2026-09-13

package itunesservice

import (
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/gin-gonic/gin"
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

// slstSingleRuleBlob builds a synthetic Smart Criteria blob in the layout
// measured in ITUNES-SMARTCRIT-PARSE (big-endian, "SLst" magic, operand at
// off with byte-length at off-4, field at off-56, operator word at off-52).
func slstSingleRuleBlob(field, op uint32, operand string) []byte {
	b := make([]byte, 136+56)
	copy(b, "SLst")
	binary.BigEndian.PutUint32(b[136:], field)
	binary.BigEndian.PutUint32(b[140:], op)
	binary.BigEndian.PutUint32(b[136+52:], uint32(len(operand)*2))
	for _, r := range operand {
		b = binary.BigEndian.AppendUint16(b, uint16(r))
	}
	return append(b, 0, 0, 0, 0)
}

// A correctly decoded blob is still not translatable while the format is only
// partially mapped: the dry run must report an EMPTY query with the reason,
// never the old match-all "*", so the maintenance op's apply guard fires.
func TestMigrateSmartPlaylists_UntranslatableCriteriaYieldEmptyQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	lib := &itunes.ITLLibrary{
		Playlists: []itunes.ITLPlaylist{
			{Title: "Synthetic Saga", IsSmart: true, PersistentID: [8]byte{0xB1},
				SmartCriteria: slstSingleRuleBlob(3, 0x01000002, "Example Saga")},
			{Title: "Not SLst", IsSmart: true, PersistentID: [8]byte{0xB2},
				SmartCriteria: []byte{0x01, 0x02, 0x03, 0x04, 0x05}},
		},
	}

	res := newPlaylistSync(store, nil).MigrateSmartPlaylists(lib, PlaylistImportOptions{DryRun: true})
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	got := res.Items[0]
	if got.Status != "would-import" || got.Query != "" {
		t.Errorf("SLst item = %+v, want would-import with empty query", got)
	}
	if !strings.Contains(got.Err, "completeness unverified") {
		t.Errorf("SLst item err = %q, want the unresolved reason", got.Err)
	}
	if bad := res.Items[1]; bad.Status != "unparseable" {
		t.Errorf("non-SLst item = %+v, want unparseable", bad)
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
