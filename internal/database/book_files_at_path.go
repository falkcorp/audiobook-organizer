// file: internal/database/book_files_at_path.go
// version: 1.0.1
// guid: 69f97b8f-0d70-497f-867c-a8be4011ceb8
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
)

// ErrBookFilesAtPathUnavailable is returned by BookFilesAtPath when it cannot
// give a complete answer. Callers use that answer to decide that a path is
// referenced by nothing (before removing a file from disk, or before planning
// a repoint), so they MUST fail closed on it.
var ErrBookFilesAtPathUnavailable = errors.New("cannot list every book_file row at a path")

// BookFilesAtPath returns EVERY book_file row whose FilePath is exactly path,
// verified against its committed Pebble row, in no particular order.
//
// GetBookFileByPath cannot answer this. Its book_file_path:<crc32> index holds
// ONE row reference per CRC, last writer wins: two rows at the same path (a
// duplicate reference, or an orphan and its replacement) leave only one of
// them findable, and two paths that collide on the CRC hide each other. A
// caller asking "does anything still reference this file?" through it can be
// told "no" while a live row points at the file.
//
// The candidates come from memdb's multi-valued FilePath index on book_files,
// unioned with the Pebble path-index hit. Every candidate is re-read from
// Pebble and kept only if its stored FilePath still equals path, so a stale
// memdb row can only be dropped, never invented. Completeness therefore rests
// on memdb holding every row: when memdb is not published, or is known to
// have lost book_file rows at warmup, this returns
// ErrBookFilesAtPathUnavailable rather than a possibly short list. (It is NOT
// served from a full Pebble scan: ~750K rows per call is not a fallback, it is
// an outage.)
func (p *PebbleStore) BookFilesAtPath(path string) ([]BookFile, error) {
	if path == "" {
		return nil, nil
	}
	m := p.mem()
	if !p.UseMemDB || m == nil {
		return nil, fmt.Errorf("%w: memdb is not serving", ErrBookFilesAtPathUnavailable)
	}
	refs, err := m.bookFileRefsAtPath(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBookFilesAtPathUnavailable, err)
	}
	if hit, err := p.GetBookFileByPath(path); err != nil {
		return nil, err
	} else if hit != nil {
		refs = append(refs, [2]string{hit.BookID, hit.ID})
	}

	seen := make(map[string]struct{}, len(refs))
	var out []BookFile
	for _, r := range refs {
		key := r[0] + "\x00" + r[1]
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		// getBookFileByID answers (nil, nil) for a row that is gone.
		f, err := p.getBookFileByID(r[0], r[1])
		if err != nil {
			return nil, fmt.Errorf("verify book_file %s: %w", r[1], err)
		}
		if f != nil && f.FilePath == path {
			out = append(out, *f)
		}
	}
	return out, nil
}

// bookFileRefsAtPath returns the (bookID, fileID) of every memdb book_file row
// indexed under path, refusing when the book_files table is known to be
// missing rows (ErrMemdbIncomplete).
func (m *MemStore) bookFileRefsAtPath(path string) ([][2]string, error) {
	if err := m.requireTablesComplete("book_files at path", memTableBookFiles); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	iter, err := txn.Get(memTableBookFiles, memIdxFilePath, path)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files at path: %w", err)
	}
	var refs [][2]string
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		if bf, ok := obj.(*BookFile); ok {
			refs = append(refs, [2]string{bf.BookID, bf.ID})
		}
	}
	return refs, nil
}
