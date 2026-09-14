// file: internal/deluge/import.go
// version: 1.5.1
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-09-13
//
// ImportToLibrary copies a Deluge-managed file into the library root,
// updates the BookFile record, and optionally tells Deluge to move
// the torrent storage to the new directory.

package deluge

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

var importLog = logger.New("deluge-import")

// sameContent reports whether a and b hold the same bytes. Sizes are compared
// first from a stat, so two files of different sizes -- the common case --
// are told apart without reading either; only equal sizes are hashed
// (SHA-256). This runs on the import request path, where hashing two whole
// audiobooks just to learn their sizes differ was the cost.
func sameContent(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	ha, _, err := fileops.ComputeFileHashAndSize(a)
	if err != nil {
		return false, err
	}
	hb, _, err := fileops.ComputeFileHashAndSize(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// destLocks serializes the imports that target one destination path: from
// the ownership check through the copy or adoption, the row update, and the
// removal of a copy whose row update failed. Without it, import B could adopt
// the copy import A made and was about to remove on its failure path -- B's
// row then named a file A deleted. Process-local, which covers every caller:
// all imports into the library run in this process.
var destLocks keyedMutex

// keyedMutex is a set of mutexes, one per key, created on first use and
// dropped when nobody holds or waits on it.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*refMutex
}

type refMutex struct {
	sync.Mutex
	refs int // holders + waiters; guarded by keyedMutex.mu
}

// lock blocks until key is free and returns the matching unlock.
func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*refMutex)
	}
	m := k.locks[key]
	if m == nil {
		m = &refMutex{}
		k.locks[key] = m
	}
	m.refs++
	k.mu.Unlock()

	m.Lock()
	return func() {
		m.Unlock()
		k.mu.Lock()
		m.refs--
		if m.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

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
// On a failed BookFile update the call leaves things as it found them:
// bookFile's fields are restored and a copy this call created is removed, so
// a retry starts clean. newPath is then "" -- unless removing the copy also
// failed, in which case newPath names the file left in the library and the
// error says so. An existing destination with the same bytes as the source
// is adopted (recorded without copying) and never removed.
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
	if store == nil {
		// Without a store there is no ownership check and no row to update:
		// fail before touching the disk.
		return "", fmt.Errorf("ImportToLibrary: store is nil")
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

	// Everything from here to the end touches dest: hold its lock throughout
	// (see destLocks), including the Deluge move, which only serializes
	// imports into this one path.
	unlock := destLocks.lock(dest)
	defer unlock()

	// A destination some other book_file row already names is that row's
	// file. Adopting it would record two rows for one file, and a copy over
	// it is refused anyway -- so refuse before touching the disk. This also
	// covers a row whose file is missing: the path is still claimed.
	owner, err := store.GetBookFileByPath(dest)
	if err != nil {
		return "", fmt.Errorf("ImportToLibrary: look up the book file at %s: %w", dest, err)
	}
	if owner != nil && owner.ID != bookFile.ID {
		return "", fmt.Errorf("ImportToLibrary: destination %s is already recorded for book file %s (book %s); not adopting or replacing it for %s", dest, owner.ID, owner.BookID, bookFile.ID)
	}

	// Create destination directory if it does not exist.
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("ImportToLibrary: create dest dir %s: %w", destDir, err)
	}

	// Clone into the library, falling back to a byte copy when the filesystem
	// cannot clone. fileops.ReflinkOrCopy refuses an existing destination
	// rather than truncating it -- so a destination that is already there is
	// adopted when it holds the same bytes as the source (a copy an earlier
	// attempt made and could not record, before failed attempts cleaned up
	// after themselves) and refused otherwise.
	created := false
	if _, statErr := os.Lstat(dest); statErr == nil {
		same, cmpErr := sameContent(src, dest)
		if cmpErr != nil {
			return "", fmt.Errorf("ImportToLibrary: compare existing destination %s with %s: %w", dest, src, cmpErr)
		}
		if !same {
			return "", fmt.Errorf("ImportToLibrary: destination %s already exists with different content than %s", dest, src)
		}
		importLog.Info("ImportToLibrary: %s already holds the bytes of %s; recording it instead of copying again", dest, src)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("ImportToLibrary: stat destination %s: %w", dest, statErr)
	} else {
		if err := fileops.ReflinkOrCopy(src, dest); err != nil {
			return "", fmt.Errorf("ImportToLibrary: copy %s -> %s: %w", src, dest, err)
		}
		created = true
	}

	// Update the BookFile record.
	prevOriginal, prevPath, prevImportedAt := bookFile.DelugeOriginalPath, bookFile.FilePath, bookFile.ImportedFromDelugeAt
	now := time.Now()
	bookFile.DelugeOriginalPath = src
	bookFile.FilePath = dest
	bookFile.ImportedFromDelugeAt = &now

	if err := store.UpdateBookFile(bookFile.ID, bookFile); err != nil {
		// The row still names the source, so nothing leads to the copy.
		// Undo both halves: the caller's struct goes back to the source
		// (a retry must copy from src again, not from dest), and the copy
		// this call made is removed so the retry does not find an orphan
		// in its way. Until 2026-09-13 both were left behind and every
		// retry failed on the existing destination.
		bookFile.DelugeOriginalPath, bookFile.FilePath, bookFile.ImportedFromDelugeAt = prevOriginal, prevPath, prevImportedAt
		updErr := fmt.Errorf("ImportToLibrary: UpdateBookFile %s: %w", bookFile.ID, err)
		if !created {
			return "", updErr
		}
		if rmErr := os.Remove(dest); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			// The copy is still there: return its path with the error so
			// the caller can report exactly what was left behind.
			return dest, fmt.Errorf("%w; the copy at %s could not be removed: %v", updErr, dest, rmErr)
		}
		return "", updErr
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
