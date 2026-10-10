// file: internal/organizer/folder_scope.go
// version: 1.1.0
// guid: 3e8a51c7-2b94-4d06-a7f3-9c1d60e4b825
// last-edited: 2026-10-10

package organizer

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// OutcomeStraddlesFolderScope counts books the folder scope held back because
// some of the files an organize would touch are inside the scoped folder and
// some are not. Organizing such a book would move or copy files the caller
// never scoped in, so it stays put. Books with nothing inside the folder are
// simply not part of the run and are not counted.
const OutcomeStraddlesFolderScope = "straddles_folder_scope"

// OutcomeFilePathOutsideFolderScope counts books whose book_file rows are all
// inside the scoped folder but whose book.file_path is not. The single-file
// copy and the in-place rename both take their source from book.file_path, so
// organizing such a book would copy or MOVE the file at that outside path. The
// usual cause is a stale file_path; the repair is to the row, not to the scope,
// which is why this is counted apart from a true straddle.
const OutcomeFilePathOutsideFolderScope = "file_path_outside_folder_scope"

// OutcomeLeftFolderScope counts books that were inside the scoped folder when
// the run listed them but, re-checked just before their organize, no longer
// have anything inside it: a scan repointed their rows and file_path in
// between. They are skipped, and the next run judges them where they are now.
const OutcomeLeftFolderScope = "left_folder_scope"

// ErrInvalidFolderScope is returned when Request.FolderPath is set but cannot be
// honored. The run is refused outright: an organize moves files, and "I could
// not understand the folder you scoped this to, so I did the whole library"
// is the failure this scope exists to prevent.
var ErrInvalidFolderScope = errors.New("organize: invalid folder_path scope")

// folderScope is a validated, cleaned Request.FolderPath.
type folderScope struct {
	root string
}

// newFolderScope validates req.FolderPath. A nil pointer means "no folder
// scope" (the whole library, or whatever BookIDs names) and yields a nil scope.
// A non-nil pointer must be an absolute path with no ".." element; it is
// cleaned (trailing slashes and "." elements removed) so the prefix match below
// compares like with like. Empty, blank, relative, and traversal paths are
// rejected, never widened.
func newFolderScope(p *string) (*folderScope, error) {
	if p == nil {
		return nil, nil
	}
	raw := *p
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty path", ErrInvalidFolderScope)
	}
	if !filepath.IsAbs(raw) {
		return nil, fmt.Errorf("%w: %q is not an absolute path", ErrInvalidFolderScope, raw)
	}
	for _, elem := range strings.Split(filepath.ToSlash(raw), "/") {
		if elem == ".." {
			return nil, fmt.Errorf("%w: %q contains a \"..\" element", ErrInvalidFolderScope, raw)
		}
	}
	return &folderScope{root: filepath.Clean(raw)}, nil
}

// contains reports whether path is the scoped folder or lies beneath it.
// pathutil.IsWithin enforces the separator boundary, so /x/a does not contain
// /x/ab; it does no cleaning, so the candidate is cleaned here. The comparison
// is textual: symlinks are not resolved and case is significant.
func (s *folderScope) contains(path string) bool {
	if path == "" {
		return false
	}
	return pathutil.IsWithin(filepath.Clean(path), s.root)
}

// scopeVerdict is what the folder scope decides about one book.
type scopeVerdict int

const (
	// scopeOutside: nothing an organize would touch is inside the folder. The
	// book is not part of the run and is not counted.
	scopeOutside scopeVerdict = iota
	// scopeInside: everything an organize would touch is inside the folder.
	scopeInside
	// scopeStraddles: held back, counted as OutcomeStraddlesFolderScope.
	scopeStraddles
	// scopeFilePathOutside: held back, counted as
	// OutcomeFilePathOutsideFolderScope.
	scopeFilePathOutside
)

// outcome is the Stats.Collisions key a held-back verdict is counted under,
// or "" for a verdict that is not counted.
func (v scopeVerdict) outcome() string {
	switch v {
	case scopeStraddles:
		return OutcomeStraddlesFolderScope
	case scopeFilePathOutside:
		return OutcomeFilePathOutsideFolderScope
	}
	return ""
}

// admit decides whether a book is in the folder scope, from exactly the paths
// an organize of it would touch (OrganizeOneBook):
//
//   - book.file_path, when non-empty. The in-place branch renames it (and,
//     when it is a directory, everything under it), and the single-file branch
//     copies or links it. It also picks the branch. It is checked even though
//     it is the stale legacy column, BECAUSE it is stale: when it disagrees
//     with the rows, it is still what those two branches act on.
//   - every book_file row that is not Missing and has a path. The directory
//     branch and the multi-file in-place move act on these. Missing rows are
//     gone from disk and are never copied or moved; empty-path rows name no
//     file and are dropped by the filter, multiFileInPlaceRows and
//     planTargetPaths alike, so they are ignored here too.
//
// A book is in scope only when every one of those paths is inside the folder.
// When none is, it is simply not part of the run. When some are, it is held
// back. A book with no live rows is judged by file_path alone, so the run
// treats it exactly as an unscoped run would (the filter skips it as missing
// files, or renames it in place under the library root, where the file it
// renames is inside the folder).
//
// scopeBooks and the per-book re-check in organizeBooksOpts both call this, so
// the listing and the row organized under the scan lock cannot be judged by
// different rules.
func (s *folderScope) admit(filePath string, rows []database.BookFileCore) scopeVerdict {
	rowsIn, rowsOut := 0, 0
	for _, f := range rows {
		if f.Missing || f.FilePath == "" {
			continue
		}
		if s.contains(f.FilePath) {
			rowsIn++
		} else {
			rowsOut++
		}
	}
	pathIn, pathOut := 0, 0
	if filePath != "" {
		if s.contains(filePath) {
			pathIn = 1
		} else {
			pathOut = 1
		}
	}
	switch {
	case rowsIn+pathIn == 0:
		return scopeOutside
	case rowsOut+pathOut == 0:
		return scopeInside
	case rowsOut == 0:
		// Every row is inside; only file_path is not.
		return scopeFilePathOutside
	default:
		return scopeStraddles
	}
}

// heldBackByScope counts the books the folder scope held back, by outcome.
type heldBackByScope map[string]int

// scopeBooks keeps the books admit puts inside the scoped folder, and counts
// the ones it holds back.
//
// One batched read serves the whole candidate set, so this adds no per-book
// store round trip to a library-scale run. A read error aborts the run for
// the same reason a bad FolderPath does.
func (orgSvc *Service) scopeBooks(books []database.Book, scope *folderScope) ([]database.Book, heldBackByScope, error) {
	if len(books) == 0 {
		return books, nil, nil
	}
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	filesByBook, err := orgSvc.db.GetBookFilesForIDsCore(ids)
	if err != nil {
		return nil, nil, fmt.Errorf("organize: read book_files to apply folder scope: %w", err)
	}
	kept := make([]database.Book, 0, len(books))
	held := heldBackByScope{}
	for i := range books {
		verdict := scope.admit(books[i].FilePath, filesByBook[books[i].ID])
		switch verdict {
		case scopeInside:
			kept = append(kept, books[i])
		case scopeOutside:
		default:
			held[verdict.outcome()]++
		}
	}
	return kept, held, nil
}
