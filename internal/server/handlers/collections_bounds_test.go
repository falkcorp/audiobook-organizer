// file: internal/server/handlers/collections_bounds_test.go
// version: 1.1.0
// guid: 8b4e1f73-2a96-4c05-9d38-e6a1c7f50b24
// last-edited: 2026-10-30

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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// perUserBooleanQueries are queries whose per-user term sits under an OR with a
// sibling, or under a NOT over a group. They were evaluated as an AND, silently,
// then refused with a 400; they are evaluated as written now (search.MixedQuery).
var perUserBooleanQueries = []string{
	"read_status:finished OR title:Alpha",
	"title:Alpha OR progress_pct:>10",
	"read_status:finished OR read_status:unstarted",
	"-(read_status:finished title:Alpha)",
	"NOT (last_played:* OR title:Bravo)",
}

// Creating or updating a dynamic collection with such a query is accepted: it is a
// per-viewer collection, evaluated for whoever reads it.
func TestCollections_AcceptAPerUserTermUnderAnOrOrAGroupedNot(t *testing.T) {
	for _, q := range perUserBooleanQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := &colFakeStore{}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "The Long Halloween", "type": "dynamic", "query": q})
			assert.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
			require.NotNil(t, store.created)
			assert.Equal(t, q, store.created.Query)
		})
		t.Run("update "+q, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:Alpha",
				MaterializedBookIDs: []string{"b1"},
			}}}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": q})
			assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, q, store.cols[0].Query)
		})
	}
	// A value the evaluator cannot judge is still refused, under an OR too.
	t.Run("a bad value under an OR is still refused", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
			map[string]any{"name": "Year One", "type": "dynamic", "query": "read_status:completed OR title:Alpha"})
		assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		assert.Contains(t, w.Body.String(), "read_status must be one of")
		assert.Nil(t, store.created, "nothing is saved")
	})
}

// A dynamic collection whose query ORs an indexed term with a per-user term is
// answered per viewer: the books the title names plus the books THAT viewer
// finished. alice finished b1 and has b2 in progress; bob finished b2; carol has no
// state (viewerStates). Read through GET, which is the path every native screen uses.
func TestCollections_APerUserOrIsEvaluatedForTheViewer(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  map[string][]string
	}{
		{"title:Charlie OR read_status:finished", map[string][]string{
			"alice": {"b1", "b3"}, "bob": {"b2", "b3"}, "carol": {"b3"},
		}},
		{"(title:Alpha OR read_status:in_progress) AND title:Bravo", map[string][]string{
			"alice": {"b2"}, "bob": {}, "carol": {},
		}},
		{"-(read_status:finished title:Alpha)", map[string][]string{
			"alice": {"b2", "b3"}, "bob": {"b1", "b2", "b3"}, "carol": {"b1", "b2", "b3"},
		}},
	} {
		for viewer, members := range tc.want {
			t.Run(tc.query+" as "+viewer, func(t *testing.T) {
				rig := newViewerRig(t, aliceFinishedCollection(tc.query), viewerStates())
				got := decodeMaterialized(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", viewer))
				assert.ElementsMatch(t, members, got)
			})
		}
	}
}

// B-1: a row that was saved before the check reads as unavailable with its own kind,
// not as a collection holding a wrong answer.
func TestCollections_AStoredQueryTheEvaluatorRefusesReadsAsUnavailable(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		perViewerRow("c1", "read_status:completed OR title:Alpha", "b1"),
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
	assert.Contains(t, rig.warns()[0], "read_status", "the cause names the term, not the query text")
}

// A smart playlist with such a query is accepted on save (create and update share
// validatePlaylistQuery), and a value the evaluator cannot judge is refused under an
// OR as it is anywhere else.
func TestPlaylists_AcceptAPerUserTermUnderAnOrOrAGroupedNot(t *testing.T) {
	create := func(h *handlers.PlaylistHandler, q string) *httptest.ResponseRecorder {
		c, w := newPlaylistCtxAs(http.MethodPost, "/playlists", "alice")
		body, _ := json.Marshal(map[string]any{"name": "Hush", "type": "smart", "query": q})
		c.Request = httptest.NewRequest(http.MethodPost, "/playlists", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.CreatePlaylist(c)
		return w
	}
	for _, q := range perUserBooleanQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t)
			store.EXPECT().CreateUserPlaylist(mock.MatchedBy(func(p *database.UserPlaylist) bool {
				return p.Query == q && p.CreatedByUserID == "alice"
			})).RunAndReturn(func(p *database.UserPlaylist) (*database.UserPlaylist, error) { return p, nil })
			w := create(handlers.NewPlaylistHandler(store, noSearchIndex), q)
			assert.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		})
	}
	t.Run("a bad value under an OR is still refused", func(t *testing.T) {
		store := handlersmocks.NewMockPlaylistStore(t) // no call may reach the store
		w := create(handlers.NewPlaylistHandler(store, noSearchIndex), "read_status:completed OR title:Alpha")
		assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		assert.Contains(t, w.Body.String(), "read_status must be one of")
	})
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
		// The log is readable by every library.view user, and one viewer's match
		// count is that viewer's listening data: the bound is logged, the count is not.
		var capped []string
		for _, line := range rig.warns() {
			if strings.Contains(line, "for a viewer") {
				capped = append(capped, line)
			}
		}
		require.NotEmpty(t, capped, "the cut is logged; warns: %v", rig.warns())
		for _, line := range capped {
			assert.Contains(t, line, fmt.Sprintf("more than %d books", database.MaxMaterializedMembers))
			assert.NotContains(t, line, fmt.Sprint(over), "a viewer's own count is not logged")
		}
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

// S-4 / S-C: a failure to read the viewer's own state is logged by class only (its
// text names the user and a book, and the activity log is readable with
// library.view); every other failure is logged with its text, because the operator
// needs it and it names no viewer.
func TestCollections_EvaluationFailureLogLinesAreRedactedOnlyForStateReads(t *testing.T) {
	stateReads := []struct {
		name    string
		err     error
		secrets []string
	}{
		{
			name: "an undecodable state row names a user and a book",
			err: fmt.Errorf("%w: %s/%s: %w", database.ErrUserBookStateUndecodable,
				"alice", "b1", errors.New("unexpected EOF at /lib/Gotham/blob")),
			secrets: []string{"alice/b1", "alice", "unexpected EOF", "Gotham"},
		},
		{
			name:    "a state scan failure with a path and a name",
			err:     errors.New("disk read failed at /lib/Gotham for user alice"),
			secrets: []string{"Gotham", "alice", "disk read"},
		},
	}
	for _, tc := range stateReads {
		t.Run(tc.name, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", "read_status:finished")}}
			ev := &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}, listErr: tc.err}
			rig := newFailureRig(t, store, ev, true)
			_ = decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
			lines := rig.warns()
			require.Len(t, lines, 1)
			assert.Contains(t, lines[0], "evaluation-failed")
			assert.Contains(t, lines[0], "details withheld")
			for _, secret := range tc.secrets {
				assert.False(t, strings.Contains(lines[0], secret), "log line %q must not contain %q", lines[0], secret)
			}
		})
	}

	t.Run("a sort spec that does not parse is logged as it is", func(t *testing.T) {
		// Saved before sort_json was validated: every evaluation of two or more
		// books fails on it, and the operator has to be able to read why.
		row := perViewerRow("c1", "-read_status:finished")
		row.SortJSON = "title"
		store := &colFakeStore{cols: []database.Collection{row}}
		docs := []search.BookDocument{{BookID: "b1", Title: "Alpha"}, {BookID: "b2", Title: "Bravo"}, {BookID: "b3", Title: "Charlie"}}
		rig := newFailureRigDocs(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true, docs)
		got := decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
		assert.True(t, got.MembersUnavailable)
		lines := rig.warns()
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "parse sort_json", "the cause is readable")
		assert.NotContains(t, lines[0], "details withheld")
	})
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
