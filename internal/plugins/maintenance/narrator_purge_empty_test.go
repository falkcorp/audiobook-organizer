// file: internal/plugins/maintenance/narrator_purge_empty_test.go
// version: 1.1.0
// guid: f7912f12-2468-4645-af36-7afebc4a496b
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/stretchr/testify/require"
)

// Which guard each test exercises:
//   - DryRun, ApplyDeletesOnlyZeroReference, NameHold*: the bulk unfiltered count.
//   - HoldsNarratorLinkedDuringRun, RecheckFailureAborts: the per-item re-check,
//     the load-bearing guard while a (possibly resumed) scan is running.
//   - StandDown*, NothingEligibleTakesNoStandDown: the scan stand-down, the
//     backup guard, and that it is never taken for a run that deletes nothing.
//   - Journal*: the undo-ledger row written before each delete, fail closed.
//   - RealPebble: the bulk count against a real store (trashed, non-primary,
//     orphan junction rows) plus the re-check on the apply path.

// narratorMockFixture: 1 linked, 2 junk, 3 junk but still credited in some
// book's narrator text, 4 junk.
func narratorMockFixture() ([]database.Narrator, database.NarratorRefs) {
	narrators := []database.Narrator{
		{ID: 1, Name: "Kate Reading"},
		{ID: 2, Name: "Chapter 01"},
		{ID: 3, Name: "Michael Kramer"},
		{ID: 4, Name: "Track 7"},
	}
	refs := database.NarratorRefs{
		ByID:   map[int]int{1: 3},
		ByName: map[string]int{util.NormalizeAuthor("Michael Kramer"): 2},
	}
	return narrators, refs
}

type narratorMock struct {
	store    *database.MockStore
	deleted  []int
	rechecks []int
}

func newNarratorMock(narrators []database.Narrator, refs database.NarratorRefs, live map[int]int) *narratorMock {
	m := &narratorMock{}
	m.store = &database.MockStore{
		ListNarratorsFunc:      func() ([]database.Narrator, error) { return narrators, nil },
		GetAllNarratorRefsFunc: func() (database.NarratorRefs, error) { return refs, nil },
		CountNarratorBookLinksFunc: func(id int) (int, error) {
			m.rechecks = append(m.rechecks, id)
			return live[id], nil
		},
		DeleteNarratorFunc: func(id int) error {
			m.deleted = append(m.deleted, id)
			return nil
		},
	}
	return m
}

func (m *narratorMock) plugin() *Plugin { return &Plugin{deps: fakeDeps{store: m.store}} }

func TestPurgeEmptyNarrators_DryRunDeletesNothing(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	for _, raw := range []string{"", `{"apply":false}`} {
		var params []byte
		if raw != "" {
			params = []byte(raw)
		}
		require.NoError(t, m.plugin().runPurgeEmptyNarrators(context.Background(), params, &fakeReporter{}))
	}
	require.Empty(t, m.deleted, "the default must be inert")
	require.Empty(t, m.rechecks, "a dry run has nothing to re-check")

	rep, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 4, rep.TotalNarrators)
	require.Equal(t, 1, rep.Referenced)
	require.Equal(t, 3, rep.Candidates)
	require.Equal(t, 1, rep.HeldNameInText)
	require.Equal(t, 2, rep.Eligible)
	require.Equal(t, []string{"Chapter 01", "Track 7"}, rep.Sample)
	require.Zero(t, rep.Deleted)
}

func TestPurgeEmptyNarrators_ApplyDeletesOnlyZeroReferenceNarrators(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	rep, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, []int{2, 4}, m.deleted, "only zero-link narrators whose name is in no book text")
	require.Equal(t, []int{2, 4}, m.rechecks, "every delete must be preceded by its own re-check")
	require.Equal(t, 2, rep.Deleted)
	require.Len(t, rep.HeldNameInTextSample, 1)
	require.Equal(t, narratorHeldNameInText, rep.HeldNameInTextSample[0].Reason)
	require.Equal(t, 3, rep.HeldNameInTextSample[0].NarratorID)
}

func TestPurgeEmptyNarrators_NameHoldCanBeOverriddenButNeverTouchesLinked(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	off := false
	_, err := m.plugin().purgeEmptyNarrators(context.Background(),
		purgeEmptyNarratorsParams{Apply: true, RequireNoNameMatch: &off}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, []int{2, 3, 4}, m.deleted, "narrator 1 is linked; no flag may delete it")
}

func TestPurgeEmptyNarrators_LimitTakesLowestIDs(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	_, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true, Limit: 1}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, []int{2}, m.deleted)
}

// THE PER-ITEM RE-CHECK. Narrator 2 had zero links at bulk-count time but has
// one by the time its turn comes (a metadata apply linked it mid-run). It
// must be held, counted, and named in the report with its reason.
func TestPurgeEmptyNarrators_HoldsNarratorLinkedDuringRun(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, map[int]int{2: 1})
	rep, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, []int{4}, m.deleted, "narrator 2 gained a link mid-run and must survive")
	require.Equal(t, 1, rep.HeldLinkedDuringRun)
	require.Equal(t, 1, rep.Deleted)
	require.Len(t, rep.HeldLinkedDuringRunSample, 1)
	require.Equal(t, heldBackNarrator{NarratorID: 2, Name: "Chapter 01", Reason: narratorHeldLinkedDuringRun, Books: 1},
		rep.HeldLinkedDuringRunSample[0])
	require.Contains(t, rep.summary(), "held(linked during run)=1")
}

func TestPurgeEmptyNarrators_RecheckFailureAborts(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	m.store.CountNarratorBookLinksFunc = func(int) (int, error) { return 0, errors.New("disk on fire") }
	_, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &fakeReporter{})
	require.ErrorContains(t, err, "disk on fire")
	require.Empty(t, m.deleted, "a narrator that could not be re-checked must not be deleted")
}

func TestPurgeEmptyNarrators_RefCountFailureAborts(t *testing.T) {
	narrators, _ := narratorMockFixture()
	m := newNarratorMock(narrators, database.NarratorRefs{}, nil)
	m.store.GetAllNarratorRefsFunc = func() (database.NarratorRefs, error) {
		return database.NarratorRefs{}, errors.New("undecodable book_narrators row")
	}
	for _, apply := range []bool{false, true} {
		_, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: apply}, &fakeReporter{})
		require.ErrorContains(t, err, "undecodable book_narrators row")
	}
	require.Empty(t, m.deleted)

	// A store that cannot answer at all (the capability is missing) must fail
	// the same way, not fall back to "nothing references anything".
	m.store.GetAllNarratorRefsFunc = nil
	_, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &fakeReporter{})
	require.Error(t, err)
	require.Empty(t, m.deleted)
}

// narratorStandDownDeps swaps fakeDeps' always-valid stand-down for a
// recordingScanController the test can steer.
type narratorStandDownDeps struct {
	fakeDeps
	scan *recordingScanController
}

func (d narratorStandDownDeps) AcquireScanStandDown(ctx context.Context, holder, reason string) (func(), error) {
	return d.scan.AcquireScanStandDown(ctx, holder, reason)
}
func (d narratorStandDownDeps) RenewScanStandDown(holder string) bool {
	return d.scan.RenewScanStandDown(holder)
}
func (d narratorStandDownDeps) ScanStandDownValid(holder string) bool {
	return d.scan.ScanStandDownValid(holder)
}

func TestPurgeEmptyNarrators_StandDownHeldOnApplyOnly(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	scan := &recordingScanController{renewOK: true}
	p := &Plugin{deps: narratorStandDownDeps{fakeDeps: fakeDeps{store: m.store}, scan: scan}}

	_, err := p.purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{}, &opIDReporter{id: "op-dry"})
	require.NoError(t, err)
	acq, _, _ := scan.counts()
	require.Zero(t, acq, "a dry run must not park the scanner")

	_, err = p.purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &opIDReporter{id: "op-apply"})
	require.NoError(t, err)
	acq, rel, ren := scan.counts()
	require.Equal(t, 1, acq)
	require.Equal(t, 1, rel)
	require.Equal(t, 2, ren, "lease renewed once per eligible narrator")
	require.Equal(t, "op-apply", scan.lastHolder)
	require.Equal(t, []int{2, 4}, m.deleted)
}

func TestPurgeEmptyNarrators_StandDownLostAbortsBeforeAnyDelete(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	scan := &recordingScanController{renewOK: false}
	p := &Plugin{deps: narratorStandDownDeps{fakeDeps: fakeDeps{store: m.store}, scan: scan}}

	rep, err := p.purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &opIDReporter{id: "op-lost"})
	require.ErrorIs(t, err, errNarratorPurgeStandDownLost)
	require.NotEmpty(t, rep.Aborted)
	require.Empty(t, m.deleted)
	require.Empty(t, m.rechecks)
}

// THE UNDO LEDGER. Every delete must be preceded by its own narrator_delete
// row carrying the op id and "<id>:<name>" -- the only surviving copy of the
// name once the row is gone.
func TestPurgeEmptyNarrators_JournalRowWrittenBeforeEachDelete(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	var events []string
	var rows []database.OperationChange
	m.store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		events = append(events, "journal:"+c.OldValue)
		rows = append(rows, *c)
		return nil
	}
	m.store.DeleteNarratorFunc = func(id int) error {
		m.deleted = append(m.deleted, id)
		events = append(events, "delete:"+map[int]string{2: "2:Chapter 01", 4: "4:Track 7"}[id])
		return nil
	}

	rep, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &opIDReporter{id: "op-j"})
	require.NoError(t, err)
	require.Equal(t, []string{
		"journal:2:Chapter 01", "delete:2:Chapter 01",
		"journal:4:Track 7", "delete:4:Track 7",
	}, events, "each ledger row must be written before its delete")
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, "op-j", r.OperationID)
		require.Equal(t, "narrator_delete", r.ChangeType)
		require.Equal(t, "narrator", r.FieldName)
		require.Equal(t, "purged_empty", r.NewValue)
		require.NotEmpty(t, r.ID)
	}
	require.Equal(t, 2, rep.Deleted)
	require.Zero(t, rep.JournalFailed)
}

// A ledger write that fails must skip THAT delete (fail closed) and let the
// rest of the run continue.
func TestPurgeEmptyNarrators_JournalFailureSkipsTheDelete(t *testing.T) {
	narrators, refs := narratorMockFixture()
	m := newNarratorMock(narrators, refs, nil)
	m.store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		if c.OldValue == "2:Chapter 01" {
			return errors.New("ledger unavailable")
		}
		return nil
	}

	rep, err := m.plugin().purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &opIDReporter{id: "op-j2"})
	require.NoError(t, err)
	require.Equal(t, []int{4}, m.deleted, "narrator 2 has no ledger row and must survive")
	require.Equal(t, 1, rep.JournalFailed)
	require.Equal(t, 1, rep.Deleted)
	require.Contains(t, rep.summary(), "held(journal failed)=1")
}

// An apply with nothing eligible must not park a running scan to delete
// nothing (the #3307 fix for purge-empty-authors, applied here).
func TestPurgeEmptyNarrators_NothingEligibleTakesNoStandDown(t *testing.T) {
	narrators := []database.Narrator{{ID: 1, Name: "Kate Reading"}}
	refs := database.NarratorRefs{ByID: map[int]int{1: 3}, ByName: map[string]int{}}
	m := newNarratorMock(narrators, refs, nil)
	journaled := 0
	m.store.CreateOperationChangeFunc = func(*database.OperationChange) error { journaled++; return nil }
	scan := &recordingScanController{renewOK: true}
	p := &Plugin{deps: narratorStandDownDeps{fakeDeps: fakeDeps{store: m.store}, scan: scan}}

	rep, err := p.purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &opIDReporter{id: "op-empty"})
	require.NoError(t, err)
	acq, rel, ren := scan.counts()
	require.Zero(t, acq, "nothing eligible: the scan must not be parked")
	require.Zero(t, rel)
	require.Zero(t, ren)
	require.Zero(t, rep.Eligible)
	require.Empty(t, m.deleted)
	require.Empty(t, m.rechecks)
	require.Zero(t, journaled)
}

func TestPurgeEmptyNarratorsDef_IsRegisteredAndValid(t *testing.T) {
	p := &Plugin{}
	def := p.purgeEmptyNarratorsDef()
	require.Equal(t, "maintenance.purge-empty-narrators", def.ID)
	require.Contains(t, def.Description, "DRY-RUN BY DEFAULT")
	require.NoError(t, registry.ValidateOpDef(def))
	reg := &phantomCaptureRegistry{}
	require.NoError(t, p.Register(reg))
	require.Contains(t, reg.ids, def.ID, "op must be registered in plugin.go")
}

// End to end against a real PebbleStore: the unfiltered count must hold a
// narrator linked only by a trashed book, only by a non-primary version, or
// only by a junction row whose book does not exist.
func TestPurgeEmptyNarrators_RealPebble_HoldsTrashedNonPrimaryAndOrphanLinks(t *testing.T) {
	s := newSeriesPhantomStore(t)
	mk := func(name string) *database.Narrator {
		n, err := s.CreateNarrator(name)
		require.NoError(t, err)
		return n
	}
	trashedOnly := mk("Trashed Only Narrator")
	nonPrimaryOnly := mk("Non Primary Only Narrator")
	orphanOnly := mk("Orphan Junction Narrator")
	textOnly := mk("Text Only Narrator")
	junk := mk("Track 01 Junk")

	link := func(title string, primary, trashed bool, narratorText string, n *database.Narrator) {
		b := &database.Book{
			Title:             title,
			FilePath:          "/narratorpurge/" + title,
			IsPrimaryVersion:  new(primary),
			MarkedForDeletion: new(trashed),
		}
		if narratorText != "" {
			b.Narrator = new(narratorText)
		}
		created, err := s.CreateBook(b)
		require.NoError(t, err)
		if n != nil {
			require.NoError(t, s.SetBookNarrators(created.ID, []database.BookNarrator{{NarratorID: n.ID, Role: "narrator"}}))
		}
	}
	link("trashed", true, true, "", trashedOnly)
	link("secondary", false, false, "", nonPrimaryOnly)
	link("credited", true, false, "Text Only Narrator", nil)
	require.NoError(t, s.SetBookNarrators("no-such-book", []database.BookNarrator{{NarratorID: orphanOnly.ID}}))

	p := &Plugin{deps: fakeDeps{store: s}}
	rep, err := p.purgeEmptyNarrators(context.Background(), purgeEmptyNarratorsParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Deleted)
	require.Equal(t, 1, rep.HeldNameInText)

	left, err := s.ListNarrators()
	require.NoError(t, err)
	var ids []int
	for _, n := range left {
		ids = append(ids, n.ID)
	}
	sort.Ints(ids)
	want := []int{trashedOnly.ID, nonPrimaryOnly.ID, orphanOnly.ID, textOnly.ID}
	sort.Ints(want)
	require.Equal(t, want, ids, "only the unreferenced, uncredited narrator may be deleted")

	gone, err := s.GetNarratorByName(junk.Name)
	require.NoError(t, err)
	require.Nil(t, gone, "the deleted narrator's name index entry must go too")
}
