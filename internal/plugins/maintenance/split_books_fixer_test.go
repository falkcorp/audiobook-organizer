// file: internal/plugins/maintenance/split_books_fixer_test.go
// version: 1.0.0
// guid: 946919fd-5279-41f2-bb7e-1331ea924e5e
// last-edited: 2026-10-12

package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// sbCluster seeds n books in dir, one numbered chapter file each
// ("<stem> 01.mp3" ...), each dur seconds long, and returns their ids
// sorted (the detector's order: the first is the suggested keep).
func sbCluster(t *testing.T, f *fragFixture, dir, stem string, n, dur int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%s %02d", stem, i)
		rel := filepath.Join(dir, name+".mp3")
		p := f.file(t, rel, 900+i)
		id := f.book(t, rel, name, p, nil)
		f.row(t, rel, id, p, name+".mp3", int64(900+i), dur, 0)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sbFixer(f *fragFixture) *splitBooksFixer { return newSplitBooksFixer(f.p) }

func sbPlan(t *testing.T, f *fragFixture, fx repairs.Fixer) *repairs.PlanResult {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), fx, nil, repairs.PlanDeps{Guard: f.s}, &fakeReporter{})
	require.NoError(t, err)
	return res
}

func sbWriter(f *fragFixture, opID string) *repairs.Writer {
	return repairs.NewWriter(f.s, f.s, splitBooksFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, opID)
}

func sbApply(t *testing.T, f *fragFixture, fx repairs.Fixer, res *repairs.PlanResult, opID string, rowIDs ...string) repairs.RowResult {
	t.Helper()
	out, err := repairs.RunApply(context.Background(), fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Writer: sbWriter(f, opID), OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, out.Rows, 1, "%+v", out.Rows)
	return out.Rows[0]
}

// sbRowWith returns the planned row whose books are exactly ids.
func sbRowWith(t *testing.T, res *repairs.PlanResult, ids []string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		got := append([]string(nil), r.BookIDs...)
		sort.Strings(got)
		if slices.Equal(got, ids) {
			return r
		}
	}
	t.Fatalf("no row with books %v", ids)
	return repairs.Row{}
}

// sbLiveJournal lists the op's journal rows that were not voided, as
// "type|field|book".
func sbLiveJournal(t *testing.T, f *fragFixture, opID string) []string {
	t.Helper()
	changes, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	var out []string
	for _, c := range changes {
		if c.RevertedAt == nil {
			out = append(out, c.ChangeType+"|"+strings.SplitN(c.FieldName, ":", 2)[0]+"|"+c.BookID)
		}
	}
	sort.Strings(out)
	return out
}

// sbFileOwners maps every book_file row of ids to the book that holds it.
func sbFileOwners(t *testing.T, f *fragFixture, ids []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range ids {
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		for _, r := range rows {
			out[r.ID] = id
		}
	}
	return out
}

func sbBook(t *testing.T, f *fragFixture, id string) *database.Book {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

// The fixer's rows are the clusters the old Split Books tab lists:
// ListSplitBookCandidates serves what dedup.split-book-scan saved
// (DetectSplitBookCandidates, then SplitBookStore.SaveAll), so the same
// fixture saved and listed that way must give the same clusters, keep and
// folder as the plan.
func TestSplitBooksFixer_PlanRowsEqualListSplitBookCandidates(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 4, 300)
	sbCluster(t, f, "lib/Author B/Book Three", "Book Three", 5, 300)
	// Two numbered books are no cluster.
	sbCluster(t, f, "lib/Author B/Book Four", "Book Four", 2, 300)

	cands, err := dedup.DetectSplitBookCandidates(context.Background(), f.s, nil)
	require.NoError(t, err)
	cs := dedup.NewSplitBookStore(f.s.DB())
	require.NoError(t, cs.SaveAll(cands))
	listed, err := cs.List()
	require.NoError(t, err)
	require.Len(t, listed, 3)

	res := sbPlan(t, f, sbFixer(f))
	require.Len(t, res.Rows, len(listed), "one row per listed candidate")
	for _, c := range listed {
		ids := append([]string(nil), c.BookIDs...)
		sort.Strings(ids)
		r := sbRowWith(t, res, ids)
		assert.True(t, r.Applicable(), "%s: %s", r.RowID, r.SkipReason)
		assert.Equal(t, c.SuggestedTitle, r.Title)
		assert.Equal(t, c.Shape, r.Class)
		require.Len(t, r.Members, len(c.BookIDs))
		for _, m := range r.Members {
			want := "fragment"
			if m.BookID == c.BookIDs[0] {
				want = "keep"
			}
			assert.Equal(t, want, m.Role, "member %s", m.BookID)
			assert.Equal(t, 1, m.Files)
		}
		assert.Contains(t, strings.Join(r.Evidence, "\n"), c.ParentFolder)
	}
}

// A cluster with an iTunes book (here: a book iTunes persistent id) is a
// skipped row with the reason "iTunes"; the other cluster stays applicable.
func TestSplitBooksFixer_ITunesClusterIsSkipped(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	plain := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	tunes := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	pid := "ABCDEF0123456789"
	_, err := f.s.ModifyBook(tunes[1], func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)

	res := sbPlan(t, f, sbFixer(f))
	r := sbRowWith(t, res, tunes)
	assert.False(t, r.Applicable())
	assert.Equal(t, repairs.SkipITunes, r.Skipped)
	assert.Equal(t, "iTunes", r.SkipReason)
	assert.Contains(t, r.Reason, tunes[1])
	assert.Len(t, r.Members, 3, "a skipped row still lists its books")
	assert.True(t, sbRowWith(t, res, plain).Applicable())
}

// Replan decides a cluster with the same evaluate as Plan: unchanged, the
// re-planned row is the planned one; a member retired since turns it into a
// row with another fingerprint.
func TestSplitBooksFixer_ReplanUsesTheSameEvaluate(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	require.Len(t, res.Rows, 2)
	for _, planned := range res.Rows {
		fresh, err := fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		assert.Equal(t, planned.Fingerprint, fresh.Fingerprint, planned.RowID)
		assert.Equal(t, planned.BookIDs, fresh.BookIDs)
		assert.Equal(t, planned.Members, fresh.Members)
		assert.Equal(t, planned.Current, fresh.Current)
		assert.Equal(t, planned.Proposed, fresh.Proposed)
		assert.Equal(t, planned.Evidence, fresh.Evidence)
		assert.NotNil(t, fresh.Detail)
	}

	planned := sbRowWith(t, res, ids)
	_, err := f.s.ModifyBook(ids[2], func(b *database.Book) error { v := true; b.MarkedForDeletion = &v; return nil })
	require.NoError(t, err)
	fresh, err := fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.NotEqual(t, planned.Fingerprint, fresh.Fingerprint)
	assert.False(t, fresh.Applicable())
}

// Applying one row merges exactly that cluster into its suggested keep:
// every file row moves onto the keep, the others are retired, the keep
// takes the suggested title, and the op journal records the decision and
// the merge's undo journal. The other cluster is untouched.
func TestSplitBooksFixer_ApplyMergesOneClusterIntoTheKeep(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	other := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	require.True(t, row.Applicable(), row.SkipReason)
	before := sbFileOwners(t, f, ids)
	require.Len(t, before, 3)

	out := sbApply(t, f, fx, res, "op-apply", row.RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)

	keep := sbBook(t, f, ids[0])
	assert.False(t, keep.IsSoftDeleted())
	assert.Equal(t, "Book One", keep.Title, "the keep takes the suggested title")
	for _, src := range ids[1:] {
		assert.True(t, sbBook(t, f, src).IsSoftDeleted(), "source %s is retired", src)
	}
	for fileID, owner := range sbFileOwners(t, f, ids) {
		assert.Equal(t, ids[0], owner, "file row %s is on the keep", fileID)
	}
	for _, id := range other {
		assert.False(t, sbBook(t, f, id).IsSoftDeleted(), "the other cluster is untouched")
	}
	assert.Equal(t, []string{
		sbChangeType + "|combine_journal|" + ids[0],
		sbChangeType + "|row|" + ids[0],
	}, sbLiveJournal(t, f, "op-apply"))

	// Applying the same plan again finds the cluster gone.
	again := sbApply(t, f, fx, res, "op-again", row.RowID)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, again.Outcome, again.Error)
}

// A merge repoints book_file rows; it never deletes one. Every row the
// cluster had before the apply exists after it, with the same id.
func TestSplitBooksFixer_NoBookFileRowIsDeleted(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 4, 300)
	// A second file on one source, and a missing row on another.
	extra := f.file(t, "lib/Author A/Book One/Book One 02b.mp3", 999)
	f.row(t, "extra", ids[1], extra, "Book One 02b.mp3", 999, 60, 0)
	f.row(t, "missing", ids[2], f.path("lib/Author A/Book One/gone.mp3"), "gone.mp3", 10, 10, 0)
	_, err := f.s.ModifyBookFile(ids[2], f.rowIDs["missing"], func(bf *database.BookFile) error { bf.Missing = true; return nil })
	require.NoError(t, err)

	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	require.True(t, row.Applicable(), row.SkipReason)
	before := sbFileOwners(t, f, ids)
	require.Len(t, before, 6)

	out := sbApply(t, f, fx, res, "op-rows", row.RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)
	after := sbFileOwners(t, f, ids)
	assert.Len(t, after, len(before), "no book_file row is deleted")
	for fileID := range before {
		assert.Equal(t, ids[0], after[fileID], "row %s survives, on the keep", fileID)
	}
}

// sbBeforeApply runs before() between the engine's re-plan and the fixer's
// Apply: another writer landing in that window.
type sbBeforeApply struct {
	*splitBooksFixer
	before func()
}

func (b sbBeforeApply) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	b.before()
	return b.splitBooksFixer.Apply(ctx, w, fresh)
}

// Membership that changed after the re-plan is caught under the merge lock
// (Precheck): the row is changed_since_plan, nothing is merged, and the
// journaled decision is voided. Membership that changed before the re-plan
// changes the fingerprint.
func TestSplitBooksFixer_ApplyRefusesChangedMembership(t *testing.T) {
	t.Parallel()
	t.Run("after the re-plan", func(t *testing.T) {
		t.Parallel()
		f := newFragFixture(t)
		ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
		base := sbFixer(f)
		res := sbPlan(t, f, base)
		row := sbRowWith(t, res, ids)
		fx := sbBeforeApply{splitBooksFixer: base, before: func() {
			_, err := f.s.ModifyBook(ids[2], func(b *database.Book) error { v := true; b.MarkedForDeletion = &v; return nil })
			require.NoError(t, err)
		}}
		out := sbApply(t, f, fx, res, "op-late", row.RowID)
		assert.Equal(t, repairs.OutcomeChangedSincePlan, out.Outcome, out.Error)
		assert.Contains(t, out.Error, ids[2])
		assert.False(t, sbBook(t, f, ids[1]).IsSoftDeleted(), "nothing is merged")
		assert.Len(t, sbFileOwners(t, f, ids[:1]), 1, "the keep gained no file")
		assert.Empty(t, sbLiveJournal(t, f, "op-late"), "the decision row of the refused merge is voided")
	})
	t.Run("before the re-plan", func(t *testing.T) {
		t.Parallel()
		f := newFragFixture(t)
		ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
		fx := sbFixer(f)
		res := sbPlan(t, f, fx)
		row := sbRowWith(t, res, ids)
		// A fourth chapter joins the folder: the detector's cluster is no
		// longer the planned one.
		p := f.file(t, "lib/Author A/Book One/Book One 04.mp3", 904)
		id := f.book(t, "four", "Book One 04", p, nil)
		f.row(t, "four", id, p, "Book One 04.mp3", 904, 300, 0)
		out := sbApply(t, f, fx, res, "op-early", row.RowID)
		assert.Equal(t, repairs.OutcomeChangedSincePlan, out.Outcome, out.Error)
		for _, src := range ids[1:] {
			assert.False(t, sbBook(t, f, src).IsSoftDeleted())
		}
		assert.Empty(t, sbLiveJournal(t, f, "op-early"))
	})
}

// TestSplitBooksFixer_D21OverlapWithFragmentNoParent is the measurement owner
// decision D21 asks for, on a synthetic library: of N split-book clusters,
// how many the fragment-consolidation fixer also lists as a "no parent" row
// with exactly the same books. It is a test, not a runtime feature; on prod
// the same comparison is made by running both trials in the Repairs lane and
// comparing the two book-id sets.
func TestSplitBooksFixer_D21OverlapWithFragmentNoParent(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	// Short chapters with an organized keep: the fragment fixer's own
	// no-parent shape.
	a := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	f.organized(t, a[0])
	// The same shape with nothing organized.
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 4, 300)
	// Hour-long parts: whole books to the fragment fixer.
	sbCluster(t, f, "lib/Author B/Book Three", "Book Three", 3, 3*3600)
	// One chapter per numbered subfolder (the grandparent shape).
	for i := 1; i <= 3; i++ {
		rel := fmt.Sprintf("lib/Author B/Book Four/%d/Book Four.mp3", i)
		p := f.file(t, rel, 950+i)
		id := f.book(t, rel, "Book Four", p, nil)
		f.row(t, rel, id, p, "Book Four.mp3", int64(950+i), 300, 0)
	}

	split := sbPlan(t, f, sbFixer(f))
	frag := f.plan(t, "plan-frag")
	noParent := map[string]repairs.Row{}
	for _, r := range frag.Rows {
		if r.Class != fragClassNoParent {
			continue
		}
		ids := append([]string(nil), r.BookIDs...)
		sort.Strings(ids)
		noParent[strings.Join(ids, ",")] = r
	}
	var both, bothApplicable []string
	for _, r := range split.Rows {
		ids := append([]string(nil), r.BookIDs...)
		sort.Strings(ids)
		if fr, ok := noParent[strings.Join(ids, ",")]; ok {
			t.Logf("D21: %q is also a fragment no-parent row (skipped=%q: %s)", r.Title, fr.Skipped, fr.SkipReason)
			both = append(both, r.Title)
			if fr.Applicable() {
				bothApplicable = append(bothApplicable, r.Title)
			}
		}
	}
	sort.Strings(both)
	sort.Strings(bothApplicable)
	t.Logf("D21: %d split-book clusters; %d also fragment no-parent rows (%v), %d of them applicable there (%v)",
		len(split.Rows), len(both), both, len(bothApplicable), bothApplicable)
	// Measured: the grandparent shape (one chapter per numbered subfolder)
	// is the split fixer's alone; the hour-long parts are a fragment
	// no-parent row too, but skipped there by its duration gate (the split
	// detector has none).
	assert.Len(t, split.Rows, 4)
	assert.Equal(t, []string{"Book One", "Book Three", "Book Two"}, both)
	assert.Equal(t, []string{"Book One", "Book Two"}, bothApplicable)
}
