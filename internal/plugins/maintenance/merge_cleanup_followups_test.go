// file: internal/plugins/maintenance/merge_cleanup_followups_test.go
// version: 1.0.1
// guid: d4f5094a-01a4-4706-8a50-b3d083766896
// last-edited: 2026-11-19

package maintenance

import (
	"path/filepath"
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
