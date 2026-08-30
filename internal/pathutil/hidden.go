// file: internal/pathutil/hidden.go
// version: 2.1.0
// guid: 5a2c7e91-4d38-4b06-9c1f-7e0a3b58d264
// last-edited: 2026-08-29

package pathutil

import (
	"path/filepath"
	"strings"
)

// visibleHiddenDirs are dot-directories that library sweeps MUST still descend
// into, despite the general rule below.
//
// The rule "skip anything dot-prefixed" is right for application state the
// library should never see (.backups, .itunes-writeback, .failed). It is wrong
// for content deliberately stored under a dot-name that IS library material.
//
// `.alternates` is that case: alternate versions/rips of a book are real
// content the scanner has to find. The alternates feature is not built yet, and
// this carve-out exists so that turning on the dot rule now does not quietly
// make that content invisible the day it lands. A folder the scanner cannot see
// fails silently -- the books simply never appear, with no error anywhere --
// which is the most expensive kind of bug this codebase keeps rediscovering.
//
// THIS MAP IS THE ONE PLACE to add another carve-out. Adding the check inline
// at a walker instead is how the scanner ended up with a hard-coded `.failed`
// test that the count phase and the watcher never knew about.
var visibleHiddenDirs = map[string]bool{
	".alternates": true,
}

// IsVisibleHiddenDir reports whether a dot-prefixed directory is explicitly
// carved out of the skip rule and must still be walked.
func IsVisibleHiddenDir(name string) bool {
	return visibleHiddenDirs[name]
}

// IsHiddenName reports whether a single path element is dot-prefixed.
//
// "." and ".." are not hidden: they are traversal elements, and treating them
// as hidden would make a walk starting at "." skip everything.
func IsHiddenName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return strings.HasPrefix(name, ".")
}

// AppDirs names the directories the APPLICATION owns inside (or outside) the
// library tree. Library sweeps must never descend into them.
//
// Why this is a required struct parameter and not a variadic/optional one:
// before this existed, application state was kept out of the walkers by a
// NAMING COINCIDENCE. `backup_dir` defaulted to `<root_dir>/.backups`, and the
// dot rule below skipped it -- but `backup_dir` is an operator-settable
// absolute path. Point it at `<root_dir>/backups` (same place, no dot) and
// every walker descends into multi-GB archives. `openlibrary_dump_dir`
// defaults to `<root_dir>/openlibrary-dumps` and never had a dot at all: it
// was being walked on every scan, nested embedded database and all.
//
// A variadic or optional parameter can be silently omitted at a new call site,
// which reproduces exactly that bug in a new place with no diagnostic. Making
// it a required parameter turns "a new walker forgot the exclusion" into a
// COMPILE ERROR. Passing an explicit zero value is still possible, but it is a
// visible, deliberate act rather than an omission nobody can see in review.
//
// Fields hold RESOLVED, ABSOLUTE paths. pathutil deliberately does not import
// internal/config or internal/backup -- it takes resolved strings so it stays
// a leaf package. `appdirs.FromConfig` is the single place that does the
// resolving; build AppDirs there, not by hand at a walker.
type AppDirs struct {
	// BackupDir is the resolved database-backup directory. Resolve it with
	// backup.ResolveDir(configured, dbPath) -- that is the one authority for
	// the rule, and re-deriving it here would be a second, divergent copy.
	BackupDir string
	// OpenLibraryDumpDir holds OpenLibrary dump archives and an embedded
	// database of its own. Thousands of files, none of them library content.
	OpenLibraryDumpDir string
	// PlaylistDir holds generated playlist files.
	PlaylistDir string
}

// all returns the configured directories. Kept as one accessor so adding a
// field is a single edit and cannot be half-applied across the matcher.
func (a AppDirs) all() [3]string {
	return [3]string{a.BackupDir, a.OpenLibraryDumpDir, a.PlaylistDir}
}

// underDir reports whether `path` is `dir` itself or anything beneath it.
//
// Three hazards are handled here deliberately:
//
//  1. An EMPTY setting must never match. This is the most dangerous bug
//     available in this function: filepath.Clean("") returns ".", and "." is a
//     live relative prefix that would match every relative path handed to it
//     -- an unset backup_dir would silently swallow the entire library. The
//     empty check therefore happens BEFORE any Clean call, not after.
//
//  2. A non-absolute dir is dropped. backup.ResolveDir can legitimately return
//     a relative path (a relative `backup_dir` with no database path to anchor
//     it). It cannot be compared against the absolute paths a walk yields, and
//     guessing a base -- filepath.Abs would resolve it against the process CWD
//     -- is how you match a subtree nobody configured. Failing open (walking a
//     directory we were unsure about) is recoverable; failing closed (skipping
//     the library) is silent data loss.
//
//  3. Component-wise comparison, not strings.HasPrefix. A naive prefix test
//     makes "/x/backups2" match "/x/backups". filepath.Rel gives a result
//     starting with ".." exactly when path is outside dir, which is the
//     component-aware answer without hand-rolling separator arithmetic.
//
// A directory configured OUTSIDE the library root simply never matches
// anything the walk reaches. That is not an error, and is not reported as one.
func underDir(dir, path string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if !filepath.IsAbs(dir) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// skipsUnderRoot reports whether `path` is application-owned FROM THE
// PERSPECTIVE OF A WALK ROOTED AT `root`, and must therefore be kept out of
// that sweep regardless of what it is named.
//
// The `root` parameter is not decoration, and this method is deliberately
// unexported so no caller can reach the root-unaware version by accident.
//
// An app dir that CONTAINS the walk root -- including one EQUAL to it -- is
// ignored entirely. Without that, exempting only the root itself buys nothing:
// WalkDir's first callback survives and every descendant is skipped, so a
// library laid out as author/title/ scans to ZERO BOOKS with no error
// anywhere. Same silent outcome as abandoning the walk, reached one callback
// later. This is not only the misconfigured `backup_dir == root_dir` case: the
// scanner walks each enabled import path as its own root, so an import path
// added at or under `<root_dir>/openlibrary-dumps` would silently contribute
// nothing.
//
// Ignoring such a dir is the same principle the root exemption already
// encodes: an explicitly configured walk root is a deliberate choice by the
// operator, and everything below it is what they asked to be walked. Exclusion
// applies to app directories found INSIDE the tree being walked, never to the
// tree itself.
func (a AppDirs) skipsUnderRoot(root, path string) bool {
	for _, dir := range a.all() {
		if underDir(dir, root) {
			// This app dir is the walk root or an ancestor of it. Honouring it
			// would skip the entire requested tree.
			continue
		}
		if underDir(dir, path) {
			return true
		}
	}
	return false
}

// ShouldSkipDir reports whether a directory reached while walking `root` should
// be skipped, because it is application-owned or dot-prefixed.
//
// Library sweeps must not descend into the application's own working state.
// The application keeps that state inside the library tree -- backups,
// `.itunes-writeback`, `.failed`, OpenLibrary dumps -- and a sweep that walks
// into it treats application state as library content. The concrete case this
// was written for: database backups at `<root_dir>/.backups`, where each
// archive is many GB and would otherwise be discovered, hashed, and considered
// for import.
//
// Until 2026-08-29 that protection was a COINCIDENCE, not a rule: it held only
// because the directory happened to be named with a leading dot. `app` makes
// it a rule. See AppDirs for why the parameter is required rather than
// optional.
//
// ORDER MATTERS, and each step is load-bearing:
//
//   - The walk root is checked FIRST and never skipped, even when it is itself
//     dot-prefixed or app-owned. filepath.WalkDir yields the root as its first
//     callback, so returning SkipDir there abandons the entire walk. An
//     explicitly configured root is a deliberate choice; only what is found
//     BELOW it is subject to these rules. NOTE that this exemption alone is
//     NOT what protects a walk rooted at an app dir -- it saves exactly one
//     callback, and the descendants would still be skipped. skipsUnderRoot
//     carries that guarantee; see its comment.
//   - Callers that walk a SUBTREE of the library should pass the library root
//     here, not the subtree start, or the exemption hands back the very
//     directory they meant to exclude. The watcher does exactly this.
//   - The app-dir test then beats the `.alternates` carve-out. A carve-out
//     exists so deliberately dot-named CONTENT stays visible; it must not
//     resurrect an app-owned tree that merely happens to sit at such a name.
func ShouldSkipDir(root, path string, app AppDirs) bool {
	if path == root {
		return false
	}
	// A cleaned equality check as well: callers pass roots with and without a
	// trailing separator, and "/srv/books/" vs "/srv/books" must not decide
	// whether the whole library is scanned.
	if filepath.Clean(root) == filepath.Clean(path) {
		return false
	}
	if app.skipsUnderRoot(root, path) {
		return true
	}
	base := filepath.Base(path)
	if IsVisibleHiddenDir(base) {
		return false
	}
	return IsHiddenName(base)
}
