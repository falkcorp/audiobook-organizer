// file: internal/plugins/maintenance/merge_cleanup_followups_test.go
// version: 1.0.0
// guid: d4f5094a-01a4-4706-8a50-b3d083766896
// last-edited: 2026-11-19

package maintenance

import (
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
	n, err := database.RetiredRowsHeldFor(x.st, x.ids[shell])
	require.NoError(t, err)
	require.Zero(t, n, "premise: the shell holds no retired row, so it is not a retired shell")
}

// ---- R6-n1: the loser is never a source of its own anchors ----

// The loser's own version record names a same-work holder, but no row moved. The
// loser is not a source, so that holder is not followed and the loser stands
// alone (it is not repointed at a book that holds nothing of it).
func TestMergeCleanupDeadEndLoserIsNotASourceWhenItsVersionHoldsNoRows(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("holder", mcHushSpec(30000))
	mcRowlessRetiredVersion(t, x, "loser", "holder", "rv-loser-rowless")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"], "the holder holds nothing of the loser's")
}
