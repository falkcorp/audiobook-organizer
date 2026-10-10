// file: internal/organizer/folder_scope.go
// version: 1.0.0
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
// some of their files are inside the scoped folder and some are not. Organizing
// such a book would move or copy files the caller never scoped in, so it stays
// put. Books with no file inside the folder are simply not part of the run and
// are not counted.
const OutcomeStraddlesFolderScope = "straddles_folder_scope"

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

// scopeBooks keeps the books whose book_file rows all lie inside the scoped
// folder, and returns how many were held back for straddling its boundary.
//
// Membership comes from book_file rows, not Book.FilePath: that column is the
// stale legacy mirror, and a multi-file book's real location is its rows.
// Missing rows are ignored (their paths are gone from disk and are not what an
// organize would move). A book with no live row is out of scope: fail closed.
//
// One batched read serves the whole candidate set, so this adds no per-book
// store round trip to a library-scale run. A read error aborts the run for
// the same reason a bad FolderPath does.
func (orgSvc *Service) scopeBooks(books []database.Book, scope *folderScope) ([]database.Book, int, error) {
	if len(books) == 0 {
		return books, 0, nil
	}
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	filesByBook, err := orgSvc.db.GetBookFilesForIDsCore(ids)
	if err != nil {
		return nil, 0, fmt.Errorf("organize: read book_files to apply folder scope: %w", err)
	}
	kept := make([]database.Book, 0, len(books))
	straddling := 0
	for i := range books {
		live, inside := 0, 0
		for _, f := range filesByBook[books[i].ID] {
			if f.Missing {
				continue
			}
			live++
			if scope.contains(f.FilePath) {
				inside++
			}
		}
		switch {
		case live > 0 && inside == live:
			kept = append(kept, books[i])
		case inside > 0:
			straddling++
		}
	}
	return kept, straddling, nil
}
