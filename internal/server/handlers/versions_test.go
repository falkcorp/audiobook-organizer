// file: internal/server/handlers/versions_test.go
// version: 1.0.0
// guid: 3a9f6d21-7c84-4e0b-bd35-9f12a7c6e840
// last-edited: 2026-06-03

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newVersionsCtx builds a gin context with the given path params and an
// optional JSON request body.
func newVersionsCtx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = params
	return c, w
}

func strptr(s string) *string { return &s }

// ── ListAudiobookVersions ─────────────────────────────────────────────────

func TestVersionsHandler_ListAudiobookVersions_NoGroup(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodGet, "/audiobooks/b1/versions", "", gin.Params{{Key: "id", Value: "b1"}})
	h.ListAudiobookVersions(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_ListAudiobookVersions_WithGroup(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBooksByVersionGroup("g1").Return([]database.Book{{ID: "b1"}, {ID: "b2"}}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodGet, "/audiobooks/b1/versions", "", gin.Params{{Key: "id", Value: "b1"}})
	h.ListAudiobookVersions(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_ListAudiobookVersions_NotFound(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("missing").Return(nil, assert.AnError)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodGet, "/audiobooks/missing/versions", "", gin.Params{{Key: "id", Value: "missing"}})
	h.ListAudiobookVersions(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── LinkAudiobookVersion ──────────────────────────────────────────────────

func TestVersionsHandler_LinkAudiobookVersion_Success(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	store.EXPECT().GetBookByID("b2").Return(&database.Book{ID: "b2"}, nil)
	store.EXPECT().UpdateBook("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)
	store.EXPECT().UpdateBook("b2", mock.Anything).Return(&database.Book{ID: "b2"}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/versions", `{"other_id":"b2"}`, gin.Params{{Key: "id", Value: "b1"}})
	h.LinkAudiobookVersion(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_LinkAudiobookVersion_MissingBody(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	// GetBookByID must never be reached when binding fails.

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/versions", `{}`, gin.Params{{Key: "id", Value: "b1"}})
	h.LinkAudiobookVersion(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── SetAudiobookPrimary ───────────────────────────────────────────────────

func TestVersionsHandler_SetAudiobookPrimary_NoGroup(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	store.EXPECT().UpdateBook("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPut, "/audiobooks/b1/set-primary", "", gin.Params{{Key: "id", Value: "b1"}})
	h.SetAudiobookPrimary(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_SetAudiobookPrimary_WithGroup(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBooksByVersionGroup("g1").Return([]database.Book{{ID: "b1"}, {ID: "b2"}}, nil)
	store.EXPECT().UpdateBook("b1", mock.Anything).Return(&database.Book{ID: "b1"}, nil)
	store.EXPECT().UpdateBook("b2", mock.Anything).Return(&database.Book{ID: "b2"}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPut, "/audiobooks/b1/set-primary", "", gin.Params{{Key: "id", Value: "b1"}})
	h.SetAudiobookPrimary(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ── GetVersionGroup ───────────────────────────────────────────────────────

func TestVersionsHandler_GetVersionGroup_Success(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBooksByVersionGroup("g1").Return([]database.Book{{ID: "b1"}}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodGet, "/version-groups/g1", "", gin.Params{{Key: "id", Value: "g1"}})
	h.GetVersionGroup(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_GetVersionGroup_Error(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBooksByVersionGroup("g1").Return(nil, assert.AnError)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodGet, "/version-groups/g1", "", gin.Params{{Key: "id", Value: "g1"}})
	h.GetVersionGroup(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── SplitVersion ──────────────────────────────────────────────────────────

func TestVersionsHandler_SplitVersion_Success(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	// Source already has a version group → skips the early UpdateBook.
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Book One", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBooksByVersionGroup("g1").Return([]database.Book{{ID: "b1"}}, nil)
	store.EXPECT().CreateBook(mock.Anything).Return(&database.Book{ID: "b2", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f1"}, "b1", "b2").Return(nil)
	// New book gets one remaining file; source has none → only the new-book UpdateBook fires.
	store.EXPECT().GetBookFiles("b2").Return([]database.BookFile{{ID: "f1", FilePath: "/x/f1.m4b"}}, nil)
	store.EXPECT().UpdateBook("b2", mock.Anything).Return(&database.Book{ID: "b2"}, nil)
	store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{}, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/split-version", `{"segment_ids":["f1"]}`, gin.Params{{Key: "id", Value: "b1"}})
	h.SplitVersion(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_SplitVersion_EmptySegments(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	// No store calls expected — request is rejected at binding/validation.

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/split-version", `{"segment_ids":[]}`, gin.Params{{Key: "id", Value: "b1"}})
	h.SplitVersion(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── SplitSegmentsToBooks ──────────────────────────────────────────────────

func TestVersionsHandler_SplitSegmentsToBooks_Success(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Book One"}, nil)
	// First GetBookFiles builds the file map.
	store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{
		{ID: "f1", FilePath: "/x/01 - A Game of Thrones.m4b", Format: "m4b"},
	}, nil).Once()
	store.EXPECT().CreateBook(mock.Anything).Return(&database.Book{ID: "nb1"}, nil)
	store.EXPECT().GetBookAuthors("b1").Return(nil, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f1"}, "b1", "nb1").Return(nil)
	store.EXPECT().GetExternalIDsForBook("b1").Return(nil, nil)
	// Second GetBookFiles fetches the remaining files (now empty).
	store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{}, nil).Once()

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/split-to-books", `{"segment_ids":["f1"]}`, gin.Params{{Key: "id", Value: "b1"}})
	h.SplitSegmentsToBooks(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_SplitSegmentsToBooks_EmptySegments(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/split-to-books", `{"segment_ids":[]}`, gin.Params{{Key: "id", Value: "b1"}})
	h.SplitSegmentsToBooks(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── MoveSegments ──────────────────────────────────────────────────────────

func TestVersionsHandler_MoveSegments_Success(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBookByID("b2").Return(&database.Book{ID: "b2", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBookFiles("b1").Return([]database.BookFile{{ID: "f1", FilePath: "/x/f1.m4b"}}, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f1"}, "b1", "b2").Return(nil)
	store.EXPECT().GetExternalIDsForBook("b1").Return(nil, nil)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/move-segments", `{"segment_ids":["f1"],"target_book_id":"b2"}`, gin.Params{{Key: "id", Value: "b1"}})
	h.MoveSegments(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVersionsHandler_MoveSegments_EmptySegments(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/move-segments", `{"segment_ids":[],"target_book_id":"b2"}`, gin.Params{{Key: "id", Value: "b1"}})
	h.MoveSegments(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestVersionsHandler_MoveSegments_GroupMismatch(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", VersionGroupID: strptr("g1")}, nil)
	store.EXPECT().GetBookByID("b2").Return(&database.Book{ID: "b2", VersionGroupID: strptr("g2")}, nil)
	// MoveBookFilesToBook must never be reached.

	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/b1/move-segments", `{"segment_ids":["f1"],"target_book_id":"b2"}`, gin.Params{{Key: "id", Value: "b1"}})
	h.MoveSegments(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
