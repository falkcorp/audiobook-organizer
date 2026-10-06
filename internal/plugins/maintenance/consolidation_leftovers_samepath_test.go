// file: internal/plugins/maintenance/consolidation_leftovers_samepath_test.go
// version: 1.6.0
// guid: f05ffd91-1eeb-4d83-be86-1d16d9d8e1e8
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"os"
	"strings"
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
	// No book-level duration, as in prod: the plan row's length comes from
	// the owner's file row.
	_, err = f.s.ModifyBook(o.ID, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes
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
// every piece back, except that the owner keeps its primary flag: the op
// never wrote it (its hand-off kept the owner, already explicit true), so
// the restored leftover yields to it rather than re-crown over it and hide
// the owner from Audiobookshelf.
func TestLeftoversSamePath_ApplyAndUndo(t *testing.T) {
	f := newLFFixture(t)
	l, o, other := f.splashdown(t)
	f.sameAudioBySize(t, l)
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
	f.requireOwnerKeepsPrimary(t, l, o)
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

// requireOwnerKeepsPrimary: after the op revert, the owner is still
// explicit primary and the restored leftover is explicit false, so the
// group's live primaries are exactly [owner].
func (f *lfFixture) requireOwnerKeepsPrimary(t *testing.T, l, o string) {
	t.Helper()
	ob, err := f.s.GetBookByID(o)
	require.NoError(t, err)
	require.NotNil(t, ob.IsPrimaryVersion)
	require.True(t, *ob.IsPrimaryVersion, "the owner keeps the primary flag the op never wrote")
	lb, err := f.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, lb.IsSoftDeleted())
	require.NotNil(t, lb.IsPrimaryVersion)
	require.False(t, *lb.IsPrimaryVersion, "the restored leftover yields to the owner")
	members, err := f.s.GetBooksByVersionGroup(spGroupID)
	require.NoError(t, err)
	var live []string
	for i := range members {
		if !members[i].IsSoftDeleted() && database.EffectiveIsPrimaryVersion(members[i].IsPrimaryVersion) {
			live = append(live, members[i].ID)
		}
	}
	require.Equal(t, []string{o}, live, "the group's live primaries")
}

// sameAudioBySize gives the leftover's dead row the owner file's exact size:
// positive same-audio evidence.
func (f *lfFixture) sameAudioBySize(t *testing.T, l string) {
	t.Helper()
	_, err := f.s.ModifyBookFile(l, f.rowIDs["L"], func(bf *database.BookFile) error { bf.FileSize = 5000; return nil })
	require.NoError(t, err)
}

// finishedReader seeds a user who finished the leftover at 100 s in.
func (f *lfFixture) finishedReader(t *testing.T, l string) string {
	t.Helper()
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, l, f.rowIDs["L"], 100))
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: l,
		Status: database.UserBookStatusFinished, ProgressPct: 100}))
	return u.ID
}

// TestLeftoversSamePath_NoAudioEvidenceCarriesNoFinish: the shared book path
// alone proves nothing about the audio (the dead row is 4000 bytes, the
// owner's file 5000, no hash or length agrees), so a finished leftover never
// finishes the owner and no position lands on it.
func TestLeftoversSamePath_NoAudioEvidenceCarriesNoFinish(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	uid := f.finishedReader(t, l)
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.Current["same_audio_evidence"], "none")
	require.Contains(t, r.Current["leftover_audio"], "4000 bytes")
	require.Contains(t, r.Current["owner_audio"], "5000 bytes")
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	st, err := f.s.GetUserBookState(uid, o)
	require.NoError(t, err)
	if st != nil {
		require.NotEqual(t, database.UserBookStatusFinished, st.Status, "no audio evidence: finished never carries")
	}
	pos, err := f.s.ListUserPositionsForBook(uid, o)
	require.NoError(t, err)
	require.Empty(t, pos, "no audio evidence: no position carries")
}

// TestLeftoversSamePath_AudioEvidenceCarriesWholeBook: an equal size or an
// equal hash is the same audio: the whole-book rule carries finished and the
// position.
func TestLeftoversSamePath_AudioEvidenceCarriesWholeBook(t *testing.T) {
	cases := map[string]func(t *testing.T, f *lfFixture, l string){
		"size": func(t *testing.T, f *lfFixture, l string) { f.sameAudioBySize(t, l) },
		"hash": func(t *testing.T, f *lfFixture, l string) {
			require.NoError(t, f.s.SetBookFileHash(f.rowIDs["L"], "hh-owner"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, o, _ := f.splashdown(t)
			setup(t, f, l)
			uid := f.finishedReader(t, l)
			r, ok := lfRow(f.planLF(t, "op-plan"), l)
			require.True(t, ok)
			require.True(t, r.Applicable(), r.SkipReason)
			require.Contains(t, r.Current["same_audio_evidence"], name)
			out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			st, err := f.s.GetUserBookState(uid, o)
			require.NoError(t, err)
			require.NotNil(t, st)
			require.Equal(t, database.UserBookStatusFinished, st.Status)
			pos, err := f.s.ListUserPositionsForBook(uid, o)
			require.NoError(t, err)
			require.Len(t, pos, 1)
			require.InDelta(t, 100, pos[0].PositionSeconds, 0.01)
		})
	}
}

// TestLeftoversSamePath_MultiFileOwnerSlice: a multi-file owner gets a slice
// at the shared file's place, counted over its LIVE rows only (a Missing row
// with no length does not make it unmappable).
func TestLeftoversSamePath_MultiFileOwnerSlice(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	f.sameAudioBySize(t, l)
	_, err := f.s.ModifyBookFile(o, f.rowIDs["O"], func(bf *database.BookFile) error { bf.TrackNumber = 3; return nil })
	require.NoError(t, err)
	p1 := f.file(t, spSeries+"/35 - Splashdown/part 2.m4b", 100)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: o, FilePath: p1, FileSize: 100, Duration: 600, TrackNumber: 2}))
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: o, FilePath: f.path(spSeries + "/35 - Splashdown/gone.m4b"),
		FileSize: 100, TrackNumber: 1, Missing: true}))
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.Current["carries"], "slice at 600 s")
}

// ownerRowMissingOnDisk gives the owner a second active row (not flagged
// Missing) at part0.m4b; with no file there, versionprimary's eligibility
// rule refuses the owner as a primary.
func (f *lfFixture) ownerRowMissingOnDisk(t *testing.T, o string) {
	t.Helper()
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: o, FilePath: f.path(spSeries + "/35 - Splashdown/part0.m4b"),
		FileSize: 100, Duration: 60, TrackNumber: 2}))
}

// TestLeftoversSamePath_DurationEvidenceCarriesWholeBook: agreeing file
// lengths (sizes and hashes differing) are same-audio evidence.
func TestLeftoversSamePath_DurationEvidenceCarriesWholeBook(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	_, err := f.s.ModifyBookFile(l, f.rowIDs["L"], func(bf *database.BookFile) error { bf.Duration = spOwnerDur; return nil })
	require.NoError(t, err)
	uid := f.finishedReader(t, l)
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.Current["same_audio_evidence"], "duration")
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	st, err := f.s.GetUserBookState(uid, o)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, database.UserBookStatusFinished, st.Status)
}

// TestLeftoversSamePath_BookLengthIsNoEvidence: a book-level length that
// agrees is metadata, not the files: no evidence.
func TestLeftoversSamePath_BookLengthIsNoEvidence(t *testing.T) {
	f := newLFFixture(t)
	l, _, _ := f.splashdown(t)
	d := spOwnerDur
	_, err := f.s.ModifyBook(l, func(b *database.Book) error { b.Duration = &d; return nil })
	require.NoError(t, err)
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.Current["same_audio_evidence"], "none")
}

// TestLeftoversSamePath_NoEvidenceMultiFileOwner: without evidence a
// multi-file owner gets no slice offset either.
func TestLeftoversSamePath_NoEvidenceMultiFileOwner(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	_, err := f.s.ModifyBookFile(o, f.rowIDs["O"], func(bf *database.BookFile) error { bf.TrackNumber = 2; return nil })
	require.NoError(t, err)
	p1 := f.file(t, spSeries+"/35 - Splashdown/part 1.m4b", 100)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: o, FilePath: p1, FileSize: 100, Duration: 600, TrackNumber: 1}))
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.Current["carries"], "never as finished")
	require.NotContains(t, r.Current["carries"], "slice at")
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
		"owner row carries an iTunes path": {func(t *testing.T, f *lfFixture, _, o string) {
			_, err := f.s.ModifyBookFile(o, f.rowIDs["O"], func(bf *database.BookFile) error {
				bf.ITunesPath = "file://localhost/W:/audiobook-organizer/35%20-%20Splashdown.m4b"
				return nil
			})
			require.NoError(t, err)
		}, leftoverSkipSamePathOwnerITunes},
		"titles differ": {func(t *testing.T, f *lfFixture, _, o string) {
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.Title = "36 - Breakwater"; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathIdentity},
		"authors differ": {func(t *testing.T, f *lfFixture, l, o string) {
			a1, err := f.s.CreateAuthor("Blaine L. Pardoe")
			require.NoError(t, err)
			a2, err := f.s.CreateAuthor("Someone Else")
			require.NoError(t, err)
			_, err = f.s.ModifyBook(l, func(b *database.Book) error { b.AuthorID = &a1.ID; return nil })
			require.NoError(t, err)
			_, err = f.s.ModifyBook(o, func(b *database.Book) error { b.AuthorID = &a2.ID; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathIdentity},
		"same-source ids differ": {func(t *testing.T, f *lfFixture, l, o string) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0LEFT", BookID: l}))
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0OWNER", BookID: o}))
		}, leftoverSkipSamePathIdentity},
		"owner in another group, the leftover's group has a live member": {func(t *testing.T, f *lfFixture, _, o string) {
			other := "vg-other"
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.VersionGroupID = &other; return nil })
			require.NoError(t, err)
			f.groupMember(t, "Splashdown (abridged)", nil)
		}, leftoverSkipSamePathPrimary},
		"a third primary in the group, the leftover not primary": {func(t *testing.T, f *lfFixture, l, _ string) {
			yes, no := true, false
			f.groupMember(t, "Splashdown (dramatized)", &yes)
			_, err := f.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathPrimary},
		"ineligible owner next to an eligible explicit non-primary sibling": {func(t *testing.T, f *lfFixture, l, o string) {
			f.sameAudioBySize(t, l)
			no := false
			f.groupMember(t, "Splashdown (abridged)", &no)
			f.ownerRowMissingOnDisk(t, o)
		}, leftoverSkipSamePathPrimary},
		"leading numbers differ": {func(t *testing.T, f *lfFixture, _, o string) {
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.Title = "38 - Splashdown"; return nil })
			require.NoError(t, err)
		}, leftoverSkipSamePathIdentity},
		"owner-manual owner": {func(t *testing.T, f *lfFixture, _, o string) {
			pub := "Big Finish Productions"
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.Publisher = &pub; return nil })
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

// TestLeftoversSamePath_SiblingMergedIntoLeftoverWinsAfterRetire: a live
// sibling whose merged_into_book_id names the leftover is not electable
// while the leftover lives, and is once the retire soft-deletes it. The
// prediction must see the group as the hand-off will (the leftover gone), so
// the row is held naming the sibling the hand-off would crown, never planned
// as the owner's.
func TestLeftoversSamePath_SiblingMergedIntoLeftoverWinsAfterRetire(t *testing.T) {
	cases := map[string]func(t *testing.T, f *lfFixture, l, o string) string{
		"an explicit-true organized sibling": func(t *testing.T, f *lfFixture, l, _ string) string {
			yes := true
			return f.mergedSibling(t, l, "Splashdown (chaptered)", &yes, false)
		},
		"an iTunes explicit-false sibling, owner flag unset": func(t *testing.T, f *lfFixture, l, o string) string {
			_, err := f.s.ModifyBook(o, func(b *database.Book) error { b.IsPrimaryVersion = nil; return nil })
			require.NoError(t, err)
			no := false
			return f.mergedSibling(t, l, "Splashdown (iTunes)", &no, true)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, o, _ := f.splashdown(t)
			x := setup(t, f, l, o)
			r, ok := lfRow(f.planLF(t, "op-plan"), l)
			require.True(t, ok)
			require.False(t, r.Applicable(), "planned although the hand-off would crown %s", x)
			require.Equal(t, leftoverSkipSamePathPrimary, r.Skipped, r.SkipReason)
			require.Contains(t, r.SkipReason, "would crown "+x+", not the owner "+o)
		})
	}
}

// mergedSibling adds a live, organized member of the Splashdown group whose
// merged_into_book_id names the leftover (so it is not electable while the
// leftover lives, and is once the retire soft-deletes it), with one m4b file
// under the library root and a chapter table, so it outranks the owner.
func (f *lfFixture) mergedSibling(t *testing.T, leftover, title string, primary *bool, itunes bool) string {
	t.Helper()
	gid := spGroupID
	p := f.file(t, spSeries+"/"+title+"/"+title+".m4b", 3000)
	b, err := f.s.CreateBook(&database.Book{Title: title, FilePath: p})
	require.NoError(t, err)
	f.organized(t, b.ID)
	into := leftover
	pid := "FEDCBA9876543210"
	_, err = f.s.ModifyBook(b.ID, func(bk *database.Book) error {
		bk.VersionGroupID, bk.IsPrimaryVersion, bk.MergedIntoBookID = &gid, primary, &into
		if itunes {
			bk.ITunesPersistentID = &pid
		}
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: p, FileSize: 3000, TrackNumber: 1}))
	require.NoError(t, f.s.SaveChaptersForBook(b.ID, []database.Chapter{{ID: 1, StartSec: 0, EndSec: 60, Title: "1"},
		{ID: 2, StartSec: 60, EndSec: 120, Title: "2"}}))
	return b.ID
}

// TestLeftoversSamePath_HandOffRefusesAnUnexpectedWinner: the owner turns
// ineligible after the locked re-plan, before the retire's hand-off. The
// hand-off expects the owner, so it crowns nobody: the row stops as
// partially applied, and the eligible explicit non-primary sibling is not
// crowned.
func TestLeftoversSamePath_HandOffRefusesAnUnexpectedWinner(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	f.sameAudioBySize(t, l)
	no := false
	sib := f.groupMember(t, "Splashdown (abridged)", &no)
	f.file(t, spSeries+"/35 - Splashdown/part0.m4b", 100)
	f.ownerRowMissingOnDisk(t, o)
	planned, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, planned.Applicable(), planned.SkipReason)
	leftoverBeforeRetireHooks.Store(l, func() {
		_ = os.Remove(f.path(spSeries + "/35 - Splashdown/part0.m4b"))
	})
	t.Cleanup(func() { leftoverBeforeRetireHooks.Delete(l) })
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Len(t, out.Rows, 1)
	require.Equal(t, repairs.OutcomePartial, out.Rows[0].Outcome, "%+v", out.Rows[0])
	require.Contains(t, out.Rows[0].Error, "not the expected")
	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion, "the sibling was never crowned")

	// The op revert: the refused hand-off wrote no member's flag (its
	// refusal note says so), so the owner keeps its flag and the restored
	// leftover yields to it.
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	f.requireOwnerKeepsPrimary(t, l, o)
	sb, err = f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion)
}

// TestLeftoversSamePath_HandOffNeverWritesAnITunesMember: an explicit-false
// iTunes copy in the group passes the apply's locked iTunes check, then its
// flag turns nil before the retire's hand-off (the window the slower
// user-state follow opens). The hand-off would demote it -- a write to an
// iTunes book's primary flag -- so it refuses under the group lock with
// nothing written: the copy stays nil, the owner keeps its flag, the row
// stops as partially applied. The op revert then leaves the owner primary.
func TestLeftoversSamePath_HandOffNeverWritesAnITunesMember(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	f.sameAudioBySize(t, l)
	no := false
	it := f.itunesSibling(t, &no)
	planned, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, planned.Applicable(), planned.SkipReason)
	leftoverBeforeRetireHooks.Store(l, func() {
		_, err := f.s.ModifyBook(it, func(b *database.Book) error { b.IsPrimaryVersion = nil; return nil })
		require.NoError(t, err)
	})
	t.Cleanup(func() { leftoverBeforeRetireHooks.Delete(l) })
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Len(t, out.Rows, 1)
	require.Equal(t, repairs.OutcomePartial, out.Rows[0].Outcome, "%+v", out.Rows[0])
	require.Contains(t, out.Rows[0].Error, "iTunes copy "+it)
	ib, err := f.s.GetBookByID(it)
	require.NoError(t, err)
	require.Nil(t, ib.IsPrimaryVersion, "the iTunes copy's flag was never written")
	ob, err := f.s.GetBookByID(o)
	require.NoError(t, err)
	require.NotNil(t, ob.IsPrimaryVersion)
	require.True(t, *ob.IsPrimaryVersion)

	// Restore the copy's explicit false (a user's fix) so the group the
	// revert judges has one real primary candidate besides the leftover.
	_, err = f.s.ModifyBook(it, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	f.requireOwnerKeepsPrimary(t, l, o)
}

// TestLeftoversSamePath_ChosenWinnerIsFingerprinted: the hand-off's chosen
// winner alone changes the row's fingerprint.
func TestLeftoversSamePath_ChosenWinnerIsFingerprinted(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	no := false
	f.groupMember(t, "Splashdown (abridged)", &no)
	plan := func(winner string) repairs.Row {
		return f.decideLFWithPrimary(t, l, func(context.Context, []database.Book) (string, error) { return winner, nil })
	}
	a, b, c := plan(o), plan("X1"), plan("X2")
	require.True(t, a.Applicable(), a.SkipReason)
	require.NotEqual(t, a.Fingerprint, b.Fingerprint)
	require.NotEqual(t, b.Fingerprint, c.Fingerprint, "only the chosen winner differs")

	// A non-primary leftover whose owner is not the group's one explicit
	// primary (here: its flag unset, so it is listed but not explicit) is
	// held with a reason that does not name the predicted winner,
	// so only the hand-off line itself can tell X1 from X2.
	_, err := f.s.ModifyBook(l, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	_, err = f.s.ModifyBook(o, func(b *database.Book) error { b.IsPrimaryVersion = nil; return nil })
	require.NoError(t, err)
	d, e := plan("X1"), plan("X2")
	require.False(t, d.Applicable())
	require.Equal(t, leftoverSkipSamePathPrimary, d.Skipped, d.SkipReason)
	require.Equal(t, d.SkipReason, e.SkipReason, "the reason does not name the winner")
	require.NotEqual(t, d.Fingerprint, e.Fingerprint, "the chosen winner alone changes the fingerprint")
}

// TestLeftoversSamePath_NoVersionPrimaryStoreHoldsTheRow: with no
// version-primary store the hand-off cannot be predicted, so the row is held
// with that reason and the plan goes on (no error).
func TestLeftoversSamePath_NoVersionPrimaryStoreHoldsTheRow(t *testing.T) {
	f := newLFFixture(t)
	l, _, _ := f.splashdown(t)
	no := false
	f.groupMember(t, "Splashdown (abridged)", &no)
	r := f.decideLFWithPrimary(t, l, func(context.Context, []database.Book) (string, error) {
		return "", errLeftoverNoVersionPrimaryStore
	})
	require.False(t, r.Applicable())
	require.Equal(t, leftoverSkipSamePathPrimary, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "version-primary store is not available")
}

// decideLFWithPrimary decides leftover l's row with the fixer's source
// reading f's store directly and its hand-off prediction replaced by
// primary.
func (f *lfFixture) decideLFWithPrimary(t *testing.T, l string, primary func(context.Context, []database.Book) (string, error)) repairs.Row {
	t.Helper()
	src, err := newConsolidationLeftoversFixer(f.p).newSource(f.s)
	require.NoError(t, err)
	src.primary = primary
	src.book = func(id string) (*database.BookCore, error) {
		b, err := f.s.GetBookByID(id)
		if err != nil || b == nil {
			return nil, err
		}
		c := b.Core()
		return &c, nil
	}
	src.rows = func(id string) ([]database.BookFileCore, error) {
		fs, err := f.s.GetBookFiles(id)
		out := make([]database.BookFileCore, len(fs))
		for i := range fs {
			out[i] = fs[i].Core()
		}
		return out, err
	}
	src.same = func(int64, string) ([]database.BookFileCore, error) { return nil, nil }
	src.owners = func(p string) ([]string, error) { return leftoverFreshPathOwners(f.s, p) }
	r, ok, err := src.decide(context.Background(), l, nil)
	require.NoError(t, err)
	require.True(t, ok)
	return r
}

// groupMember adds a live, organized, non-iTunes member to the Splashdown
// version group with the given primary flag.
func (f *lfFixture) groupMember(t *testing.T, title string, primary *bool) string {
	t.Helper()
	gid := spGroupID
	p := f.file(t, spSeries+"/"+title+"/"+title+".m4b", 3000)
	b, err := f.s.CreateBook(&database.Book{Title: title, FilePath: p})
	require.NoError(t, err)
	f.organized(t, b.ID)
	_, err = f.s.ModifyBook(b.ID, func(bk *database.Book) error {
		bk.VersionGroupID, bk.IsPrimaryVersion = &gid, primary
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: p, FileSize: 3000, TrackNumber: 1}))
	return b.ID
}

// TestLeftoversSamePath_ExplicitNonPrimaryGroupMemberDoesNotHold: a third
// member explicitly non-primary leaves the owner the group's sole primary.
func TestLeftoversSamePath_ExplicitNonPrimaryGroupMemberDoesNotHold(t *testing.T) {
	f := newLFFixture(t)
	l, _, _ := f.splashdown(t)
	no := false
	f.groupMember(t, "Splashdown (abridged)", &no)
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.True(t, r.Applicable(), r.SkipReason)
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

// TestLeftoversSamePath_MissingRowIsNoOwner: a book whose only reference to
// the path is a row already marked Missing does not own the file; it is
// named in the evidence and the row stays applicable.
func TestLeftoversSamePath_MissingRowIsNoOwner(t *testing.T) {
	f := newLFFixture(t)
	l, o, _ := f.splashdown(t)
	b, err := f.s.CreateBook(&database.Book{Title: "38 - Splashdown stale"})
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path(spShared), FileSize: 5000, Missing: true}))
	r, ok := lfRow(f.planLF(t, "op-plan"), l)
	require.True(t, ok)
	require.Equal(t, leftoverClassSamePath, r.Class, r.Reason)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Contains(t, r.Current["owner_book"], o)
	require.Contains(t, strings.Join(r.Evidence, "\n"), b.ID)
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
		"a third primary joins the group": func(t *testing.T, f *lfFixture, _ string) {
			yes := true
			f.groupMember(t, "Splashdown (dramatized)", &yes)
		},
		"owner turns ineligible next to an explicit non-primary sibling": func(t *testing.T, f *lfFixture, _ string) {
			require.NoError(t, os.Remove(f.path(spSeries+"/35 - Splashdown/part0.m4b")))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, o, _ := f.splashdown(t)
			if name == "owner turns ineligible next to an explicit non-primary sibling" {
				f.sameAudioBySize(t, l)
				no := false
				f.groupMember(t, "Splashdown (abridged)", &no)
				f.file(t, spSeries+"/35 - Splashdown/part0.m4b", 100)
				f.ownerRowMissingOnDisk(t, o)
			}
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
