// file: internal/server/series_rename_ops_test.go
// version: 1.2.0
// guid: 9e4b675d-617b-4d69-ac2c-756c396eeb37
// last-edited: 2026-09-12

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeSeriesRenameRegistry records the one EnqueueOp call the series rename
// handlers make. The whitebox handler tests in handlers_unit_test.go run on a
// Server whose opRegistry is nil.
type fakeSeriesRenameRegistry struct {
	defID  string
	params any
}

func (f *fakeSeriesRenameRegistry) EnqueueOp(_ context.Context, defID string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
	f.defID = defID
	f.params = params
	return "op-series-rename", nil
}

// newEntitiesHandlerWithRegistry is newEntitiesHandler with an injected
// registry.
func newEntitiesHandlerWithRegistry(s *Server, reg entities.OperationsRegistry) *entities.Handler {
	return entities.New(
		s.storeForWiring(),
		s.workService,
		s.authorSeriesService,
		reg,
		s.authorsCache,
		s.seriesCache,
		s.dedupCache,
		func(b []database.Book) []any { return make([]any, len(b)) },
	)
}

// seriesRenameOpReporter is relinkOpReporter plus the op id the registry's
// real reporter exposes, so the journal rows land under a known id.
type seriesRenameOpReporter struct {
	relinkOpReporter
	id string
}

func (r *seriesRenameOpReporter) OpID() string { return r.id }

func newSeriesRenameOpServer(st database.Store) *Server {
	return &Server{
		store:       st,
		dedupCache:  cache.New[gin.H]("series-rename-dedup", time.Hour),
		seriesCache: cache.New[*audiobookspkg.SeriesWithCountsResponse]("series-rename-series", time.Hour),
	}
}

// runSeriesRenameOp drives the registered op, as the registry would, under opID.
func runSeriesRenameOp(t *testing.T, st database.Store, opID string, seriesID int, name string) error {
	t.Helper()
	s := newSeriesRenameOpServer(st)
	def := relinkOpDef(t, s.RegisterSeriesRenameOp, "entities.series-rename")
	params, err := json.Marshal(seriesRenameOpParams{SeriesID: seriesID, Name: name})
	require.NoError(t, err)
	return def.Run(context.Background(), params, &seriesRenameOpReporter{id: opID})
}

func seriesName(t *testing.T, st database.Store, id int) string {
	t.Helper()
	s, err := st.GetSeriesByID(id)
	require.NoError(t, err)
	require.NotNil(t, s)
	return s.Name
}

// The op renames the series and journals exactly one series_rename row
// carrying the series id and the old and new names.
func TestSeriesRenameOp_RenamesAndJournals(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Old Name", nil)
	require.NoError(t, err)

	require.NoError(t, runSeriesRenameOp(t, st, "op-rename", s.ID, "New Name"))
	require.Equal(t, "New Name", seriesName(t, st, s.ID))

	changes, err := st.GetOperationChanges("op-rename")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	c := changes[0]
	require.Equal(t, undo.ChangeTypeSeriesRename, c.ChangeType)
	require.Equal(t, "series_name", c.FieldName)
	require.NotNil(t, c.SeriesID)
	require.Equal(t, s.ID, *c.SeriesID)
	require.Equal(t, "Old Name", c.OldValue)
	require.Equal(t, "New Name", c.NewValue)
	require.Empty(t, c.BookID)
}

// Undo of the op restores the old name, index included.
func TestSeriesRenameOp_UndoRestoresOldName(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Old Name", nil)
	require.NoError(t, err)
	require.NoError(t, runSeriesRenameOp(t, st, "op-undo", s.ID, "New Name"))

	res, err := audiobookspkg.NewRevertService(st).RevertOperation("op-undo")
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored)
	require.Equal(t, "Old Name", seriesName(t, st, s.ID))
	byName, err := st.GetSeriesByName("Old Name", nil)
	require.NoError(t, err)
	require.NotNil(t, byName)
	require.Equal(t, s.ID, byName.ID)
}

// A later manual rename is not clobbered: the revert refuses the row, the
// preflight reports it as renamed-since, and the manual name stays.
func TestSeriesRenameOp_UndoAfterLaterRenameRefuses(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Old Name", nil)
	require.NoError(t, err)
	require.NoError(t, runSeriesRenameOp(t, st, "op-later", s.ID, "New Name"))
	require.NoError(t, st.UpdateSeriesName(s.ID, "Manual Name"))

	changes, err := st.GetOperationChanges("op-later")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	var ref *undo.ReferentError
	require.ErrorAs(t, undo.CheckRestoreReferent(st, changes[0]), &ref)
	require.Equal(t, undo.ReasonSeriesRenamedSince, ref.Reason)

	res, err := audiobookspkg.NewRevertService(st).RevertOperation("op-later")
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, 0, res.Restored)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, "Manual Name", seriesName(t, st, s.ID))
}

// A series already back on the old name counts as restored with nothing
// written, in the preflight check and in the revert.
func TestSeriesRenameOp_UndoWhenAlreadyOldCountsRestored(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Old Name", nil)
	require.NoError(t, err)
	require.NoError(t, runSeriesRenameOp(t, st, "op-already", s.ID, "New Name"))
	require.NoError(t, st.UpdateSeriesName(s.ID, "Old Name"))

	changes, err := st.GetOperationChanges("op-already")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.True(t, errors.Is(undo.CheckRestoreReferent(st, changes[0]), undo.ErrAlreadyRestored))

	// The preflight the Activity Log shows first counts the row Safe, not as a
	// renamed-since conflict.
	report, err := undo.PreflightUndoConflicts(st, "op-already")
	require.NoError(t, err)
	require.Equal(t, 1, report.Safe)
	require.Empty(t, report.SeriesRenamedSince)

	res, err := audiobookspkg.NewRevertService(st).RevertOperation("op-already")
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, "Old Name", seriesName(t, st, s.ID))

	// Marked reverted, so a second revert reports "already reverted".
	after, err := st.GetOperationChanges("op-already")
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.NotNil(t, after[0].RevertedAt)
}

// Collisions behave as the synchronous handlers did: UpdateSeriesName does
// not refuse a name another series under the same author holds, so neither
// does the op.
func TestSeriesRenameOp_CollisionRenamesAsBefore(t *testing.T) {
	st := newRelinkOpStore(t)
	a, err := st.CreateSeries("Alpha", nil)
	require.NoError(t, err)
	b, err := st.CreateSeries("Beta", nil)
	require.NoError(t, err)

	require.NoError(t, runSeriesRenameOp(t, st, "op-collide", b.ID, "Alpha"))
	require.Equal(t, "Alpha", seriesName(t, st, b.ID))
	require.Equal(t, "Alpha", seriesName(t, st, a.ID))
	changes, err := st.GetOperationChanges("op-collide")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "Beta", changes[0].OldValue)
}

// Renaming to the current name writes nothing and journals nothing.
func TestSeriesRenameOp_SameNameIsNoOp(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Same", nil)
	require.NoError(t, err)
	require.NoError(t, runSeriesRenameOp(t, st, "op-same", s.ID, "Same"))
	changes, err := st.GetOperationChanges("op-same")
	require.NoError(t, err)
	require.Empty(t, changes)
}

// An unknown series fails the op without journaling.
func TestSeriesRenameOp_UnknownSeriesFails(t *testing.T) {
	st := newRelinkOpStore(t)
	require.Error(t, runSeriesRenameOp(t, st, "op-missing", 424242, "Name"))
	changes, err := st.GetOperationChanges("op-missing")
	require.NoError(t, err)
	require.Empty(t, changes)
}

// interferingSeriesStore lets a test rename the series from "another writer"
// in the window between the op's read and its write: interfere runs right
// after each journal row lands, with the 1-based attempt number.
type interferingSeriesStore struct {
	*database.PebbleStore
	attempts  int
	interfere func(attempt int)
}

func (w *interferingSeriesStore) CreateOperationChange(c *database.OperationChange) error {
	if err := w.PebbleStore.CreateOperationChange(c); err != nil {
		return err
	}
	w.attempts++
	if w.interfere != nil {
		w.interfere(w.attempts)
	}
	return nil
}

// Another writer renames the series between the op's read and its write. The
// compare-and-set refuses the stale write, the op retries against the name
// the other writer set, and the live journal row records THAT name. The
// refused attempt's row is retired, so the undo restores the other writer's
// name rather than the stale one.
func TestSeriesRenameOp_ConcurrentRenameJournalsReplacedName(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Foo", nil)
	require.NoError(t, err)
	w := &interferingSeriesStore{PebbleStore: st, interfere: func(attempt int) {
		if attempt == 1 {
			require.NoError(t, st.UpdateSeriesName(s.ID, "Foo Bar"))
		}
	}}

	changed, err := runSeriesRename(w, "op-race", s.ID, "New")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 2, w.attempts)
	require.Equal(t, "New", seriesName(t, st, s.ID))

	changes, err := st.GetOperationChanges("op-race")
	require.NoError(t, err)
	require.Len(t, changes, 2)
	var live []*database.OperationChange
	for _, c := range changes {
		if c.RevertedAt == nil {
			live = append(live, c)
		} else {
			require.Equal(t, "Foo", c.OldValue, "only the refused attempt's row is retired")
		}
	}
	require.Len(t, live, 1)
	require.Equal(t, "Foo Bar", live[0].OldValue)
	require.Equal(t, "New", live[0].NewValue)

	report, err := undo.PreflightUndoConflicts(st, "op-race")
	require.NoError(t, err)
	require.Equal(t, 1, report.Safe)
	require.Empty(t, report.SeriesRenamedSince)

	res, err := audiobookspkg.NewRevertService(st).RevertOperation("op-race")
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, "Foo Bar", seriesName(t, st, s.ID))
}

// A writer that renames the series on every attempt exhausts the bounded
// retries: the op fails, its rename never lands, and every row it journaled
// is retired, so an undo has nothing to act on.
func TestSeriesRenameOp_ConcurrentRenameRetriesAreBounded(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Foo", nil)
	require.NoError(t, err)
	w := &interferingSeriesStore{PebbleStore: st, interfere: func(attempt int) {
		require.NoError(t, st.UpdateSeriesName(s.ID, fmt.Sprintf("Other %d", attempt)))
	}}

	changed, err := runSeriesRename(w, "op-busy", s.ID, "New")
	require.ErrorIs(t, err, database.ErrRenameSeriesRenamedSince)
	require.False(t, changed)
	require.Equal(t, seriesRenameMaxAttempts, w.attempts)
	require.Equal(t, fmt.Sprintf("Other %d", seriesRenameMaxAttempts), seriesName(t, st, s.ID))

	changes, err := st.GetOperationChanges("op-busy")
	require.NoError(t, err)
	require.Len(t, changes, seriesRenameMaxAttempts)
	for _, c := range changes {
		require.NotNil(t, c.RevertedAt, "row %s (old %q) must be retired", c.ID, c.OldValue)
	}
}

// RenameSeriesIfCurrent refuses a series that is no longer named
// expectCurrent, and otherwise keeps UpdateSeriesName's collision behaviour.
func TestRenameSeriesIfCurrent_ChecksNameKeepsCollisions(t *testing.T) {
	st := newRelinkOpStore(t)
	a, err := st.CreateSeries("Alpha", nil)
	require.NoError(t, err)
	b, err := st.CreateSeries("Beta", nil)
	require.NoError(t, err)

	require.ErrorIs(t, st.RenameSeriesIfCurrent(b.ID, "Wrong", "Gamma"), database.ErrRenameSeriesRenamedSince)
	require.Equal(t, "Beta", seriesName(t, st, b.ID))
	require.ErrorIs(t, st.RenameSeriesIfCurrent(424242, "x", "y"), database.ErrRenameSeriesNotFound)

	require.NoError(t, st.RenameSeriesIfCurrent(b.ID, "Beta", "Alpha"))
	require.Equal(t, "Alpha", seriesName(t, st, b.ID))
	require.Equal(t, "Alpha", seriesName(t, st, a.ID))
}

// Undo through the operations handler's revert drops the series caches, so
// GET /series shows the restored name straight away instead of the cached
// renamed one.
func TestSeriesRenameOp_UndoRefreshesSeriesList(t *testing.T) {
	st := newRelinkOpStore(t)
	s, err := st.CreateSeries("Old Name", nil)
	require.NoError(t, err)
	srv := newSeriesRenameOpServer(st)
	srv.authorSeriesService = audiobookspkg.NewAuthorSeriesService(st)
	h := newEntitiesHandlerWithRegistry(srv, &fakeSeriesRenameRegistry{})
	list := func() string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/series", nil)
		h.ListSeries(c)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	def := relinkOpDef(t, srv.RegisterSeriesRenameOp, seriesRenameOpID)
	params, err := json.Marshal(seriesRenameOpParams{SeriesID: s.ID, Name: "New Name"})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), params, &seriesRenameOpReporter{id: "op-list"}))

	require.Contains(t, list(), "New Name") // primes the "all" cache entry

	_, err = srv.revertOperation("op-list")
	require.NoError(t, err)
	body := list()
	require.Contains(t, body, "Old Name")
	require.NotContains(t, body, "New Name")
}

// The three ops that rename series declare the series write-set, so the
// dispatcher's write-set gate never runs two of them at once.
func TestSeriesWriters_DeclareSeriesWriteSet(t *testing.T) {
	s := newSeriesRenameOpServer(newRelinkOpStore(t))
	for _, tc := range []struct {
		id       string
		register func(*opsregistry.Registry) error
	}{
		{seriesRenameOpID, s.RegisterSeriesRenameOp},
		{"dedup.series-merge", s.RegisterSeriesMergeOp},
		{"dedup.series-normalize", s.RegisterSeriesNormalizeOp},
	} {
		def := relinkOpDef(t, tc.register, tc.id)
		require.True(t, slices.Contains(def.Writes, opsregistry.ResSeries), "%s must declare Writes ResSeries", tc.id)
	}
}
