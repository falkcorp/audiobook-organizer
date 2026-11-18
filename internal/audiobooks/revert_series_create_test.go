// file: internal/audiobooks/revert_series_create_test.go
// version: 1.3.0
// guid: 5e1c8a37-d204-4b69-9f0a-7c3b82e6d915
// last-edited: 2026-11-18

package audiobooks

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// linkingStore is a PebbleStore that, once, links book "b2" into the base series
// at the moment a revert asks about deleting it: right after the revert's own
// count answered (the old count-then-delete path) or right before the
// conditional delete (the path that counts and deletes under the store's locks).
// Either way a book lands in the series AFTER the revert last looked at it.
type linkingStore struct {
	*database.PebbleStore
	baseID int
	once   sync.Once
	fired  int
}

func (s *linkingStore) link() {
	s.once.Do(func() {
		s.fired++
		if _, err := s.ModifyBook("b2", func(b *database.Book) error {
			b.SeriesID = &s.baseID
			return nil
		}); err != nil {
			panic(err)
		}
	})
}

func (s *linkingStore) GetSeriesBookRefCount(id int) (int, error) {
	n, err := s.PebbleStore.GetSeriesBookRefCount(id)
	s.link() // the answer is already stale when the caller gets it
	return n, err
}

func (s *linkingStore) DeleteSeriesIfUnreferenced(id int, name string, authorID *int) (bool, error) {
	s.link()
	return s.PebbleStore.DeleteSeriesIfUnreferenced(id, name, authorID)
}

func baseSeriesStillThere(t *testing.T, store *database.PebbleStore, id int) {
	t.Helper()
	got, err := store.GetSeriesByID(id)
	if err != nil || got == nil {
		t.Fatalf("base series %d is gone (%v): a book that names it now points at nothing", id, err)
	}
}

// A book is linked into the created base series between the revert's last look
// at it and the delete. The series keeps its book: the revert reports it kept
// (not failed), and nothing is left pointing at a deleted id.
func TestRevertSeriesCreate_ABookLinkedAfterTheLastLookKeepsTheSeries(t *testing.T) {
	f := newDenumberFixture(t)
	if _, err := f.store.CreateBook(&database.Book{ID: "b2", Title: "Year One"}); err != nil {
		t.Fatal(err)
	}
	ls := &linkingStore{PebbleStore: f.store, baseID: f.base.ID}

	res, err := NewRevertService(ls).RevertOperation(f.op)

	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if ls.fired != 1 {
		t.Fatalf("the late link ran %d times, want exactly 1 (the test would pass without proving anything)", ls.fired)
	}
	baseSeriesStillThere(t, f.store, f.base.ID)
	b2, _ := f.store.GetBookByID("b2")
	if b2.SeriesID == nil || *b2.SeriesID != f.base.ID {
		t.Fatalf("b2 = series %v, want the base %d", b2.SeriesID, f.base.ID)
	}
	if res.SeriesKept != 1 || res.Failed != 0 {
		t.Errorf("result = %+v, want the created series reported kept and nothing failed", res)
	}
	if !keptMentions(res, "still references it") {
		t.Errorf("kept notes %v do not say a book still references the series", res.Kept)
	}
}

func keptMentions(res *RevertResult, text string) bool {
	for _, k := range res.Kept {
		if strings.Contains(k.Text, text) {
			return true
		}
	}
	return false
}

// A base series renamed, held, or already deleted since the operation made it is
// not the operation's to delete: each is a kept outcome in the report, and the
// revert still succeeds.
func TestRevertSeriesCreate_ASeriesChangedHeldOrGoneIsReportedKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		act    func(t *testing.T, f *denumberFixture)
		note   string
		exists bool
	}{
		{"renamed", func(t *testing.T, f *denumberFixture) {
			if err := f.store.RenameSeriesIfCurrent(f.base.ID, f.base.Name, "Court of Owls (Earth One)"); err != nil {
				t.Fatal(err)
			}
		}, "renamed", true},
		{"held", func(t *testing.T, f *denumberFixture) {
			if err := f.store.HoldSeries(f.base.ID, "series-prune-fixer"); err != nil {
				t.Fatal(err)
			}
		}, "held", true},
		{"already deleted", func(t *testing.T, f *denumberFixture) {
			if err := f.store.DeleteSeries(f.base.ID); err != nil {
				t.Fatal(err)
			}
		}, "no longer exists", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDenumberFixture(t)
			tc.act(t, f)

			res, err := NewRevertService(f.store).RevertOperation(f.op)

			if err != nil || res.Failed != 0 {
				t.Fatalf("RevertOperation: %v (%+v)", err, res)
			}
			if res.SeriesKept != 1 || !keptMentions(res, tc.note) {
				t.Errorf("result = %+v, want one kept series whose note mentions %q (kept: %v)", res, tc.note, res.Kept)
			}
			got, _ := f.store.GetSeriesByID(f.base.ID)
			if (got != nil) != tc.exists {
				t.Errorf("base series present = %v, want %v", got != nil, tc.exists)
			}
		})
	}
}

// noConditionalDelete is a store the revert can read through but that cannot
// check-and-delete: the revert fails the row closed instead of falling back to a
// count and a plain delete.
type noConditionalDelete struct{ revertServiceStore }

func TestRevertSeriesCreate_AStoreWithoutTheConditionalDeleteFailsClosed(t *testing.T) {
	f := newDenumberFixture(t)

	res, err := NewRevertService(noConditionalDelete{f.store}).RevertOperation(f.op)

	// The wrapper cannot restore series either, so the operation's other rows
	// fail too; what matters is that the series_create row is among the failures
	// with the reason named, and that nothing was deleted.
	if err == nil || res.Failed < 1 {
		t.Fatalf("err = %v, result %+v, want the series_create row failed", err, res)
	}
	if !strings.Contains(err.Error(), "revert created series") || !strings.Contains(err.Error(), "check and delete a series in one step") {
		t.Errorf("error %q does not say the created series could not be checked and deleted in one step", err)
	}
	if res.SeriesKept != 0 {
		t.Errorf("a store that could not answer was reported as a kept series: %+v", res)
	}
	baseSeriesStillThere(t, f.store, f.base.ID)
}

// failingBookStore fails every ModifyBook of one book while fail is set: the
// transient write error that leaves one of the operation's own series_id rows
// unrestored.
type failingBookStore struct {
	*database.PebbleStore
	bookID string
	fail   bool
}

func (s *failingBookStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.fail && id == s.bookID {
		return nil, errors.New("simulated transient write failure")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func changeReverted(t *testing.T, store *database.PebbleStore, op, id string) bool {
	t.Helper()
	rows, err := store.GetOperationChanges(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.RevertedAt != nil
		}
	}
	t.Fatalf("change %s not found", id)
	return false
}

// The book the operation moved into the base is still in it because ITS OWN
// series_id row failed to restore. The base is then not "somebody else's": it is
// owed. The series_create row must stay owed (not be stamped reverted as kept),
// or the retry puts the book back and leaves an empty base with no row left to
// remove it.
func TestRevertSeriesCreate_AnUnrestoredOwnLinkLeavesTheCreateOwed(t *testing.T) {
	f := newDenumberFixture(t)
	fs := &failingBookStore{PebbleStore: f.store, bookID: "b1", fail: true}

	res, _ := NewRevertService(fs).RevertOperation(f.op)

	if res == nil || res.Failed < 2 {
		t.Fatalf("result = %+v, want the book's link row and the series_create row both failed", res)
	}
	if res.SeriesKept != 0 {
		t.Errorf("the base was reported kept although the operation's own book is still in it: %+v", res)
	}
	if changeReverted(t, f.store, f.op, "r1") {
		t.Fatal("the series_create row was stamped reverted while the operation's own link into it was unrestored")
	}
	baseSeriesStillThere(t, f.store, f.base.ID)

	// The retry: the write works now, the book goes back, and the base goes.
	fs.fail = false
	res, err := NewRevertService(fs).RevertOperation(f.op)
	if err != nil || res.Failed != 0 {
		t.Fatalf("retry: %v (%+v)", err, res)
	}
	if got, _ := f.store.GetSeriesByID(f.base.ID); got != nil {
		t.Fatalf("the retry left the created base series %d in place", f.base.ID)
	}
	b1, _ := f.store.GetBookByID("b1")
	if b1.SeriesID == nil || *b1.SeriesID != f.src.ID {
		t.Fatalf("b1 = series %v, want the numbered series %d", b1.SeriesID, f.src.ID)
	}
	if !changeReverted(t, f.store, f.op, "r1") {
		t.Error("the series_create row is not marked reverted after the retry removed the series")
	}
}

// An unrestored row of the operation that does NOT move a book into the base
// does not make the base owed: a book somebody else linked into it keeps it, and
// that is a kept outcome whatever else failed.
func TestRevertSeriesCreate_AnUnrelatedUnrestoredRowDoesNotMakeTheCreateOwed(t *testing.T) {
	f := newDenumberFixture(t)
	if _, err := f.store.CreateBook(&database.Book{ID: "b2", Title: "Year One"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateBook(&database.Book{ID: "b9", Title: "Zero Year"}); err != nil {
		t.Fatal(err)
	}
	// A link row of this operation into some other series, refused at revert
	// because the book is not in that series any more.
	other := f.base.ID + 1000
	if err := database.RecordOperationChange(f.store, &database.OperationChange{
		OperationID: f.op, ID: "r5", BookID: "b9", ChangeType: "metadata_update", FieldName: "series_id",
		OldValue: strconv.Itoa(other + 1), NewValue: strconv.Itoa(other),
	}); err != nil {
		t.Fatal(err)
	}
	ls := &linkingStore{PebbleStore: f.store, baseID: f.base.ID}

	res, _ := NewRevertService(ls).RevertOperation(f.op)

	if res == nil || res.SeriesKept != 1 {
		t.Fatalf("result = %+v, want the base kept for the outside book", res)
	}
	if !keptMentions(res, "still references it") {
		t.Errorf("kept notes %v do not say a book still references the series", res.Kept)
	}
	if !changeReverted(t, f.store, f.op, "r1") {
		t.Error("a kept series_create row is not stamped reverted")
	}
}

// failOnceDelete makes the first conditional delete fail with a transient error.
type failOnceDelete struct {
	*database.PebbleStore
	failed bool
}

func (s *failOnceDelete) DeleteSeriesIfUnreferenced(id int, name string, authorID *int) (bool, error) {
	if !s.failed {
		s.failed = true
		return false, errors.New("simulated transient delete failure")
	}
	return s.PebbleStore.DeleteSeriesIfUnreferenced(id, name, authorID)
}

// The operation's own links into the base were restored by an EARLIER run (their
// rows are marked reverted); only the series_create row is left. A book somebody
// else links into the base before the retry keeps it: that is a kept outcome,
// not an owed row, because nothing of the operation's own is left in the series.
func TestRevertSeriesCreate_AnEarlierRunsRestoredLinkIsNotOwed(t *testing.T) {
	f := newDenumberFixture(t)
	if _, err := f.store.CreateBook(&database.Book{ID: "b2", Title: "Year One"}); err != nil {
		t.Fatal(err)
	}
	fs := &failOnceDelete{PebbleStore: f.store}

	res, _ := NewRevertService(fs).RevertOperation(f.op)
	if res == nil || res.Failed != 1 || !changeReverted(t, f.store, f.op, "r2") || changeReverted(t, f.store, f.op, "r1") {
		t.Fatalf("setup: the first run should restore the links and fail only the series_create row, got %+v", res)
	}
	if _, err := f.store.ModifyBook("b2", func(b *database.Book) error {
		b.SeriesID = &f.base.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := NewRevertService(fs).RevertOperation(f.op)

	if err != nil || res.Failed != 0 || res.SeriesKept != 1 {
		t.Fatalf("retry: %v (%+v), want the base kept for the outside book and nothing failed", err, res)
	}
	baseSeriesStillThere(t, f.store, f.base.ID)
}

// A series_create row reverted on its own, with no plan (a lone row, as the
// retire tests do), has no hand-off table to consult: a referenced series is
// kept, as before, and nothing dereferences the missing plan.
func TestRevertSeriesCreate_ALoneRowWithoutAPlanKeepsAReferencedSeries(t *testing.T) {
	f := newDenumberFixture(t)
	if _, err := f.store.ModifyBook("b1", func(b *database.Book) error {
		b.SeriesID = &f.base.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.GetOperationChanges(f.op)
	if err != nil {
		t.Fatal(err)
	}
	var create *database.OperationChange
	for _, r := range rows {
		if r.ID == "r1" {
			create = r
		}
	}
	if create == nil {
		t.Fatal("setup: no r1 row")
	}

	err = NewRevertService(f.store).revertChangeIn(create, nil)

	var kept *seriesKept
	if !errors.As(err, &kept) {
		t.Fatalf("revertChangeIn(series_create, nil plan) = %v, want the series reported kept", err)
	}
	baseSeriesStillThere(t, f.store, f.base.ID)
}

// The summary says what kept now covers. "No longer the operation's to delete"
// was untrue for a base kept because of the operation's own book.
func TestRevertResult_SummaryNamesWhyASeriesWasKept(t *testing.T) {
	text := (&RevertResult{Total: 4, Restored: 4, SeriesKept: 1}).Summary()

	if strings.Contains(text, "no longer the operation's to delete") {
		t.Errorf("summary %q still says kept series are no longer the operation's to delete", text)
	}
	if !strings.Contains(text, "1 created series kept") || !strings.Contains(text, "a book it did not move") {
		t.Errorf("summary %q does not say why a series was kept", text)
	}
}
