// file: internal/audiobooks/purge_root_ready.go
// version: 1.1.0
// guid: 0e7b3d52-9a14-4c68-b2f7-5d1c8a4e6b93
// last-edited: 2026-10-25

package audiobooks

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ErrLibraryRootNotReady is what a purge with delete_files is refused with when
// the library does not look mounted. See libraryRootReady.
var ErrLibraryRootNotReady = errors.New("the library root is not ready")

const (
	// rootSampleFiles is how many live book files libraryRootReady stats.
	rootSampleFiles = 50
	// rootSampleWindow is how many books it reads to find them: a window at a
	// random offset, so a library whose first books happen to be gone is not the
	// only part ever looked at.
	rootSampleWindow = 400
	// rootGuardTTL is how long a pass trusts a successful check before it makes
	// the next (rootGuard).
	rootGuardTTL = 15 * time.Second
)

// libraryRootReady returns nil only when the library looks like it is actually
// there: the root is configured, exists and is a directory, and most of a sample
// of the live book files the database says live under it can be seen.
//
// Why. A purge with delete_files reads "nothing at this path" (ENOENT) as "the
// file is already gone", which is right for a file that was removed, and wrong for
// every file at once when the library is a mount that is not up: a mount point
// that is not mounted is an empty directory, so every path under it is ENOENT.
// A pass would then purge each book with no line, and a replay would clear every
// journal record, and the files, which are all still on the real disk, would have
// nothing naming them for the next library scan to import back as new books.
// The same read of a root that does not exist, or that is not configured, is
// equally "gone".
//
// Why a sample of the database's own files, and not "the root is not empty".
// On production the root is never empty while the application runs: the
// database (.appdata), the activity log, the backups and the cover cache all live
// inside root_dir, the first on a dataset of its own that mounts at a path under
// a library dataset that may not have. A directory that holds the application's
// own folders says nothing about whether the books are there. The books the
// database names are the evidence: up to rootSampleFiles live book_file paths
// under the root, from a random window of books, are stat-ed, and the library is
// ready only when more than half are present. A file that cannot be stat-ed for
// any reason other than "not there" (EIO, ESTALE) counts as not present. A
// library with no live file under the root has nothing to compare against and is
// ready (a purge has nothing live to protect then, and the per-path check of
// planPurgeFile, ENOENT counts as gone only when its folder exists, still
// applies).
//
// Cost: up to rootSampleFiles stats, a window read and one book_file read per
// book examined, per check. A pass checks at its start and then at most every
// rootGuardTTL (rootGuard); the single-book paths check per decision.
//
// A library that has lost most of its live files for real (deleted by hand,
// moved out of the root) is also refused until they are back or their books are
// dealt with: a purge cannot tell that from an unmounted share.
func (svc *AudiobookService) libraryRootReady(root string) error {
	if root == "" {
		return fmt.Errorf("%w: no library root (root_dir) is configured", ErrLibraryRootNotReady)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %w", ErrLibraryRootNotReady, root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrLibraryRootNotReady, root)
	}
	paths, err := svc.sampleLiveFilesUnder(root)
	if err != nil {
		return fmt.Errorf("%w: cannot sample the library's live files to check that it is mounted: %w", ErrLibraryRootNotReady, err)
	}
	if len(paths) == 0 {
		return nil
	}
	present := 0
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			present++
		}
	}
	if present*2 <= len(paths) {
		return fmt.Errorf("%w: only %d of %d sampled live book files under %s can be seen (a library that is not mounted, or only partly, looks like this), so a missing file cannot be told from a missing mount; nothing is removed and no record is cleared until they are back", ErrLibraryRootNotReady, present, len(paths), root)
	}
	return nil
}

// sampleLiveFilesUnder returns up to rootSampleFiles paths of live (not
// trashed, not retired) book_file rows that lie inside root, taken from a window
// of books at a random offset. Each book contributes its first such file, so the
// sample spreads over books. A store read that fails is an error, not an empty
// sample: nothing can be vouched for.
func (svc *AudiobookService) sampleLiveFilesUnder(root string) ([]string, error) {
	total, err := svc.store.CountPrimaryBooks()
	if err != nil {
		return nil, err
	}
	offset := 0
	if total > rootSampleWindow {
		offset = rand.IntN(total - rootSampleWindow + 1)
	}
	cores, err := svc.store.GetAllBooksCore(rootSampleWindow, offset)
	if err != nil {
		return nil, err
	}
	if len(cores) == 0 && offset > 0 {
		if cores, err = svc.store.GetAllBooksCore(rootSampleWindow, 0); err != nil {
			return nil, err
		}
	}
	cleanRoot := filepath.Clean(root)
	var out []string
	for _, c := range cores {
		if len(out) >= rootSampleFiles {
			break
		}
		if (c.MarkedForDeletion != nil && *c.MarkedForDeletion) || (c.LibraryState != nil && *c.LibraryState == "deleted") {
			continue
		}
		files, err := svc.store.GetBookFiles(c.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Retired || f.FilePath == "" {
				continue
			}
			rel, rerr := filepath.Rel(cleanRoot, filepath.Clean(f.FilePath))
			if rerr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			out = append(out, f.FilePath)
			break
		}
	}
	return out, nil
}

// rootGuard is a pass's check of the library root, shared by its workers: a
// successful check is trusted for rootGuardTTL, so a pass of thousands of books
// does not sample the library for each, and a mount lost in the middle of a pass
// is noticed within that time (the per-path check of planPurgeFile, which reads a
// missing file as gone only when its folder exists, covers the gap). A failed
// check is never cached.
type rootGuard struct {
	svc  *AudiobookService
	root string
	now  func() time.Time

	mu   sync.Mutex
	okAt time.Time
}

// newRootGuard returns a guard whose first check is the one just made (okAt).
func newRootGuard(svc *AudiobookService, root string) *rootGuard {
	return &rootGuard{svc: svc, root: root, now: time.Now, okAt: time.Now()}
}

func (g *rootGuard) ready() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.okAt.IsZero() && g.now().Sub(g.okAt) < rootGuardTTL {
		return nil
	}
	if err := g.svc.libraryRootReady(g.root); err != nil {
		g.okAt = time.Time{}
		return err
	}
	g.okAt = g.now()
	return nil
}

// rootCheck is the library-root check for one decision: the pass's guard
// (cached for rootGuardTTL) when the decision is part of a pass, a fresh check
// otherwise.
func (svc *AudiobookService) rootCheck(env purgeEnv) error {
	if env.root != nil {
		return env.root.ready()
	}
	return svc.libraryRootReady(config.Snapshot().RootDir)
}

// folderIsThere says whether the folder a missing file would be in exists. A
// file's ENOENT is "already gone" only when it does: with the folder missing too
// the file may be on a library that is not mounted (nested datasets mount
// separately, and a root can look populated without them), so the answer is
// "cannot tell". ok is true when the folder is a directory; a lookup that fails
// for any other reason than "not there" is an error.
func folderIsThere(path string) (ok bool, err error) {
	info, err := os.Stat(filepath.Dir(path))
	switch {
	case err == nil:
		return info.IsDir(), nil
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return false, nil
	}
	return false, err
}
