// file: internal/plugins/maintenance/consolidation_leftovers_samepath_test.go
// version: 1.0.0
// guid: f05ffd91-1eeb-4d83-be86-1d16d9d8e1e8
// last-edited: 2026-10-06

package maintenance

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const (
	spSeries   = "lib/Blaine L. Pardoe/Land & Sea"
	spShared   = spSeries + "/35 - Splashdown/35 - Splashdown.m4b"
	spDeadRow  = spSeries + "/35 - Splashdown (old)/35 - Splashdown.m4b"
	spGroupID  = "vg-splashdown"
	spSize     = 4000
	spHash     = "hh-splash"
	spOwnerDur = 942
)

// splashdown seeds the 2026-10-06 shape the owner reviewed (prod leftover
// "35 - Splashdown", 0 min): the leftover's one row names a path that is gone
// from disk, while its book file_path names a file on disk that a live,
// organized book ("35 - Splashdown", 15.7 min) owns through its own row. Both
// sit in one version group, both explicitly primary. A "38 - Splashdown" book
// under the same series folder owns a "_copy14" file of the dead row's exact
// size and hash, which a hash match would pick.
func (f *lfFixture) splashdown(t *testing.T) (leftover, owner, other string) {
	t.Helper()
	yes := true
	gid := spGroupID
	sharedPath := f.file(t, spShared, 5000)
	o, err := f.s.CreateBook(&database.Book{Title: "35 - Splashdown", FilePath: sharedPath})
	require.NoError(t, err)
	f.ids["O"] = o.ID
	f.organized(t, o.ID)
	dur := spOwnerDur
	_, err = f.s.ModifyBook(o.ID, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion, b.Duration = &gid, &yes, &dur
		return nil
	})
	require.NoError(t, err)
	obf := &database.BookFile{BookID: o.ID, FilePath: sharedPath, FileSize: 5000, FileHash: "hh-owner", Duration: spOwnerDur, TrackNumber: 1}
	require.NoError(t, f.s.CreateBookFile(obf))
	f.rowIDs["O"] = obf.ID

	l, err := f.s.CreateBook(&database.Book{Title: "35 - Splashdown", FilePath: sharedPath})
	require.NoError(t, err)
	f.ids["L"] = l.ID
	f.organized(t, l.ID)
	_, err = f.s.ModifyBook(l.ID, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes
		return nil
	})
	require.NoError(t, err)
	lbf := &database.BookFile{BookID: l.ID, FilePath: f.path(spDeadRow), FileSize: spSize, FileHash: spHash, TrackNumber: 1}
	require.NoError(t, f.s.CreateBookFile(lbf))
	f.rowIDs["L"] = lbf.ID

	other = f.combined(t, "C38", spSeries+"/38 - Splashdown",
		map[string]int{"38 - Splashdown_copy14.m4b": spSize, "38 - Splashdown.m4b": 7000},
		map[string]string{"38 - Splashdown_copy14.m4b": spHash})
	return l.ID, o.ID, other
}

// TestLeftoversSamePath_PicksSamePathOwnerOverHashMatch: the leftover folds
// into the live book on its own path, never into the hash-matched book.
func TestLeftoversSamePath_PicksSamePathOwnerOverHashMatch(t *testing.T) {
	f := newLFFixture(t)
	l, o, other := f.splashdown(t)
	res := f.planLF(t, "op-plan")
	r, ok := lfRow(res, l)
	require.True(t, ok)
	require.Equal(t, leftoverClassSamePath, r.Class, r.Reason)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.ElementsMatch(t, []string{l, o}, r.BookIDs, "the hash-matched 38 - Splashdown is no part of the row")
	require.NotContains(t, r.BookIDs, other)
	require.Equal(t, f.path(spShared), r.Current["shared_path"])
	require.Contains(t, r.Current["owner_book"], o)
	require.Contains(t, r.Current["owner_book"], "35 - Splashdown")
	require.Contains(t, r.Current["owner_book"], "15m42s")
	require.NotEmpty(t, r.Current["carries"])

	// The fixture's dead row does hash-match 38 - Splashdown: with the
	// shared file gone the old classes retire the leftover there. The
	// same-path owner is what wins above.
	g := newLFFixture(t)
	gl, _, gother := g.splashdown(t)
	require.NoError(t, os.Remove(g.path(spShared)))
	gr, ok := lfRow(g.planLF(t, "op-plan"), gl)
	require.True(t, ok)
	require.Equal(t, leftoverClassRetire, gr.Class, gr.Reason)
	require.Contains(t, gr.BookIDs, gother)
	require.Equal(t, leftoverBasisSizeHash, gr.Current["match_basis"])

	// A leftover whose book path is on disk with no other owner is still the
	// old hold.
	bp := f.leftover(t, "BP", "lib/A9/S9/09 - Nine/09 - Nine.mp3", 2700, "")
	bpPath := f.file(t, "lib/A9/S9/Nine book/09.mp3", 1)
	_, err := f.s.ModifyBook(bp, func(b *database.Book) error { b.FilePath = bpPath; return nil })
	require.NoError(t, err)
	res = f.planLF(t, "op-plan2")
	r, ok = lfRow(res, bp)
	require.True(t, ok)
	require.Equal(t, leftoverSkipBookPath, r.Skipped, r.SkipReason)
}

// TestLeftoversSamePath_ApplyAndUndo: the apply marks the dead row Missing
// (kept), retires the leftover into the owner with its listening state and
// external ids, leaves the owner the group's one primary; the op revert puts
// every piece back.
func TestLeftoversSamePath_ApplyAndUndo(t *testing.T) {
	f := newLFFixture(t)
	l, o, other := f.splashdown(t)
	require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0SPLASH", BookID: l}))
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, l, f.rowIDs["L"], 100))
	otherRows, err := f.s.GetBookFiles(other)
	require.NoError(t, err)

	f.planLF(t, "op-plan")
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	lb, err := f.s.GetBookByID(l)
	require.NoError(t, err)
	require.True(t, lb.IsSoftDeleted())
	require.NotNil(t, lb.MergedIntoBookID)
	require.Equal(t, o, *lb.MergedIntoBookID)
	rows, err := f.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the leftover's row is kept, never deleted")
	require.True(t, rows[0].Missing)
	require.Equal(t, f.path(spDeadRow), rows[0].FilePath)
	ob, err := f.s.GetBookByID(o)
	require.NoError(t, err)
	require.False(t, ob.IsSoftDeleted())
	require.True(t, database.EffectiveIsPrimaryVersion(ob.IsPrimaryVersion), "the owner is the group's primary")
	require.Equal(t, f.path(spShared), ob.FilePath)
	owner, err := f.s.GetBookByExternalID("audible", "B0SPLASH")
	require.NoError(t, err)
	require.Equal(t, o, owner)
	pos, err := f.s.ListUserPositionsForBook(u.ID, o)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the position followed onto the owner")
	require.InDelta(t, 100, pos[0].PositionSeconds, 0.01, "the whole-book rule: same book, same position")
	after, err := f.s.GetBookFiles(other)
	require.NoError(t, err)
	require.Equal(t, otherRows, after, "38 - Splashdown is untouched")

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	lb, err = f.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, lb.IsSoftDeleted())
	require.Equal(t, f.path(spShared), lb.FilePath)
	require.True(t, database.EffectiveIsPrimaryVersion(lb.IsPrimaryVersion))
	rows, err = f.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].Missing, "the revert restores the Missing flag")
	pos, err = f.s.ListUserPositionsForBook(u.ID, l)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the revert puts the position back")
	owner, err = f.s.GetBookByExternalID("audible", "B0SPLASH")
	require.NoError(t, err)
	require.Equal(t, l, owner)
}

// TestLeftoversSamePath_Holds: every hold the owner named, each its own kind.
func TestLeftoversSamePath_Holds(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, f *lfFixture, l, o string)
		skip  string
	}{
		"two owners": {func(t *testing.T, f *lfFixture, _, _ string) {
			b, err := f.s.CreateBook(&database.Book{Title: "35 - Splashdown copy"})
			require.NoError(t, err)
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path(spShared), FileSize: 5000}))
		}, leftoverSkipSamePathOwners},
		"iTunes copy in the version group": {func(t *testing.T, f *lfFixture, _, _ string) {
			f.itunesSibling(t, nil)
		}, leftoverSkipSamePathITunesGroup},
		"owner is an iTunes book": {func(t *testing.T, f *lfFixture, _, o string) {
			pid := "ABCDEF0123456789"
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathOwnerITunes},
		"leftover audio differs": {func(t *testing.T, f *lfFixture, l, _ string) {
			_, err := f.s.ModifyBookFile(l, f.rowIDs["L"], func(bf *database.BookFile) error { bf.Duration = 3600; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathAudio},
		"owner not listed": {func(t *testing.T, f *lfFixture, _, o string) {
			imported := "imported"
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.LibraryState = &imported; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathNotListed},
		"owner owns only the book path": {func(t *testing.T, f *lfFixture, _, o string) {
			_, err := f.s.ModifyBookFile(o, f.rowIDs["O"], func(bf *database.BookFile) error {
				bf.FilePath = f.path(spSeries + "/35 - Splashdown/other.m4b")
				return nil
			})
			require.NoError(t, err)
		}, leftoverSkipSamePathNoRow},
		"owner-manual owner": {func(t *testing.T, f *lfFixture, _, o string) {
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.Title = "Doctor Who: Splashdown"; return nil })
			require.NoError(t, err)
		}, repairs.SkipOwnerManual},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, o, _ := f.splashdown(t)
			tc.setup(t, f, l, o)
			res := f.planLF(t, "op-plan")
			r, ok := lfRow(res, l)
			require.True(t, ok)
			require.Equal(t, tc.skip, r.Skipped, r.SkipReason)
			require.False(t, r.Applicable())
		})
	}
}

// itunesSibling adds an iTunes copy to the Splashdown version group with the
// given primary flag.
func (f *lfFixture) itunesSibling(t *testing.T, primary *bool) string {
	t.Helper()
	gid := spGroupID
	pid := "0123456789ABCDEF"
	p := f.file(t, "itunes/iTunes Media/Audiobooks/Blaine L. Pardoe/01 Splashdown.m4b", 9000)
	b, err := f.s.CreateBook(&database.Book{Title: "Splashdown", FilePath: p})
	require.NoError(t, err)
	_, err = f.s.ModifyBook(b.ID, func(bk *database.Book) error {
		bk.VersionGroupID, bk.IsPrimaryVersion, bk.ITunesPersistentID = &gid, primary, &pid
		return nil
	})
	require.NoError(t, err)
	return b.ID
}

// TestLeftoversSamePath_ExplicitNonPrimaryITunesSiblingDoesNotHold: only an
// iTunes copy that is NOT explicitly non-primary holds the row.
func TestLeftoversSamePath_ExplicitNonPrimaryITunesSiblingDoesNotHold(t *testing.T) {
	f := newLFFixture(t)
	l, _, _ := f.splashdown(t)
	no := false
	f.itunesSibling(t, &no)
	res := f.planLF(t, "op-plan")
	r, ok := lfRow(res, l)
	require.True(t, ok)
	require.Equal(t, leftoverClassSamePath, r.Class, r.Reason)
	require.Empty(t, r.Skipped, r.SkipReason)
}

// TestLeftoversSamePath_ChangedSincePlan: a second owner, an iTunes copy
// joining the group, or the owner leaving the listing after the plan refuses
// the row at apply with nothing written.
func TestLeftoversSamePath_ChangedSincePlan(t *testing.T) {
	cases := map[string]func(t *testing.T, f *lfFixture, o string){
		"second owner": func(t *testing.T, f *lfFixture, _ string) {
			b, err := f.s.CreateBook(&database.Book{Title: "35 - Splashdown copy", FilePath: f.path(spShared)})
			require.NoError(t, err)
			f.organized(t, b.ID)
		},
		"iTunes copy joins the group": func(t *testing.T, f *lfFixture, _ string) {
			f.itunesSibling(t, nil)
		},
		"owner quarantined": func(t *testing.T, f *lfFixture, o string) {
			no := false
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			require.NoError(t, err)
		},
		"owner file gone": func(t *testing.T, f *lfFixture, _ string) {
			require.NoError(t, os.Remove(f.path(spShared)))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, o, _ := f.splashdown(t)
			planned, ok := lfRow(f.planLF(t, "op-plan"), l)
			require.True(t, ok)
			require.Equal(t, leftoverClassSamePath, planned.Class, planned.Reason)
			require.True(t, planned.Applicable(), planned.SkipReason)
			change(t, f, o)
			out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
			require.Zero(t, out.Applied, "%+v", out.Rows)
			b, err := f.s.GetBookByID(l)
			require.NoError(t, err)
			require.False(t, b.IsSoftDeleted(), "nothing written")
			rows, err := f.s.GetBookFiles(l)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.False(t, rows[0].Missing, "nothing written")
		})
	}
}
