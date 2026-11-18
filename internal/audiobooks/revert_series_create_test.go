// file: internal/audiobooks/revert_series_create_test.go
// version: 1.0.0
// guid: 5e1c8a37-d204-4b69-9f0a-7c3b82e6d915
// last-edited: 2026-11-18

package audiobooks

import (
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
