// file: internal/scanner/scan_cache_writeback_test.go
// version: 1.3.0
// guid: d23028ef-779a-4609-8fd4-f113ebedf97d
// last-edited: 2026-08-24

package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScanCacheStore implements only the two methods writeBackScanCache uses.
// The embedded nil scannerStore makes any OTHER method call panic rather than
// silently returning a zero value, so the test fails loudly if the helper grows
// a dependency it should not have.
type fakeScanCacheStore struct {
	scannerStore

	getBook     func(string) (*database.Book, error)
	update      func(id string, mtime, size int64) error
	updateCalls int

	markRescan    func(id string) error
	markRescanIDs []string
}

func (f *fakeScanCacheStore) GetBookByFilePath(p string) (*database.Book, error) {
	return f.getBook(p)
}

func (f *fakeScanCacheStore) UpdateScanCache(id string, mtime, size int64) error {
	f.updateCalls++
	if f.update == nil {
		return nil
	}
	return f.update(id, mtime, size)
}

// MarkNeedsRescan must be implemented on the fake rather than left to the
// embedded nil scannerStore. writeBackScanCache runs under a recover() guard,
// so a missing method here would not fail a test -- it would be swallowed into
// scanCachePanicCount and the re-arm would silently never happen. That is why
// scanCacheCounters below counts panics.
func (f *fakeScanCacheStore) MarkNeedsRescan(id string) error {
	f.markRescanIDs = append(f.markRescanIDs, id)
	if f.markRescan == nil {
		return nil
	}
	return f.markRescan(id)
}

type scanCacheCounters struct {
	statErr, lookupErr, noRow, updateErr int64
	// panics is counted so that a method missing from fakeScanCacheStore
	// surfaces as a test failure instead of being absorbed by the recover()
	// guard in writeBackScanCache.
	panics          int64
	rearm, rearmErr int64
}

func snapshotScanCacheCounters() scanCacheCounters {
	return scanCacheCounters{
		statErr:   scanCacheStatErrCount.Load(),
		lookupErr: scanCacheLookupErrCount.Load(),
		noRow:     scanCacheNoRowCount.Load(),
		updateErr: scanCacheUpdateErrCount.Load(),
		panics:    scanCachePanicCount.Load(),
		rearm:     scanCacheRearmCount.Load(),
		rearmErr:  scanCacheRearmErrCount.Load(),
	}
}

// delta returns how much each counter moved since the snapshot.
func (before scanCacheCounters) delta() scanCacheCounters {
	now := snapshotScanCacheCounters()
	return scanCacheCounters{
		statErr:   now.statErr - before.statErr,
		lookupErr: now.lookupErr - before.lookupErr,
		noRow:     now.noRow - before.noRow,
		updateErr: now.updateErr - before.updateErr,
		panics:    now.panics - before.panics,
		rearm:     now.rearm - before.rearm,
		rearmErr:  now.rearmErr - before.rearmErr,
	}
}

// withRescanAge pins the rescan-age gate for one test. The existing write-back
// tests are about the error taxonomy and say nothing about freshness, so they
// pin it OFF rather than depending on config.AppConfig happening to be zero.
func withRescanAge(t *testing.T, hours int) {
	t.Helper()
	prev := config.AppConfig.MinRescanAgeHours
	config.AppConfig.MinRescanAgeHours = hours
	t.Cleanup(func() { config.AppConfig.MinRescanAgeHours = prev })
}

func withStore(t *testing.T, s scannerStore) {
	t.Helper()
	prev := getStore()
	SetStore(s)
	t.Cleanup(func() { SetStore(prev) })
}

// writableFile creates a real file so os.Stat succeeds and returns its path.
func writableFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.m4b")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o600))
	return path
}

// TestWriteBackScanCache_HappyPath pins that a real file with a real row gets
// its cache stamped with that file's actual mtime and size.
func TestWriteBackScanCache_HappyPath(t *testing.T) {
	withRescanAge(t, -1)
	path := writableFile(t)
	fi, err := os.Stat(path)
	require.NoError(t, err)

	var gotID string
	var gotMtime, gotSize int64
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		update: func(id string, mtime, size int64) error {
			gotID, gotMtime, gotSize = id, mtime, size
			return nil
		},
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, 1, store.updateCalls, "the cache write must actually happen")
	assert.Equal(t, "book-1", gotID)
	assert.Equal(t, fi.ModTime().Unix(), gotMtime)
	assert.Equal(t, fi.Size(), gotSize)
	assert.Equal(t, scanCacheCounters{}, d, "a successful write-back must not move any failure counter")
}

// TestWriteBackScanCache_NoRowIsCounted is the instrument for the structural
// case: saveBookToDatabase returns early without creating a row for a file that
// duplicates an already-version-linked book, so GetBookByFilePath finds nothing
// and the path can never acquire a cache entry. Before this counter existed the
// condition was indistinguishable from a successful write.
func TestWriteBackScanCache_NoRowIsCounted(t *testing.T) {
	path := writableFile(t)
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) { return nil, nil },
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.noRow, "a missing book row must be counted")
	assert.Equal(t, int64(0), d.lookupErr, "a missing row is not a lookup error")
	assert.Equal(t, 0, store.updateCalls, "there is no book id to write against")
}

// TestWriteBackScanCache_LookupErrorIsCounted covers the error that used to be
// discarded entirely: `dbErr == nil && dbBook != nil` swallowed a store failure
// and a missing row into the same do-nothing branch.
func TestWriteBackScanCache_LookupErrorIsCounted(t *testing.T) {
	path := writableFile(t)
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) { return nil, errors.New("pebble: boom") },
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.lookupErr, "a store error must be counted as a store error")
	assert.Equal(t, int64(0), d.noRow, "a store error must NOT be miscounted as a missing row")
	assert.Equal(t, 0, store.updateCalls)
}

// TestWriteBackScanCache_StatErrorIsCounted covers a file that vanished between
// being scanned and being stamped.
func TestWriteBackScanCache_StatErrorIsCounted(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.m4b")
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			t.Fatal("lookup must not be attempted when the file cannot be stat'd")
			return nil, nil
		},
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(missing, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.statErr)
	assert.Equal(t, int64(0), d.lookupErr)
	assert.Equal(t, int64(0), d.noRow)
}

// TestWriteBackScanCache_UpdateErrorIsCounted keeps the pre-existing H5 counter
// wired through the extracted helper.
func TestWriteBackScanCache_UpdateErrorIsCounted(t *testing.T) {
	path := writableFile(t)
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		update: func(string, int64, int64) error { return errors.New("write failed") },
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.updateErr)
	assert.Equal(t, int64(0), d.noRow)
	assert.Equal(t, int64(0), d.lookupErr)
}

// TestWriteBackScanCache_NilStoreIsSafe pins that the helper is inert rather
// than panicking when the scanner has no store wired.
func TestWriteBackScanCache_NilStoreIsSafe(t *testing.T) {
	path := writableFile(t)
	withStore(t, nil)

	before := snapshotScanCacheCounters()
	assert.NotPanics(t, func() { writeBackScanCache(path, nil, logger.New("test")) })
	assert.Equal(t, scanCacheCounters{}, before.delta(),
		"an unwired store is a configuration state, not a per-file failure")
}

// TestWriteBackScanCache_UsesCallerFileInfo pins the self-healing property the
// suspicious-file path depends on.
//
// That path stats a file, finds it under the minimum-size threshold, marks it
// LibraryState="suspicious", then hashes the whole file — a wide window in
// which a still-downloading file grows past the threshold. If the write-back
// re-stats, the cache is stamped with the POST-growth mtime/size, and the next
// scan's classifySkipFile (which compares only NeedsRescan/Mtime/Size, never
// LibraryState) skips the file, leaving it flagged suspicious forever.
//
// Stamping the size the decision was actually made on leaves a mismatch, so the
// next scan re-reads and the flag clears itself.
func TestWriteBackScanCache_UsesCallerFileInfo(t *testing.T) {
	path := writableFile(t)
	before, err := os.Stat(path)
	require.NoError(t, err)

	// The file grows after the caller took its FileInfo, as a completing
	// download would.
	require.NoError(t, os.WriteFile(path, []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"), 0o600))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.NotEqual(t, before.Size(), after.Size(), "precondition: the file must actually have grown")

	var gotSize int64
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		update: func(_ string, _ int64, size int64) error {
			gotSize = size
			return nil
		},
	}
	withStore(t, store)

	writeBackScanCache(path, before, logger.New("test"))

	assert.Equal(t, before.Size(), gotSize,
		"the caller's FileInfo must win: stamping the post-growth size makes the next scan skip the file "+
			"and it stays flagged suspicious permanently")
	assert.NotEqual(t, after.Size(), gotSize)
}

// TestWriteBackScanCache_NilFileInfoStats pins the other half: the main path
// passes nil and the helper stats for itself.
func TestWriteBackScanCache_NilFileInfoStats(t *testing.T) {
	path := writableFile(t)
	fi, err := os.Stat(path)
	require.NoError(t, err)

	var gotSize int64
	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		update: func(_ string, _ int64, size int64) error {
			gotSize = size
			return nil
		},
	}
	withStore(t, store)

	writeBackScanCache(path, nil, logger.New("test"))
	assert.Equal(t, fi.Size(), gotSize)
}

// ── rescan-age re-arm ──────────────────────────────────────────────────────

// UpdateScanCache CLEARS NeedsRescan. A file that is still inside the
// rescan-age window has to have the flag put back, or the gate defers it for a
// full period on whatever half-written metadata this pass recorded.
//
// This is the regression the gate would otherwise introduce, and it lands on
// exactly the population that already hurts: a file discovered mid-write is a
// cache MISS, so it is read immediately and a row is created from a partial
// file. Before the gate that self-healed on the next scan.
func TestWriteBackScanCache_RearmsStillFreshFile(t *testing.T) {
	withRescanAge(t, 144)
	path := writableFile(t) // just created, so its mtime is inside the window

	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, []string{"book-1"}, store.markRescanIDs,
		"a file still inside the rescan-age window must be re-armed, or the gate defers it on partial metadata")
	assert.Equal(t, int64(1), d.rearm)
	assert.Equal(t, int64(0), d.rearmErr)
	assert.Equal(t, int64(0), d.panics, "the fake must implement every method the helper calls")
	assert.Equal(t, 1, store.updateCalls, "the cache is still stamped; the re-arm is in addition, not instead")
}

// The complement: a settled file must NOT be re-armed. Re-arming everything
// would make NeedsRescan universally true and disable the gate entirely, which
// would look like it was working while doing nothing.
func TestWriteBackScanCache_DoesNotRearmSettledFile(t *testing.T) {
	withRescanAge(t, 144)
	path := writableFile(t)

	// Backdate the file well outside the 144h window.
	old := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))

	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Empty(t, store.markRescanIDs, "a settled file must not be re-armed")
	assert.Equal(t, int64(0), d.rearm)
	assert.Equal(t, int64(0), d.panics)
}

// With the gate disabled there is nothing to protect against, so the re-arm
// must not fire either — otherwise disabling the gate would still leave every
// fresh file flagged for rescan.
func TestWriteBackScanCache_NoRearmWhenGateDisabled(t *testing.T) {
	withRescanAge(t, -1)
	path := writableFile(t)

	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))

	assert.Empty(t, store.markRescanIDs, "a disabled gate needs no re-arm")
	assert.Equal(t, scanCacheCounters{}, before.delta())
}

// A failed re-arm must be counted, not swallowed. Losing it silently leaves a
// still-changing file gated for a full period on partial metadata — the precise
// failure the re-arm exists to prevent.
func TestWriteBackScanCache_RearmErrorIsCounted(t *testing.T) {
	withRescanAge(t, 144)
	path := writableFile(t)

	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		markRescan: func(string) error { return errors.New("pebble: boom") },
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.rearmErr)
	assert.Equal(t, int64(0), d.rearm, "a failed re-arm must not be counted as a successful one")
	assert.Equal(t, int64(0), d.updateErr, "the cache write itself succeeded")
	assert.Equal(t, int64(0), d.panics)
}

// A failed cache write must NOT be followed by a re-arm: without a stamped
// entry the file is re-read next scan anyway, and marking it dirty on top would
// double-count the same file as a forced rescan.
func TestWriteBackScanCache_NoRearmAfterFailedUpdate(t *testing.T) {
	withRescanAge(t, 144)
	path := writableFile(t)

	store := &fakeScanCacheStore{
		getBook: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
		update:     func(string, int64, int64) error { return errors.New("write failed") },
		markRescan: func(string) error { t.Fatal("re-arm must not run after a failed cache write"); return nil },
	}
	withStore(t, store)

	before := snapshotScanCacheCounters()
	writeBackScanCache(path, nil, logger.New("test"))
	d := before.delta()

	assert.Equal(t, int64(1), d.updateErr)
	assert.Equal(t, int64(0), d.rearm)
	assert.Empty(t, store.markRescanIDs)
}
