// file: internal/versions/fs.go
// version: 1.1.1
// guid: 6c2a1f8d-4b3e-4f70-a9c5-2e8d0f1b9a47
// last-edited: 2026-10-29
//
// Filesystem primitives for the `.versions/{id}/` layout (spec 3.1).
//
// On ZFS, all operations here are O(1) rename within the same
// dataset — no copy, no hash re-verify. On other filesystems the
// same call sequence degrades to standard POSIX rename, which is
// still atomic within a single directory hierarchy.
//
// A move is made only of a source that is there and a destination that is free.
// A source that is missing is an error, never "already moved": taking a file at
// the destination to be the missing source adopts whatever is there (another
// book's file with the same name), and the swap that rests on it then
// trashes and purges the real audio. ResumeVersionSwap (swap_resume.go) resumes
// a half-done swap and does not infer a move from names either: it finds each
// file at exactly one of the two places it can be, or refuses.

package versions

import (
	"errors"
	"fmt"
	"io/fs"
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
// location into `bookDir/.versions/{versionID}/`. It returns the new paths
// (same order as input), which of the files THIS call renamed (same order; a
// file that failed is false), and the per-file errors. A source that is missing
// or a destination that is taken is an error for that file (see the package
// comment above): nothing is clobbered, and nothing is taken to be already moved.
func MoveToVersionsDir(bookDir, versionID string, filePaths []string) (newPaths []string, renamed []bool, errs []error) {
	slot, err := EnsureVersionsDir(bookDir, versionID)
	if err != nil {
		return nil, nil, []error{err}
	}
	newPaths = make([]string, len(filePaths))
	renamed = make([]bool, len(filePaths))
	for i, src := range filePaths {
		dst := filepath.Join(slot, filepath.Base(src))
		newPaths[i] = dst
		ok, err := movePreservingAttrs(src, dst)
		renamed[i] = ok
		if err != nil {
			errs = append(errs, fmt.Errorf("move %s → %s: %w", src, dst, err))
		}
	}
	return newPaths, renamed, errs
}

// MoveFromVersionsDir is the reverse: move each file from its
// version slot up to `bookDir/<basename>`. Used when an alt
// becomes the primary. Returns the new natural-path locations, which of the
// files this call renamed, and the per-file errors, like MoveToVersionsDir.
func MoveFromVersionsDir(bookDir, versionID string, filePaths []string) (newPaths []string, renamed []bool, errs []error) {
	newPaths = make([]string, len(filePaths))
	renamed = make([]bool, len(filePaths))
	for i, src := range filePaths {
		dst := filepath.Join(bookDir, filepath.Base(src))
		newPaths[i] = dst
		ok, err := movePreservingAttrs(src, dst)
		renamed[i] = ok
		if err != nil {
			errs = append(errs, fmt.Errorf("move %s → %s: %w", src, dst, err))
		}
	}
	return newPaths, renamed, errs
}

// movePreservingAttrs renames src to dst and reports whether it did. The source
// must exist (Lstat: a dangling link is a source that is there, and is moved as
// the link it is) and the destination must be free: it is made by a no-replace
// rename (renameNoReplace), so a file that appears at dst between the check and
// the rename is not overwritten either.
//
// Permission bits are preserved by rename on the same filesystem.
// Cross-filesystem moves need a copy path, which we don't implement
// here — callers on ZFS have guaranteed same-fs and callers on
// other filesystems should be within a single disk.
func movePreservingAttrs(src, dst string) (renamed bool, err error) {
	if src == dst {
		return false, fmt.Errorf("source and destination are the same path: %s", src)
	}
	// Create the parent for dst if needed (version slot already
	// exists when called via MoveToVersionsDir, but MoveFromVersionsDir
	// writes directly into bookDir which always exists — this is a
	// safety belt for reuse).
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o775); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", parent, err)
	}

	srcInfo, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Errorf("source: %w", err)
	}
	if err := renameNoReplace(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("destination already exists: %s", dst)
		}
		return false, err
	}

	// Best-effort permission fix: many target filesystems (NFS, some
	// SMB) silently rewrite mode bits on rename. Re-apply the source
	// file's mode explicitly. Ignore failure — not everyone can
	// chmod on every fs. A link keeps its own (Chmod would follow it).
	if srcInfo.Mode()&os.ModeSymlink == 0 {
		_ = os.Chmod(dst, srcInfo.Mode())
	}
	return true, nil
}

// PruneEmptyVersionsDir removes `bookDir/.versions/` if empty, so
// a book with all its versions gone doesn't leave an empty dot
// folder lying around.
//
// A .versions that is a symlink is left alone, whatever it points at: ReadDir
// would follow it, and removing "the empty folder" would remove the link.
func PruneEmptyVersionsDir(bookDir string) error {
	dir := VersionsDir(bookDir)
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
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
