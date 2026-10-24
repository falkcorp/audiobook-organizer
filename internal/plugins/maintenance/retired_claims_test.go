// file: internal/plugins/maintenance/retired_claims_test.go
// version: 1.0.1
// guid: c41f7a0e-2b95-4d63-8e1a-5f90d3b7a264
// last-edited: 2026-10-24

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireFileOnto creates a row for path under a fresh shell book and retires it
// into holderID, the way the retire-versions repair does: the row ends up
// keyed under the holder, flagged Retired, tied to a retired version. It
// returns the retired row. The path index is left naming the retired row (no
// live row carries the path), which is exactly the state in which
// GetBookFileByPath answers nil for a file a retired version holds.
func retireFileOnto(t *testing.T, ps *database.PebbleStore, holderID, path string) *database.BookFile {
	t.Helper()
	shell, err := ps.CreateBook(&database.Book{Title: "Retired Shell", FilePath: filepath.Join(t.TempDir(), "shell")})
	require.NoError(t, err)
	now := time.Now()
	v, err := ps.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-test", RetiredAt: &now,
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: path, Format: "mp3", Duration: 100, FileSize: 1000}
	require.NoError(t, ps.CreateBookFile(row))
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, v.ID))
	got, err := ps.GetBookFileByID(holderID, row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Retired)
	// The premise of every test below: the single-owner getter cannot see it.
	hidden, err := ps.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, hidden, "GetBookFileByPath must not return a retired row")
	return got
}

// A file held only by a retired version is not free: fs-regroup-xml must refuse
// the group rather than create a second, live row for the gap on it. The gap
// lookup used GetBookFileByPath, which never returns a retired row, so the
// "held as a retired version" guard could not fire and a second row was made.
func TestFsRegroupApply_GapOnRetiredVersionFileCreatesNoSecondRow(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	holder := fsSeedBook(t, s, "Retire Holder", filepath.Join(t.TempDir(), "holder"))
	// Member 2 has no row of its own: its file is the gap, and a retired version
	// of another book holds it.
	gapPath := filepath.Join(fsFragBase(t), "Cage of Souls - 2", "3.mp3")
	require.FileExists(t, gapPath)
	retired := retireFileOnto(t, s, holder, gapPath)

	plan := fsPlan(t, s)
	frag := fsGroupsOf(plan, fsCatFragments)
	require.Len(t, frag, 1, "the fragment group is still planned: the plan reads live rows only (%s)", plan.summary())
	liveBefore := fsRowCount(t, s)

	res, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-retired-gap"})
	require.NoError(t, err)
	require.Zero(t, res.RowsCreated, "no row may be created on a retired version's file: %s", res)

	at, err := s.BookFilesAtPath(gapPath)
	require.NoError(t, err)
	require.Len(t, at, 1, "exactly one row (the retired one) may sit on the file")
	require.Equal(t, retired.ID, at[0].ID)
	require.Equal(t, liveBefore, fsRowCount(t, s), "no live row was added")
	for _, id := range ids {
		require.False(t, fsSoftDeleted(t, s, id), "a refused group soft-deletes nothing")
	}
}

// The lookup error path now rides on the complete lookup, so the fixture's
// injected error must reach it too.
func TestFsRegroupApply_CompleteLookupErrorSkipsGroup(t *testing.T) {
	s := regroupStore(t)
	seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	g := &fsGuardStore{PebbleStore: s, atPathRowsErr: context.DeadlineExceeded}
	plan := fsPlan(t, g)
	before := fsRowCount(t, s)
	res, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-lookup2"})
	require.Error(t, err)
	require.Zero(t, res.Groups)
	require.Equal(t, before, fsRowCount(t, s))
}

// A junk-titled book whose only file is held by a retired version of another
// book is a fragment (occupied), not a book to retitle. GetBookFileByPath
// answered nil for it, so the book was not classified and the fixer went on to
// act on it.
func TestJunkTitleFixer_FileHeldOnlyByRetiredVersionIsAFragment(t *testing.T) {
	lib := newJunkLib(t)
	holder, err := lib.store.CreateBook(&database.Book{Title: "Retire Holder", FilePath: "/lib/Retire/Holder"})
	require.NoError(t, err)
	// "Opening" is a chapter-style junk title; the book has no rows, so its
	// path list is its own FilePath.
	const heldPath = "/lib/Retire/Held/x.mp3"
	b, err := lib.store.CreateBook(&database.Book{Title: "Opening", FilePath: heldPath, Format: "mp3"})
	require.NoError(t, err)
	lib.ids["retired-held"] = b.ID
	retired := retireFileOnto(t, lib.store, holder.ID, heldPath)

	_, _, rows := lib.plan(t)
	r, ok := rows["retired-held"]
	require.True(t, ok, "no plan row for the book")
	require.Equal(t, junkSkipFragment, r.Skipped, "skip reason: %s", r.SkipReason)
	require.Contains(t, r.SkipReason, "held as a retired version of book "+holder.ID)
	require.Equal(t, holder.ID, retired.BookID)
}

// The leftover walk identifies the LIVE twin of a leftover's audio: a retired
// row at a same-size file is not an owner, and a live row still is. The guard
// that says so now reads a lookup that can return a retired row.
func TestLeftoverWalkOwners_RetiredRowIsNotAnOwner(t *testing.T) {
	ps := regroupStore(t)
	dir := t.TempDir()
	retiredPath := filepath.Join(dir, "retired.mp3")
	livePath := filepath.Join(dir, "live.mp3")
	for _, p := range []string{retiredPath, livePath} {
		require.NoError(t, os.WriteFile(p, []byte("12345"), 0o644))
	}
	holder, err := ps.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(dir, "h")})
	require.NoError(t, err)
	liveBook, err := ps.CreateBook(&database.Book{Title: "Live", FilePath: livePath})
	require.NoError(t, err)
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: liveBook.ID, FilePath: livePath, Format: "mp3"}))
	retired := retireFileOnto(t, ps, holder.ID, retiredPath)

	got, err := leftoverWalkOwners(ps, dir, 5, map[string]bool{})
	require.NoError(t, err)
	require.Len(t, got, 1, "only the live row is an owner: %+v", got)
	require.Equal(t, liveBook.ID, got[0].BookID)
	require.NotEqual(t, retired.ID, got[0].ID)
}

// A file held by a retired version is not free for build-folder-book-files,
// even when the retired version is keyed under the very book being built (a
// live book with only retired rows) or under a soft-deleted holder.
func TestBuildFolderBookFiles_FileHeldByRetiredVersionIsNotBuilt(t *testing.T) {
	cases := []struct {
		name   string
		holder func(t *testing.T, s *fbSpyStore, target *database.Book) string
	}{
		{"holder-is-another-live-book", func(t *testing.T, s *fbSpyStore, _ *database.Book) string {
			b, err := s.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(t.TempDir(), "h")})
			require.NoError(t, err)
			return b.ID
		}},
		{"holder-is-the-book-being-built", func(_ *testing.T, _ *fbSpyStore, target *database.Book) string { return target.ID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFolderBuildStore(t)
			stubProbe(t, map[string]int{"01.mp3": 100, "02.mp3": 200})
			dir := makeFolder(t, "01.mp3", "02.mp3")
			target := absBook(t, s, "Target", dir)
			holderID := tc.holder(t, s, target)
			held := filepath.Join(dir, "02.mp3")
			retired := retireFileOnto(t, s.PebbleStore, holderID, held)

			r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{Apply: true, BookIDs: []string{target.ID}}, &fakeReporter{})
			require.NoError(t, err)
			e := onlyEntry(t, r, target.ID)
			require.Equal(t, fbBuilt, e.Decision)
			require.Equal(t, 1, e.FileCount, "only the free file is built")
			require.Equal(t, 1, e.OwnedSkippedCount)
			require.Len(t, e.OwnedSkipped, 1)
			require.Equal(t, held, e.OwnedSkipped[0].Path)
			require.Equal(t, holderID, e.OwnedSkipped[0].RetiredHolderBookID, "the report names the retired version's holder")

			at, err := s.BookFilesAtPath(held)
			require.NoError(t, err)
			require.Len(t, at, 1, "no second row on the retired version's file")
			require.Equal(t, retired.ID, at[0].ID)
		})
	}
}

// The repoint ops' path claim: a retired row claims its file whoever it is keyed
// under, the book being repaired and a holder that is not a live book included;
// a live row of another live book still claims it by name; a free path is free.
func TestRfPathClaim_RetiredRowIsAClaimWhoeverHoldsIt(t *testing.T) {
	ps := regroupStore(t)
	dir := t.TempDir()
	repaired, err := ps.CreateBook(&database.Book{Title: "Repaired", FilePath: filepath.Join(dir, "r")})
	require.NoError(t, err)
	other, err := ps.CreateBook(&database.Book{Title: "Other live", FilePath: filepath.Join(dir, "o")})
	require.NoError(t, err)
	deadHolder, err := ps.CreateBook(&database.Book{Title: "Dead holder", FilePath: filepath.Join(dir, "d")})
	require.NoError(t, err)

	byOther := filepath.Join(dir, "by-other.mp3")
	byRepairedHolder := filepath.Join(dir, "by-self.mp3")
	byDeadHolder := filepath.Join(dir, "by-dead.mp3")
	byLiveHolder := filepath.Join(dir, "by-live.mp3")
	free := filepath.Join(dir, "free.mp3")
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: byOther, Format: "mp3"}))
	retireFileOnto(t, ps, repaired.ID, byRepairedHolder)
	retireFileOnto(t, ps, deadHolder.ID, byDeadHolder)
	retireFileOnto(t, ps, other.ID, byLiveHolder)

	liveBooks := map[string]bool{repaired.ID: true, other.ID: true} // deadHolder is not a live book

	claim, err := rfPathClaim(ps, liveBooks, repaired.ID, byOther)
	require.NoError(t, err)
	require.Equal(t, rfClaim{OtherBookID: other.ID}, claim)

	claim, err = rfPathClaim(ps, liveBooks, repaired.ID, byRepairedHolder)
	require.NoError(t, err)
	require.Equal(t, rfClaim{RetiredHolderBookID: repaired.ID}, claim, "the book's own retired version still claims the file")

	claim, err = rfPathClaim(ps, liveBooks, repaired.ID, byDeadHolder)
	require.NoError(t, err)
	require.Equal(t, rfClaim{RetiredHolderBookID: deadHolder.ID}, claim, "a holder that is not a live book still holds the file")

	claim, err = rfPathClaim(ps, liveBooks, repaired.ID, byLiveHolder)
	require.NoError(t, err)
	require.True(t, claim.held())
	require.Equal(t, other.ID, claim.RetiredHolderBookID)
	require.Empty(t, claim.OtherBookID, "a live holder is not named as a second live owner on the strength of a retired row")

	claim, err = rfPathClaim(ps, liveBooks, repaired.ID, free)
	require.NoError(t, err)
	require.False(t, claim.held())
}

// While no retired version exists, the gap check does not need the complete
// lookup: no retired row can hold the file, so the group still applies while
// memdb is warming up (the complete lookup reports unavailable).
func TestFsRegroupApply_CompleteLookupDownWithNoRetiredVersionsStillApplies(t *testing.T) {
	s := regroupStore(t)
	seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	g := &fsGuardStore{PebbleStore: s, atPathRowsErr: database.ErrBookFilesAtPathUnavailable}
	plan := fsPlan(t, g)
	res, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-warm-noretired"})
	require.NoError(t, err)
	require.NotZero(t, res.Groups, "the group applies: %s", res)
}

// Once a retired version exists the complete lookup is required, and with it
// unavailable the group is refused before any write.
func TestFsRegroupApply_CompleteLookupDownWithRetiredVersionsRefusesTheGroup(t *testing.T) {
	s := regroupStore(t)
	seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	holder := fsSeedBook(t, s, "Retire Holder", filepath.Join(t.TempDir(), "holder"))
	retireFileOnto(t, s, holder, filepath.Join(t.TempDir(), "elsewhere.mp3"))
	g := &fsGuardStore{PebbleStore: s, atPathRowsErr: database.ErrBookFilesAtPathUnavailable}
	plan := fsPlan(t, g)
	before := fsRowCount(t, s)
	res, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-warm-retired"})
	require.Error(t, err, "the group fails visibly; the apply reports a count of errors")
	require.Zero(t, res.Groups)
	require.Equal(t, before, fsRowCount(t, s))
}
