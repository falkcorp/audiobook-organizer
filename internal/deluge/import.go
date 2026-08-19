// file: internal/deluge/import.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-19
//
// ImportToLibrary copies a Deluge-managed file into the library root,
// updates the BookFile record, and optionally tells Deluge to move
// the torrent storage to the new directory.

package deluge

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

// ImportToLibrary copies a file from a Deluge-managed path into the library root,
// updates the BookFile record in the database, and optionally tells Deluge to move
// the torrent storage to the new directory.
//
// Parameters:
//   - cfg: app config (used for RootDir and DelugeMoveEnabled)
//   - delugeClient: Deluge JSON-RPC client (may be nil; if nil, MoveStorage is skipped)
//   - store: database store (used to call UpdateBookFile)
//   - bookFile: the BookFile to import; its FilePath must point to the source file.
//     After a successful return, bookFile.FilePath is updated to the new path.
//
// Returns the new absolute file path and nil on success.
// Returns an error if the source file cannot be read or the destination cannot be written.
// A MoveStorage failure is NOT returned as an error — it is logged only.
//
// Idempotent: if bookFile.ImportedFromDelugeAt is already set, returns the current
// FilePath immediately without repeating the copy or DB update.
func ImportToLibrary(
	cfg *config.Config,
	delugeClient *Client,
	store Store,
	bookFile *database.BookFile,
) (newPath string, err error) {
	if bookFile == nil {
		return "", fmt.Errorf("ImportToLibrary: bookFile is nil")
	}

	// Idempotency guard: already imported.
	if bookFile.ImportedFromDelugeAt != nil {
		slog.Info("ImportToLibrary already imported at , skipping", "bookFile", bookFile.FilePath, "value1", bookFile.ImportedFromDelugeAt.Format(time.RFC3339))
		return bookFile.FilePath, nil
	}

	src := bookFile.FilePath
	if src == "" {
		return "", fmt.Errorf("ImportToLibrary: bookFile.FilePath is empty")
	}

	// Determine destination path inside RootDir, validating with safepath.
	// If the source is under RootDir, preserve its relative structure. Otherwise
	// place the file directly under RootDir.
	rel, relErr := filepath.Rel(cfg.RootDir, filepath.Dir(src))

	var destSP safepath.SafePath
	if relErr == nil && !filepath.IsAbs(rel) && !isParentTraversal(rel) {
		// Source is under RootDir — preserve structure.
		destSP, err = safepath.Join(cfg.RootDir, rel, filepath.Base(src))
		if err != nil {
			return "", fmt.Errorf("ImportToLibrary: invalid destination path: %w", err)
		}
	} else {
		// Source is outside RootDir — place directly under RootDir.
		destSP, err = safepath.Join(cfg.RootDir, filepath.Base(src))
		if err != nil {
			return "", fmt.Errorf("ImportToLibrary: invalid destination path: %w", err)
		}
	}

	dest := destSP.String()

	// Do not copy if source and destination are the same path.
	if src == dest {
		slog.Info("ImportToLibrary source and dest are the same (), skipping copy", "src", src)
		return src, nil
	}

	// Create destination directory if it does not exist.
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("ImportToLibrary: create dest dir %s: %w", destDir, err)
	}

	// Attempt reflink copy (Linux: ioctl FICLONE; macOS: clonefile).
	// Falls back to io.Copy on any error.
	copyErr := reflinkCopy(src, dest)
	if copyErr != nil {
		slog.Debug("ImportToLibrary reflink failed (), falling back to io.Copy", "copyErr", copyErr)
		if err := ioCopy(src, dest); err != nil {
			return "", fmt.Errorf("ImportToLibrary: copy %s -> %s: %w", src, dest, err)
		}
	}

	// Update the BookFile record.
	now := time.Now()
	bookFile.DelugeOriginalPath = src
	bookFile.FilePath = dest
	bookFile.ImportedFromDelugeAt = &now

	if err := store.UpdateBookFile(bookFile.ID, bookFile); err != nil {
		// The file has been copied but the DB update failed. Log it — the
		// caller is responsible for retry or rollback.
		return dest, fmt.Errorf("ImportToLibrary: UpdateBookFile %s: %w", bookFile.ID, err)
	}

	slog.Info("ImportToLibrary copied ->", "src", src, "dest", dest)

	// Best-effort: tell Deluge to move the torrent storage.
	if cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
		moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
		if moveErr != nil {
			slog.Warn("ImportToLibrary MoveStorage for hash failed (non-fatal)", "bookFile", bookFile.DelugeHash, "moveErr", moveErr)
			// Do NOT return this error. MoveStorage is best-effort.
		} else {
			slog.Info("ImportToLibrary MoveStorage for hash -> succeeded", "bookFile", bookFile.DelugeHash, "filepath", filepath.Dir(dest))
		}
	}

	return dest, nil
}

// isParentTraversal returns true if the rel path starts with ".." (escapes root).
func isParentTraversal(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}

// reflinkCopy attempts a copy-on-write clone of src to dest using OS-specific
// syscalls. Returns an error if the reflink is not supported or fails.
func reflinkCopy(src, dest string) error {
	return reflinkCopyOS(src, dest)
}

// ReflinkOrCopy attempts a copy-on-write reflink then falls back to a
// full io.Copy if the reflink path is unsupported. Exported so callers in
// other packages (e.g., the deluge plugin) can reuse the same semantics.
func ReflinkOrCopy(src, dest string) error {
	if err := reflinkCopy(src, dest); err != nil {
		// Attempt a plain io.Copy as a fallback. Return the copy error if that
		// also fails.
		if err2 := ioCopy(src, dest); err2 != nil {
			return err2
		}
	}
	return nil
}

// ioCopy copies src to dest using standard io.Copy (read all bytes, write all bytes).
func ioCopy(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	return nil
}
