// file: internal/plugins/maintenance/fragment_attribution_test.go
// version: 1.1.0
// guid: 02ce91e2-c227-4b3c-b13d-45a89faf23c9
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// looseCut plans the seeded lib/Loose group and returns the planned row,
// its survivor, and the two other members with their planned rows.
func (f *fragFixture) looseCut(t *testing.T) (planned repairs.Row, survivor string, others, otherRows []string) {
	t.Helper()
	f.seed(t)
	res := f.plan(t, "op-plan")
	planned = findRow(t, res, noParentRowID(f.path("lib/Loose"), "loose"))
	require.True(t, planned.Applicable(), planned.SkipReason)
	survivor = planned.Proposed["survivor"]
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			others = append(others, f.ids["loose"+n])
			otherRows = append(otherRows, f.rowIDs["l"+n])
		}
	}
	return planned, survivor, others, otherRows
}

// moveAndRetire moves the members' rows onto survivor and retires the first
// n of them through w, as fixerID (a run cut off after those retires).
func (f *fragFixture) moveAndRetire(t *testing.T, w *repairs.Writer, fixerID, survivor string, others, otherRows []string, n int) {
	t.Helper()
	for i := range others {
		require.NoError(t, w.MoveBookFiles([]string{otherRows[i]}, others[i], survivor))
	}
	for i := 0; i < n; i++ {
		_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fixerID, others[i], survivor, &merge.SliceMapping{Mappable: true})
		require.NoError(t, err)
	}
}

// journalPlanRecord journals r's plan record through w, as Apply does before
// a run's first write: a run simulated step by step through w is then one of
// the row's own runs, whose flag changes a re-plan credits to the row.
func (f *fragFixture) journalPlanRecord(t *testing.T, w *repairs.Writer, r repairs.Row) {
	t.Helper()
	plan, ok := r.Detail.(*fragGroupPlan)
	require.True(t, ok, "a no-parent row")
	var st fragGroupState
	require.NoError(t, json.Unmarshal(r.State, &st))
	rec, err := planRecordOf(r, plan, st)
	require.NoError(t, err)
	require.NoError(t, w.Journal(plan.SurvivorID, undo.ChangeTypeRepairPlanRecord, fragRecordField(r.RowID), "", rec))
}

// discardOp deletes op id's row, as registry.Discard does for a failed or
// interrupted apply (PebbleStore.DeleteOperationV2): the op's journal
// (opchange rows) stays. The fixture's op rows live in planOps, the store
// the fixer's attribution reads.
func (f *fragFixture) discardOp(id string) {
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	delete(f.ops.rows, id)
}

// requireOneLiveBookHoldsAll: of the row's books, only survivor is live,
// and it holds every row the plan listed.
func (f *fragFixture) requireOneLiveBookHoldsAll(t *testing.T, planned repairs.Row, survivor string) {
	t.Helper()
	var live []string
	for _, id := range planned.BookIDs {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if !b.IsSoftDeleted() {
			live = append(live, id)
		}
	}
	require.Equal(t, []string{survivor}, live, "one work, one live book")
	var st fragGroupState
	require.NoError(t, json.Unmarshal(planned.State, &st))
	var want, got []string
	for _, fid := range st.Files {
		want = append(want, fid)
	}
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	for _, r := range rows {
		got = append(got, r.ID)
	}
	sort.Strings(want)
	sort.Strings(got)
	require.Equal(t, want, got, "the survivor holds every planned row")
}

// TestFragmentFixer_DiscardedApplyStillAttributes (review 5 blocker): an
// apply cut off after moving every row and retiring both members ends
// failed or interrupted, and Discard deletes its op row while its journal
// stays. The journal rows carry the fixer's id themselves, so the re-plan
// keeps the fingerprint and the resume finishes to one live book. Before
// the stamp, attribution read the op row: the resume refused, and a fresh
// plan split the work into two live books.
func TestFragmentFixer_DiscardedApplyStillAttributes(t *testing.T) {
	f := newFragFixture(t)
	planned, survivor, others, otherRows := f.looseCut(t)
	f.moveAndRetire(t, f.fragWriter(t, "op-cut"), fragFixerID, survivor, others, otherRows, 2)
	f.discardOp("op-cut")

	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.Equal(t, planned.Fingerprint, got.Fingerprint, "reason=%q", got.Reason)
	require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
	out := f.apply(t, "op-plan", "op-resume", []string{planned.RowID}, nil)
	require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
	f.requireOneLiveBookHoldsAll(t, planned, survivor)
}

// TestFragmentFixer_AnotherFixersSourceIsNotOurs: a journal row stamped by
// another fixer is never this fixer's, even when its op row claims to be a
// fragment-consolidation apply: the row's own Source decides.
func TestFragmentFixer_AnotherFixersSourceIsNotOurs(t *testing.T) {
	f := newFragFixture(t)
	planned, survivor, others, otherRows := f.looseCut(t)
	f.applyOp("op-x", fragFixerID) // the op row says ours
	w := repairs.NewWriter(f.s, f.s, "some-other-fixer", "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-x")
	f.moveAndRetire(t, w, "some-other-fixer", survivor, others, otherRows, 1)

	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.NotEqual(t, planned.Fingerprint, got.Fingerprint, "a changed row")
	require.Contains(t, got.Reason, "not by a "+fragFixerID+" apply")
}

// legacyJournal stores journal rows without a Source, as every writer did
// before the field existed.
type legacyJournal struct{ *database.PebbleStore }

func (l legacyJournal) CreateOperationChange(c *database.OperationChange) error {
	c.Source = ""
	return l.PebbleStore.CreateOperationChange(c)
}

// TestFragmentFixer_LegacyJournalRowsFallBackToTheOpRow: a journal row with
// no Source is attributed by its op row; with the op row gone it is not
// ours (a missing op row never is), and the row is planned again rather
// than guessed at.
func TestFragmentFixer_LegacyJournalRowsFallBackToTheOpRow(t *testing.T) {
	for _, discarded := range []bool{false, true} {
		name := "op row present: ours"
		if discarded {
			name = "op row discarded: refused"
		}
		t.Run(name, func(t *testing.T) {
			f := newFragFixture(t)
			planned, survivor, others, otherRows := f.looseCut(t)
			f.applyOp("op-legacy", fragFixerID)
			w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, legacyJournal{f.s}, "op-legacy")
			f.moveAndRetire(t, w, fragFixerID, survivor, others, otherRows, 1)
			cs, err := f.s.GetBookChanges(others[0])
			require.NoError(t, err)
			require.NotEmpty(t, cs)
			for _, c := range cs {
				require.Empty(t, c.Source, "a legacy row")
			}
			if discarded {
				f.discardOp("op-legacy")
			}
			got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			if !discarded {
				require.Equal(t, planned.Fingerprint, got.Fingerprint, "reason=%q", got.Reason)
				return
			}
			require.NotEqual(t, planned.Fingerprint, got.Fingerprint)
			require.Contains(t, got.Reason, "not by a "+fragFixerID+" apply")
		})
	}
}

// noScanHist is a FragmentRepairReader without ScanOperationChanges (and
// with no Unwrap), so loadJournal takes its per-book GetBookChanges path.
type noScanHist struct {
	s     *database.PebbleStore
	calls atomic.Int32
}

func (h *noScanHist) GetBookPathHistory(id string) ([]database.BookPathChange, error) {
	return h.s.GetBookPathHistory(id)
}

func (h *noScanHist) GetBookChanges(id string) ([]*database.OperationChange, error) {
	h.calls.Add(1)
	return h.s.GetBookChanges(id)
}

func (h *noScanHist) GetBookFileByPath(p string) (*database.BookFile, error) {
	return h.s.GetBookFileByPath(p)
}

func (h *noScanHist) BookFilesAtPath(p string) ([]database.BookFile, error) {
	return h.s.BookFilesAtPath(p)
}

// noScanDeps hands the fixer noScanHist as its FragmentRepairReader.
type noScanDeps struct {
	scanDeps
	hist FragmentRepairReader
}

func (d noScanDeps) FragmentRepairReader() FragmentRepairReader { return d.hist }

// TestFragmentFixer_JournalFallbackWithoutScanner: a store without the
// one-pass scan reads each judged book's journal with GetBookChanges, and
// the re-plan reaches the same verdicts (a resumable cut-off row keeps its
// fingerprint; another fixer's retire still refuses).
func TestFragmentFixer_JournalFallbackWithoutScanner(t *testing.T) {
	// Plan needs the one-pass scan (an interrupted run would go unseen
	// without it, so it fails closed); the swap to the per-book store is
	// made after the plan, for the re-plan this test is about.
	setup := func(t *testing.T, f *fragFixture) *noScanHist {
		h := &noScanHist{s: f.s}
		_, ok := database.AsCapability[opChangeScanner](FragmentRepairReader(h))
		require.False(t, ok, "the wrapper hides the scan")
		sd, ok := f.p.deps.(scanDeps)
		require.True(t, ok)
		f.p.deps = noScanDeps{scanDeps: sd, hist: h}
		return h
	}
	t.Run("ours resumes", func(t *testing.T) {
		f := newFragFixture(t)
		planned, survivor, others, otherRows := f.looseCut(t)
		h := setup(t, f)
		f.moveAndRetire(t, f.fragWriter(t, "op-cut"), fragFixerID, survivor, others, otherRows, 1)
		h.calls.Store(0)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		require.Equal(t, planned.Fingerprint, got.Fingerprint, "reason=%q", got.Reason)
		require.Positive(t, h.calls.Load(), "the journal came from GetBookChanges")
		out := f.apply(t, "op-plan", "op-resume", []string{planned.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		f.requireOneLiveBookHoldsAll(t, planned, survivor)
	})
	t.Run("another fixer's retire refuses", func(t *testing.T) {
		f := newFragFixture(t)
		planned, survivor, others, otherRows := f.looseCut(t)
		h := setup(t, f)
		f.applyOp("op-x", "some-other-fixer")
		w := repairs.NewWriter(f.s, f.s, "some-other-fixer", "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-x")
		f.moveAndRetire(t, w, "some-other-fixer", survivor, others, otherRows, 1)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		require.Positive(t, h.calls.Load())
		require.NotEqual(t, planned.Fingerprint, got.Fingerprint)
		require.Contains(t, got.Reason, "not by a "+fragFixerID+" apply")
	})
}

// dropHandOffNote refuses every hand-off note, as a process that died
// between versionprimary's crown write and its note would leave it.
type dropHandOffNote struct{ *database.PebbleStore }

func (d dropHandOffNote) CreateOperationChange(c *database.OperationChange) error {
	if c.ChangeType == undo.ChangeTypeBookPrimaryHandoff {
		return errors.New("injected: the hand-off note is lost")
	}
	return d.PebbleStore.CreateOperationChange(c)
}

// TestFragmentFixer_ForeignCrownIsAChange (review 5 nit 1): a member's
// primary flag raised since the plan is this row's own only when one of its
// hand-off notes names that member, or when a retiree of the group still
// owes its hand-off (the crown written, its note cut off). An outside actor
// crowning another member of the group is a change.
func TestFragmentFixer_ForeignCrownIsAChange(t *testing.T) {
	const dir = "lib/Clarke/02_light_of_other_days"
	// orig[0] and orig[4] non-primary, orig[2] primary, all in one group;
	// retiring orig[2] hands the group to orig[0] (the lower id).
	setup := func(t *testing.T, journal func(*database.PebbleStore) repairs.ChangeJournal) (*fragFixture, repairs.Row, *fragGroupPlan, []string) {
		f := newFragFixture(t)
		orig, _ := f.seedChapterCopies(t, dir, nil)
		for _, id := range orig {
			f.organized(t, id)
		}
		group := "vg-ch"
		for _, i := range []int{0, 2, 4} {
			primary := i == 2
			_, err := f.s.ModifyBook(orig[i], func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = &primary; return nil })
			require.NoError(t, err)
		}
		r, plan := f.p7Plan(t, dir)
		require.NotContains(t, []string{orig[0], orig[2], orig[4]}, plan.SurvivorID)
		f.applyOp("op-cut", fragFixerID)
		w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, journal(f.s), "op-cut")
		f.journalPlanRecord(t, w, r)
		var m fragGroupMember
		for _, x := range plan.Members {
			if x.Frag.Book.ID == orig[2] {
				m = x
			}
		}
		require.NotNil(t, m.Frag)
		require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
		_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
			&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
		require.NoError(t, err)
		b0, err := f.s.GetBookByID(orig[0])
		require.NoError(t, err)
		require.True(t, b0.IsPrimaryVersion == nil || *b0.IsPrimaryVersion, "the hand-off crowned orig[0]")
		return f, r, plan, orig
	}
	plain := func(s *database.PebbleStore) repairs.ChangeJournal { return s }
	recrown := func(t *testing.T, f *fragFixture, from, to string) {
		yes, no := true, false
		_, err := f.s.ModifyBook(from, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		require.NoError(t, err)
		_, err = f.s.ModifyBook(to, func(b *database.Book) error { b.IsPrimaryVersion = &yes; return nil })
		require.NoError(t, err)
	}

	t.Run("the member our hand-off named resumes", func(t *testing.T) {
		f, r, plan, _ := setup(t, plain)
		f.requireResumes(t, r, "op-apply")
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})

	t.Run("an outside actor crowns another member", func(t *testing.T) {
		f, r, _, orig := setup(t, plain)
		recrown(t, f, orig[0], orig[4])
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint, "a changed row")
		require.Contains(t, got.Reason, "book "+orig[4]+" is now primary=true")
		require.Contains(t, got.Reason, "did not change it")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 0, out.Applied)
	})

	t.Run("a fresh plan continues the cut run with its plan time", func(t *testing.T) {
		// Review 6 S1: the continuation keeps the cut run's plan time, so
		// the run's own demote, crown and note (written before the fresh
		// plan) still explain the flags.
		f, r, plan, _ := setup(t, plain)
		row := findRow(t, f.plan(t, "op-plan2"), r.RowID)
		require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
		require.Equal(t, r.Fingerprint, row.Fingerprint)
		require.Equal(t, plan.SurvivorID, row.Proposed["survivor"])
		var st0, st1 fragGroupState
		require.NoError(t, json.Unmarshal(r.State, &st0))
		require.NoError(t, json.Unmarshal(row.State, &st1))
		require.True(t, st0.PlannedAt.Equal(st1.PlannedAt), "the continuation keeps the run's plan time")
		out := f.apply(t, "op-plan2", "op-apply2", []string{row.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})

	t.Run("crown written, note cut off, then an outside actor moves the crown", func(t *testing.T) {
		// Review 6 N1: the pending window accepts only the member a replay
		// of the hand-off crowns (orig[0]); orig[4] is a change.
		f, r, _, orig := setup(t, func(s *database.PebbleStore) repairs.ChangeJournal { return dropHandOffNote{s} })
		recrown(t, f, orig[0], orig[4])
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint)
		require.Contains(t, got.Reason, "book "+orig[4]+" is now primary=true")
	})

	t.Run("crown written, its note cut off: resumes", func(t *testing.T) {
		f, r, plan, orig := setup(t, func(s *database.PebbleStore) repairs.ChangeJournal { return dropHandOffNote{s} })
		cs, err := f.s.GetBookChanges(orig[2])
		require.NoError(t, err)
		for _, c := range cs {
			require.NotEqual(t, undo.ChangeTypeBookPrimaryHandoff, c.ChangeType, "the note was cut off")
		}
		f.requireResumes(t, r, "op-apply")
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})
}

// TestFragmentFixer_PlanStateGates (review 5 nits 3 and 4).
func TestFragmentFixer_PlanStateGates(t *testing.T) {
	const dir = "lib/Clarke/02_light_of_other_days"

	for _, tc := range []struct {
		name string
		edit func(*fragGroupState, repairs.Row)
	}{
		{"no flags", func(st *fragGroupState, _ repairs.Row) { st.Flags = nil }},
		{"no plan time", func(st *fragGroupState, _ repairs.Row) { st.PlannedAt = time.Time{} }},
		{"a book missing from the flags", func(st *fragGroupState, r repairs.Row) { delete(st.Flags, r.BookIDs[len(r.BookIDs)-1]) }},
	} {
		t.Run("a plan stored with "+tc.name+" is planned again", func(t *testing.T) {
			f := newFragFixture(t)
			f.seedChapterCopies(t, dir, nil)
			r, _ := f.p7Plan(t, dir)
			var st fragGroupState
			require.NoError(t, json.Unmarshal(r.State, &st))
			require.NotEmpty(t, st.Flags)
			require.False(t, st.PlannedAt.IsZero())
			tc.edit(&st, r)
			raw, err := json.Marshal(st)
			require.NoError(t, err)
			r.State = raw
			got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
			require.NoError(t, err)
			require.NotEqual(t, r.Fingerprint, got.Fingerprint)
			require.Contains(t, got.Reason, "plan again")
		})
	}

	t.Run("a demote of ours from before the plan does not explain a later one", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		// An earlier run of this fixer journaled a demote of copies[1] (and,
		// say, never wrote it): it predates the plan below.
		require.NoError(t, f.fragWriter(t, "op-old").Journal(copies[1], undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
		time.Sleep(2 * time.Millisecond)
		r, _ := f.p7Plan(t, dir)
		var st fragGroupState
		require.NoError(t, json.Unmarshal(r.State, &st))
		require.True(t, st.Flags[copies[1]].Primary, "planned primary")
		no := false
		_, err := f.s.ModifyBook(copies[1], func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint)
		require.Contains(t, got.Reason, "did not change it")
	})
}

// failGroupStore fails every version-group read, so
// versionprimary.EnsureSinglePrimary errors inside a retire's hand-off.
type failGroupStore struct{ *database.PebbleStore }

func (failGroupStore) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return nil, errors.New("injected group read failure")
}

// crownSet seeds the copies folder with orig[0], orig[2] and orig[4] in one
// version group, orig[2] its explicit primary, plans it, and returns orig[2]'s
// member (retiring it demotes it and hands the group off).
func crownSet(t *testing.T) (*fragFixture, repairs.Row, *fragGroupPlan, []string, fragGroupMember) {
	t.Helper()
	f := newFragFixture(t)
	orig, _ := f.seedChapterCopies(t, "lib/Clarke/02_light_of_other_days", nil)
	for _, id := range orig {
		f.organized(t, id)
	}
	group := "vg-ch"
	for _, i := range []int{0, 2, 4} {
		primary := i == 2
		_, err := f.s.ModifyBook(orig[i], func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = &primary; return nil })
		require.NoError(t, err)
	}
	r, plan := f.p7Plan(t, "lib/Clarke/02_light_of_other_days")
	var m fragGroupMember
	for _, x := range plan.Members {
		if x.Frag.Book.ID == orig[2] {
			m = x
		}
	}
	require.NotNil(t, m.Frag)
	return f, r, plan, orig, m
}

// TestRetireInto_FailedHandOffIsReturned (review 6 N2): a hand-off whose
// EnsureSinglePrimary fails stops the row (the error is returned) instead of
// leaving the group owed a primary with no note while the row reports
// applied; the next run finishes the hand-off.
func TestRetireInto_FailedHandOffIsReturned(t *testing.T) {
	f, _, plan, orig, m := crownSet(t)
	w := f.fragWriter(t, "op-cut")
	require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
	_, err := retireInto(context.Background(), f.p, failGroupStore{f.s}, w, time.Now, fragFixerID, orig[2], plan.SurvivorID,
		&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
	require.ErrorContains(t, err, "primary hand-off of "+orig[2])
	require.False(t, f.live(t, "c:02_003"), "the retire itself is written")
	require.False(t, handoffJournaled(t, f.s, "op-cut", orig[2]), "no note for a hand-off that failed")
	require.Empty(t, f.livePrimaries(t, "vg-ch"), "the group is owed its primary")

	_, err = retireInto(context.Background(), f.p, f.s, f.fragWriter(t, "op-retry"), time.Now, fragFixerID, orig[2], plan.SurvivorID,
		&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
	require.NoError(t, err)
	require.True(t, handoffJournaled(t, f.s, "op-retry", orig[2]))
	require.Len(t, f.livePrimaries(t, "vg-ch"), 1)
}

// TestFragmentFixer_AnotherRetireFixerFinishesOurHandOff (review 6 N3): our
// retire demoted the group's primary and its hand-off failed; the
// duplicate-copies fixer's row then finished that owed hand-off (its note
// carries its own Source). The member it crowned is credited to our demote,
// so our row resumes instead of stalling.
func TestFragmentFixer_AnotherRetireFixerFinishesOurHandOff(t *testing.T) {
	f, r, plan, orig, m := crownSet(t)
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, r)
	require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
	_, err := retireInto(context.Background(), f.p, failGroupStore{f.s}, w, time.Now, fragFixerID, orig[2], plan.SurvivorID,
		&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
	require.Error(t, err)

	f.applyOp("op-dc", dcFixerID)
	wdc := repairs.NewWriter(f.s, f.s, dcFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-dc")
	require.NoError(t, resumeHandOff(context.Background(), f.p, f.s, wdc, dcFixerID, orig[2], plan.SurvivorID))
	require.True(t, handoffJournaled(t, f.s, "op-dc", orig[2]), "the other fixer journaled the hand-off")
	crowned := f.livePrimaries(t, "vg-ch")
	require.Len(t, crowned, 1)

	f.requireResumes(t, r, "op-apply")
	f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
}

// TestFragmentFixer_AnotherPlansRunExplainsNothing (review 6 N4): a flag
// change made by an apply of a DIFFERENT plan of the same row (written after
// this row's plan time) is not this row's: only the runs whose plan record
// names this row id AND plan time explain a flag.
func TestFragmentFixer_AnotherPlansRunExplainsNothing(t *testing.T) {
	f, r, _, orig, _ := crownSet(t)
	time.Sleep(2 * time.Millisecond)
	r2, plan2 := f.p7Plan(t, "lib/Clarke/02_light_of_other_days")
	require.Equal(t, r.RowID, r2.RowID)
	var st1, st2 fragGroupState
	require.NoError(t, json.Unmarshal(r.State, &st1))
	require.NoError(t, json.Unmarshal(r2.State, &st2))
	require.False(t, st1.PlannedAt.Equal(st2.PlannedAt))
	var m fragGroupMember
	for _, x := range plan2.Members {
		if x.Frag.Book.ID == orig[2] {
			m = x
		}
	}
	w := f.fragWriter(t, "op-other")
	f.journalPlanRecord(t, w, r2)
	require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan2.SurvivorID))
	_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, orig[2], plan2.SurvivorID,
		&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
	require.NoError(t, err)

	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	require.NotEqual(t, r.Fingerprint, got.Fingerprint, "the first plan's row does not claim the second plan's run")
	require.Contains(t, got.Reason, "did not change it")
	got2, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r2, nil)
	require.NoError(t, err)
	require.Equal(t, r2.Fingerprint, got2.Fingerprint, "the second plan's row resumes its own run: %s", got2.Reason)
}
