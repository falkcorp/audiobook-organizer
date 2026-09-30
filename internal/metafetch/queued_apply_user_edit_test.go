// file: internal/metafetch/queued_apply_user_edit_test.go
// version: 1.0.0
// guid: 2e9c4b17-6a3f-4d58-b0e1-8f7a2c5d9b36
// last-edited: 2026-09-30

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A user's web bulk edit (POST /audiobooks/batch) after a queued apply was
// enqueued must refuse that apply. Before 2026-09-30 the bulk edit wrote no
// change history, so the queued apply saw nothing and overwrote it.
func TestApplyEditsSince_BulkEditAfterEnqueueRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(*database.Book)
		updates map[string]any
		field   string
	}{
		// description is lockable; version_notes has no lock at all.
		{"edit description", nil, map[string]any{"description": "User blurb"}, "description"},
		{"edit an unlocked field", nil, map[string]any{"version_notes": "remaster"}, "version_notes"},
		// A clear records no lock, but it is still the user's edit.
		{"clear narrator", func(b *database.Book) { b.Narrator = new("Reader") }, map[string]any{"narrator": ""}, "narrator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, svc, book := queuedApplyFixture(t)
			if tc.seed != nil {
				_, err := store.ModifyBook(book.ID, func(b *database.Book) error { tc.seed(b); return nil })
				require.NoError(t, err)
			}
			mark, err := svc.ApplyEditMark(book.ID)
			require.NoError(t, err)

			resp := batch.NewBatchService(store).UpdateAudiobooks(&batch.BatchUpdateRequest{IDs: []string{book.ID}, Updates: tc.updates})
			require.Equal(t, 1, resp.Success, "%+v", resp)

			edits, err := svc.ApplyEditsSince(book.ID, mark, "apply-queued-own")
			require.NoError(t, err)
			require.Contains(t, edits.Others, tc.field+" (manual, manual)")
		})
	}
}

// A bulk edit that changes nothing records nothing, so it does not refuse a
// queued apply.
func TestApplyEditsSince_BulkNoOpEditIsNotAnEdit(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)

	resp := batch.NewBatchService(store).UpdateAudiobooks(&batch.BatchUpdateRequest{
		IDs: []string{book.ID}, Updates: map[string]any{"title": book.Title}})
	require.Equal(t, 1, resp.Success, "%+v", resp)

	history, err := store.GetBookChangeHistory(book.ID, 100)
	require.NoError(t, err)
	require.Empty(t, history)
	edits, err := svc.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.Empty(t, edits.Others)
}
