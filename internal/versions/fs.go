// file: internal/versions/fs.go
// version: 1.0.2
// guid: 6c2a1f8d-4b3e-4f70-a9c5-2e8d0f1b9a47
// last-edited: 2026-10-27
//
// Filesystem primitives for the `.versions/{id}/` layout (spec 3.1).
//
// On ZFS, all operations here are O(1) rename within the same
// dataset — no copy, no hash re-verify. On other filesystems the
// same call sequence degrades to standard POSIX rename, which is
// still atomic within a single directory hierarchy.
//
// All functions are safe to call under partial state: if a move
// was already half-done by a previous run, repeat calls are no-ops
// on the already-moved side. This matches the resumable-tracked-op
// semantics described in spec 3.1 §4.

package versions

import (
	"fmt"
	"os"
	"path/filepath"
)

// VersionsDirName is the hidden directory inside a book's folder
// where alt-version files live.
const VersionsDirName = ".versions"

// VersionsDir returns the absolute path to the .versions/ directory
// for a book whose primary file lives at bookDir. Does NOT create
// the directory — call EnsureVersionsDir when a move is imminent.
func VersionsDir(bookDir string) string {
	return filepath.Join(bookDir, VersionsDirName)
}

// VersionSlotDir returns the per-version subdirectory for a given
// version ID.
func VersionSlotDir(bookDir, versionID string) string {
	return filepath.Join(VersionsDir(bookDir), versionID)
}

// EnsureVersionsDir creates `bookDir/.versions/{versionID}` if it
// doesn't exist. Idempotent.
func EnsureVersionsDir(bookDir, versionID string) (string, error) {
	slot := VersionSlotDir(bookDir, versionID)
	if err := os.MkdirAll(slot, 0o775); err != nil {
		return "", fmt.Errorf("create version slot %s: %w", slot, err)
	}
	return slot, nil
}

// MoveToVersionsDir moves each file in `filePaths` from its current
// location into `bookDir/.versions/{versionID}/`. Returns the list
// of new paths (same order as input) plus any per-file errors
// encountered. A file that's already in the version slot is treated
// as a success (idempotent under partial completion).
func MoveToVersionsDir(bookDir, versionID string, filePaths []string) ([]string, []error) {
	slot, err := EnsureVersionsDir(bookDir, versionID)
	if err != nil {
		return nil, []error{err}
	}
	newPaths := make([]string, len(filePaths))
	var errs []error
	for i, src := range filePaths {
		dst := filepath.Join(slot, filepath.Base(src))
		newPaths[i] = dst
		if _, err := movePreservingAttrs(src, dst); err != nil {
			errs = append(errs, fmt.Errorf("move %s → %s: %w", src, dst, err))
		}
	}
	return newPaths, errs
}

// MoveFromVersionsDir is the reverse: move each file from its
// version slot up to `bookDir/<basename>`. Used when an alt
// becomes the primary. Returns the list of new natural-path
// locations.
func MoveFromVersionsDir(bookDir, versionID string, filePaths []string) ([]string, []error) {
	newPaths := make([]string, len(filePaths))
	var errs []error
	for i, src := range filePaths {
		dst := filepath.Join(bookDir, filepath.Base(src))
		newPaths[i] = dst
		if _, err := movePreservingAttrs(src, dst); err != nil {
			errs = append(errs, fmt.Errorf("move %s → %s: %w", src, dst, err))
		}
	}
	return newPaths, errs
}

// movePreservingAttrs renames src to dst. Returns (alreadyThere,
// err) — alreadyThere is true when src doesn't exist but dst does
// (previous run completed this move; current call is a no-op).
//
// Permission bits are preserved by rename on the same filesystem.
// Cross-filesystem moves need a copy path, which we don't implement
// here — callers on ZFS have guaranteed same-fs and callers on
// other filesystems should be within a single disk.
func movePreservingAttrs(src, dst string) (bool, error) {
	if src == dst {
		return true, nil
	}
	// Create the parent for dst if needed (version slot already
	// exists when called via MoveToVersionsDir, but MoveFromVersionsDir
	// writes directly into bookDir which always exists — this is a
	// safety belt for reuse).
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o775); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", parent, err)
	}

	srcInfo, srcErr := os.Stat(src)
	_, dstErr := os.Stat(dst)

	// Case: src gone, dst present — already-moved.
	if os.IsNotExist(srcErr) && dstErr == nil {
		return true, nil
	}
	if srcErr != nil {
		return false, srcErr
	}
	// Case: dst already present AND src also present — don't clobber
	// silently; caller likely has a bug / partial state.
	if dstErr == nil {
		return false, fmt.Errorf("destination already exists: %s", dst)
	}

	// Execute the rename.
	if err := os.Rename(src, dst); err != nil {
		return false, err
	}

	// Best-effort permission fix: many target filesystems (NFS, some
	// SMB) silently rewrite mode bits on rename. Re-apply the source
	// file's mode explicitly. Ignore failure — not everyone can
	// chmod on every fs.
	_ = os.Chmod(dst, srcInfo.Mode())
	return false, nil
}

// PruneEmptyVersionsDir removes `bookDir/.versions/` if empty, so
// a book with all its versions gone doesn't leave an empty dot
// folder lying around.
func PruneEmptyVersionsDir(bookDir string) error {
	dir := VersionsDir(bookDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	return os.Remove(dir)
}
