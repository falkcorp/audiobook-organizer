// file: internal/server/handlers/collections_viewer_failures_test.go
// version: 1.0.2
// guid: 3e9c1a74-6b05-4d82-a7f1-c20d58e4b196
// last-edited: 2026-10-26

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// Failure and cost behaviour of the per-viewer evaluation on the native routes:
// a members list that could not be worked out is flagged (never passed off as "no
// matches"), a stored list of an unparseable query is never served, the viewer's
// state is read once per request, and evaluations are bounded process-wide.

// countingEval is an eval store that also implements the one-scan state lister,
// so a test can count how the evaluator reads the viewer's state.
type countingEval struct {
	colFakeBooks
	listCalls  atomic.Int64
	pointReads atomic.Int64
	// listErr, when set, is what the state scan returns.
	listErr error
	// onList, when set, runs inside each scan (the semaphore test blocks here).
	onList func()
}

func (c *countingEval) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	c.pointReads.Add(1)
	return c.colFakeBooks.GetUserBookState(userID, bookID)
}

func (c *countingEval) ListUserBookStates(_ context.Context, userID string) (map[string]*database.UserBookState, error) {
	c.listCalls.Add(1)
	if c.onList != nil {
		c.onList()
	}
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := map[string]*database.UserBookState{}
	for k, v := range c.states {
		if strings.HasPrefix(k, userID+"/") {
			out[v.BookID] = v
		}
	}
	return out, nil
}

type failureRig struct {
	r   *gin.Engine
	h   *handlers.CollectionHandler
	log *warnRecorder
}

// newFailureRig is newViewerRig with a caller-supplied eval store and a switch
// for the search index: indexOpen=false hands the handler a nil index, the
// state of a server whose index is still opening or rebuilding.
func newFailureRig(t *testing.T, store *colFakeStore, ev handlers.CollectionEvalStore, indexOpen bool) *failureRig {
	t.Helper()
	return newFailureRigDocs(t, store, ev, indexOpen, []search.BookDocument{
		{BookID: "b1", Title: "Alpha"},
		{BookID: "b2", Title: "Bravo"},
		{BookID: "b3", Title: "Charlie"},
	})
}

// newFailureRigDocs is newFailureRig over the given indexed documents.
func newFailureRigDocs(t *testing.T, store *colFakeStore, ev handlers.CollectionEvalStore, indexOpen bool, docs []search.BookDocument) *failureRig {
	t.Helper()
	var idx *search.BleveIndex
	if indexOpen {
		var err error
		idx, err = search.Open(filepath.Join(t.TempDir(), "bleve"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = idx.Close() })
		require.NoError(t, idx.MarkRebuilt())
		require.NoError(t, idx.IndexBookBatch(docs))
	}
	gin.SetMode(gin.TestMode)
	h := handlers.NewCollectionHandler(store, nil, ev, func() *search.BleveIndex { return idx })
	rec := &warnRecorder{}
	handlers.SetCollectionLogger(h, rec)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if id := c.GetHeader("X-Test-User"); id != "" {
			u := &database.User{ID: id, Username: id}
			c.Request = c.Request.WithContext(auth.WithUser(c.Request.Context(), u))
			c.Set("auth_user", u)
		}
		c.Next()
	})
	g := r.Group("/api/v1")
	g.GET("/collections", h.ListCollections)
	g.GET("/collections/:id", h.GetCollection)
	g.POST("/collections", h.CreateCollection)
	g.PUT("/collections/:id", h.UpdateCollection)
	g.POST("/collections/:id/materialize", h.MaterializeCollection)
	return &failureRig{r: r, h: h, log: rec}
}

func (f *failureRig) do(t *testing.T, method, path, asUser string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return (&viewerRig{r: f.r}).send(t, method, path, asUser, body)
}

func (f *failureRig) warns() []string {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	return append([]string(nil), f.log.lines...)
}

// viewerResponse is the part of a collection response these tests read.
type viewerResponse struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Members            []string `json:"materialized_book_ids"`
	MembersUnavailable bool     `json:"members_unavailable"`
	Message            string   `json:"members_unavailable_message"`
}

func decodeViewerResponse(t *testing.T, w *httptest.ResponseRecorder) viewerResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var env struct {
		Data viewerResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env.Data
}

func decodeViewerList(t *testing.T, w *httptest.ResponseRecorder) map[string]viewerResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var env struct {
		Items []viewerResponse `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	out := map[string]viewerResponse{}
	for _, it := range env.Items {
		out[it.ID] = it
	}
	return out
}

func perViewerRow(id, query string, stored ...string) database.Collection {
	return database.Collection{
		ID: id, Name: "Knightfall " + id, Type: database.CollectionTypeDynamic,
		Query: query, CreatedByUserID: "alice", MaterializedBookIDs: stored,
	}
}

// B1: a stored per-viewer query that no longer parses must never serve its stored
// list. Nobody can say it reads no per-user state, so the list may be a creator's;
// it cannot be evaluated either, so the answer is "no members" and it says why.
func TestUnparseablePerViewerQueryNeverServesItsStoredList(t *testing.T) {
	const broken = `read_status:finished "unterminated`
	newStore := func() *colFakeStore {
		return &colFakeStore{cols: []database.Collection{perViewerRow("c1", broken, "b1", "b2")}}
	}
	t.Run("get", func(t *testing.T) {
		rig := newFailureRig(t, newStore(), &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)
		got := decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
		assert.Empty(t, got.Members, "the stored list is never served")
		assert.True(t, got.MembersUnavailable)
		assert.Contains(t, got.Message, "query")
		require.Len(t, rig.warns(), 1)
		assert.Contains(t, rig.warns()[0], "query-invalid", "a different kind from an unavailable index, so its own log line")
	})
	t.Run("list", func(t *testing.T) {
		rig := newFailureRig(t, newStore(), &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)
		got := decodeViewerList(t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))["c1"]
		assert.Empty(t, got.Members)
		assert.True(t, got.MembersUnavailable)
		assert.Contains(t, got.Message, "query")
	})
	t.Run("view helper used by the ABS surface", func(t *testing.T) {
		store := newStore()
		rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)
		res, err := rig.h.ViewerMembers(context.Background(), store.cols, "alice")
		require.NoError(t, err)
		assert.Equal(t, []string{}, res.Sets["c1"])
		assert.Error(t, res.Unavailable["c1"])
	})
}

// S2: a rename-only PUT on a per-viewer collection answers with the caller's own
// members. The detail page keeps the PUT's answer as its state, so an answer with
// no members showed an empty collection until a reload.
func TestRenameOnlyPutOfAPerViewerCollectionAnswersTheCallersMembers(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished", "b9")}}
	rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)

	got := decodeViewerResponse(t, rig.do(t, http.MethodPut, "/api/v1/collections/c1", "alice",
		map[string]any{"name": "Renamed Knightfall"}))
	assert.Equal(t, "Renamed Knightfall", got.Name)
	assert.Equal(t, []string{"b1"}, got.Members, "alice's own members, though only the name changed")
	assert.False(t, got.MembersUnavailable)
	assert.Equal(t, "Renamed Knightfall", store.cols[0].Name)
	assert.Empty(t, store.cols[0].MaterializedBookIDs, "the viewer's members are never stored; the leftover list is cleared")

	got = decodeViewerResponse(t, rig.do(t, http.MethodPut, "/api/v1/collections/c1", "bob",
		map[string]any{"description": "second edit"}))
	assert.Equal(t, []string{"b2"}, got.Members, "a different editor gets their own")
}

// S4: an evaluation that cannot run is flagged on every native path that returns
// members, and logged, once per kind per minute.
func TestUnavailableMembersAreFlaggedAndLoggedOncePerKind(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		perViewerRow("c1", "read_status:finished", "b1"),
		{ID: "plain", Name: "Year One", Type: database.CollectionTypeDynamic, Query: "title:Alpha",
			MaterializedBookIDs: []string{"b1"}},
	}}
	rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, false)

	got := decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
	assert.True(t, got.MembersUnavailable, "a closed index is flagged, not shown as no matches")
	assert.Empty(t, got.Members)
	assert.Contains(t, got.Message, "search index")
	require.Len(t, rig.warns(), 1)
	assert.Contains(t, rig.warns()[0], "search-index-unavailable")
	assert.Contains(t, rig.warns()[0], "c1")

	list := decodeViewerList(t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))
	assert.True(t, list["c1"].MembersUnavailable)
	assert.False(t, list["plain"].MembersUnavailable, "a shared collection serves its last known list unflagged")
	assert.Equal(t, []string{"b1"}, list["plain"].Members)

	put := decodeViewerResponse(t, rig.do(t, http.MethodPut, "/api/v1/collections/c1", "alice",
		map[string]any{"name": "Renamed Knightfall"}))
	assert.True(t, put.MembersUnavailable, "the edit is saved and the members flagged")
	assert.Equal(t, "Renamed Knightfall", store.cols[0].Name)

	assert.Len(t, rig.warns(), 1, "the same kind is logged once per minute, not once per request")
}

// N4: a state row that cannot be read is an error, never "this viewer has no
// state". Evaluated as no state, -read_status:finished would match every book.
func TestUnreadableViewerStateIsUnavailableNotEveryBook(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		perViewerRow("c1", "-read_status:finished"),
		perViewerRow("c2", "read_status:unstarted"),
	}}
	ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}, listErr: errors.New("disk read failed at /lib/Gotham")}
	rig := newFailureRig(t, store, ev, true)

	get := rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil)
	got := decodeViewerResponse(t, get)
	assert.True(t, got.MembersUnavailable)
	assert.Empty(t, got.Members, "not every book")
	assert.NotContains(t, get.Body.String(), "Gotham", "internal error text is not sent to the client")

	list := decodeViewerList(t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))
	for _, id := range []string{"c1", "c2"} {
		assert.True(t, list[id].MembersUnavailable, id)
		assert.Empty(t, list[id].Members, id)
	}
	require.NotEmpty(t, rig.warns())
	assert.Contains(t, rig.warns()[0], "evaluation-failed")
}

// The viewer's own state is read once per request and shared by every per-viewer
// collection on the page: one prefix scan, no point read per candidate book.
func TestListReadsTheViewersStateOncePerRequest(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		perViewerRow("c1", "read_status:finished"),
		perViewerRow("c2", "-read_status:finished"),
		perViewerRow("c3", "read_status:unstarted"),
	}}
	ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}
	rig := newFailureRig(t, store, ev, true)

	list := decodeViewerList(t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))
	assert.Equal(t, []string{"b1"}, list["c1"].Members)
	assert.ElementsMatch(t, []string{"b2", "b3"}, list["c2"].Members)
	assert.Equal(t, []string{"b3"}, list["c3"].Members)
	assert.EqualValues(t, 1, ev.listCalls.Load(), "three collections, one scan of alice's rows")
	assert.EqualValues(t, 0, ev.pointReads.Load(), "no point read per candidate book")
}

// The evaluation bound is process-wide: however many requests arrive together,
// no more than the limit are inside an evaluation at once.
func TestConcurrentEvaluationsAreBoundedAcrossRequests(t *testing.T) {
	const limit = 2
	defer handlers.SetCollectionEvalLimit(limit)()

	var inflight, peak atomic.Int64
	gate := make(chan struct{})
	var once sync.Once
	ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}
	ev.onList = func() {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= limit {
			once.Do(func() { close(gate) })
		}
		// Hold the slot until the limit is reached (so the test proves two can run
		// together) or a timeout (so a broken test cannot hang).
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
		}
		time.Sleep(5 * time.Millisecond)
		inflight.Add(-1)
	}
	store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished")}}
	rig := newFailureRig(t, store, ev, true)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil)
			assert.Equal(t, http.StatusOK, w.Code)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, limit, peak.Load(), "twelve requests, at most %d evaluations at a time (and %d did run together)", limit, limit)
}
