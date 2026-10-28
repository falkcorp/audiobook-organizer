// file: internal/versions/serve_guard.go
// version: 1.1.0
// guid: b3e71d58-2a94-4c06-8f1e-5d9a07c4e621
// last-edited: 2026-10-27

package versions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// servedGuard answers "does a live book still serve this file?" for the purge
// of a trashed version. A file is served when
//
//   - it is the version's own book's FilePath, or lies inside it when that is a
//     folder (a multi-file book's FilePath is its containing folder; the rule
//     is database.BookOwnFolder's), outside any .versions/ slot;
//   - it is named by a book_file row of the book's ACTIVE version, or of no
//     version;
//   - it is the same file as one of those, reached by a symlink or a hard link;
//   - or ANOTHER live book names it the same ways (its FilePath, any ancestor
//     folder its FilePath names, a row of its active version or of no
//     version): deluge and import folders are shared between books.
//
// When the version's own book is itself trashed it serves nothing, so only the
// ANOTHER-live-book rules apply.
//
// Everything errs toward keeping the file: an unreadable book, version list or
// row list is an error, never "not served".
type servedGuard struct {
	store versionPurger
	book  *database.Book

	exact  map[string]struct{} // canonical served file paths of the own book
	folder string              // canonical folder the own book's FilePath names, or ""
	infos  []os.FileInfo       // stat of each served file, for hard links

	// live caches, per other book, whether it is live and its active version ids.
	others map[string]*otherBook
}

type otherBook struct {
	live   bool
	active map[string]struct{}
}

// canonPath is p made absolute and cleaned, with symlinks resolved when the
// path exists; otherwise the absolute cleaned path.
func canonPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func newServedGuard(store versionPurger, book *database.Book, files []database.BookFile) (*servedGuard, error) {
	g := &servedGuard{store: store, book: book, exact: map[string]struct{}{}, others: map[string]*otherBook{}}
	if bookIsTrashed(book) {
		return g, nil
	}

	active, err := g.activeVersionIDs(book.ID)
	if err != nil {
		return nil, err
	}
	add := func(p string) {
		if p == "" {
			return
		}
		g.exact[canonPath(p)] = struct{}{}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			g.infos = append(g.infos, fi)
		}
	}
	add(book.FilePath)
	if book.FilePath != "" {
		if dir := database.BookOwnFolder(book.FilePath, files); dir != "" && dir == filepath.Clean(book.FilePath) {
			g.folder = canonPath(dir)
		}
	}
	for i := range files {
		if _, ok := active[files[i].VersionID]; ok || files[i].VersionID == "" {
			add(files[i].FilePath)
		}
	}
	return g, nil
}

func (g *servedGuard) activeVersionIDs(bookID string) (map[string]struct{}, error) {
	vers, err := g.store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return nil, fmt.Errorf("list versions of book %s: %w", bookID, err)
	}
	active := make(map[string]struct{})
	for i := range vers {
		if vers[i].Status == database.BookVersionStatusActive {
			active[vers[i].ID] = struct{}{}
		}
	}
	return active, nil
}

// serves reports whether a live book still serves p.
func (g *servedGuard) serves(p string) (bool, error) {
	if g.ownBookServes(p) {
		return true, nil
	}
	return g.otherBookServes(p)
}

func (g *servedGuard) ownBookServes(p string) bool {
	c := canonPath(p)
	if _, ok := g.exact[c]; ok {
		return true
	}
	if g.folder != "" && pathutil.IsWithin(c, g.folder) {
		rest := strings.TrimPrefix(c, g.folder)
		inSlot := false
		for _, part := range strings.Split(rest, string(filepath.Separator)) {
			if part == VersionsDirName {
				inSlot = true
			}
		}
		if !inSlot {
			return true
		}
	}
	// Same file under another name (hard link, or a symlink the resolution
	// above could not follow).
	if len(g.infos) > 0 {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			for _, other := range g.infos {
				if os.SameFile(fi, other) {
					return true
				}
			}
		}
	}
	return false
}

// otherBookServes reports whether a live book other than the version's own
// names p. Any read error is returned: the purge then deletes nothing.
func (g *servedGuard) otherBookServes(p string) (bool, error) {
	variants := []string{p}
	if c := canonPath(p); c != p {
		variants = append(variants, c)
	}
	for _, v := range variants {
		rows, err := g.store.BookFilesAtPath(v)
		if err != nil {
			return false, fmt.Errorf("find other books naming %s: %w", v, err)
		}
		for i := range rows {
			r := &rows[i]
			if r.Retired || r.BookID == g.book.ID {
				continue
			}
			ob, err := g.other(r.BookID)
			if err != nil {
				return false, err
			}
			if !ob.live {
				continue
			}
			if _, ok := ob.active[r.VersionID]; ok || r.VersionID == "" {
				return true, nil
			}
		}
		// A book whose FilePath is v itself, or any folder v sits in.
		for _, path := range pathAndAncestors(v) {
			b, err := g.store.GetBookByFilePath(path)
			if err != nil {
				return false, fmt.Errorf("find book at %s: %w", path, err)
			}
			if b != nil && b.ID != g.book.ID && !bookIsTrashed(b) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (g *servedGuard) other(bookID string) (*otherBook, error) {
	if ob, ok := g.others[bookID]; ok {
		return ob, nil
	}
	b, err := g.store.GetBookByID(bookID)
	if err != nil {
		return nil, fmt.Errorf("read book %s: %w", bookID, err)
	}
	ob := &otherBook{}
	if b != nil && !bookIsTrashed(b) {
		active, err := g.activeVersionIDs(bookID)
		if err != nil {
			return nil, err
		}
		ob.live, ob.active = true, active
	}
	g.others[bookID] = ob
	return ob, nil
}

// pathAndAncestors returns p followed by each ancestor directory of p, nearest
// first, stopping before the filesystem root and before the configured library
// root (neither is a book's own folder), and before any ancestor that p only
// reaches through a .versions/ slot: a file in X/.versions/{vid}/ is not served
// by a book whose FilePath is X.
func pathAndAncestors(p string) []string {
	libRoot := ""
	if root := config.AppConfig.RootDir; root != "" {
		libRoot = canonPath(root)
	}
	out := []string{p}
	cur := p
	for {
		if filepath.Base(cur) == VersionsDirName {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." || parent == string(filepath.Separator) || (libRoot != "" && (parent == libRoot || canonPath(parent) == libRoot)) {
			break
		}
		out = append(out, parent)
		cur = parent
	}
	return out
}
