// file: internal/audiobooks/purge_root_ready.go
// version: 1.2.0
// guid: 0e7b3d52-9a14-4c68-b2f7-5d1c8a4e6b93
// last-edited: 2026-10-26

package audiobooks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrLibraryRootNotReady is what a purge with delete_files is refused with when
// the library does not look mounted. See libraryRootReady.
var ErrLibraryRootNotReady = errors.New("the library root is not ready")

const (
	// rootSampleFiles is how many live book files libraryRootReady stats.
	rootSampleFiles = 50
	// rootGuardTTL is how long a pass trusts a successful check before it makes
	// the next (rootGuard).
	rootGuardTTL = 15 * time.Second
)

// rootStatTimeout is how long the root check waits for its stats. A variable so
// a test can shorten it.
var rootStatTimeout = 10 * time.Second

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
// under the root, drawn from a few random places in the library and one per
// book (database.LiveBookFileSampler), are stat-ed, and the library is ready
// only when more than half are present. A file that cannot be stat-ed for any
// reason other than "not there" (EIO, ESTALE) counts as not present, and a stat
// that does not answer within rootStatTimeout (a hung network mount) makes the
// root not ready: the check fails closed rather than wait for a mount that is
// not answering.
//
// The sample is short of rootSampleFiles only when it is every live file under
// the root, so there is no minimum to apply: one file is evidence when it is the
// only one. A library with no live file under the root at all has nothing to
// compare against and is ready (a purge has nothing live to protect then, and
// the per-path check of planPurgeFile, ENOENT counts as gone only when its folder
// is there, still applies). A store that cannot sample (memdb not serving, or a
// store without the capability) is not ready.
//
// Cost: the sampler visits rows until it has rootSampleFiles (it does not copy
// the library), then up to rootSampleFiles stats at once. A pass checks at its
// start and then at most every rootGuardTTL (rootGuard).
//
// A library that has lost most of its live files for real (deleted by hand,
// moved out of the root) is also refused until they are back or their books are
// dealt with: a purge cannot tell that from an unmounted share.
func (svc *AudiobookService) libraryRootReady(root string) error {
	if root == "" {
		return fmt.Errorf("%w: no library root (root_dir) is configured", ErrLibraryRootNotReady)
	}
	sampler, ok := database.AsCapability[database.LiveBookFileSampler](svc.store)
	if !ok {
		return fmt.Errorf("%w: store %T cannot sample the library's live files (database.LiveBookFileSampler) to check that it is mounted", ErrLibraryRootNotReady, svc.store)
	}
	paths, err := sampler.SampleLiveBookFilePathsUnder(root, rootSampleFiles)
	if err != nil {
		return fmt.Errorf("%w: cannot sample the library's live files to check that it is mounted: %w", ErrLibraryRootNotReady, err)
	}
	// The root first, then the sample, answered together: one wait for all of them.
	probe := append([]string{root}, paths...)
	found, err := statAll(probe, rootStatTimeout)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLibraryRootNotReady, err)
	}
	if found[0].err != nil {
		return fmt.Errorf("%w: cannot read %s: %w", ErrLibraryRootNotReady, root, found[0].err)
	}
	if !found[0].dir {
		return fmt.Errorf("%w: %s is not a directory", ErrLibraryRootNotReady, root)
	}
	if len(paths) == 0 {
		return nil
	}
	present := 0
	for _, f := range found[1:] {
		if f.err == nil {
			present++
		}
	}
	if present*2 <= len(paths) {
		return fmt.Errorf("%w: only %d of %d sampled live book files under %s can be seen (a library that is not mounted, or only partly, looks like this), so a missing file cannot be told from a missing mount; nothing is removed and no record is cleared until they are back", ErrLibraryRootNotReady, present, len(paths), root)
	}
	return nil
}

// statResult is one path's stat.
type statResult struct {
	err error
	dir bool
}

// rootStat is the stat the root check uses, os.Stat as the purge's own decisions
// use it (a symlink into a mount that is not up is not "present"). A variable so
// a test can make one hang.
var rootStat = os.Stat

// statAll stats every path at once (at most rootSampleFiles+1 of them) and
// returns when all have answered or d has passed, whichever is first. A path
// that has not answered by then is an error for the whole call: the check cannot
// vouch for a library whose stats hang. The stat goroutines write to their own
// slot of a buffered channel's results, so a stat that answers after the
// deadline is dropped, and none of them holds a lock.
func statAll(paths []string, d time.Duration) ([]statResult, error) {
	type answer struct {
		i int
		r statResult
	}
	ch := make(chan answer, len(paths))
	for i, p := range paths {
		go func() {
			info, err := rootStat(p)
			ch <- answer{i, statResult{err: err, dir: err == nil && info.IsDir()}}
		}()
	}
	out := make([]statResult, len(paths))
	timer := time.NewTimer(d)
	defer timer.Stop()
	for range paths {
		select {
		case a := <-ch:
			out[a.i] = a.r
		case <-timer.C:
			return nil, fmt.Errorf("the library did not answer a stat within %v (a hung mount?), so it cannot be vouched for", d)
		}
	}
	return out, nil
}

// rootGuard is a pass's check of the library root, shared by its workers: a
// successful check is trusted for rootGuardTTL, so a pass of thousands of books
// does not sample the library for each, and a mount lost in the middle of a pass
// is noticed within that time (the per-path check of planPurgeFile, which reads a
// missing file as gone only when its folder is there, covers the gap). A failed
// check is never cached. Workers that find the check stale at the same moment
// share one check (singleflight) and none holds a lock while it runs, so a slow
// stat parks the workers that need the answer and nothing else.
type rootGuard struct {
	svc  *AudiobookService
	root string
	now  func() time.Time

	mu     sync.Mutex
	okAt   time.Time
	flight singleflight.Group
}

// newRootGuard returns a guard that trusts the check the caller made at
// checkedAt (the zero time: none was made, so the first decision checks).
func newRootGuard(svc *AudiobookService, root string, checkedAt time.Time) *rootGuard {
	return &rootGuard{svc: svc, root: root, now: time.Now, okAt: checkedAt}
}

func (g *rootGuard) fresh() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.okAt.IsZero() && g.now().Sub(g.okAt) < rootGuardTTL
}

func (g *rootGuard) ready() error {
	if g.fresh() {
		return nil
	}
	_, err, _ := g.flight.Do("check", func() (any, error) {
		// Another caller may have finished a check while this one waited to start.
		if g.fresh() {
			return nil, nil
		}
		// The time the check began, not the time it ended: what it saw is as old
		// as its start.
		began := g.now()
		err := g.svc.libraryRootReady(g.root)
		g.mu.Lock()
		if err != nil {
			g.okAt = time.Time{}
		} else {
			g.okAt = began
		}
		g.mu.Unlock()
		return nil, err
	})
	return err
}

// preLockRoot makes the library-root check for a single-book purge BEFORE the
// action takes the global merge lock, and returns the guard that carries the
// answer into it: the decisions made under the lock then reuse the check (within
// rootGuardTTL) and do no sampling or stat while the lock is held. A nil guard
// (delete_files is off: nothing is decided about a file) means no check was made.
func (svc *AudiobookService) preLockRoot() (*rootGuard, error) {
	if !config.Snapshot().PurgeSoftDeletedDeleteFiles {
		return nil, nil
	}
	root := config.Snapshot().RootDir
	at := time.Now()
	if err := svc.libraryRootReady(root); err != nil {
		return nil, err
	}
	return newRootGuard(svc, root, at), nil
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
