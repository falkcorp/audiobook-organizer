// file: internal/tagger/safe_write.go
// version: 1.3.0
// guid: 4a7e1c3b-9f02-4d85-b8e6-2f5a0d3c7b91
// last-edited: 2026-08-15
//
// WriteTagsSafe / WriteImageSafe — pre-flight guard for all taglib writes.
//
// If the target file lives under a Deluge-managed (protected) path, it is
// first copied into the library via the LibraryImporter before the tag write
// proceeds. This ensures we never modify a file that is still being seeded by
// Deluge, preserving seeding integrity while allowing the organizer to enrich
// metadata.
//
// Both functions delegate the actual write to fileops.WriteTagsSafe, which
// copies the file to a sibling temp file, writes tags into the copy, and
// atomically renames it over the original. This keeps the on-disk state
// consistent even if the process is killed mid-write.

package tagger

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	taglib "go.senan.xyz/taglib"
)

// PathChecker reports whether a filesystem path is Deluge-protected.
// Satisfied by *deluge.ProtectedPathCache.
type PathChecker interface {
	IsProtected(filePath string) bool
}

// LibraryImporter copies a protected file into the library root and returns
// the new path. Implementations are expected to be idempotent.
//
// The context is passed through for cancellation; a nil context is treated as
// context.Background() by well-behaved implementations.
type LibraryImporter interface {
	// ImportPath resolves src to a library path, copying if necessary.
	// Returns the effective (possibly new) path to write to.
	ImportPath(ctx context.Context, srcPath string) (libraryPath string, err error)
}

// SafeWriteDeps bundles the optional dependencies needed for the pre-flight
// guard. All fields are optional; nil values disable the guard for that
// concern (the write proceeds directly against the original path).
type SafeWriteDeps struct {
	// ProtectedCache checks whether a path is under a Deluge save_path prefix.
	// If nil, no protection check is performed.
	ProtectedCache PathChecker

	// Importer copies a file from a protected path into the library.
	// If nil (and ProtectedCache is set), a protected-path hit is logged but
	// the write still proceeds in-place — callers should always wire both.
	Importer LibraryImporter
}

// WriteTagsSafe writes tags to path, importing first if the path is protected.
//
// opts is the taglib write option (0 = merge, taglib.Clear = replace-all).
// deps may be zero-value; in that case this is equivalent to a plain
// taglib.WriteTags call wrapped in an atomic copy+rename.
func WriteTagsSafe(ctx context.Context, path string, tags map[string][]string, opts taglib.WriteOption, deps SafeWriteDeps) error {
	if ctx == nil {
		ctx = context.Background()
	}

	effectivePath, err := resolvePath(ctx, path, deps)
	if err != nil {
		return fmt.Errorf("WriteTagsSafe: resolve path: %w", err)
	}

	_, _, err = fileops.WriteTagsSafe(effectivePath, func(tmpPath string) error {
		return taglib.WriteTags(tmpPath, tags, opts)
	}, fileops.WriteTagsSafeOptions{})
	if err != nil {
		return fmt.Errorf("WriteTagsSafe: %w", err)
	}
	return nil
}

// WriteTagsInPlace writes tags directly to path with NO copy-and-rename wrapper
// and NO protected-path resolution.
//
// Use this ONLY when the caller is already operating on a temp copy produced by
// an outer fileops.WriteTagsSafe, and has already made the protected-path
// decision on the real path. Writing to a live library file through this
// function bypasses the atomic-rename safety net — call WriteTagsSafe instead.
//
// Why this exists: the write-back path wrapped every tag write in
// fileops.WriteTagsSafe and then called a writer that wrapped it AGAIN. Each
// wrapper hashes the whole file twice and copies it once, so one tag write cost
// four full-file SHA-256 passes and two full-file copies of the audio — and the
// inner pair was discarded, because the inner call passed empty options and
// ignored its return values. On NAS-backed multi-hundred-MB files that redundant
// I/O, not the tag encoding, was the cost of write-back. The inner wrapper also
// protected nothing the outer one did not already cover: if the tag write
// corrupts its target and still returns nil, the outer rename publishes it
// either way.
func WriteTagsInPlace(path string, tags map[string][]string, opts taglib.WriteOption) error {
	if err := taglib.WriteTags(path, tags, opts); err != nil {
		return fmt.Errorf("WriteTagsInPlace: %w", err)
	}
	return nil
}

// WriteImageInPlace embeds cover art directly into path with no copy-and-rename
// wrapper. Same contract and same caveats as WriteTagsInPlace.
func WriteImageInPlace(path string, data []byte) error {
	if err := taglib.WriteImage(path, data); err != nil {
		return fmt.Errorf("WriteImageInPlace: %w", err)
	}
	return nil
}

// WriteImageSafe embeds cover art into the audio file at path, importing first
// if the path is protected.
//
// data is the raw image bytes (JPEG, PNG, etc.).
// deps may be zero-value; in that case this is equivalent to a plain
// taglib.WriteImage call wrapped in an atomic copy+rename.
func WriteImageSafe(ctx context.Context, path string, data []byte, deps SafeWriteDeps) error {
	if ctx == nil {
		ctx = context.Background()
	}

	effectivePath, err := resolvePath(ctx, path, deps)
	if err != nil {
		return fmt.Errorf("WriteImageSafe: resolve path: %w", err)
	}

	_, _, err = fileops.WriteTagsSafe(effectivePath, func(tmpPath string) error {
		return taglib.WriteImage(tmpPath, data)
	}, fileops.WriteTagsSafeOptions{})
	if err != nil {
		return fmt.Errorf("WriteImageSafe: %w", err)
	}
	return nil
}

// ResolvePathForWrite returns the path to actually write to.
// If the path is protected and an Importer is configured, it imports the file
// and returns the new library path. Otherwise it returns path unchanged.
//
// Exported so that build-variant packages (e.g. metadata/taglib_cgo.go) can
// call the same resolution logic without duplicating it.
func ResolvePathForWrite(ctx context.Context, path string, deps SafeWriteDeps) (string, error) {
	return resolvePath(ctx, path, deps)
}

// resolvePath is the internal implementation used by WriteTagsSafe, WriteImageSafe,
// and ResolvePathForWrite.
func resolvePath(ctx context.Context, path string, deps SafeWriteDeps) (string, error) {
	if deps.ProtectedCache == nil {
		return path, nil
	}

	if !deps.ProtectedCache.IsProtected(path) {
		return path, nil
	}

	// Path is protected.
	slog.Info("safe_write importing protected path before tag write", "path", path)

	if deps.Importer == nil {
		// Guard is incomplete — log a warning and proceed in-place rather than
		// failing silently or corrupting a torrent file. This should not happen
		// in production (both fields must be wired together).
		slog.Warn("safe_write LibraryImporter is nil for protected path; writing in-place", "path", path)
		return path, nil
	}

	libraryPath, err := deps.Importer.ImportPath(ctx, path)
	if err != nil {
		return "", fmt.Errorf("import protected path %s: %w", path, err)
	}

	slog.Info("safe_write imported protected path before tag write", "src", path, "dest", libraryPath)
	return libraryPath, nil
}
