// file: internal/database/retired_lookup.go
// version: 1.4.0
// guid: 5c9e1f27-0a3b-4d68-b4e2-7f1a90c36d84
// last-edited: 2026-10-23

package database

import (
	"errors"
	"fmt"
)

// ErrRetiredReaderUnavailable is returned by the IncludingRetired helpers when
// the store offers neither the retired-row reader nor a plain getter to fall
// back on, so a caller that needs the retired rows learns it cannot have them
// instead of quietly working from the live subset.
var ErrRetiredReaderUnavailable = errors.New("store cannot read retired book_file rows")

// AllBookFilesCoreIncludingRetired returns every book_file row, live and
// retired, for callers that narrow the store to a local interface and so
// cannot name RetiredBookFileReader themselves.
//
// The store is resolved through AsCapability, so it reaches the Pebble store
// through decorators. A store that does not implement the reader (a MockStore,
// a hand-written test fake) is an in-memory library that has never had a row
// retired, so for it the plain GetAllBookFilesCore IS the complete list and is
// used; the production store always takes the first branch.
func AllBookFilesCoreIncludingRetired(s any) ([]BookFileCore, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetAllBookFilesCoreIncludingRetired()
	}
	if g, ok := s.(interface {
		GetAllBookFilesCore() ([]BookFileCore, error)
	}); ok {
		return g.GetAllBookFilesCore()
	}
	return nil, fmt.Errorf("all book_file rows: %w", ErrRetiredReaderUnavailable)
}

// BookFilesIncludingRetired is GetBookFiles with the retired rows kept, with the
// same capability resolution and fallback as AllBookFilesCoreIncludingRetired.
func BookFilesIncludingRetired(s any, bookID string) ([]BookFile, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetBookFilesIncludingRetired(bookID)
	}
	if g, ok := s.(interface {
		GetBookFiles(bookID string) ([]BookFile, error)
	}); ok {
		return g.GetBookFiles(bookID)
	}
	return nil, fmt.Errorf("book_files of %s: %w", bookID, ErrRetiredReaderUnavailable)
}

// BookFileByPIDIncludingRetired is GetBookFileByPID that also answers a PID
// held only by a retired row, with the same capability resolution and fallback
// as AllBookFilesCoreIncludingRetired.
func BookFileByPIDIncludingRetired(s any, itunesPID string) (*BookFile, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetBookFileByPIDIncludingRetired(itunesPID)
	}
	if g, ok := s.(interface {
		GetBookFileByPID(itunesPID string) (*BookFile, error)
	}); ok {
		return g.GetBookFileByPID(itunesPID)
	}
	return nil, fmt.Errorf("book_file by PID %s: %w", itunesPID, ErrRetiredReaderUnavailable)
}

// BookFileByPathIncludingRetired is GetBookFileByPath that also answers a path
// held only by a retired row: the single-row path-index hit, for the "is this
// path already claimed?" checks that cannot use the complete BookFilesAtPath
// (a caller that wants only the one row, or the warmup fallback of
// BookFileRowsAtPath). Same capability resolution and fallback as
// BookFileByPIDIncludingRetired.
func BookFileByPathIncludingRetired(s any, filePath string) (*BookFile, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetBookFileByPathIncludingRetired(filePath)
	}
	if g, ok := s.(interface {
		GetBookFileByPath(filePath string) (*BookFile, error)
	}); ok {
		return g.GetBookFileByPath(filePath)
	}
	return nil, fmt.Errorf("book_file by path %s: %w", filePath, ErrRetiredReaderUnavailable)
}
