// file: internal/pathutil/hidden.go
// version: 1.0.0
// guid: 5a2c7e91-4d38-4b06-9c1f-7e0a3b58d264
// last-edited: 2026-08-29

package pathutil

import (
	"path/filepath"
	"strings"
)

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
	return IsHiddenName(filepath.Base(path))
}
