// file: core/internal/backuparchive/openfile.go
// version: 1.0.0
// guid: 4884d25f-4a37-487d-ad50-5ff6c3e8b053
// last-edited: 2026-11-12

package backuparchive

import (
	"fmt"
	"os"
)

// OpenRegular opens path for reading and refuses anything that is not a
// regular file reached without a symlink: the check is made on the path
// (Lstat), then again on the open descriptor, which must be the same file, so
// a path swapped for a link or a FIFO between the two is refused rather than
// followed. maxBytes > 0 also refuses a file larger than it.
//
// Every archive, sidecar and staged file core/backup reads from a directory a
// network share or an operator may write to is opened through it.
func OpenRegular(path string, maxBytes int64) (*os.File, os.FileInfo, error) {
	lst, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s is %s", ErrNotRegularFile, path, lst.Mode().Type())
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		return nil, nil, err
	}
	fst, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fst.Mode().IsRegular() || !os.SameFile(lst, fst) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s changed while it was opened", ErrNotRegularFile, path)
	}
	if maxBytes > 0 && fst.Size() > maxBytes {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrLimit, path, fst.Size(), maxBytes)
	}
	return f, fst, nil
}
