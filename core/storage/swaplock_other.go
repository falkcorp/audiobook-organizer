// file: core/storage/swaplock_other.go
// version: 1.0.0
// guid: c5f9d64a-f6c3-4e7c-bbda-57e2346da787
// last-edited: 2026-11-09

//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package storage

import (
	"fmt"
	"io"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// osSwapLock has no flock(2) here: both modes take vfs.Default's exclusive
// lock, which also excludes a second holder in this process. Fail-closed:
// two openers refuse each other, and an offline swap still refuses every
// opener.
func osSwapLock(stateDir string, _ bool, _ int) (ExclusiveSwapLock, error) {
	path := SwapLockPath(stateDir)
	l, err := vfs.Default.Lock(path)
	if err != nil {
		return nil, fmt.Errorf("%w (%s: %v)", ErrSwapLockHeld, path, err)
	}
	return vfsSwapLock{l}, nil
}

// vfsSwapLock is the one exclusive lock both modes take here.
type vfsSwapLock struct{ io.Closer }

// Downgrade keeps the lock exclusive: there is no shared mode to turn to,
// and staying exclusive refuses more, not less.
func (vfsSwapLock) Downgrade() error { return nil }
