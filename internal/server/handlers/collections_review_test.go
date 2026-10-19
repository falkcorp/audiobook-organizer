// file: internal/server/handlers/collections_review_test.go
// version: 1.1.0
// guid: b4e8c1a7-6f23-4d95-8a10-3c9d7e5f2b61
// last-edited: 2026-10-18

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// ── SF3: an evaluator is resolved only for a per-user query ─────────────────

// A query that reads nobody's state has no evaluating user, so it must neither
// depend on the owner row nor answer 503 because the install has no active admin.
func TestCollectionPlainQueryNeverNeedsAnEvaluator(t *testing.T) {
	noAdmin := []database.User{{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"}}
	for name, owner := range map[string]string{"owner gone": "deleted-user", "no owner": "", "_local": "_local"} {
		t.Run(name, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Plain", Type: database.CollectionTypeDynamic,
				Query: "title:Alpha", CreatedByUserID: owner, MaterializedBookIDs: []string{"b9"},
			}}}
			rig := newOwnerRig(t, store, noAdmin, ownerStates())

			w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			require.Equal(t, http.StatusOK, w.Code, "no admin at all, still not a 503: %s", w.Body.String())
			assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)

			store.cols[0].MaterializedBookIDs = []string{"b9"}
			w = rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w))
		})
	}

	t.Run("create works with no admin and no owner row", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newOwnerRig(t, store, noAdmin, ownerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "ghost",
			map[string]any{"name": "Plain", "type": "dynamic", "query": "title:Bravo"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("a per-user query with no evaluator is still a 503", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Per-user", Type: database.CollectionTypeDynamic,
			Query: "read_status:finished", CreatedByUserID: "deleted-user",
		}}}
		rig := newOwnerRig(t, store, noAdmin, ownerStates())
		w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	})
}

// With auth off every request is "_local", the one user whose state exists, so a
// collection it owns is evaluated as it and per-user queries work.
func TestCollectionAuthOffLocalIsTheEvaluator(t *testing.T) {
	authOff := handlers.WithAuthEnabled(func() bool { return false })
	states := map[string]*database.UserBookState{
		"_local/b1": {UserID: "_local", BookID: "b1", Status: database.UserBookStatusFinished},
		"_local/b3": {UserID: "_local", BookID: "b3", Status: database.UserBookStatusFinished},
		"alice/b2":  {UserID: "alice", BookID: "b2", Status: database.UserBookStatusFinished},
	}

	t.Run("create with and without a per-user field", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newOwnerRig(t, store, nil, states, authOff) // no user rows at all
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "",
			map[string]any{"name": "Finished", "type": "dynamic", "query": "read_status:finished"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "_local", store.cols[0].CreatedByUserID)
		assert.Equal(t, []string{"b1", "b3"}, store.cols[0].MaterializedBookIDs, "evaluated as _local")

		w = rig.send(t, http.MethodPost, "/api/v1/collections", "",
			map[string]any{"name": "Plain", "type": "dynamic", "query": "title:Alpha"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1"}, store.cols[1].MaterializedBookIDs)
	})

	t.Run("materialize and read, with and without a per-user field", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{
			{ID: "pu", Name: "Per-user", Type: database.CollectionTypeDynamic,
				Query: "read_status:finished", CreatedByUserID: "_local", MaterializedBookIDs: []string{"b9"}},
			{ID: "plain", Name: "Plain", Type: database.CollectionTypeDynamic,
				Query: "title:Bravo", CreatedByUserID: "_local", MaterializedBookIDs: []string{"b9"}},
		}}
		rig := newOwnerRig(t, store, nil, states, authOff)

		w := rig.do(t, http.MethodPost, "/api/v1/collections/pu/materialize", "")
		assert.Equal(t, []string{"b1", "b3"}, decodeMaterialized(t, w))
		assert.NotContains(t, w.Body.String(), "evaluation_blocked")
		w = rig.do(t, http.MethodPost, "/api/v1/collections/plain/materialize", "")
		assert.Equal(t, []string{"b2"}, decodeMaterialized(t, w))

		store.cols[0].MaterializedBookIDs = []string{"b9"}
		w = rig.do(t, http.MethodGet, "/api/v1/collections/pu", "")
		assert.Equal(t, []string{"b1", "b3"}, decodeMaterialized(t, w))
		assert.NotContains(t, w.Body.String(), "evaluation_blocked", "_local is an owner, not a stand-in")
	})

	t.Run("the same collection with auth on is blocked", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "pu", Name: "Per-user", Type: database.CollectionTypeDynamic,
			Query: "read_status:finished", CreatedByUserID: "_local", MaterializedBookIDs: []string{"b9"},
		}}}
		rig := newOwnerRig(t, store, ownerUsers(), states) // default: auth on
		w := rig.do(t, http.MethodPost, "/api/v1/collections/pu/materialize", "")
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
	})

	t.Run("the _local owner may change its own per-user query", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "pu", Name: "Per-user", Type: database.CollectionTypeDynamic,
			Query: "title:Alpha", CreatedByUserID: "_local",
		}}}
		rig := newOwnerRig(t, store, nil, states, authOff)
		w := rig.send(t, http.MethodPut, "/api/v1/collections/pu", "", map[string]any{"query": "read_status:finished"})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1", "b3"}, store.cols[0].MaterializedBookIDs)
	})
}

// ── SF4: adoption needs adopt_owner ─────────────────────────────────────────

func TestCollectionAdoptOwner(t *testing.T) {
	orphan := func(query string) *colFakeStore {
		return &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic,
			Query: query, CreatedByUserID: "deleted-user", MaterializedBookIDs: []string{"b9"}, Version: 1,
		}}}
	}
	t.Run("a re-send never adopts, changed or not", func(t *testing.T) {
		store := orphan("read_status:finished")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		for _, q := range []string{"read_status:finished", "read_status:in_progress", "read_status:finished title:Alpha"} {
			w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": q})
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "deleted-user", store.cols[0].CreatedByUserID, "query %q must not adopt", q)
		}
	})

	t.Run("the first active admin adopts with the flag", func(t *testing.T) {
		store := orphan("read_status:finished")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
			map[string]any{"query": "read_status:finished", "adopt_owner": true})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "alice", store.cols[0].CreatedByUserID)
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs, "evaluated as alice")
		assert.NotContains(t, w.Body.String(), "evaluation_blocked")
	})

	t.Run("anyone else is refused, even with the flag", func(t *testing.T) {
		store := orphan("read_status:finished")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob",
			map[string]any{"query": "read_status:finished", "adopt_owner": true})
		assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "deleted-user", store.cols[0].CreatedByUserID)
		assert.Zero(t, store.updates)
	})

	t.Run("the flag on a collection that has an owner is a conflict", func(t *testing.T) {
		store := orphan("read_status:finished")
		store.cols[0].CreatedByUserID = "bob"
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
			map[string]any{"query": "read_status:finished", "adopt_owner": true})
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "bob", store.cols[0].CreatedByUserID)
	})

	t.Run("the flag needs a per-user query", func(t *testing.T) {
		store := orphan("read_status:finished")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"adopt_owner": true})
		assert.Equal(t, http.StatusBadRequest, w.Code, "no query: %s", w.Body.String())
		w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
			map[string]any{"query": "title:Alpha", "adopt_owner": true})
		assert.Equal(t, http.StatusBadRequest, w.Code, "no per-user field: %s", w.Body.String())
		assert.Equal(t, "deleted-user", store.cols[0].CreatedByUserID)
	})
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
		h := handlers.NewCollectionHandler(store, store, nil, &colFakeBooks{}, nil)
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

// ── NIT3: the owner-fallback warning is once per collection ─────────────────

type warnRecorder struct{ lines []string }

func (w *warnRecorder) Warn(msg string, args ...any) {
	w.lines = append(w.lines, fmt.Sprintf(msg, args...))
}

func TestCollectionOwnerFallbackWarnsOncePerCollection(t *testing.T) {
	rec := &warnRecorder{}
	store := &colFakeStore{cols: []database.Collection{
		{ID: "c1", Name: "One", Type: database.CollectionTypeDynamic, Query: "read_status:finished", CreatedByUserID: "gone"},
		{ID: "c2", Name: "Two", Type: database.CollectionTypeDynamic, Query: "read_status:finished", CreatedByUserID: "gone"},
	}}
	rig := newOwnerRig(t, store, ownerUsers(), ownerStates(), handlers.WithCollectionLogger(rec))

	for i := 0; i < 4; i++ {
		rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
		rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
	}
	require.Len(t, rec.lines, 1, "eight evaluations of one collection, one warning: %v", rec.lines)
	assert.Contains(t, rec.lines[0], "c1")

	rig.do(t, http.MethodGet, "/api/v1/collections/c2", "bob")
	require.Len(t, rec.lines, 2, "a second collection warns for itself")
	assert.Contains(t, rec.lines[1], "c2")
}

// A create has no id yet, and the gate resolves an evaluator for it; the fallback
// line would name an empty collection. Auth off is the case that reached it: the
// creator is "_local", which with auth on is a stand-in.
func TestCollectionCreateNeverWarnsWithAnEmptyID(t *testing.T) {
	for name, opts := range map[string][]handlers.CollectionOption{
		"auth on":  nil,
		"auth off": {handlers.WithAuthEnabled(func() bool { return false })},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &warnRecorder{}
			store := &colFakeStore{}
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates(),
				append(opts, handlers.WithCollectionLogger(rec))...)
			rig.send(t, http.MethodPost, "/api/v1/collections", "",
				map[string]any{"name": "Finished", "type": "dynamic", "query": "read_status:finished"})
			rig.send(t, http.MethodPost, "/api/v1/collections", "ghost",
				map[string]any{"name": "Ghost", "type": "dynamic", "query": "read_status:finished"})
			assert.Empty(t, rec.lines, "no fallback line for a create")
		})
	}
}

// ── NIT4: a PUT on a blocked orphan says it is blocked ──────────────────────

func TestCollectionPutOnBlockedOrphanReportsTheFlag(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic, Query: "read_status:finished",
		CreatedByUserID: "deleted-user", MaterializedBookIDs: []string{"b9"}, Version: 1,
	}}}
	rig := newOwnerRig(t, store, ownerUsers(), ownerStates())

	w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob", map[string]any{"limit": 5})
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, handlers.ErrCodeCollectionOwnerMissingPerUserQuery, body.Data["evaluation_blocked"])
	assert.EqualValues(t, 5, body.Data["limit"], "the edit itself was saved")
	assert.Equal(t, []any{"b9"}, body.Data["materialized_book_ids"], "the members were left alone")
	assert.Equal(t, 5, store.cols[0].Limit)

	// Sort is the other shaping field.
	w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob", map[string]any{"sort_json": `[{"field":"title"}]`})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "evaluation_blocked")

	// A collection that is not blocked has no flag.
	store.cols[0].CreatedByUserID = "alice"
	w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob", map[string]any{"limit": 6})
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "evaluation_blocked")
}

// ── SF6: the scheduled refresh of relative-date collections ─────────────────

func refreshStates() map[string]*database.UserBookState {
	now := time.Now()
	return map[string]*database.UserBookState{
		"alice/b1": {UserID: "alice", BookID: "b1", LastActivityAt: now.AddDate(0, 0, -10)},
		"alice/b2": {UserID: "alice", BookID: "b2", LastActivityAt: now.AddDate(0, 0, -40)},
		"bob/b3":   {UserID: "bob", BookID: "b3", LastActivityAt: now.AddDate(0, 0, -1)},
	}
}

func refreshStore() *colFakeStore {
	dyn := func(id, query, owner string) database.Collection {
		return database.Collection{ID: id, Name: id, Type: database.CollectionTypeDynamic, Query: query,
			CreatedByUserID: owner, MaterializedBookIDs: []string{"b9"}, Version: 1}
	}
	return &colFakeStore{cols: []database.Collection{
		dyn("rel-alice", "last_played:>-30d", "alice"),
		dyn("rel-bob", "last_played:>-30d", "bob"),
		dyn("orphan", "last_played:>-30d", "deleted-user"),
		dyn("no-date", "title:Alpha", "alice"),
		dyn("absolute-date", "last_played:>2020-01-01", "alice"),
		{ID: "static", Name: "static", Type: database.CollectionTypeStatic, BookIDs: []string{"b9"}, Version: 1},
	}}
}

func TestRefreshRelativeDynamic(t *testing.T) {
	t.Run("re-materializes only the relative-date collections, as their owners", func(t *testing.T) {
		store := refreshStore()
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())

		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, Refreshed: 2, Blocked: 1}, res)

		byID := map[string]database.Collection{}
		for _, c := range store.cols {
			byID[c.ID] = c
		}
		assert.Equal(t, []string{"b1"}, byID["rel-alice"].MaterializedBookIDs, "alice played b1 10 days ago and b2 40")
		assert.Equal(t, []string{"b3"}, byID["rel-bob"].MaterializedBookIDs, "as bob, not as alice")
		assert.Equal(t, []string{"b9"}, byID["orphan"].MaterializedBookIDs, "a per-user orphan keeps its members")
		assert.Equal(t, []string{"b9"}, byID["no-date"].MaterializedBookIDs, "no relative date: not touched")
		assert.Equal(t, []string{"b9"}, byID["absolute-date"].MaterializedBookIDs, "an absolute date does not slide: not touched")
		assert.Equal(t, []string{"b9"}, byID["static"].BookIDs)
		assert.Equal(t, 2, store.updates)
	})

	t.Run("a second run writes nothing when nothing moved", func(t *testing.T) {
		store := refreshStore()
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		_, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		writes := store.updates

		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, Unchanged: 2, Blocked: 1}, res)
		assert.Equal(t, writes, store.updates, "Version and UpdatedAt must not move on an idle hour")
	})

	t.Run("it follows the clock: a stale member list is replaced", func(t *testing.T) {
		store := refreshStore()
		store.cols[0].MaterializedBookIDs = []string{"b2"} // b2 was inside the window once; its play is now 40 days old
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		_, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("a dry run stores nothing and counts what would change", func(t *testing.T) {
		store := refreshStore()
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), true, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, Refreshed: 2, Blocked: 1}, res)
		assert.Zero(t, store.updates)
		assert.Equal(t, []string{"b9"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("a version conflict is skipped and counted, not an error", func(t *testing.T) {
		store := refreshStore()
		store.updateErr = fmt.Errorf("collection x: %w", database.ErrCollectionVersionConflict)
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, Conflicts: 2, Blocked: 1}, res)
		assert.Equal(t, []string{"b9"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("another write failure is counted as failed", func(t *testing.T) {
		store := refreshStore()
		store.updateErr = errors.New("disk full")
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, Failed: 2, Blocked: 1}, res)
	})

	t.Run("a closed index is a clean no-op: nothing evaluated, no error, no writes", func(t *testing.T) {
		store := refreshStore()
		gin.SetMode(gin.TestMode)
		h := handlers.NewCollectionHandler(store, nil, &colFakeUsers{list: ownerUsers()},
			&colFakeBooks{states: refreshStates()}, nil)
		res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err, "a closed index is routine, not a failure")
		assert.Equal(t, handlers.RelativeRefreshResult{Considered: 3, IndexUnavailable: true}, res)
		assert.Zero(t, store.updates)
	})

	t.Run("no relative-date collection and a closed index is still not an error", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
			Query: "title:Alpha", CreatedByUserID: "alice", Version: 1}}}
		h := handlers.NewCollectionHandler(store, nil, &colFakeUsers{list: ownerUsers()}, &colFakeBooks{}, nil)
		res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, handlers.RelativeRefreshResult{}, res)
	})

	t.Run("a collection with a plain query is evaluated with no evaluator at all", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic, Query: "date_added:>-7d",
			CreatedByUserID: "deleted-user", MaterializedBookIDs: []string{"b9"}, Version: 1,
		}}}
		noAdmin := []database.User{{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"}}
		rig := newOwnerRig(t, store, noAdmin, nil)
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, res.Considered)
		assert.Zero(t, res.Blocked+res.Failed, "an ownerless plain query is not blocked")
		assert.Equal(t, 1, res.Refreshed, "the index rows carry no date_added, so the stale b9 is replaced by nothing")
		assert.Empty(t, store.cols[0].MaterializedBookIDs)
	})

	t.Run("many collections, bounded concurrency, counts partition the work", func(t *testing.T) {
		store := &colFakeStore{}
		for i := 0; i < 40; i++ {
			store.cols = append(store.cols, database.Collection{
				ID: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("c%02d", i), Type: database.CollectionTypeDynamic,
				Query: "last_played:>-30d", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}, Version: 1,
			})
		}
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
		var calls, last atomic.Int64
		res, err := rig.h.RefreshRelativeDynamic(context.Background(), false, func(done, total int) {
			calls.Add(1)
			last.Store(int64(done))
			assert.Equal(t, 40, total)
		})
		require.NoError(t, err)
		assert.Equal(t, 40, res.Considered)
		assert.Equal(t, 40, res.Refreshed+res.Unchanged+res.Blocked+res.Conflicts+res.Failed)
		assert.EqualValues(t, 40, calls.Load())
		assert.EqualValues(t, 40, last.Load(), "progress counts reach the total")
		assert.Equal(t, 40, store.updates, "each collection written exactly once")
	})

	t.Run("a cancelled context stops the run and says so", func(t *testing.T) {
		store := refreshStore()
		rig := newOwnerRig(t, store, ownerUsers(), refreshStates())
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
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			w := rig.send(t, http.MethodPost, "/api/v1/collections", "alice",
				map[string]any{"name": "X", "type": "dynamic", "query": q})
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "translate query")
			assert.Empty(t, store.cols, "nothing is stored")
			assert.Nil(t, store.created)
		})
		t.Run("update "+q, func(t *testing.T) {
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
				Query: "title:Alpha", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1"}, Version: 1,
			}}}
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": q})
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "translate query")
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
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
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

	cases := []struct {
		name string
		cols []database.Collection
		want bool
	}{
		{"none at all", nil, false},
		{"plain and absolute-date queries slide with nothing", []database.Collection{plain, abs, static}, false},
		{"one relative-date collection", []database.Collection{plain, rel}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newOwnerRig(t, &colFakeStore{cols: tc.cols}, ownerUsers(), nil)
			got, err := rig.h.RelativeRefreshPending()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("a closed index means nothing to do even with a relative-date collection", func(t *testing.T) {
		h := handlers.NewCollectionHandler(&colFakeStore{cols: []database.Collection{rel}}, nil,
			&colFakeUsers{list: ownerUsers()}, &colFakeBooks{}, nil)
		got, err := h.RelativeRefreshPending()
		require.NoError(t, err)
		assert.False(t, got)
	})
}
