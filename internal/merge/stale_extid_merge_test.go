// file: internal/merge/stale_extid_merge_test.go
// version: 1.0.0
// guid: 4a9af738-8fc5-4051-b19b-a0b74a2cb19a
// last-edited: 2026-09-22

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleExtIDStore makes GetExternalIDsForBook(loser) also return a mapping the
// winner owns: the shape a stale reverse-index key produced in prod before the
// store filtered it.
type staleExtIDStore struct {
	database.Store
	loserID string
	leaked  database.ExternalIDMapping
}

func (s *staleExtIDStore) GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error) {
	got, err := s.Store.GetExternalIDsForBook(bookID)
	if err != nil || bookID != s.loserID {
		return got, err
	}
	return append(got, s.leaked), nil
}

// A merge must never queue removal of an iTunes PID the loser does not own. On
// 2026-09-22 the Wriggly Little Hands merge would have deleted the surviving
// book's own track this way.
func TestMergeBooks_DoesNotRemoveWinnersPIDListedUnderLoser(t *testing.T) {
	real := setupTestStore(t)
	keep := seedGuardBook(t, real, "Keep", "m4b", true)
	lose := seedGuardBook(t, real, "Lose", "mp3", true)
	seedITunesPID(t, real, keep.ID, "PID-KEEP")
	seedITunesPID(t, real, lose.ID, "PID-LOSE")

	store := &staleExtIDStore{
		Store:   real,
		loserID: lose.ID,
		leaked:  database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-KEEP", BookID: keep.ID},
	}
	enq := &b3FakeEnqueuer{}
	ms := NewService(store)
	ms.SetWriteBackBatcher(enq)

	_, err := ms.MergeBooks([]string{keep.ID, lose.ID}, keep.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"PID-LOSE"}, enq.removed, "only the loser's own PID may be removed from iTunes")
	assert.Equal(t, keep.ID, bookOfPID(t, real, "PID-KEEP"))
}
