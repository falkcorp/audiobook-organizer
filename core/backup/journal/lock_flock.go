// file: core/backup/journal/lock_flock.go
// version: 1.2.0
// guid: 75a49a61-111f-44fd-a9f3-316c5c6ebe2a
// last-edited: 2026-11-11

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package journal

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow makes an open fail on a symbolic link at the last component of
// the path (O_NOFOLLOW) instead of following it.
const openNoFollow = syscall.O_NOFOLLOW

// openNoBlock makes an open of a named pipe return at once (O_NONBLOCK): a
// read-only open of a FIFO otherwise waits for a writer, before the check that
// the path is a regular file can refuse it. Reading a regular file is not
// affected.
const openNoBlock = syscall.O_NONBLOCK

// lockJournal takes flock(2) LOCK_EX on the open journal, without waiting.
// The lock belongs to this open file description, so a second OpenFile of
// the same path, in this process or another, is refused with ErrLocked, and
// closing the file releases it. (storage's swap lock is the same flock(2)
// take, but it is bound to <state_dir>/swap.lock, and pebble's vfs lock
// would be a third-party import this L4 leaf does not make.)
func lockJournal(f *os.File) error {
	var err error
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return fmt.Errorf("%w (%s)", ErrLocked, f.Name())
	default:
		return fmt.Errorf("journal: lock %s: %w", f.Name(), err)
	}
}
