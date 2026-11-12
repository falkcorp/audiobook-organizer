// file: core/internal/libfs/libfs.go
// version: 1.1.0
// guid: 90ca9fee-7d93-4cef-b3cd-8e044edd56b2
// last-edited: 2026-11-11

// Package libfs is the byte-level filesystem layer under core's library
// package (05-library 5L.3, "Implementation of the local driver"). It holds
// what os.Root cannot express: a rename that never replaces an existing name,
// a hard link and a copy-on-write clone between two directories, a directory
// fsync, and the streaming copy loop. It re-implements the rules of ABO's
// internal/fileops (copy.go, reflink*.go, hash.go) and internal/versions
// (rename_noreplace*.go) for core, which cannot import them.
//
// Confinement is the caller's job and is preserved by construction: every
// operation here takes a Dir, a directory opened THROUGH an *os.Root, and one
// single-component name inside it. A name never carries a separator, so the
// kernel never walks a path component that could be a symlink swapped in after
// the check, and the only way to name a directory is to have opened it through
// the root.
//
// Nothing here keeps package-level mutable state: fault injection for tests
// lives in library, which wraps the operations it calls.
package libfs

import (
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"strings"
)

// LibraryFileMode is the mode an ingested library file is created with, before
// the process umask applies: the 0664 fileops.LibraryFileMode has always been.
// Group-writable on purpose, the library is served over a share.
const LibraryFileMode fs.FileMode = 0o664

// ChunkSize is the read size of Copy and the unit between two context checks
// (05-library 5L.6: long operations stop at a 1 MiB boundary).
const ChunkSize = 1 << 20

// ErrReflinkUnsupported reports that the filesystem cannot clone extents, or
// that source and destination are on different filesystems, or that the
// platform has no clone primitive. It is the cue to fall back, never a
// statement about the destination.
var ErrReflinkUnsupported = errors.New("library: reflink not supported on this filesystem")

// ErrCrossDevice reports a hard link or a rename that would cross filesystems.
var ErrCrossDevice = errors.New("library: hardlink across filesystems")

// ErrBadName reports a name that is not one path component.
var ErrBadName = errors.New("libfs: name is not a single path component")

// ID is a file's device and inode: the pair names one directory whatever the
// names that lead to it (a symlink, a bind mount, a second path).
type ID struct{ Dev, Ino uint64 }

// MaxAncestry bounds the walk of Ancestry: no directory tree is this deep.
const MaxAncestry = 4096

// ErrNotUnder reports an Ancestry walk that reached the top of the filesystem
// without meeting the directory it was to stop at: the descriptor is not below
// it (the directory was moved out of the tree, or the tree was replaced).
var ErrNotUnder = errors.New("libfs: the directory is not under the root")

// checkName refuses anything but one ordinary path component. The *at calls
// would follow ".." and treat a name with a separator as a path, which would
// walk outside the directory the caller proved.
func checkName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%w: %q", ErrBadName, name)
	case strings.ContainsAny(name, "/\x00"):
		return fmt.Errorf("%w: %q", ErrBadName, name)
	}
	return nil
}

// CopyOptions tunes Copy.
type CopyOptions struct {
	// Hash, when set, receives every byte read.
	Hash hash.Hash
	// Check runs before each chunk; a non-nil error stops the copy and is
	// returned as is. Pass ctx.Err for cancellation.
	Check func() error
	// Throttle runs after each chunk with its length, for a byte-rate limit; a
	// non-nil error stops the copy.
	Throttle func(n int) error
}

// Copy streams src to dst in ChunkSize reads and returns the bytes moved. It
// hashes what it READS, so the digest describes the source bytes that were
// offered to dst, not a second read of a file that may have changed.
func Copy(dst io.Writer, src io.Reader, o CopyOptions) (int64, error) {
	buf := make([]byte, ChunkSize)
	var total int64
	for {
		if o.Check != nil {
			if err := o.Check(); err != nil {
				return total, err
			}
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if o.Hash != nil {
				o.Hash.Write(buf[:n]) // hash.Hash.Write never fails
			}
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total, werr
			}
			if w != n {
				return total, io.ErrShortWrite
			}
			if o.Throttle != nil {
				if err := o.Throttle(n); err != nil {
					return total, err
				}
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}
