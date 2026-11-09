// file: core/storage/swaplock_flock.go
// version: 1.0.0
// guid: ef78ecfd-f631-4f1b-8683-69001adbe6db
// last-edited: 2026-11-09

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// osSwapLock takes flock(2) on path, shared or exclusive, without waiting.
// The file is opened anew on every take, so a second take in this process
// contends as one in another process would. O_RDONLY|O_CREATE needs write
// permission on the state directory only when the file is absent, and
// O_NOFOLLOW refuses a symbolic link planted in its place.
func osSwapLock(path string, exclusive bool) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storage: open the swap lock: %w", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = fmt.Errorf("%w (%s)", ErrSwapLockHeld, path)
		} else {
			err = fmt.Errorf("storage: take the swap lock %s: %w", path, err)
		}
		// The lock was not taken, so the descriptor holds nothing; a
		// failed close still goes back with the refusal (errors.Is on
		// ErrSwapLockHeld still matches the joined error).
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("storage: close the swap lock %s: %w", path, cerr))
		}
		return nil, err
	}
	return f, nil
}
