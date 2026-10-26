// file: internal/batch/restore_locked_judge_test.go
// version: 1.1.0
// guid: 0f6c2b8e-4d19-4a73-9b5e-6a1d3c7e8f24
// last-edited: 2026-10-26

package batch

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// liveOnFirstRead is a store whose first GetBookByID answers as if the book
// were live, and every later read answers with what is stored. The batch
// service makes that first read for its early permission check; the row is
// really in the trash by the time RestoreFromTrash reads and locks it, as it
// would be if another writer trashed it in between. Only the judge passed to
// RestoreFromTrash can then refuse the restore; the early check cannot.
type liveOnFirstRead struct {
	*database.PebbleStore
	reads atomic.Int32
}

func (s *liveOnFirstRead) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if err != nil || b == nil || s.reads.Add(1) > 1 {
		return b, err
	}
	live := *b
	notMarked, organized := false, "organized"
	live.MarkedForDeletion, live.MarkedForDeletionAt, live.LibraryState = &notMarked, nil, &organized
	return &live, nil
}

// A row trashed between the batch's early check and RestoreFromTrash's own
// read is refused for a caller without library.organize by every route that
// restores (the "restore" action and both marked_for_deletion=false payload
// routes), the early check having seen a live book. This proves that the judge
// argument reaches RestoreFromTrash on each route, and that judgeRestore's
// restore branch refuses; it is the preview call (run on the row as
// RestoreFromTrash read it) that refuses here, not the one inside the write,
// which TestRestorePayload_RowTrashedBeforeTheWriteIsJudgedInsideTheWrite
// reaches. Mutants that drop the judge from these calls, or the restore
// branch of judgeRestore, leave the row restored and fail this.
func TestRestore_RowTrashedAfterTheEarlyCheckIsRefusedByRestoreFromTrash(t *testing.T) {
	payload := map[string]any{"marked_for_deletion": false}
	routes := map[string]func(s *liveOnFirstRead, id string, caller *TrashGrants) *BatchResponse{
		"restore action": func(s *liveOnFirstRead, id string, caller *TrashGrants) *BatchResponse {
			return NewBatchService(s).ExecuteOperations(opsFor(id, "restore", false, nil, caller))
		},
		"update operation": func(s *liveOnFirstRead, id string, caller *TrashGrants) *BatchResponse {
			return NewBatchService(s).ExecuteOperations(opsFor(id, "update", false, payload, caller))
		},
		"batch update": func(s *liveOnFirstRead, id string, caller *TrashGrants) *BatchResponse {
			return NewBatchService(s).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{id}, Updates: payload, Caller: caller})
		},
	}
	for name, run := range routes {
		t.Run(name, func(t *testing.T) {
			for who, caller := range map[string]*TrashGrants{"no caller": nil, "delete only": {Trash: true}} {
				f, id := trashedLikeDeleteAudiobook(t)
				s := &liveOnFirstRead{PebbleStore: f.S}

				resp := run(s, id, caller)

				require.Equal(t, 1, resp.Failed, "%s: %+v", who, resp.Results)
				require.Contains(t, resp.Results[0].Error, "library.organize", who)
				require.Greater(t, s.reads.Load(), int32(1), "%s: fixture: the restore read the row again", who)
				require.True(t, stillInTrash(t, f, id), "%s: the refused restore wrote nothing", who)
			}

			f, id := trashedLikeDeleteAudiobook(t)
			resp := run(&liveOnFirstRead{PebbleStore: f.S}, id, &TrashGrants{Restore: true})
			require.Equal(t, 1, resp.Success, "control, library.organize held: %+v", resp.Results)
			require.False(t, stillInTrash(t, f, id))
		})
	}
}

// A payload that both restores and re-labels a trashed book "deleted" cannot be
// honoured: the write would clear the deletion flag and the merge link and
// leave the row in the trash by label alone. It is refused for a caller who
// holds both permissions (the handler admits that shape for them), on every
// route, and the row keeps its flag and its merge link.
func TestRestorePayload_ThatLeavesTheBookInTheTrashIsRefused(t *testing.T) {
	updates := map[string]any{"marked_for_deletion": false, "library_state": "deleted"}
	for who, caller := range map[string]*TrashGrants{
		"both grants":  {Trash: true, Restore: true},
		"restore only": {Restore: true},
	} {
		check := func(t *testing.T, f *vptest.Fixture, id string, resp *BatchResponse) {
			t.Helper()
			require.Equal(t, 1, resp.Failed, "%s: %+v", who, resp.Results)
			require.Contains(t, resp.Results[0].Error, "put it back in the trash", who)
			b, err := f.S.GetBookByID(id)
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), "%s: the deletion flag is untouched", who)
			require.NotNil(t, b.MergedIntoBookID, "%s: the merge link is untouched", who)
		}
		f, id := trashedLikeDeleteAudiobook(t)
		check(t, f, id, NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{id}, Updates: updates, Caller: caller}))
		f, id = trashedLikeDeleteAudiobook(t)
		check(t, f, id, NewBatchService(f.S).ExecuteOperations(opsFor(id, "update", false, updates, caller)))
	}
}

// trashBeforeWrite is a store whose next ModifyBook first puts the row in the
// trash, as another writer that got in after RestoreFromTrash read a live row
// would. The restore's payload then lands on a trashed row it never saw: no
// preview judged it, so only the judge inside the write can refuse.
type trashBeforeWrite struct {
	*database.PebbleStore
	t *testing.T
}

func (s trashBeforeWrite) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	_, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
		marked, now, deleted := true, time.Now(), "deleted"
		b.MarkedForDeletion, b.MarkedForDeletionAt, b.LibraryState = &marked, &now, &deleted
		return nil
	})
	require.NoError(s.t, err)
	return s.PebbleStore.ModifyBook(id, fn)
}

// The same row, live when the restore payload reads it and trashed by the time
// the write locks it: the judge inside the write sees a trashed row and refuses
// a caller without library.organize, so the payload never clears its flag.
func TestRestorePayload_RowTrashedBeforeTheWriteIsJudgedInsideTheWrite(t *testing.T) {
	payload := map[string]any{"marked_for_deletion": false}
	routes := map[string]func(s trashBeforeWrite, id string, caller *TrashGrants) *BatchResponse{
		"update operation": func(s trashBeforeWrite, id string, caller *TrashGrants) *BatchResponse {
			return NewBatchService(s).ExecuteOperations(opsFor(id, "update", false, payload, caller))
		},
		"batch update": func(s trashBeforeWrite, id string, caller *TrashGrants) *BatchResponse {
			return NewBatchService(s).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{id}, Updates: payload, Caller: caller})
		},
	}
	for name, run := range routes {
		t.Run(name, func(t *testing.T) {
			for who, caller := range map[string]*TrashGrants{"no caller": nil, "delete only": {Trash: true}} {
				f := vptest.New(t)
				id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})

				resp := run(trashBeforeWrite{f.S, t}, id, caller)

				require.Equal(t, 1, resp.Failed, "%s: %+v", who, resp.Results)
				require.Contains(t, resp.Results[0].Error, "library.organize", who)
				require.True(t, stillInTrash(t, f, id), "%s: the payload did not take the row out of the trash", who)
			}

			f := vptest.New(t)
			id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
			// With library.organize held the permission is not the refusal: the
			// payload alone cannot restore a row the write found trashed (the
			// restore rule ran on the live row it read, so the "deleted" label is
			// still there), and judgeRestore refuses to write a row that is then
			// in the trash with its flag cleared. The row keeps its flag.
			resp := run(trashBeforeWrite{f.S, t}, id, &TrashGrants{Restore: true})
			require.Equal(t, 1, resp.Failed, "library.organize held: %+v", resp.Results)
			require.NotContains(t, resp.Results[0].Error, "library.organize")
			// The caller sent no library_state, so the refusal must not blame
			// one: it says another change moved the book to the trash.
			require.Contains(t, resp.Results[0].Error, "moved to the trash by another change")
			require.NotContains(t, resp.Results[0].Error, "library_state")
			require.NotContains(t, resp.Results[0].Error, "marked_for_deletion")
			b, err := f.S.GetBookByID(id)
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), "the flag the other writer set is untouched")
		})
	}
}
