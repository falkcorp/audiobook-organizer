// file: internal/plugins/maintenance/fragment_hash_copy_test.go
// version: 1.0.0
// guid: 7d2f4a68-3c1b-4e95-b0a7-6f8e1d5c9b34
// last-edited: 2026-10-21

package maintenance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The hash-copy rule (owner decision 2026-10-21): a single-file book whose
// sole present file row carries the file hash of a PRESENT row of another
// live multi-file book is a copy of a track the parent already owns, whatever
// its title and file name look like, and is retired into that parent. Every
// fixture here is synthetic.

// hcParentRows is the number of rows of the fixtures' parent book.
const hcParentRows = 5

// hcSeed builds the parent "Alpha Omnibus" with hcParentRows present rows,
// each with its own hash ("hash-pNN"), and returns the fixture.
func hcSeed(t *testing.T) *fragFixture {
	t.Helper()
	f := newFragFixture(t)
	parent := f.book(t, "parent", "Alpha Omnibus", f.path("lib/Alpha Omnibus"), nil)
	for i := 1; i <= hcParentRows; i++ {
		role := fmt.Sprintf("p%02d", i)
		p := f.file(t, fmt.Sprintf("lib/Alpha Omnibus/track-%02d.mp3", i), 1000+i)
		f.row(t, role, parent, p, fmt.Sprintf("track-%02d.mp3", i), int64(1000+i), 600, i)
		f.hcSetHash(t, "parent", role, "hash-"+role)
	}
	return f
}

func (f *fragFixture) hcSetHash(t *testing.T, bookRole, rowRole, hash string) {
	t.Helper()
	f.updateRow(t, f.ids[bookRole], f.rowIDs[rowRole], func(r *database.BookFile) { r.FileHash = hash })
}

// hcChild adds a live single-file book named so that it carries NO chapter
// evidence (a plain title, a plain file name), whose one row has hash.
func (f *fragFixture) hcChild(t *testing.T, role, title, hash string) string {
	t.Helper()
	p := f.file(t, "lib/Stray "+role+"/stray-"+role+".mp3", 3000)
	id := f.book(t, role, title, p, nil)
	f.row(t, role, id, p, "stray-"+role+".mp3", 3000, 600, 0)
	if hash != "" {
		f.hcSetHash(t, role, role, hash)
	}
	f.organized(t, id)
	return id
}

func hcRowID(parent, child string) string { return fragClassHashCopy + ":" + parent + "/" + child }

func TestFragmentFixer_HashCopy_ProposesTrialThenRetiresUndoably(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]
	rows0, err := f.s.GetBookFiles(parent)
	require.NoError(t, err)
	b0, err := f.s.GetBookByID(parent)
	require.NoError(t, err)

	// Trial: the plan lists the proposal and writes nothing.
	res := f.plan(t, "op-plan")
	r := findRow(t, res, hcRowID(parent, child))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, fragClassHashCopy, r.Class)
	require.Equal(t, 1, res.ByClass[fragClassHashCopy], "the trial's summary count")
	require.ElementsMatch(t, []string{parent, child}, r.BookIDs)
	require.Equal(t, "Alpha Omnibus", r.Title)
	require.Len(t, r.Evidence, 1)
	// Child id and title, parent id and title, the matching path and hash.
	for _, want := range []string{f.path("lib/Stray c3/stray-c3.mp3"), f.path("lib/Alpha Omnibus/track-03.mp3"), "hash-p03", child, parent, "Stray Recording", "Alpha Omnibus"} {
		require.Contains(t, r.Evidence[0]+" "+r.Current["child"]+" "+r.Current["parent"], want)
	}
	require.True(t, f.live(t, "c3"), "a plan writes nothing")

	// Apply retires the child as a merge does; no row is deleted or moved.
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	b, err := f.s.GetBookByID(child)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted())
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, parent, *b.MergedIntoBookID)
	crow, err := f.s.GetBookFiles(child)
	require.NoError(t, err)
	require.Len(t, crow, 1, "no book_file row is ever deleted")
	require.Equal(t, f.rowIDs["c3"], crow[0].ID)
	rows1, err := f.s.GetBookFiles(parent)
	require.NoError(t, err)
	require.Equal(t, rows0, rows1, "the parent's rows are not written")
	b1, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Equal(t, *b0, *b1, "the parent book is not written")
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	require.NotEmpty(t, changes, "journaled")
	for _, c := range changes {
		require.Equal(t, child, c.BookID, "only the child is written: %+v", c)
	}

	// The apply is undoable.
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.True(t, f.live(t, "c3"))
	b, err = f.s.GetBookByID(child)
	require.NoError(t, err)
	require.Nil(t, b.MergedIntoBookID)
}

func TestFragmentFixer_HashCopy_UserStateFollowsToTheParent(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, child, f.rowIDs["c3"], 100))
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: child, Status: database.UserBookStatusFinished}))

	res := f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{hcRowID(parent, child)}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	_ = res

	pos, err := f.s.ListUserPositionsForBook(u.ID, child)
	require.NoError(t, err)
	require.Empty(t, pos, "the child's position followed")
	pos, err = f.s.ListUserPositionsForBook(u.ID, parent)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	// The child is the parent's third row: two 600 s rows come before it.
	require.InDelta(t, 1300, pos[0].PositionSeconds, 0.01)
	st, err := f.s.GetUserBookState(u.ID, parent)
	require.NoError(t, err)
	require.NotNil(t, st, "the read status followed too")
}

func TestFragmentFixer_HashCopy_NoProposalWithoutAMatchOrWhenTheParentIsNotEligible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fragFixture)
	}{
		{"no hash matches", func(t *testing.T, f *fragFixture) {}},
		{"parent soft-deleted", func(t *testing.T, f *fragFixture) {
			yes := true
			_, err := f.s.ModifyBook(f.ids["parent"], func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
			require.NoError(t, err)
		}},
		{"parent row missing", func(t *testing.T, f *fragFixture) {
			f.updateRow(t, f.ids["parent"], f.rowIDs["p03"], func(r *database.BookFile) { r.Missing = true })
		}},
		{"child row missing", func(t *testing.T, f *fragFixture) {
			f.updateRow(t, f.ids["c3"], f.rowIDs["c3"], func(r *database.BookFile) { r.Missing = true })
		}},
		{"parent has one row", func(t *testing.T, f *fragFixture) {
			for i := 2; i <= hcParentRows; i++ {
				require.NoError(t, f.s.DeleteBookFile(f.rowIDs[fmt.Sprintf("p%02d", i)]))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			hash := "hash-p03"
			if tc.name == "no hash matches" {
				hash = "hash-unrelated"
			}
			f.hcChild(t, "c3", "Stray Recording", hash)
			tc.setup(t, f)
			res := f.plan(t, "op-plan")
			require.Zero(t, res.ByClass[fragClassHashCopy], "%+v", res.ByClass)
			for _, r := range res.Rows {
				require.NotContains(t, r.BookIDs, f.ids["c3"], "no row at all names the stray book: %s", r.RowID)
			}
		})
	}
}

func TestFragmentFixer_HashCopy_SkippedNoHash(t *testing.T) {
	t.Parallel()
	for _, side := range []string{"child", "parent"} {
		t.Run(side+" has no hash", func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			// The parent's third row is "opening-theme.mp3" (no chapter shape);
			// the child has that name and size, but proof is missing.
			np := f.file(t, "lib/Alpha Omnibus/opening-theme.mp3", 1003)
			f.updateRow(t, f.ids["parent"], f.rowIDs["p03"], func(r *database.BookFile) { r.FilePath = np })
			p := f.file(t, "lib/Stray/opening-theme.mp3", 1003)
			id := f.book(t, "c3", "Stray Recording", p, nil)
			f.row(t, "c3", id, p, "opening-theme.mp3", 1003, 600, 0)
			if side == "child" {
				f.hcSetHash(t, "parent", "p03", "hash-p03")
			} else {
				f.hcSetHash(t, "c3", "c3", "hash-p03")
				f.updateRow(t, f.ids["parent"], f.rowIDs["p03"], func(r *database.BookFile) { r.FileHash = "" })
			}
			res := f.plan(t, "op-plan")
			require.Equal(t, 1, res.SkippedByKind[fragSkipNoHash], "%+v", res.SkippedByKind)
			var r repairs.Row
			for _, row := range res.Rows {
				if row.Skipped == fragSkipNoHash {
					r = row
				}
			}
			require.Equal(t, fragClassHashCopy, r.Class)
			require.False(t, r.Applicable())
			require.Contains(t, r.BookIDs, id)
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Zero(t, out.Applied)
			require.True(t, f.live(t, "c3"))
		})
	}
}

func TestFragmentFixer_HashCopy_TwoParentsIsHeldAmbiguous(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	// A second live multi-file book holds a row with the same hash.
	other := f.book(t, "other", "Beta Omnibus", f.path("lib/Beta Omnibus"), nil)
	for i := 1; i <= 2; i++ {
		role := fmt.Sprintf("o%02d", i)
		p := f.file(t, fmt.Sprintf("lib/Beta Omnibus/track-%02d.mp3", i), 2000+i)
		f.row(t, role, other, p, fmt.Sprintf("track-%02d.mp3", i), int64(2000+i), 600, i)
	}
	f.hcSetHash(t, "other", "o01", "hash-p03")
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")

	res := f.plan(t, "op-plan")
	require.Equal(t, 1, res.SkippedByKind[fragSkipAmbiguousParent], "%+v", res.SkippedByKind)
	var r repairs.Row
	for _, row := range res.Rows {
		if row.Skipped == fragSkipAmbiguousParent {
			r = row
		}
	}
	require.Equal(t, fragClassHashCopy, r.Class)
	require.False(t, r.Applicable())
	require.Contains(t, r.BookIDs, child)
	require.Contains(t, r.BookIDs, f.ids["parent"])
	require.Contains(t, r.BookIDs, other)
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied)
	require.True(t, f.live(t, "c3"))
}

func TestFragmentFixer_HashCopy_TwoRowsOfOneParentIsNotAmbiguous(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	// The parent holds the same file twice (rows 02 and 03 share a hash): one
	// parent, so one proposal, and the same row is chosen on every plan.
	f.hcSetHash(t, "parent", "p02", "hash-p03")
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]

	r1 := findRow(t, f.plan(t, "op-plan-1"), hcRowID(parent, child))
	r2 := findRow(t, f.plan(t, "op-plan-2"), hcRowID(parent, child))
	require.True(t, r1.Applicable(), "%s: %s", r1.Skipped, r1.SkipReason)
	require.Equal(t, r1.Fingerprint, r2.Fingerprint, "deterministic")
	out := f.apply(t, "op-plan-1", "op-apply", []string{r1.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
}

// A second live parent that appeared after the plan is seen when the row is
// decided again under the merge lock: nothing is written.
func TestFragmentFixer_HashCopy_SecondParentAfterPlanChangesTheRow(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]
	res := f.plan(t, "op-plan")
	r := findRow(t, res, hcRowID(parent, child))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)

	other := f.book(t, "other", "Beta Omnibus", f.path("lib/Beta Omnibus"), nil)
	for i := 1; i <= 2; i++ {
		role := fmt.Sprintf("o%02d", i)
		p := f.file(t, fmt.Sprintf("lib/Beta Omnibus/track-%02d.mp3", i), 2000+i)
		f.row(t, role, other, p, fmt.Sprintf("track-%02d.mp3", i), int64(2000+i), 600, i)
	}
	f.hcSetHash(t, "other", "o01", "hash-p03")

	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.True(t, f.live(t, "c3"))
}

// A non-chapter single-file book that matches nothing leaks into no other
// class: the rule adds proposals, never unplaced or no-parent rows.
func TestFragmentFixer_HashCopy_StrayWithoutMatchLeavesNoRows(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	f.hcChild(t, "c1", "Stray One", "hash-x1")
	f.hcChild(t, "c2", "Stray Two", "")
	res := f.plan(t, "op-plan")
	require.Empty(t, res.Rows, "%+v", res.ByClass)
}

// Library scale: many children of one 59-row parent are all found through
// the hash index in one plan (the worker pool runs under -race), each on its
// own row, and one apply retires them all.
func TestFragmentFixer_HashCopy_ManyChildren(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	const rows, kids = 59, 52
	parent := f.book(t, "parent", "Large Omnibus", f.path("lib/Large Omnibus"), nil)
	for i := 1; i <= rows; i++ {
		role := fmt.Sprintf("p%02d", i)
		p := f.file(t, fmt.Sprintf("lib/Large Omnibus/part-%02d.mp3", i), 500+i)
		f.row(t, role, parent, p, fmt.Sprintf("part-%02d.mp3", i), int64(500+i), 300, i)
		f.hcSetHash(t, "parent", role, "hash-"+role)
	}
	var want []string
	for i := 1; i <= kids; i++ {
		role := fmt.Sprintf("k%02d", i)
		id := f.hcChild(t, role, "Stray "+role, fmt.Sprintf("hash-p%02d", i))
		want = append(want, hcRowID(parent, id))
	}
	res := f.plan(t, "op-plan")
	require.Equal(t, kids, res.ByClass[fragClassHashCopy])
	require.Equal(t, kids, res.Applicable)
	out := f.apply(t, "op-plan", "op-apply", want, nil)
	require.Equal(t, kids, out.Applied, "%+v", out.Rows)
	for i := 1; i <= kids; i++ {
		require.False(t, f.live(t, fmt.Sprintf("k%02d", i)))
	}
	require.True(t, f.live(t, "parent"))
}
