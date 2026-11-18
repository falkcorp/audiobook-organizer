// file: internal/maintenance/jobs/cleanup_series_locks_test.go
// version: 1.0.0
// guid: 8a52882a-d496-49d1-867a-f2eccb43d436
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A user who locked a book's series (series_name) has said "this book belongs to
// that series". cleanup-series writes SeriesID in two places: phase 1 clears it
// to drop a series, phase 2 repoints it onto a keeper. Both read the lock under
// the book's write lock and leave a locked book where it is.

// lockedCasStore is casStore with a lock table: the book ids in locked have
// series_name locked, the ones in unreadable have a lock read that fails.
type lockedCasStore struct {
	*casStore
	locked     map[string]bool
	unreadable map[string]bool
}

func (s *lockedCasStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	if s.unreadable[id] {
		return nil, errors.New("field states: store closed")
	}
	if s.locked[id] {
		return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeySeriesName, OverrideLocked: true}}, nil
	}
	return nil, nil
}

func (s *lockedCasStore) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }

func newLockedCasStore(locked, unreadable []string, rows ...database.Book) *lockedCasStore {
	st := &lockedCasStore{casStore: newCasStore(rows...), locked: map[string]bool{}, unreadable: map[string]bool{}}
	for _, id := range locked {
		st.locked[id] = true
	}
	for _, id := range unreadable {
		st.unreadable[id] = true
	}
	return st
}

func TestCsUnlinkAndDeleteSeries_LeavesAUserLockedBookAndKeepsTheSeries(t *testing.T) {
	st := newLockedCasStore([]string{"locked-1"}, nil,
		database.Book{ID: "locked-1", SeriesID: intp(703), SeriesSequence: intp(1)},
		database.Book{ID: "free-1", SeriesID: intp(703), SeriesSequence: intp(1)},
	)
	members := database.SeriesBooksMap{703: {{ID: "locked-1"}, {ID: "free-1"}}}

	res, err := csUnlinkAndDeleteSeries(st, []database.BookCore{{ID: "locked-1"}, {ID: "free-1"}}, database.Series{ID: 703}, members)
	if err != nil {
		t.Fatalf("csUnlinkAndDeleteSeries: %v", err)
	}
	if got := st.seriesOf("locked-1"); got != 703 {
		t.Fatalf("locked book is in series %d, want 703: the user's series lock was overwritten", got)
	}
	if b := st.books["locked-1"]; b.SeriesSequence == nil || *b.SeriesSequence != 1 {
		t.Fatalf("locked book's position was cleared: %+v", b)
	}
	// Control: the unlocked sibling in the same call is unlinked.
	if got := st.seriesOf("free-1"); got != 0 {
		t.Fatalf("unlocked book is in series %d, want it unlinked", got)
	}
	// A locked book still names the series, so the delete is not made, and the
	// outcome is its own bucket rather than a misleading "linked in later".
	if len(st.deleted) != 0 {
		t.Fatalf("deleted %v: a series a locked book still names must stay", st.deleted)
	}
	if res.Outcome != csKeptLockHeld || res.BooksHeld != 1 || res.Unlinked != 1 {
		t.Fatalf("outcome=%v held=%d unlinked=%d, want csKeptLockHeld/1/1", res.Outcome, res.BooksHeld, res.Unlinked)
	}
	if len(members[703]) != 1 || members[703][0].ID != "locked-1" {
		t.Fatalf("members must still list the held book on 703: %v", members)
	}
}

func TestCsUnlinkAndDeleteSeries_UnreadableLocksWriteNothingAndAbort(t *testing.T) {
	st := newLockedCasStore(nil, []string{"bad-1"},
		database.Book{ID: "bad-1", SeriesID: intp(703)},
	)
	members := database.SeriesBooksMap{703: {{ID: "bad-1"}}}

	res, err := csUnlinkAndDeleteSeries(st, []database.BookCore{{ID: "bad-1"}}, database.Series{ID: 703}, members)
	if !errors.Is(err, database.ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if st.seriesOf("bad-1") != 703 || len(st.written) != 0 {
		t.Fatalf("a book whose locks cannot be read was written: series=%d written=%v", st.seriesOf("bad-1"), st.written)
	}
	if len(st.deleted) != 0 || res.Outcome != csNotAttempted {
		t.Fatalf("deleted=%v outcome=%v: an unreadable lock must stop before the delete", st.deleted, res.Outcome)
	}
}

func TestCsMergeSeriesGroup_LeavesAUserLockedBookAndKeepsTheMergedFromSeries(t *testing.T) {
	st := newLockedCasStore([]string{"locked-1"}, nil,
		database.Book{ID: "locked-1", SeriesID: intp(7)},
		database.Book{ID: "free-1", SeriesID: intp(7)},
	)
	members := database.SeriesBooksMap{7: {{ID: "locked-1"}, {ID: "free-1"}}}

	res, err := csMergeSeriesGroup(st, 1, csSeries(7), map[int]int{7: 2}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	if got := st.seriesOf("locked-1"); got != 7 {
		t.Fatalf("locked book is in series %d, want 7: the user's series lock was overwritten", got)
	}
	if got := st.seriesOf("free-1"); got != 1 {
		t.Fatalf("unlocked book is in series %d, want the keeper 1", got)
	}
	if len(st.deleted) != 0 {
		t.Fatalf("deleted %v: series 7 is still named by a locked book", st.deleted)
	}
	// Counted as held, not Refused: the guard is about rows the run could not see.
	if res.Merged != 0 || res.Refused != 0 || res.Kept.LockHeld != 1 || res.BooksHeld != 1 {
		t.Fatalf("merged=%d refused=%d keptLockHeld=%d booksHeld=%d, want 0/0/1/1", res.Merged, res.Refused, res.Kept.LockHeld, res.BooksHeld)
	}
}

func TestCsMergeSeriesGroup_UnreadableLocksWriteNothingAndStopTheGroup(t *testing.T) {
	st := newLockedCasStore(nil, []string{"bad-1"},
		database.Book{ID: "bad-1", SeriesID: intp(7)},
	)
	members := database.SeriesBooksMap{7: {{ID: "bad-1"}}}

	res, err := csMergeSeriesGroup(st, 1, csSeries(7), map[int]int{7: 1}, members)
	if !errors.Is(err, database.ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if st.seriesOf("bad-1") != 7 || len(st.written) != 0 || len(st.deleted) != 0 {
		t.Fatalf("series=%d written=%v deleted=%v, want nothing touched", st.seriesOf("bad-1"), st.written, st.deleted)
	}
	if res.NotReached != 1 {
		t.Fatalf("NotReached = %d, want 1", res.NotReached)
	}
}

// Run-level: a 1-book series whose only book the user locked is neither unlinked
// nor deleted, and the operator is told why in the job log.
func TestCleanupSeriesRun_Phase1KeepsASeriesWhoseOnlyBookIsUserLocked(t *testing.T) {
	var deleted []int
	store := newCsPhase1Store(t, map[int]int{7: 1}, &deleted)
	var updated []string
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updated = append(updated, id)
		return b, nil
	}
	store.MockStore.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeySeriesName, OverrideLocked: true}}, nil
	}
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("wrote %v: a user-locked series link was cleared", updated)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v: the series is still named by the locked book", deleted)
	}
	var held, kept bool
	for _, l := range rep.logs {
		if strings.Contains(l, "Left 1 books in single-book cleanup") {
			held = true
		}
		if strings.Contains(l, "1 single-book series and 0 merged-from series were kept because a book the user locked") {
			kept = true
		}
	}
	if !held || !kept {
		t.Fatalf("the hold must reach the job log (held=%v kept=%v): %v", held, kept, rep.logs)
	}
}
