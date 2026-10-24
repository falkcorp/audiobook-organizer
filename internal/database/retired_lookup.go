// file: internal/database/retired_lookup.go
// version: 1.8.0
// guid: 5c9e1f27-0a3b-4d68-b4e2-7f1a90c36d84
// last-edited: 2026-10-24

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

// NoRetiredBookFiles is implemented by an in-memory test double that is
// complete by construction: it never holds a retired row, so its plain
// (live-only) book_file reads ARE the full answer. The IncludingRetired helpers
// fall back to the plain read for such a store and for no other.
//
// The marker exists so the fallback is an opt-in and not a guess. A store that
// merely lacks RetiredBookFileReader (a decorator that was never taught to
// forward the capability, a new backend) could be hiding retired rows, and
// answering from its live subset would read a retired version's path, PID or
// file as free; the helpers return ErrRetiredReaderUnavailable for it instead.
type NoRetiredBookFiles interface {
	HoldsNoRetiredBookFiles() bool
}

// plainReadsAreComplete reports whether s declares (through any opted-in
// decorator chain) that its live-only reads are the complete answer.
func plainReadsAreComplete(s any) bool {
	m, ok := AsCapability[NoRetiredBookFiles](s)
	return ok && m.HoldsNoRetiredBookFiles()
}

// AllBookFilesCoreIncludingRetired returns every book_file row, live and
// retired, for callers that narrow the store to a local interface and so
// cannot name RetiredBookFileReader themselves.
//
// The store is resolved through AsCapability, so it reaches the Pebble store
// through decorators. A store that does not implement the reader answers from
// the plain GetAllBookFilesCore only when it declares NoRetiredBookFiles (a
// MockStore, or a test fake that says it never holds a retired row); any other
// store returns ErrRetiredReaderUnavailable, since its live subset might be
// hiding retired rows. The production store always takes the first branch.
func AllBookFilesCoreIncludingRetired(s any) ([]BookFileCore, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetAllBookFilesCoreIncludingRetired()
	}
	if g, ok := s.(interface {
		GetAllBookFilesCore() ([]BookFileCore, error)
	}); ok && plainReadsAreComplete(s) {
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
	}); ok && plainReadsAreComplete(s) {
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
	}); ok && plainReadsAreComplete(s) {
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
	}); ok && plainReadsAreComplete(s) {
		return g.GetBookFileByPath(filePath)
	}
	return nil, fmt.Errorf("book_file by path %s: %w", filePath, ErrRetiredReaderUnavailable)
}

// RetiredVersionsFromBook returns the retired versions that were retired out of
// shellID (the trashed or merged book their files used to belong to), with the
// same capability resolution and fallback as AllBookFilesCoreIncludingRetired.
// A non-empty answer means shellID is a shell: its files live on as retired
// rows under another book, and a scan that reaches the shell by path or by
// organizer ID must not read it as a book that still owns them.
func RetiredVersionsFromBook(s any, shellID string) ([]BookVersion, error) {
	if r, ok := AsCapability[RetiredBookFileReader](s); ok {
		return r.GetRetiredVersionsFromBook(shellID)
	}
	if plainReadsAreComplete(s) {
		return nil, nil
	}
	return nil, fmt.Errorf("retired versions of %s: %w", shellID, ErrRetiredReaderUnavailable)
}

// BookFilesHoldingHash is BookFilesWithHash for callers that hold a store as
// any (the scanner's package store, narrowed to its own interface): every row,
// live and retired, whose FileHash or OriginalFileHash is hash. It resolves the
// method through AsCapability and keeps BookFilesWithHash's contract: a store that
// cannot give the complete answer returns ErrBookFilesWithHashUnavailable and
// the caller must fail closed on it.
//
// A store that declares NoRetiredBookFiles (a MockStore, or a fake that says
// it never holds a retired row) has no retired rows to find, so an
// ErrBookFilesWithHashUnavailable from it (the MockStore's unstubbed default)
// or a missing method is an empty answer; a test that stubs
// BookFilesWithHashFunc still gets what it returned. Any other store without
// the method is unavailable, never empty.
func BookFilesHoldingHash(s any, hash string) ([]BookFile, error) {
	h, ok := AsCapability[interface {
		BookFilesWithHash(hash string) ([]BookFile, error)
	}](s)
	if !ok {
		if plainReadsAreComplete(s) {
			return nil, nil
		}
		return nil, fmt.Errorf("book_files with hash: %w", ErrBookFilesWithHashUnavailable)
	}
	rows, err := h.BookFilesWithHash(hash)
	if err != nil && errors.Is(err, ErrBookFilesWithHashUnavailable) && plainReadsAreComplete(s) {
		return nil, nil
	}
	return rows, err
}
