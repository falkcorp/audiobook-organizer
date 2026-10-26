// file: internal/server/handlers/collections_review_test.go
// version: 2.0.1
// guid: b4e8c1a7-6f23-4d95-8a10-3c9d7e5f2b61
// last-edited: 2026-10-26

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// ── a plain query has no viewer and no owner to depend on ───────────────────

// A query that reads nobody's state is evaluated with no user at all, so it must
// not depend on who created the collection (a deleted user, none, "_local") or on
// who is looking.
func TestCollectionPlainQueryNeverNeedsAnOwnerOrViewer(t *testing.T) {
	for name, owner := range map[string]string{"owner gone": "deleted-user", "no owner": "", "_local": "_local"} {
		t.Run(name, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Plain", Type: database.CollectionTypeDynamic,
				Query: "title:Alpha", CreatedByUserID: owner, MaterializedBookIDs: []string{"b9"},
			}}}
			rig := newViewerRig(t, store, viewerStates())

			w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)

			store.cols[0].MaterializedBookIDs = []string{"b9"}
			w = rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w))
		})
	}

	t.Run("create works with no owner row", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "ghost",
			map[string]any{"name": "Plain", "type": "dynamic", "query": "title:Bravo"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs)
	})
}

// With auth off every request is the caller "_local" (CallingUserID), the one
// person whose listening state exists, so a per-user collection is evaluated for
// them like for any other viewer, and nobody else's state is read.
func TestCollectionAuthOffLocalIsJustTheViewer(t *testing.T) {
	states := map[string]*database.UserBookState{
		"_local/b1": {UserID: "_local", BookID: "b1", Status: database.UserBookStatusFinished},
		"_local/b3": {UserID: "_local", BookID: "b3", Status: database.UserBookStatusFinished},
		"alice/b2":  {UserID: "alice", BookID: "b2", Status: database.UserBookStatusFinished},
	}
	store := &colFakeStore{}
	rig := newViewerRig(t, store, states)

	w := rig.send(t, http.MethodPost, "/api/v1/collections", "",
		map[string]any{"name": "Finished", "type": "dynamic", "query": "read_status:finished"})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	assert.ElementsMatch(t, []string{"b1", "b3"}, decodeMaterialized(t, w), "evaluated as _local")
	assert.Empty(t, store.cols[0].MaterializedBookIDs, "and not stored")

	assert.ElementsMatch(t, []string{"b1", "b3"},
		decodeMaterialized(t, rig.do(t, http.MethodGet, "/api/v1/collections/"+store.cols[0].ID, "")))
	assert.ElementsMatch(t, []string{"b2"},
		decodeMaterialized(t, rig.do(t, http.MethodGet, "/api/v1/collections/"+store.cols[0].ID, "alice")),
		"a named viewer sees their own, not _local's")
}

// ── nobody owns the right to write a per-user field any more ────────────────

// The owner gate (403) and adopt_owner existed only because the collection was
// evaluated as its owner and the answer shown to everyone. Evaluated for the
// viewer, a per-user field in a query reads only the state of whoever is looking,
// so any collections manager may write one and nothing is adopted.
func TestAnyManagerMayWriteAPerUserFieldAndOwnershipNeverMoves(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic,
		Query: "title:Alpha", CreatedByUserID: "deleted-user", Version: 1,
	}}}
	rig := newViewerRig(t, store, viewerStates())
	for _, who := range []string{"alice", "bob", "carol"} {
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", who,
			map[string]any{"query": "read_status:finished", "adopt_owner": true})
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", who, w.Body.String())
		assert.Equal(t, "deleted-user", store.cols[0].CreatedByUserID, "adopt_owner is gone: ownership never moves")
		assert.NotContains(t, w.Body.String(), "evaluation_blocked")
	}
}

// ── SF5: a whole-list PUT needs the version it read ─────────────────────────

func TestCollectionPutBookIDsRequiresVersion(t *testing.T) {
	newStore := func() *colFakeStore {
		return &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Shelf", Type: database.CollectionTypeStatic, BookIDs: []string{"b1"}, Version: 3,
		}}}
	}
	errBody := func(t *testing.T, raw []byte) map[string]any {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m), string(raw))
		return m
	}

	t.Run("missing version is a 400 naming the field", func(t *testing.T) {
		store := newStore()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{"book_ids": []string{"b1", "b2"}})
		require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		assert.Contains(t, errBody(t, w.Body.Bytes())["error"], "version")
		assert.Zero(t, store.updates)
	})

	t.Run("a stale version is a 409 carrying the current one", func(t *testing.T) {
		store := newStore()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1",
			map[string]any{"book_ids": []string{"b1", "b2"}, "version": 2})
		require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.EqualValues(t, 3, errBody(t, w.Body.Bytes())["current_version"])
		assert.Equal(t, []string{"b1"}, store.cols[0].BookIDs)
		assert.Zero(t, store.updates)
	})

	t.Run("the current version replaces the list", func(t *testing.T) {
		store := newStore()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1",
			map[string]any{"book_ids": []string{"b2", "b1"}, "version": 3})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b2", "b1"}, store.cols[0].BookIDs)
	})

	t.Run("requests without book_ids are unchanged", func(t *testing.T) {
		store := newStore()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{"name": "Renamed"})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "Renamed", store.cols[0].Name)
	})

	t.Run("an atomic add between the read and the PUT refuses the PUT", func(t *testing.T) {
		store := newStore()
		r, _ := colRouter(store)

		// The caller reads the collection (version 3, books [b1]) ...
		w := colDo(t, r, http.MethodGet, "/api/v1/collections/c1", nil)
		require.Equal(t, http.StatusOK, w.Code)
		var read struct {
			Data database.Collection `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &read))
		require.Equal(t, 3, read.Data.Version)

		// ... someone else adds b2 through the atomic route ...
		w = colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books", map[string]any{"book_ids": []string{"b2"}})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

		// ... and the caller's whole-list PUT, built from what it read, must not
		// silently drop b2.
		w = colDo(t, r, http.MethodPut, "/api/v1/collections/c1",
			map[string]any{"book_ids": []string{"b1", "b3"}, "version": read.Data.Version})
		require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.EqualValues(t, 4, errBody(t, w.Body.Bytes())["current_version"])
		assert.Equal(t, []string{"b1", "b2"}, store.cols[0].BookIDs, "b2 is still there")
	})

	t.Run("against the real store", func(t *testing.T) {
		store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/c.pebble")
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		created, err := store.CreateCollection(&database.Collection{
			Name: "Shelf", Type: database.CollectionTypeStatic, BookIDs: []string{"b1"}})
		require.NoError(t, err)
		gin.SetMode(gin.TestMode)
		h := handlers.NewCollectionHandler(store, store, &colFakeBooks{}, nil)
		r := gin.New()
		r.PUT("/collections/:id", h.UpdateCollection)
		r.POST("/collections/:id/books", h.AddBooksToCollection)

		readVersion := created.Version
		w := colDo(t, r, http.MethodPost, "/collections/"+created.ID+"/books", map[string]any{"book_ids": []string{"b2"}})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

		w = colDo(t, r, http.MethodPut, "/collections/"+created.ID,
			map[string]any{"book_ids": []string{"b1"}, "version": readVersion})
		require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		got, err := store.GetCollection(created.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"b1", "b2"}, got.BookIDs)
	})
}

// ── scheduled-refresh warning recorder ──────────────────────────────────────

type warnRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnRecorder) Warn(msg string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, fmt.Sprintf(msg, args...))
}

// ── SF6: the scheduled refresh of relative-date collections ─────────────────

// refreshStore holds two refreshable collections (relative date, no per-user
// field), a relative-date collection WITH a per-user field (never refreshed: it
// stores no list, and the stale one is left for the boot purge), and three that
// are not due. The rig indexes b1, b2, b3 as added 2, 20 and 60 days ago.
func refreshStore() *colFakeStore {
	dyn := func(id, query, owner string) database.Collection {
		return database.Collection{ID: id, Name: id, Type: database.CollectionTypeDynamic, Query: query,
			CreatedByUserID: owner, MaterializedBookIDs: []string{"b9"}, Version: 1}
	}
	return &colFakeStore{cols: []database.Collection{
		dyn("rel-7d", "date_added:>-7d", "alice"),
		dyn("rel-30d", "date_added:>-30d", "deleted-user"),
		dyn("per-user-rel", "last_played:>-30d", "alice"),
		dyn("no-date", "title:Alpha", "alice"),
		dyn("absolute-date", "date_added:>2020-01-01", "alice"),
		{ID: "static", Name: "static", Type: database.CollectionTypeStatic, BookIDs: []string{"b9"}, Version: 1},
	}}
}

func TestRefreshRelativeDynamic(t *testing.T) {
	t.Run("re-materializes only the relative-date collections without a per-user field", func(t *testing.T) {
		store := refreshStore()
		rig := newViewerRig(t, store, viewerStates())

		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, Refreshed: 2}, res)

		byID := map[string]database.Collection{}
		for _, c := range store.cols {
			byID[c.ID] = c
		}
		assert.Equal(t, []string{"b1"}, byID["rel-7d"].MaterializedBookIDs)
		assert.Equal(t, []string{"b1", "b2"}, byID["rel-30d"].MaterializedBookIDs, "an ownerless collection refreshes like any other")
		assert.Equal(t, []string{"b9"}, byID["per-user-rel"].MaterializedBookIDs,
			"a per-user collection is not refreshed: no viewer, and nothing is stored for it")
		assert.Equal(t, []string{"b9"}, byID["no-date"].MaterializedBookIDs, "no relative date: not touched")
		assert.Equal(t, []string{"b9"}, byID["absolute-date"].MaterializedBookIDs, "an absolute date does not slide: not touched")
		assert.Equal(t, []string{"b9"}, byID["static"].BookIDs)
		assert.Equal(t, 2, store.updates)
	})

	t.Run("a second run writes nothing when nothing moved", func(t *testing.T) {
		store := refreshStore()
		rig := newViewerRig(t, store, viewerStates())
		_, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		writes := store.updates

		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, Unchanged: 2}, res)
		assert.Equal(t, writes, store.updates, "Version and UpdatedAt must not move on an idle hour")
	})

	t.Run("it follows the clock: a stale member list is replaced", func(t *testing.T) {
		store := refreshStore()
		store.cols[0].MaterializedBookIDs = []string{"b2"} // b2 was added 20 days ago: outside the 7 day window
		rig := newViewerRig(t, store, viewerStates())
		_, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("a dry run stores nothing and counts what would change", func(t *testing.T) {
		store := refreshStore()
		rig := newViewerRig(t, store, viewerStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), true, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, Refreshed: 2}, res)
		assert.Zero(t, store.updates)
		assert.Equal(t, []string{"b9"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("a version conflict is skipped and counted, not an error", func(t *testing.T) {
		store := refreshStore()
		store.updateErr = fmt.Errorf("collection x: %w", database.ErrCollectionVersionConflict)
		rig := newViewerRig(t, store, viewerStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, Conflicts: 2}, res)
		assert.Equal(t, []string{"b9"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("another write failure is counted as failed", func(t *testing.T) {
		store := refreshStore()
		store.updateErr = errors.New("disk full")
		rig := newViewerRig(t, store, viewerStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, Failed: 2}, res)
	})

	t.Run("a closed index is a clean no-op: nothing evaluated, no error, no writes", func(t *testing.T) {
		store := refreshStore()
		gin.SetMode(gin.TestMode)
		rec := &warnRecorder{}
		h := handlers.NewCollectionHandler(store, nil, &colFakeBooks{states: viewerStates()}, nil)
		handlers.SetCollectionLogger(h, rec)
		res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err, "a closed index is routine, not a failure")
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 2, IndexUnavailable: true}, res)
		assert.Zero(t, store.updates)
		assert.Empty(t, rec.lines, "no warning per collection: nothing is evaluated while the index is closed")
	})

	t.Run("no relative-date collection and a closed index is still not an error", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
			Query: "title:Alpha", CreatedByUserID: "alice", Version: 1}}}
		h := handlers.NewCollectionHandler(store, nil, &colFakeBooks{}, nil)
		res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{}, res)
	})

	// A system evaluation has no viewer. The job must never store the answer of a
	// per-user collection (nothing, or someone's): it is not even considered.
	t.Run("a per-user relative-date collection is neither considered nor written", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic, Query: "last_played:>-30d",
			CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}, Version: 1,
		}}}
		rig := newViewerRig(t, store, viewerStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{}, res)
		assert.Zero(t, store.updates)
		pending, err := rig.h.RelativeRefreshPending()
		require.NoError(t, err)
		assert.False(t, pending, "and it alone gives the scheduler nothing to enqueue")
	})

	t.Run("many collections, bounded concurrency, counts partition the work", func(t *testing.T) {
		store := &colFakeStore{}
		for i := range 40 {
			store.cols = append(store.cols, database.Collection{
				ID: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("c%02d", i), Type: database.CollectionTypeDynamic,
				Query: "date_added:>-30d", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}, Version: 1,
			})
		}
		rig := newViewerRig(t, store, viewerStates())
		var calls, last atomic.Int64
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, func(done, total int) {
			calls.Add(1)
			last.Store(int64(done))
			assert.Equal(t, 40, total)
		})
		require.NoError(t, err)
		assert.Equal(t, 40, res.Considered)
		assert.Equal(t, 40, res.Refreshed+res.Unchanged+res.Conflicts+res.Failed)
		assert.EqualValues(t, 40, calls.Load())
		assert.EqualValues(t, 40, last.Load(), "progress counts reach the total")
		assert.Equal(t, 40, store.updates, "each collection written exactly once")
	})

	t.Run("a cancelled context stops the run and says so", func(t *testing.T) {
		store := refreshStore()
		rig := newViewerRig(t, store, viewerStates())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := rig.h.RefreshRelativeDynamic(ctx, false, nil)
		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, store.updates)
	})
}

// ── B1: a query that cannot be translated is a 400 before anything is stored ─

// materialize already answered 400 for these; create and PUT swallowed the same
// error (the evaluate there ignores failures so a closed index cannot block a
// save) and stored a query that could never work.
func TestCollectionMalformedQueryIsRefusedOnCreateAndUpdate(t *testing.T) {
	queries := []string{
		"last_played:>30d",
		"-last_played:>30d",
		"title:Alpha -last_played:garbage",
		"last_played:<2w",
		"last_played:(>30d|-1d)",
		"last_played:[a TO b]",
	}
	for _, q := range queries {
		t.Run("create "+q, func(t *testing.T) {
			store := &colFakeStore{}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "X", "type": "dynamic", "query": q})
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "last_played", "the message names the field that is wrong")
			assert.Empty(t, store.cols, "nothing is stored")
			assert.Nil(t, store.created)
		})
		t.Run("update "+q, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
				Query: "title:Alpha", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1"}, Version: 1,
			}}}
			rig := newViewerRig(t, store, viewerStates())
			w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": q})
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "last_played", "the message names the field that is wrong")
			assert.Equal(t, "title:Alpha", store.cols[0].Query, "the stored query is unchanged")
			assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)
			assert.Zero(t, store.updates, "nothing is written")

			w = rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "alice")
			assert.Equal(t, http.StatusOK, w.Code, "the untouched collection still works")
		})
	}

	t.Run("a closed index still lets a good query be saved", func(t *testing.T) {
		store := &colFakeStore{}
		r, _ := colRouter(store) // nil index getter: the index is closed
		w := colDo(t, r, http.MethodPost, "/api/v1/collections",
			map[string]any{"name": "X", "type": "dynamic", "query": "date_added:>-30d"})
		assert.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	})

	t.Run("a stored bad query can still be renamed", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
			Query: "last_played:>30d", CreatedByUserID: "alice", Version: 1,
		}}}
		rig := newViewerRig(t, store, viewerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"name": "renamed"})
		assert.Equal(t, http.StatusOK, w.Code, "only a CHANGED query is validated: %s", w.Body.String())
		assert.Equal(t, "renamed", store.cols[0].Name)
	})
}

// RelativeRefreshPending is the scheduler's precondition: a relative-date dynamic
// collection exists AND the index is open.
func TestRelativeRefreshPending(t *testing.T) {
	rel := database.Collection{ID: "r", Name: "r", Type: database.CollectionTypeDynamic, Query: "date_added:>-7d", CreatedByUserID: "alice"}
	plain := database.Collection{ID: "p", Name: "p", Type: database.CollectionTypeDynamic, Query: "title:Alpha", CreatedByUserID: "alice"}
	abs := database.Collection{ID: "a", Name: "a", Type: database.CollectionTypeDynamic, Query: "date_added:2026-01-01", CreatedByUserID: "alice"}
	static := database.Collection{ID: "s", Name: "s", Type: database.CollectionTypeStatic}
	perUser := database.Collection{ID: "u", Name: "u", Type: database.CollectionTypeDynamic, Query: "last_played:>-30d", CreatedByUserID: "alice"}

	cases := []struct {
		name string
		cols []database.Collection
		want bool
	}{
		{"none at all", nil, false},
		{"plain and absolute-date queries slide with nothing", []database.Collection{plain, abs, static}, false},
		{"a per-user relative date has nothing to store", []database.Collection{perUser}, false},
		{"one relative-date collection", []database.Collection{plain, perUser, rel}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newViewerRig(t, &colFakeStore{cols: tc.cols}, nil)
			got, err := rig.h.RelativeRefreshPending()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("a closed index means nothing to do even with a relative-date collection", func(t *testing.T) {
		h := handlers.NewCollectionHandler(&colFakeStore{cols: []database.Collection{rel}}, nil,
			&colFakeBooks{}, nil)
		got, err := h.RelativeRefreshPending()
		require.NoError(t, err)
		assert.False(t, got)
	})
}
