// file: core/internal/backuparchive/limits.go
// version: 1.0.1
// guid: 5bb5bf41-6a4b-4ad1-85be-b441f0967ac1
// last-edited: 2026-11-12

package backuparchive

import (
	"errors"
	"fmt"
)

// Limits bound every read of an archive: its size on disk, the number of
// entries, each entry's size and the sum of all of them (decompressed). A
// header's size is checked against them before anything is read or sized from
// it, so a hostile or corrupt archive costs a refusal, not unbounded CPU, memory
// or time. They do not bound disk by themselves: a compressed or sparse entry
// declares a size far above what it costs to ship, so every extraction also
// checks its declared bytes against the free space of the filesystem it writes
// to before it writes one (backup's requireSpace), and the Extractor stops at a
// byte budget. A zero field takes the package default.
type Limits struct {
	MaxArchiveBytes int64 // the file on disk
	MaxEntries      int
	MaxEntryBytes   int64
	MaxTotalBytes   int64 // the sum of the entries' sizes
}

// The defaults sit far above any real store (a Pebble checkpoint of a 40 GiB
// library is some hundreds of files of at most a few GiB; 05-backup's own
// default retention is 40 GiB of archives) and a long way below what one
// request could otherwise ask a host to decode: 4 TiB of declared content, 256
// GiB in one entry, 262144 entries, a 4 TiB file. An operator with a larger
// store sets Options.Limits; the free-space check and the Extractor's byte
// budget are what hold an extraction to the size it declared, not these.
const (
	DefaultMaxArchiveBytes = int64(4) << 40
	DefaultMaxEntries      = 1 << 18
	DefaultMaxEntryBytes   = int64(256) << 30
	DefaultMaxTotalBytes   = int64(4) << 40
)

// MaxNameBytes bounds an entry's name.
const MaxNameBytes = 1024

// MaxManifestBytes bounds the manifest entry, which is read whole into memory.
const MaxManifestBytes = 16 << 20

// withDefaults returns l with each zero field replaced.
func (l Limits) withDefaults() Limits {
	if l.MaxArchiveBytes <= 0 {
		l.MaxArchiveBytes = DefaultMaxArchiveBytes
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.MaxEntryBytes <= 0 {
		l.MaxEntryBytes = DefaultMaxEntryBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = DefaultMaxTotalBytes
	}
	return l
}

// Refusals of the reader and extractor. Each is wrapped with the entry's name
// (a name is never a secret) and compared with errors.Is.
var (
	// ErrEntryName: a name that is not a clean relative path (absolute, a `..`
	// element, an empty or doubled element, a backslash, a NUL, too long).
	ErrEntryName = errors.New("backuparchive: entry name is not a safe relative path")
	// ErrEntryType: an entry that is not a regular file (or, for a legacy
	// archive, a directory): a symlink, hardlink, device, FIFO or a sparse file
	// (GNU old-format or PAX), whose logical size costs nothing to ship and the
	// whole of it to write.
	ErrEntryType = errors.New("backuparchive: entry is not a regular file")
	// ErrLimit: the archive, an entry or the entry count passes a Limits bound.
	ErrLimit = errors.New("backuparchive: archive exceeds a limit")
	// ErrDuplicateEntry: two entries of one name.
	ErrDuplicateEntry = errors.New("backuparchive: duplicate entry")
	// ErrEmptyArchive: an archive with no entries.
	ErrEmptyArchive = errors.New("backuparchive: archive has no entries")
	// ErrNotRegularFile: the archive path is a symlink, a directory, a device
	// or a FIFO, or changed between the check and the open.
	ErrNotRegularFile = errors.New("backuparchive: not a regular file")
	// ErrEntryMismatch: an entry's size or SHA-256 differs from what the
	// manifest recorded, or an entry is not in the manifest.
	ErrEntryMismatch = errors.New("backuparchive: entry does not match the manifest")
	// ErrTrailingData: bytes after the tar stream's end that are not padding.
	ErrTrailingData = errors.New("backuparchive: data after the end of the archive")
)

// refuse wraps sentinel with the entry name and a reason.
func refuse(sentinel error, name, format string, args ...any) error {
	return fmt.Errorf("%w: %q: %s", sentinel, name, fmt.Sprintf(format, args...))
}
