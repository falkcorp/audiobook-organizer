// file: internal/database/book_files_with_hash.go
// version: 1.0.1
// guid: 3c8e1f47-5a2b-4d96-b0e7-8f14a6d2c953
// last-edited: 2026-10-01

package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// ErrBookFilesWithHashUnavailable is returned by BookFilesWithHash when it
// cannot give a complete answer. Its callers veto a write on a hit (the
// duplicate-copies fixer's box-set check), so they MUST fail closed on it.
var ErrBookFilesWithHashUnavailable = errors.New("cannot list every book_file row with a hash")

// BookFilesWithHash returns EVERY book_file row whose FileHash or
// OriginalFileHash is hash, verified against its committed Pebble row, in no
// particular order. Soft-deleted books' rows are included; the caller decides
// what a live holder is.
//
// GetBookBySegmentFileHash cannot answer this: its book_file_hash: and
// book_file_orig_hash: indexes hold ONE row reference per hash, last writer
// wins, so the second book holding a file is invisible through them.
//
// The candidates come from memdb's multi-valued FileHash and OriginalFileHash
// indexes on book_files, unioned with the two Pebble single-owner hits. Every
// candidate is re-read from Pebble and kept only if a stored hash still equals
// hash, so a stale memdb row can only be dropped, never invented. Completeness
// rests on memdb holding every row (the indexes are computed from the row
// itself, so every write that goes through the memdb write-through keeps them
// current): when memdb is not serving, or is known to have lost book_file rows
// at warmup, this returns ErrBookFilesWithHashUnavailable rather than a
// possibly short list. It is never served from a full scan.
func (p *PebbleStore) BookFilesWithHash(hash string) ([]BookFile, error) {
	if hash == "" {
		return nil, nil
	}
	m := p.mem()
	if !p.UseMemDB || m == nil {
		return nil, fmt.Errorf("%w: memdb is not serving", ErrBookFilesWithHashUnavailable)
	}
	refs, err := m.bookFileRefsWithHash(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBookFilesWithHashUnavailable, err)
	}
	for _, prefix := range []string{"book_file_hash:", "book_file_orig_hash:"} {
		v, closer, err := p.db.Get([]byte(prefix + hash))
		if err == pebble.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s%s: %w", prefix, hash, err)
		}
		ref := string(v)
		_ = closer.Close()
		if parts := strings.SplitN(ref, ":", 2); len(parts) == 2 {
			refs = append(refs, [2]string{parts[0], parts[1]})
		}
	}
	seen := make(map[string]struct{}, len(refs))
	var out []BookFile
	for _, r := range refs {
		key := r[0] + "\x00" + r[1]
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		f, err := p.getBookFileByID(r[0], r[1])
		if err != nil {
			return nil, fmt.Errorf("verify book_file %s: %w", r[1], err)
		}
		if f != nil && (f.FileHash == hash || f.OriginalFileHash == hash) {
			out = append(out, *f)
		}
	}
	return out, nil
}

// bookFileRefsWithHash returns the (bookID, fileID) of every memdb book_file
// row indexed under hash as its FileHash or its OriginalFileHash, refusing
// when the book_files table is known to be missing rows (ErrMemdbIncomplete).
func (m *MemStore) bookFileRefsWithHash(hash string) ([][2]string, error) {
	if err := m.requireTablesComplete("book_files with hash", memTableBookFiles); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	var refs [][2]string
	for _, idx := range []string{memIdxFileHash, memIdxOriginalFileHash} {
		iter, err := txn.Get(memTableBookFiles, idx, hash)
		if err != nil {
			return nil, fmt.Errorf("memdb book_files by %s: %w", idx, err)
		}
		for obj := iter.Next(); obj != nil; obj = iter.Next() {
			if bf, ok := obj.(*BookFile); ok {
				refs = append(refs, [2]string{bf.BookID, bf.ID})
			}
		}
	}
	return refs, nil
}
