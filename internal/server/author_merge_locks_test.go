// file: internal/server/author_merge_locks_test.go
// version: 1.0.0
// guid: c41e7a92-5d08-4b3f-a6e1-0f9b2d73c845
// last-edited: 2026-10-13

package server

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
)

// lockAuthorName pins a book's author with a real user lock.
func lockAuthorName(t *testing.T, st *database.PebbleStore, bookID string) {
	t.Helper()
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: bookID, Field: database.FieldKeyAuthorName, OverrideLocked: true,
	}))
}

func runAuthorMergeOpParams(t *testing.T, s *Server, p entities.AuthorMergeOpParams) (*relinkOpReporter, error) {
	t.Helper()
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	rep := &relinkOpReporter{}
	return rep, def.Run(context.Background(), raw, rep)
}

// A locked book stays on its author, the author survives because that book still
// credits it, the unlocked book moves, and the run reports the lock and fails
// naming the author left behind (so the Authors view keeps the variant).
func TestAuthorMergeOp_HonoursAnAuthorNameLockByDefault(t *testing.T) {
	st := newRelinkOpStore(t)
	from, err := st.CreateAuthor("Lock Merge From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Lock Merge Into")
	require.NoError(t, err)
	locked := mkRelinkOpBook(t, st, "lock-merge-locked", from.ID, false)
	free := mkRelinkOpBook(t, st, "lock-merge-free", from.ID, false)
	lockAuthorName(t, st, locked)

	// No honor_locks field at all: the Authors view's request shape.
	rep, runErr := runAuthorMergeOpParams(t, newRelinkOpServer(st), entities.AuthorMergeOpParams{
		KeepID: into.ID, MergeIDs: []int{from.ID}, KeepName: into.Name,
	})

	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), AuthorMergeFailedIDsPrefix+strconv.Itoa(from.ID)+"]")
	require.Contains(t, runErr.Error(), "user-locked")
	require.Equal(t, []int{from.ID}, junctionIDs(t, st, locked), "the locked book kept its author")
	require.Equal(t, []int{into.ID}, junctionIDs(t, st, free), "the unlocked book moved")
	kept, err := st.GetAuthorByID(from.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)
	require.Equal(t, from.ID, kept.ID, "the author a locked book still credits must not be deleted")
	require.True(t, rep.logged("user-locked"), "logs: %v", rep.logs)
	full, err := st.GetBookByID(locked)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, from.ID, *full.AuthorID, "the legacy AuthorID of a locked book is untouched too")
}

func TestAuthorMergeOp_HonorLocksFalseMovesTheLockedBookToo(t *testing.T) {
	st := newRelinkOpStore(t)
	from, err := st.CreateAuthor("Lock Off From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Lock Off Into")
	require.NoError(t, err)
	locked := mkRelinkOpBook(t, st, "lock-off-locked", from.ID, false)
	lockAuthorName(t, st, locked)

	off := false
	_, runErr := runAuthorMergeOpParams(t, newRelinkOpServer(st), entities.AuthorMergeOpParams{
		KeepID: into.ID, MergeIDs: []int{from.ID}, KeepName: into.Name, HonorLocks: &off,
	})

	require.NoError(t, runErr)
	require.Equal(t, []int{into.ID}, junctionIDs(t, st, locked))
	requireSourceDeleted(t, st, from.ID)
}

// An id that is already gone (merged by an earlier run, or by another screen)
// counts as merged instead of being re-merged, and a gone keep fails every id.
func TestAuthorMergeOp_GoneIDsAreDoneAndAGoneKeepFailsEverything(t *testing.T) {
	st := newRelinkOpStore(t)
	from, err := st.CreateAuthor("Gone From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Gone Into")
	require.NoError(t, err)
	mkRelinkOpBook(t, st, "gone-book", from.ID, false)

	s := newRelinkOpServer(st)
	_, runErr := runAuthorMergeOpParams(t, s, entities.AuthorMergeOpParams{KeepID: into.ID, MergeIDs: []int{from.ID}})
	require.NoError(t, runErr)

	// The same merge again: from no longer exists as its own author.
	_, runErr = runAuthorMergeOpParams(t, s, entities.AuthorMergeOpParams{KeepID: into.ID, MergeIDs: []int{from.ID}})
	require.NoError(t, runErr, "an id that is already merged is not a failure")

	// Folding a live author into the gone one has nowhere to go.
	other, err := st.CreateAuthor("Gone Other")
	require.NoError(t, err)
	_, runErr = runAuthorMergeOpParams(t, s, entities.AuthorMergeOpParams{KeepID: from.ID, MergeIDs: []int{other.ID}})
	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), AuthorMergeFailedIDsPrefix+strconv.Itoa(other.ID)+"]")
	still, err := st.GetAuthorByID(other.ID)
	require.NoError(t, err)
	require.Equal(t, other.ID, still.ID, "nothing was merged into a keep author that is gone")
}
