// file: internal/database/book_file_path_key.go
// version: 1.1.0
// guid: 8a41d0c6-2f7e-4b39-95d8-c6e1f3a07b24
// last-edited: 2026-10-01

package database

import (
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// ClaimBookFilePathKey points the single-owner book_file_path key of row
// fileID's current path at that row, and writes nothing else: not the row,
// not its other indexes, not memdb, and no aggregate recompute or change
// notification follows. It is how an undo hands the key back to the row that
// held it before a repoint took it, when that row belongs to a book no write
// may touch (an iTunes copy): a no-op row write would still notify and
// recompute the book.
//
// The row is re-read and the key written while holding the row's book_file
// stripe AND its book's owner stripe; claimed is false (nil error) when the
// row is gone or has moved, so a caller never points the key at a row
// elsewhere.
//
// The owner stripe is what serializes this against MoveBookFilesToBook(Bulk):
// a move takes no book_file stripes, but its commit re-checks every source
// row and applies its batch under the source's and the target's owner
// stripes (commitBookFileBatch). With the file stripe alone a move could
// commit between this read and this write, and the key would be pointed at
// bookID:fileID after the row had left bookID. Owner stripes are the
// innermost lock (book_delete_owns_files.go LOCK ORDER), so taking one under
// the file stripe is in order.
func (p *PebbleStore) ClaimBookFilePathKey(bookID, fileID, path string) (claimed bool, err error) {
	if path == "" {
		return false, nil
	}
	unlock := p.lockBookFile(fileID)
	defer unlock()
	unlockOwner := p.lockBookOwners(bookID)
	defer unlockOwner()
	f, err := p.getBookFileByID(bookID, fileID)
	if err != nil {
		return false, fmt.Errorf("read book_file %s: %w", fileID, err)
	}
	if f == nil || f.FilePath != path {
		return false, nil
	}
	if claimPathKeyBeforeSetHook != nil {
		claimPathKeyBeforeSetHook()
	}
	key := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(path)))
	if err := p.db.Set(key, []byte(bookID+":"+fileID), pebble.Sync); err != nil {
		return false, fmt.Errorf("claim the path key for %s: %w", fileID, err)
	}
	return true, nil
}

// claimPathKeyBeforeSetHook, when non-nil, runs in ClaimBookFilePathKey after
// the row was re-read at path and before the key is written. Test-only.
var claimPathKeyBeforeSetHook func()
