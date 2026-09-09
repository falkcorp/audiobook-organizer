// file: internal/server/quarantine_handlers_test.go
// version: 1.0.0
// guid: 7b2e5c14-9a30-4d68-bf27-c05e83a4d916
// last-edited: 2026-09-09

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// countFailStore is a full database.Store whose CountQuarantinedBooks always
// fails. Embedding the interface supplies the other 397 methods, so overriding
// one does not mean hand-writing a mock for the whole surface.
type countFailStore struct {
	database.Store
	err error
}

func (c countFailStore) CountQuarantinedBooks() (int, error) { return 0, c.err }

// seedQuarantinedBook creates a real file under the test library root, stores a
// book pointing at it, and quarantines it. The quarantine service physically
// moves the file into .failed/, so the file must actually exist on disk.
func seedQuarantinedBook(t *testing.T, srv *Server, store database.Store, title string) *database.Book {
	t.Helper()
	src := filepath.Join(config.AppConfig.RootDir, "Author", title, "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("fake audio"), 0o644))

	book, err := store.CreateBook(&database.Book{Title: title, FilePath: src, Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, srv.quarantineSvc.QuarantineBook(book.ID, "taglib failed"))
	return book
}

// quarantineListCtx runs listQuarantinedBooks against srv and returns the
// recorder, without needing the full router wired up.
func quarantineListCtx(srv *Server) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/quarantined", nil)
	srv.listQuarantinedBooks(c)
	return w
}

// TestListQuarantinedBooks_CountErrorIsNotSwallowed pins the fix: the handler
// used to write `total, _ :=` and return 200 with total=0 next to a non-empty
// Books array. Reverting to the discarded error makes this fail, because the
// response becomes 200 instead of 500.
func TestListQuarantinedBooks_CountErrorIsNotSwallowed(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	real := srv.storeForWiring()
	require.NotNil(t, real)

	// Seed one quarantined book so the list path succeeds and only the count
	// fails — that is the exact shape the old code turned into total=0.
	seedQuarantinedBook(t, srv, real, "QuarantinedBook")

	srv.store = countFailStore{Store: real, err: errors.New("iterator closed")}

	w := quarantineListCtx(srv)

	require.Equal(t, http.StatusInternalServerError, w.Code,
		"a failed count must not be reported as a successful total=0")
	require.NotContains(t, w.Body.String(), `"total":0`,
		"the incoherent total=0-with-books response must not be produced")
}

// TestListQuarantinedBooks_ReportsFullTotal is the happy path: total is the
// full quarantined count, not the length of the returned page.
func TestListQuarantinedBooks_ReportsFullTotal(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	real := srv.storeForWiring()
	require.NotNil(t, real)

	for _, title := range []string{"A", "B", "C"} {
		seedQuarantinedBook(t, srv, real, title)
	}

	w := quarantineListCtx(srv)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data struct {
			Books []database.Book `json:"books"`
			Total int             `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 3, body.Data.Total)
	require.Len(t, body.Data.Books, 3)
}
