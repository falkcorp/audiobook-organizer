// file: internal/database/book_file_owners.go
// version: 1.0.0
// guid: 0f379efd-6c7f-48b0-8737-96f1cfeab991
// last-edited: 2026-09-28

package database

import "errors"

// BookFilePathLookup is the read surface BookFileRowsAtPath needs: the
// complete multi-row lookup plus the single-row index it falls back to.
type BookFilePathLookup interface {
	GetBookFileByPath(filePath string) (*BookFile, error)
	BookFilesAtPath(path string) ([]BookFile, error)
}

// BookFileRowsAtPath answers "which book_file rows claim exactly this path?"
// for callers deciding whether a file already BELONGS to a book -- the
// scanner before it imports a file as a new book, and the organizer before it
// moves one.
//
// It prefers BookFilesAtPath, which returns every row at the path. When that
// cannot be complete (memdb not serving: startup warmup, tests, a
// Pebble-direct deployment) it falls back to GetBookFileByPath, whose index
// holds one row per path CRC. The fallback can under-report -- two rows at one
// path leave only the last-written findable, and a CRC collision can hide a
// path entirely -- so it is a best-effort answer, which is why it is a
// fallback and not the primary lookup. Its hit is kept only when the stored
// FilePath equals path exactly, so a CRC collision can hide a row but never
// invent one.
//
// Any other store error is returned: an ownership question that could not be
// answered must not be read as "nobody owns it".
func BookFileRowsAtPath(s BookFilePathLookup, path string) ([]BookFile, error) {
	if path == "" {
		return nil, nil
	}
	rows, err := s.BookFilesAtPath(path)
	if err == nil {
		return rows, nil
	}
	if !errors.Is(err, ErrBookFilesAtPathUnavailable) {
		return nil, err
	}
	hit, err := s.GetBookFileByPath(path)
	if err != nil {
		return nil, err
	}
	if hit == nil || hit.FilePath != path {
		return nil, nil
	}
	return []BookFile{*hit}, nil
}
