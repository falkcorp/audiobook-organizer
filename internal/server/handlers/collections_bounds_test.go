// file: internal/server/handlers/collections_bounds_test.go
// version: 1.0.0
// guid: 8b4e1f73-2a96-4c05-9d38-e6a1c7f50b24
// last-edited: 2026-10-26

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// perUserBooleanQueries are queries whose per-user term the evaluator cannot give
// its boolean meaning (under an OR with a sibling, or under a NOT over a group).
// They used to be evaluated as an AND, silently.
var perUserBooleanQueries = []string{
	"read_status:finished OR title:Alpha",
	"title:Alpha OR progress_pct:>10",
	"read_status:finished OR read_status:unstarted",
	"-(read_status:finished title:Alpha)",
	"NOT (last_played:* OR title:Bravo)",
}

// B-1: creating or updating a dynamic collection with such a query is refused with a
// 400 that says what to write instead, and nothing is saved.
func TestCollections_RefuseAPerUserTermTheEvaluatorCannotEvaluateAsWritten(t *testing.T) {
	for _, q := range perUserBooleanQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := &colFakeStore{}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "The Long Halloween", "type": "dynamic", "query": q})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "read_status:(finished|in_progress)", "the message names the working spelling")
			assert.Nil(t, store.created, "nothing is saved")
		})
		t.Run("update "+q, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:Alpha",
				MaterializedBookIDs: []string{"b1"},
			}}}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": q})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "title:Alpha", store.cols[0].Query, "the stored query is untouched")
			assert.Zero(t, store.updates)
		})
	}
	t.Run("the working spellings are accepted", func(t *testing.T) {
		for _, q := range []string{"read_status:(finished|in_progress)", "-read_status:finished title:Alpha", "title:Alpha OR title:Bravo"} {
			rig := newViewerRig(t, &colFakeStore{}, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "Year One", "type": "dynamic", "query": q})
			assert.Equal(t, http.StatusCreated, w.Code, "%s: %s", q, w.Body.String())
		}
	})
}

// B-1: a row that was saved before the check reads as unavailable with its own kind,
// not as a collection holding the AND answer.
func TestCollections_AStoredQueryTheEvaluatorRefusesReadsAsUnavailable(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		perViewerRow("c1", "read_status:finished OR title:Alpha", "b1"),
	}}
	rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)

	got := decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
	assert.True(t, got.MembersUnavailable)
	assert.Empty(t, got.Members, "neither the stored list nor an AND answer")
	assert.Contains(t, got.Message, "query")
	list := decodeViewerList(t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))
	assert.True(t, list["c1"].MembersUnavailable)
	require.NotEmpty(t, rig.warns())
	assert.Contains(t, rig.warns()[0], "query-invalid")
	assert.Contains(t, rig.warns()[0], "OR", "the cause names the shape, not the query text")
}

func TestPlaylists_RefuseAPerUserTermTheEvaluatorCannotEvaluateAsWritten(t *testing.T) {
	for _, q := range perUserBooleanQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t) // no call may reach the store
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			c, w := newPlaylistCtxAs(http.MethodPost, "/playlists", "alice")
			body, _ := json.Marshal(map[string]any{"name": "Hush", "type": "smart", "query": q})
			c.Request = httptest.NewRequest(http.MethodPost, "/playlists", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			h.CreatePlaylist(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "read_status:(finished|in_progress)")
		})
		t.Run("update "+q, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t)
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			c, w := newPlaylistCtxAs(http.MethodPut, "/playlists/pl-1", "alice")
			body, _ := json.Marshal(map[string]any{"query": q})
			c.Request = httptest.NewRequest(http.MethodPut, "/playlists/pl-1", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			h.UpdatePlaylist(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
	}
}

// synthetic documents: ids s00000.., titles "Synthetic N".
func syntheticDocs(n int) []search.BookDocument {
	docs := make([]search.BookDocument, n)
	for i := range docs {
		docs[i] = search.BookDocument{BookID: fmt.Sprintf("s%05d", i), Title: fmt.Sprintf("Synthetic %d", i)}
	}
	return docs
}

// S-1: a query is evaluated with no result window, so a broad one answers with the
// whole library. The stored list, the response and the ABS expansion are cut to
// database.MaxMaterializedMembers and the real count is reported.
func TestCollections_MemberListsAreCutToTheBoundAndReportTheTotal(t *testing.T) {
	over := database.MaxMaterializedMembers + 300
	docs := syntheticDocs(over)
	newRig := func(t *testing.T, store *colFakeStore) *failureRig {
		return newFailureRigDocs(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true, docs)
	}
	type view struct {
		Members []string `json:"materialized_book_ids"`
		Total   int      `json:"materialized_total"`
	}
	decode := func(t *testing.T, w *httptest.ResponseRecorder) view {
		t.Helper()
		require.Contains(t, []int{http.StatusOK, http.StatusCreated}, w.Code, "body: %s", w.Body.String())
		var env struct {
			Data view `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		return env.Data
	}

	t.Run("a stored list is cut on create, read and materialize and is not rewritten when nothing changed", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newRig(t, store)
		created := decode(t, rig.do(t, http.MethodPost, "/api/v1/collections", "alice",
			map[string]any{"name": "Hush", "type": "dynamic", "query": "Synthetic"}))
		require.Len(t, store.cols, 1)
		assert.Len(t, store.cols[0].MaterializedBookIDs, database.MaxMaterializedMembers, "the stored list is cut")
		assert.Equal(t, over, store.cols[0].MaterializedTotal, "and carries the real count")
		assert.Len(t, created.Members, database.MaxMaterializedMembers)
		assert.Equal(t, over, created.Total)

		store.cols[0].Version = 1
		updatesBefore := store.updates
		for _, call := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/collections/col-1"},
			{http.MethodPost, "/api/v1/collections/col-1/materialize"},
		} {
			got := decode(t, rig.do(t, call.method, call.path, "alice", nil))
			assert.Len(t, got.Members, database.MaxMaterializedMembers, call.path)
			assert.Equal(t, over, got.Total, call.path)
		}
		assert.Equal(t, updatesBefore, store.updates, "an unchanged oversized answer is a no-op, not a rewrite per read")
		require.NotEmpty(t, rig.warns())
		assert.Contains(t, rig.warns()[0], "cut to the first 10000")
	})

	t.Run("a viewer's own list is cut in the response and nothing is stored", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{perViewerRow("pv", "read_status:unstarted")}}
		rig := newRig(t, store)
		got := decode(t, rig.do(t, http.MethodGet, "/api/v1/collections/pv", "alice", nil))
		assert.Len(t, got.Members, database.MaxMaterializedMembers)
		assert.Equal(t, over, got.Total)
		list := decodeListView[view](t, rig.do(t, http.MethodGet, "/api/v1/collections", "alice", nil))
		assert.Len(t, list["pv"].Members, database.MaxMaterializedMembers)
		assert.Equal(t, over, list["pv"].Total)
		assert.Empty(t, store.cols[0].MaterializedBookIDs)
		assert.Zero(t, store.cols[0].MaterializedTotal)
	})

	t.Run("a collection under the bound reports no total", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newFailureRigDocs(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true, syntheticDocs(50))
		got := decode(t, rig.do(t, http.MethodPost, "/api/v1/collections", "alice",
			map[string]any{"name": "Year One", "type": "dynamic", "query": "Synthetic"}))
		assert.Len(t, got.Members, 50)
		assert.Zero(t, got.Total)
	})
}

func decodeListView[T any](t *testing.T, w *httptest.ResponseRecorder) map[string]T {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var env struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	out := map[string]T{}
	for _, raw := range env.Items {
		var id struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(raw, &id))
		var v T
		require.NoError(t, json.Unmarshal(raw, &v))
		out[id.ID] = v
	}
	return out
}

// S-2: a request that is cancelled while it waits for an evaluation slot gives the
// slot queue up and is dropped as a client that went away: not evaluated, not
// flagged unavailable, not logged.
func TestCollections_ACancelledRequestLeavesTheSlotQueue(t *testing.T) {
	defer handlers.SetCollectionEvalLimit(1)()

	started := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int64
	ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}
	ev.onList = func() {
		if scans.Add(1) == 1 {
			close(started)
			<-release
		}
	}
	store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished")}}
	rig := newFailureRig(t, store, ev, true)

	done := func(ctx context.Context) <-chan *httptest.ResponseRecorder {
		out := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/c1", nil).WithContext(ctx)
			req.Header.Set("X-Test-User", "alice")
			w := httptest.NewRecorder()
			rig.r.ServeHTTP(w, req)
			out <- w
		}()
		return out
	}

	first := done(context.Background())
	<-started // the only slot is now held

	ctx, cancel := context.WithCancel(context.Background())
	second := done(ctx)
	time.Sleep(50 * time.Millisecond) // let it reach the slot wait
	cancel()

	select {
	case w := <-second:
		assert.Equal(t, 499, w.Code, "a client that went away is not a 200 and not a 500")
		assert.NotContains(t, w.Body.String(), "members_unavailable")
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled request kept waiting behind the evaluation that holds the slot")
	}
	assert.EqualValues(t, 1, scans.Load(), "the cancelled request never started an evaluation")
	assert.Empty(t, rig.warns(), "a client leaving is not logged as a failure of the collection")

	close(release)
	w := <-first
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCollections_ViewerMembersWithACancelledContextReturnsItsError(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished")}}
	rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := rig.h.ViewerMembers(ctx, store.cols, "alice")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, res.Unavailable, "cancellation is not an unavailable collection")
	assert.Empty(t, rig.warns())
}

// S-4: the failure log line carries the class of the failure and no user or book id,
// no path and no store text: the activity log is readable with library.view.
func TestCollections_EvaluationFailureLogLinesAreRedacted(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    string
		secrets []string
	}{
		{
			name: "an undecodable state row names a user and a book",
			err: fmt.Errorf("%w: %s/%s: %w", database.ErrUserBookStateUndecodable,
				"alice", "b1", errors.New("unexpected EOF at /lib/Gotham/blob")),
			want:    "did not decode",
			secrets: []string{"alice/b1", "alice", "unexpected EOF", "Gotham"},
		},
		{
			name:    "a store error with a path and a name",
			err:     errors.New("disk read failed at /lib/Gotham for user alice"),
			want:    "failed",
			secrets: []string{"Gotham", "alice", "disk read"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished")}}
			ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}, listErr: tc.err}
			rig := newFailureRig(t, store, ev, true)
			_ = decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
			lines := rig.warns()
			require.Len(t, lines, 1)
			assert.Contains(t, lines[0], "evaluation-failed")
			assert.Contains(t, lines[0], tc.want)
			for _, secret := range tc.secrets {
				assert.False(t, strings.Contains(lines[0], secret), "log line %q must not contain %q", lines[0], secret)
			}
		})
	}
}

// S-1 for smart playlists, which share the evaluator: the answer and the cached
// list are cut to the bound and carry the real count.
func TestPlaylists_ASmartPlaylistAnswerIsCutToTheBoundAndReportsTheTotal(t *testing.T) {
	over := database.MaxMaterializedMembers + 300
	idx, err := search.Open(t.TempDir() + "/bleve")
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch(syntheticDocs(over)))

	pl := &database.UserPlaylist{ID: "pl-1", Name: "No Man's Land", Type: database.UserPlaylistTypeSmart,
		Query: "Synthetic", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
	var saved *database.UserPlaylist
	store.EXPECT().UpdateUserPlaylist(pl).RunAndReturn(func(p *database.UserPlaylist) error {
		cp := *p
		saved = &cp
		return nil
	})

	h := handlers.NewPlaylistHandler(store, func() *search.BleveIndex { return idx })
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1", "alice")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.GetPlaylist(c)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var env struct {
		Data struct {
			BookIDs      []string `json:"book_ids"`
			BookIDsTotal int      `json:"book_ids_total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	assert.Len(t, env.Data.BookIDs, database.MaxMaterializedMembers)
	assert.Equal(t, over, env.Data.BookIDsTotal)
	require.NotNil(t, saved, "the cached list is written")
	assert.Len(t, saved.MaterializedBookIDs, database.MaxMaterializedMembers)
	assert.Equal(t, over, saved.MaterializedTotal)
}
