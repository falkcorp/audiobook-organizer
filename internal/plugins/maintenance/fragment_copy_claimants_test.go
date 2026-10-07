// file: internal/plugins/maintenance/fragment_copy_claimants_test.go
// version: 1.1.0
// guid: 3f8b2c61-7d4e-4a19-9c05-e2b6a8d17f43
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// copyClaimantsFixture seeds the owner's 2026-10-06 shape, synthetically: a
// two-row parent whose files are both on disk, and three single-file
// fragments that each claim the parent's row 02 by its original name and
// size: two in the library folder (libA, libB) and one under the iTunes
// library (itm). hashed puts the parent row's hash on every claimant
// (proven copies); otherwise the claims rest on the name and size only.
func copyClaimantsFixture(t *testing.T, hashed bool) *fragFixture {
	t.Helper()
	f := newFragFixture(t)
	p1 := f.file(t, "lib/Many Parts/01.mp3", 2001)
	p2 := f.file(t, "lib/Many Parts/02.mp3", 2002)
	parent := f.book(t, "parent", "Many Parts", f.path("lib/Many Parts"), nil)
	f.row(t, "p01", parent, p1, "01.mp3", 2001, 600, 1)
	f.row(t, "p02", parent, p2, "02.mp3", 2002, 600, 2)
	claim := func(role, rel string) string {
		p := f.file(t, rel, 2002)
		id := f.book(t, role, "02", p, nil)
		f.row(t, role, id, p, "02.mp3", 2002, 590, 0)
		f.organized(t, id)
		return id
	}
	claim("libA", "lib/Many Parts copy A/02.mp3")
	claim("libB", "lib/Many Parts copy B/02.mp3")
	claim("itm", "books/itunes/iTunes Media/Music/Many Parts/02.mp3")
	if hashed {
		for row, book := range map[string]string{"p02": "parent", "libA": "libA", "libB": "libB", "itm": "itm"} {
			f.updateRow(t, f.ids[book], f.rowIDs[row], func(r *database.BookFile) { r.FileHash = "hash-02" })
		}
	}
	return f
}

func (f *fragFixture) updateRow(t *testing.T, bookID, rowID string, mod func(*database.BookFile)) {
	t.Helper()
	r, err := f.s.GetBookFileByID(bookID, rowID)
	require.NoError(t, err)
	require.NotNil(t, r)
	mod(r)
	require.NoError(t, f.s.UpdateBookFile(r.ID, r))
}

// linkParentToITunes gives every parent row an iTunes path: the parent is
// then an iTunes-linked book (itunesCopyWhy's "row iTunes path").
func linkParentToITunes(t *testing.T, f *fragFixture) {
	t.Helper()
	rows, err := f.s.GetBookFiles(f.ids["parent"])
	require.NoError(t, err)
	for i := range rows {
		f.updateRow(t, f.ids["parent"], rows[i].ID, func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/Music/iTunes Media/Many Parts/" + r.OriginalFilename
		})
	}
}

// TestFragmentFixer_CopyClaimants (owner decision 2026-10-06): several
// fragments claiming a parent row whose file is still on disk are each a
// copy of it, not a contradiction.
func TestFragmentFixer_CopyClaimants(t *testing.T) {
	t.Parallel()

	t.Run("unproven library copies are listed as copies; the iTunes Media one is manual-only", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, fragRowCopyUnproven+":"+f.ids["parent"])
		require.Equal(t, fragClassCopy, r.Class)
		require.Equal(t, fragSkipCopyUnproven, r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"], f.ids["libB"]}, r.BookIDs)
		itm := findRow(t, res, "manual:"+f.ids["itm"])
		require.Equal(t, fragClassManual, itm.Class)
		require.False(t, itm.Applicable())
		for _, row := range res.Rows {
			require.NotEqual(t, fragClassAmbiguous, row.Class, "%s: %s", row.RowID, row.SkipReason)
		}
	})

	t.Run("unproven copies of an iTunes-linked parent are still only listed", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		r := findRow(t, f.plan(t, "op-plan"), fragRowCopyUnproven+":"+f.ids["parent"])
		require.Equal(t, fragSkipCopyUnproven, r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"], f.ids["libB"]}, r.BookIDs)
	})

	t.Run("iTunes-linked parent: the library copies retire and nothing on the parent is written", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		parent := f.ids["parent"]
		// The parent's group, a sibling in it, a reader's position on it,
		// and a fragment that is primary in a single-member group of its own.
		vgP, vgA := "vg-parent", "vg-libA"
		yes, no := true, false
		sib := f.book(t, "sib", "Many Parts (other edition)", f.file(t, "lib/Other/x.m4b", 77), nil)
		for id, flag := range map[string]*bool{parent: &yes, sib: &no} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &vgP; b.IsPrimaryVersion = flag; return nil })
			require.NoError(t, err)
		}
		_, err := f.s.ModifyBook(f.ids["libA"], func(b *database.Book) error { b.VersionGroupID = &vgA; b.IsPrimaryVersion = &yes; return nil })
		require.NoError(t, err)
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0PARENT", BookID: parent}))
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, f.s.SetUserPosition(u.ID, parent, f.rowIDs["p01"], 42))

		snap := func() (database.Book, []database.BookFile, []database.ExternalIDMapping, []database.UserPosition, database.Book) {
			b, err := f.s.GetBookByID(parent)
			require.NoError(t, err)
			rows, err := f.s.GetBookFiles(parent)
			require.NoError(t, err)
			exts, err := f.s.GetExternalIDsForBook(parent)
			require.NoError(t, err)
			pos, err := f.s.ListUserPositionsForBook(u.ID, parent)
			require.NoError(t, err)
			s, err := f.s.GetBookByID(sib)
			require.NoError(t, err)
			return *b, rows, exts, pos, *s
		}
		b0, rows0, exts0, pos0, sib0 := snap()
		_, hadSync, err := f.s.GetSyncIDForBook(parent)
		require.NoError(t, err)

		res := f.plan(t, "op-plan")
		r := findRow(t, res, "copy:"+parent)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{parent, f.ids["libA"], f.ids["libB"]}, r.BookIDs)
		require.Contains(t, r.Current["itunes_parent"], "row iTunes path")
		require.Equal(t, fragClassManual, findRow(t, res, "manual:"+f.ids["itm"]).Class)

		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		for _, role := range []string{"libA", "libB"} {
			b, err := f.s.GetBookByID(f.ids[role])
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), role)
			require.NotNil(t, b.MergedIntoBookID)
			require.Equal(t, parent, *b.MergedIntoBookID)
		}
		require.True(t, f.live(t, "itm"), "the iTunes Media copy is never written")

		b1, rows1, exts1, pos1, sib1 := snap()
		require.Equal(t, b0, b1, "the parent book is not written")
		require.Equal(t, rows0, rows1, "the parent's rows are not written")
		require.Equal(t, exts0, exts1, "no external id moves onto the parent")
		require.Equal(t, pos0, pos1, "no listening state follows onto the parent")
		require.Equal(t, sib0, sib1, "the parent's group is not re-ranked")
		_, hasSync, err := f.s.GetSyncIDForBook(parent)
		require.NoError(t, err)
		require.Equal(t, hadSync, hasSync, "no sync id minted for the parent")
		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		require.NotEmpty(t, changes)
		for _, c := range changes {
			require.Contains(t, []string{f.ids["libA"], f.ids["libB"]}, c.BookID, "only the fragments are written: %+v", c)
			require.NotContains(t, []string{undo.ChangeTypeUserStateFollow, undo.ChangeTypeExternalIDReassign}, c.ChangeType, "%+v", c)
		}
	})

	t.Run("iTunes-linked parent: a fragment with listening state or an external id is held", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["libA"], f.rowIDs["libA"], 100))
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0LIBB", BookID: f.ids["libB"]}))
		res := f.plan(t, "op-plan")
		a := findRow(t, res, "held:"+f.ids["libA"])
		require.Equal(t, repairs.SkipITunes, a.Skipped)
		require.Contains(t, a.SkipReason, "parent is iTunes-linked")
		require.Contains(t, a.SkipReason, "listening state")
		b := findRow(t, res, "held:"+f.ids["libB"])
		require.Equal(t, repairs.SkipITunes, b.Skipped)
		require.Contains(t, b.SkipReason, "audible/B0LIBB would have to move onto it")
		for _, row := range res.Rows {
			require.NotEqual(t, "copy:"+f.ids["parent"], row.RowID, "no copy row is left to apply")
		}
	})

	t.Run("iTunes-linked parent: a path twin of a held fragment is held with it", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		// A second single-row book registered for libA's file, carrying none
		// of its facts (no size or hash), so it can only join libA as a twin.
		aPath := f.path("lib/Many Parts copy A/02.mp3")
		twin := f.book(t, "twin", "Many Parts - 02", aPath, nil)
		f.row(t, "twin", twin, aPath, "", 0, 0, 0)
		r0 := findRow(t, f.plan(t, "op-plan0"), "copy:"+f.ids["parent"])
		require.Contains(t, r0.BookIDs, twin, "the twin joins its donor's row")
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["libA"], f.rowIDs["libA"], 100))
		res := f.plan(t, "op-plan")
		require.Equal(t, repairs.SkipITunes, findRow(t, res, "held:"+f.ids["libA"]).Skipped)
		tw := findRow(t, res, "held:"+twin)
		require.Contains(t, tw.SkipReason, "shares its file with fragment "+f.ids["libA"])
		r := findRow(t, res, "copy:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, r.BookIDs)
	})

	t.Run("iTunes-linked parent: the fragment's group holds an iTunes book", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		// libA grouped with the parent itself: retiring libA would hand the
		// parent's group primary on.
		vg := "vg-shared"
		for _, id := range []string{f.ids["parent"], f.ids["libA"]} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &vg; return nil })
			require.NoError(t, err)
		}
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, vg)
	})

	t.Run("a gone parent row with two present claimants stays ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		require.NoError(t, os.Remove(f.path("lib/Many Parts/02.mp3")))
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "parent row "+f.rowIDs["p02"]+" is claimed by 3 fragments")
		}
	})

	t.Run("claimants whose sizes on disk conflict stay ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		require.NoError(t, os.WriteFile(f.path("lib/Many Parts copy B/02.mp3"), make([]byte, 2100), 0o644))
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "is claimed by 3 fragments")
		}
		for _, row := range res.Rows {
			require.False(t, strings.HasPrefix(row.RowID, fragRowCopyUnproven+":"), "no copy row: %s", row.RowID)
		}
	})

	t.Run("claimants whose hashes disagree stay ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		// The parent row has no hash, so each still matches it by name and
		// size; the claimants' own hashes say they are different audio.
		f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) { r.FileHash = "hash-a" })
		f.updateRow(t, f.ids["libB"], f.rowIDs["libB"], func(r *database.BookFile) { r.FileHash = "hash-b" })
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "is claimed by 3 fragments")
		}
	})

	t.Run("parent turns iTunes-linked after the plan: refused, nothing written", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		linkParentToITunes(t, f)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	t.Run("parent turns iTunes-linked after the locked re-plan: the pre-write check refuses", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		plan := f.plan(t, "op-plan")
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { linkParentToITunes(t, f) }
		err := applyRowInRun(t, f, fx, context.Background(), plan, "copy:"+f.ids["parent"])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	t.Run("iTunes-linked parent: a fragment grouped with the parent after the locked re-plan is refused", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		plan := f.plan(t, "op-plan")
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() {
			vg := "vg-late"
			for _, id := range []string{f.ids["parent"], f.ids["libA"]} {
				_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &vg; return nil })
				require.NoError(t, err)
			}
		}
		err := applyRowInRun(t, f, fx, context.Background(), plan, "copy:"+f.ids["parent"])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), "vg-late")
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	t.Run("iTunes-linked parent: listening state landing after the locked re-plan is refused", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		linkParentToITunes(t, f)
		plan := f.plan(t, "op-plan")
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		// The state lands on the fragment that sorts LAST: the refusal must
		// come before the first fragment is retired, not when the retire
		// loop reaches it (review round 1, S1).
		last, lastRole := f.ids["libA"], "libA"
		if f.ids["libB"] > last {
			last, lastRole = f.ids["libB"], "libB"
		}
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() {
			require.NoError(t, f.s.SetUserPosition(u.ID, last, f.rowIDs[lastRole], 100))
		}
		err = applyRowInRun(t, f, fx, context.Background(), plan, "copy:"+f.ids["parent"])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.NotErrorIs(t, err, repairs.ErrPartiallyApplied)
		require.Contains(t, err.Error(), "listening state")
		require.True(t, f.live(t, "libA"), "nothing retired")
		require.True(t, f.live(t, "libB"), "nothing retired")
		pos, err := f.s.ListUserPositionsForBook(u.ID, f.ids["parent"])
		require.NoError(t, err)
		require.Empty(t, pos, "nothing followed onto the parent")
	})

	t.Run("a library copy whose own row has an iTunes path is manual-only, never retired", func(t *testing.T) {
		// Review round 1, B1: on prod most library-copy rows carry an
		// itunes_path naming their own file; such a fragment is an iTunes
		// book (itunesCopyWhy) and must not be retired.
		t.Parallel()
		for _, itParent := range []bool{false, true} {
			f := copyClaimantsFixture(t, true)
			if itParent {
				linkParentToITunes(t, f)
			}
			f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
				r.ITunesPath = "file://localhost/Music/iTunes Media/Many Parts/02.mp3"
			})
			res := f.plan(t, "op-plan")
			m := findRow(t, res, "manual:"+f.ids["libA"])
			require.Equal(t, fragClassManual, m.Class)
			require.Equal(t, repairs.SkipITunes, m.Skipped)
			require.Contains(t, m.SkipReason, "row iTunes path")
			r := findRow(t, res, "copy:"+f.ids["parent"])
			require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, r.BookIDs)
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.True(t, f.live(t, "libA"), "the iTunes-linked copy is never retired")
			require.False(t, f.live(t, "libB"))
		}
	})

	t.Run("a row iTunes path landing after the locked re-plan refuses the retire", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		plan := f.plan(t, "op-plan")
		first, firstRole := f.ids["libA"], "libA"
		if f.ids["libB"] < first {
			first, firstRole = f.ids["libB"], "libB"
		}
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() {
			f.updateRow(t, first, f.rowIDs[firstRole], func(r *database.BookFile) { r.ITunesPath = "file://localhost/late.mp3" })
		}
		err := applyRowInRun(t, f, fx, context.Background(), plan, "copy:"+f.ids["parent"])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), "iTunes path")
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	t.Run("a donor whose path twin is hands-off is held with it", func(t *testing.T) {
		// Review round 1, S2: the twin is split off (manual: an iTunes path
		// on its row), so its donor, which shares its file, must not retire
		// beside a live co-owner.
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		aPath := f.path("lib/Many Parts copy A/02.mp3")
		twin := f.book(t, "twin", "Many Parts - 02", aPath, nil)
		f.row(t, "twin", twin, aPath, "", 0, 0, 0)
		f.updateRow(t, twin, f.rowIDs["twin"], func(r *database.BookFile) { r.ITunesPath = "file://localhost/twin.mp3" })
		res := f.plan(t, "op-plan")
		require.Equal(t, fragClassManual, findRow(t, res, "manual:"+twin).Class)
		d := findRow(t, res, "held:"+f.ids["libA"])
		require.Contains(t, d.SkipReason, "shares its file with fragment "+twin)
		r := findRow(t, res, "copy:"+f.ids["parent"])
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, r.BookIDs)
	})

	for _, tc := range []struct {
		name string
		link func(t *testing.T, f *fragFixture)
	}{
		{"book iTunes id", func(t *testing.T, f *fragFixture) { setBookPID(t, f, f.ids["parent"], "PARENTPID") }},
		{"row iTunes id", func(t *testing.T, f *fragFixture) {
			f.updateRow(t, f.ids["parent"], f.rowIDs["p01"], func(r *database.BookFile) { r.ITunesPersistentID = "ROWPID" })
		}},
		{"itunes external id", func(t *testing.T, f *fragFixture) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "EXTPID", BookID: f.ids["parent"]}))
		}},
	} {
		t.Run("parent linked by "+tc.name+": fragment-only apply leaves it unwritten", func(t *testing.T) {
			t.Parallel()
			f := copyClaimantsFixture(t, true)
			tc.link(t, f)
			before := parentSnapshot(t, f)
			r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			require.Equal(t, repairs.RiskReview, r.Risk)
			require.NotEmpty(t, r.Current["itunes_parent"])
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.False(t, f.live(t, "libA"))
			require.False(t, f.live(t, "libB"))
			require.Equal(t, before, parentSnapshot(t, f), "the parent is not written")
			assertOnlyFragmentsJournaled(t, f, "op-apply", f.ids["libA"], f.ids["libB"])
		})
	}
}

// fragParentSnap is what the fragment-only retire must leave alone.
type fragParentSnap struct {
	Book database.Book
	Rows []database.BookFile
	Exts []database.ExternalIDMapping
	Sync bool
}

func parentSnapshot(t *testing.T, f *fragFixture) fragParentSnap {
	t.Helper()
	id := f.ids["parent"]
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	rows, err := f.s.GetBookFiles(id)
	require.NoError(t, err)
	exts, err := f.s.GetExternalIDsForBook(id)
	require.NoError(t, err)
	_, has, err := f.s.GetSyncIDForBook(id)
	require.NoError(t, err)
	return fragParentSnap{Book: *b, Rows: rows, Exts: exts, Sync: has}
}

func assertOnlyFragmentsJournaled(t *testing.T, f *fragFixture, opID string, frags ...string) {
	t.Helper()
	changes, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	for _, c := range changes {
		require.Contains(t, frags, c.BookID, "only the fragments are written: %+v", c)
		require.NotContains(t, []string{undo.ChangeTypeUserStateFollow, undo.ChangeTypeExternalIDReassign}, c.ChangeType, "%+v", c)
	}
}
