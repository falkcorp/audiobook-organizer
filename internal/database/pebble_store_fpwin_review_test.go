// file: internal/database/pebble_store_fpwin_review_test.go
// version: 1.0.0
// guid: 8d2c6f14-3a5e-4b71-9c08-e4f1a7b2d953
// last-edited: 2026-09-19

package database

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests added from the PR #3452 adversarial review: the dedupe carry-over must
// be a strict no-op when there is nothing to carry, the window locks must not
// serialize unrelated files, and write paths that keep a row must keep its
// windows.

// carryOver adapts the multi-donor API for these tests.
func carryOver(s *PebbleStore, to FingerprintWindowRef, from ...FingerprintWindowRef) (int, error) {
	return s.CarryOverFingerprintWindows(from, to)
}

// dropIDIndex makes a row look like one written before book_file_id existed,
// so resolving it by ID falls back to the full scan.
func dropIDIndex(t *testing.T, s *PebbleStore, fileID string) {
	t.Helper()
	require.NoError(t, s.db.Delete([]byte("book_file_id:"+fileID), nil))
	require.Nil(t, s.lookupBookFileByIDIndex(fileID), "precondition: index entry gone")
}

// Every row in production has no windows today, so this path is what every
// dedupe group takes. An id the key scheme cannot represent can hold no
// windows, so it is "nothing to carry", not an error that skips the group.
func TestFpwin_CarryOver_NoWindowsIsANoOpEvenForInvalidRefs(t *testing.T) {
	env := newBookSigEnv(t)
	_, keeper := fpwinSeedFile(t, env.store, "/lib/n/k.m4b", nil)

	n, err := carryOver(env.store, FileWindowRef(keeper), FingerprintWindowRef("f:bad;id"))
	require.NoError(t, err, "invalid donor with no windows")
	require.Zero(t, n)

	_, donor := fpwinSeedFile(t, env.store, "/lib/n/d.m4b", nil)
	n, err = carryOver(env.store, FingerprintWindowRef("f:bad;keeper"), FileWindowRef(donor))
	require.NoError(t, err, "invalid keeper, donor with no windows")
	require.Zero(t, n)

	n, err = carryOver(env.store, FileWindowRef("01NOSUCHKEEPER"), FileWindowRef(donor))
	require.NoError(t, err, "missing keeper, donor with no windows")
	require.Zero(t, n)
}

// A keeper written before the book_file_id index resolves by a full scan of
// every book_file row (362k on prod). A group with nothing to carry must not
// pay it at all.
func TestFpwin_CarryOver_NoWindowsNeverScansForTheKeeper(t *testing.T) {
	env := newBookSigEnv(t)
	_, keeper := fpwinSeedFile(t, env.store, "/lib/s/k.m4b", nil)
	_, d1 := fpwinSeedFile(t, env.store, "/lib/s/d1.m4b", nil)
	_, d2 := fpwinSeedFile(t, env.store, "/lib/s/d2.m4b", nil)
	dropIDIndex(t, env.store, keeper)

	before := env.store.bookFileIDScans.Load()
	n, err := carryOver(env.store, FileWindowRef(keeper), FileWindowRef(d1), FileWindowRef(d2))
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, before, env.store.bookFileIDScans.Load(), "a no-window carry-over scanned for the keeper")
}

// When there IS something to carry, the keeper is resolved once per group, not
// once per donor.
func TestFpwin_CarryOver_ResolvesThePreIndexKeeperOncePerGroup(t *testing.T) {
	env := newBookSigEnv(t)
	_, keeper := fpwinSeedFile(t, env.store, "/lib/o/k.m4b", nil)
	_, d1 := fpwinSeedFile(t, env.store, "/lib/o/d1.m4b", nil)
	_, d2 := fpwinSeedFile(t, env.store, "/lib/o/d2.m4b", nil)
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(FileWindowRef(d1), WindowKindWindow, 1000, 1)))
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(FileWindowRef(d2), WindowKindWindow, 9000, 2)))
	dropIDIndex(t, env.store, keeper)

	before := env.store.bookFileIDScans.Load()
	n, err := carryOver(env.store, FileWindowRef(keeper), FileWindowRef(d1), FileWindowRef(d2))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, before+1, env.store.bookFileIDScans.Load(), "keeper must be resolved exactly once")
	got, err := env.store.GetFingerprintWindows(FileWindowRef(keeper))
	require.NoError(t, err)
	require.Len(t, got, 2)
	requireNoWindowsLeft(t, env.store, d1)
	requireNoWindowsLeft(t, env.store, d2)
}

// Put racing the delete of the same row: whichever wins, a row that is gone
// never has windows left behind. Run under -race.
func TestFpwin_PutRacingDeleteNeverOrphansAWindow(t *testing.T) {
	env := newBookSigEnv(t)
	for i := range 60 {
		_, id := fpwinSeedFile(t, env.store, fmt.Sprintf("/lib/race/%03d.m4b", i), nil)
		ref := FileWindowRef(id)
		var wg sync.WaitGroup
		var putErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			putErr = env.store.PutFingerprintWindow(fpwinFixture(ref, WindowKindWindow, 5000, byte(i)))
		}()
		go func() {
			defer wg.Done()
			require.NoError(t, env.store.DeleteBookFile(id))
		}()
		wg.Wait()
		_ = putErr // either outcome is legal; the final state is what matters
		require.Nil(t, env.store.lookupBookFileByIDIndex(id))
		requireNoWindowsLeft(t, env.store, id)
	}
}

// And a Put that lands while the row lives is kept.
func TestFpwin_PutBeforeDeleteIsKeptUntilTheDelete(t *testing.T) {
	env := newBookSigEnv(t)
	_, id := fpwinSeedFile(t, env.store, "/lib/race/kept.m4b", nil)
	require.NoError(t, env.store.PutFingerprintWindow(fpwinFixture(FileWindowRef(id), WindowKindWindow, 5000, 1)))
	got, err := env.store.GetFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// The window locks are striped per ref: holding one file's stripe must not
// block the deletes of files on other stripes.
func TestFpwin_DeletesOfDifferentFilesAreNotSerialized(t *testing.T) {
	env := newBookSigEnv(t)
	_, held := fpwinSeedFile(t, env.store, "/lib/l/held.m4b", nil)
	heldStripe := stripeFor(string(FileWindowRef(held)))
	var others []string
	for i := 0; len(others) < 2; i++ {
		_, id := fpwinSeedFile(t, env.store, fmt.Sprintf("/lib/l/o%02d.m4b", i), nil)
		if stripeFor(string(FileWindowRef(id))) != heldStripe {
			others = append(others, id)
		}
	}
	seedWindowsFor(t, env.store, others[0])
	seedWindowsFor(t, env.store, others[1])

	unlock := env.store.lockWindowRefs(FileWindowRef(held))
	defer unlock()

	done := make(chan error, 2)
	go func() { done <- env.store.DeleteBookFile(others[0]) }()
	go func() { done <- env.store.DeleteBookFilesByIDs([]string{others[1]}) }()
	for range 2 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("a delete of an unrelated file blocked on another file's window stripe")
		}
	}
	requireNoWindowsLeft(t, env.store, others[0])
	requireNoWindowsLeft(t, env.store, others[1])
}

// Write paths that KEEP the row run deleteBookFileSecondaryIndexes too; they
// must leave its windows alone.
func TestFpwin_BatchUpsertKeepsWindows(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, id := fpwinSeedFile(t, env.store, "/lib/nit/up.m4b", nil)
	seedWindowsFor(t, env.store, id)
	require.NoError(t, env.store.BatchUpsertBookFiles([]*BookFile{{ID: id, BookID: bookID, FilePath: "/lib/nit/up.m4b", Format: "m4b", Duration: 99}}))
	got, err := env.store.GetFingerprintWindows(FileWindowRef(id))
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestFpwin_PIDTransferKeepsWindowsOfBothRows(t *testing.T) {
	env := newBookSigEnv(t)
	bookID, prior := fpwinSeedFile(t, env.store, "/lib/nit/prior.m4b", nil)
	f, err := env.store.GetBookFileByID(bookID, prior)
	require.NoError(t, err)
	f.ITunesPersistentID = "PID-FPWIN-1"
	require.NoError(t, env.store.UpdateBookFile(prior, f))
	seedWindowsFor(t, env.store, prior)

	taker := &BookFile{BookID: bookID, FilePath: "/lib/nit/taker.m4b", Format: "m4b", ITunesPersistentID: "PID-FPWIN-1"}
	require.NoError(t, env.store.CreateBookFile(taker))
	seedWindowsFor(t, env.store, taker.ID)

	after, err := env.store.GetBookFileByID(bookID, prior)
	require.NoError(t, err)
	require.Empty(t, after.ITunesPersistentID, "precondition: the PID transferred")
	for _, id := range []string{prior, taker.ID} {
		got, gerr := env.store.GetFingerprintWindows(FileWindowRef(id))
		require.NoError(t, gerr)
		require.Len(t, got, 3, "row %s", id)
	}
}
