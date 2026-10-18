// file: internal/audiobooks/revert_combine_override_coauthor_test.go
// version: 1.0.0
// guid: 81557a7d-bf1f-4979-a259-566cb53ce578
// last-edited: 2026-10-18

package audiobooks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// A co-author linked to the survivor after a combine's author override (the
// primary author left as the override set it) is someone's later edit. The
// revert puts the replaced authors back and keeps the co-author after them,
// and says so in Kept. It used to write the replaced authors alone and drop
// the co-author without a word.
func TestRevertCombineOverride_LaterCoAuthorIsKept(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCrashFixture(t, store)
	prior := seedPriorAuthor(t, store, f.keep)
	_, err := merge.NewService(store).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep,
		&merge.CombineOverride{Author: "Jane Example"}, merge.CombineOptions{OperationID: "op-coauthor"})
	require.NoError(t, err)
	cur, err := store.GetBookAuthors(f.keep)
	require.NoError(t, err)
	require.Len(t, cur, 1, "fixture: the override leaves one author")
	co, err := store.CreateAuthor("Later CoAuthor")
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(f.keep, append(cur, database.BookAuthor{BookID: f.keep, AuthorID: co.ID, Role: "author", Position: 1})))

	rv, err := NewRevertService(store).RevertOperation("op-coauthor")
	require.NoError(t, err)
	after, err := store.GetBookAuthors(f.keep)
	require.NoError(t, err)
	ids := make([]int, len(after))
	for i, a := range after {
		ids[i] = a.AuthorID
	}
	require.Equal(t, []int{prior.ID, co.ID}, ids, "the replaced author is back first; the later co-author is kept after it")
	require.Contains(t, strings.Join(rv.Kept, "\n"), "was linked to book "+f.keep+" after the combine",
		"the revert says the co-author was kept: %v", rv.Kept)
}
