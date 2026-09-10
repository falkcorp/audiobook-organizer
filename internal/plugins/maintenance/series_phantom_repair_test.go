// file: internal/plugins/maintenance/series_phantom_repair_test.go
// version: 1.0.0
// guid: baa66b78-fd15-4fb8-87c8-2892d296df6e
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The fixture: one real series with one book, and two PHANTOM series ids —
// 777 held by two live books and one trashed book, 888 held by one live book.
// Nothing in the store has a series row for 777 or 888, which is exactly the
// state the 2026-08-14 production measurement found 6,893 times over.
type seriesPhantomFixture struct {
	realSeriesID int
	realBookID   string
	live777      [2]string
	trashed777   string
	live888      string
}

const (
	phantomA = 777
	phantomB = 888
)

func seedSeriesPhantomFixture(t *testing.T, s *database.PebbleStore) seriesPhantomFixture {
	t.Helper()
	real, err := s.CreateSeries("Real Series", nil)
	require.NoError(t, err)

	author := 42
	mk := func(title string, seriesID int) string {
		sid := seriesID
		b, err := s.CreateBook(&database.Book{Title: title, SeriesID: &sid, AuthorID: &author})
		require.NoError(t, err)
		return b.ID
	}
	fx := seriesPhantomFixture{realSeriesID: real.ID}
	fx.realBookID = mk("On A Real Series", real.ID)
	fx.live777[0] = mk("Phantom A Book 1", phantomA)
	fx.live777[1] = mk("Phantom A Book 2", phantomA)
	fx.trashed777 = mk("Phantom A Book 3 (trashed)", phantomA)
	fx.live888 = mk("Phantom B Book 1", phantomB)

	// Trash one holder the way the app does: the MarkedForDeletion flag (what
	// bookIsSoftDeleted reads), not a delete. The timestamp alone is not trash.
	tb, err := s.GetBookByID(fx.trashed777)
	require.NoError(t, err)
	now := time.Now()
	tb.MarkedForDeletion = boolPtr(true)
	tb.MarkedForDeletionAt = &now
	_, err = s.UpdateBook(tb.ID, tb)
	require.NoError(t, err)
	return fx
}

func newSeriesPhantomStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

func seriesIDOf(t *testing.T, s *database.PebbleStore, bookID string) *int {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, b, "book %s vanished", bookID)
	return b.SeriesID
}

func requireSeriesID(t *testing.T, s *database.PebbleStore, bookID string, want int) {
	t.Helper()
	got := seriesIDOf(t, s, bookID)
	require.NotNil(t, got, "book %s lost its series_id", bookID)
	require.Equal(t, want, *got, "book %s series_id", bookID)
}

func requireFixtureUntouched(t *testing.T, s *database.PebbleStore, fx seriesPhantomFixture) {
	t.Helper()
	requireSeriesID(t, s, fx.realBookID, fx.realSeriesID)
	requireSeriesID(t, s, fx.live777[0], phantomA)
	requireSeriesID(t, s, fx.live777[1], phantomA)
	requireSeriesID(t, s, fx.trashed777, phantomA)
	requireSeriesID(t, s, fx.live888, phantomB)
	all, err := s.GetAllSeries()
	require.NoError(t, err)
	require.Len(t, all, 1, "series list must be untouched")
}

func boolPtr(b bool) *bool { return &b }

func runSeriesPhantom(t *testing.T, p *Plugin, params seriesPhantomRepairParams) (*seriesPhantomRepairResult, error) {
	t.Helper()
	return p.seriesPhantomRepair(context.Background(), params, &opIDReporter{id: "op-345"})
}

func TestSeriesPhantomRepair_ReportGroupsPhantomsByHolderCountAndWritesNothing(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}
	reportPath := filepath.Join(t.TempDir(), "phantoms.tsv")

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{ReportPath: reportPath})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Equal(t, seriesPhantomModeReport, res.Mode)
	require.True(t, res.DryRun, "the report is never a write")
	require.Equal(t, 2, res.PhantomSeries)
	require.Equal(t, 3, res.BooksLive)
	require.Equal(t, 1, res.BooksTrashed)
	require.Equal(t, 0, res.BooksUnseen)
	require.Equal(t, map[string]int{"3": 1, "1": 1}, res.PhantomsByHolderCount,
		"grouped by how many books hold each phantom id")

	require.Len(t, res.TopGroups, 2)
	require.Equal(t, phantomA, res.TopGroups[0].SeriesID, "sorted by ref count, largest first")
	require.Equal(t, 3, res.TopGroups[0].RefCount)
	require.Equal(t, 2, res.TopGroups[0].Live)
	require.Equal(t, 1, res.TopGroups[0].Trashed)
	require.Equal(t, []string{"Phantom A Book 1", "Phantom A Book 2", "Phantom A Book 3 (trashed)"}, res.TopGroups[0].SampleTitles,
		"live holders sample first, trashed last")
	require.Equal(t, phantomB, res.TopGroups[1].SeriesID)

	// The TSV carries one row per holder, with the trashed one flagged.
	require.Equal(t, reportPath, res.ReportPath)
	raw, rerr := os.ReadFile(reportPath)
	require.NoError(t, rerr)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 5, "header + 4 holder rows:\n%s", raw)
	require.Equal(t, "series_id\tref_count\tlive\ttrashed\tunseen\tbook_id\ttitle\tbook_trashed\tauthor_id", lines[0])
	require.Contains(t, string(raw), "\t"+fx.trashed777+"\tPhantom A Book 3 (trashed)\ttrue\t42\n")

	// Nothing was written: not a book, not a series, not a ledger row.
	requireFixtureUntouched(t, s, fx)
	changes, cerr := s.GetOperationChanges("op-345")
	require.NoError(t, cerr)
	require.Empty(t, changes)
}

func TestSeriesPhantomRepair_NoPhantomsIsAQuietZero(t *testing.T) {
	s := newSeriesPhantomStore(t)
	real, err := s.CreateSeries("Only Series", nil)
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "Fine", SeriesID: &real.ID})
	require.NoError(t, err)
	p := &Plugin{deps: fakeDeps{store: s}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.NoError(t, err)
	require.Equal(t, 0, res.PhantomSeries)
	require.Equal(t, 0, res.Nulled)
	requireSeriesID(t, s, mustOnlyBookID(t, s), real.ID)
}

func mustOnlyBookID(t *testing.T, s *database.PebbleStore) string {
	t.Helper()
	ids, err := s.ListBookIDs()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	return ids[0]
}

func TestSeriesPhantomRepair_UnknownModeIsRejected(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	_, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: "delete", DryRun: boolPtr(false)})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown mode "delete"`)
	requireFixtureUntouched(t, s, fx)
}

func TestSeriesPhantomRepair_NullDefaultsToDryRun(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	// mode=null with NO dry_run key: the harmless default must be the one taken.
	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull})
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Equal(t, 4, res.Nulled, "reports what it WOULD clear")
	requireFixtureUntouched(t, s, fx)
	changes, cerr := s.GetOperationChanges("op-345")
	require.NoError(t, cerr)
	require.Empty(t, changes, "a dry run writes no ledger rows either")
}

func TestSeriesPhantomRepair_NullApplyClearsEveryHolderAndJournalsEachOne(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.NoError(t, err)
	require.False(t, res.DryRun)
	require.Equal(t, 4, res.Nulled)
	require.Equal(t, 0, res.Failed)
	require.Empty(t, res.Skipped)
	require.Empty(t, res.Aborted)

	// Every holder — live AND trashed — no longer points at a phantom.
	for _, id := range []string{fx.live777[0], fx.live777[1], fx.trashed777, fx.live888} {
		require.Nil(t, seriesIDOf(t, s, id), "book %s should have been cleared", id)
	}
	// ANTI-OVER-SUPPRESSION: the book on the real series is untouched.
	requireSeriesID(t, s, fx.realBookID, fx.realSeriesID)

	// One ledger row per cleared book, replayable: old value is the phantom id.
	changes, cerr := s.GetOperationChanges("op-345")
	require.NoError(t, cerr)
	require.Len(t, changes, 4)
	byBook := map[string]*database.OperationChange{}
	for _, c := range changes {
		require.Equal(t, seriesPhantomChangeType, c.ChangeType)
		require.Equal(t, seriesPhantomFieldName, c.FieldName)
		require.Equal(t, "", c.NewValue)
		byBook[c.BookID] = c
	}
	require.Equal(t, strconv.Itoa(phantomA), byBook[fx.live777[0]].OldValue)
	require.Equal(t, strconv.Itoa(phantomA), byBook[fx.trashed777].OldValue)
	require.Equal(t, strconv.Itoa(phantomB), byBook[fx.live888].OldValue)

	// Idempotent: the report now finds nothing.
	again, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{})
	require.NoError(t, err)
	require.Equal(t, 0, again.PhantomSeries)
}

func TestSeriesPhantomRepair_NullApplyIsUndoable(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	_, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.NoError(t, err)
	require.Nil(t, seriesIDOf(t, s, fx.live777[0]), "precondition: the apply cleared it")

	// The ledger rows must be enough for internal/undo to put the ids back.
	ures, err := undo.RunUndoOperation(s, "op-345", nil, nil)
	require.NoError(t, err)
	require.Equal(t, 4, ures.Reverted, "undo errors: %v", ures.Errors)
	require.Equal(t, 0, ures.Failed, "undo errors: %v", ures.Errors)

	requireFixtureUntouched(t, s, fx)
}

// journalFailStore is a store whose undo ledger is broken. The op must then
// leave every book exactly as it found it: an edit nobody can replay is worse
// than a dangling id that survives to the next run.
//
// It embeds the CONCRETE store, not database.Store: an interface embed would
// hide GetAllSeriesBookRefCounts (a capability, not a Store method) and the op
// would refuse before ever reaching the ledger — a different, earlier guard.
type journalFailStore struct {
	*database.PebbleStore
}

func (journalFailStore) CreateOperationChange(*database.OperationChange) error {
	return errors.New("ledger unavailable")
}

func TestSeriesPhantomRepair_JournalFailureLeavesTheBookIntact(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: journalFailStore{PebbleStore: s}}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.NoError(t, err, "a per-book failure degrades the run, it does not abort it")
	require.Equal(t, 4, res.Failed)
	require.Equal(t, 0, res.Nulled)
	requireFixtureUntouched(t, s, fx)
}

func TestSeriesPhantomRepair_RespectsTheUsersSeriesLock(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	// The user locked the series field on one holder. Whatever they locked —
	// even a dangling id — is theirs to keep.
	require.NoError(t, s.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID:         fx.live777[1],
		Field:          database.FieldKeySeriesName,
		OverrideLocked: true,
	}))

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.NoError(t, err)
	require.Equal(t, 3, res.Nulled)
	require.Equal(t, map[string]int{"locked": 1}, res.Skipped)

	requireSeriesID(t, s, fx.live777[1], phantomA)
	require.Nil(t, seriesIDOf(t, s, fx.live777[0]))

	// No ledger row for a write that did not happen.
	changes, cerr := s.GetOperationChanges("op-345")
	require.NoError(t, cerr)
	require.Len(t, changes, 3)
	for _, c := range changes {
		require.NotEqual(t, fx.live777[1], c.BookID)
	}
}

func TestSeriesPhantomRepair_LimitCapsTheApplyNotTheReport(t *testing.T) {
	s := newSeriesPhantomStore(t)
	seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false), Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, res.PhantomSeries, "the census is complete regardless of limit")
	require.Equal(t, 1, res.Nulled)
	require.Equal(t, 3, res.Skipped["over_limit"])
}

func TestSeriesPhantomRepair_RecreateDryRunMintsNoSeries(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{
		Mode:  seriesPhantomModeRecreate,
		Names: map[string]string{strconv.Itoa(phantomA): "Recovered Series"},
	})
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Equal(t, 1, res.SeriesRecreated, "would create")
	require.Equal(t, 3, res.Repointed, "would repoint")
	require.Equal(t, 1, res.Skipped["no_name"], "888 has no name and is reported, not guessed")
	requireFixtureUntouched(t, s, fx)
}

func TestSeriesPhantomRepair_RecreateApplyCreatesTheSeriesAndRepointsItsHolders(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{
		Mode:   seriesPhantomModeRecreate,
		DryRun: boolPtr(false),
		Names:  map[string]string{strconv.Itoa(phantomA): "Recovered Series"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.SeriesRecreated)
	require.Equal(t, 3, res.Repointed)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, 1, res.Skipped["no_name"])

	author := 42
	created, gerr := s.GetSeriesByName("Recovered Series", &author)
	require.NoError(t, gerr)
	require.NotNil(t, created, "the series row was created under the holders' author")

	for _, id := range []string{fx.live777[0], fx.live777[1], fx.trashed777} {
		requireSeriesID(t, s, id, created.ID)
	}
	requireSeriesID(t, s, fx.live888, phantomB)
	requireSeriesID(t, s, fx.realBookID, fx.realSeriesID)

	changes, cerr := s.GetOperationChanges("op-345")
	require.NoError(t, cerr)
	require.Len(t, changes, 3)
	for _, c := range changes {
		require.Equal(t, strconv.Itoa(phantomA), c.OldValue)
		require.Equal(t, strconv.Itoa(created.ID), c.NewValue)
	}

	// 777 is gone from the census; 888 remains because nobody named it.
	again, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{})
	require.NoError(t, err)
	require.Equal(t, 1, again.PhantomSeries)
	require.Equal(t, phantomB, again.TopGroups[0].SeriesID)
}

// noSeriesRefCountStore hides the unfiltered counter. The op must refuse
// outright: the filtered count is the instrument that created the phantoms.
type noSeriesRefCountStore struct {
	database.Store
}

func TestSeriesPhantomRepair_RefusesToRunWithoutTheUnfilteredCount(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: noSeriesRefCountStore{Store: s}}}

	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRun: boolPtr(false)})
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "unfiltered reference counts")
	requireFixtureUntouched(t, s, fx)
}

func TestSeriesPhantomRepairDef_IsRegisteredAndDeclaresItsWrites(t *testing.T) {
	p := &Plugin{}
	def := p.seriesPhantomRepairDef()
	require.Equal(t, "maintenance.series-phantom-repair", def.ID)
	require.Contains(t, def.Description, "dry-run by default")
	require.Contains(t, def.Description, "Nothing is ever deleted")
	reg := &phantomCaptureRegistry{}
	require.NoError(t, p.Register(reg))
	require.Contains(t, reg.ids, def.ID, "op must be registered in plugin.go")
}

type phantomCaptureRegistry struct{ ids []string }

func (r *phantomCaptureRegistry) RegisterOp(def sdk.OperationDef) error {
	r.ids = append(r.ids, def.ID)
	return nil
}

func (r *phantomCaptureRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}
