// file: internal/merge/plan_virtual_retired_test.go
// version: 1.0.0
// guid: 5d0b8e63-91a4-4c27-b6f3-7e2a1c9d4058
// last-edited: 2026-10-23

package merge

import (
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireRowOnto creates a row for path under a fresh shell book and retires it
// into holderID, so the row is keyed under the holder, flagged Retired, and the
// path index names it. Returns the retired row's id.
func retireRowOnto(t *testing.T, store database.Store, holderID, path string) string {
	t.Helper()
	ps, ok := store.(*database.PebbleStore)
	require.True(t, ok)
	ps.WaitForWarmup()
	shell, err := ps.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Retired shell", FilePath: "/tmp/pvr-shell-" + ulid.Make().String()})
	require.NoError(t, err)
	now := time.Now()
	v, err := ps.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-pvr", RetiredAt: &now,
	})
	require.NoError(t, err)
	row := &database.BookFile{ID: ulid.Make().String(), BookID: shell.ID, FilePath: path, Format: "mp3"}
	require.NoError(t, ps.CreateBookFile(row))
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, v.ID))
	hidden, err := ps.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, hidden, "the single-owner getter must not see a retired row")
	return row.ID
}

// A combine must not put a second row on a file a retired version holds: not a
// created row (the book's own FilePath is that file) and not a move of the
// retired row onto the target. planVirtualFile asked GetBookFileByPath, which
// cannot see a retired row, so it planned a create.
func TestPlanVirtualFile_FileHeldByRetiredVersionPlansNothing(t *testing.T) {
	for _, tc := range []struct {
		name         string
		holderIsSelf bool
	}{
		{"held-by-another-book", false},
		{"held-by-the-target-book-itself", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			ms := NewService(store)
			other, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Other", FilePath: "/tmp/pvr-other.mp3"})
			require.NoError(t, err)
			target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/pvr-held.mp3"})
			require.NoError(t, err)
			holder := other.ID
			if tc.holderIsSelf {
				holder = target.ID
			}
			retiredID := retireRowOnto(t, store, holder, target.FilePath)

			plan := ms.planVirtualFile(target, target.ID)
			assert.Nil(t, plan.create, "no row may be created on a retired version's file")
			assert.Nil(t, plan.existing, "the retired row must not be moved")
			assert.False(t, plan.attached)
			assert.Empty(t, plan.fm.FileID)
			assert.Zero(t, ms.applyVirtualFile(plan, target.ID))

			at, err := store.(*database.PebbleStore).BookFilesAtPath(target.FilePath)
			require.NoError(t, err)
			require.Len(t, at, 1)
			assert.Equal(t, retiredID, at[0].ID)
			assert.True(t, at[0].Retired)
		})
	}
}
