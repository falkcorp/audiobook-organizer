// file: core/backup/offline.go
// version: 1.0.1
// guid: 6c1d6a52-0b0e-4c21-9c4e-2f7a3d1e5b94
// last-edited: 2026-11-12

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// LegacyIDPrefix starts the ID of a legacy-abo/0 archive: it has no manifest,
// so its ID is its file name's stem (catalogue, staged restore and offline
// restore all name one the same way).
const LegacyIDPrefix = "legacy-"

// IsLegacyID reports whether id names a legacy-abo/0 archive.
func IsLegacyID(id string) bool { return strings.HasPrefix(id, LegacyIDPrefix) }

// legacyIDOf is the catalogue ID of the legacy archive file called name: the
// name without its codec and tar extensions, after LegacyIDPrefix.
func legacyIDOf(name string) ArchiveID {
	stem := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".zst"), ".gz"), ".tar")
	return ArchiveID(LegacyIDPrefix + stem)
}

// dbPrefix is where a mediacore-backup/1 archive keeps the store directory.
const dbPrefix = "db"

// ExtractStore is the offline restore's archive extractor
// (kernel.Config.ArchiveExtract, 05-storage §5S.3.12): it extracts the store
// of archive into storeDir, an existing empty directory, and returns the
// archive's ID. It runs in the operator's process with the service stopped and
// opens nothing but the archive.
//
//   - A mediacore-backup/1 archive is read through its manifest: the manifest
//     sidecar, when present, must equal the archive's own first entry, every
//     entry must be listed and match its size and SHA-256 as it streams, and
//     only the entries under db/ are written, named without that prefix, so a
//     rehearsal copy (--into) never receives the config or boot-record
//     entries. The ID is the manifest's.
//   - Anything whose first entry is not a manifest is read as a legacy-abo/0
//     archive: one top-level store directory, scanned end to end before one
//     byte is written, then extracted with the top-level directory stripped.
//     The ID is "legacy-" and the file name's stem.
//
// A checksum sidecar (<archive>.sha256), when there is one, must match the
// archive's SHA-256 before anything is read as an archive; one that is
// unreadable or malformed refuses too. An archive with no sidecar is
// extracted as an operator naming the file by hand intends: the kernel then
// opens the extracted store read-only with this build's guard before it
// stages anything, so a store this build cannot open is never swapped in.
//
// Provenance is not checked here. The offline restore is the state
// directory's owner at a terminal with the service stopped and --accept-loss
// given (the kernel refuses anything else), which is the authority to replace
// the store by hand; the archive's MAC is the staged restore's gate against
// an archive that arrives through the API (StageRestore, 05-backup §5B.3.7).
//
// Every read is bounded by the package's default limits (archive size, entry
// count, entry and total size), symbolic links, special entries and sparse
// files are refused, the declared bytes must fit the free space of storeDir's
// filesystem before the first is written (and the extraction stops at them),
// and files are created 0600 in directories 0700.
func ExtractStore(ctx context.Context, archive, storeDir string) (id string, err error) {
	return extractStore(ctx, archive, storeDir, Disk{})
}

// extractStore is ExtractStore over disk, which answers the free-space check.
// The offline restore has no service and no settings, so the check keeps no
// headroom beyond the archive's declared bytes (margin 0): nothing else is
// running to starve, and an extraction that fills the volume to the byte still
// fits.
func extractStore(ctx context.Context, archive, storeDir string, disk Disk) (id string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(archive) || !filepath.IsAbs(storeDir) {
		return "", fmt.Errorf("backup: extract store: %q and %q must be absolute", archive, storeDir)
	}
	if err := requireEmptyDir(storeDir); err != nil {
		return "", err
	}
	lim := backuparchive.Limits{}
	if _, err := os.Lstat(archive + ChecksumSidecar); err == nil {
		if err := checkSumFile(ctx, archive); err != nil {
			return "", fmt.Errorf("backup: extract store: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	a, err := openArchive(archive, lim)
	switch {
	case err == nil:
		defer func() {
			if cerr := a.Close(); cerr != nil {
				id, err = "", errors.Join(err, fmt.Errorf("backup: extract store: %w", cerr))
			}
		}()
		return extractCoreStore(ctx, archive, a, storeDir, disk)
	case errors.Is(err, ErrManifest):
		return extractLegacyStore(ctx, archive, storeDir, lim, disk, err)
	default:
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
}

// requireEmptyDir refuses a storeDir that is not an existing, empty directory
// (a symbolic link to one included: the extractor is handed a real directory).
func requireEmptyDir(dir string) (err error) {
	lst, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("backup: extract store: %w", err)
	}
	if !lst.IsDir() {
		return fmt.Errorf("backup: extract store: %s is %s, not a directory", dir, lst.Mode().Type())
	}
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("backup: extract store: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.Readdirnames(1); err == nil {
		return fmt.Errorf("backup: extract store: %s is not empty", dir)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("backup: extract store: %w", err)
	}
	return nil
}

func extractCoreStore(ctx context.Context, archive string, a *openedArchive, storeDir string, disk Disk) (string, error) {
	if side, err := readBounded(archive+ManifestSidecar, MaxManifestBytes); err == nil {
		if !bytes.Equal(side, a.ManifestBytes) {
			return "", fmt.Errorf("backup: extract store: %w: the manifest sidecar differs from the archive's manifest", ErrManifest)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("backup: extract store: %w: %v", ErrManifest, err)
	}
	holdsStore := false
	for _, e := range a.Manifest.Entries {
		if strings.HasPrefix(e.Path, dbPrefix+"/") {
			holdsStore = true
			break
		}
	}
	if !holdsStore {
		return "", fmt.Errorf("backup: extract store: %w: the manifest lists no entry under %s/", ErrManifest, dbPrefix)
	}
	// Only the entries under db/ are written; their declared bytes must fit the
	// target's filesystem first, and the extractor stops at them.
	declared, err := declaredBytes(a.Manifest.Entries, dbPrefix+"/")
	if err != nil {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	if err := requireSpace(disk, 0, spaceClaim{dir: storeDir, bytes: declared}); err != nil {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	ex, err := backuparchive.NewExtractor(storeDir)
	if err != nil {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	ex.SetBudget(budgetOf(declared))
	cerr := a.consume(ctx, ex, dbPrefix)
	if serr := errors.Join(ex.Sync(), ex.Close()); cerr == nil {
		cerr = serr
	}
	if cerr != nil {
		return "", fmt.Errorf("backup: extract store: %w", cerr)
	}
	return string(a.Manifest.ArchiveID), nil
}

func extractLegacyStore(ctx context.Context, archive, storeDir string, lim backuparchive.Limits, disk Disk, why error) (string, error) {
	info, err := backuparchive.ScanLegacy(ctx, archive, lim)
	if err != nil {
		return "", fmt.Errorf("backup: extract store: %w", errors.Join(err, why))
	}
	if err := requireSpace(disk, 0, spaceClaim{dir: storeDir, bytes: uint64(info.Bytes)}); err != nil {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	if err := backuparchive.ExtractLegacy(ctx, archive, info, storeDir, lim); err != nil {
		return "", fmt.Errorf("backup: extract store: %w", err)
	}
	id := legacyIDOf(filepath.Base(archive))
	if !ValidArchiveID(string(id)) {
		// A name with a dot or a space is not an ID the catalogue could address;
		// the digest names the archive instead.
		id = ArchiveID(LegacyIDPrefix + info.SHA256[:16])
	}
	return string(id), nil
}
