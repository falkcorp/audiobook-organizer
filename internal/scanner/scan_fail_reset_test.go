// file: internal/scanner/scan_fail_reset_test.go
// version: 1.1.0
// guid: 7091c056-0ab5-4755-bcfa-5e5f6c8c8ad9
// last-edited: 2026-10-03

package scanner

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/quarantine"
	"github.com/stretchr/testify/require"
)

// fakeScanFailStore implements only the two scan-fail counter writes. The
// embedded nil scannerStore makes any other method call panic, which
// callScanFailStore's recover would absorb into a panic counter -- so every
// case below asserts the panic counters too, or a dependency the helpers
// should not have would pass silently.
type fakeScanFailStore struct {
	scannerStore
	incrErr, resetErr   error
	incrKeys, resetKeys []string
}

func (f *fakeScanFailStore) IncrScanFailCount(pathHash string) (int, error) {
	f.incrKeys = append(f.incrKeys, pathHash)
	return len(f.incrKeys), f.incrErr
}

func (f *fakeScanFailStore) ResetScanFailCount(pathHash string) error {
	f.resetKeys = append(f.resetKeys, pathHash)
	return f.resetErr
}

type scanFailCounters struct{ incrErrs, incrPanics, resetErrs, resetPanics int64 }

func snapshotScanFail() scanFailCounters {
	return scanFailCounters{
		scanFailCountErrCount.Load(), scanFailIncrPanicCount.Load(),
		scanFailResetErrCount.Load(), scanFailResetPanicCount.Load(),
	}
}

func (before scanFailCounters) delta() scanFailCounters {
	now := snapshotScanFail()
	return scanFailCounters{
		now.incrErrs - before.incrErrs, now.incrPanics - before.incrPanics,
		now.resetErrs - before.resetErrs, now.resetPanics - before.resetPanics,
	}
}

const scanFailTestPath = "/library/Author/Book/01.mp3"

// TestScanFailCounterWrites covers the outcomes of both counter writes. The
// reset used to sit under a bare `defer func() { recover() }()` with `_ =` on
// its error, and the increment had no recover at all, so a panic out of either
// was silent or fatal.
func TestScanFailCounterWrites(t *testing.T) {
	log := logger.New("test")

	t.Run("success writes the shared key", func(t *testing.T) {
		fake := &fakeScanFailStore{}
		withStore(t, fake)
		before := snapshotScanFail()

		incrScanFailCount(scanFailTestPath, log)
		resetScanFailCount(scanFailTestPath, log)

		key := database.ScanFailKey(scanFailTestPath)
		require.Equal(t, []string{key}, fake.incrKeys)
		require.Equal(t, []string{key}, fake.resetKeys)
		require.Equal(t, scanFailCounters{}, before.delta())
	})

	t.Run("store errors are counted per op, not swallowed", func(t *testing.T) {
		withStore(t, &fakeScanFailStore{incrErr: errors.New("disk full"), resetErr: errors.New("disk full")})
		before := snapshotScanFail()

		incrScanFailCount(scanFailTestPath, log)
		resetScanFailCount(scanFailTestPath, log)

		require.Equal(t, scanFailCounters{incrErrs: 1, resetErrs: 1}, before.delta())
	})

	t.Run("panics are recovered and counted per op, not swallowed or fatal", func(t *testing.T) {
		// The bare embedded interface implements neither method, so each call
		// dereferences a nil interface: a panic out of the store call, the
		// same shape as a closed Pebble store's.
		withStore(t, struct{ scannerStore }{})
		before := snapshotScanFail()

		require.NotPanics(t, func() { incrScanFailCount(scanFailTestPath, log) })
		require.NotPanics(t, func() { resetScanFailCount(scanFailTestPath, log) })

		require.Equal(t, scanFailCounters{incrPanics: 1, resetPanics: 1}, before.delta())
	})

	t.Run("no store is a no-op", func(t *testing.T) {
		withStore(t, nil)
		before := snapshotScanFail()

		incrScanFailCount(scanFailTestPath, log)
		resetScanFailCount(scanFailTestPath, log)

		require.Equal(t, scanFailCounters{}, before.delta())
	})
}

// quarantineKeyStore is the reader side: the quarantine service lists books and
// reads each one's scan-fail counter. It records the keys it is asked for and
// reports every counter below the threshold, so nothing is quarantined.
type quarantineKeyStore struct {
	quarantine.Store
	books    []database.BookCore
	readKeys []string
}

func (q *quarantineKeyStore) GetAllBooksCore(_, _ int) ([]database.BookCore, error) {
	return q.books, nil
}

func (q *quarantineKeyStore) GetScanFailCount(pathHash string) (int, error) {
	q.readKeys = append(q.readKeys, pathHash)
	return 0, nil
}

// TestScanFailKeyIsSharedWithQuarantine pins the writer and the reader to one
// key for one file. If the scanner and the quarantine service ever derive the
// key differently again, increments land where the threshold check never
// looks and auto-quarantine silently stops working -- with every unit test on
// either side still green, which is why this test drives both.
func TestScanFailKeyIsSharedWithQuarantine(t *testing.T) {
	writer := &fakeScanFailStore{}
	withStore(t, writer)
	incrScanFailCount(scanFailTestPath, logger.New("test"))

	reader := &quarantineKeyStore{books: []database.BookCore{{ID: "b1", FilePath: scanFailTestPath}}}
	quarantine.NewQuarantineService(reader, &config.Config{}, nil).AutoQuarantineFailedScans()

	require.Len(t, writer.incrKeys, 1)
	require.Equal(t, writer.incrKeys, reader.readKeys,
		"scanner increments a counter the quarantine service never reads")
}
