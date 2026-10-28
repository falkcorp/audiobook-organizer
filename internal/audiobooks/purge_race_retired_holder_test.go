// file: internal/audiobooks/purge_race_retired_holder_test.go
// version: 1.0.0
// guid: 8d4c7e12-3b59-4a60-9f1e-6c2a0b5d7e93
// last-edited: 2026-10-28

package audiobooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredRowOnDeleteStore lands a retired book_file row on the book at the
// moment DeleteBook runs: after every check the purge made before it, so the
// refusal comes from DeleteBook itself.
type retiredRowOnDeleteStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string
}

func (s *retiredRowOnDeleteStore) DeleteBook(id string) error {
	if id == s.bookID {
		// RetireBookFiles refuses a soft-deleted holder, so the holder is live for
		// the moment the version is retired into it, and trashed again.
		_, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt, b.LibraryState = nil, nil, new("imported")
			return nil
		})
		require.NoError(s.t, err)
		retireRowInto(s.t, s.PebbleStore, id)
		trashBook(s.t, s.PebbleStore, id)
	}
	return s.PebbleStore.DeleteBook(id)
}

// A retired row that lands after the purge's pre-check and refuses DeleteBook is
// reported the way the pre-check reports a holder of retired versions: under its
// own reason (holds_retired_versions), not as a book that "owns files".
func TestRunPurge_RetiredRowLandingBeforeDeleteBookIsReportedAsHoldingRetiredVersions(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "holder", "")
	svc := NewAudiobookService(&retiredRowOnDeleteStore{PebbleStore: store, t: t, bookID: "holder"})

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true})
	require.NoError(t, err)
	require.Zero(t, res.Purged, "result %+v", res)
	require.Equal(t, 1, res.SkippedHoldsRetiredVersions, "result %+v", res)
	require.Equal(t, []string{"holder"}, res.SkippedHoldsRetiredVersionsIDs)
	require.Zero(t, res.SkippedOwnsFiles, "a holder of retired versions only is not reported as owning files: %+v", res)
	require.Empty(t, res.Errors)
	require.False(t, bookGone(t, store, "holder"))
}
