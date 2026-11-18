// file: internal/books/store/book_write_series_exists_test.go
// version: 1.0.1
// guid: 5c0e8a14-6b27-4d93-a1f8-3e9d27b40c61
// last-edited: 2026-11-18

package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A book write that sets series_id names a series row. The delete the
// maintenance ops use (DeleteSeriesIfUnreferenced) is atomic against every book
// write, but a writer that read series N BEFORE the delete and writes
// series_id = N AFTER it never looks again: it strands its own book on a row
// that is gone, and every reader renders that book with no series. These tests
// pin the write verifying the series itself, for both row writers (createBook,
// updateBookLockedMode) and every path through them.

// seedSeriesRow makes series row id exist under a fixed id, for fixtures that
// name a series by a constant. A book write now refuses a series that does not
// exist, so a fixture that links books to series 900 creates series 900 first.
// A series that already exists (made by CreateSeries under the same id) is left
// as it is.
func seedSeriesRow(t testing.TB, s any, id int) {
	t.Helper()
	if g := AsSeriesRefGetter(s); g != nil {
		if ser, err := g.GetSeriesByID(id); err == nil && ser != nil {
			return
		}
	}
	r, ok := AsCapability[SeriesRestorer](s)
	require.True(t, ok, "the store under test restores series")
	require.NoError(t, r.RestoreSeries(id, fmt.Sprintf("Fixture Series %d", id), nil))
}

// deletedSeries makes a series and deletes it, returning the id a stale writer
// would still hold.
func deletedSeries(t *testing.T, s *PebbleStore, name string) int {
	t.Helper()
	ser, err := s.CreateSeries(name, nil)
	require.NoError(t, err)
	deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
	require.NoError(t, err)
	require.True(t, deleted, "precondition: nothing referenced the series")
	return ser.ID
}

func TestCreateBook_RefusesASeriesThatDoesNotExist(t *testing.T) {
	s := seedRefStore(t)
	gone := deletedSeries(t, s, "Hush")

	_, err := s.CreateBook(&Book{ID: "01HUSHPART0NE00000000000AA", Title: "Hush Part One", FilePath: "/w/hush-1", SeriesID: &gone})
	require.ErrorIs(t, err, ErrSeriesNotFound)

	got, gerr := s.GetBookByID("01HUSHPART0NE00000000000AA")
	require.NoError(t, gerr)
	require.Nil(t, got, "a refused create must leave no row behind")
}

func TestCreateBook_AcceptsALiveSeriesAndNoSeries(t *testing.T) {
	s := seedRefStore(t)
	ser, err := s.CreateSeries("Year One", nil)
	require.NoError(t, err)

	_, err = s.CreateBook(&Book{Title: "Year One Part One", FilePath: "/w/yo-1", SeriesID: &ser.ID})
	require.NoError(t, err)
	_, err = s.CreateBook(&Book{Title: "The Long Halloween", FilePath: "/w/lh"})
	require.NoError(t, err)
}

func TestUpdateBook_RefusesMovingABookOntoASeriesThatDoesNotExist(t *testing.T) {
	s := seedRefStore(t)
	gone := deletedSeries(t, s, "Knightfall")
	book, err := s.CreateBook(&Book{Title: "Broken Bat", FilePath: "/w/kf-1"})
	require.NoError(t, err)

	book.SeriesID = &gone
	_, err = s.UpdateBook(book.ID, book)
	require.ErrorIs(t, err, ErrSeriesNotFound)

	got, gerr := s.GetBookByID(book.ID)
	require.NoError(t, gerr)
	require.Nil(t, got.SeriesID, "a refused write leaves the stored row as it was")
}

func TestModifyBook_RefusesMovingABookOntoASeriesThatDoesNotExist(t *testing.T) {
	s := seedRefStore(t)
	gone := deletedSeries(t, s, "No Mans Land")
	book, err := s.CreateBook(&Book{Title: "Quake", FilePath: "/w/nml-1"})
	require.NoError(t, err)

	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.SeriesID = &gone; return nil })
	require.ErrorIs(t, err, ErrSeriesNotFound)

	got, gerr := s.GetBookByID(book.ID)
	require.NoError(t, gerr)
	require.Nil(t, got.SeriesID)
	n, cerr := s.GetSeriesBookRefCount(gone)
	require.NoError(t, cerr)
	require.Zero(t, n, "no book names the deleted series")
}

// A book that ALREADY names a series that is gone (production holds rows like
// that: the phantom-series repair exists for them) must stay editable. The check
// is on the link being made, not on every write of a book that has one.
func TestUpdateBook_AnExistingDanglingLinkDoesNotBlockOtherEdits(t *testing.T) {
	s := seedRefStore(t)
	gone := deletedSeries(t, s, "Contagion")
	book, err := s.CreateBook(&Book{Title: "Contagion Part One", FilePath: "/w/ct-1"})
	require.NoError(t, err)
	// Seed the dangling link the way an older build left it.
	_, err = s.SeedLegacyBookRowForTest(book.ID, func(b *Book) error { b.SeriesID = &gone; return nil })
	require.NoError(t, err)

	updated, err := s.ModifyBook(book.ID, func(b *Book) error { b.Title = "Contagion Part Two"; return nil })
	require.NoError(t, err, "the series_id did not change, so it is not checked")
	require.Equal(t, "Contagion Part Two", updated.Title)
	require.NotNil(t, updated.SeriesID)
	require.Equal(t, gone, *updated.SeriesID)

	// Clearing the dangling link is always allowed, and so is moving to a live series.
	live, err := s.CreateSeries("Cataclysm", nil)
	require.NoError(t, err)
	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.SeriesID = &live.ID; return nil })
	require.NoError(t, err)
	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.SeriesID = nil; return nil })
	require.NoError(t, err)
}

// A series held for an undo exists: linking books back into it is the whole
// point of the hold.
func TestUpdateBook_AHeldSeriesIsALegalTarget(t *testing.T) {
	s := seedRefStore(t)
	ser, err := s.CreateSeries("Zero Year", nil)
	require.NoError(t, err)
	require.NoError(t, s.HoldSeries(ser.ID, "test-undo"))
	book, err := s.CreateBook(&Book{Title: "Secret City", FilePath: "/w/zy-1"})
	require.NoError(t, err)

	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.SeriesID = &ser.ID; return nil })
	require.NoError(t, err)
}

// The window itself, through the real delete: a writer that has read the series
// is blocked behind the delete (it takes every book stripe), and runs after it.
// Before the writer verified the series it wrote series_id = N onto the deleted
// row. Pinned at both hook positions of the delete.
func TestDeleteSeriesIfUnreferenced_AStaleWriterBlockedBehindItIsRefused(t *testing.T) {
	for _, pos := range deleteHookPositions {
		t.Run(pos.name, func(t *testing.T) {
			s := seedRefStore(t)
			ser, err := s.CreateSeries("Year Two", nil)
			require.NoError(t, err)
			book, err := s.CreateBook(&Book{Title: "Year Two Part One", FilePath: "/w/y2-1"})
			require.NoError(t, err)

			var (
				mu       sync.Mutex
				writeErr error
				landed   = make(chan struct{})
			)
			setSeriesDeleteHook(t, pos.set, func(id int) {
				go func() {
					defer close(landed)
					err := repointBook(s, book.ID, id, nil)
					mu.Lock()
					writeErr = err
					mu.Unlock()
				}()
			})

			deleted, err := s.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
			require.NoError(t, err)
			require.True(t, deleted)
			select {
			case <-landed:
			case <-time.After(10 * time.Second):
				t.Fatal("the held-back write never completed after the delete returned")
			}

			mu.Lock()
			defer mu.Unlock()
			require.ErrorIs(t, writeErr, ErrSeriesNotFound, "the write that ran after the delete must be refused")
			got, gerr := s.GetBookByID(book.ID)
			require.NoError(t, gerr)
			require.Nil(t, got.SeriesID, "the book must not be left naming the deleted series")
		})
	}
}

// Writers set series_id from a list of series ids they read earlier while a
// deleter removes them. Whatever interleaving happens, no book may be left
// naming a series that does not resolve. -race covers the locks; the callback
// that creates a series covers the stripe -> series-lock order.
func TestBookWrites_NeverStrandABookOnADeletedSeriesUnderConcurrentDeletes(t *testing.T) {
	s := seedRefStore(t)
	const nSeries, nBooks = 24, 16
	ids := make([]int, nSeries)
	names := make([]string, nSeries)
	for i := range ids {
		names[i] = fmt.Sprintf("Batman Beyond %02d", i)
		ser, err := s.CreateSeries(names[i], nil)
		require.NoError(t, err)
		ids[i] = ser.ID
	}
	books := make([]string, nBooks)
	for i := range books {
		b, err := s.CreateBook(&Book{Title: fmt.Sprintf("Batman Beyond Issue %d", i), FilePath: fmt.Sprintf("/w/bb-%d", i)})
		require.NoError(t, err)
		books[i] = b.ID
	}

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 120 {
				id := ids[(w*5+i)%nSeries]
				bid := books[(w*3+i)%nBooks]
				_, err := s.ModifyBook(bid, func(b *Book) error { b.SeriesID = &id; return nil })
				if err != nil && !errors.Is(err, ErrSeriesNotFound) {
					t.Errorf("unexpected write error: %v", err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := range 6 {
			for i := round; i < nSeries; i += 6 {
				_, _ = s.DeleteSeriesIfUnreferenced(ids[i], names[i], nil)
			}
		}
	}()
	wg.Add(1)
	go func() { // a callback that creates a series takes stripe -> series lock
		defer wg.Done()
		for i := range 40 {
			bid := books[i%nBooks]
			_, err := s.ModifyBook(bid, func(b *Book) error {
				ser, cerr := s.CreateSeries(fmt.Sprintf("Batman Beyond Return %d", i%4), nil)
				if cerr != nil {
					return cerr
				}
				b.SeriesID = &ser.ID
				return nil
			})
			if err != nil && !errors.Is(err, ErrSeriesNotFound) {
				t.Errorf("unexpected create-link error: %v", err)
			}
		}
	}()
	wg.Wait()

	for _, bid := range books {
		got, err := s.GetBookByID(bid)
		require.NoError(t, err)
		if got.SeriesID == nil {
			continue
		}
		ser, err := s.GetSeriesByID(*got.SeriesID)
		require.NoError(t, err)
		require.NotNil(t, ser, "book %s names series %d, which does not exist", bid, *got.SeriesID)
	}
}

// DeleteSeriesIfUnreferenced and the book writers exclude each other with locks
// that live in one process. That is enough only while no second process can open
// the same database: Pebble takes the directory's lock file, so a second open of
// a live store's path is refused rather than sharing it. Pinned because the
// atomicity above silently stops holding if that ever changes (a shared or
// read-write second opener would need the check to move into the database).
func TestPebbleStore_ASecondOpenOfALiveStoreIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "series-lock.pebble")
	first, err := NewPebbleStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	second, err := NewPebbleStore(path)
	if err == nil {
		_ = second.Close()
	}
	require.Error(t, err, "a second opener of the same directory must be refused while the first is open")
}
