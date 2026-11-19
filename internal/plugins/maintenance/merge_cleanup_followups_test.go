// file: internal/plugins/maintenance/merge_cleanup_followups_test.go
// version: 1.0.4
// guid: d4f5094a-01a4-4706-8a50-b3d083766896
// last-edited: 2026-11-19

package maintenance

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Review follow-ups to the merge-cleanup fixer's source rule (R6-n1 to R6-n3,
// and two paths the round-6 review found unpinned). Every name and title is
// invented.

// mcRowlessRetiredVersion leaves a retired version record, made from shell and
// held by holder, with no retired row under it. Two real states look like this:
// a retire cut off after the version was created and before its rows moved, and
// a row revert whose version delete was refused or has not run yet. The reader
// the fixer uses for a book's holders (GetRetiredVersionsFromBook) reads version
// records and never looks at rows, so it still names the holder.
func mcRowlessRetiredVersion(t *testing.T, x *mcFixture, shell, holder, versionID string) {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond)
	_, err := x.st.CreateBookVersion(&database.BookVersion{ID: versionID, BookID: x.ids[holder], Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: x.ids[shell], RetireOpID: "op-test", RetiredAt: &at})
	require.NoError(t, err)
}

// mcRequireNoRowsHeld is the premise of the tests below: the book holds no
// retired row, so it is not a retired shell and the hold for one does not
// settle it before the dead-end walk.
func mcRequireNoRowsHeld(t *testing.T, x *mcFixture, name string) {
	t.Helper()
	n, err := database.RetiredRowsHeldFor(x.st, x.ids[name])
	require.NoError(t, err)
	require.Zero(t, n, "premise: %s holds no retired row", name)
}

// ---- R6-n1: the loser is never a source of its own anchors ----

// The loser's own version record names a same-work holder, but no row moved. The
// loser is not a source, so that holder is not followed and the loser stands
// alone (it is not repointed at a book that holds nothing of it). Since a
// source's holders are the books that still hold its rows (R6-n2), only the
// loser's exclusion keeps the group read out too; this pins the outcome either
// way, and failed before that filter existed when the exclusion was dropped.
func TestMergeCleanupDeadEndLoserIsNotASourceWhenItsVersionHoldsNoRows(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("holder", mcHushSpec(30000))
	mcRowlessRetiredVersion(t, x, "loser", "holder", "rv-loser-rowless")
	mcRequireNoRowsHeld(t, x, "loser")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"], "the holder holds nothing of the loser's")
}

// ---- R6-n2: a source's holders are the books that still hold its rows ----

// A co-loser's version record names a same-work holder, but no row moved there.
// The holder holds nothing of the co-loser's content, so it is not where the
// loser's content went: the loser stands alone rather than becoming a repoint at
// a book that passed the identity and owner gates on the strength of nothing.
func TestMergeCleanupDeadEndCoLosersRowlessVersionDoesNotNameAHolder(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("h", mcHushSpec(30000))
	mcRowlessRetiredVersion(t, x, "b", "h", "rv-b-rowless")
	mcRequireNoRowsHeld(t, x, "b")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// The same for a hop on the loser's own chain.
func TestMergeCleanupDeadEndHopsRowlessVersionDoesNotNameAHolder(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("h", mcHushSpec(30000))
	mcRowlessRetiredVersion(t, x, "b", "h", "rv-b-rowless")
	mcRequireNoRowsHeld(t, x, "b")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "b")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// A source with one version that still holds its rows and one that does not
// names only the first: the row is a repoint at it, not held as ambiguous
// between two holders.
func TestMergeCleanupDeadEndOnlyTheHolderThatStillHoldsRowsIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("h-real", mcHushSpec(30000))
	x.mk("h-empty", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["b"], x.ids["h-real"], "rb", filepath.Join(x.root, "retired", "b.m4b"))
	mcRowlessRetiredVersion(t, x, "b", "h-empty", "rv-b-rowless")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["h-real"], r.Proposed["merged_into_book_id"])
}

// The holder's rows are matched to the version, not just to the holder: a
// holder that holds some other book's retired rows is not a holder of a
// rowless version made from the source.
func TestMergeCleanupDeadEndHolderOfAnotherBooksRowsIsNotAHolderOfARowlessVersion(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("other", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.mk("h", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["other"], x.ids["h"], "ro", filepath.Join(x.root, "retired", "o.m4b"))
	mcRowlessRetiredVersion(t, x, "b", "h", "rv-b-rowless")
	mcRequireNoRowsHeld(t, x, "b")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// mcTornReader is a RetiredBookFileReader whose version list and row reads are
// scripted, to show how mcRowHolders reads a version that moves between them.
type mcTornReader struct {
	database.RetiredBookFileReader
	versions [][]database.BookVersion // one list per call, the last repeats
	rows     map[string][]database.BookFile
	calls    int
}

func (r *mcTornReader) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	i := min(r.calls, len(r.versions)-1)
	r.calls++
	return r.versions[i], nil
}

func (r *mcTornReader) GetRetiredBookFiles(holder string) ([]database.BookFile, error) {
	return r.rows[holder], nil
}

func TestMcRowHoldersReadsAgainWhenAVersionChangesHolderMidRead(t *testing.T) {
	rows := map[string][]database.BookFile{"holder-b": {{ID: "r1", VersionID: "v1"}}}
	atA := []database.BookVersion{{ID: "v1", BookID: "holder-a"}}
	atB := []database.BookVersion{{ID: "v1", BookID: "holder-b"}}

	// The version is listed under holder-a, whose rows are gone because the version
	// was re-homed to holder-b before they were read; the list read after the
	// rows shows the move, so the read is repeated and finds holder-b.
	r := &mcTornReader{versions: [][]database.BookVersion{atA, atB, atB, atB}, rows: rows}
	got, err := mcRowHolders(r, "src")
	require.NoError(t, err)
	require.Equal(t, []string{"holder-b"}, got, "a torn read is not read as a holder with no rows")

	// A version that keeps moving is refused rather than guessed.
	flip := &mcTornReader{versions: [][]database.BookVersion{atA, atB, atA, atB, atA, atB, atA, atB}, rows: rows}
	_, err = mcRowHolders(flip, "src")
	require.ErrorContains(t, err, "kept changing holder")

	// A source with no version names no holder and reads no row.
	none := &mcTornReader{versions: [][]database.BookVersion{nil}}
	got, err = mcRowHolders(none, "src")
	require.NoError(t, err)
	require.Empty(t, got)
}

// ---- R6-n3: a hold reason names the book the anchor was read from ----

// A holder that loops, reached from a co-loser's retired versions: the hold says
// whose retired versions it holds, not "its", which a reader takes for the dead
// book's.
func TestMergeCleanupAmbiguousHoldNamesTheSourceOfAnUnprovenAnchor(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("holder-a", mcHushSpec(30000))
	x.mk("holder-b", mcHushSpec(30000))
	x.point("holder-a", "holder-b")
	x.point("holder-b", "holder-a")
	retireRowOnto(t, x.st, x.ids["b"], x.ids["holder-a"], "rl", filepath.Join(x.root, "retired", "l.m4b"))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointAmbiguous)
	require.Contains(t, r.SkipReason, x.ids["holder-a"])
	require.Contains(t, r.SkipReason, "holds the retired versions of "+x.ids["b"], "the source is named")
	require.NotContains(t, r.SkipReason, "its retired versions", "'its' reads as the dead book's")
	require.NotContains(t, r.SkipReason, "its version group")
}

// A live book of a co-loser's version group that no walk reached: the hold names
// the group and the co-loser it was read from, not "its version group".
func TestMergeCleanupGroupNoPrimaryHoldNamesTheGroupAndItsSource(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("a", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, x.ids["a"])
	require.Contains(t, r.SkipReason, "version group g", "the group is named")
	require.Contains(t, r.SkipReason, x.ids["b"], "the book the group was read from is named")
	require.NotContains(t, r.SkipReason, "its version group", "'its' reads as the dead book's")
}

// The same hold for the loser's own dead hop keeps naming that hop's group.
func TestMergeCleanupGroupNoPrimaryHoldNamesTheDeadHopsOwnGroup(t *testing.T) {
	x := newMCFixture(t)
	mcGroupedDeadEnd(x, "g", mcFalse())
	x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, x.ids["sibling"])
	require.Contains(t, r.SkipReason, "version group g")
	require.Contains(t, r.SkipReason, x.ids["dead"], "the dead book the group was read from is named")
}

// Several live copies around one dead end: the reason says where the copies come
// from without calling it the dead book's own group.
func TestMergeCleanupDeadEndSharedHoldSaysWhoseVersionsAndGroupTheCopiesCameThrough(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("mate", mcHushSpec(30000))
	x.point("mate", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipDeadEndShared)
	require.Contains(t, r.SkipReason, "through the retired versions or version group of a book that was merged into it")
	require.NotContains(t, r.SkipReason, "from its retired versions or version group")
}

// A group read from two sources is named for the first one in the walk's fixed
// order (the sorted ids), so the same plan always reads the same.
func TestMergeCleanupGroupNoPrimaryHoldNamesTheFirstSourceOfASharedGroup(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	for _, n := range []string{"b1", "b2"} {
		x.mk(n, mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
		x.point(n, "t")
	}
	x.mk("a", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	first, second := x.ids["b1"], x.ids["b2"]
	if second < first {
		first, second = second, first
	}
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, "in version group g of "+first)
	require.NotContains(t, r.SkipReason, "in version group g of "+second)
}

// A live primary reached through a co-loser's group is reported with that group
// and the co-loser it was read from.
func TestMergeCleanupRepointEvidenceNamesTheGroupAndSourceItWasReadFrom(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("p", mcHush("g", mcTrue()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Contains(t, strings.Join(r.Evidence, " "), "is the primary of version group g of "+x.ids["b"])
}

// ---- a book merged INTO the loser is a source too ----

// Y -> loser -> D: Y's chain enters the dead book D through the loser, so Y is
// one of D's sources like any co-loser, though it is not beside the loser but
// behind it. Whatever Y's content moved to is where the loser's belongs too.
func TestMergeCleanupDeadEndABookMergedIntoTheLoserIsASource(t *testing.T) {
	t.Run("its version group's live primary", func(t *testing.T) {
		for _, trashed := range []bool{false, true} {
			name := "live"
			if trashed {
				name = "in the trash"
			}
			t.Run(name, func(t *testing.T) {
				x := newMCFixture(t)
				x.mk("d", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
				x.mk("loser", mcHushSpec(30000))
				x.point("loser", "d")
				x.mk("y", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse(), trashed: trashed})
				x.point("y", "loser")
				x.mk("p", mcHush("g", mcTrue()))
				r := x.plan()["loser"]
				require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
				require.Equal(t, x.ids["p"], r.Proposed["merged_into_book_id"])
				require.Contains(t, strings.Join(r.Evidence, " "), "is the primary of version group g of "+x.ids["y"])
			})
		}
	})

	t.Run("the live holder of its retired rows", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("d", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
		x.mk("loser", mcHushSpec(30000))
		x.point("loser", "d")
		x.mk("y", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
		x.point("y", "loser")
		x.mk("h", mcHushSpec(30000))
		retireRowOnto(t, x.st, x.ids["y"], x.ids["h"], "ry", filepath.Join(x.root, "retired", "y.m4b"))
		r := x.plan()["loser"]
		require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
		require.Equal(t, x.ids["h"], r.Proposed["merged_into_book_id"])
		require.Contains(t, strings.Join(r.Evidence, " "), "holds the retired versions of "+x.ids["y"])
	})

	t.Run("a book with nothing behind it leaves the loser alone", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("d", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
		x.mk("loser", mcHushSpec(30000))
		x.point("loser", "d")
		x.mk("y", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
		x.point("y", "loser")
		wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	})
}
