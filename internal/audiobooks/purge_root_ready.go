// file: internal/audiobooks/purge_root_ready.go
// version: 1.4.0
// guid: 0e7b3d52-9a14-4c68-b2f7-5d1c8a4e6b93
// last-edited: 2026-10-26

package audiobooks

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrLibraryRootNotReady is what a purge with delete_files is refused with when
// the library does not look mounted. See libraryRootReady.
var ErrLibraryRootNotReady = errors.New("the library root is not ready")

// rootSampleFiles is how many live book files libraryRootReady stats.
const rootSampleFiles = 50

// rootGuardTTL is how long a pass trusts a successful check before it makes the
// next (rootGuard). A variable so a test can make every decision check again.
var rootGuardTTL = 15 * time.Second

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
// not answering. While stats from an earlier timed-out check are still blocked
// (rootStatsHung) no new check starts and the answer is "not ready", so a hung
// mount costs one check's threads, however often it is asked.
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
	if n := rootStatsHung.Load(); n > 0 {
		return fmt.Errorf("%w: %d stat(s) of the library from an earlier check have still not answered (a hung mount?), so it is not asked again until they do", ErrLibraryRootNotReady, n)
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

// rootStatsHung counts the root-check stats that did not answer within
// rootStatTimeout and have not answered since: goroutines still blocked in the
// kernel on a mount that is not answering. Each holds an OS thread for as long as
// it is blocked, and Go ends the process at 10,000 threads, so a hung mount must
// cost one check's worth of them in total, not one per check: libraryRootReady
// refuses at once while this is above zero and starts no new stat.
var rootStatsHung atomic.Int64

// statCall is the bookkeeping of one statAll call, so that a stat answering
// after the call gave up takes itself off rootStatsHung exactly once.
type statCall struct {
	mu       sync.Mutex
	timedOut bool
	pending  int
}

func (c *statCall) answered() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending--
	if c.timedOut {
		rootStatsHung.Add(-1)
	}
}

func (c *statCall) giveUp() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timedOut = true
	rootStatsHung.Add(int64(c.pending))
}

// statAll stats every path at once (at most rootSampleFiles+1 of them) and
// returns when all have answered or d has passed, whichever is first. A path
// that has not answered by then is an error for the whole call: the check cannot
// vouch for a library whose stats hang. The stat goroutines write to their own
// slot of a buffered channel's results, so a stat that answers after the
// deadline is dropped, and none of them holds a lock. The ones still blocked at
// the deadline are counted in rootStatsHung until they return.
func statAll(paths []string, d time.Duration) ([]statResult, error) {
	type answer struct {
		i int
		r statResult
	}
	ch := make(chan answer, len(paths))
	call := &statCall{pending: len(paths)}
	for i, p := range paths {
		go func() {
			info, err := rootStat(p)
			call.answered()
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
			call.giveUp()
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

// purgeFileMode is what a single-book purge decides about file deletion BEFORE
// it takes the global merge lock, and carries into it: whether files are
// deleted at all (the setting, read once, so the decision about the file, the
// delete and the removal are all about the same value) and, when they are, the
// guard that holds the library-root check made outside the lock. The zero value
// (taken false) means nothing was decided outside the lock: the caller reads the
// setting itself and the decisions under the lock check the root themselves.
type purgeFileMode struct {
	taken       bool
	deleteFiles bool
	root        *rootGuard
}

// preLockRoot reads the delete-files setting once and, when it is on, makes the
// library-root check for a single-book purge BEFORE the action takes the global
// merge lock, returning the guard that carries the answer into it: the decisions
// made under the lock then reuse the check (within rootGuardTTL) and do no
// sampling or stat while the lock is held. With delete_files off nothing is
// decided about a file and no check is made.
func (svc *AudiobookService) preLockRoot() (purgeFileMode, error) {
	mode := purgeFileMode{taken: true, deleteFiles: config.Snapshot().PurgeSoftDeletedDeleteFiles}
	if !mode.deleteFiles {
		return mode, nil
	}
	root := config.Snapshot().RootDir
	at := time.Now()
	if err := svc.libraryRootReady(root); err != nil {
		return purgeFileMode{}, err
	}
	mode.root = newRootGuard(svc, root, at)
	return mode, nil
}

// settle returns mode as it is to be used under the lock: as decided before it
// when that was done, else the setting read now with no guard (the decisions then
// check the root themselves, under the lock).
func (m purgeFileMode) settle() purgeFileMode {
	if m.taken {
		return m
	}
	return purgeFileMode{taken: true, deleteFiles: config.Snapshot().PurgeSoftDeletedDeleteFiles}
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

// dirSet is a set of folders shared by the workers of one removal run: the
// folders a worker is about to unlink a file in. Safe for concurrent use; a nil
// set is empty and ignores adds.
type dirSet struct {
	mu   sync.Mutex
	dirs map[string]struct{}
}

func (s *dirSet) add(dir string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirs == nil {
		s.dirs = map[string]struct{}{}
	}
	s.dirs[filepath.Clean(dir)] = struct{}{}
}

func (s *dirSet) has(dir string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.dirs[filepath.Clean(dir)]
	return ok
}

// folderCheck is what vouches for a missing file's ENOENT: its folder must be
// there (folderIsThere) and must not be an EMPTY folder that nothing explains. A
// dataset that is not mounted leaves its mount point behind as an empty
// directory, so a file directly in it reads ENOENT with its folder "there"; an
// empty folder is therefore "cannot tell" too, unless
//
//   - the folder is the library root itself (the root check, libraryRootReady,
//     is what vouches for it: an empty root is a mount point that is not up and
//     is refused there), or
//   - this run is itself unlinking in it (emptied: the worker registers a folder
//     before its first unlink there), which is how a folder comes to be empty
//     between the first worker finishing and a neighbour's already-gone file
//     being decided.
//
// missing: the folder is not there. empty: it is there, holds nothing, and
// nothing explains that.
func folderCheck(path, root string, emptied *dirSet) (missing, empty bool, err error) {
	ok, err := folderIsThere(path)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return true, false, nil
	}
	dir := filepath.Dir(path)
	if root != "" && filepath.Clean(dir) == filepath.Clean(root) {
		return false, false, nil
	}
	f, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return true, false, nil
		}
		return false, false, err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil {
		if errors.Is(err, io.EOF) {
			// Asked only AFTER the folder was seen empty: a worker registers a
			// folder before its unlink, so a folder that this run emptied is
			// already in the set by the time it can be seen empty. Asking before
			// reading would race with that worker (set checked, then its unlink,
			// then the read).
			return false, !emptied.has(dir), nil
		}
		return false, false, err
	}
	return false, false, nil
}
