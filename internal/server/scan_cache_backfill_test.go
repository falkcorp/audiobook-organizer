// file: internal/server/scan_cache_backfill_test.go
// version: 1.0.0
// guid: 5c2a9f81-73de-4b06-a1c8-6e9d0b34f2a7
// last-edited: 2026-08-25

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newScanCacheBackfillServer builds a server over a real store holding ONE
// single-file book that owns no book_file row -- the exact population the
// backfill exists to repair, and the one the scan never creates a row for.
func newScanCacheBackfillServer(t *testing.T) (*Server, *database.PebbleStore, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	require.NoError(t, err)
	orig := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(orig)
		store.Close()
	})

	dir := t.TempDir()
	audio := filepath.Join(dir, "solo.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio"), 0o600))

	mtime, size := int64(4242), int64(5)
	book, err := store.CreateBook(&database.Book{
		Title: "Solo", FilePath: audio, LastScanMtime: &mtime, LastScanSize: &size,
	})
	require.NoError(t, err)

	return NewServer(store), store, audio, book.ID
}

func postBackfill(t *testing.T, srv *Server, query string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/backfill-scan-cache"+query, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusNotFound, w.Code,
		"the route is not registered; the backfill would be unreachable and the "+
			"per-file scan cache could never be seeded before a deploy")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response was not JSON (code %d): %s", w.Code, w.Body.String())
	}
	return body
}

// TestBackfillScanCacheDefaultsToDryRun pins the safety property: an accidental
// POST must PREVIEW, never write.
//
// This endpoint creates book_file rows and stamps scan caches across the whole
// library, so the difference between "previewed" and "applied" is the difference
// between a no-op and tens of thousands of writes. Defaulting to dry-run is the
// only thing standing between a mistyped curl and that, which makes the default
// itself worth a test rather than a convention.
//
// It asserts on the STORE, not on the response body: a handler that reported
// dry_run=true while writing anyway would satisfy any assertion made against its
// own JSON.
func TestBackfillScanCacheDefaultsToDryRun(t *testing.T) {
	srv, store, audio, bookID := newScanCacheBackfillServer(t)

	body := postBackfill(t, srv, "") // no dry_run parameter at all
	data, _ := body["data"].(map[string]any)
	require.NotNil(t, data, "unexpected response shape: %v", body)
	require.Equal(t, true, data["dry_run"], "an unqualified POST must default to a dry run")

	// The store must be untouched.
	m, err := store.GetScanCacheMap()
	require.NoError(t, err)
	require.NotContains(t, m, audio,
		"the default POST STAMPED the scan cache; a dry run that writes is not a dry run")

	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Empty(t, files,
		"the default POST CREATED a book_file row; an accidental request must preview, not migrate")
}

// TestBackfillScanCacheAppliesOnlyWhenAskedExplicitly is the other half: the
// endpoint must actually do the work when opted into, or the dry-run default
// above would be trivially satisfiable by an endpoint that never works at all.
func TestBackfillScanCacheAppliesOnlyWhenAskedExplicitly(t *testing.T) {
	srv, store, audio, bookID := newScanCacheBackfillServer(t)

	body := postBackfill(t, srv, "?dry_run=false")
	data, _ := body["data"].(map[string]any)
	require.NotNil(t, data, "unexpected response shape: %v", body)
	require.Equal(t, false, data["dry_run"])

	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 1,
		"the single-file book still owns no book_file row, so the file-keyed scan cache "+
			"cannot see it and it would be re-read on every scan forever")

	m, err := store.GetScanCacheMap()
	require.NoError(t, err)
	require.Contains(t, m, audio, "the created row carries no scan stamp")
}
