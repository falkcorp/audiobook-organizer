// file: internal/database/book_file_owners.go
// version: 1.1.0
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
//
// Rows flagged Missing are returned and COUNT as ownership. A Missing row is
// a book's claim on a file it could not see at the last check -- a file on an
// unmounted share, one mid-move, one a partial download has not written yet.
// When the file reappears at that path it is still that book's file; letting a
// scan import it as a new book (or the organizer move it as some other book's)
// because the owner's row happened to say Missing is exactly the fragment
// bleed this lookup exists to stop. Callers that want only present rows filter
// on BookFile.Missing themselves.
//
// This is the scanner's variant. A caller about to MOVE or otherwise mutate a
// file on the answer must use BookFileRowsAtPathStrict instead.
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

// ErrOwnershipLookupIncomplete is returned by BookFileRowsAtPathStrict when the
// complete multi-row lookup is not available. It wraps
// ErrBookFilesAtPathUnavailable, so errors.Is matches either.
var ErrOwnershipLookupIncomplete = errors.New("book_file ownership lookup incomplete (memdb not serving); retry later")

type ownershipIncompleteError struct{ path string }

func (e ownershipIncompleteError) Error() string {
	return ErrOwnershipLookupIncomplete.Error() + ": " + e.path
}

func (e ownershipIncompleteError) Is(target error) bool {
	return target == ErrOwnershipLookupIncomplete || target == ErrBookFilesAtPathUnavailable
}

// BookFileRowsAtPathStrict is BookFileRowsAtPath without the single-row
// fallback, for callers that ACT on the answer -- the organizer before it moves
// a file. During the ~130s memdb warmup after a restart the fallback index
// holds one row per path CRC: a second owner at the same path is invisible and
// a CRC collision can hide the path entirely, so "no other owner" from it is a
// guess. A guess is fine for deciding whether to skip an import (the next scan
// asks again) and not fine for moving a file out from under its owner. Here an
// incomplete lookup is an error wrapping ErrOwnershipLookupIncomplete; the
// caller skips the book and retries on a later pass.
func BookFileRowsAtPathStrict(s BookFilePathLookup, path string) ([]BookFile, error) {
	if path == "" {
		return nil, nil
	}
	rows, err := s.BookFilesAtPath(path)
	if err == nil {
		return rows, nil
	}
	if errors.Is(err, ErrBookFilesAtPathUnavailable) {
		return nil, ownershipIncompleteError{path: path}
	}
	return nil, err
}
