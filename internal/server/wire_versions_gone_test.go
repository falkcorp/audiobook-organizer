// file: internal/server/wire_versions_gone_test.go
// version: 1.0.0
// guid: 9b1e4d27-3c60-4a85-8f12-d7a5c0e63b49
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

// retiredVersionAliasRoutes are the three /books/:id/versions/:vid... aliases
// retired by 01-P81k, with the permission each kept and the exact gone() body
// each answers. Each names its live /audiobooks/ twin.
var retiredVersionAliasRoutes = []struct {
	method, pattern, path, twin, perm, body string
}{
	{http.MethodDelete, "/api/v1/books/:id/versions/:vid", "/api/v1/books/synthetic-book/versions/synthetic-version",
		"/api/v1/audiobooks/:id/versions/:vid", auth.PermLibraryDelete,
		`{"error":"gone","message":"this endpoint has been removed; use DELETE /api/v1/audiobooks/:id/versions/:vid instead"}`},
	{http.MethodPost, "/api/v1/books/:id/versions/:vid/restore", "/api/v1/books/synthetic-book/versions/synthetic-version/restore",
		"/api/v1/audiobooks/:id/versions/:vid/restore", auth.PermLibraryOrganize,
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/audiobooks/:id/versions/:vid/restore instead"}`},
	{http.MethodPost, "/api/v1/books/:id/versions/:vid/purge-now", "/api/v1/books/synthetic-book/versions/synthetic-version/purge-now",
		"/api/v1/audiobooks/:id/versions/:vid/purge-now", auth.PermLibraryDelete,
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/audiobooks/:id/versions/:vid/purge-now instead"}`},
}

// TestRetiredVersionAliasRoutes_Answer410 drives each retired alias through
// the real router as an admin: 410 with the exact body naming the twin.
func TestRetiredVersionAliasRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredVersionAliasRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredVersionAliasRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns they are
// gone.
func TestRetiredVersionAliasRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredVersionAliasRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, httptest.NewRequest(r.method, r.path, nil))
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredVersionAliasRoutes_PermissionRunsBeforeGone: the stubs keep their
// s.perm guards. A viewer session is valid but holds neither library.delete
// nor library.organize, so it gets 403 and never learns the alias is gone. A
// session holding exactly the permission the route requires reaches the 410,
// which proves the guard is that permission and not a broader one; a session
// holding exactly one other permission is refused.
func TestRetiredVersionAliasRoutes_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)
	_, viewerTok := f.viewerGoneSession(t)
	onlyDelete := f.exactPermGoneSession(t, "only-delete", auth.PermLibraryDelete)
	onlyOrganize := f.exactPermGoneSession(t, "only-organize", auth.PermLibraryOrganize)

	for _, r := range retiredVersionAliasRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewerTok, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, "viewer: %s", w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)

			holds, lacks := onlyDelete, onlyOrganize
			if r.perm == auth.PermLibraryOrganize {
				holds, lacks = onlyOrganize, onlyDelete
			}
			w = f.do(t, holds, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, "exact permission: %s", w.Body.String())
			w = f.do(t, lacks, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, "other single permission: %s", w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredVersionAliasRoutes_TouchNothingAndTwinsStillWork: a retired alias
// leaves a real version untouched, and the live /audiobooks/ twins still run
// their handlers against the same seeded book and versions.
func TestRetiredVersionAliasRoutes_TouchNothingAndTwinsStillWork(t *testing.T) {
	f := setupCredGuardServer(t)

	book, err := f.store.CreateBook(&database.Book{Title: "Synthetic Version Book", FilePath: "/nonexistent/synthetic/book.m4b", Format: "m4b"})
	require.NoError(t, err)
	newVersion := func(status string) string {
		t.Helper()
		v, err := f.store.CreateBookVersion(&database.BookVersion{BookID: book.ID, Status: status, Format: "m4b", Source: "imported"})
		require.NoError(t, err)
		return v.ID
	}
	status := func(id string) string {
		t.Helper()
		v, err := f.store.GetBookVersion(id)
		require.NoError(t, err)
		require.NotNil(t, v)
		return v.Status
	}

	active, trashed := newVersion(database.BookVersionStatusActive), newVersion(database.BookVersionStatusTrash)

	// Retired aliases: 410, and the rows keep their status.
	w := f.do(t, f.sessionToken, http.MethodDelete, "/api/v1/books/"+book.ID+"/versions/"+active, nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusActive, status(active), "alias DELETE must not trash the version")
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/books/"+book.ID+"/versions/"+trashed+"/restore", nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusTrash, status(trashed), "alias restore must not restore the version")
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/books/"+book.ID+"/versions/"+trashed+"/purge-now", nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusTrash, status(trashed), "alias purge-now must not purge the version")

	// Live twins on the same rows: they really run (200, row changes).
	w = f.do(t, f.sessionToken, http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"/versions/"+active, nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusTrash, status(active))
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/versions/"+active+"/restore", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusAlt, status(active))
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/versions/"+trashed+"/purge-now", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusInactivePurged, status(trashed))

	// The sibling DELETE /purged-versions/:vid is unaffected.
	w = f.do(t, f.sessionToken, http.MethodDelete, "/api/v1/purged-versions/"+trashed, nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
