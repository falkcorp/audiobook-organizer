// file: internal/database/retired_lookup.go
// version: 1.10.0
// guid: 5c9e1f27-0a3b-4d68-b4e2-7f1a90c36d84
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
)

// ErrRetiredReaderUnavailable is returned by the IncludingRetired helpers when
// the store does not offer the retired-row reader, so a caller that needs the
// retired rows learns it cannot have them instead of quietly working from the
// live subset.
var ErrRetiredReaderUnavailable = errors.New("store cannot read retired book_file rows")

// AllBookFilesCoreIncludingRetired returns every book_file row, live and
// retired, for callers that narrow the store to a local interface and so
// cannot name RetiredBookFileReader themselves.
//
// The store is resolved through AsCapability, so it reaches the Pebble store
// through decorators that opt in via Unwrap. It FAILS CLOSED: a store that does
// not resolve to a RetiredBookFileReader (a decorator that embeds Store without
// Unwrap, a test double that never declared itself) answers
// ErrRetiredReaderUnavailable, never the live-only GetAllBookFilesCore. A claim
// set built from the live rows alone would silently omit every retired row's
// path and let a write land on a file a retired version holds, and the result
// would look exactly like a complete one. The hand-written MockStore implements
// the reader (it holds no retired rows), so tests that use it are unaffected;
// any other fake an op reaches must implement GetAllBookFilesCoreIncludingRetired
// itself.
func AllBookFilesCoreIncludingRetired(s any) ([]BookFileCore, error) {
	r, ok := AsCapability[RetiredBookFileReader](s)
	if !ok {
		return nil, fmt.Errorf("all book_file rows: %w", ErrRetiredReaderUnavailable)
	}
	return r.GetAllBookFilesCoreIncludingRetired()
}

// BookFilesIncludingRetired is GetBookFiles with the retired rows kept, for
// callers that hold the store as any. Like AllBookFilesCoreIncludingRetired it
// resolves the reader through AsCapability and fails closed.
func BookFilesIncludingRetired(s any, bookID string) ([]BookFile, error) {
	r, ok := AsCapability[RetiredBookFileReader](s)
	if !ok {
		return nil, fmt.Errorf("book_files of %s: %w", bookID, ErrRetiredReaderUnavailable)
	}
	return r.GetBookFilesIncludingRetired(bookID)
}

// BookFileByPIDIncludingRetired is GetBookFileByPID that also answers a PID
// held only by a retired row, with the same capability resolution and the same
// fail-closed answer as AllBookFilesCoreIncludingRetired.
func BookFileByPIDIncludingRetired(s any, itunesPID string) (*BookFile, error) {
	r, ok := AsCapability[RetiredBookFileReader](s)
	if !ok {
		return nil, fmt.Errorf("book_file by PID %s: %w", itunesPID, ErrRetiredReaderUnavailable)
	}
	return r.GetBookFileByPIDIncludingRetired(itunesPID)
}

// RetiredVersionsFromBook returns the retired versions that were retired out of
// shellID (the trashed or merged book their files used to belong to), with the
// same capability resolution and fail-closed answer as
// AllBookFilesCoreIncludingRetired. A non-empty answer means shellID is a
// shell: its files live on as retired rows under another book, and a scan that
// reaches the shell by path or by organizer ID must not read it as a book that
// still owns them.
func RetiredVersionsFromBook(s any, shellID string) ([]BookVersion, error) {
	r, ok := AsCapability[RetiredBookFileReader](s)
	if !ok {
		return nil, fmt.Errorf("retired versions of %s: %w", shellID, ErrRetiredReaderUnavailable)
	}
	return r.GetRetiredVersionsFromBook(shellID)
}

// BookFilesHoldingHash is BookFilesWithHash for callers that hold a store as
// any (the scanner's package store, narrowed to its own interface): every row,
// live and retired, whose FileHash or OriginalFileHash is hash. It resolves the
// method through AsCapability and keeps BookFilesWithHash's contract: a store
// that cannot give the complete answer, or has no such method, returns
// ErrBookFilesWithHashUnavailable and the caller must fail closed on it. It
// never turns that refusal into an empty answer; RetiredHoldersOfHash is the
// caller that decides what a refusal means for an import.
func BookFilesHoldingHash(s any, hash string) ([]BookFile, error) {
	h, ok := AsCapability[interface {
		BookFilesWithHash(hash string) ([]BookFile, error)
	}](s)
	if !ok {
		return nil, fmt.Errorf("book_files with hash: %w", ErrBookFilesWithHashUnavailable)
	}
	return h.BookFilesWithHash(hash)
}

// BookFileByPathIncludingRetired is GetBookFileByPath that also answers a path
// held only by a retired row, for callers that narrow the store to a local
// interface. Like AllBookFilesCoreIncludingRetired it fails closed. It reads
// the single-owner path index (one row per CRC, last writer wins); a caller
// gating a WRITE on "no row holds this path" reads every row at the path with
// BookFileRowsAtPathStrict and asks RetiredClaimOnPath.
func BookFileByPathIncludingRetired(s any, path string) (*BookFile, error) {
	r, ok := AsCapability[RetiredBookFileReader](s)
	if !ok {
		return nil, fmt.Errorf("book_file at %q: %w", path, ErrRetiredReaderUnavailable)
	}
	return r.GetBookFileByPathIncludingRetired(path)
}

// RetiredClaimOnPath returns the retired row among rows (one path's
// BookFilesAtPath answer), or nil when none is retired. When several are, the
// smallest (BookID, ID) is returned so the answer, and the message built from
// it, is deterministic.
//
// It is the single place the claim rule in iface_retired.go is written for
// WRITE gates: a retired row counts as a claim on its file whoever holds it,
// whether or not the holder is live, and even when the holder is the very book
// that wants a row on the path. It deliberately takes no "self" or liveness
// argument; every caller that filtered its own or a dead book's rows out of an
// "owners" list was letting a retired version of itself through.
func RetiredClaimOnPath(rows []BookFile) *BookFile {
	var best *BookFile
	for i := range rows {
		r := &rows[i]
		if !r.Retired {
			continue
		}
		if best == nil || r.BookID < best.BookID || (r.BookID == best.BookID && r.ID < best.ID) {
			best = r
		}
	}
	return best
}
