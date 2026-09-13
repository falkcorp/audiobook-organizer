// file: internal/merge/itunes_guard.go
// version: 1.0.0
// guid: 7a35388d-79af-4a1e-a553-a61d6dfcf4ae
// last-edited: 2026-09-13

package merge

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// ErrITunesProtected is the sentinel every *ITunesProtectedError matches with
// errors.Is. Standing rule: nothing this application does may mutate the active
// iTunes library (books/itunes/**). A merge, combine, duplicate-of apply,
// auto-resolve, LLM auto-merge, same-path merge or split-book merge rewrites
// book rows, moves book_file rows, soft- or hard-deletes losers and queues ITL
// removals, so any of them touching a book whose files live in that tree is
// refused at APPLY time.
//
// Why at apply time and not only where proposals are generated
// (regroup_shattered_ai.go excludes the tree at the source): a proposal filter
// protects one lane. Review queues, candidate rows, bulk ops and HTTP merge
// endpoints all reach the same mutating chokepoints by other routes, and a
// candidate written before the source filter existed is still approvable.
var ErrITunesProtected = errors.New("merge refused: a participating book has a file under the active iTunes library")

// ITunesProtectedError names the book and path that caused a refusal. It is a
// typed refusal (IsRefusal reports true), so HTTP callers answer 409 and ops
// count it as refused rather than failed.
//
// BookID and Path are empty when the refusal is about the configuration itself
// (iTunes sync enabled with no library paths configured). Cause is set when the
// refusal is fail-closed on a store read: the guard could not see a book's
// files, so it cannot prove they are outside the protected tree.
type ITunesProtectedError struct {
	BookID string
	Path   string
	Root   string
	Reason string
	Cause  error
}

func (e *ITunesProtectedError) Error() string {
	msg := "merge refused (iTunes library is read-only)"
	if e.BookID != "" {
		msg += ": book " + e.BookID
	}
	if e.Path != "" {
		msg += fmt.Sprintf(" has file %q", e.Path)
	}
	if e.Root != "" {
		msg += fmt.Sprintf(" under protected root %q", e.Root)
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Is makes errors.Is(err, ErrITunesProtected) true for every instance.
func (e *ITunesProtectedError) Is(target error) bool { return target == ErrITunesProtected }

// Unwrap exposes the store failure behind a fail-closed refusal.
func (e *ITunesProtectedError) Unwrap() error { return e.Cause }

// ITunesGuardStore is the read surface the guard needs. Exported so packages
// with their own store (internal/dedup) can forward it into GuardITunesProtected.
type ITunesGuardStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// frozenITunesSegmentRoot labels refusals that came from the config-independent
// books/itunes/ segment match rather than a configured root.
const frozenITunesSegmentRoot = "books/itunes/"

// ITunesProtectedRoots returns the cleaned protected roots for cfg: the folder
// holding itunes.library_read_path (the library file and, in the standard
// layout, the whole iTunes tree) and itunes.media_root.
//
// Empty configuration:
//   - sync DISABLED and both paths empty: no configured roots, no error. The
//     guard still applies the config-independent books/itunes/ segment match.
//   - sync ENABLED and both paths empty: an *ITunesProtectedError. The app is
//     told an iTunes library is live but not where it is, so no merge can be
//     proven safe; refusing is the fail-closed reading.
func ITunesProtectedRoots(cfg config.ITunesConfig) ([]string, error) {
	var roots []string
	if cfg.LibraryReadPath != "" {
		roots = append(roots, filepath.Clean(filepath.Dir(cfg.LibraryReadPath)))
	}
	if cfg.MediaRoot != "" {
		roots = append(roots, filepath.Clean(cfg.MediaRoot))
	}
	if len(roots) == 0 && cfg.SyncEnabled {
		return nil, &ITunesProtectedError{Reason: "iTunes sync is enabled but neither itunes.library_read_path nor itunes.media_root is set, so no merge can be proven to stay outside the iTunes library"}
	}
	return roots, nil
}

// GuardITunesProtected refuses when any book in bookIDs — survivor and losers
// alike — has its own FilePath or any book_file FilePath under a protected
// iTunes root. Call it at the top of every mutating merge-family entry point,
// while holding the merge lock and before any write.
//
// Fail closed: a store error reading a book or its files is a refusal (with
// Cause set), never a pass. A book the store has no row for is skipped; the
// caller's own lookup reports it as not found.
//
// Only FilePath is checked, not BookFile.ITunesPath. ITunesPath is provenance
// (where iTunes had the file when it was imported); a file since moved into the
// managed library is no longer under the frozen tree, and merging its row does
// not touch that tree's files.
func GuardITunesProtected(store ITunesGuardStore, bookIDs []string) error {
	roots, err := ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return err
	}
	return guardITunesProtected(store, bookIDs, roots)
}

// GuardITunesProtectedLoaded is GuardITunesProtected for a caller that has
// already loaded every participant and its book_file rows (MergeBooks does, for
// the scan-state and election checks). It re-reads nothing, so the caller's own
// fail-closed read errors keep their meaning and no extra store round trip is
// paid. Every book in books must have its rows in filesByID.
func GuardITunesProtectedLoaded(books []*database.Book, filesByID map[string][]database.BookFile) error {
	roots, err := ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return err
	}
	for _, b := range books {
		if b == nil {
			continue
		}
		if err := checkITunesPath(b.ID, b.FilePath, roots); err != nil {
			return err
		}
		for _, f := range filesByID[b.ID] {
			if err := checkITunesPath(b.ID, f.FilePath, roots); err != nil {
				return err
			}
		}
	}
	return nil
}

func guardITunesProtected(store ITunesGuardStore, bookIDs []string, roots []string) error {
	seen := make(map[string]bool, len(bookIDs))
	for _, id := range bookIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		book, err := store.GetBookByID(id)
		if err != nil {
			return &ITunesProtectedError{BookID: id, Reason: "cannot read the book to verify it is outside the iTunes library", Cause: err}
		}
		if book == nil {
			continue
		}
		if err := checkITunesPath(id, book.FilePath, roots); err != nil {
			return err
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			return &ITunesProtectedError{BookID: id, Reason: "cannot read the book's files to verify they are outside the iTunes library", Cause: err}
		}
		for _, f := range files {
			if err := checkITunesPath(id, f.FilePath, roots); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkITunesPath matches p against the protected roots on a path-separator
// boundary (pathutil.IsWithin), after filepath.Clean so "/root/../x" cannot
// sneak in or out. A relative path is refused: it has no base to resolve
// against, so it can never be proven outside an absolute root.
func checkITunesPath(bookID, p string, roots []string) error {
	if p == "" {
		return nil
	}
	if config.UnderFrozenITunesTree(p) {
		return &ITunesProtectedError{BookID: bookID, Path: p, Root: frozenITunesSegmentRoot}
	}
	if !filepath.IsAbs(p) {
		return &ITunesProtectedError{BookID: bookID, Path: p, Reason: "relative path cannot be verified against the iTunes library roots"}
	}
	clean := filepath.Clean(p)
	for _, root := range roots {
		if pathutil.IsWithin(clean, root) {
			return &ITunesProtectedError{BookID: bookID, Path: p, Root: root}
		}
	}
	return nil
}
