// file: internal/server/handlers/collections_values_test.go
// version: 1.0.2
// guid: c3a8e5d1-6b27-4f90-a1d4-52e7b09c6f38
// last-edited: 2026-10-26

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// refusedTermQueries are per-user terms whose VALUE the evaluator cannot give a
// meaning: "completed" is not a status (read_status:completed matched nothing,
// -read_status:completed matched every book), a pattern is compared as plain text,
// a number or a date that does not parse is not a bound, and a field name written
// with capitals is not a per-user term to the translator at all.
//
// The tag: terms are the other refused class: a tag: filter is exact and applies
// to the whole query, so it is refused under an OR, a NOT or an alternation, and as
// a prefix or fuzzy term (search.SplitTagTerms).
var refusedTermQueries = []string{
	"read_status:completed",
	"-read_status:completed",
	"read_status:fin*",
	"progress_pct:>half",
	"last_played:>soon",
	"READ_STATUS:finished",
	"title:Alpha OR tag:noir",
	"-tag:noir",
	"tag:(noir|gotham)",
	"tag:noir*",
}

// B-alpha: creating or updating a dynamic collection with such a value is refused
// with a 400 that names what is valid, and nothing is saved.
func TestCollections_RefuseAPerUserValueTheEvaluatorCannotEvaluate(t *testing.T) {
	for _, q := range refusedTermQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := &colFakeStore{}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "The Long Halloween", "type": "dynamic", "query": q})
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.NotContains(t, w.Body.String(), "query:", "the refusal is not wrapped in a lead-in of its own")
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
			assert.NotContains(t, w.Body.String(), "query:", "the refusal is not wrapped in a lead-in of its own")
			assert.Equal(t, "title:Alpha", store.cols[0].Query, "the stored query is untouched")
			assert.Zero(t, store.updates)
		})
	}

	t.Run("the message names the valid statuses", func(t *testing.T) {
		rig := newViewerRig(t, &colFakeStore{}, viewerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
			map[string]any{"name": "Year One", "type": "dynamic", "query": "read_status:completed"})
		require.Equal(t, http.StatusBadRequest, w.Code)
		for _, status := range []string{"unstarted", "in_progress", "finished", "abandoned"} {
			assert.Contains(t, w.Body.String(), status)
		}
	})

	t.Run("the working spellings are accepted", func(t *testing.T) {
		for _, q := range []string{
			"read_status:finished", "-read_status:in_progress", "read_status:(finished|abandoned)",
			"progress_pct:>50", "progress_pct:[10 TO 90]", "last_played:>-30d", "last_played:*", "-last_played:*",
			// tag: is exact, as a conjunct of the whole query
			"tag:noir", `tag:"dark-knight" title:Alpha`, "(read_status:finished tag:noir)",
		} {
			rig := newViewerRig(t, &colFakeStore{}, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "Year One", "type": "dynamic", "query": q})
			assert.Equal(t, http.StatusCreated, w.Code, "%s: %s", q, w.Body.String())
		}
	})
}

// B-alpha / N-b: a row saved before the check reads as unavailable with its own
// kind and the same guidance the person would get on save, and logs as
// query-invalid. The cause names the shape, never the viewer.
func TestCollections_AStoredValueTheEvaluatorRefusesReadsAsUnavailableWithGuidance(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"read_status:completed", "read_status must be one of"},
		{"-read_status:completed", "read_status must be one of"},
		{"progress_pct:>half", "progress_pct takes a number"},
		{"read_status:finished OR title:Alpha", "cannot be combined"},
		// A stored query that is not per-viewer serves its stored list and is judged when
		// it is refreshed; one that is per-viewer is evaluated on every read, so a tag:
		// term it cannot apply exactly reads as unavailable with the sentence from save.
		{"read_status:finished (title:Alpha OR tag:noir)", "tag: cannot be used inside an OR group"},
		{"read_status:finished -tag:noir", "tag: cannot be used inside an OR group"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{perViewerRow("c1", tc.query, "b1")}}
			rig := newFailureRig(t, store, &countingEval{colFakeBooks: colFakeBooks{states: viewerStates()}}, true)

			got := decodeViewerResponse(t, rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice", nil))
			assert.True(t, got.MembersUnavailable)
			assert.Empty(t, got.Members, "neither the stored list nor a wrong answer")
			assert.Contains(t, got.Message, tc.want, "the person is told what to fix")
			assert.NotContains(t, got.Message, "query:", "no wrapper lead-in is stacked in front of the reason")
			require.NotEmpty(t, rig.warns())
			assert.Contains(t, rig.warns()[0], "query-invalid")
		})
	}
}

// S-C: sort_json is checked when a collection is saved, because a spec that does not
// parse fails every evaluation of two or more books, for every viewer.
func TestCollections_SortJSONIsValidatedOnCreateAndUpdate(t *testing.T) {
	const good = `[{"field":"title","direction":"asc"}]`
	t.Run("create refuses a spec that is not a JSON array of sorts", func(t *testing.T) {
		for _, bad := range []string{"title", `{"field":"title"}`, `[{"field":"title","direction":`} {
			store := &colFakeStore{}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "Knightfall", "type": "dynamic", "query": "title:Alpha", "sort_json": bad})
			assert.Equal(t, http.StatusBadRequest, w.Code, "%q: %s", bad, w.Body.String())
			assert.Contains(t, w.Body.String(), "sort_json")
			assert.Nil(t, store.created)
		}
	})
	t.Run("create accepts a valid spec", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
			map[string]any{"name": "Knightfall", "type": "dynamic", "query": "title:Alpha", "sort_json": good})
		assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	})
	t.Run("update refuses a changed bad spec and keeps the stored one", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:Alpha", SortJSON: good,
		}}}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"sort_json": "title"})
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Equal(t, good, store.cols[0].SortJSON)
	})
	t.Run("update does not refuse a bad spec it was sent back unchanged", func(t *testing.T) {
		// Saved before sort_json was validated: a rename must still go through.
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:Alpha", SortJSON: "title",
		}}}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
			map[string]any{"name": "Hush II", "sort_json": "title"})
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
	t.Run("update accepts a changed valid spec", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:Alpha",
		}}}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"sort_json": good})
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, good, store.cols[0].SortJSON)
	})
}

// N-g: a refresh the caller cancelled is not a failure and is not logged. The
// context reports itself live to the loop that dispatches the collection and
// cancelled to everything after it, which is what a cancel between the two does.
type cancelAfterDispatchCtx struct {
	context.Context
	calls atomic.Int64
}

func (c *cancelAfterDispatchCtx) Err() error {
	if c.calls.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

func TestRefreshRelativeDynamic_ACancelledRunIsNeitherFailedNorLogged(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "rel-7d", Name: "Year One", Type: database.CollectionTypeDynamic, Query: "date_added:>-7d",
		CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}, Version: 1,
	}}}
	h := newRefreshHandler(t, store)
	rec := &warnRecorder{}
	handlers.SetCollectionLogger(h, rec)

	res, err := h.RefreshRelativeDynamic(&cancelAfterDispatchCtx{Context: context.Background()}, false, nil)

	require.ErrorIs(t, err, context.Canceled, "the run returns the cancellation")
	assert.Equal(t, 1, res.Considered)
	assert.Zero(t, res.Failed, "a cancelled evaluation is not a failed one")
	assert.Zero(t, res.Refreshed)
	assert.Empty(t, rec.lines, "and it is not a warning")
	assert.Equal(t, []string{"b9"}, store.cols[0].MaterializedBookIDs, "nothing is stored")
	assert.Zero(t, store.updates)
}

func playlistRequest(t *testing.T, method, path, userID string, body any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := newPlaylistCtxAs(method, path, userID)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	return c, w
}

// B-alpha for smart playlists: the same values are refused on create and update.
func TestPlaylists_RefuseAPerUserValueTheEvaluatorCannotEvaluate(t *testing.T) {
	for _, q := range refusedTermQueries {
		t.Run("create "+q, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t) // no call may reach the store
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			c, w := playlistRequest(t, http.MethodPost, "/playlists", "alice",
				map[string]any{"name": "Hush", "type": "smart", "query": q})
			h.CreatePlaylist(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
		t.Run("update "+q, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t) // the row is read, never written
			store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
				ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart, Query: "title:Alpha", CreatedByUserID: "alice",
			}, nil)
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			c, w := playlistRequest(t, http.MethodPut, "/playlists/pl-1", "alice", map[string]any{"query": q})
			h.UpdatePlaylist(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
	}
}

// S-B: a stored smart playlist whose query is refused is not a bad request. The
// playlist is fine and its answer is unavailable, so it reads as no members with
// the reason, and the list cached by the old evaluation is dropped (the ABS surface
// serves that cache).
func TestPlaylists_AStoredRefusedQueryReadsAsUnavailableAndClearsItsCache(t *testing.T) {
	for _, q := range []string{"read_status:completed", "read_status:finished OR title:Alpha", "title:Alpha OR tag:noir"} {
		t.Run(q, func(t *testing.T) {
			pl := &database.UserPlaylist{ID: "pl-1", Name: "No Man's Land", Type: database.UserPlaylistTypeSmart,
				Query: q, CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1", "b2"}, MaterializedTotal: 2}
			store := handlersmocks.NewMockPlaylistStore(t)
			store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
			var saved *database.UserPlaylist
			store.EXPECT().UpdateUserPlaylist(pl).RunAndReturn(func(p *database.UserPlaylist) error {
				cp := *p
				saved = &cp
				return nil
			})
			// A nil index: the refusal is about the query and comes first, so the
			// closed index does not turn it into "try again shortly".
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1", "alice")
			c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
			h.GetPlaylist(c)

			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			var env struct {
				Data struct {
					BookIDs            []string `json:"book_ids"`
					MembersUnavailable bool     `json:"members_unavailable"`
					Message            string   `json:"members_unavailable_message"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
			assert.Empty(t, env.Data.BookIDs)
			assert.True(t, env.Data.MembersUnavailable)
			assert.Contains(t, env.Data.Message, "Edit the query")
			assert.NotContains(t, env.Data.Message, "query:", "the refusal is not wrapped twice")
			require.NotNil(t, saved, "the cached list is cleared")
			assert.Empty(t, saved.MaterializedBookIDs)
			assert.Zero(t, saved.MaterializedTotal)
		})
	}
}

// S-A: a snapshot of a smart playlist is a static list that nothing downstream
// cuts, so it is cut at the bound and keeps the true total.
func TestPlaylists_ASnapshotIsCutToTheBoundAndReportsTheTotal(t *testing.T) {
	over := database.MaxMaterializedMembers + 300
	idx, err := search.Open(t.TempDir() + "/bleve")
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch(syntheticDocs(over)))

	src := &database.UserPlaylist{ID: "pl-1", Name: "No Man's Land", Type: database.UserPlaylistTypeSmart,
		Query: "Synthetic", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(src, nil)
	var snap *database.UserPlaylist
	store.EXPECT().CreateUserPlaylist(mock.Anything).RunAndReturn(func(p *database.UserPlaylist) (*database.UserPlaylist, error) {
		cp := *p
		snap = &cp
		return &cp, nil
	})

	h := handlers.NewPlaylistHandler(store, func() *search.BleveIndex { return idx })
	c, w := newPlaylistCtxAs(http.MethodPost, "/playlists/pl-1/materialize", "alice")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.MaterializePlaylist(c)

	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	require.NotNil(t, snap)
	assert.Len(t, snap.BookIDs, database.MaxMaterializedMembers)
	assert.Equal(t, over, snap.MaterializedTotal)
	assert.Contains(t, snap.Description, "matched 10300 books")
}

// S-A: a snapshot under the bound is whole and claims no total.
func TestPlaylists_ASnapshotUnderTheBoundIsWhole(t *testing.T) {
	idx, err := search.Open(t.TempDir() + "/bleve")
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch(syntheticDocs(5)))

	src := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "Synthetic", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(src, nil)
	var snap *database.UserPlaylist
	store.EXPECT().CreateUserPlaylist(mock.Anything).RunAndReturn(func(p *database.UserPlaylist) (*database.UserPlaylist, error) {
		cp := *p
		snap = &cp
		return &cp, nil
	})
	h := handlers.NewPlaylistHandler(store, func() *search.BleveIndex { return idx })
	c, w := newPlaylistCtxAs(http.MethodPost, "/playlists/pl-1/materialize", "alice")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.MaterializePlaylist(c)

	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	require.NotNil(t, snap)
	assert.Len(t, snap.BookIDs, 5)
	assert.Zero(t, snap.MaterializedTotal)
	assert.NotContains(t, snap.Description, "matched")
}

// N-h: a client that resends the stored query with a rename is not refused for a
// query it did not touch (a row saved before the check existed stays renamable).
func TestPlaylists_UpdateValidatesTheQueryOnlyWhenItChanged(t *testing.T) {
	pl := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "read_status:completed", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
	store.EXPECT().UpdateUserPlaylist(pl).Return(nil)
	h := handlers.NewPlaylistHandler(store, noSearchIndex)
	c, w := playlistRequest(t, http.MethodPut, "/playlists/pl-1", "alice",
		map[string]any{"name": "Hush II", "query": "read_status:completed"})
	h.UpdatePlaylist(c)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
}

// S-C: sort_json is validated for smart playlists on create and when changed.
func TestPlaylists_SortJSONIsValidatedOnCreateAndUpdate(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store := handlersmocks.NewMockPlaylistStore(t)
		h := handlers.NewPlaylistHandler(store, noSearchIndex)
		c, w := playlistRequest(t, http.MethodPost, "/playlists", "alice",
			map[string]any{"name": "Hush", "type": "smart", "query": "title:Alpha", "sort_json": "title"})
		h.CreatePlaylist(c)
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "sort_json")
	})
	t.Run("update", func(t *testing.T) {
		store := handlersmocks.NewMockPlaylistStore(t)
		store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
			ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart, Query: "title:Alpha", CreatedByUserID: "alice",
		}, nil)
		h := handlers.NewPlaylistHandler(store, noSearchIndex)
		c, w := playlistRequest(t, http.MethodPut, "/playlists/pl-1", "alice", map[string]any{"sort_json": "title"})
		h.UpdatePlaylist(c)
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})
}
