// file: internal/versions/serve_guard.go
// version: 1.8.0
// guid: b3e71d58-2a94-4c06-8f1e-5d9a07c4e621
// last-edited: 2026-10-30

package versions

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// servedGuard answers "does a live book still serve this file?" for the purge
// of a trashed version. A file is served when
//
//   - it is a file of the version's own book that is NOT inside this version's
//     own .versions/{id}/ slot: trashing a version never moves its files, so a file outside a slot
//     is still where the book plays it (the player streams the book's rows with
//     no version filter, and Book.FilePath is often stale, so FilePath is not
//     the test). Only files inside the version's own slot are ever purged;
//   - it is the version's own book's FilePath, or lies inside it when that is a
//     folder (a multi-file book's FilePath is its containing folder; the rule
//     is database.BookOwnFolder's), outside any .versions/ slot;
//   - it is named by a book_file row of the book's ACTIVE version, or of no
//     version;
//   - it is the same file as one of those, reached by a symlink or a hard link;
//   - or ANOTHER live book names it: its FilePath, any ancestor folder its
//     FilePath names, or any non-retired row of it whose version is not
//     trashed or purged (alt versions included, because the player streams rows
//     regardless of version): deluge and import folders are shared between
//     books;
//   - or ANOTHER live book reaches it through a symlink: a row or FilePath of
//     it that is a link resolving to the file (or, for a FilePath, to a folder
//     above it), found by the link index in serve_guard_links.go.
//
// When the version's own book is itself trashed, or no longer exists, it serves
// nothing, so only the ANOTHER-live-book rules apply.
//
// Everything errs toward keeping the file: an unreadable book, version list or
// row list is an error, never "not served".
type servedGuard struct {
	store versionPurger
	book  *database.Book

	// verID is the version being purged; only files inside ITS slot,
	// .versions/{verID}/, are purgeable while the own book is live.
	verID string

	// skipOwn is set when the own book is itself trashed: it serves nothing.
	skipOwn bool

	// bookExists is false only when the own book row is gone (row gone:
	// hard-deleted). A trashed book's row still exists, though it serves nothing
	// now (skipOwn). It does not decide what a purge or the kept-file sweep may
	// remove: that is only ever inside the version's own slot (inVersionSlot).
	bookExists bool

	exact  map[string]struct{} // canonical served file paths of the own book
	folder string              // canonical folder the own book's FilePath names, or ""
	infos  []os.FileInfo       // stat of each served file, for hard links

	// served lists every path the own book is taken to serve (FilePath, and the
	// rows of its active or no version), whether or not it is on disk, for
	// requireServedFileOnDisk.
	served []string

	// others caches, per other book, whether it is live and which of its
	// versions are dead (trashed or purged).
	others map[string]*otherBook

	// link is the index of the paths other live books reach through a symlink
	// (serve_guard_links.go), built on first use.
	linkOnce sync.Once
	link     linkIndex
	linkErr  error
}

type otherBook struct {
	live bool
	dead map[string]struct{}
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

// pathSpellings is every way a path can be written that the stores key on:
// the path as given, and canonPath(path) (absolute, symlinks resolved) when
// that differs. The stores match path strings exactly, so a row written through
// a symlinked library root (/link/lib) and a lookup through its real path
// (/tank/lib) do not meet. Every "who else names this file?" question is asked
// of each spelling, and a hit on any is a hit. It cannot find a row stored under
// a spelling the question never produces (a row written with the link when the
// question holds the real path): a symlink is followed one way only.
func pathSpellings(p string) []string {
	if c := canonPath(p); c != p {
		return []string{p, c}
	}
	return []string{p}
}

// targetKey identifies a purge target across spellings of its folder: the
// folder is canonical, the file name is not resolved. A file that is itself a
// symlink is a different target from the file it points at (removing it unlinks
// the link and leaves the file), so the leaf is never followed.
func targetKey(p string) string {
	return filepath.Join(canonPath(filepath.Dir(p)), filepath.Base(p))
}

// rebuildServedGuard is newServedGuard from the book and rows as the store has
// them now, for the guard to be asked again after the version was claimed.
func rebuildServedGuard(store versionPurger, ver *database.BookVersion) (*servedGuard, error) {
	book, err := store.GetBookByID(ver.BookID)
	if err != nil {
		return nil, fmt.Errorf("read book %s: %w", ver.BookID, err)
	}
	own, err := store.GetBookFiles(ver.BookID)
	if err != nil {
		return nil, fmt.Errorf("list book files of %s: %w", ver.BookID, err)
	}
	return newServedGuard(store, book, ver.BookID, own, ver.ID)
}

// newServedGuard builds the guard for one version purge. book is the version's
// own book, nil when it no longer exists (bookID is then its id): a book that is
// gone serves nothing, like a trashed one.
func newServedGuard(store versionPurger, book *database.Book, bookID string, files []database.BookFile, verID string) (*servedGuard, error) {
	missing := book == nil
	if missing {
		book = &database.Book{ID: bookID}
	}
	g := &servedGuard{store: store, book: book, verID: verID, bookExists: !missing, exact: map[string]struct{}{}, others: map[string]*otherBook{}}
	if missing || bookIsTrashed(book) {
		g.skipOwn = true
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
		g.served = append(g.served, p)
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

// requireServedFileOnDisk refuses, with a *NothingServedError, when the own book
// is live and not one of the paths it serves (its FilePath, the rows of its
// active version) exists on disk. That state is "unknown", not "nothing is
// served": the rules above compare candidates against what the book serves, by
// path and by inode, and with no served file to compare against a stale
// FilePath or a lost file leaves the guard nothing to protect. A trashed or
// missing book serves nothing, so this does not apply to it.
//
// A served path that is a folder counts only while it holds a regular file
// outside .versions/: an empty folder, or one that holds nothing but version
// slots, serves nothing a purge could be compared against.
//
// It is not a *CannotTellError: that is for files that may be on a drive that
// is down, and a retry can settle it. This one is about the book's own path,
// which only fixing the book (or restoring the version) changes.
func (g *servedGuard) requireServedFileOnDisk() error {
	if g.skipOwn {
		return nil
	}
	var notes []database.PurgeFileNote
	for _, p := range g.served {
		present, why := servedPathPresent(p)
		if present {
			return nil
		}
		notes = append(notes, database.PurgeFileNote{Path: p, Reason: why})
	}
	if len(notes) == 0 {
		notes = append(notes, database.PurgeFileNote{Path: g.book.FilePath, Reason: "the book names no file it serves"})
	}
	return &NothingServedError{
		VersionID: g.verID,
		BookID:    g.book.ID,
		BookPath:  g.book.FilePath,
		Files:     notes,
	}
}

// servedPathPresent reports whether p is a file that exists, or a folder with a
// regular file somewhere in it outside .versions/. When it is not, why says why.
func servedPathPresent(p string) (bool, string) {
	fi, err := os.Stat(p)
	if err != nil {
		return false, err.Error()
	}
	if !fi.IsDir() {
		return true, ""
	}
	// WalkDir does not follow a symlink at the root; the folder may be one.
	root := p
	if r, rerr := filepath.EvalSymlinks(p); rerr == nil {
		root = r
	}
	found := false
	werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable corner is skipped: the walk is looking for any one
			// file, and a folder that is readable elsewhere still serves it.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() && d.Name() == VersionsDirName && path != root {
			return fs.SkipDir
		}
		if d.Type().IsRegular() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if found {
		return true, ""
	}
	if werr != nil {
		return false, "the folder cannot be read: " + werr.Error()
	}
	return false, "the folder holds no file outside " + VersionsDirName + "/"
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

// hasSlotPair reports whether p has the consecutive path parts
// .versions/{verID} (pathutil.HasSlotPair, shared with internal/database).
func hasSlotPair(p, verID string) bool {
	return pathutil.HasSlotPair(p, verID)
}

// inVersionSlot reports whether p lies inside the version's own slot,
// .versions/{verID}/. Both the path as stored and its symlink-resolved form
// must contain the pair: a slot directory that is a symlink out to the book's
// folder is not a slot, and os.Remove on a path through it would delete the real
// file. A .versions directory that is not this version's (a copied library tree
// in an import folder, another version's slot) is not its slot either.
func (g *servedGuard) inVersionSlot(p string) bool {
	return inVersionSlot(p, g.verID)
}

// inVersionSlot is servedGuard.inVersionSlot without a guard: the swap's
// preflight asks the same question of the files it is about to move up.
func inVersionSlot(p, verID string) bool {
	return hasSlotPair(p, verID) && hasSlotPair(canonPath(p), verID)
}

func (g *servedGuard) ownBookServes(p string) bool {
	if g.skipOwn {
		return false
	}
	if !g.inVersionSlot(p) {
		return true
	}
	c := canonPath(p)
	if _, ok := g.exact[c]; ok {
		return true
	}
	if g.folder != "" && pathutil.IsWithin(c, g.folder) && !pathutil.UnderVersionsDir(c, g.folder) {
		return true
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
	for _, v := range pathSpellings(p) {
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
			if _, dead := ob.dead[r.VersionID]; !dead {
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
	// Last, and only for a file nothing above vouches for: a row or FilePath of
	// another live book that is a symlink to it (serve_guard_links.go).
	return g.reachedByOtherBookLink(p)
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
		vers, err := g.store.GetBookVersionsByBookID(bookID)
		if err != nil {
			return nil, fmt.Errorf("list versions of book %s: %w", bookID, err)
		}
		ob.live, ob.dead = true, map[string]struct{}{}
		for i := range vers {
			if st := vers[i].Status; st == database.BookVersionStatusTrash || st == database.BookVersionStatusInactivePurged {
				ob.dead[vers[i].ID] = struct{}{}
			}
		}
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
	if root := config.Snapshot().RootDir; root != "" {
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
