// file: internal/server/handlers/split_book_merge_revert_test.go
// version: 1.0.0
// guid: 6a1d9e53-2f74-4c08-8b6e-d30f5a72c194
// last-edited: 2026-10-18

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// POST /dedup/split-book-candidates/:id/merge runs in no operation: the
// split-book merge journals its undo pointer under a correlation id of its
// own and the result carries it as operation_id. Reverting that operation
// (the engine POST /operations/:id/revert runs) brings the merged books and
// their file rows back.
func TestMergeSplitBookCandidate_UndoneByOperationRevert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := dbtest.NewStore(t)
	keep, src := ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, src} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter " + id, Format: "mp3", FilePath: "/tmp/split-revert/" + id})
		require.NoError(t, err)
	}
	keepFile, srcFile := ulid.Make().String(), ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: keepFile, BookID: keep, FilePath: "/tmp/split-revert/" + keep + "/01.mp3", Format: "mp3", TrackNumber: 1, Duration: 30}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: srcFile, BookID: src, FilePath: "/tmp/split-revert/" + src + "/02.mp3", Format: "mp3", TrackNumber: 1, Duration: 30}))

	cands := &splitBookBulkStore{candidates: map[string]*dedup.SplitBookCandidate{
		"cand": {ID: "cand", BookIDs: []string{keep, src}},
	}}
	h := NewSplitBookHandler(nil, cands, store)
	router := gin.New()
	router.POST("/dedup/split-book-candidates/:id/merge", h.MergeSplitBookCandidate)
	req := httptest.NewRequest(http.MethodPost, "/dedup/split-book-candidates/cand/merge", strings.NewReader(`{"keep_id":"`+keep+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data dedup.SplitBookMergeResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	opID := body.Data.OperationID
	require.NotEmpty(t, opID, w.Body.String())
	require.Equal(t, 1, body.Data.MergedSrcCount)
	moved, err := store.GetBookFileByID(keep, srcFile)
	require.NoError(t, err)
	require.NotNil(t, moved, "the source's file row moved to keep")

	res, err := audiobooks.NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err)
	require.Equal(t, 1, res.RestoredTypes[undo.ChangeTypeMergeCombineJournal])
	b, err := store.GetBookByID(src)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "the merged source is live again")
	back, err := store.GetBookFileByID(src, srcFile)
	require.NoError(t, err)
	require.NotNil(t, back, "its file row is back on it")
	own, err := store.GetBookFileByID(keep, keepFile)
	require.NoError(t, err)
	require.NotNil(t, own, "keep keeps its own file")
}
