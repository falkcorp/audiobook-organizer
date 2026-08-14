// file: internal/fileops/write_tags_safe.go
// version: 1.1.0
// guid: b4c5d6e7-f8a9-0b1c-2d3e-4f5a6b7c8d9e
// last-edited: 2026-05-15

package fileops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// WriteTagsSafeOptions configures WriteTagsSafe behavior.
type WriteTagsSafeOptions struct {
	// BookFileID is the database row ID for hash tracking. Empty = skip DB update.
	BookFileID string
	// Store receives the pre- and post-write hashes. Nil = skip DB update.
	Store database.BookFileHashUpdater
}

// WriteTagsSafe writes audio metadata tags to path safely:
//  1. Computes original_file_hash (SHA-256) before any write
//  2. Copies the file to a sibling temp file in the same directory
//  3. Calls writeFn(tmpPath) to perform the actual tag write on the copy
//  4. On success: atomically renames the temp file over the original
//  5. Computes post_metadata_hash from the updated file
//  6. If opts.BookFileID != "" and opts.Store != nil, persists both hashes
//
// Returns (originalHash, postHash, error). On writeFn failure the original
// file is left untouched and the temp file is removed.
func WriteTagsSafe(path string, writeFn func(tmpPath string) error, opts WriteTagsSafeOptions) (originalHash, postHash string, err error) {
	// Step 1: fingerprint the original file before any modification.
	originalHash, err = ComputeFileHash(path)
	if err != nil {
		return "", "", fmt.Errorf("WriteTagsSafe: hash original %s: %w", path, err)
	}

	// Step 2: create temp file in the same directory so os.Rename is atomic
	// (same filesystem mount). Use the same extension so taglib can detect
	// the container format correctly.
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	tmpFile, err := os.CreateTemp(dir, ".writetmp-*"+ext)
	if err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// Always remove the temp file on failure.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// Step 3: copy original → temp (preserve permissions).
	if err = copyFileContents(path, tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: copy to temp: %w", err)
	}

	// Step 4: let the caller write tags into the temp copy.
	if err = writeFn(tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: writeFn: %w", err)
	}

	// Step 5: atomic rename — old file replaced only on success.
	if err = os.Rename(tmpPath, path); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: rename: %w", err)
	}

	// Step 6: fingerprint the result.
	postHash, err = ComputeFileHash(path)
	if err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: hash result %s: %w", path, err)
	}

	// Step 7: best-effort DB recording (non-fatal; caller has both hashes).
	if opts.BookFileID != "" && opts.Store != nil {
		_ = opts.Store.UpdateBookFileHashes(opts.BookFileID, originalHash, postHash)
	}

	return originalHash, postHash, nil
}

// copyFileContents copies src → dst, preserving the source file mode.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	// The mode argument above applies only when OpenFile CREATES dst. Our dst
	// already exists (os.CreateTemp made it, mode 0600), so an explicit chmod
	// is required — without it every tag rewrite silently replaced the
	// original with an 0600 file, zeroing the ACL mask and locking out every
	// non-owner reader (found via the 2026-08-14 E08 canary: 100 books'
	// files went share-unreadable).
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
