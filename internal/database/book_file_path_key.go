// file: internal/database/book_file_path_key.go
// version: 1.5.0
// guid: 8a41d0c6-2f7e-4b39-95d8-c6e1f3a07b24
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
	"strings"

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
// elsewhere. It is also false for a RETIRED row: the single-owner index rule
// (retired_book_files.go) lets a retired row keep a path key only while no live
// row carries the path, and decides that in RetireBookFiles and
// UnretireBookFiles. An undo handing a key back must never move it from a live
// row onto a retired one, so a retired row is refused here, not claimed.
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
	return p.claimPathKey(bookID, fileID, path, false)
}

// ClaimRetiredBookFilePathKey is ClaimBookFilePathKey for a RETIRED row: the
// undo of a repoint that took a path key from a retired row hands it back to
// that row, because a retired row keeps a path key while no live row carries
// the path (the single-owner index rule, retired_book_files.go) and the
// single-row "is this path claimed?" read (GetBookFileByPathIncludingRetired)
// needs it there. The same row checks apply with the opposite flag (the row
// must be retired and still at path), plus one: the key is written only while
// it is free or names a row that is gone, has left the path, or is itself
// retired. A key held by a live row at path stays with it, so an undo can never
// move a key from a live row onto a retired one. claimed is false (nil error)
// when any check fails.
func (p *PebbleStore) ClaimRetiredBookFilePathKey(bookID, fileID, path string) (claimed bool, err error) {
	return p.claimPathKey(bookID, fileID, path, true)
}

// claimPathKey is the shared body of the two public claims: retired says which
// kind of row may be claimed for.
func (p *PebbleStore) claimPathKey(bookID, fileID, path string, retired bool) (claimed bool, err error) {
	if path == "" {
		return false, nil
	}
	unlock := p.lockBookFile(fileID)
	defer unlock()
	unlockOwner := p.lockBookOwners(bookID)
	f, err := p.getBookFileByID(bookID, fileID)
	if err != nil {
		unlockOwner()
		return false, fmt.Errorf("read book_file %s: %w", fileID, err)
	}
	if f == nil || f.FilePath != path || f.Retired != retired {
		unlockOwner()
		return false, nil
	}
	key := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(path)))
	if claimPathKeyBeforeSetHook != nil {
		claimPathKeyBeforeSetHook()
	}
	// NoSync under the owner stripe, fsync after releasing it: no fsync runs
	// under an owner stripe (book_delete_owns_files.go, WHAT RUNS UNDER THEM).
	// The index mutex covers the live-holder check of a retired claim as well as
	// the Set: a live writer of this key holds it across its commit, so the key
	// cannot pass to a live row between the check and the write.
	unlockIndex := p.lockSingleOwnerIndex()
	if retired {
		held, err := p.pathKeyHeldByLiveRowAt(key, path)
		if err != nil {
			unlockIndex()
			unlockOwner()
			return false, err
		}
		if held {
			unlockIndex()
			unlockOwner()
			return false, nil
		}
	}
	err = p.db.Set(key, []byte(bookID+":"+fileID), pebble.NoSync)
	unlockIndex()
	unlockOwner()
	if err != nil {
		return false, fmt.Errorf("claim the path key for %s: %w", fileID, err)
	}
	// The key is applied (visible) even if the fsync fails, so claimed is
	// true alongside an ErrBookFileDurabilityUnknown error.
	if err := p.syncBookFileWAL(); err != nil {
		return true, fmt.Errorf("claim the path key for %s: %w", fileID, err)
	}
	return true, nil
}

// pathKeyHeldByLiveRowAt reports whether key names a non-retired row whose
// path is path.
func (p *PebbleStore) pathKeyHeldByLiveRowAt(key []byte, path string) (bool, error) {
	v, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the path key of %s: %w", path, err)
	}
	ref := string(v)
	_ = closer.Close()
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return false, nil
	}
	o, err := p.getBookFileByID(parts[0], parts[1])
	if err != nil {
		return false, fmt.Errorf("read the path key's owner %s: %w", parts[1], err)
	}
	return o != nil && !o.Retired && o.FilePath == path, nil
}

// claimPathKeyBeforeSetHook, when non-nil, runs in ClaimBookFilePathKey after
// the row was re-read at path and before the key is written. Test-only.
var claimPathKeyBeforeSetHook func()
