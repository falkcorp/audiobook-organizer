// file: internal/merge/combine_pending_order_test.go
// version: 1.0.0
// guid: 40362a91-201c-4c22-bc85-28be669fd4ae
// last-edited: 2026-10-18

package merge

import (
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A pending user-state repair Z -> survivor that the combine completes is
// completed BEFORE the combine's before-snapshot, so the progress it carries
// onto the survivor is in the survivor's before-state and the undo keeps it.
// When the combine completed it after the snapshot (inside the follow, the
// order until 2026-10-18), the undo put the survivor back to a before-state
// without Z's progress -- Z itself already drained -- and that progress was
// on no book. With the completion dropped, the combine never carries it at
// all.
func TestCombineBooks_UserStatePendingRepairCompletedBeforeSnapshotSurvivesUndo(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	z := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: z, Title: "Z", Format: "mp3", FilePath: "/tmp/undo/z.mp3"})
	require.NoError(t, err)
	require.NoError(t, store.SetUserPositionAt(f.user.ID, z, "seg-z", 777, time.Now().UTC()))
	require.NoError(t, putPendingRepair(store, PendingUserStateRepair{LoserBookID: z, WinnerBookID: f.survivor, RecordedAt: time.Now().UTC(), BookIDChange: true}))

	res, err := NewService(store).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.True(t, positionOn(t, store, f.user.ID, "seg-z", f.survivor), "the combine did not complete the pending repair onto the survivor")
	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, positionOn(t, store, f.user.ID, "seg-z", f.survivor, z, f.absA, f.absB),
		"Z's progress, carried by the pending repair the combine completed, is on no book after the undo")
}
