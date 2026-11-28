// file: internal/books/identity_abs_alias_boot_test.go
// version: 0.1.0
// guid: 1b7f4d92-8e3a-4c65-9d20-c5a6e81f3b47
// last-edited: 2026-11-22

package books

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
)

// The remove-from-continue-listening aliases on the flipped build (F4-08 B10z).
// HEAD registers GET and POST /api/me/item/:id/remove-from-continue-listening
// (handlers/abs/handler.go) beside GET /api/me/item/listening-sessions/:id, two
// patterns core's router folds into one generalised pattern
// (httpapi.foldComponent). A request for an unknown item gets the handler's
// own plain-text "Not Found", which reads like an unregistered route, so these
// cells use a real book: each alias answers 200 {} and hides the book from
// Continue Listening, the sibling keeps answering, and a path nobody
// registered is still a 404.

// absAs is one request to the ABS surface as the Access person behind assertion.
func absAs(t *testing.T, r *runningApp, method, path, assertion string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.Header.Set(cfaccess.CFAccessHeader, assertion)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestABSAlias_RemoveFromContinueListeningAnswersAsHEADOnTheFlip(t *testing.T) {
	r := bootAccess(t, newFakeAccessVerifier(), nil)
	code, alfredID, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, body)
	require.NotEmpty(t, alfredID)

	ids := map[string]string{} // sync id -> book id
	newBook := func(title string) string {
		b, err := r.store.CreateBook(&database.Book{Title: title, FilePath: filepath.Join(t.TempDir(), title+".m4b")})
		require.NoError(t, err)
		// An ABS client names a book by its sync id (lookupItemRef); listing a
		// library mints one, here minted directly.
		syncID, err := r.store.MintOrGetSyncID(b.ID)
		require.NoError(t, err)
		ids[syncID] = b.ID
		return syncID
	}
	hidden := func(syncID string) bool {
		st, err := r.store.GetUserBookState(alfredID, ids[syncID])
		require.NoError(t, err)
		return st != nil && st.HideFromContinueListening
	}

	for i, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/item/%s/remove-from-continue-listening"},
		{http.MethodPost, "/api/me/item/%s/remove-from-continue-listening"},
		{http.MethodGet, "/api/me/progress/%s/remove-from-continue-listening"},
		{http.MethodPost, "/api/me/progress/%s/remove-from-continue-listening"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			id := newBook(fmt.Sprintf("Hush part %d", i+1))
			require.False(t, hidden(id))
			rec := absAs(t, r, tc.method, fmt.Sprintf(tc.path, id), accessAlfred)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.JSONEq(t, `{}`, rec.Body.String(), "HEAD answers a non-empty JSON object")
			require.True(t, hidden(id), "the alias did not hide the book from Continue Listening")
		})
	}

	t.Run("the sibling GET /api/me/item/listening-sessions/:id still answers", func(t *testing.T) {
		id := newBook("The Long Halloween")
		rec := absAs(t, r, http.MethodGet, "/api/me/item/listening-sessions/"+id, accessAlfred)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"sessions"`)
	})

	t.Run("a path nobody registered under the folded pattern is still not served", func(t *testing.T) {
		id := newBook("Dark Victory")
		rec := absAs(t, r, http.MethodGet, "/api/me/item/"+id+"/batcave-unregistered", accessAlfred)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		require.False(t, hidden(id))
	})
}
