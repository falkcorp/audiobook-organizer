// file: internal/server/handlers/split_book_merge_recheck_test.go
// version: 1.0.0
// guid: 9f2a6d84-3c17-4b50-8e91-a4d7c0b35f62
// last-edited: 2026-10-21

package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// seedMergeRecheckCluster stores three single-file books in one folder and
// returns their ids.
func seedMergeRecheckCluster(t *testing.T, store database.Store, titleOf func(i int) string, fileSecs int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= 3; i++ {
		id := ulid.Make().String()
		ids = append(ids, id)
		path := fmt.Sprintf("/tmp/split-recheck/Sample Author/Sample Work/%02d.mp3", i)
		_, err := store.CreateBook(&database.Book{ID: id, Title: titleOf(i), Format: "mp3", FilePath: path})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: id, FilePath: path, Format: "mp3", TrackNumber: 1, Duration: fileSecs}))
	}
	return ids
}

func postSplitMerge(h *SplitBookHandler, id, keep string) *httptest.ResponseRecorder {
	router := gin.New()
	router.POST("/dedup/split-book-candidates/:id/merge", h.MergeSplitBookCandidate)
	req := httptest.NewRequest(http.MethodPost, "/dedup/split-book-candidates/"+id+"/merge", strings.NewReader(`{"keep_id":"`+keep+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// A stored candidate whose members are now vetoed (distinct Book numbers in
// their titles) is refused with 409 and nothing is written: no book retired,
// no file row moved.
func TestMergeSplitBookCandidate_RefusesAStoredCandidateThatIsNowVetoed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := dbtest.NewStore(t)
	ids := seedMergeRecheckCluster(t, store, func(i int) string { return fmt.Sprintf("Sample Saga Book %d", i) }, 600)
	cands := &splitBookBulkStore{candidates: map[string]*dedup.SplitBookCandidate{
		"stale": {ID: "stale", BookIDs: ids, Shape: "parent", ParentFolder: "/tmp/split-recheck/Sample Author/Sample Work"},
	}}
	w := postSplitMerge(NewSplitBookHandler(nil, cands, store), "stale", ids[0])
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "V2")
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "%s must not be retired", id)
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1, "%s keeps its own file row", id)
	}
}

// The duration gate and the unknown-duration hold apply on the single route too.
func TestMergeSplitBookCandidate_RefusesLongAndUnknownDurations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, secs := range map[string]int{"over the chapter limit": 4 * 3600, "unknown": 0} {
		t.Run(name, func(t *testing.T) {
			store := dbtest.NewStore(t)
			ids := seedMergeRecheckCluster(t, store, func(i int) string { return fmt.Sprintf("Sample Work - Chapter %d", i) }, secs)
			cands := &splitBookBulkStore{candidates: map[string]*dedup.SplitBookCandidate{"c": {ID: "c", BookIDs: ids}}}
			w := postSplitMerge(NewSplitBookHandler(nil, cands, store), "c", ids[0])
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			b, err := store.GetBookByID(ids[1])
			require.NoError(t, err)
			require.False(t, b.IsSoftDeleted())
		})
	}
}
