// file: internal/server/handlers/admindebug/handler_test.go
// version: 1.0.0
// guid: 553f618b-e6c3-4b54-ad05-30f3d39136b9
// last-edited: 2026-09-25

package admindebug

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeActivity struct {
	mu      sync.Mutex
	entries []database.ActivityEntry
}

func (f *fakeActivity) Record(e database.ActivityEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeActivity) all() []database.ActivityEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]database.ActivityEntry(nil), f.entries...)
}

type fixture struct {
	store    *database.PebbleStore
	activity *fakeActivity
	handler  *Handler
	book     *database.Book
	known    *database.BookFile // duration 1000
	broken   *database.BookFile // duration 0, really 44962
}

// newFixture builds a real Pebble store (memdb warmed) with one book of two
// files. FileSize is 0 on both files so the store's millisecond-duration
// heuristic (which needs a size to fire) leaves the durations alone.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()

	book, err := store.CreateBook(&database.Book{Title: "Long Book", FilePath: "/srv/library/Author/Long Book"})
	require.NoError(t, err)
	known := &database.BookFile{BookID: book.ID, FilePath: "/srv/library/Author/Long Book/01.m4b", Duration: 1000}
	broken := &database.BookFile{BookID: book.ID, FilePath: "/srv/library/Author/Long Book/02.m4b", Duration: 0}
	require.NoError(t, store.CreateBookFile(known))
	require.NoError(t, store.CreateBookFile(broken))
	require.NoError(t, store.RecomputeBookAggregates(book.ID))

	act := &fakeActivity{}
	return &fixture{store: store, activity: act, handler: New(store, act), book: book, known: known, broken: broken}
}

func (fx *fixture) router(user *database.User) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Set("auth_user", user) // what the auth middleware stores for CurrentUser
		}
		c.Next()
	})
	fx.handler.Register(r.Group("/api/v1"))
	return r
}

var admin = &database.User{ID: "u-admin", Username: "owner", Roles: []string{"admin"}}

func do(t *testing.T, r *gin.Engine, method, url, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func (fx *fixture) fileDuration(t *testing.T, f *database.BookFile) int {
	t.Helper()
	got, err := fx.store.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	return got.Duration
}

func (fx *fixture) bookDuration(t *testing.T) int {
	t.Helper()
	b, err := fx.store.GetBookByID(fx.book.ID)
	require.NoError(t, err)
	require.NotNil(t, b.Duration)
	return *b.Duration
}

func (fx *fixture) editCount(t *testing.T) int64 {
	t.Helper()
	n, err := fx.store.CountPrefix(editKeyPrefix)
	require.NoError(t, err)
	return n
}

func TestPatchBookFile_PreviewWritesNothing(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	before, err := fx.store.GetBookFileByID(fx.book.ID, fx.broken.ID)
	require.NoError(t, err)
	bookBefore := fx.bookDuration(t)

	w, out := do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+fx.broken.ID, `{"duration":44962}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, true, out["dry_run"])
	require.Equal(t, fx.book.ID, out["book_id"])
	diff := out["diff"].([]any)
	require.Len(t, diff, 1)
	d := diff[0].(map[string]any)
	require.Equal(t, "duration", d["field"])
	require.EqualValues(t, 0, d["before"])
	require.EqualValues(t, 44962, d["after"])
	require.Nil(t, out["edit_id"])

	after, err := fx.store.GetBookFileByID(fx.book.ID, fx.broken.ID)
	require.NoError(t, err)
	require.Equal(t, 0, after.Duration, "preview wrote the row")
	require.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "preview touched updated_at")
	require.Equal(t, bookBefore, fx.bookDuration(t), "preview changed the book aggregate")
	require.Zero(t, fx.editCount(t), "preview saved an undo image")
	require.Empty(t, fx.activity.all(), "preview wrote an audit row")
}

func TestPatchBookFile_ApplyRecomputesBookAndAudits(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	require.Equal(t, 1000, fx.bookDuration(t))

	w, out := do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+fx.broken.ID+"?apply=true", `{"duration":44962}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, false, out["dry_run"])
	editID, _ := out["edit_id"].(string)
	require.NotEmpty(t, editID)
	require.Nil(t, out["not_applied"])

	require.Equal(t, 44962, fx.fileDuration(t, fx.broken))
	require.Equal(t, 45962, fx.bookDuration(t), "book aggregate did not follow the file edit")
	require.EqualValues(t, 1, fx.editCount(t))

	entries := fx.activity.all()
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "audit", e.Tier)
	require.Equal(t, "admin_debug_edit", e.Type)
	require.Equal(t, fx.book.ID, e.BookID)
	require.Equal(t, editID, e.Details["edit_id"])
	require.Equal(t, "owner (u-admin)", e.Details["actor"])
	require.Equal(t, entityBookFile, e.Details["entity_type"])
	require.Equal(t, fx.broken.ID, e.Details["entity_id"])
	changes := e.Details["changes"].([]diffEntry)
	require.Len(t, changes, 1)
	require.JSONEq(t, `0`, string(changes[0].Before))
	require.JSONEq(t, `44962`, string(changes[0].After))

	w, out = do(t, r, http.MethodGet, "/api/v1/admin/debug/edits?limit=10", "")
	require.Equal(t, http.StatusOK, w.Code)
	edits := out["edits"].([]any)
	require.Len(t, edits, 1)
	require.Equal(t, editID, edits[0].(map[string]any)["edit_id"])
}

func TestUndo_RestoresAndRefusesTwice(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	_, out := do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+fx.broken.ID+"?apply=true", `{"duration":44962}`)
	editID := out["edit_id"].(string)

	w, _ := do(t, r, http.MethodPost, "/api/v1/admin/debug/edits/"+editID+"/undo", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 0, fx.fileDuration(t, fx.broken))
	require.Equal(t, 1000, fx.bookDuration(t))
	entries := fx.activity.all()
	require.Len(t, entries, 2)
	require.Equal(t, "admin_debug_undo", entries[1].Type)

	w, _ = do(t, r, http.MethodPost, "/api/v1/admin/debug/edits/"+editID+"/undo", "")
	require.Equal(t, http.StatusConflict, w.Code, "a second undo of the same edit must be refused")
}

func TestUndo_RefusedAfterLaterChangeUnlessForced(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	_, out := do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+fx.broken.ID+"?apply=true", `{"duration":44962}`)
	editID := out["edit_id"].(string)

	// Someone else changes the same field afterwards.
	_, err := fx.store.ModifyBookFile(fx.book.ID, fx.broken.ID, func(f *database.BookFile) error {
		f.Duration = 50000
		return nil
	})
	require.NoError(t, err)

	w, out := do(t, r, http.MethodPost, "/api/v1/admin/debug/edits/"+editID+"/undo", "")
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Equal(t, 50000, fx.fileDuration(t, fx.broken), "a refused undo wrote the row")
	require.Len(t, fx.activity.all(), 1, "a refused undo wrote an audit row")

	w, out = do(t, r, http.MethodPost, "/api/v1/admin/debug/edits/"+editID+"/undo?force=true", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, true, out["forced"])
	require.Equal(t, 0, fx.fileDuration(t, fx.broken))
}

func TestPatch_RejectsUnknownAndIdentityFields(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	for _, tc := range []struct{ url, body string }{
		{"/api/v1/admin/debug/book-files/" + fx.broken.ID, `{"no_such_field":1}`},
		{"/api/v1/admin/debug/book-files/" + fx.broken.ID, `{"id":"other"}`},
		{"/api/v1/admin/debug/book-files/" + fx.broken.ID, `{"book_id":"other"}`},
		{"/api/v1/admin/debug/book-files/" + fx.broken.ID, `{"duration":"long"}`},
		{"/api/v1/admin/debug/book-files/" + fx.broken.ID, `{"itunes_path":"W:/x.m4b"}`},
		{"/api/v1/admin/debug/books/" + fx.book.ID, `{"id":"other"}`},
		{"/api/v1/admin/debug/books/" + fx.book.ID, `{"authors":[]}`},
	} {
		w, _ := do(t, r, http.MethodPatch, tc.url+"?apply=true", tc.body)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s %s: %s", tc.url, tc.body, w.Body.String())
	}
	require.Equal(t, 0, fx.fileDuration(t, fx.broken))
	require.Zero(t, fx.editCount(t))
}

func TestRoutes_RequireAdmin(t *testing.T) {
	fx := newFixture(t)
	user := &database.User{ID: "u-2", Username: "reader", Roles: []string{"user"}}
	r := fx.router(user)
	for _, rt := range []struct{ method, url, body string }{
		{http.MethodGet, "/api/v1/admin/debug/books/" + fx.book.ID, ""},
		{http.MethodGet, "/api/v1/admin/debug/book-files/" + fx.broken.ID, ""},
		{http.MethodGet, "/api/v1/admin/debug/edits", ""},
		{http.MethodPatch, "/api/v1/admin/debug/book-files/" + fx.broken.ID + "?apply=true", `{"duration":44962}`},
		{http.MethodPost, "/api/v1/admin/debug/edits/x/undo", ""},
	} {
		w, _ := do(t, r, rt.method, rt.url, rt.body)
		require.Equal(t, http.StatusForbidden, w.Code, "%s %s", rt.method, rt.url)
	}
	w, _ := do(t, fx.router(nil), http.MethodGet, "/api/v1/admin/debug/books/"+fx.book.ID, "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, 0, fx.fileDuration(t, fx.broken))
}

func TestITunesGuard(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)
	it := &database.BookFile{BookID: fx.book.ID, FilePath: "/srv/books/itunes/Author/Book/01.m4b", Duration: 0}
	require.NoError(t, fx.store.CreateBookFile(it))

	// A non-path field of a row under the frozen iTunes tree is editable.
	w, _ := do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+it.ID+"?apply=true", `{"duration":3600}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 3600, fx.fileDuration(t, it))

	// Moving that row's path out of the tree is refused, preview and apply.
	for _, q := range []string{"", "?apply=true"} {
		w, _ = do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+it.ID+q, `{"file_path":"/srv/library/x.m4b"}`)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	}
	// Pointing an ordinary row into the tree is refused.
	w, _ = do(t, r, http.MethodPatch, "/api/v1/admin/debug/book-files/"+fx.broken.ID+"?apply=true", `{"file_path":"/srv/books/itunes/x.m4b"}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	w, _ = do(t, r, http.MethodPatch, "/api/v1/admin/debug/books/"+fx.book.ID+"?apply=true", `{"file_path":"/srv/books/itunes/Author/Book"}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	got, err := fx.store.GetBookFileByID(fx.book.ID, it.ID)
	require.NoError(t, err)
	require.Equal(t, "/srv/books/itunes/Author/Book/01.m4b", got.FilePath)
	require.EqualValues(t, 1, fx.editCount(t), "only the duration edit may have been saved")
}

func TestLookups(t *testing.T) {
	fx := newFixture(t)
	r := fx.router(admin)

	w, out := do(t, r, http.MethodGet, "/api/v1/admin/debug/book-files/"+fx.broken.ID, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, fx.book.ID, out["book_id"])

	w, _ = do(t, r, http.MethodGet, "/api/v1/admin/debug/book-files/no-such-file", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	w, out = do(t, r, http.MethodGet, "/api/v1/admin/debug/books/"+fx.book.ID, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 2, out["book_file_count"])

	// Before the book_atpath index is built the book half is withheld, not
	// answered from a library scan; the book_file half still answers.
	w, out = do(t, r, http.MethodGet, "/api/v1/admin/debug/lookup?path="+url.QueryEscape(fx.book.FilePath), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Nil(t, out["book_ids"])
	require.NotEmpty(t, out["book_ids_unavailable"])
	_, err := fx.store.BackfillBookAtPathIndex(context.Background())
	require.NoError(t, err)

	w, out = do(t, r, http.MethodGet, "/api/v1/admin/debug/lookup?path="+url.QueryEscape(fx.broken.FilePath), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	files := out["book_files"].([]any)
	require.Len(t, files, 1)
	require.Equal(t, fx.broken.ID, files[0].(map[string]any)["id"])

	w, out = do(t, r, http.MethodGet, "/api/v1/admin/debug/lookup?path="+url.QueryEscape(fx.book.FilePath), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []any{fx.book.ID}, out["book_ids"])
}
