// file: internal/versions/serve_guard_links.go
// version: 1.0.0
// guid: 084c9637-0527-4749-af27-b59f723f52ef
// last-edited: 2026-10-30

package versions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The link index: who else reaches a file through a symlink.
//
// otherBookServes asks the stores which rows name a path, and the stores match
// path strings exactly. A book organized with the "symlink" strategy has a
// book_file row that names the LINK, so the question "does another book serve
// /lib/A/.versions/v1/x.m4b?" never meets a row whose FilePath is
// /lib/B/x.m4b, a link that resolves to it, and a purge deleted the file the
// link plays. (The same hole exists for a Book.FilePath that is a link, and
// for a folder FilePath that is a link to a folder.)
//
// linkIndex closes it from the other side: every path another live book names
// (its non-retired book_file rows and its FilePath) is resolved once and keyed
// by what it resolves to, link hops included. A purge target is then looked up
// by its own canonical spellings.
//
// Why every row and not only the rows marked OrganizeMethod "symlink": nothing
// writes that field today, and a link also arrives through a scan, an import, a
// hand-made link or a repoint (repoint_book_file_rows accepts a link). A
// narrower scope could only be justified by a field that is not reliably set,
// so the scope is the whole set of rows other books hold.
//
// Cost. It is O(rows) once per guard, built lazily by the first path that
// reaches the other-books rules, and the rebuilt guard (rebuildServedGuard,
// after the claim) builds its own, so a link made while the version was being
// claimed is seen. One os.Lstat per row, one EvalSymlinks per distinct folder
// (cached), run by a worker pool of runtime.NumCPU() goroutines over chunks of
// rows. The workers only touch the filesystem and their own result slice; the
// index is merged serially afterwards, and the liveness questions that use
// servedGuard.others (a plain map) are asked serially at lookup time.
//
// Fail closed. A row whose path cannot be resolved for any reason except "it is
// not there" (a link loop, a folder that cannot be read, a dataset that is
// down) makes the whole index fail, and the purge deletes nothing: a path that
// cannot be resolved might be the link that plays the file. The price is that
// one stray unreadable path anywhere in the library holds every purge until it
// is fixed; the alternative, skipping the row, is exactly the silent fail-open
// this index exists to remove. A path that is not there (fs.ErrNotExist,
// ENOTDIR) points at nothing right now and is skipped.
type linkIndex map[string][]linkOwner

// errSymlinkLoop is wrapped by the error for a chain of links that does not end.
var errSymlinkLoop = errors.New("symlink loop")

// linkOwner is one path another live book names that resolves to a key.
type linkOwner struct {
	bookID    string
	versionID string // of the book_file row; "" for a book's FilePath
	// bookPath is true for Book.FilePath: it serves what it resolves to AND,
	// when that is a folder, every file below it (outside a .versions/ slot).
	bookPath bool
	// named is the path as the store holds it, for the log.
	named string
}

// linkCandidate is a path to resolve.
type linkCandidate struct {
	path string
	own  linkOwner
}

// linkChunk is how many candidates one worker takes at a time. A variable so a
// test can make a small library span several chunks.
var linkChunk = 256

// maxLinkHops bounds a chain of links; a longer one is treated as a loop, like
// the kernel's own limit.
const maxLinkHops = 40

// buildLinkIndex indexes every non-retired book_file row and every FilePath of
// the live books other than ownBookID. store must provide
// database.BookCompletenessReader (resolved with AsCapability): the Complete
// getters are the ones documented for a caller that reads the ABSENCE of a row
// as permission to destroy, which is the purge's case.
func buildLinkIndex(store any, ownBookID string) (linkIndex, error) {
	rd, ok := database.AsCapability[database.BookCompletenessReader](store)
	if !ok {
		return nil, fmt.Errorf("store %T does not provide the complete-library readers (database.BookCompletenessReader) the symlink check needs", store)
	}
	files, err := rd.GetAllBookFilesCoreComplete()
	if err != nil {
		return nil, fmt.Errorf("list every book file for the symlink check: %w", err)
	}
	books, err := rd.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list every book for the symlink check: %w", err)
	}
	cands := make([]linkCandidate, 0, len(files)+len(books))
	for i := range files {
		f := &files[i]
		if f.Retired || f.FilePath == "" || f.BookID == ownBookID {
			continue
		}
		cands = append(cands, linkCandidate{path: f.FilePath, own: linkOwner{bookID: f.BookID, versionID: f.VersionID, named: f.FilePath}})
	}
	for i := range books {
		b := &books[i]
		if b.FilePath == "" || b.ID == ownBookID {
			continue
		}
		cands = append(cands, linkCandidate{path: b.FilePath, own: linkOwner{bookID: b.ID, bookPath: true, named: b.FilePath}})
	}
	files, books = nil, nil // the rows are only needed for their paths

	type hit struct {
		key string
		own linkOwner
	}
	chunks := (len(cands) + linkChunk - 1) / linkChunk
	results := make([][]hit, chunks)
	res := &linkResolver{dirs: map[string]resolvedDir{}}

	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(runtime.NumCPU())
	for c := 0; c < chunks; c++ {
		c := c
		g.Go(func() error {
			lo, hi := c*linkChunk, min((c+1)*linkChunk, len(cands))
			var out []hit
			for _, cand := range cands[lo:hi] {
				if err := ctx.Err(); err != nil {
					return err
				}
				keys, err := res.resolve(cand.path)
				if err != nil {
					return fmt.Errorf("book %s names %s: %w", cand.own.bookID, cand.path, err)
				}
				for _, k := range keys {
					out = append(out, hit{k, cand.own})
				}
			}
			// Each chunk owns its slot: no two workers write the same element.
			results[c] = out
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	idx := linkIndex{}
	for _, r := range results {
		for _, h := range r {
			idx[h.key] = append(idx[h.key], h.own)
		}
	}
	return idx, nil
}

// resolvedDir is the cached answer for one folder.
type resolvedDir struct {
	real string
	gone bool
}

// linkResolver resolves paths one hop at a time so the links on the way are
// kept. Its folder cache is shared by the workers.
type linkResolver struct {
	mu   sync.Mutex
	dirs map[string]resolvedDir
}

// notThere reports an error that means the path points at nothing right now.
func notThere(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// realDir is filepath.EvalSymlinks of a folder, cached. gone reports a folder
// that does not exist.
func (r *linkResolver) realDir(dir string) (string, bool, error) {
	r.mu.Lock()
	c, ok := r.dirs[dir]
	r.mu.Unlock()
	if ok {
		return c.real, c.gone, nil
	}
	real, err := filepath.EvalSymlinks(dir)
	switch {
	case err == nil:
		c = resolvedDir{real: real}
	case notThere(err):
		c = resolvedDir{gone: true}
	default:
		return "", false, fmt.Errorf("resolve folder %s: %w", dir, err)
	}
	r.mu.Lock()
	r.dirs[dir] = c
	r.mu.Unlock()
	return c.real, c.gone, nil
}

// resolve returns the keys path is reached under: its own spelling with the
// folders resolved, then each link it passes through, ending with what the last
// link points at. A removed middle link breaks the links before it, so every
// hop is a key, not only the end. It returns nothing when path names itself
// (no link, no linked folder: the stores' exact-path questions cover it) or
// points at nothing.
func (r *linkResolver) resolve(path string) ([]string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make %s absolute: %w", path, err)
	}
	var keys []string
	cur := abs
	for hops := 0; ; hops++ {
		if hops > maxLinkHops {
			return nil, fmt.Errorf("%w (more than %d links) from %s", errSymlinkLoop, maxLinkHops, path)
		}
		dir, gone, err := r.realDir(filepath.Dir(cur))
		if err != nil {
			return nil, err
		}
		if gone {
			return keys, nil
		}
		leaf := filepath.Join(dir, filepath.Base(cur))
		fi, err := os.Lstat(leaf)
		if err != nil {
			if notThere(err) {
				return keys, nil
			}
			return nil, fmt.Errorf("stat %s: %w", leaf, err)
		}
		keys = append(keys, leaf)
		if fi.Mode()&fs.ModeSymlink == 0 {
			break
		}
		tgt, err := os.Readlink(leaf)
		if err != nil {
			return nil, fmt.Errorf("read link %s: %w", leaf, err)
		}
		if !filepath.IsAbs(tgt) {
			tgt = filepath.Join(filepath.Dir(leaf), tgt)
		}
		cur = filepath.Clean(tgt)
	}
	if len(keys) == 1 && keys[0] == filepath.Clean(path) {
		return nil, nil // names itself
	}
	return keys, nil
}

// linkIdx returns the guard's link index, building it on first use.
func (g *servedGuard) linkIdx() (linkIndex, error) {
	g.linkOnce.Do(func() {
		g.link, g.linkErr = buildLinkIndex(g.store, g.book.ID)
		if g.linkErr != nil {
			lifecycleLog.Warn("purge guard: cannot tell which files other books reach through a symlink, so no file of version %s will be deleted: %v", g.verID, g.linkErr)
		}
	})
	return g.link, g.linkErr
}

// reachedByOtherBookLink reports whether a live book other than the version's
// own names p through a link: a book_file row (of a version that is not
// trashed or purged) or a FilePath that resolves to p or, for a FilePath, to a
// folder above p that is not reached through a .versions/ slot. The keys are
// the spellings a removal of p would break: p's folder resolved with the leaf
// kept (removing p unlinks p itself), and p fully resolved.
func (g *servedGuard) reachedByOtherBookLink(p string) (bool, error) {
	idx, err := g.linkIdx()
	if err != nil {
		return false, err
	}
	if len(idx) == 0 {
		return false, nil
	}
	for _, key := range []string{targetKey(p), canonPath(p)} {
		for i, k := range pathAndAncestors(key) {
			for _, o := range idx[k] {
				// Only a book's FilePath serves what lies below it.
				if i > 0 && !o.bookPath {
					continue
				}
				ob, err := g.other(o.bookID)
				if err != nil {
					return false, err
				}
				if !ob.live {
					continue
				}
				if _, dead := ob.dead[o.versionID]; o.versionID != "" && dead {
					continue
				}
				return true, nil
			}
		}
	}
	return false, nil
}
