// file: internal/server/wire_itunes_gone_test.go
// version: 1.1.0
// guid: 1c5e8a03-d92b-4f67-a4e0-6b3d7f18c295
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const pidRepairGoneBody = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`

// TestRetiredPIDRepairRoute_Answer410 drives POST /itunes/pid-repair through
// the real router as an admin: 410 with the shared gone() body, whatever the
// body or query (dry_run included), and nothing is written.
func TestRetiredPIDRepairRoute_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)

	// Two rows share one PID: the state the retired handler would have cleared.
	b1, err := f.store.CreateBook(&database.Book{Title: "Synthetic Pid Book One", FilePath: "/nonexistent/synthetic/one"})
	require.NoError(t, err)
	b2, err := f.store.CreateBook(&database.Book{Title: "Synthetic Pid Book Two", FilePath: "/nonexistent/synthetic/two"})
	require.NoError(t, err)
	const pid = "DEADBEEF00000002"
	f1 := &database.BookFile{BookID: b1.ID, FilePath: "/nonexistent/synthetic/track.m4b", ITunesPersistentID: pid}
	require.NoError(t, f.store.CreateBookFile(f1))
	f2 := &database.BookFile{BookID: b2.ID, FilePath: "/nonexistent/synthetic/track.m4b"}
	require.NoError(t, f.store.CreateBookFile(f2))
	f2.ITunesPersistentID = pid
	require.NoError(t, f.store.UpdateBookFile(f2.ID, f2))

	for _, path := range []string{"/api/v1/itunes/pid-repair", "/api/v1/itunes/pid-repair?dry_run=true"} {
		t.Run(path, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodPost, path, map[string]any{"dry_run": false})
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, pidRepairGoneBody, w.Body.String())
		})
	}

	for _, bf := range []*database.BookFile{f1, f2} {
		got, err := f.store.GetBookFileByID(bf.BookID, bf.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, pid, got.ITunesPersistentID, "the stub must not clear a PID")
	}
}

// TestRetiredPIDRepairRoute_AuthRunsFirst: no credentials, no 410.
func TestRetiredPIDRepairRoute_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/itunes/pid-repair", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}

// TestRetiredPIDRepairRoute_PermissionRunsBeforeGone: the stub keeps
// library.edit_metadata. A viewer session gets 403 and never learns the route
// is gone; a custom role holding exactly library.edit_metadata reaches the 410,
// and one holding exactly library.delete is refused.
func TestRetiredPIDRepairRoute_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)
	_, viewerTok := f.viewerGoneSession(t)
	onlyEdit := f.exactPermGoneSession(t, "only-edit", auth.PermLibraryEditMetadata)
	onlyDelete := f.exactPermGoneSession(t, "only-delete", auth.PermLibraryDelete)

	w := f.do(t, viewerTok, http.MethodPost, "/api/v1/itunes/pid-repair", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)

	w = f.do(t, onlyDelete, http.MethodPost, "/api/v1/itunes/pid-repair", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)

	w = f.do(t, onlyEdit, http.MethodPost, "/api/v1/itunes/pid-repair", nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, pidRepairGoneBody, w.Body.String())
}

// TestRetiredPIDRepairRoute_KeptNeighboursNotGone: the owner-kept read-only
// diagnostics (D73) are still registered and run their handlers; neither answers
// 410, and pid-integrity answers 200 with its census report.
func TestRetiredPIDRepairRoute_KeptNeighboursNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, k := range []struct{ method, path, answers string }{
		{http.MethodGet, "/api/v1/itunes/pid-integrity", `"report"`},
		{http.MethodGet, "/api/v1/itunes/library-stats", ""},
	} {
		t.Run(k.method+" "+k.path, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.path], "route not registered")
			w := f.do(t, f.sessionToken, k.method, k.path, nil)
			assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
			if k.answers != "" {
				// Not just "not 410": the handler itself answered 200 with its payload.
				assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.Contains(t, w.Body.String(), k.answers)
			}
		})
	}
}
