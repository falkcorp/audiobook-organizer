// file: internal/metafetch/queued_apply_history_test.go
// version: 1.0.0
// guid: 3f0c8a61-9b27-4e5d-a813-c64e0b2d7f95
// last-edited: 2026-09-30

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func queuedApplyFixture(t *testing.T) (*database.PebbleStore, *Service, *database.Book) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "DB Title", FilePath: "/library/q.m4b", Format: "m4b"})
	require.NoError(t, err)
	return store, NewService(store), book
}

// (a) The scanner's merge, which the queued apply waits behind, rewrites the
// row from the file's tags (here: the title) between enqueue and run. It
// writes no change history, so it is not a later edit and the apply runs.
func TestApplyEditsSince_ScannerStyleRowWriteIsNotAnEdit(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)

	_, err = store.ModifyBook(book.ID, func(b *database.Book) error { b.Title = "Tag Title"; return nil })
	require.NoError(t, err)

	edits, err := svc.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.False(t, edits.OwnApplied)
	require.Empty(t, edits.Others, "a row write without history (the scanner's merge) must not refuse the queued apply")
}

// (b) The user applies candidate B after the 202. The queued apply of A sees
// B's history rows as a later edit and refuses.
func TestApplyEditsSince_LaterApplyIsAnEdit(t *testing.T) {
	_, svc, book := queuedApplyFixture(t)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)

	_, err = svc.ApplyMetadataCandidate(book.ID, MetadataCandidate{Title: "Candidate B", Source: "audible"}, nil)
	require.NoError(t, err)

	edits, err := svc.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.False(t, edits.OwnApplied)
	require.Contains(t, edits.Others, "title (fetched, audible)")
}

// (c) The queued apply of A landed (with its own batch id) and the op re-runs
// after a restart: its own rows are recognised, nothing else counts, and the
// re-run must not apply again -- the caller completes "already applied", so
// the history keeps exactly one apply batch.
func TestApplyEditsSince_OwnBatchIsAlreadyApplied(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)
	const own = "apply-queued-own"

	_, err = svc.ApplyMetadataCandidateWithOptions(book.ID, MetadataCandidate{Title: "Candidate A", Source: "audible"}, nil, ApplyOptions{BatchID: own})
	require.NoError(t, err)

	edits, err := svc.ApplyEditsSince(book.ID, mark, own)
	require.NoError(t, err)
	require.True(t, edits.OwnApplied)
	require.Empty(t, edits.Others)

	history, err := store.GetBookChangeHistory(book.ID, 1<<30)
	require.NoError(t, err)
	for _, r := range history {
		require.Equal(t, own, r.BatchID, "every history row of the queued apply carries its fixed batch id")
	}
	require.NotEmpty(t, history)
}

// File-side rows (rename, write-back, cover archive) are not field edits.
func TestApplyEditsSince_FileSideRowsAreNotEdits(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)
	for _, ct := range []string{"rename", "write-back", "cover-archive"} {
		require.NoError(t, store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: book.ID, Field: "file", ChangeType: ct}))
	}
	edits, err := svc.ApplyEditsSince(book.ID, mark, "x")
	require.NoError(t, err)
	require.Empty(t, edits.Others)
}
