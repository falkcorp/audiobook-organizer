// file: internal/backup/extract_store.go
// version: 1.0.2
// guid: c3e71b58-2a94-4d6f-8b10-5e9f2d4a7c63
// last-edited: 2026-11-09

package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// extractLog logs ExtractStore.
var extractLog = logger.New("backup.extract-store")

// ExtractStore is ABO's storage restore-offline --archive extractor
// (app.Options.R1ArchiveExtract, kernel.Config.ArchiveExtract): it
// extracts archivePath, an archive in HEAD's backup format, into storeDir,
// an empty directory the kernel made in the swap's staging directory, and
// returns the archive's SHA-256, which the restore intent records as its
// archive ID.
//
// HEAD's archive of a Pebble store holds one top-level directory, the store
// (CreateBackup and CreateBackupWithCheckpoint name it after the store's or
// the checkpoint's directory). ExtractStore extracts into a private
// directory beside storeDir (extractArchive: every entry confined, an empty
// archive refused), requires that one directory, moves its entries into
// storeDir, which keeps the kernel's mode, and syncs every file and
// directory it wrote, because the swap renames storeDir into place and the
// store must survive a crash after it. An archive of a single file (HEAD's
// SQLite backups) or of several top-level entries is refused.
//
// The checksum sidecar <archive>.sha256 that CreateBackup writes is checked
// when it exists: a mismatch refuses before anything is extracted. An
// archive with no sidecar is extracted unverified, as an operator naming
// the file by hand intends; the kernel then opens the extracted store
// read-only with this build's guard before anything is staged, so an
// archive that is not a store this build can open is never swapped in.
//
// Release R1 only: F8's backup extractor replaces it.
func ExtractStore(ctx context.Context, archivePath, storeDir string) (string, error) {
	sum, _, err := extractStore(ctx, archivePath, storeDir)
	return sum, err
}

// extractStore is ExtractStore, also reporting whether the archive was
// checked against its sidecar. It reads the sidecar once, so a caller that
// requires verification (StageRestore) learns what the extraction checked
// rather than reading the sidecar again after it.
func extractStore(ctx context.Context, archivePath, storeDir string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if !filepath.IsAbs(archivePath) || !filepath.IsAbs(storeDir) {
		return "", false, fmt.Errorf("backup: extract store: %q and %q must be absolute", archivePath, storeDir)
	}
	fi, err := os.Stat(archivePath)
	if err != nil {
		return "", false, fmt.Errorf("backup: extract store: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("backup: extract store: %s is not a regular file", archivePath)
	}
	if entries, err := os.ReadDir(storeDir); err != nil {
		return "", false, fmt.Errorf("backup: extract store: %w", err)
	} else if len(entries) > 0 {
		return "", false, fmt.Errorf("backup: extract store: %s is not empty", storeDir)
	}
	sum, err := calculateFileChecksum(archivePath, nil)
	if err != nil {
		return "", false, fmt.Errorf("backup: extract store: checksum %s: %w", archivePath, err)
	}
	verified, err := checkSidecar(archivePath, sum)
	if err != nil {
		return "", false, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(storeDir), ".extract-")
	if err != nil {
		return "", false, fmt.Errorf("backup: extract store: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(tmp); rerr != nil {
			extractLog.Warn("could not remove the extraction directory %s: %s", tmp, logger.SanitizeLogValue(rerr.Error()))
		}
	}()
	if err := extractArchive(archivePath, tmp); err != nil {
		return "", false, fmt.Errorf("backup: extract store: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	root, err := archiveRoot(tmp)
	if err != nil {
		return "", false, fmt.Errorf("backup: extract store: %s: %w", filepath.Base(archivePath), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false, fmt.Errorf("backup: extract store: %w", err)
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(root, e.Name()), filepath.Join(storeDir, e.Name())); err != nil {
			return "", false, fmt.Errorf("backup: extract store: %w", err)
		}
	}
	if err := syncTree(storeDir); err != nil {
		return "", false, fmt.Errorf("backup: extract store: sync %s: %w", storeDir, err)
	}
	return sum, verified, nil
}

// checkSidecar compares sum with the digest the sidecar beside archivePath
// records (ErrChecksumMismatch) and reports whether it checked: no sidecar
// is no check (false).
func checkSidecar(archivePath, sum string) (bool, error) {
	digest, present, err := sidecarDigest(archivePath)
	if err != nil {
		return false, err
	}
	if !present {
		extractLog.Warn("%s has no checksum sidecar; it is extracted unverified", archivePath)
		return false, nil
	}
	if !strings.EqualFold(digest, sum) {
		return false, fmt.Errorf("%w: %s: recorded %s, computed %s", ErrChecksumMismatch, filepath.Base(archivePath), digest, sum)
	}
	return true, nil
}

// sidecarDigest is the digest the checksum sidecar beside archivePath
// records, and whether the sidecar exists. A sidecar that exists but holds
// no digest is an error.
func sidecarDigest(archivePath string) (digest string, present bool, err error) {
	sidecar := checksumSidecarPath(archivePath)
	raw, err := os.ReadFile(sidecar)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("backup: read %s: %w", sidecar, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", false, fmt.Errorf("backup: checksum sidecar %s is empty or malformed", sidecar)
	}
	return fields[0], true, nil
}

// archiveRoot is the one top-level directory extracted into dir.
func archiveRoot(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return "", fmt.Errorf("the archive holds %v at its top level, not the one store directory a Pebble backup holds", names)
	}
	return filepath.Join(dir, entries[0].Name()), nil
}

// syncTree fsyncs every regular file and directory under root, root
// included, deepest first, so a directory is synced after the entries
// renamed or written into it.
func syncTree(root string) error {
	var paths []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(paths) - 1; i >= 0; i-- {
		f, err := os.Open(paths[i])
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return fmt.Errorf("%s: %w", paths[i], err)
		}
	}
	return nil
}
