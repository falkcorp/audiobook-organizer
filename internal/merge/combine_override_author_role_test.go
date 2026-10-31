// file: internal/merge/combine_override_author_role_test.go
// version: 1.0.1
// guid: 4c81e0d7-2b5a-4f93-8e16-a07d3c9b52f4
// last-edited: 2026-10-30

package merge

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Round-4 B1 (#4064): the undo of an author override took back every survivor
// link to the override's author, whatever its role. The override wrote only
// (A, "author"); a later (A, narrator) row (an author-narrated book) or
// (A, co-author) row is someone else's credit and must survive the undo, with
// the usual "linked after the combine; kept" warning. Before the fix the
// survivor came back with no links at all and no warning (the reviewer's
// probe_narrator_test.go).
func TestUndoCombine_KeepsLaterLinkToTheOverrideAuthorInAnotherRole(t *testing.T) {
	for _, role := range []string{"narrator", "co-author"} {
		t.Run(role, func(t *testing.T) {
			store := setupTestStore(t).(*database.PebbleStore)
			ms, keep, a, res := combineCreatingAuthor(t, store)
			require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{
				{BookID: keep, AuthorID: a.ID, Role: overrideAuthorRole, Position: 0},
				{BookID: keep, AuthorID: a.ID, Role: role, Position: 1},
			}))

			undo, err := ms.UndoCombine(res.JournalID)
			require.NoError(t, err)

			links, err := store.GetBookAuthors(keep)
			require.NoError(t, err)
			require.Len(t, links, 1, "only the override's own (A, author) link is taken back: %+v", links)
			require.Equal(t, a.ID, links[0].AuthorID)
			require.Equal(t, role, links[0].Role)

			joined := strings.Join(WarningTexts(undo.Warnings), "\n")
			require.Contains(t, joined, fmt.Sprintf("author %d (%s) was linked to book %s after the combine; kept", a.ID, role, keep))
			require.NotContains(t, joined, "purge-empty-authors", "the survivor still credits the author, so no purge note")
			requireAuthorLive(t, store, a.ID)

			b, err := store.GetBookByID(keep)
			require.NoError(t, err)
			require.Nil(t, b.AuthorID, "the book row's author goes back to what it was before the combine")
		})
	}
}
