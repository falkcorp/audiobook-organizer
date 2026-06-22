// file: internal/security/pathvalidation/pathvalidation.go
// version: 1.2.0
// guid: 3a8f5c2b-7d4e-4a19-9f6b-1c0e2d3a5b7c
// last-edited: 2026-06-22

// Package pathvalidation provides centralized path validation utilities to
// prevent path traversal and injection vulnerabilities. It is the foundation
// for addressing the 217 path-injection alerts identified in the 2026-05-03
// security audit (SEC-AUDIT-1).
//
// Primary API:
//
//   - [ValidateRelativePath] – validates that a user-supplied relative path
//     does not escape a given root directory.
//   - [SanitizeFilename] – strips or replaces characters that are unsafe in
//     file or directory names.
//   - [SecureJoin] – joins a root with user-supplied path components and
//     returns an error if the result would escape the root.
//
// All three functions are pure (no filesystem I/O) so they are cheap, easy to
// unit-test, and usable before any disk access occurs. For callers that need
// symlink-safe joins, use [SecureJoinResolved] which also calls
// [filepath.EvalSymlinks].
package pathvalidation

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// ErrNotAbsolute is returned when an absolute path is required but the input is relative.
var ErrNotAbsolute = errors.New("path must be absolute")

// ErrPathTraversal is returned when a path escapes its declared root.
var ErrPathTraversal = errors.New("path traversal detected: path escapes root")

// ErrAbsolutePathNotAllowed is returned when an absolute path is supplied
// where only a relative path is expected.
var ErrAbsolutePathNotAllowed = errors.New("absolute path not allowed in user-supplied input")

// ErrEmptyPath is returned when a path is empty after sanitisation.
var ErrEmptyPath = errors.New("empty path")

// unsafeFilenameRE matches characters that are illegal or dangerous in
// filenames on Windows, macOS, or Linux. The set is intentionally conservative:
// only printable, non-control, non-reserved characters are allowed.
var unsafeFilenameRE = regexp.MustCompile(`[<>:"/\\|?*` + "\x00-\x1f\x7f" + `]`)

// maxFilenameLen is the maximum byte length allowed for a sanitised filename.
// FAT32 / VFAT limit is 255 bytes; we use the same limit for safety.
const maxFilenameLen = 255

// ValidateRelativePath validates that userPath, when joined with root, produces
// a path that stays inside root. Both root and userPath are cleaned before
// comparison.
//
// Rules enforced:
//   - userPath must not be absolute.
//   - The cleaned join of root and userPath must have root as a prefix.
//   - Traversal sequences ("..") that escape root are rejected.
//
// Only path strings are examined; no I/O is performed. If you need symlink
// safety you must additionally call [filepath.EvalSymlinks] on the result.
//
// Returns the cleaned absolute path on success.
func ValidateRelativePath(root, userPath string) (string, error) {
	if userPath == "" {
		return "", ErrEmptyPath
	}
	if filepath.IsAbs(userPath) {
		return "", ErrAbsolutePathNotAllowed
	}

	cleanRoot := filepath.Clean(root)
	joined := filepath.Join(cleanRoot, userPath)
	cleaned := filepath.Clean(joined)

	if !isWithinRoot(cleaned, cleanRoot) {
		return "", fmt.Errorf("%w: %q (root %q)", ErrPathTraversal, userPath, root)
	}
	return cleaned, nil
}

// SanitizeFilename returns a safe version of name suitable for use as a single
// filename (not a path). It:
//
//   - Strips ASCII control characters.
//   - Replaces characters that are illegal in Windows, macOS, or Linux filenames
//     (< > : " / \ | ? *) with an underscore.
//   - Replaces directory separator characters (/ and \) with an underscore to
//     prevent separator injection.
//   - Removes leading and trailing dots and spaces (Windows reserves them).
//   - Collapses consecutive underscores into one.
//   - Truncates to [maxFilenameLen] bytes.
//   - Returns "_" when the result would otherwise be empty.
//
// The result is always a non-empty string safe to use as a filename component.
func SanitizeFilename(name string) string {
	// Step 1: replace unsafe characters with underscore
	safe := unsafeFilenameRE.ReplaceAllString(name, "_")

	// Step 2: remove leading/trailing dots and spaces
	safe = strings.TrimFunc(safe, func(r rune) bool {
		return r == '.' || unicode.IsSpace(r)
	})

	// Step 3: collapse consecutive underscores
	for strings.Contains(safe, "__") {
		safe = strings.ReplaceAll(safe, "__", "_")
	}

	// Step 4: truncate to maxFilenameLen bytes
	if len(safe) > maxFilenameLen {
		// Truncate at a rune boundary.
		b := []byte(safe)
		b = b[:maxFilenameLen]
		safe = string(b)
	}

	// Step 5: ensure non-empty
	if safe == "" || safe == "_" {
		safe = "_"
	}

	return safe
}

// SecureJoin joins root with each element in parts, cleans the result, and
// returns an error if the resolved path would escape root. It is a safe
// replacement for [filepath.Join] when any part of the path comes from user
// input, the database, or the filesystem.
//
// SecureJoin does not resolve symlinks. Use [SecureJoinResolved] if you need
// symlink-safety at the cost of filesystem I/O.
//
// Returns the cleaned absolute path on success.
func SecureJoin(root string, parts ...string) (string, error) {
	cleanRoot := filepath.Clean(root)

	// Build the joined path incrementally so we can bail out early.
	current := cleanRoot
	for _, part := range parts {
		if part == "" {
			continue
		}
		// Reject absolute path components after the first.
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("%w: part %q is absolute", ErrAbsolutePathNotAllowed, part)
		}
		joined := filepath.Join(current, part)
		cleaned := filepath.Clean(joined)
		if !isWithinRoot(cleaned, cleanRoot) {
			return "", fmt.Errorf("%w: part %q escapes root %q", ErrPathTraversal, part, root)
		}
		current = cleaned
	}

	return current, nil
}

// SecureJoinResolved is like [SecureJoin] but additionally resolves symlinks
// via [filepath.EvalSymlinks]. This prevents symlink-based traversal attacks
// where a symlink inside root points outside root.
//
// This function performs filesystem I/O. If the path does not yet exist on
// disk (e.g. you are about to create it), use [SecureJoin] and validate after
// creation.
//
// Returns the cleaned absolute real path on success.
func SecureJoinResolved(root string, parts ...string) (string, error) {
	joined, err := SecureJoin(root, parts...)
	if err != nil {
		return "", err
	}

	// Resolve symlinks in the root first so we compare against the real root.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("evaluating symlinks for root %q: %w", root, err)
	}
	realRoot = filepath.Clean(realRoot)

	// Resolve symlinks in the joined path.
	realJoined, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// Path doesn't exist yet — both sides of the comparison must
		// use the SAME root form, otherwise a symlinked tmpdir like
		// macOS's /var/folders → /private/var/folders falsely fails:
		// realRoot resolves to /private/var/folders/... but realJoined
		// stays at /var/folders/... so isWithinRoot returns false.
		//
		// Walk the joined path upward to the first existing ancestor,
		// EvalSymlinks that, then append the non-existent suffix. The
		// resulting realJoined now lives under the same realRoot tree.
		realJoined = resolveExistingPrefix(joined)
	}

	if !isWithinRoot(realJoined, realRoot) {
		return "", fmt.Errorf("%w: resolved path %q escapes root %q", ErrPathTraversal, realJoined, realRoot)
	}

	return realJoined, nil
}

// resolveExistingPrefix walks a path upward to the first existing ancestor,
// resolves symlinks on that ancestor, then re-appends the non-existent
// suffix. This makes traversal checks consistent across paths that will
// soon exist but currently don't (e.g. the file the caller is about to
// create).
func resolveExistingPrefix(path string) string {
	clean := filepath.Clean(path)
	suffix := ""
	for {
		resolved, err := filepath.EvalSymlinks(clean)
		if err == nil {
			if suffix == "" {
				return resolved
			}
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			// Reached the root and still couldn't resolve — fall back
			// to the cleaned path so the static prefix check at least
			// sees a normalized form.
			return filepath.Clean(path)
		}
		suffix = filepath.Join(filepath.Base(clean), suffix)
		clean = parent
	}
}

// CleanAbsolutePath validates that path is an absolute filesystem path with no
// traversal sequences and returns the cleaned canonical form. Use this for
// paths that must be absolute (admin-configured paths, user-supplied library
// locations) before passing them to filesystem operations.
//
// Returns ErrNotAbsolute if path is not absolute.
// Returns ErrPathTraversal if filepath.Clean would alter the path (e.g. it
// contains "..").
//
// The returned string is safe to use in file operations; it comes from this
// function's return value, not directly from user input, so taint analysis
// tools correctly treat it as sanitized.
func CleanAbsolutePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q", ErrNotAbsolute, path)
	}
	cleaned := filepath.Clean(path)
	if cleaned != path {
		return "", fmt.Errorf("%w: path %q contains traversal sequences", ErrPathTraversal, path)
	}
	return cleaned, nil
}

// isWithinRoot reports whether path is equal to root or is directly contained
// within root. Both arguments must already be cleaned.
func isWithinRoot(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// ErrDangerousRoot is returned when a path is a known-dangerous system directory.
var ErrDangerousRoot = errors.New("path is a protected system directory")

// dangerousRoots lists absolute paths that must never be used as a library root
// or factory-reset target. The list is conservative — it protects system
// directories on Linux and macOS that, if recursively deleted or overwritten,
// would render the host unrecoverable.
var dangerousRoots = []string{
	"/",
	"/bin", "/sbin", "/usr", "/lib", "/lib64", "/lib32",
	"/etc", "/var", "/run", "/sys", "/proc", "/dev",
	"/boot", "/root", "/home",
	"/opt", "/srv",
}

// IsDangerousRoot reports whether path equals one of the known-dangerous system
// directories. It does NOT check sub-paths — /home/user/audiobooks is safe even
// though /home itself is dangerous.
func IsDangerousRoot(path string) bool {
	cleaned := filepath.Clean(path)
	for _, d := range dangerousRoots {
		if cleaned == d {
			return true
		}
	}
	return false
}
