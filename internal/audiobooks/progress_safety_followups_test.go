// file: internal/audiobooks/progress_safety_followups_test.go
// version: 1.0.0
// guid: 47073aff-f39f-47da-8927-07cdae028edb
// last-edited: 2026-10-05

package audiobooks

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Review follow-ups to #3771 / #3772 (trash progress carry).

// fileRowAfterFirstReadStore adds a book_file row to bookID right after the
// first read of it: a file row landing between DiscardProgressAndPurge's
// up-front checks and the merge lock.
type fileRowAfterFirstReadStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string
	mu     sync.Mutex
	reads  int
}

func (s *fileRowAfterFirstReadStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id != s.bookID {
		return b, err
	}
	s.mu.Lock()
	s.reads++
	first := s.reads == 1
	s.mu.Unlock()
	if first {
		require.NoError(s.t, s.PebbleStore.CreateBookFile(&database.BookFile{BookID: id, FilePath: "/x/" + id + ".m4b", Format: "m4b"}))
	}
	return b, err
}

// G1: a file row that lands after the up-front check refuses the discard in
// the precheck under the merge lock, BEFORE any state is cleared. Without
// the re-check the state was discarded and only then did DeleteBook refuse.
func TestDiscardProgressAndPurge_FileRowMeanwhileRefusesBeforeClearing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	svc := NewAudiobookService(&fileRowAfterFirstReadStore{PebbleStore: store, t: t, bookID: "held"})
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"), "no state was cleared")
	pos, err := store.ListUserPositionsForBook(u.ID, "held")
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the position is still there")
	require.Empty(t, rec.entries)
}

// siblingReadFailsStore fails reads of the sibling once the purge has
// listed the version group, so only the carry precheck's re-read fails.
type siblingReadFailsStore struct {
	*database.PebbleStore
	sibling string
	mu      sync.Mutex
	armed   bool
}

func (s *siblingReadFailsStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
	return s.PebbleStore.GetBooksByVersionGroup(gid)
}

func (s *siblingReadFailsStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	armed := s.armed
	s.mu.Unlock()
	if armed && id == s.sibling {
		return nil, fmt.Errorf("injected read failure for %s", id)
	}
	return s.PebbleStore.GetBookByID(id)
}

// G2: the carry precheck failing to re-read the sibling is a failed carry
// reported with its error, not a quiet kept_has_progress.
func TestPurge_SiblingReadErrorIsCarryFailed(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&siblingReadFailsStore{PebbleStore: store, sibling: "keep"})

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.CarryFailed, "result %+v", res)
	require.Equal(t, []string{"dup"}, res.CarryFailedIDs)
	require.Zero(t, res.KeptHasProgress)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0], "injected read failure")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
}
