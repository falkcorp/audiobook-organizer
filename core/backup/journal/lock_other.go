// file: core/backup/journal/lock_other.go
// version: 1.2.0
// guid: 8aeea5b5-d059-4280-9e81-f5727253ea95
// last-edited: 2026-11-11

//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package journal

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// openNoFollow is 0 where O_NOFOLLOW is absent: the journal cannot be locked
// there at all (lockJournal), so no file is ever opened for writing, and the
// regular-file check on the open descriptor still refuses a link's target
// that is not a file.
const openNoFollow = 0

// openNoBlock is 0 where O_NONBLOCK is not portable: the journal cannot be
// opened for writing there at all.
const openNoBlock = 0

// lockJournal fails closed where flock(2) is absent: without an exclusive
// lock a second writer could reuse seqs, so OpenFile returns a broken File
// with this error. Its Append fails and its Gap works, so the revoking
// callers still commit their revocations with journal_gap/ records (§5B.3.6
// row "Append failure"). The release targets are linux and darwin.
func lockJournal(f *os.File) error {
	return fmt.Errorf("journal: no exclusive file lock for %s on %s: %w", f.Name(), runtime.GOOS, errors.ErrUnsupported)
}
