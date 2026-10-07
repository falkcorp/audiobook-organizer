// file: internal/plugins/maintenance/itunes_stale_path_twin_test.go
// version: 1.0.0
// guid: 389e1da9-89bf-4ccc-bd83-816813eaccf9
// last-edited: 2026-10-07

package maintenance

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Synthetic placeholders shaped like the prod case (2026-10-07): a parent
// whose rows' iTunes paths back no track, a library clone of one of its
// files carrying an iTunes path that backs no track either, and the
// clone's source, the real iTunes Media file, as a non-primary twin in the
// clone's version group.
const (
	twParent02 = "lib/Unknown Author/Many Parts/02.mp3"
	twFrag     = "lib/Many Parts/02/02.mp3"
	twTwin     = "books/itunes/iTunes Media/Audiobooks/Placeholder Author/02.mp3"
	// Where iTunes has the twin's track (imported and write-back library).
	twTrackURL    = "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Placeholder%20Author/02.mp3"
	twTrackNative = `W:\itunes\iTunes Media\Audiobooks\Placeholder Author\02.mp3`
	// The clone's stale iTunes path (names its own file, no track there).
	twFragITunes = "file://localhost/W:/audiobook-organizer/Many%20Parts/02/02.mp3"
	twGroup      = "vg-placeholder-02"
)

func twinFixture(t *testing.T) *staleFixture {
	t.Helper()
	f := newStaleFixture(t, []string{twTrackURL}, []string{twTrackNative})
	p1 := f.file(t, "lib/Unknown Author/Many Parts/01.mp3", 2001)
	p2 := f.file(t, twParent02, 2002)
	parent := f.book(t, "parent", "Many Parts", f.path("lib/Unknown Author/Many Parts"), nil)
	f.row(t, "p01", parent, p1, "01.mp3", 2001, 600, 1)
	f.row(t, "p02", parent, p2, "02.mp3", 2002, 600, 2)
	// The parent is iTunes-linked by its rows' computed iTunes paths, which
	// no track backs either.
	for _, role := range []string{"p01", "p02"} {
		f.updateRow(t, parent, f.rowIDs[role], func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/W:/audiobook-organizer/Unknown%20Author/Many%20Parts/" + r.OriginalFilename
		})
	}
	fp := f.file(t, twFrag, 2002)
	frag := f.book(t, "frag", "02", fp, nil)
	f.row(t, "frag", frag, fp, "02.mp3", 2002, 590, 0)
	f.updateRow(t, frag, f.rowIDs["frag"], func(r *database.BookFile) { r.ITunesPath = twFragITunes })
	tp := f.file(t, twTwin, 2002)
	twin := f.book(t, "twin", "02", tp, nil)
	f.row(t, "twin", twin, tp, "02.mp3", 2002, 590, 0)
	f.writeSame(t, "same", twParent02, twFrag, twTwin)
	digest, err := filehash.BookFileHash(tp)
	require.NoError(t, err)
	g, yes, no := twGroup, true, false
	_, err = f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion = &g, &yes
		return nil
	})
	require.NoError(t, err)
	_, err = f.s.ModifyBook(twin, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion = &g, &no
		b.FileHash, b.OriginalFileHash = &digest, &digest
		return nil
	})
	require.NoError(t, err)
	return f
}

// clearFragPath runs the stale-itunes-path trial and apply on the fragment
// alone (the prod trial is the fragments, never the parent).
func (f *staleFixture) clearFragPath(t *testing.T) {
	t.Helper()
	sp := f.fixerPlan(t, staleITPFixerID, "op-sp", staleITPParams{BookIDs: []string{f.ids["frag"]}})
	r := findRow(t, sp, f.ids["frag"])
	require.True(t, r.Applicable(), r.SkipReason)
	out := f.fixerApply(t, staleITPFixerID, "op-sp", "op-sp-apply", []string{r.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Empty(t, f.rowITunesPath(t, "frag"))
}

func (f *staleFixture) book0(t *testing.T, role string) database.Book {
	t.Helper()
	b, err := f.s.GetBookByID(f.ids[role])
	require.NoError(t, err)
	require.NotNil(t, b)
	return *b
}

// The prod shape end to end: the stale path is cleared, the fragment and
// its iTunes twin go on the parent's owner row, bulk refuses it, the
// owner's grant retires the twin then the fragment (database rows only),
// the parent and every file are untouched, and the op revert restores both.
func TestStaleITunesPath_E2E_TwinRetiredByOwner(t *testing.T) {
	t.Parallel()
	f := twinFixture(t)
	parent, frag, twin := f.ids["parent"], f.ids["frag"], f.ids["twin"]

	f.clearFragPath(t)

	p0 := f.book0(t, "parent")
	rows0, err := f.s.GetBookFiles(parent)
	require.NoError(t, err)
	twinFile0, err := os.ReadFile(f.path(twTwin))
	require.NoError(t, err)

	res := f.plan(t, "op-plan")
	m := findRow(t, res, fragRowOwner+":"+parent)
	require.False(t, m.Applicable())
	require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
	require.Equal(t, []string{twin}, m.OwnerITunesDatabaseOnly)
	require.ElementsMatch(t, []string{frag, twin}, m.OwnerWrites)
	require.Contains(t, m.BookIDs, twin)
	require.Contains(t, m.OwnerApplyReason, "iTunes twin")
	roles := map[string]string{}
	for _, mm := range m.Members {
		roles[mm.BookID] = mm.Role
	}
	require.Equal(t, "itunes-twin", roles[twin])
	for _, r := range res.Rows {
		if r.RowID != m.RowID {
			require.NotContains(t, r.BookIDs, frag, "the fragment is on the owner row alone: %s", r.RowID)
			require.NotContains(t, r.BookIDs, twin, "the twin is on the owner row alone: %s", r.RowID)
		}
	}

	out := f.apply(t, "op-plan", "op-bulk", []string{m.RowID}, nil)
	require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome)
	require.True(t, f.live(t, "frag"))
	require.True(t, f.live(t, "twin"))

	out = f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	for _, role := range []string{"frag", "twin"} {
		b := f.book0(t, role)
		require.True(t, b.IsSoftDeleted(), role)
		require.NotNil(t, b.MergedIntoBookID, role)
		require.Equal(t, parent, *b.MergedIntoBookID, role)
		rows, err := f.s.GetBookFiles(f.ids[role])
		require.NoError(t, err)
		require.Len(t, rows, 1, "%s keeps its book_file row", role)
	}
	require.Equal(t, p0, f.book0(t, "parent"), "the parent book is not written")
	rows1, err := f.s.GetBookFiles(parent)
	require.NoError(t, err)
	require.Equal(t, rows0, rows1, "the parent's rows are not written")
	twinFile1, err := os.ReadFile(f.path(twTwin))
	require.NoError(t, err)
	require.Equal(t, twinFile0, twinFile1, "the iTunes file is not touched")
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	for _, c := range changes {
		require.Contains(t, []string{frag, twin}, c.BookID, "only the fragment and the twin are written: %+v", c)
	}

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	for role, primary := range map[string]bool{"frag": true, "twin": false} {
		require.True(t, f.live(t, role), "the op revert restores %s", role)
		b := f.book0(t, role)
		require.True(t, b.MergedIntoBookID == nil || *b.MergedIntoBookID == "", role)
		require.NotNil(t, b.IsPrimaryVersion, role)
		require.Equal(t, primary, *b.IsPrimaryVersion, "%s keeps its primary flag", role)
		require.Equal(t, twGroup, *b.VersionGroupID, role)
	}
}

// Every twin check refuses: the fragment does not go on the owner row with
// that book, and the owner's grant retires nothing.
func TestStaleITunesPath_TwinRefusals(t *testing.T) {
	t.Parallel()
	setTwin := func(fn func(*database.Book)) func(*testing.T, *staleFixture) {
		return func(t *testing.T, f *staleFixture) {
			_, err := f.s.ModifyBook(f.ids["twin"], func(b *database.Book) error { fn(b); return nil })
			require.NoError(t, err)
		}
	}
	setTwinRow := func(fn func(*database.BookFile)) func(*testing.T, *staleFixture) {
		return func(t *testing.T, f *staleFixture) { f.updateRow(t, f.ids["twin"], f.rowIDs["twin"], fn) }
	}
	cases := map[string]func(*testing.T, *staleFixture){
		"twin primary":            setTwin(func(b *database.Book) { yes := true; b.IsPrimaryVersion = &yes }),
		"twin primary flag unset": setTwin(func(b *database.Book) { b.IsPrimaryVersion = nil }),
		"twin book PID":           setTwin(func(b *database.Book) { pid := "TWINPID0"; b.ITunesPersistentID = &pid }),
		"twin hash mismatch": setTwin(func(b *database.Book) {
			h := "0000000000000000000000000000000000000000000000000000000000000000"
			b.FileHash, b.OriginalFileHash = &h, &h
		}),
		"twin row PID":         setTwinRow(func(r *database.BookFile) { r.ITunesPersistentID = "TWINROWPID" }),
		"twin row iTunes path": setTwinRow(func(r *database.BookFile) { r.ITunesPath = twTrackURL }),
		"twin itunes external id (tombstoned)": func(t *testing.T, f *staleFixture) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "DEADTWIN", BookID: f.ids["twin"], Tombstoned: true}))
		},
		"twin live external id": func(t *testing.T, f *staleFixture) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0TWIN", BookID: f.ids["twin"]}))
		},
		"twin listening state": func(t *testing.T, f *staleFixture) {
			u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
			require.NoError(t, err)
			require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["twin"], f.rowIDs["twin"], 100))
		},
		"twin file a different size on disk": func(t *testing.T, f *staleFixture) {
			require.NoError(t, os.WriteFile(f.path(twTwin), fragFixtureBytes("same", 4096), 0o644))
		},
		"twin with a second file row": func(t *testing.T, f *staleFixture) {
			extra := f.file(t, "books/itunes/iTunes Media/Audiobooks/Placeholder Author/02b.mp3", 2003)
			f.row(t, "twin2", f.ids["twin"], extra, "02b.mp3", 2003, 10, 0)
		},
		"a third member in the group": func(t *testing.T, f *staleFixture) {
			op := f.file(t, "lib/Other/02.mp3", 2004)
			other := f.book(t, "other", "02 other", op, nil)
			f.row(t, "other", other, op, "02.mp3", 2004, 590, 0)
			f.setVG(t, other, twGroup, false)
		},
		"Doctor Who twin": setTwin(func(b *database.Book) { b.Title = "Doctor Who: Placeholder" }),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := twinFixture(t)
			f.clearFragPath(t)
			mutate(t, f)
			res := f.plan(t, "op-plan")
			for _, r := range res.Rows {
				if r.OwnerApplicable {
					require.NotContains(t, r.BookIDs, f.ids["twin"], "%s: twin on an owner row: %s", r.RowID, r.SkipReason)
					require.NotContains(t, r.OwnerITunesDatabaseOnly, f.ids["twin"], r.RowID)
				}
			}
			r := rowWithBook(t, res.Rows, f.ids["frag"])
			require.False(t, r.Applicable(), "%s: %s", r.RowID, r.SkipReason)
			out := f.ownerApply(t, "op-plan", "op-apply", []string{r.RowID}, "", nil)
			require.Zero(t, out.Applied, "%+v", out.Rows)
			require.True(t, f.live(t, "frag"))
			require.True(t, f.live(t, "twin"))
			if name == "a third member in the group" {
				require.Contains(t, r.SkipReason, "not owner-applicable", "D9: the reason it is not the owner's is shown")
			}
		})
	}
}

// Changes landing after the plan are refused under the merge lock, and
// nothing is written.
func TestStaleITunesPath_TwinChangedSincePlan(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*testing.T, *staleFixture){
		"twin gains a PID": func(t *testing.T, f *staleFixture) { setBookPID(t, f.fragFixture, f.ids["twin"], "TWINPID1") },
		"twin gains listening state": func(t *testing.T, f *staleFixture) {
			u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
			require.NoError(t, err)
			require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["twin"], f.rowIDs["twin"], 100))
		},
		"a member joins the group": func(t *testing.T, f *staleFixture) {
			op := f.file(t, "lib/Other/02.mp3", 2004)
			other := f.book(t, "other", "02 other", op, nil)
			f.row(t, "other", other, op, "02.mp3", 2004, 590, 0)
			f.setVG(t, other, twGroup, false)
		},
		"twin made primary": func(t *testing.T, f *staleFixture) { f.setVG(t, f.ids["twin"], twGroup, true) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := twinFixture(t)
			f.clearFragPath(t)
			m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
			require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
			mutate(t, f)
			out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
			require.Zero(t, out.Applied, "%+v", out.Rows)
			require.True(t, f.live(t, "frag"))
			require.True(t, f.live(t, "twin"))
			changes, err := f.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			for _, c := range changes {
				require.NotEqual(t, "soft_delete", c.ChangeType, "%+v", c)
			}
		})
	}
}

// Before the clear the fragment's own (stale) iTunes path names its own
// file, so the owner row already holds it with its twin; the clear does
// not change that the twin rides with it.
func TestStaleITunesPath_TwinOwnerRowBeforeClear(t *testing.T) {
	t.Parallel()
	f := twinFixture(t)
	m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
	require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
	require.Equal(t, []string{f.ids["twin"]}, m.OwnerITunesDatabaseOnly)
}

// A twinless owner row whose fragment loses its iTunes path after the plan
// (a stale-path clear landing in between) is no longer the owner's to
// apply: refused, nothing written.
func TestStaleITunesPath_OwnerRowPathClearedSincePlan(t *testing.T) {
	t.Parallel()
	f := ownerFixture(t, true)
	m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
	require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
	require.Empty(t, m.OwnerITunesDatabaseOnly)
	f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) { r.ITunesPath = "" })
	out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "libA"))
}
