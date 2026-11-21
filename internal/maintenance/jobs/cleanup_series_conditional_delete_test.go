// file: internal/maintenance/jobs/cleanup_series_conditional_delete_test.go
// version: 1.3.0
// guid: 11f4d28a-ab07-4fe7-97c6-9d5d2854e15d
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// CLEANUP-SERIES-DELETE-NOT-CONDITIONAL. Both phases of cleanup-series end in a
// delete of a series row, and both decide to delete from membership and
// reference counts read ONCE at the start of the run. A book another writer
// links into the series after that read is left naming a series id that no
// longer exists. The delete must therefore be the store's atomic
// count-and-delete (DeleteSeriesIfUnreferenced), not a plain DeleteSeries.
//
// csCondStore is a stateful JobStore: rows really change, the "other writer" is a
// hook that links a book into a series right after the bulk membership read (the
// last read the job takes before it writes), and DeleteSeriesIfUnreferenced
// counts the rows that name the series at the moment of the delete. A canned
// "false" would only prove the fixture; this makes the refusal come from the
// state.
type csCondStore struct {
	*database.MockStore
	series map[int]database.Series
	books  map[string]*database.Book

	// afterMembership runs once, right after the bulk membership read returns.
	afterMembership func(s *csCondStore)
	// modifyErr, when set, fails every ModifyBook.
	modifyErr error
	// notReady, when set, is what SeriesDeleteReady returns: a store that has the
	// conditional delete but cannot count right now.
	notReady error
	// deleteIfErr, when set, is what DeleteSeriesIfUnreferenced returns instead of
	// looking at the rows.
	deleteIfErr error
	// deleteErrFor, when it has the id, is what DeleteSeriesIfUnreferenced returns
	// for that series alone: a failure about one row, not about the store.
	deleteErrFor map[int]error
	// modifyFailOnce, when it has the book id, fails that book's next ModifyBook
	// with the error and is then consumed: a transient write failure.
	modifyFailOnce map[string]error
	// hidden books are the non-primary versions: every row that names the series
	// still counts as a reference and is in the AllVersions membership, but the
	// listing getters and the display count do not show them.
	hidden map[string]bool
	// trashed books are named in the reference count only: no getter lists them.
	trashed map[string]bool

	conditionalCalls []int // ids passed to DeleteSeriesIfUnreferenced
	membershipReads  int
}

func newCsCondStore(series []database.Series, books ...database.Book) *csCondStore {
	s := &csCondStore{
		MockStore: &database.MockStore{},
		series:    map[int]database.Series{},
		books:     map[string]*database.Book{},
	}
	for _, ser := range series {
		s.series[ser.ID] = ser
	}
	for i := range books {
		b := books[i]
		s.books[b.ID] = &b
	}
	return s
}

func (s *csCondStore) refs(id int) int {
	n := 0
	for _, b := range s.books {
		if b.SeriesID != nil && *b.SeriesID == id {
			n++
		}
	}
	return n
}

// booksNaming lists the books the AllVersions membership returns: everything but
// the trashed rows.
func (s *csCondStore) booksNaming(id int) []database.BookCore {
	return s.listNaming(id, false)
}

// listNaming lists the books naming id; core also drops the hidden (non-primary)
// versions, as the listing getter does.
func (s *csCondStore) listNaming(id int, core bool) []database.BookCore {
	var out []database.BookCore
	for _, b := range s.books {
		if s.trashed[b.ID] || (core && s.hidden[b.ID]) {
			continue
		}
		if b.SeriesID != nil && *b.SeriesID == id {
			out = append(out, database.BookCore{ID: b.ID, SeriesID: b.SeriesID, SeriesSequence: b.SeriesSequence})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// dangling lists the books that name a series id with no row: the damage this
// change exists to prevent.
func (s *csCondStore) dangling() []string {
	var out []string
	for _, b := range s.books {
		if b.SeriesID != nil {
			if _, ok := s.series[*b.SeriesID]; !ok {
				out = append(out, b.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *csCondStore) linkBook(id string, seriesID int) {
	sid := seriesID
	s.books[id] = &database.Book{ID: id, SeriesID: &sid}
}

func (s *csCondStore) GetAllSeries() ([]database.Series, error) {
	out := make([]database.Series, 0, len(s.series))
	for _, ser := range s.series {
		out = append(out, ser)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetAllSeriesBookCounts is the display count: no trashed or non-primary rows.
func (s *csCondStore) GetAllSeriesBookCounts() (map[int]int, error) {
	out := map[int]int{}
	for id := range s.series {
		if n := len(s.listNaming(id, true)); n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

// GetAllSeriesBookRefCounts is the unfiltered count: every row that names the series.
func (s *csCondStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	out := map[int]int{}
	for id := range s.series {
		if n := s.refs(id); n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

func (s *csCondStore) GetBooksBySeriesIDCore(id int) ([]database.BookCore, error) {
	return s.listNaming(id, true), nil
}

func (s *csCondStore) GetBooksBySeriesIDsAllVersions(ids []int) (map[int][]database.BookCore, error) {
	s.membershipReads++
	out := map[int][]database.BookCore{}
	for _, id := range ids {
		out[id] = s.booksNaming(id)
	}
	if s.afterMembership != nil {
		hook := s.afterMembership
		s.afterMembership = nil
		hook(s)
	}
	return out, nil
}

func (s *csCondStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.modifyErr != nil {
		return nil, s.modifyErr
	}
	if err, ok := s.modifyFailOnce[id]; ok {
		delete(s.modifyFailOnce, id)
		return nil, err
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
	return row, nil
}

func (s *csCondStore) DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (bool, error) {
	s.conditionalCalls = append(s.conditionalCalls, id)
	if s.deleteIfErr != nil {
		return false, s.deleteIfErr
	}
	if err, ok := s.deleteErrFor[id]; ok {
		return false, err
	}
	row, ok := s.series[id]
	if !ok {
		return false, database.ErrSeriesNotFound
	}
	if row.Name != expectName || (row.AuthorID == nil) != (expectAuthorID == nil) ||
		(row.AuthorID != nil && *row.AuthorID != *expectAuthorID) {
		return false, database.ErrSeriesChanged
	}
	if row.HeldBy != "" {
		return false, database.ErrSeriesHeld
	}
	if s.refs(id) > 0 {
		return false, nil
	}
	delete(s.series, id)
	return true, nil
}

func (s *csCondStore) SeriesDeleteReady() error { return s.notReady }

func (s *csCondStore) hasSeries(id int) bool {
	_, ok := s.series[id]
	return ok
}

// csSeries is the series rows a merge group's helper takes, for tests that do
// not care about names.
func csSeries(ids ...int) []database.Series {
	out := make([]database.Series, len(ids))
	for i, id := range ids {
		out[i] = database.Series{ID: id, Name: "Hush " + strconv.Itoa(id)}
	}
	return out
}

// csAlwaysUnreferenced is a conditional delete for fixtures with no late writer:
// every series it is asked about is unreferenced.
type csAlwaysUnreferenced struct{}

func (csAlwaysUnreferenced) DeleteSeriesIfUnreferenced(int, string, *int) (bool, error) {
	return true, nil
}

// csUnreferencedWriter pairs a store's book write with csAlwaysUnreferenced, for
// tests of the helpers that are about the book writes (they hand the helpers a
// bare MockStore, which has no conditional delete).
func csUnreferencedWriter(m seriesBookModifier) cleanupSeriesStore {
	return cleanupSeriesStore{seriesBookModifier: m, SeriesUnreferencedDeleter: csAlwaysUnreferenced{}}
}

func csLogContains(rep *csPhase1Reporter, parts ...string) bool {
	for _, l := range rep.logs {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// --- phase 1 ---------------------------------------------------------------

func TestCleanupSeriesRun_Phase1KeepsASeriesABookWasLinkedIntoAfterTheMembershipRead(t *testing.T) {
	// Series 7 is a one-book series: "long-halloween-1" is its only book when the
	// run reads membership. Right after that read another writer files
	// "dark-victory-1" in it. Phase 1 unlinks the book it knows about, and the
	// delete must then find the new one and refuse.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	store.afterMembership = func(s *csCondStore) { s.linkBook("dark-victory-1", 7) }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) {
		t.Fatal("series 7 was deleted although a book was linked into it after the membership read")
	}
	if d := store.dangling(); len(d) != 0 {
		t.Fatalf("books %v name a series that no longer exists", d)
	}
	if got := store.books["dark-victory-1"].SeriesID; got == nil || *got != 7 {
		t.Fatalf("the late-linked book lost its series: %v", got)
	}
	if !csLogContains(rep, "the conditional delete found live references in 1 single-book series and 0 merged-from series") {
		t.Fatalf("the refusal must be counted in the warn line an operator reads, got %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase1DeletesAnUnchangedOneBookSeriesThroughTheConditionalDelete(t *testing.T) {
	// Control: nothing changes between the read and the delete, so the delete goes
	// through, and it goes through DeleteSeriesIfUnreferenced. Without it a store
	// that refuses everything passes the test above.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if store.hasSeries(7) {
		t.Fatal("series 7 should have been deleted: nothing references it")
	}
	if len(store.conditionalCalls) != 1 || store.conditionalCalls[0] != 7 {
		t.Fatalf("conditional deletes = %v, want [7]", store.conditionalCalls)
	}
	if csLogContains(rep, "live references") {
		t.Fatalf("nothing was refused, yet the warn line reports it: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase1FailsClosedWhenTheConditionalDeleteErrors(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	cause := csStoreWideErr(database.ErrMemdbIncomplete)
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}

	csRequireStopError(t, (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false), cause, "phase 1")

	if !store.hasSeries(7) {
		t.Fatal("series 7 was deleted although the store could not verify it unreferenced")
	}
	// A store that cannot count would send every delete down this path; the job
	// log an operator reads must say so, not only the error log.
	if !csLogContains(rep, "could not verify 1 single-book series and 0 merged-from series unreferenced") {
		t.Fatalf("the failed delete must be counted in the warn line, got %v", rep.logs)
	}
}

// --- phase 2 ---------------------------------------------------------------

// dupSeriesStore: "Court of Owls" (id 7, one book) and "court of owls" (id 8, the
// keeper: it holds more books) normalize to the same name, so phase 2 merges 7
// into 8. Series 7 has a book so phase 1 (single-book collapse) is not the one
// that handles it: the keeper's count is higher, and phase 1 only fires on a
// one-book series with sequence at most 1, so give series 7 a sequence of 2.
func dupSeriesStore() *csCondStore {
	return newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 8, Name: "court of owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7), SeriesSequence: new(2)},
		database.Book{ID: "night-of-owls-1", SeriesID: new(8)},
		database.Book{ID: "night-of-owls-2", SeriesID: new(8)},
	)
}

func TestCleanupSeriesRun_Phase2KeepsAMergedFromSeriesABookWasLinkedIntoAfterTheMembershipRead(t *testing.T) {
	store := dupSeriesStore()
	store.afterMembership = func(s *csCondStore) { s.linkBook("court-of-owls-2", 7) }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) {
		t.Fatal("series 7 was deleted although a book was linked into it after the membership read")
	}
	if d := store.dangling(); len(d) != 0 {
		t.Fatalf("books %v name a series that no longer exists", d)
	}
	if got := store.books["court-of-owls-2"].SeriesID; got == nil || *got != 7 {
		t.Fatalf("the late-linked book was moved or lost its series: %v", got)
	}
	if got := store.books["court-of-owls-1"].SeriesID; got == nil || *got != 8 {
		t.Fatalf("the book the run knew about was not re-homed onto the keeper: %v", got)
	}
	if !csLogContains(rep, "the conditional delete found live references in 0 single-book series and 1 merged-from series") {
		t.Fatalf("the refusal must be counted in the warn line an operator reads, got %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase2DeletesAnUnchangedMergedFromSeriesThroughTheConditionalDelete(t *testing.T) {
	// Control.
	store := dupSeriesStore()
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if store.hasSeries(7) {
		t.Fatal("series 7 should have been merged away: nothing references it after the re-home")
	}
	if len(store.conditionalCalls) != 1 || store.conditionalCalls[0] != 7 {
		t.Fatalf("conditional deletes = %v, want [7]", store.conditionalCalls)
	}
	if got := store.books["court-of-owls-1"].SeriesID; got == nil || *got != 8 {
		t.Fatalf("book not re-homed onto the keeper: %v", got)
	}
	if csLogContains(rep, "live references") {
		t.Fatalf("nothing was refused, yet the warn line reports it: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase2FailsClosedWhenTheConditionalDeleteErrors(t *testing.T) {
	store := dupSeriesStore()
	cause := csStoreWideErr(database.ErrMemdbIncomplete)
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}

	csRequireStopError(t, (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false), cause, "phase 2")

	if !store.hasSeries(7) {
		t.Fatal("series 7 was deleted although the store could not verify it unreferenced")
	}
	if !csLogContains(rep, "could not verify 0 single-book series and 1 merged-from series unreferenced") {
		t.Fatalf("the failed delete must be counted in the warn line, got %v", rep.logs)
	}
}

// --- the other outcomes of the conditional delete -------------------------

func TestCleanupSeriesRun_Phase1AnUnlinkFailureIsNotCountedAsAFailedDelete(t *testing.T) {
	// The unlink fails, so the delete is never attempted: the series stays by
	// design, and the warn line must not claim the store could not verify it.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	store.modifyErr = errors.New("simulated write failure")
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) || len(store.conditionalCalls) != 0 {
		t.Fatalf("series kept=%v, conditional deletes=%v: nothing may be deleted after a failed unlink", store.hasSeries(7), store.conditionalCalls)
	}
	if csLogContains(rep, "could not verify") {
		t.Fatalf("a failed unlink was reported as a failed delete: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase1KeepsASeriesRenamedAfterTheMembershipRead(t *testing.T) {
	// The delete compares the name this run read with the live row. A series
	// renamed since is somebody's edit: kept, counted, not an error.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	store.afterMembership = func(s *csCondStore) { s.series[7] = database.Series{ID: 7, Name: "Dark Victory"} }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) {
		t.Fatal("a series renamed after the read was deleted")
	}
	if !csLogContains(rep, "kept 1 single-book series and 0 merged-from series whose row was renamed, re-authored or held") {
		t.Fatalf("the kept series must be counted in the warn line, got %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase1KeepsAHeldSeries(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	store.afterMembership = func(s *csCondStore) {
		s.series[7] = database.Series{ID: 7, Name: "The Long Halloween", HeldBy: "repair"}
	}
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) {
		t.Fatal("a held series was deleted")
	}
	if !csLogContains(rep, "kept 1 single-book series and 0 merged-from series whose row was renamed, re-authored or held") {
		t.Fatalf("the kept series must be counted in the warn line, got %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase1SkipsASeriesHeldAtTheRead(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween", HeldBy: "repair"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if b := store.books["long-halloween-1"]; b.SeriesID == nil || *b.SeriesID != 7 {
		t.Fatalf("a held series' book lost its link: %v", b.SeriesID)
	}
	if !store.hasSeries(7) || len(store.conditionalCalls) != 0 {
		t.Fatalf("series kept=%v, conditional deletes=%v: a held series must not even be offered to the delete", store.hasSeries(7), store.conditionalCalls)
	}
	if !csLogContains(rep, "1 single-book series were held when this run started") {
		t.Fatalf("the held-at-read count must be in the warn line, got %v", rep.logs)
	}
	if csLogContains(rep, "renamed, re-authored or held after") {
		t.Fatalf("a hold seen at the read is not the after-the-read race: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_DryRunDoesNotReportAHeldSeriesAsACandidate(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween", HeldBy: "repair"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
	)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, true); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !csLogContains(rep, "1 single-book series were held when this run started") {
		t.Fatalf("a dry run must still name the held series, got %v", rep.logs)
	}
	if !csLogContains(rep, "Kept 1-book series 7") {
		t.Fatalf("the per-series warn line is missing: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_AHeldSeriesWithANameTwinIsNotMergedAway(t *testing.T) {
	// Series 7 is held and has a normalized-name twin, 8, with more books. The
	// held skip must settle 7: otherwise phase 2 groups it and re-homes its book
	// onto 8 before the conditional delete refuses the row.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls", HeldBy: "repair"}, {ID: 8, Name: "court of owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7)},
		database.Book{ID: "night-of-owls-1", SeriesID: new(8), SeriesSequence: new(2)},
		database.Book{ID: "night-of-owls-2", SeriesID: new(8), SeriesSequence: new(3)},
	)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if b := store.books["court-of-owls-1"]; b.SeriesID == nil || *b.SeriesID != 7 {
		t.Fatalf("phase 2 re-homed the book of a held series: %v", b.SeriesID)
	}
	if !store.hasSeries(7) || len(store.conditionalCalls) != 0 {
		t.Fatalf("series 7 kept=%v, conditional deletes=%v", store.hasSeries(7), store.conditionalCalls)
	}
}

func TestCleanupSeriesRun_Phase1CountsASeriesAlreadyDeletedBySomeoneElse(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "The Long Halloween"}, {ID: 8, Name: "Haunted Knight"}},
		database.Book{ID: "long-halloween-1", SeriesID: new(7)},
		database.Book{ID: "haunted-knight-1", SeriesID: new(8), SeriesSequence: new(2)},
		database.Book{ID: "haunted-knight-2", SeriesID: new(8), SeriesSequence: new(3)},
	)
	store.afterMembership = func(s *csCondStore) { delete(s.series, 7) }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !csLogContains(rep, "1 single-book series and 0 merged-from series were already deleted by someone else") {
		t.Fatalf("an already-gone series must be counted, got %v", rep.logs)
	}
	if csLogContains(rep, "live references") {
		t.Fatalf("an already-gone series is not a live reference: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_APhase1SeriesDeletedBySomeoneElseIsNotMergedInPhase2(t *testing.T) {
	// Series 7 collapses in phase 1 and is deleted by someone else just before the
	// run's delete; series 8 has the same normalized name and more books. Phase 2
	// must not group the row that no longer exists: it would try to merge a dead
	// series into 8, hit the reference guard (the book phase 1 unlinked is still
	// counted by the start-of-run counts) and report a refusal that never was one.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 8, Name: "court of owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7)},
		database.Book{ID: "night-of-owls-1", SeriesID: new(8), SeriesSequence: new(2)},
		database.Book{ID: "night-of-owls-2", SeriesID: new(8), SeriesSequence: new(3)},
	)
	store.afterMembership = func(s *csCondStore) { delete(s.series, 7) }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(store.conditionalCalls) != 1 || store.conditionalCalls[0] != 7 {
		t.Fatalf("conditional deletes = %v, want only phase 1's [7]: phase 2 grouped a series that no longer exists", store.conditionalCalls)
	}
	if csLogContains(rep, "Reference guard") {
		t.Fatalf("phase 2 reported a refusal for a series that was already gone: %v", rep.logs)
	}
}

func TestCleanupSeriesRun_Phase2KeepsOneMergedFromSeriesAndStillDeletesTheNext(t *testing.T) {
	// Three series normalize to "court of owls": 8 is the keeper (more books), 7 and
	// 9 are merged into it. A book is linked into 7 after the read: 7 stays, and
	// 9, which nothing references, is still deleted. A kept series must not stop
	// the group.
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 8, Name: "court of owls"}, {ID: 9, Name: "The Court of Owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7), SeriesSequence: new(2)},
		database.Book{ID: "court-of-owls-3", SeriesID: new(9), SeriesSequence: new(2)},
		database.Book{ID: "night-of-owls-1", SeriesID: new(8)},
		database.Book{ID: "night-of-owls-2", SeriesID: new(8)},
	)
	store.afterMembership = func(s *csCondStore) { s.linkBook("court-of-owls-2", 7) }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !store.hasSeries(7) {
		t.Fatal("series 7 was deleted although a book was linked into it after the read")
	}
	if store.hasSeries(9) {
		t.Fatal("series 9 is unreferenced and must still be merged away after 7 was kept")
	}
	if d := store.dangling(); len(d) != 0 {
		t.Fatalf("books %v name a series that no longer exists", d)
	}
	if !csLogContains(rep, "the conditional delete found live references in 0 single-book series and 1 merged-from series") {
		t.Fatalf("the refusal must be counted, got %v", rep.logs)
	}
}

func TestCsMergeSeriesGroup_AStoreErrorStopsTheGroupAndKeepsTheSeries(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 9, Name: "The Court of Owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7)},
		database.Book{ID: "court-of-owls-3", SeriesID: new(9)},
	)
	store.deleteIfErr = csStoreWideErr(database.ErrMemdbIncomplete)
	members := database.SeriesBooksMap{7: {{ID: "court-of-owls-1"}}, 9: {{ID: "court-of-owls-3"}}}

	res, err := csMergeSeriesGroup(store, 8, []database.Series{store.series[7], store.series[9]}, map[int]int{7: 1, 9: 1}, members)
	if err == nil || !strings.Contains(err.Error(), "DeleteSeriesIfUnreferenced(7)") {
		t.Fatalf("err = %v, want the failed delete named", err)
	}
	if res.Merged != 0 || !store.hasSeries(7) || !store.hasSeries(9) {
		t.Fatalf("merged=%d, series 7 kept=%v, series 9 kept=%v: a store that cannot answer must delete nothing",
			res.Merged, store.hasSeries(7), store.hasSeries(9))
	}
	if len(store.conditionalCalls) != 1 {
		t.Fatalf("conditional deletes = %v, want the group to stop at the first error", store.conditionalCalls)
	}
	if res.Kept.Failed != 1 {
		t.Fatalf("Kept.Failed = %d, want the failed delete counted", res.Kept.Failed)
	}
	if !res.Stopped || res.NotReached != 1 {
		t.Fatalf("Stopped=%v NotReached=%d, want a store-wide failure to stop the group with the series behind it not reached", res.Stopped, res.NotReached)
	}
}

// A failure about one row does not stop the group: the series is counted failed,
// logged, and the next one is settled.
func TestCsMergeSeriesGroup_ARowLocalDeleteErrorKeepsTheSeriesAndGoesOn(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 9, Name: "The Court of Owls"}},
		database.Book{ID: "court-of-owls-1", SeriesID: new(7)},
		database.Book{ID: "court-of-owls-3", SeriesID: new(9)},
	)
	store.deleteErrFor = map[int]error{7: errors.New("decode series 7: invalid character")}
	members := database.SeriesBooksMap{7: {{ID: "court-of-owls-1"}}, 9: {{ID: "court-of-owls-3"}}}

	res, err := csMergeSeriesGroup(store, 8, []database.Series{store.series[7], store.series[9]}, map[int]int{7: 1, 9: 1}, members)
	if err != nil {
		t.Fatalf("err = %v, want a row-local failure not to fail the group", err)
	}
	if res.Stopped || res.NotReached != 0 || res.Kept.Failed != 1 || res.Merged != 1 {
		t.Fatalf("Stopped=%v NotReached=%d Failed=%d Merged=%d, want 7 failed, 9 merged, nothing stopped",
			res.Stopped, res.NotReached, res.Kept.Failed, res.Merged)
	}
	if !store.hasSeries(7) || store.hasSeries(9) {
		t.Fatalf("series 7 kept=%v, series 9 kept=%v", store.hasSeries(7), store.hasSeries(9))
	}
}

// csStubDeleter is a SeriesUnreferencedDeleter with a canned answer, for the
// mapping table below (the stateful csCondStore covers the answers coming from
// the rows).
type csStubDeleter func() (bool, error)

func (f csStubDeleter) DeleteSeriesIfUnreferenced(int, string, *int) (bool, error) { return f() }

func TestCsDeleteSeriesIfUnreferenced_MapsEveryStoreAnswer(t *testing.T) {
	boom := errors.New("memdb is missing rows")
	cases := []struct {
		name    string
		gone    bool
		err     error
		want    csDeleteOutcome
		wantErr bool
	}{
		{"deleted", true, nil, csDeleted, false},
		{"still referenced", false, nil, csKeptReferenced, false},
		{"already gone", false, database.ErrSeriesNotFound, csAlreadyGone, false},
		{"already gone, wrapped", false, fmt.Errorf("store: %w", database.ErrSeriesNotFound), csAlreadyGone, false},
		{"renamed or re-authored", false, database.ErrSeriesChanged, csKeptChanged, false},
		{"held", false, database.ErrSeriesHeld, csKeptChanged, false},
		{"cannot count", false, boom, csDeleteFailed, true},
		// A store answering (true, err) is a contract violation; the error wins,
		// the row is not reported deleted.
		{"error with gone set", true, boom, csDeleteFailed, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := csDeleteSeriesIfUnreferenced(csStubDeleter(func() (bool, error) { return c.gone, c.err }),
				database.Series{ID: 7, Name: "The Long Halloween"})
			if got != c.want || (err != nil) != c.wantErr {
				t.Fatalf("outcome=%d err=%v, want outcome=%d err=%v", got, err, c.want, c.wantErr)
			}
		})
	}
}

// --- the capability --------------------------------------------------------

// csNoDeleter is a JobStore with no conditional delete: the MockStore has none
// (it is not on database.Store) and nothing here adds it.
type csNoDeleter struct {
	*database.MockStore
}

func (csNoDeleter) GetAllSeriesBookRefCounts() (map[int]int, error) { return map[int]int{}, nil }

func TestCleanupSeriesRun_RefusesALiveRunWithoutTheConditionalDelete(t *testing.T) {
	reads := 0
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		reads++
		return []database.Series{{ID: 7, Name: "The Long Halloween"}}, nil
	}
	rep := &csPhase1Reporter{}

	err := (&cleanupSeriesJob{}).Run(context.Background(), csNoDeleter{mock}, rep, false)
	if err == nil || !strings.Contains(err.Error(), "cannot delete a series only if nothing references it") {
		t.Fatalf("err = %v, want a refusal naming the missing capability", err)
	}
	if reads != 0 {
		t.Fatalf("the run read the library %d times before refusing; it must refuse first", reads)
	}
}

func TestCleanupSeriesRun_ADryRunDoesNotNeedTheConditionalDelete(t *testing.T) {
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) { return nil, nil }
	mock.GetAllSeriesBookCountsFunc = func() (map[int]int, error) { return map[int]int{}, nil }
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), csNoDeleter{mock}, rep, true); err != nil {
		t.Fatalf("a dry run writes nothing and must not need the capability: %v", err)
	}
}

// csStoreWideErr is what a store that cannot count references to any series
// returns from the delete, wrapped the way the real one wraps it.
func csStoreWideErr(sentinel error) error {
	return fmt.Errorf("count references to series 7: %w", sentinel)
}
