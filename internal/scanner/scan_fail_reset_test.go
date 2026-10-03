// file: internal/scanner/scan_fail_reset_test.go
// version: 1.0.0
// guid: 7091c056-0ab5-4755-bcfa-5e5f6c8c8ad9
// last-edited: 2026-10-03

package scanner

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/require"
)

// fakeScanFailResetStore implements only ResetScanFailCount. The embedded nil
// scannerStore makes any other method call panic, which resetScanFailCount's
// recover would absorb into scanFailResetPanicCount -- so every case below
// asserts the panic counter too, or a dependency the helper should not have
// would pass silently.
type fakeScanFailResetStore struct {
	scannerStore
	err  error
	keys []string
}

func (f *fakeScanFailResetStore) ResetScanFailCount(pathHash string) error {
	f.keys = append(f.keys, pathHash)
	return f.err
}

type scanFailResetCounters struct{ errs, panics int64 }

func snapshotScanFailReset() scanFailResetCounters {
	return scanFailResetCounters{scanFailResetErrCount.Load(), scanFailResetPanicCount.Load()}
}

func (before scanFailResetCounters) delta() scanFailResetCounters {
	now := snapshotScanFailReset()
	return scanFailResetCounters{now.errs - before.errs, now.panics - before.panics}
}

// TestResetScanFailCount covers the three outcomes the old bare
// `defer func() { recover() }()` plus `_ =` collapsed into one silent nothing.
func TestResetScanFailCount(t *testing.T) {
	const path = "/library/Author/Book/01.mp3"

	t.Run("success resets the key the increment path uses", func(t *testing.T) {
		fake := &fakeScanFailResetStore{}
		withStore(t, fake)
		before := snapshotScanFailReset()

		resetScanFailCount(path, logger.New("test"))

		require.Equal(t, []string{scanFailKey(path)}, fake.keys)
		require.Len(t, fake.keys[0], 16, "key is 8 bytes of SHA-256, hex")
		require.Equal(t, scanFailResetCounters{}, before.delta())
	})

	t.Run("store error is counted, not swallowed", func(t *testing.T) {
		fake := &fakeScanFailResetStore{err: errors.New("disk full")}
		withStore(t, fake)
		before := snapshotScanFailReset()

		resetScanFailCount(path, logger.New("test"))

		require.Equal(t, scanFailResetCounters{errs: 1}, before.delta(),
			"a failed reset must reach the run summary; it used to be discarded with `_ =`")
	})

	t.Run("panic is recovered and counted, not swallowed", func(t *testing.T) {
		// The bare embedded interface has no ResetScanFailCount, so the call
		// dereferences a nil interface -- the same panic a closed Pebble store
		// raises in shape: a panic out of the store call.
		withStore(t, struct{ scannerStore }{})
		before := snapshotScanFailReset()

		require.NotPanics(t, func() { resetScanFailCount(path, logger.New("test")) })

		require.Equal(t, scanFailResetCounters{panics: 1}, before.delta())
	})

	t.Run("no store is a no-op", func(t *testing.T) {
		withStore(t, nil)
		before := snapshotScanFailReset()

		resetScanFailCount(path, logger.New("test"))

		require.Equal(t, scanFailResetCounters{}, before.delta())
	})
}
