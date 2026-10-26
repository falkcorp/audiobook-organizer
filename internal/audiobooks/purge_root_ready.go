// file: internal/audiobooks/purge_root_ready.go
// version: 1.0.0
// guid: 0e7b3d52-9a14-4c68-b2f7-5d1c8a4e6b93
// last-edited: 2026-10-25

package audiobooks

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrLibraryRootNotReady is what a purge with delete_files is refused with when
// the library root does not look mounted and populated. See libraryRootReady.
var ErrLibraryRootNotReady = errors.New("the library root is not ready")

// libraryRootReady returns nil only when root looks like a library that is
// actually there: it is configured, it exists, it is a directory, and it holds at
// least one entry.
//
// Why. A purge with delete_files reads "nothing at this path" (ENOENT) as "the
// file is already gone", which is right for a file that was removed, and wrong for
// every file at once when the library is a mount that is not up: a mount point
// that is not mounted is an empty directory, so every path under it is ENOENT.
// A pass would then purge each book with no line, and a replay would clear every
// journal record, and the files, which are all still on the real disk, would have
// nothing naming them for the next library scan to import back as new books.
// The same read of a root that does not exist, or that is not configured, is
// equally "gone".
//
// Why "non-empty" and not another check. Comparing the root's device number with
// its parent's (the way a mount is usually detected) fails for a library that is a
// plain subdirectory of one filesystem, which is a supported layout. A sentinel
// file would need the owner to create it and every installed library to be
// migrated. A library that is genuinely empty has no file for a purge to remove,
// so refusing a delete_files purge until the root holds a file costs nothing
// real; a purge with "also delete files" off is not subject to it.
//
// It is checked before any decision or removal that depends on a path being
// absent: at the start of a delete_files pass, before a replay of the journal,
// before a single-book purge decides its file, and again at the start of every
// removal (removeDeferredFiles), so a mount lost mid-pass keeps its records too.
func libraryRootReady(root string) error {
	if root == "" {
		return fmt.Errorf("%w: no library root (root_dir) is configured", ErrLibraryRootNotReady)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %w", ErrLibraryRootNotReady, root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrLibraryRootNotReady, root)
	}
	d, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("%w: cannot open %s: %w", ErrLibraryRootNotReady, root, err)
	}
	defer d.Close()
	if _, err := d.Readdirnames(1); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: %s is empty (a library that is not mounted looks like this), so a missing file cannot be told from a missing mount; nothing is removed and no record is cleared until it holds a file", ErrLibraryRootNotReady, root)
		}
		return fmt.Errorf("%w: cannot list %s: %w", ErrLibraryRootNotReady, root, err)
	}
	return nil
}
