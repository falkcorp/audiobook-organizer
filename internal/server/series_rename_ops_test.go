// file: internal/server/series_rename_ops_test.go
// version: 1.0.0
// guid: 9e4b675d-617b-4d69-ac2c-756c396eeb37
// last-edited: 2026-09-12

package server

import (
	"context"
	"encoding/json"
	"errors"
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

	res, err := audiobookspkg.NewRevertService(st).RevertOperation("op-already")
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, "Old Name", seriesName(t, st, s.ID))
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
