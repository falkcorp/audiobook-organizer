// file: internal/server/tombstone_sweep_guard_wiring_test.go
// version: 1.0.0
// guid: e17b3a60-2c95-4d48-9f06-b84d1c7a5e23
// last-edited: 2026-10-30

//go:build !windows

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestWireHandlers_TombstoneSweepGuardIsInstalled: POST /operations/sweep-tombstones
// through the real router unlinks a clean, stamped tombstone's file only because
// the server gave the operations handler the book purge's guard. Without the
// wiring the handler refuses every removal (fail closed), so the file removed
// here is the oracle that the wiring is there, and the unstamped tombstone's file
// kept is the oracle that the guard is not a rubber stamp.
func TestWireHandlers_TombstoneSweepGuardIsInstalled(t *testing.T) {
	srv, _, admin := repairsPermServer(t, true, true)
	store, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "the test server runs on a PebbleStore")
	store.WaitForWarmup()

	lib := config.AppConfig.RootDir
	write := func(rel string) string {
		p := filepath.Join(lib, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("audio"), 0o644))
		return p
	}
	// The purge only believes the library is mounted when a live book's file is there.
	witness := write("Gotham/witness.m4b")
	_, err := store.CreateBook(&database.Book{ID: "witness", Title: "Batman: Year One", FilePath: witness})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "witness-f", BookID: "witness", FilePath: witness, Format: "m4b"}))

	clean := write("Gotham/the-killing-joke.m4b")
	info, err := os.Stat(clean)
	require.NoError(t, err)
	unstamped := write("Gotham/arkham-asylum.m4b")
	st, isStat := info.Sys().(*syscall.Stat_t)
	require.True(t, isStat, "unix stat result")
	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t-clean", FilePath: clean},
		database.TombstoneFileStamp{HasInode: true, Ino: uint64(st.Ino), Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}))
	require.NoError(t, store.CreateBookTombstone(&database.Book{ID: "t-unstamped", FilePath: unstamped}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/sweep-tombstones", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data struct {
			TombstonesCleaned int      `json:"tombstones_cleaned"`
			RefusedFiles      int      `json:"refused_files"`
			Refusals          []string `json:"refusals"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	assert.Equal(t, 1, body.Data.TombstonesCleaned, "body: %s", w.Body.String())
	assert.Equal(t, 1, body.Data.RefusedFiles, "body: %s", w.Body.String())
	assert.Len(t, body.Data.Refusals, 1)
	assert.NoFileExists(t, clean, "the stamped, unreferenced file inside the library is removed")
	assert.FileExists(t, unstamped, "a tombstone with no stamp never costs its file")
}
