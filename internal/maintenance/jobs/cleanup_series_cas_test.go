// file: internal/maintenance/jobs/cleanup_series_cas_test.go
// version: 1.1.0
// guid: 5d2b8f41-7a36-4e09-b1c8-93e4f6a02d75
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// cleanup-series reads series membership ONCE at the start of a run and then
// writes from that map. Any other writer that moves a book between the read and
// the write (series-denumber is the one that matters: it moves "Batman 03" into
// the plain "Batman" series) would be undone by a write that does not look at
// the book's CURRENT series:
//
//   - phase 1 cleared SeriesID unconditionally, so a book another writer had
//     just filed in a new series lost its series link;
//   - phase 2 set SeriesID to the keeper unless it already was, so a book that
//     was moved elsewhere was re-homed onto the keeper (a wrong link).
//
// Both phases now compare-and-set inside the ModifyBook closure: a book whose
// SeriesID no longer names the series being cleaned is left alone and counted
// as skipped. These tests drive that with a store whose rows really change.

// casStore is a tiny stateful store: rows that ModifyBook reads and writes, a
// hook that runs before each read (the "other writer"), and a delete record.
type casStore struct {
	books        map[string]*database.Book
	beforeModify func(id string)
	deleted      []int
	written      []string
}

func newCasStore(rows ...database.Book) *casStore {
	s := &casStore{books: map[string]*database.Book{}}
	for i := range rows {
		b := rows[i]
		s.books[b.ID] = &b
	}
	return s
}

func (s *casStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.beforeModify != nil {
		s.beforeModify(id)
	}
	row := s.books[id]
	if row == nil {
		return nil, nil
	}
	cp := *row
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return row, nil
		}
		return nil, err
	}
	*row = cp
	s.written = append(s.written, id)
	return row, nil
}

// DeleteSeriesIfUnreferenced is the only delete casStore offers, because it is
// the only one the helpers' interfaces carry. These tests have no writer that
// links a book in late, so every delete is of an unreferenced series; the
// stateful csCondStore covers the refusal.
func (s *casStore) DeleteSeriesIfUnreferenced(id int, _ string, _ *int) (bool, error) {
	s.deleted = append(s.deleted, id)
	return true, nil
}

func (s *casStore) seriesOf(id string) int {
	if b := s.books[id]; b != nil && b.SeriesID != nil {
		return *b.SeriesID
	}
	return 0
}

func intp(v int) *int { return &v }

// --- phase 1 ---------------------------------------------------------------

func TestCsUnlinkAndDeleteSeries_SkipsABookAnotherWriterMovedToAnotherSeries(t *testing.T) {
	// "Batman 3" was listed in series 703 when the job read membership; the
	// other writer filed it in series 900 before the unlink wrote.
	store := newCasStore(database.Book{ID: "batman-3", SeriesID: intp(900), SeriesSequence: intp(3)})
	members := database.SeriesBooksMap{703: {{ID: "batman-3"}}}

	res, err := csUnlinkAndDeleteSeries(store, []database.BookCore{{ID: "batman-3"}}, database.Series{ID: 703}, members)
	if err != nil {
		t.Fatalf("csUnlinkAndDeleteSeries: %v", err)
	}
	skipped := res.Skipped
	if got := store.seriesOf("batman-3"); got != 900 {
		t.Fatalf("book is in series %d, want 900: phase 1 cleared a link it no longer owned", got)
	}
	if store.books["batman-3"].SeriesSequence == nil || *store.books["batman-3"].SeriesSequence != 3 {
		t.Fatalf("the other writer's position was cleared: %+v", store.books["batman-3"])
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(store.written) != 0 {
		t.Fatalf("wrote %v, want no write at all", store.written)
	}
	// The hoisted map is kept current: the book now sits in 900, not 703.
	if len(members[703]) != 0 || len(members[900]) != 1 {
		t.Fatalf("members not updated for the skipped book: %v", members)
	}
}

func TestCsUnlinkAndDeleteSeries_StillUnlinksABookThatNamesTheSeries(t *testing.T) {
	// Positive control: without it a CAS that skips everything passes the test above.
	store := newCasStore(database.Book{ID: "robin-1", SeriesID: intp(703), SeriesSequence: intp(1)})
	members := database.SeriesBooksMap{703: {{ID: "robin-1"}}}

	res, err := csUnlinkAndDeleteSeries(store, []database.BookCore{{ID: "robin-1"}}, database.Series{ID: 703}, members)
	if err != nil {
		t.Fatalf("csUnlinkAndDeleteSeries: %v", err)
	}
	skipped := res.Skipped
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if got := store.books["robin-1"]; got.SeriesID != nil || got.SeriesSequence != nil {
		t.Fatalf("book still linked: %+v", got)
	}
	if len(store.deleted) != 1 || store.deleted[0] != 703 {
		t.Fatalf("deleted = %v, want [703]", store.deleted)
	}
}

func TestCsUnlinkAndDeleteSeries_UnlinksTheRestWhenOneBookMovedAway(t *testing.T) {
	store := newCasStore(
		database.Book{ID: "alfred-1", SeriesID: intp(703)},
		database.Book{ID: "alfred-2", SeriesID: intp(900)},
	)
	members := database.SeriesBooksMap{703: {{ID: "alfred-1"}, {ID: "alfred-2"}}}

	res, err := csUnlinkAndDeleteSeries(store, []database.BookCore{{ID: "alfred-1"}, {ID: "alfred-2"}}, database.Series{ID: 703}, members)
	if err != nil {
		t.Fatalf("csUnlinkAndDeleteSeries: %v", err)
	}
	skipped := res.Skipped
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if store.seriesOf("alfred-1") != 0 || store.seriesOf("alfred-2") != 900 {
		t.Fatalf("alfred-1 in %d (want none), alfred-2 in %d (want 900)", store.seriesOf("alfred-1"), store.seriesOf("alfred-2"))
	}
}

func TestCleanupSeriesRun_Phase1DoesNotClearTheLinkOfABookMovedSinceMembershipWasRead(t *testing.T) {
	// The job-level shape of the reviewer's scenario: membership says "VISIBLE"
	// is the one book of series 7; by the time phase 1 writes, the row names 900.
	var deleted []int
	store := newCsPhase1Store(t, map[int]int{7: 1}, &deleted)
	store.MockStore.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, SeriesID: intp(900)}, nil
	}
	var updated []string
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updated = append(updated, id)
		return b, nil
	}
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("wrote %v for a book that no longer names series 7", updated)
	}
	var found bool
	for _, l := range rep.logs {
		if strings.Contains(l, "Skipped 1 books in single-book cleanup") && strings.Contains(l, "no longer named the series") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the skip must reach the job log an operator reads, got %v", rep.logs)
	}
}

// --- phase 2 ---------------------------------------------------------------

func TestCsMergeSeriesGroup_DoesNotRehomeABookAnotherWriterMovedAway(t *testing.T) {
	// Series 7 is merged into keeper 1. "Batman 3" was listed in 7, but the other
	// writer filed it in the plain series 900 first. Re-homing it onto 1 would be
	// a wrong link.
	store := newCasStore(database.Book{ID: "batman-3", SeriesID: intp(900)})
	members := database.SeriesBooksMap{7: {{ID: "batman-3"}}}

	res, err := csMergeSeriesGroup(store, 1, csSeries(7), map[int]int{7: 1}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	merged, refused, skipped := res.Merged, res.Refused, res.Skipped
	if got := store.seriesOf("batman-3"); got != 900 {
		t.Fatalf("book is in series %d, want 900: phase 2 re-homed a book it did not own", got)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(store.written) != 0 {
		t.Fatalf("wrote %v, want no write", store.written)
	}
	// The book left series 7 on its own, so nothing is stranded and 7 can go.
	if merged != 1 || refused != 0 {
		t.Fatalf("merged=%d refused=%d, want 1/0: the skipped book is verifiably not in series 7", merged, refused)
	}
	if len(members[7]) != 0 || len(members[900]) != 1 {
		t.Fatalf("members not updated for the skipped book: %v", members)
	}
}

func TestCsMergeSeriesGroup_StillRehomesABookThatNamesTheSeries(t *testing.T) {
	// Positive control.
	store := newCasStore(database.Book{ID: "robin-1", SeriesID: intp(7)})
	members := database.SeriesBooksMap{7: {{ID: "robin-1"}}}

	res, err := csMergeSeriesGroup(store, 1, csSeries(7), map[int]int{7: 1}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	merged, refused, skipped := res.Merged, res.Refused, res.Skipped
	if store.seriesOf("robin-1") != 1 || skipped != 0 || merged != 1 || refused != 0 {
		t.Fatalf("book in %d, skipped=%d merged=%d refused=%d, want 1/0/1/0",
			store.seriesOf("robin-1"), skipped, merged, refused)
	}
}

func TestCsMergeSeriesGroup_ABookAlreadyInTheKeeperIsNotASkip(t *testing.T) {
	// A book already filed in the keeper is the old "nothing to write" case, not
	// a concurrent mover: it counts as reassigned, not skipped.
	store := newCasStore(database.Book{ID: "robin-1", SeriesID: intp(1)})
	members := database.SeriesBooksMap{7: {{ID: "robin-1"}}}

	res, err := csMergeSeriesGroup(store, 1, csSeries(7), map[int]int{7: 1}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	merged, skipped := res.Merged, res.Skipped
	if skipped != 0 || merged != 1 {
		t.Fatalf("skipped=%d merged=%d, want 0/1", skipped, merged)
	}
}

func TestCsMergeSeriesGroup_AHiddenRowStillRefusesTheDeleteWhenABookWasSkipped(t *testing.T) {
	// refCounts says 3 rows reference series 7; the membership lists one, and that
	// one moved away. Two rows are still unaccounted for, so the delete is refused.
	store := newCasStore(database.Book{ID: "batman-3", SeriesID: intp(900)})
	members := database.SeriesBooksMap{7: {{ID: "batman-3"}}}

	res, err := csMergeSeriesGroup(store, 1, csSeries(7), map[int]int{7: 3}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	merged, refused, skipped := res.Merged, res.Refused, res.Skipped
	if merged != 0 || refused != 1 || skipped != 1 || len(store.deleted) != 0 {
		t.Fatalf("merged=%d refused=%d skipped=%d deleted=%v, want 0/1/1/none", merged, refused, skipped, store.deleted)
	}
}

func TestCleanupSeriesRun_Phase2DoesNotRehomeABookMovedSinceMembershipWasRead(t *testing.T) {
	// Two series that normalize to the same name: 8 (five books) is the keeper,
	// 7 holds A and B. Before phase 2 writes, the other writer filed A in 900.
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 2, 8: 5}, &deleted, &unlinked)
	store.MockStore.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 7, Name: "Solo"}, {ID: 8, Name: "solo"}}, nil
	}
	store.MockStore.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{7: 2, 8: 5}, nil
	}
	store.MockStore.GetBooksBySeriesIDsAllVersionsFunc = func([]int) (map[int][]database.BookCore, error) {
		return map[int][]database.BookCore{
			7: {{ID: "A"}, {ID: "B"}},
			8: {{ID: "K1"}, {ID: "K2"}, {ID: "K3"}, {ID: "K4"}, {ID: "K5"}},
		}, nil
	}
	store.MockStore.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 7
		if id == "A" {
			sid = 900
		}
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	rehomed := map[string]int{}
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			rehomed[id] = *b.SeriesID
		}
		return b, nil
	}
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, wrote := rehomed["A"]; wrote {
		t.Fatalf("phase 2 wrote A (now %d) although it names series 900, not 7", rehomed["A"])
	}
	if rehomed["B"] != 8 {
		t.Fatalf("B was not re-homed onto the keeper: %v", rehomed)
	}
	var found bool
	for _, l := range rep.logs {
		if strings.Contains(l, "and 1 books in series merges") && strings.Contains(l, "no longer named the series") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the skip must reach the job log, got %v", rep.logs)
	}
}
