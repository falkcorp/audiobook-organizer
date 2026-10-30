// file: internal/pathutil/protected.go
// version: 1.0.0
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
	"syscall"
)

// maxSymlinkHops bounds link following, so a link cycle is an error.
const maxSymlinkHops = 255

// maxResolveDepth bounds the nested resolution of a relative link's folder.
const maxResolveDepth = 40

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

// evalSymlinksCycle reports whether err is filepath.EvalSymlinks giving up on a
// link cycle. It does so with a plain errors.New("EvalSymlinks: too many links"),
// not with ELOOP, so the message is all there is to match on;
// TestResolveExistingPrefix_LinkCycleIsELOOP fails if a Go release changes it.
func evalSymlinksCycle(err error) bool {
	return err != nil && strings.Contains(err.Error(), "too many links")
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

// UnderProtectedPath is the one test of whether a file path is in a place the
// application must not delete from, move out of, or write into: under any of
// the folders in roots (import paths, the folders of the iTunes library files),
// inside an "iTunes Media" folder, or inside a ".failed/" quarantine folder.
//
// The path is tested in the spelling it is given AND in its symlink-resolved
// spelling, and each root likewise. A file reached through a link that points
// into a protected folder is the same file, and so is any path under a
// protected folder that is itself configured through a link. A path whose
// final element does not exist is resolved through its nearest existing
// parent; a dangling link is followed (ResolveExistingPrefix).
//
// The resolved check only ever ADDS protection. The plain spelling is tested
// first, with no filesystem access, and a hit there is final, so a path that was
// protected before symlinks were considered still is, whatever resolution does.
//
// Fail closed: if the path or a root cannot be resolved for a reason other than
// not-exist (a link cycle, a permission error), protected is true and err says
// what failed. Callers log err; they must not read it as "could not tell".
// Relative paths and roots are made absolute against the working directory, as
// the checks this replaces did, so an empty root means the working directory
// rather than "no root"; callers skip a root that is not configured.
func UnderProtectedPath(p string, roots []string) (protected bool, err error) {
	abs, aerr := filepath.Abs(p)
	if aerr != nil {
		return true, fmt.Errorf("make %q absolute: %w", p, aerr)
	}
	var absRoots []string
	for _, r := range roots {
		ra, rerr := filepath.Abs(r)
		if rerr != nil {
			return true, fmt.Errorf("make protected folder %q absolute: %w", r, rerr)
		}
		absRoots = append(absRoots, ra)
	}

	if protectedBySpelling(abs, absRoots) {
		return true, nil
	}

	resolved, rerr := ResolveExistingPrefix(abs)
	if rerr != nil {
		return true, fmt.Errorf("resolve symlinks in %q: %w", p, rerr)
	}
	resolvedRoots := make([]string, 0, len(absRoots))
	for _, r := range absRoots {
		rr, err := ResolveExistingPrefix(r)
		if err != nil {
			return true, fmt.Errorf("resolve symlinks in protected folder %q: %w", r, err)
		}
		resolvedRoots = append(resolvedRoots, rr)
	}

	// Every pairing of {plain, resolved} path with {plain, resolved} roots; the
	// plain/plain pair already failed above.
	allRoots := append(slices.Clone(absRoots), resolvedRoots...)
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
