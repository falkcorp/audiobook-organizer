// file: internal/repairs/guards.go
// version: 1.1.0
// guid: 5a2c9e14-6f3b-4d87-b0e1-9c7d4a8f2e56
// last-edited: 2026-09-28

package repairs

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Guard skip kinds. They are also the decision kinds
// maintenance.version-group-primary-repair reports for the same groups, so a
// row skipped here reads the same in both places.
const (
	// SkipITunes: a book of the row has a file (active or missing) under
	// books/itunes/**, the live iTunes library, which is hands-off.
	SkipITunes = "skipped_itunes"
	// SkipOwnerManual: a book of the row is Doctor Who / Big Finish /
	// Torchwood by path or series; the owner applies those by hand.
	SkipOwnerManual = "skipped_owner_manual"
)

// GuardBookPaths is the one hands-off check for a single book. paths should
// hold Book.FilePath and the path of every book_file row, missing rows
// included: the rules are about where the book lives, not whether its file
// is on disk right now. seriesName is the book's series name ("" for none).
// It returns "" when the book may be touched.
func GuardBookPaths(bookID string, paths []string, seriesName string) (kind, reason string) {
	paths = withResolved(paths)
	for _, p := range paths {
		if p != "" && pathutil.UnderFrozenITunesTree(p) {
			return SkipITunes, fmt.Sprintf("member %s has a file under books/itunes/** (hands-off): %s", bookID, p)
		}
	}
	for _, p := range paths {
		if applygate.IsOwnerManualOnly(p, seriesName) {
			return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (path %q, series %q); owner applies these by hand", bookID, p, seriesName)
		}
	}
	// A series match with no paths at all still counts.
	if len(paths) == 0 && applygate.IsOwnerManualOnly("", seriesName) {
		return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (series %q); owner applies these by hand", bookID, seriesName)
	}
	return "", ""
}

// withResolved adds, after each path, the path its symlinks resolve to when
// that differs: a library symlink into books/itunes/** is under the frozen
// tree however it is spelled. A path that cannot be resolved (it is gone,
// or unreadable) is checked lexically only; for one that no longer exists,
// its folder is resolved instead.
func withResolved(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p)
		if p == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			dir, derr := filepath.EvalSymlinks(filepath.Dir(p))
			if derr != nil {
				continue
			}
			r = filepath.Join(dir, filepath.Base(p))
		}
		if r != p {
			out = append(out, r)
		}
	}
	return out
}

// GuardReader is what the framework guard reads.
type GuardReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// SeriesNamer resolves a series id to its name. Built once per run.
type SeriesNamer func(id int) string

// SeriesNamesFrom builds a SeriesNamer from a series list.
func SeriesNamesFrom(all []database.Series) SeriesNamer {
	m := make(map[int]string, len(all))
	for _, s := range all {
		m[s.ID] = s.Name
	}
	return func(id int) string { return m[id] }
}

// GuardBooks runs GuardBookPaths over every book id, reading each book and
// its files fresh. A book that is gone or soft-deleted is not checked (it
// has nothing left to protect), matching the vg op's member guard. A read
// error is returned: without the paths the guard cannot see a hands-off book,
// so the caller must not treat the row as clear.
func GuardBooks(r GuardReader, series SeriesNamer, bookIDs []string) (kind, reason string, err error) {
	ids := append([]string(nil), bookIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		b, err := r.GetBookByID(id)
		if err != nil {
			return "", "", fmt.Errorf("guard: read book %s: %w", id, err)
		}
		if b == nil || b.IsSoftDeleted() {
			continue
		}
		files, err := r.GetBookFiles(id)
		if err != nil {
			return "", "", fmt.Errorf("guard: read files of %s: %w", id, err)
		}
		paths := make([]string, 0, len(files)+1)
		paths = append(paths, b.FilePath)
		for _, f := range files {
			paths = append(paths, f.FilePath)
		}
		name := ""
		if b.SeriesID != nil && series != nil {
			name = series(*b.SeriesID)
		}
		if k, why := GuardBookPaths(id, paths, name); k != "" {
			return k, why, nil
		}
	}
	return "", "", nil
}
