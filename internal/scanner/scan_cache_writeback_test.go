// file: internal/scanner/scan_cache_writeback_test.go
// version: 1.0.0
// guid: d23028ef-779a-4609-8fd4-f113ebedf97d
// last-edited: 2026-08-24

package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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

type scanCacheCounters struct {
	statErr, lookupErr, noRow, updateErr int64
}

func snapshotScanCacheCounters() scanCacheCounters {
	return scanCacheCounters{
		statErr:   scanCacheStatErrCount.Load(),
		lookupErr: scanCacheLookupErrCount.Load(),
		noRow:     scanCacheNoRowCount.Load(),
		updateErr: scanCacheUpdateErrCount.Load(),
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
	}
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
	writeBackScanCache(path, logger.New("test"))
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
	writeBackScanCache(path, logger.New("test"))
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
	writeBackScanCache(path, logger.New("test"))
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
	writeBackScanCache(missing, logger.New("test"))
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
	writeBackScanCache(path, logger.New("test"))
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
	assert.NotPanics(t, func() { writeBackScanCache(path, logger.New("test")) })
	assert.Equal(t, scanCacheCounters{}, before.delta(),
		"an unwired store is a configuration state, not a per-file failure")
}
