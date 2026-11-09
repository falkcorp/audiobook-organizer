// file: core/storage/swaplock.go
// version: 1.0.0
// guid: cf4363ba-fe79-48e3-9f10-fe0f3f01ffd7
// last-edited: 2026-11-09

package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// SwapLockName is the swap lock's file, directly under the state directory
// (core.paths.state_dir): outside restore/, so the slot's probe never reads
// it, and never moved or deleted by a swap.
const SwapLockName = "swap.lock"

// SwapLockPath is <stateDir>/swap.lock, the swap lock's one path.
func SwapLockPath(stateDir string) string { return filepath.Join(stateDir, SwapLockName) }

// ErrSwapLockHeld: the swap lock is held in the other mode. Under a shared
// take it means an offline restore or reset is running; under an exclusive
// take, that a service (or another opener of the live store) is running or
// starting. Compared with errors.Is.
var ErrSwapLockHeld = errors.New("storage: the swap lock is held")

// The swap lock is the exclusion between every opener of the live store and
// the offline swaps that move it (R1-25 review B1). The pebble LOCK cannot
// be it: an offline swap opens the placed store at the same path for its
// sidecar rewrite, and a serve boot runs P0a, which renames the store,
// before any open takes that LOCK. So:
//
//   - every opener holds it shared for as long as it may use the store: a
//     serve boot from the start of P0a until the kernel closes, an offline
//     open until Close, ABO's standalone open until the store closes;
//   - the offline restore (and its escape) and the offline reset hold it
//     exclusively from before their stopped-service check until they
//     return; R1-27's rollback-restore takes it the same way.
//
// Both takes are non-blocking: a boot that waited on an offline restore
// would be a hang nothing diagnoses, and the service manager's restart
// covers the window. The lock is flock(2) on the platforms that have it, so
// flock(1) in a script contends with it, and it conflicts within one
// process as well as across processes (each take opens the file anew).
// Elsewhere both modes are the exclusive vfs.Default.Lock: fail-closed, at
// the price of two shared holders refusing each other. On any filesystem
// other than the real one no lock is taken, as with the checkpoint sweep
// lock: its files are one process's memory, which no other process
// reaches, and the crash-matrix kit runs its boots one at a time.

// LockSwapShared takes the swap lock shared, without waiting. It fails
// with ErrSwapLockHeld while an offline swap holds it exclusively. Close
// releases it.
func LockSwapShared(fs vfs.FS, stateDir string) (io.Closer, error) {
	return lockSwap(fs, stateDir, false)
}

// LockSwapExclusive takes the swap lock exclusively, without waiting. It
// fails with ErrSwapLockHeld while any opener holds it. Close releases it.
func LockSwapExclusive(fs vfs.FS, stateDir string) (io.Closer, error) {
	return lockSwap(fs, stateDir, true)
}

func lockSwap(fs vfs.FS, stateDir string, exclusive bool) (io.Closer, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("storage: the swap lock needs an absolute state directory, not %q", stateDir)
	}
	if vfs.Root(fs) != vfs.Default {
		return noLock{}, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("storage: the swap lock's state directory: %w", err)
	}
	return osSwapLock(SwapLockPath(stateDir), exclusive)
}

// noLock is the swap lock off the real filesystem.
type noLock struct{}

func (noLock) Close() error { return nil }
