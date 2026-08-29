// file: internal/pathutil/hidden.go
// version: 1.1.0
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

// ShouldSkipDir reports whether a directory reached while walking `root` should
// be skipped because it is dot-prefixed.
//
// Library sweeps must not descend into dot-directories. The application keeps
// its own working state inside the library tree -- `.backups`,
// `.itunes-writeback`, `.failed` -- and a sweep that walks into those treats
// application state as library content. The concrete case this was written for:
// database backups moved to `<root_dir>/.backups`, where each archive is ~15 GB
// and would otherwise be discovered, hashed, and considered for import.
//
// It replaces a scattering of one-off checks. Before this, the scanner skipped
// exactly one hidden directory by name (`.failed`), which meant every new
// hidden directory had to remember to add itself to a list it did not know
// existed. A rule is cheaper to keep true than an inventory.
//
// The walk root is never skipped, even when it is itself dot-prefixed.
// filepath.WalkDir yields the root as its first callback, so returning
// SkipDir there would abandon the entire walk -- which is precisely what would
// happen to anyone who pointed an import path at a hidden directory on purpose.
// An explicitly configured root is a deliberate choice; only what is found
// BELOW it is subject to this rule.
func ShouldSkipDir(root, path string) bool {
	if path == root {
		return false
	}
	// A cleaned equality check as well: callers pass roots with and without a
	// trailing separator, and "/srv/books/" vs "/srv/books" must not decide
	// whether the whole library is scanned.
	if filepath.Clean(root) == filepath.Clean(path) {
		return false
	}
	base := filepath.Base(path)
	if IsVisibleHiddenDir(base) {
		return false
	}
	return IsHiddenName(base)
}
