// file: internal/server/naming_audit_books_alias_test.go
// version: 1.0.0
// guid: 7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-09-25

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// TestDeprecatedBooksAliases_AllNineRoutesAnswer is the naming-audit
// 2026-09-25 class-1 regression test: it drives every one of the nine
// /books/:id/... routes moved under /audiobooks/:id/... through the real
// router (srv.router), not by calling a handler function directly, and
// asserts each old path AND its new canonical twin both answer 200 with a
// real fixture (not just "not 404", which the version handlers also return
// for a missing version and would prove nothing).
func TestDeprecatedBooksAliases_AllNineRoutesAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)

	if _, err := store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/tmp/b1", Format: "m4b",
	}); err != nil {
		t.Fatalf("create book: %v", err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		ID: "s1", BookID: "b1", FilePath: "/tmp/s1", TrackNumber: 1, Duration: 600,
	}); err != nil {
		t.Fatalf("create book file: %v", err)
	}

	// One fresh version per (old-path, new-path) pair so trash/restore/purge
	// calls never race each other's state.
	newVersion := func(status string) string {
		t.Helper()
		ver, err := store.CreateBookVersion(&database.BookVersion{
			BookID: "b1", Status: status, Format: "m4b", Source: "imported",
		})
		if err != nil {
			t.Fatalf("create version: %v", err)
		}
		return ver.ID
	}

	call := func(method, path, body string) int {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, r)
		if w.Code < 200 || w.Code >= 300 {
			t.Errorf("%s %s: got %d, want 2xx: %s", method, path, w.Code, w.Body.String())
		}
		return w.Code
	}

	// 1. POST position
	call(http.MethodPost, "/api/v1/books/b1/position", `{"segment_id":"s1","position_seconds":100}`)
	call(http.MethodPost, "/api/v1/audiobooks/b1/position", `{"segment_id":"s1","position_seconds":200}`)

	// 2. GET position
	call(http.MethodGet, "/api/v1/books/b1/position", "")
	call(http.MethodGet, "/api/v1/audiobooks/b1/position", "")

	// 3. GET state
	call(http.MethodGet, "/api/v1/books/b1/state", "")
	call(http.MethodGet, "/api/v1/audiobooks/b1/state", "")

	// 4. PATCH status
	call(http.MethodPatch, "/api/v1/books/b1/status", `{"status":"in_progress"}`)
	call(http.MethodPatch, "/api/v1/audiobooks/b1/status", `{"status":"in_progress"}`)

	// 5. DELETE status
	call(http.MethodDelete, "/api/v1/books/b1/status", "")
	call(http.MethodDelete, "/api/v1/audiobooks/b1/status", "")

	// 6. POST status/repair (dry run — default apply=false)
	call(http.MethodPost, "/api/v1/books/b1/status/repair", "")
	call(http.MethodPost, "/api/v1/audiobooks/b1/status/repair", "")

	// 7. DELETE versions/:vid (trash)
	call(http.MethodDelete, "/api/v1/books/b1/versions/"+newVersion(database.BookVersionStatusActive), "")
	call(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+newVersion(database.BookVersionStatusActive), "")

	// 8. POST versions/:vid/restore (version must start in trash)
	call(http.MethodPost, "/api/v1/books/b1/versions/"+newVersion(database.BookVersionStatusTrash)+"/restore", "")
	call(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+newVersion(database.BookVersionStatusTrash)+"/restore", "")

	// 9. POST versions/:vid/purge-now
	call(http.MethodPost, "/api/v1/books/b1/versions/"+newVersion(database.BookVersionStatusActive)+"/purge-now", "")
	call(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+newVersion(database.BookVersionStatusActive)+"/purge-now", "")
}
