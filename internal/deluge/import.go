// file: internal/deluge/import.go
// version: 1.9.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-09-19
//
// ImportToLibrary copies a Deluge-managed file into the library root,
// updates the BookFile record, and optionally tells Deluge to move
// the torrent storage to the new directory.

package deluge

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
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

// ImportOptions configures ImportToLibraryWith.
type ImportOptions struct {
	// Protected is the protected-path predicate (the server's
	// ProtectedPathCache). Nil disables the checks that need it.
	Protected tagger.PathChecker
	// ExpectedBookFileID, when set, is the caller's book_file row for the
	// source. The import is refused when bookFile is another row, or when the
	// row the path index holds for the source is another row: the index keeps
	// one entry per path, so a second row naming the same file would
	// otherwise be repointed on the first row's behalf.
	ExpectedBookFileID string
	// NoMoveStorage never asks Deluge to move the torrent's storage. Set for
	// imports the tag-write guard triggers: those land at RootDir/<basename>,
	// and moving a torrent's storage there makes the library root a Deluge
	// save path, protecting every book in it.
	NoMoveStorage bool
}

// ImportToLibrary is ImportToLibraryWith with only the protected-path
// predicate set: the user-initiated import (the discovery endpoint).
func ImportToLibrary(
	cfg *config.Config,
	delugeClient *Client,
	store Store,
	bookFile *database.BookFile,
	protected tagger.PathChecker,
) (newPath string, err error) {
	return ImportToLibraryWith(cfg, delugeClient, store, bookFile, ImportOptions{Protected: protected})
}

// ImportToLibraryWith copies a file from a Deluge-managed path into the library root,
// updates the BookFile record in the database, and optionally tells Deluge to move
// the torrent storage to the new directory.
//
// Parameters:
//   - cfg: app config (used for RootDir, ProtectedPaths, the iTunes library and
//     DelugeMoveEnabled)
//   - delugeClient: Deluge JSON-RPC client (may be nil; if nil, MoveStorage is skipped)
//   - store: database store (used to call UpdateBookFile)
//   - bookFile: the BookFile to import; its FilePath must point to the source file.
//     After a successful return, bookFile.FilePath is updated to the new path.
//   - opts: see ImportOptions.
//
// Returns the new absolute file path and nil on success.
// Returns an error if the source file cannot be read or the destination cannot be written.
// A MoveStorage failure is NOT returned as an error — it is logged only.
//
// Only a Deluge save_path may be imported. A source under a static protected
// prefix (config.ProtectedPaths, the iTunes library) is refused with an error
// wrapping tagger.ErrProtectedPathWrite: those files are never copied out and
// repointed, whatever asked.
//
// On a failed BookFile update the call leaves things as it found them:
// bookFile's fields are restored and a copy this call created is removed, so
// a retry starts clean. newPath is then "" -- unless removing the copy also
// failed, in which case newPath names the file left in the library and the
// error says so. An existing destination with the same bytes as the source
// is adopted (recorded without copying) and never removed.
//
// Idempotent: if bookFile.ImportedFromDelugeAt is already set and FilePath is
// not protected, returns the current FilePath without repeating the copy or DB
// update. A row marked imported whose FilePath is still protected (it was
// pointed back at the seeding copy) is imported again: returning that path
// sent every tag write to the protected file, where the write guard refused
// it, so the book could never be tagged.
//
// A protected source whose library destination is the source itself (a
// protected directory under RootDir) cannot be copied anywhere, so it is
// refused with an error wrapping tagger.ErrProtectedPathWrite. An unprotected
// source already at its destination is not copied; its row is marked imported
// (so the discovery list stops offering it) and the source path is returned.
func ImportToLibraryWith(
	cfg *config.Config,
	delugeClient *Client,
	store Store,
	bookFile *database.BookFile,
	opts ImportOptions,
) (newPath string, err error) {
	if bookFile == nil {
		return "", fmt.Errorf("ImportToLibrary: bookFile is nil")
	}
	if store == nil {
		// Without a store there is no ownership check and no row to update:
		// fail before touching the disk.
		return "", fmt.Errorf("ImportToLibrary: store is nil")
	}
	if opts.ExpectedBookFileID != "" && bookFile.ID != opts.ExpectedBookFileID {
		return "", fmt.Errorf("ImportToLibrary: asked to import book file %s for caller's row %s; refusing to repoint a different row", bookFile.ID, opts.ExpectedBookFileID)
	}

	// Idempotency guard: already imported, and the row names a library file.
	if bookFile.ImportedFromDelugeAt != nil {
		if !isProtected(opts.Protected, bookFile.FilePath) {
			importLog.Info("ImportToLibrary: %s already imported at %s, skipping",
				logger.SanitizeLogValue(bookFile.FilePath), bookFile.ImportedFromDelugeAt.Format(time.RFC3339))
			return bookFile.FilePath, nil
		}
		importLog.Info("ImportToLibrary: book file %s is marked imported at %s but names protected path %s; importing it again",
			logger.SanitizeLogValue(bookFile.ID), bookFile.ImportedFromDelugeAt.Format(time.RFC3339), logger.SanitizeLogValue(bookFile.FilePath))
	}

	src := bookFile.FilePath
	if src == "" {
		return "", fmt.Errorf("ImportToLibrary: bookFile.FilePath is empty")
	}

	// A static protected prefix (the iTunes library, config.ProtectedPaths)
	// is never imported: only a Deluge save_path hit may be copied out.
	if why := staticProtection(cfg, opts.Protected, src); why != "" {
		return "", fmt.Errorf("ImportToLibrary: %s is under %s; only a Deluge save path may be imported: %w", src, why, tagger.ErrProtectedPathWrite)
	}

	// The row the path index holds for src must be this row. The index keeps
	// one entry per path; importing on behalf of another row would repoint
	// this one and leave that one naming the seeding file.
	if atSrc, lookErr := store.GetBookFileByPath(src); lookErr != nil {
		return "", fmt.Errorf("ImportToLibrary: look up the book file at %s: %w", src, lookErr)
	} else if atSrc != nil && atSrc.ID != bookFile.ID {
		return "", fmt.Errorf("ImportToLibrary: the path index names book file %s (book %s) for %s, not %s (book %s); refusing to import",
			atSrc.ID, atSrc.BookID, src, bookFile.ID, bookFile.BookID)
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

	// Source and destination are the same path: nothing to copy. For a
	// protected source that means there is no library copy to write to, so
	// refuse rather than hand the protected path back as the write target.
	if src == dest {
		if isProtected(opts.Protected, src) {
			return "", fmt.Errorf("ImportToLibrary: %s is protected and is its own library destination (a protected directory under RootDir), so there is nowhere to copy it: %w", src, tagger.ErrProtectedPathWrite)
		}
		// Already in place: record that, or the discovery list offers this
		// file again on every run (until 2026-09-14 it did).
		prevImportedAt := bookFile.ImportedFromDelugeAt
		now := time.Now()
		bookFile.ImportedFromDelugeAt = &now
		markErr := store.UpdateBookFile(bookFile.ID, bookFile)
		if errors.Is(markErr, database.ErrBookFileDurabilityUnknown) {
			importLog.Error("ImportToLibrary: marking %s imported was written but its fsync failed (%v): durability unknown; keeping it", bookFile.ID, markErr)
			markErr = nil
		}
		if err := markErr; err != nil {
			bookFile.ImportedFromDelugeAt = prevImportedAt
			return "", fmt.Errorf("ImportToLibrary: mark book file %s imported (already at its library destination %s): %w", bookFile.ID, src, err)
		}
		importLog.Info("ImportToLibrary: source and destination are both %s; nothing copied, row %s marked imported",
			logger.SanitizeLogValue(src), logger.SanitizeLogValue(bookFile.ID))
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

	updErr0 := store.UpdateBookFile(bookFile.ID, bookFile)
	repointDurable := true
	if errors.Is(updErr0, database.ErrBookFileDurabilityUnknown) {
		repointDurable = false
		// The row now names the copy (the write is visible; only its fsync
		// failed). Undoing it here would delete the file the row points at.
		importLog.Error("ImportToLibrary: repointing %s to %s was written but its fsync failed (%v): durability unknown; keeping the row and the copy", bookFile.ID, dest, updErr0)
		updErr0 = nil
	}
	if err := updErr0; err != nil {
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

	importLog.Info("ImportToLibrary copied %s -> %s", logger.SanitizeLogValue(src), logger.SanitizeLogValue(dest))

	// Best-effort: tell Deluge to move the torrent storage. Never for an
	// import the write guard triggered (opts.NoMoveStorage).
	//
	// Skipped when the repoint's durability is unknown: were its WAL record
	// lost, the row would revert to src — and Deluge would already have moved
	// the torrent away from src. The copy at dest serves either way.
	if !repointDurable && !opts.NoMoveStorage && cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
		importLog.Error("ImportToLibrary: skipping MoveStorage for hash %s: the repoint of %s is not known to be durable", bookFile.DelugeHash, bookFile.ID)
	}
	if repointDurable && !opts.NoMoveStorage && cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
		moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
		if moveErr != nil {
			importLog.Warn("ImportToLibrary MoveStorage for hash %s failed (non-fatal): %v", bookFile.DelugeHash, moveErr)
			// Do NOT return this error. MoveStorage is best-effort.
		} else {
			importLog.Info("ImportToLibrary MoveStorage for hash %s -> %s succeeded", bookFile.DelugeHash, filepath.Dir(dest))
		}
	}

	return dest, nil
}

// protectionClassifier is implemented by *ProtectedPathCache: it says why a
// path is protected.
type protectionClassifier interface {
	ProtectedBy(filePath string) string
}

// staticProtection names the static protected prefix path lies under -- the
// checker's static list, config.ProtectedPaths, or the iTunes library -- or
// returns "" when there is none. A Deluge save_path is not static.
func staticProtection(cfg *config.Config, protected tagger.PathChecker, path string) string {
	if pc, ok := protected.(protectionClassifier); ok && pc.ProtectedBy(path) == ProtectedByStatic {
		return "a static protected path"
	}
	if cfg == nil {
		return ""
	}
	for _, p := range cfg.ProtectedPaths {
		if p != "" && pathutil.IsWithin(path, p) {
			return "the configured protected path " + p
		}
	}
	for _, lib := range []string{cfg.ITunes.LibraryReadPath, cfg.ITunes.LibraryWritePath} {
		if lib == "" {
			continue
		}
		if dir := filepath.Dir(lib); pathutil.IsWithin(path, dir) {
			return "the iTunes library " + dir
		}
	}
	return ""
}

// isProtected reports whether path is protected; a nil checker protects nothing.
func isProtected(protected tagger.PathChecker, path string) bool {
	return protected != nil && protected.IsProtected(path)
}

// isParentTraversal returns true if the rel path starts with ".." (escapes root).
func isParentTraversal(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}
