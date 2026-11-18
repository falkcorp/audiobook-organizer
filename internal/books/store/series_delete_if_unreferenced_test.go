// file: internal/books/store/series_delete_if_unreferenced_test.go
// version: 1.3.1
// guid: 0b5d7e92-3c4a-4f18-a6d1-52e8c9f03b74
// last-edited: 2026-11-18

package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The tests below are about ONE window: the time between "no book references
// this series" and "the series row is deleted". A count and a DeleteSeries are
// two store calls, and a book written into the series between them leaves a
// book pointing at a series id that no longer resolves.
//
// They share the package-level seriesDeleteTestHookAfterCount, so none runs in
// parallel.

// deleteHookPositions are the two points inside DeleteSeriesIfUnreferenced
// where a test can act with the method's locks held: right after the count (the
// window between counting and taking the locks would show here) and right
// before the delete (the window between the check and the delete).
var deleteHookPositions = []struct {
	name string
	set  func(fn func(id int))
}{
	{"after the count", func(fn func(int)) { seriesDeleteTestHookAfterCount = fn }},
	{"before the delete", func(fn func(int)) { seriesDeleteTestHookBeforeDelete = fn }},
}

func setSeriesDeleteHook(t *testing.T, set func(fn func(id int)), fn func(id int)) {
	t.Helper()
	set(fn)
	t.Cleanup(func() {
		seriesDeleteTestHookAfterCount = nil
		seriesDeleteTestHookBeforeDelete = nil
	})
}

func repointBook(s *PebbleStore, bookID string, seriesID int, atWrite func()) error {
	_, err := s.ModifyBook(bookID, func(b *Book) error {
		if atWrite != nil {
			atWrite()
		}
		b.SeriesID = &seriesID
		return nil
	})
	return err
}

// The gap itself, on the old path: count says zero, a book is written into the
// series, DeleteSeries (which never looks) deletes it. The book is left naming
// a series that does not resolve. This is why DeleteSeriesIfUnreferenced
// exists; it is the failure the tests below pin shut for the new method, and
// DeleteSeries stays the unconditional delete it always was.
func TestDeleteSeries_CountThenDeleteStrandsABookWrittenBetween(t *testing.T) {
	s := seedRefStore(t)
	ser, err := s.CreateSeries("Hush", nil)
	require.NoError(t, err)
	book, err := s.CreateBook(&Book{Title: "Hush Part One", FilePath: "/gap/hush-1"})
	require.NoError(t, err)

	n, err := s.GetSeriesBookRefCount(ser.ID)
	require.NoError(t, err)
	require.Zero(t, n, "precondition: the count that authorises the delete says zero")

	require.NoError(t, repointBook(s, book.ID, ser.ID, nil)) // lands between count and delete
	require.NoError(t, s.DeleteSeries(ser.ID))

	got, err := s.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SeriesID)
	require.Equal(t, ser.ID, *got.SeriesID)
	gone, err := s.GetSeriesByID(ser.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "the book is left pointing at a series that no longer resolves")
}

// A book written into the series after the count and before the delete must
// not land in that window. The hook runs with the method's locks held; the
// writer it starts cannot finish until the method has returned, and when it
// does the series is already gone (so the stranded reference, if the writer
// writes anyway, is the writer's own stale read and not a window inside this
// method). Both hook positions: after the count, and right before the delete.
func TestDeleteSeriesIfUnreferenced_AWriteBetweenCountAndDeleteCannotLand(t *testing.T) {
	for _, pos := range deleteHookPositions {
		t.Run(pos.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("Year One", nil)
			require.NoError(t, err)
			book, err := s.CreateBook(&Book{Title: "Year One Part One", FilePath: "/gap/year-one-1"})
			require.NoError(t, err)

			var (
				landed        = make(chan struct{})
				mu            sync.Mutex
				seriesAtWrite *Series
				sawRow        bool
				writeErr      error
			)
			setSeriesDeleteHook(t, pos.set, func(id int) {
				go func() {
					defer close(landed)
					err := repointBook(s, book.ID, id, func() {
						row, _ := s.GetSeriesByID(id)
						mu.Lock()
						seriesAtWrite, sawRow = row, true
						mu.Unlock()
					})
					mu.Lock()
					writeErr = err
					mu.Unlock()
				}()
				select {
				case <-landed:
					t.Error("a book write landed between the count and the delete")
				case <-time.After(300 * time.Millisecond):
				}
			})

			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			require.NoError(t, err)
			require.True(t, deleted, "nothing referenced the series when it was counted under the locks")

			select {
			case <-landed:
			case <-time.After(10 * time.Second):
				t.Fatal("the held-back write never completed after the method returned")
			}
			mu.Lock()
			defer mu.Unlock()
			require.True(t, sawRow)
			require.Nil(t, seriesAtWrite, "the write must run after the delete, never between the count and it")
			// It runs after the delete, so it names a series that is gone: the
			// write verifies that and is refused rather than stranding the book
			// (book_write_series_exists_test.go pins the same from the writer's side).
			require.ErrorIs(t, writeErr, ErrSeriesNotFound)
		})
	}
}

// The exclusion above is the property; this pins what provides it, so that a
// change which keeps the ordering by luck (a slow machine, a lucky stripe) still
// fails: at both hook positions every book write stripe and the series
// name-index lock are held. TryLock cannot be satisfied by timing.
func TestDeleteSeriesIfUnreferenced_HoldsEveryBookStripeAndTheSeriesLockAcrossTheCheck(t *testing.T) {
	for _, pos := range deleteHookPositions {
		t.Run(pos.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("The Long Halloween", nil)
			require.NoError(t, err)

			var checked atomic.Bool
			setSeriesDeleteHook(t, pos.set, func(int) {
				checked.Store(true)
				free := 0
				for i := range s.bookLocks {
					if s.bookLocks[i].TryLock() {
						s.bookLocks[i].Unlock()
						free++
					}
				}
				if free != 0 {
					t.Errorf("%d of %d book write stripes are free %s", free, len(s.bookLocks), pos.name)
				}
				if s.nameIdx.series.TryLock() {
					s.nameIdx.series.Unlock()
					t.Errorf("the series name-index lock is free %s", pos.name)
				}
			})
			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			require.NoError(t, err)
			require.True(t, deleted)
			require.True(t, checked.Load(), "the hook did not run")

			// Everything is released afterwards: a book write and a series write both go
			// straight through (a leaked lock would hang here).
			book, err := s.CreateBook(&Book{Title: "The Long Halloween Part One", FilePath: "/gap/tlh-1"})
			require.NoError(t, err)
			seedSeriesRow(t, s, 77)
			require.NoError(t, repointBook(s, book.ID, 77, nil))
			_, err = s.CreateSeries("Dark Victory", nil)
			require.NoError(t, err)
		})
	}
}

// What counts as a reference: every book row naming the series, whatever its
// trash or primary-version state, exactly as GetSeriesBookRefCount counts. Each
// case is a series referenced by one kind of row only, so a check that skips a
// kind deletes it.
func TestDeleteSeriesIfUnreferenced_KeepsASeriesAnyKindOfRowReferences(t *testing.T) {
	cases := []struct {
		name             string
		primary, trashed bool
	}{
		{"a live primary book", true, false},
		{"only a trashed book", true, true},
		{"only a secondary version", false, false},
		{"only a trashed secondary version", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("Knightfall", nil)
			require.NoError(t, err)
			mkBook(t, s, "Knightfall Part One", ser.ID, tc.primary, tc.trashed)

			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			require.NoError(t, err)
			require.False(t, deleted, "a referenced series must be kept")
			got, err := s.GetSeriesByID(ser.ID)
			require.NoError(t, err)
			require.NotNil(t, got, "the series row must still be there")
			byName, err := s.GetSeriesByName("Knightfall", nil)
			require.NoError(t, err)
			require.NotNil(t, byName, "and still resolve by name")
		})
	}
}

func TestDeleteSeriesIfUnreferenced_DeletesAnUnreferencedSeriesAndItsNameIndex(t *testing.T) {
	s := seedRefStore(t)
	ser, err := s.CreateSeries("No Man's Land", nil)
	require.NoError(t, err)
	other, err := s.CreateSeries("Contagion", nil)
	require.NoError(t, err)
	mkBook(t, s, "Contagion Part One", other.ID, true, false) // another series' reference does not count

	deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
	require.NoError(t, err)
	require.True(t, deleted)
	got, err := s.GetSeriesByID(ser.ID)
	require.NoError(t, err)
	require.Nil(t, got)
	byName, err := s.GetSeriesByName("No Man's Land", nil)
	require.NoError(t, err)
	require.Nil(t, byName, "the name index entry goes with the row")
	kept, err := s.GetSeriesByID(other.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "the other series is untouched")
}

func TestDeleteSeriesIfUnreferenced_Refusals(t *testing.T) {
	t.Run("a series that does not exist", func(t *testing.T) {
		s := seedRefStore(t)
		deleted, err := s.DeleteSeriesIfUnreferenced(424242, "Detective Comics", nil)
		require.ErrorIs(t, err, ErrSeriesNotFound)
		require.False(t, deleted)
	})
	t.Run("a held series", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Zero Year", nil)
		require.NoError(t, err)
		require.NoError(t, s.HoldSeries(ser.ID, "test-hold"))
		deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
		require.ErrorIs(t, err, ErrSeriesHeld)
		require.False(t, deleted)
		got, err := s.GetSeriesByID(ser.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
	})
	t.Run("a memdb known to be missing book rows", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Endgame", nil)
		require.NoError(t, err)
		s.mem().recordLostRows(memTableBooks, 1)
		deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
		require.ErrorIs(t, err, ErrMemdbIncomplete, "a short count answers low, which authorises a delete: refuse instead")
		require.False(t, deleted)
		got, err := s.GetSeriesByID(ser.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
	})
}

// The caller read the series (and journaled what it read) before calling; the
// delete is only of that row. A row that has since been renamed, or given another
// author or none, is not deleted: nothing the caller recorded would describe it.
func TestDeleteSeriesIfUnreferenced_RefusesARowChangedSinceTheCallerReadIt(t *testing.T) {
	author := 7
	other := 8
	for _, tc := range []struct {
		name       string
		expectName string
		expectAuth *int
	}{
		{"a different name", "Batman: Hush", &author},
		{"a name differing only by case", "the long halloween", &author},
		{"a different author", "The Long Halloween", &other},
		{"an author where the row has none", "The Long Halloween", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("The Long Halloween", &author)
			require.NoError(t, err)

			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, tc.expectName, tc.expectAuth)

			require.ErrorIs(t, err, ErrSeriesChanged)
			require.False(t, deleted)
			got, err := s.GetSeriesByID(ser.ID)
			require.NoError(t, err)
			require.NotNil(t, got, "a refused delete leaves the row")
			byName, err := s.GetSeriesByName("The Long Halloween", &author)
			require.NoError(t, err)
			require.NotNil(t, byName, "and its name index entry")
		})
	}
	t.Run("a row that has no author, expected with one", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Dark Victory", nil)
		require.NoError(t, err)
		deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, "Dark Victory", &author)
		require.ErrorIs(t, err, ErrSeriesChanged)
		require.False(t, deleted)
	})
	t.Run("a rename between the caller's read and the call", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Year One", nil)
		require.NoError(t, err)
		read := *ser // what the caller read and journaled
		require.NoError(t, s.RenameSeriesIfCurrent(ser.ID, "Year One", "Batman: Year One"))

		deleted, err := s.DeleteSeriesIfUnreferenced(read.ID, read.Name, read.AuthorID)

		require.ErrorIs(t, err, ErrSeriesChanged)
		require.False(t, deleted)
		got, err := s.GetSeriesByID(ser.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "Batman: Year One", got.Name, "the rename stands")
	})
}

// The compare and the delete are one step under the series lock: a rename that
// starts while the method runs waits for it, and then finds the series gone. Run
// at both hook positions, so a compare moved out from under the lock, or a lock
// released before the delete, shows here.
func TestDeleteSeriesIfUnreferenced_ARenameCannotLandBetweenTheCompareAndTheDelete(t *testing.T) {
	for _, pos := range deleteHookPositions {
		t.Run(pos.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("Zero Year", nil)
			require.NoError(t, err)

			var (
				done      = make(chan struct{})
				mu        sync.Mutex
				renameErr error
			)
			setSeriesDeleteHook(t, pos.set, func(id int) {
				go func() {
					defer close(done)
					err := s.RenameSeriesIfCurrent(id, "Zero Year", "Batman: Zero Year")
					mu.Lock()
					renameErr = err
					mu.Unlock()
				}()
				select {
				case <-done:
					t.Error("a rename landed while the compare and the delete were running")
				case <-time.After(300 * time.Millisecond):
				}
			})

			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			require.NoError(t, err)
			require.True(t, deleted)

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the held-back rename never completed after the method returned")
			}
			mu.Lock()
			defer mu.Unlock()
			require.ErrorIs(t, renameErr, ErrRenameSeriesNotFound, "the rename ran after the delete")
		})
	}
}

// A store built without a memdb counts with the Pebble scan, and answers the
// same: the arm is not a way round the check.
func TestDeleteSeriesIfUnreferenced_WithoutAMemdbCountsWithThePebbleScan(t *testing.T) {
	s := seedRefStore(t)
	s.UseMemDB = false
	held, err := s.CreateSeries("Batman Eternal", nil)
	require.NoError(t, err)
	free, err := s.CreateSeries("Batman Incorporated", nil)
	require.NoError(t, err)
	mkBook(t, s, "Batman Eternal Part One", held.ID, true, true)

	deleted, err := s.DeleteSeriesIfUnreferenced(held.ID, held.Name, held.AuthorID)
	require.NoError(t, err)
	require.False(t, deleted)
	deleted, err = s.DeleteSeriesIfUnreferenced(free.ID, free.Name, free.AuthorID)
	require.NoError(t, err)
	require.True(t, deleted)
}

// The lock order the method documents holds up under load: writers that take a
// book stripe and then a series lock (a ModifyBook callback that creates a
// series) run against repeated deletes that take every stripe and then the
// series lock. A reversed order would deadlock here; the timeout turns that
// into a failure instead of a hung run. -race covers the data races.
func TestDeleteSeriesIfUnreferenced_DoesNotDeadlockWithWritersThatCreateSeries(t *testing.T) {
	s := seedRefStore(t)
	const books = 12
	ids := make([]string, books)
	for i := range ids {
		b, err := s.CreateBook(&Book{Title: fmt.Sprintf("Detective Comics %d", i), FilePath: fmt.Sprintf("/lock/dc-%d", i)})
		require.NoError(t, err)
		ids[i] = b.ID
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				id := ids[(w*7+i)%books]
				_, _ = s.ModifyBook(id, func(b *Book) error {
					ser, err := s.CreateSeries(fmt.Sprintf("Knightfall %d", (w+i)%5), nil)
					if err != nil {
						return err
					}
					b.SeriesID = &ser.ID
					return nil
				})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			ser, err := s.CreateSeries(fmt.Sprintf("No Mans Land %d", i%3), nil)
			if err == nil {
				_, _ = s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			}
		}
	}()
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock: writers holding a book stripe and the delete holding every stripe never finished")
	}
}

func TestAsCapability_ResolvesSeriesUnreferencedDeleter(t *testing.T) {
	s := seedRefStore(t)
	d, ok := AsCapability[SeriesUnreferencedDeleter](&decoratorStore{Store: s})
	require.True(t, ok, "the delete must be reachable through a store decorator")
	require.NotNil(t, d)
}

// SeriesDeleteReady is the up-front form of the refusals the delete itself makes:
// it must agree with them in every state, or a caller that probes first would
// write and then meet the refusal the probe was meant to head off.
func TestSeriesDeleteReady_AgreesWithTheDelete(t *testing.T) {
	t.Run("a healthy store is ready and the delete answers", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Year One", nil)
		require.NoError(t, err)
		require.NoError(t, s.SeriesDeleteReady())
		deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
		require.NoError(t, err)
		require.True(t, deleted)
	})
	t.Run("a memdb known to be missing book rows", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Endgame", nil)
		require.NoError(t, err)
		s.mem().recordLostRows(memTableBooks, 1)
		require.ErrorIs(t, s.SeriesDeleteReady(), ErrMemdbIncomplete)
		_, derr := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
		require.ErrorIs(t, derr, ErrMemdbIncomplete)
	})
	t.Run("a memdb that is not serving", func(t *testing.T) {
		s := seedRefStore(t)
		ser, err := s.CreateSeries("Knightfall", nil)
		require.NoError(t, err)
		s.memPtr.Store(nil)
		require.ErrorIs(t, s.SeriesDeleteReady(), ErrSeriesDeleteMemdbNotServing)
		_, derr := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
		require.ErrorIs(t, derr, ErrSeriesDeleteMemdbNotServing)
	})
	t.Run("a store built without a memdb is ready", func(t *testing.T) {
		s := seedRefStore(t)
		s.UseMemDB = false
		require.NoError(t, s.SeriesDeleteReady())
	})
	t.Run("resolves through a store decorator", func(t *testing.T) {
		s := seedRefStore(t)
		r, ok := AsCapability[SeriesDeleteReadiness](&decoratorStore{Store: s})
		require.True(t, ok)
		require.NotNil(t, r)
	})
}
