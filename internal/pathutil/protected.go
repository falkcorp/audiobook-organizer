// file: internal/pathutil/protected.go
// version: 1.2.0
// guid: f82848e0-20ca-446c-b8c5-b0e2f209bd51
// last-edited: 2026-10-29

package pathutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrProtectedFolderUnresolved marks an UnderProtectedPath failure that is in a
// protected folder (a configured iTunes or import folder), not in the path being
// asked about. Every path would fail the same way until the configuration is
// fixed, so a caller must not blame the path for it.
var ErrProtectedFolderUnresolved = errors.New("a protected folder cannot be resolved")

// maxSymlinkHops bounds the dangling links ResolveExistingPrefix follows by hand,
// so a chain of them is an error. filepath.EvalSymlinks stops at 255 links
// itself, so on a real filesystem this is a second line of defence that a test
// reaches by lowering it. A variable for that reason only.
var maxSymlinkHops = 255

// maxResolveDepth bounds the nested resolution of a relative link's folder; same
// reasoning and same reason for being a variable.
var maxResolveDepth = 40

// ResolveExistingPrefix is filepath.EvalSymlinks for a path that may not
// exist: it resolves the longest existing prefix and re-appends the rest.
// A DANGLING symlink is followed with os.Readlink (a relative target resolves
// against the link's directory), because EvalSymlinks reports only "not
// exist" for it -- a dangling link into a protected tree would otherwise
// resolve to itself and pass. A resolve error other than not-exist (a link
// cycle, a permission error) is returned, and a guard built on this must
// refuse on it. A link cycle, or links nested too deep, is reported as
// syscall.ELOOP (errors.Is), so a caller can tell a path that will never resolve
// from a filesystem that failed to answer.
func ResolveExistingPrefix(p string) (string, error) {
	return resolveExistingPrefixDepth(p, 0)
}

// evalSymlinksCycleMsg is what filepath.EvalSymlinks says when it gives up on a
// link cycle (path/filepath/symlink.go, go1.27.2: errors.New with exactly this
// text, no path in it).
const evalSymlinksCycleMsg = "EvalSymlinks: too many links"

// evalSymlinksCycle reports whether err is filepath.EvalSymlinks giving up on a
// link cycle. It does so with a plain errors.New, not with ELOOP, so its text is
// all there is to match on, and the match is on the WHOLE text: any other error
// (an lstat failure, say) carries the path, and a folder called "too many links"
// must not turn a permission error into a link cycle.
// TestResolveExistingPrefix_LinkCycleIsELOOP fails if a Go release rewords it.
func evalSymlinksCycle(err error) bool {
	return err != nil && err.Error() == evalSymlinksCycleMsg
}

func resolveExistingPrefixDepth(p string, depth int) (string, error) {
	if depth > maxResolveDepth {
		return "", fmt.Errorf("symlink nesting deeper than %d resolving %s: %w", maxResolveDepth, p, syscall.ELOOP)
	}
	var tail []string
	cur := p
	hops := 0
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for _, t := range slices.Backward(tail) {
				r = filepath.Join(r, t)
			}
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			if evalSymlinksCycle(err) {
				return "", fmt.Errorf("%w: %w", err, syscall.ELOOP)
			}
			return "", err
		}
		if fi, lerr := os.Lstat(cur); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxSymlinkHops {
				return "", fmt.Errorf("more than %d symlinks resolving %s: %w", maxSymlinkHops, p, syscall.ELOOP)
			}
			target, rerr := os.Readlink(cur)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(target) {
				// A relative target is relative to where the link really
				// lives. Its folder may itself be reached through a symlink,
				// so resolve the folder before joining: textually, "../b.m4b"
				// from /lib/d/a.m4b is /lib/b.m4b even when /lib/d is a link
				// into a protected tree.
				dir, derr := resolveExistingPrefixDepth(filepath.Dir(cur), depth+1)
				if derr != nil {
					return "", derr
				}
				target = filepath.Join(dir, target)
			}
			cur = filepath.Clean(target)
			continue
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			for _, t := range slices.Backward(tail) {
				cur = filepath.Join(cur, t)
			}
			return cur, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// protectedRootCacheTTL is how long a resolved set of protected folders is
// reused. The folders are configuration and the links in front of them change
// rarely, and the callers run in whole-library loops, so resolving every folder
// for every path was a stat walk per path per folder. Not a const so tests can
// shrink it. A root that failed to resolve is cached too, as a failure, so it
// stays fail-closed for the whole window and is not re-walked per call.
var protectedRootCacheTTL = 5 * time.Second

// RootError is an UnderProtectedPath failure in a protected folder: the folder
// could not be resolved, so no path can be shown to be outside it. It matches
// ErrProtectedFolderUnresolved and, through Err, the underlying cause (ELOOP for
// a link cycle). Root is the folder as it was configured (the spelling in the
// list passed to UnderProtectedPath), whichever step failed: that is the only
// spelling that exists when the folder cannot even be made absolute, and it is
// the key reportedRootFailures uses, so a failure is cleared by the same name it
// was recorded under.
type RootError struct {
	Root string
	Err  error
}

func (e *RootError) Error() string {
	return fmt.Sprintf("%v: resolve symlinks in %q: %v", ErrProtectedFolderUnresolved, e.Root, e.Err)
}

func (e *RootError) Unwrap() []error { return []error{ErrProtectedFolderUnresolved, e.Err} }

// rootFailureRelogInterval is how long a protected folder that keeps failing to
// resolve stays unreported after a report. Not a const so tests can shrink it.
var rootFailureRelogInterval = time.Hour

// clock is the time source of the failure log throttle, replaceable by tests.
var clock = time.Now

// absPath makes a protected folder absolute; replaceable by tests, because
// filepath.Abs fails only when the working directory is gone.
var absPath = filepath.Abs

// reportedRootFailures holds, for each folder whose failure has been reported,
// when it was last reported. A folder that resolves again is removed.
var (
	reportedRootFailuresMu sync.Mutex
	reportedRootFailures   = map[string]time.Time{}
)

// ShouldLogProtectedPathError says whether a caller of UnderProtectedPath should
// log err. A failure in a protected folder is the same for every path asked
// about, so it is reported once per folder, then again at most once per
// rootFailureRelogInterval while the folder keeps failing (a process that runs
// for weeks must not go silent about a folder that is still broken), and at once
// when it fails again after having resolved; any other failure is about one path
// and is always reported.
func ShouldLogProtectedPathError(err error) bool {
	var re *RootError
	if errors.As(err, &re) {
		reportedRootFailuresMu.Lock()
		defer reportedRootFailuresMu.Unlock()
		now := clock()
		if last, seen := reportedRootFailures[re.Root]; seen && now.Sub(last) < rootFailureRelogInterval {
			return false
		}
		reportedRootFailures[re.Root] = now
		return true
	}
	return true
}

// clearRootFailure forgets that root's failure was reported.
func clearRootFailure(root string) {
	reportedRootFailuresMu.Lock()
	delete(reportedRootFailures, root)
	reportedRootFailuresMu.Unlock()
}

// protectedRoots is one list of protected folders, made absolute when it is
// created and resolved through symlinks the first time a path needs it. It is
// read-only after that, so any number of goroutines share it.
type protectedRoots struct {
	raw     []string // the folders as configured, parallel to abs
	abs     []string
	absErr  error // a folder that could not be made absolute
	at      time.Time
	once    sync.Once
	resolve []string
	resErr  error // the first folder that could not be resolved
}

var (
	protectedRootCacheMu sync.Mutex
	protectedRootCache   = map[string]*protectedRoots{}
)

// newProtectedRoots makes roots absolute. It touches no cache, so a caller that
// needs the folders as they are on disk right now builds its own.
func newProtectedRoots(roots []string, at time.Time) *protectedRoots {
	pr := &protectedRoots{at: at, raw: roots}
	for _, r := range roots {
		ra, err := absPath(r)
		if err != nil {
			pr.absErr = &RootError{Root: r, Err: fmt.Errorf("make absolute: %w", err)}
			break
		}
		pr.abs = append(pr.abs, ra)
	}
	return pr
}

// protectedRootsFor returns the shared protectedRoots for roots, creating it (and
// dropping expired ones) when there is no fresh one.
func protectedRootsFor(roots []string) *protectedRoots {
	key := strings.Join(roots, "\x00")
	now := time.Now()
	protectedRootCacheMu.Lock()
	defer protectedRootCacheMu.Unlock()
	if pr := protectedRootCache[key]; pr != nil && now.Sub(pr.at) < protectedRootCacheTTL {
		return pr
	}
	for k, pr := range protectedRootCache {
		if now.Sub(pr.at) >= protectedRootCacheTTL {
			delete(protectedRootCache, k)
		}
	}
	pr := newProtectedRoots(slices.Clone(roots), now)
	protectedRootCache[key] = pr
	return pr
}

// resolved returns the symlink-resolved folders, resolving them on the first
// call only. On a failure it returns the *RootError of the first folder that
// could not be resolved.
func (pr *protectedRoots) resolved() ([]string, error) {
	pr.once.Do(func() {
		out := make([]string, 0, len(pr.abs))
		for i, r := range pr.abs {
			rr, err := ResolveExistingPrefix(r)
			if err != nil {
				pr.resErr = &RootError{Root: pr.raw[i], Err: err}
				return
			}
			clearRootFailure(pr.raw[i])
			out = append(out, rr)
		}
		pr.resolve = out
	})
	return pr.resolve, pr.resErr
}

// UnderProtectedPath tests whether a file path is in a place the application
// must not delete from, move out of, or write into: under any of the folders in
// roots (import paths, the folders of the iTunes library files), inside an
// "iTunes Media" folder, or inside a ".failed/" quarantine folder. It is the
// shared test for the purge, the organizer and the metadata write-back. It is
// NOT the only protected-path check: deluge.ProtectedPathCache is a separate
// check the callers consult alongside it, and it still compares paths as
// spelled. It holds both the Deluge seeding-folder list and
// config.ProtectedPaths (the statically configured protected prefixes), so a
// static prefix configured through a symlink, or a file reached through a link
// into a Deluge or static folder, is not seen by it.
//
// The path is tested in the spelling it is given AND in its symlink-resolved
// spelling, and each root likewise. A file reached through a link that points
// into a protected folder is the same file, and so is any path under a
// protected folder that is itself configured through a link. A path whose
// final element does not exist is resolved through its nearest existing
// parent; a dangling link is followed (ResolveExistingPrefix).
//
// The resolved check only ever ADDS protection. The plain spelling is tested
// first, with no filesystem access beyond the working directory, and a hit there
// is final, so a path that was protected before symlinks were considered still
// is, whatever resolution does.
//
// The folders are made absolute and resolved once per protectedRootCacheTTL for
// a given list, not once per call; resolving the path itself is per call. A
// link that is re-pointed inside that window is therefore not seen until it
// ends, which can only make this answer protect LESS than a fresh look, so a
// caller that deletes uses UnderProtectedPathUncached instead.
//
// Fail closed: if the path or a root cannot be resolved for a reason other than
// not-exist (a link cycle, a permission error), protected is true and err says
// what failed (a *RootError, errors.Is ErrProtectedFolderUnresolved, when it was
// a folder rather than the path). Callers log err with
// ShouldLogProtectedPathError; they must not read it as "could not tell".
// Relative paths and roots are made absolute against the working directory, as
// the checks this replaces did, so an empty root means the working directory
// rather than "no root"; callers skip a root that is not configured.
func UnderProtectedPath(p string, roots []string) (protected bool, err error) {
	return underProtectedPath(p, protectedRootsFor(roots))
}

// UnderProtectedPathUncached is UnderProtectedPath with the folders made
// absolute and resolved afresh for this call, and nothing kept. It is for a
// caller that deletes: UnderProtectedPath can use folders resolved up to
// protectedRootCacheTTL ago, so a link re-pointed into (or out of) a protected
// folder in that window changes the answer of a fresh look but not of the
// cached one, and for a deletion a stale "not protected" is the one wrong answer
// that loses a file. The price is a stat walk per folder per call. A failure in
// a folder is reported and throttled exactly as in UnderProtectedPath.
func UnderProtectedPathUncached(p string, roots []string) (protected bool, err error) {
	return underProtectedPath(p, newProtectedRoots(slices.Clone(roots), time.Now()))
}

func underProtectedPath(p string, set *protectedRoots) (protected bool, err error) {
	abs, aerr := filepath.Abs(p)
	if aerr != nil {
		return true, fmt.Errorf("make %q absolute: %w", p, aerr)
	}
	if set.absErr != nil {
		return true, set.absErr
	}

	if protectedBySpelling(abs, set.abs) {
		return true, nil
	}

	resolved, rerr := ResolveExistingPrefix(abs)
	if rerr != nil {
		return true, fmt.Errorf("resolve symlinks in %q: %w", p, rerr)
	}
	resolvedRoots, rootErr := set.resolved()
	if rootErr != nil {
		return true, rootErr
	}

	// Every pairing of {plain, resolved} path with {plain, resolved} roots; the
	// plain/plain pair already failed above.
	allRoots := append(slices.Clone(set.abs), resolvedRoots...)
	return protectedBySpelling(resolved, allRoots) || protectedBySpelling(abs, resolvedRoots), nil
}

// protectedBySpelling reports whether one spelling of a path is under any of
// roots or inside an iTunes Media / .failed folder. The substring rules are the
// long-standing ones: "iTunes Media" anywhere in the path, and a ".failed"
// folder with something in it.
func protectedBySpelling(p string, roots []string) bool {
	for _, r := range roots {
		if IsWithin(p, r) {
			return true
		}
	}
	if strings.Contains(p, "iTunes Media") || strings.Contains(p, "iTunes%20Media") {
		return true
	}
	return strings.Contains(filepath.ToSlash(p), "/.failed/")
}
