// file: internal/backup/backup.go
// version: 1.16.0
// guid: 8f9e0a1b-2c3d-4e5f-6a7b-8c9d0e1f2a3b
// last-edited: 2026-08-29

package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/diskstats"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

// Checkpointable is satisfied by PebbleStore. The backup handler type-asserts
// to this interface at runtime; callers without PebbleStore fall back to the
// live-file copy path. Not part of the main database.Store interface to avoid
// propagating to every mock.
type Checkpointable interface {
	// Checkpoint writes a consistent snapshot of the database to destDir.
	// destDir must not exist.
	Checkpoint(destDir string) error
}

// BackupInfo contains information about a backup
type BackupInfo struct {
	Filename     string    `json:"filename"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	Checksum     string    `json:"checksum"`
	DatabaseType string    `json:"database_type"`
	CreatedAt    time.Time `json:"created_at"`
}

// Progress phases reported through BackupConfig.Progress. A phase is entered
// once and then re-reported as it advances; a caller that only cares about
// "something moved" can ignore the name entirely.
const (
	// PhaseCheckpoint: flushing and hard-linking the Pebble snapshot. Reported
	// once on entry and once on completion — Checkpoint() is opaque to us, so
	// there is nothing honest to report in between.
	PhaseCheckpoint = "checkpoint"
	// PhaseArchive: walking the snapshot and writing tar+gzip. Reported per
	// file, so filesDone/bytesDone strictly increase while real work happens.
	PhaseArchive = "archive"
	// PhaseChecksum: SHA-256 over the finished archive. Reported per chunk.
	PhaseChecksum = "checksum"
)

// BackupProgress receives evidence that a backup is making forward progress.
//
// WHY THE COUNTERS AND NOT JUST A TICK: the caller for this is the pre-organize
// auto-backup, whose whole problem is that the ops-registry watchdog CANCELS an
// operation that goes ProgressTimeout (default 5m) without an UpdateProgress
// stamp — see internal/operations/registry/watchdog.go. A ticker that stamps on
// a timer would satisfy the watchdog whether or not the backup was alive, which
// turns a hang detector into a hang concealer. filesDone and bytesDone come off
// the actual archive walk, so a stalled backup stops producing them and the
// watchdog still fires.
//
// Optional; nil disables reporting. Called synchronously on the backup's
// goroutine, so implementations must not block — throttle inside the callback.
type BackupProgress func(phase string, filesDone int, bytesDone int64)

// BackupConfig holds backup configuration
type BackupConfig struct {
	BackupDir  string
	MaxBackups int
	// MaxTotalBytes bounds the COMBINED size of retained archives. Zero means
	// unlimited.
	//
	// MaxBackups alone is not a usable bound. It was set to 10 when an archive
	// was 247 MB (2.5 GB retained). Production archives are now ~15 GB, so the
	// same policy targets 150 GB -- on a 141 GB filesystem. On 2026-08-29 that
	// filled the disk, PebbleDB took a fatal commit error writing its WAL to
	// the same filesystem, and the process died and was restarted by systemd
	// every ~17 minutes for hours. A count bound cannot express "do not consume
	// the disk" when the thing being counted grows 60x.
	MaxTotalBytes    uint64
	CompressionLevel int
	// Progress is optional. See BackupProgress.
	Progress BackupProgress
}

// DefaultBackupConfig returns default backup configuration
// ResolveDir decides where backups are written.
//
// `configured` is the user's backup_dir setting; `dbPath` is the database file
// or directory. An absolute configured path wins outright. Anything else falls
// back to the historical behaviour -- a "backups" directory beside the database
// -- so an unset config behaves exactly as before.
//
// This exists because the same `if !filepath.IsAbs(...) { join(dir(dbPath)) }`
// was copy-pasted at five call sites (the organizer's auto-backup and four
// handlers). Five copies of a path rule is five chances for the create path and
// the list path to disagree about where backups live, which would surface as
// "the backup succeeded but the list is empty". One resolver, one answer.
//
// A relative configured path is deliberately NOT resolved against the process
// working directory: for a service that is wherever systemd happened to start
// it, which is never a location a person meant to fill with 15 GB archives.
func ResolveDir(configured, dbPath string) string {
	if filepath.IsAbs(configured) {
		return configured
	}
	dir := configured
	if dir == "" {
		dir = "backups"
	}
	if dbPath == "" {
		return dir
	}
	return filepath.Join(filepath.Dir(dbPath), dir)
}

func DefaultBackupConfig() BackupConfig {
	return BackupConfig{
		BackupDir:        "backups",
		MaxBackups:       10,
		MaxTotalBytes:    defaultMaxTotalBytes,
		CompressionLevel: gzip.BestCompression,
	}
}

// ResolveMaxTotalBytes turns the configured backup budget into the value
// enforceRetention expects.
//
// It translates between two DIFFERENT zero conventions, which is why it exists
// as a named function rather than an inline cast at each call site:
//
//	config value  0  -> "not configured", so the built-in default applies
//	config value <0  -> unlimited, matching MaxBackups' negative convention
//	config value >0  -> that many bytes
//
// BackupConfig.MaxTotalBytes uses 0 for UNLIMITED. Passing a config zero
// straight through would therefore turn "the operator never set this" into "keep
// archives without bound" -- the same shape of defect as
// chapter_consolidation_threshold_min, where an unset zero silently became a
// permanent behaviour change nobody chose. The translation is deliberate and
// belongs in exactly one place.
func ResolveMaxTotalBytes(configured int64) uint64 {
	switch {
	case configured < 0:
		return 0 // unlimited
	case configured == 0:
		return defaultMaxTotalBytes
	default:
		return uint64(configured)
	}
}

// defaultMaxTotalBytes caps retained archives at 40 GiB. Sized so that the
// production database (~16 GB compressed) keeps two full generations plus room
// for an incoming third, while leaving a 141 GB filesystem far from full.
const defaultMaxTotalBytes uint64 = 40 << 30

// backupSpaceMargin is the headroom required BEYOND the estimated archive size
// before a backup is allowed to start.
//
// It is not arbitrary padding. The database being archived is live: Pebble is
// still writing its WAL and running compactions on the same filesystem for the
// whole duration of the archive (20-25 minutes on production). Sizing the check
// at exactly the archive size would let the backup finish and still leave Pebble
// with nothing to write into, which is the failure this guard exists to prevent.
const backupSpaceMargin uint64 = 2 << 30

// diskStatsFn is a seam so tests can drive the guard without needing a real
// full filesystem. Production always uses diskstats.Stats.
var diskStatsFn = diskstats.Stats

// ErrInsufficientDiskSpace is returned when a backup would not fit.
//
// It is deliberately a normal error, not a panic or a fatal: every caller
// (notably organizer.autoBackup) already logs a failed backup and continues.
// Skipping a backup is a bad outcome; filling the disk and killing the database
// the backup was protecting is a far worse one.
var ErrInsufficientDiskSpace = errors.New("insufficient disk space for backup")

// dirSizeBytes sums the regular files under root.
//
// Used as the archive-size estimate. That is not pessimism: a Pebble database
// is mostly already-compressed SST data, and production measured a 16 GB
// database producing a ~15 GB gzip archive -- close enough to 1:1 that assuming
// compression will save the day is how the disk filled in the first place.
// Files that vanish mid-walk (Pebble compaction deletes SSTs constantly) are
// skipped rather than failing the estimate.
func dirSizeBytes(root string) (uint64, error) {
	var total uint64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// ensureSpaceForBackup refuses the backup unless the destination filesystem can
// hold the estimated archive plus backupSpaceMargin.
//
// Fails CLOSED when free space cannot be determined. That is the deliberate
// choice: this guard exists because writing blind is what killed production, so
// "I could not measure the disk" is treated as "do not write to it". The cost of
// being wrong is a skipped backup with a loud warning; the cost of the other
// direction is the outage this function was written for.
func ensureSpaceForBackup(destDir, sourceDir string) error {
	need, err := dirSizeBytes(sourceDir)
	if err != nil {
		return fmt.Errorf("%w: cannot size %s: %v", ErrInsufficientDiskSpace, sourceDir, err)
	}
	need += backupSpaceMargin

	_, free, err := diskStatsFn(destDir)
	if err != nil {
		return fmt.Errorf("%w: cannot determine free space on %s: %v", ErrInsufficientDiskSpace, destDir, err)
	}
	if free < need {
		return fmt.Errorf("%w: %s needs ~%s (archive %s + %s margin) but only %s is free",
			ErrInsufficientDiskSpace, destDir,
			formatBytes(need), formatBytes(need-backupSpaceMargin),
			formatBytes(backupSpaceMargin), formatBytes(free))
	}
	return nil
}

// formatBytes renders a byte count for operator-facing messages.
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// CreateBackup creates a compressed backup of the database
func CreateBackup(databasePath, databaseType string, config BackupConfig) (*BackupInfo, error) {
	// Ensure backup directory exists
	if err := os.MkdirAll(config.BackupDir, 0775); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Retention runs BEFORE the write, not only after it.
	//
	// It used to run only on success (at the end of this function). That is
	// exactly backwards: when the disk is full the write fails, so the prune
	// that would have freed room never executed, and every retry refilled the
	// disk. Pruning first means a backup that only fits after retention can
	// actually be taken. incoming is passed so retention accounts for the
	// archive that is about to be written, not just the ones already there.
	incoming, sizeErr := dirSizeBytes(databasePath)
	if sizeErr != nil {
		incoming = 0
	}
	if err := enforceRetention(config.BackupDir, config.MaxBackups, config.MaxTotalBytes, incoming); err != nil {
		slog.Warn("backup pre-write retention failed", "error", err)
	}

	// Refuse rather than filling the filesystem the live database writes to.
	if err := ensureSpaceForBackup(config.BackupDir, databasePath); err != nil {
		return nil, err
	}

	// Generate backup filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	backupFilename := fmt.Sprintf("audiobooks_%s_%s.tar.gz", databaseType, timestamp)
	backupPath := filepath.Join(config.BackupDir, backupFilename)

	// Create backup file
	backupFile, err := os.Create(backupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup file: %w", err)
	}
	defer backupFile.Close()

	// Create gzip writer
	gzipWriter, err := gzip.NewWriterLevel(backupFile, config.CompressionLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip writer: %w", err)
	}
	defer gzipWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	// Add database files to archive
	if err := addToArchive(tarWriter, databasePath, databaseType, config.Progress); err != nil {
		os.Remove(backupPath) // Clean up on failure
		return nil, fmt.Errorf("failed to add files to archive: %w", err)
	}

	// Close writers to ensure all data is flushed
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close tar writer: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}
	if err := backupFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close backup file: %w", err)
	}

	// Get backup file info
	fileInfo, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat backup file: %w", err)
	}

	// Calculate checksum
	checksum, err := calculateFileChecksum(backupPath, config.Progress)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate checksum: %w", err)
	}

	info := &BackupInfo{
		Filename:     backupFilename,
		Path:         backupPath,
		Size:         fileInfo.Size(),
		Checksum:     checksum,
		DatabaseType: databaseType,
		CreatedAt:    time.Now(),
	}

	// Clean up old backups
	if err := enforceRetention(config.BackupDir, config.MaxBackups, config.MaxTotalBytes, 0); err != nil {
		// Log error but don't fail the backup
		slog.Warn("backup failed to clean up old backups", "error", err)
	}

	return info, nil
}

// CreateBackupWithCheckpoint creates a consistent PebbleDB backup via
// Checkpoint(destDir) instead of a live filepath.Walk. The Checkpoint API
// flushes all in-flight writes and hard-links SST files into destDir, so the
// archive is always internally consistent. Falls back to CreateBackup (live
// walk) if the store does not implement Checkpointable (e.g. in tests).
//
// dbSourcePath is the original database path. Its basename is used as the root
// entry in the tar archive so that restoring the archive recreates a directory
// named after the source DB (e.g. "audiobooks.pebble/") rather than the random
// checkpoint temp-dir name ("pebble-checkpoint-XYZ/").
func CreateBackupWithCheckpoint(store Checkpointable, dbSourcePath, databaseType string, config BackupConfig) (*BackupInfo, error) {
	if err := os.MkdirAll(config.BackupDir, 0775); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	// Retention MUST run before the space check, not after it.
	//
	// This is the production path (Pebble), and getting the order wrong here
	// made the space guard a one-way door. Observed in production on
	// 2026-08-29: the guard correctly refused a backup ("needs ~32.3 GiB but
	// only 758.6 MiB is free") and so kept the database alive -- but because
	// the refusal happened before retention, the 101 GB of old archives that
	// caused the shortage were never pruned. The system was stable and
	// permanently stuck: every subsequent attempt refused for the same reason,
	// and nothing could free the space except a human with rm.
	//
	// CreateBackup below already prunes before its own write, but on this path
	// it is never reached. A second call there is harmless -- retention finds
	// nothing over budget the second time.
	incoming, sizeErr := dirSizeBytes(dbSourcePath)
	if sizeErr != nil {
		incoming = 0
	}
	if err := enforceRetention(config.BackupDir, config.MaxBackups, config.MaxTotalBytes, incoming); err != nil {
		slog.Warn("backup pre-checkpoint retention failed", "error", err)
	}

	// Checked here as well as in CreateBackup below. The checkpoint itself is
	// cheap (Checkpoint hard-links SSTs, it does not copy them), but running it
	// on a filesystem that cannot hold the resulting archive means doing the
	// flush and the link work only to fail at the write. Checking first also
	// keeps the failure message about the real cause -- free space -- rather
	// than surfacing as an opaque checkpoint error.
	if err := ensureSpaceForBackup(config.BackupDir, dbSourcePath); err != nil {
		return nil, err
	}

	// Create a temp directory for the checkpoint.
	tmpDir, err := os.MkdirTemp(config.BackupDir, "pebble-checkpoint-*")
	if err != nil {
		return nil, fmt.Errorf("create temp checkpoint dir: %w", err)
	}
	// Pebble requires the destination not to exist — remove the empty dir
	// it was created with (MkdirTemp creates it), then let Checkpoint re-create it.
	if err := os.Remove(tmpDir); err != nil {
		return nil, fmt.Errorf("remove pre-created checkpoint tmp dir: %w", err)
	}

	// Use a closure so the defer always removes whichever path is current.
	cleanupDir := tmpDir
	defer func() { os.RemoveAll(cleanupDir) }()

	// Bracket the checkpoint rather than reporting inside it: Checkpoint() is
	// opaque to us, so a stamp during it would be a guess. Bracketing still
	// tells the caller which phase is running when it goes quiet, which is the
	// difference between "the backup is hung" and "the backup is hung IN THE
	// CHECKPOINT".
	if config.Progress != nil {
		config.Progress(PhaseCheckpoint, 0, 0)
	}
	if err := store.Checkpoint(tmpDir); err != nil {
		return nil, fmt.Errorf("pebble checkpoint: %w", err)
	}
	if config.Progress != nil {
		config.Progress(PhaseCheckpoint, 1, 0)
	}

	// Rename checkpoint dir to source DB basename so tar archive entries use
	// the expected name. Restoring to a target then creates "target/test.pebble/"
	// rather than "target/pebble-checkpoint-XYZ/".
	if base := filepath.Base(dbSourcePath); base != "" && base != "." {
		named := filepath.Join(filepath.Dir(tmpDir), base)
		if renErr := os.Rename(tmpDir, named); renErr == nil {
			cleanupDir = named
			tmpDir = named
		}
	}

	return CreateBackup(tmpDir, databaseType, config)
}

// RestoreBackup restores a database from a backup file
func RestoreBackup(backupPath, targetPath string, verify bool) error {
	// Verify checksum if requested
	if verify {
		// TODO: Store checksums in metadata file and verify
		slog.Info("backup checksum verification not yet implemented")
	}

	// Open backup file
	backupFile, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer backupFile.Close()

	// Create gzip reader
	gzipReader, err := gzip.NewReader(backupFile)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	// Create tar reader
	tarReader := tar.NewReader(gzipReader)

	// Extract files
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Normalise the archive entry name: strip any leading slashes so that
		// absolute-path entries (e.g. "/etc/passwd") are treated as relative
		// paths inside the target directory — the same behaviour as
		// filepath.Join(root, "/etc/passwd") on Unix, but explicit.
		entryName := strings.TrimLeft(filepath.ToSlash(header.Name), "/")
		if entryName == "" {
			continue
		}
		// safepath.Join validates that the entry stays within targetPath and
		// returns a clean path value — breaking the CodeQL taint chain.
		targetSP, err := safepath.Join(targetPath, entryName)
		if err != nil {
			return fmt.Errorf("archive entry %q escapes target directory: %w", header.Name, err)
		}
		target := targetSP.String()

		// Handle directories and files
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0775); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0775); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %w", target, err)
			}

			// Create file
			outFile, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			// Copy data
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}

			outFile.Close()

			// Set file permissions
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("failed to set permissions on %s: %w", target, err)
			}
		default:
			slog.Warn("backup unsupported file type", "type", header.Typeflag, "name", header.Name)
		}
	}

	return nil
}

// ListBackups returns the archives in backupDir WITHOUT computing checksums.
//
// Checksumming used to happen here, unconditionally, for every archive on every
// call -- and nothing read the result. That made an O(bytes-on-disk) read the
// price of merely LISTING files, which is O(entries) work everywhere else in
// this codebase.
//
// It was not a theoretical cost. Measured on production 2026-08-29 with ~16 GB
// of archives, GET /api/v1/backup/list did not return within two minutes. Worse,
// enforceRetention calls this on EVERY backup, so each run hashed the entire
// backup directory before it could decide anything: with 101 GB of archives
// present, the auto-backup that logged "failed after 18m38s" spent essentially
// all of that time hashing files in order to report a disk-space refusal it
// could have made instantly.
//
// Callers that genuinely need checksums ask for them explicitly via
// ListBackupsWithChecksums. Verification is a deliberate act; listing is not.
func ListBackups(backupDir string) ([]BackupInfo, error) {
	return listBackups(backupDir, false)
}

// ListBackupsWithChecksums is ListBackups plus a SHA-256 for each archive.
//
// This READS EVERY BYTE of every archive in the directory. At the sizes this
// application produces (~15 GB per archive) that is minutes of I/O, so it
// belongs behind an explicit request from someone who wants integrity
// verification -- never on a listing or a retention path.
func ListBackupsWithChecksums(backupDir string) ([]BackupInfo, error) {
	return listBackups(backupDir, true)
}

func listBackups(backupDir string, withChecksums bool) ([]BackupInfo, error) {
	var backups []BackupInfo

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return backups, nil // No backups directory yet
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar.gz") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		backupPath := filepath.Join(backupDir, entry.Name())
		var checksum string
		if withChecksums {
			checksum, _ = calculateFileChecksum(backupPath, nil)
		}

		// Parse database type from filename
		dbType := "unknown"
		if strings.Contains(entry.Name(), "_pebble_") {
			dbType = "pebble"
		} else if strings.Contains(entry.Name(), "_sqlite_") {
			dbType = "sqlite"
		}

		backups = append(backups, BackupInfo{
			Filename:     entry.Name(),
			Path:         backupPath,
			Size:         info.Size(),
			Checksum:     checksum,
			DatabaseType: dbType,
			CreatedAt:    info.ModTime(),
		})
	}

	return backups, nil
}

// DeleteBackup deletes a specific backup file
func DeleteBackup(backupPath string) error {
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("failed to delete backup: %w", err)
	}
	return nil
}

// addToArchive adds a database path to a tar archive
func addToArchive(tarWriter *tar.Writer, path, dbType string, progress BackupProgress) error {
	// Check if path is a directory (PebbleDB) or file (SQLite)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat database path: %w", err)
	}

	// filesDone/bytesDone are the forward-progress evidence handed to the
	// caller. They only advance when a file has actually been written into the
	// archive, which is the whole point — see BackupProgress.
	filesDone := 0
	var bytesDone int64

	if info.IsDir() {
		// PebbleDB - archive entire directory
		root := path
		return filepath.Walk(root, func(file string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Validate that the file really lies within the root directory.
			sp, err := safepath.Validate(root, file)
			if err != nil {
				return fmt.Errorf("safepath validation failed for %q: %w", file, err)
			}

			// Create tar header
			header, err := tar.FileInfoHeader(fi, fi.Name())
			if err != nil {
				return err
			}

			// Use a sanitized, relative path in the archive that does not contain
			// any parent-traversal components.
			relPath, err := filepath.Rel(root, sp.String())
			if err != nil {
				return err
			}
			relPath = filepath.Clean(relPath)
			if relPath == "." {
				header.Name = filepath.ToSlash(filepath.Base(root))
			} else {
				// header.Name must use forward slashes per TAR spec
				header.Name = filepath.ToSlash(filepath.Join(filepath.Base(root), relPath))
			}

			// Write header
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}

			// Write file content if not a directory
			if !fi.IsDir() {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()

				written, err := io.Copy(tarWriter, f)
				if err != nil {
					return err
				}
				filesDone++
				bytesDone += written
				if progress != nil {
					progress(PhaseArchive, filesDone, bytesDone)
				}
			}

			return nil
		})
	} else {
		// SQLite - archive single file
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.Base(path)

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		written, err := io.Copy(tarWriter, file)
		if err != nil {
			return err
		}
		filesDone++
		bytesDone += written
		if progress != nil {
			progress(PhaseArchive, filesDone, bytesDone)
		}
		return nil
	}
}

// calculateFileChecksum calculates SHA256 checksum of a file
func calculateFileChecksum(path string, progress BackupProgress) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if progress == nil {
		if _, err := io.Copy(hash, file); err != nil {
			return "", err
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}

	// Hash in chunks so the caller keeps hearing from us. On production this
	// reads a 14 GB archive, long enough on its own to trip a 5-minute
	// progress watchdog if it were a single opaque io.Copy.
	const checksumChunk = 32 << 20 // 32 MiB
	var done int64
	buf := make([]byte, checksumChunk)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, werr := hash.Write(buf[:n]); werr != nil {
				return "", werr
			}
			done += int64(n)
			progress(PhaseChecksum, 0, done)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// enforceRetention removes old archives until both the count bound
// (maxBackups) and the total-size bound (maxTotalBytes) are satisfied,
// oldest first. incomingBytes is the size of an archive about to be written,
// so a pre-write call reserves room for it; pass 0 after the write.
func enforceRetention(backupDir string, maxBackups int, maxTotalBytes, incomingBytes uint64) error {
	backups, err := ListBackups(backupDir)
	if err != nil {
		return err
	}

	// Oldest first -- the deletion order.
	sort.SliceStable(backups, func(a, b int) bool {
		return backups[a].CreatedAt.Before(backups[b].CreatedAt)
	})

	var total uint64
	for _, b := range backups {
		if b.Size > 0 {
			total += uint64(b.Size)
		}
	}

	// overBudget reports whether the archives remaining from index i onward
	// still violate either bound, counting the archive about to be written.
	//
	// The two bounds use DIFFERENT zero conventions, and that asymmetry is
	// deliberate rather than an oversight:
	//
	//   - maxBackups == 0 means "keep none" (delete every archive). This is
	//     pre-existing behaviour that TestCreateBackupMaxBackupsZero pins, so
	//     it is preserved; a NEGATIVE value means unlimited. The old
	//     implementation computed len(backups)-maxBackups as a loop bound, so a
	//     negative value indexed past the slice -- unlimited now short-circuits
	//     instead of panicking.
	//   - maxTotalBytes == 0 means unlimited, the ordinary Go zero-value
	//     convention for a newly added optional bound. Making it mean "keep no
	//     bytes" would turn every BackupConfig literal that predates this field
	//     into a silent delete-everything.
	// `remaining` is the number of archives ACTUALLY still on disk, which is not
	// the same as the loop index. A delete can fail -- the backup directory now
	// lives outside the application's own tree, where a sticky bit or foreign
	// ownership can make an archive unremovable by the service account. Deriving
	// `remaining` from the loop index would count such a file as freed, so
	// retention would stop early believing it had reclaimed a slot it had not,
	// and the count bound would drift permanently out of step with the disk.
	overBudget := func(remaining int) bool {
		if maxBackups >= 0 && remaining+boolToInt(incomingBytes > 0) > maxBackups {
			return true
		}
		if maxTotalBytes > 0 && total+incomingBytes > maxTotalBytes {
			return true
		}
		return false
	}

	// The byte bound can demand more room than deleting everything provides.
	// Production, measured 2026-08-29: a 30.3 GiB incoming archive against a
	// 40 GiB budget leaves 9.7 GiB of headroom, and every existing archive is
	// ~15 GB -- so `overBudget` stays true after each deletion and the loop
	// runs to the end, destroying the entire backup history to make room for
	// one new archive. If that new write then fails (which is exactly the
	// situation retention is being asked to rescue), the result is ZERO
	// backups.
	//
	// So the last archive is a floor, not a candidate. Retention exists to stop
	// backups consuming the disk; it must never be the thing that leaves the
	// database with no backup at all. An over-budget state with one archive
	// left is reported and kept rather than "resolved" by deleting it.
	//
	// maxBackups == 0 is the one exception: that means "keep none" explicitly,
	// and TestCreateBackupMaxBackupsZero pins it.
	floor := 1
	if maxBackups == 0 {
		floor = 0
	}

	remaining := len(backups)
	for i := 0; i < len(backups)-floor && overBudget(remaining); i++ {
		if err := os.Remove(backups[i].Path); err != nil {
			// Deliberately do NOT decrement `remaining` or `total`: the archive
			// is still occupying the directory and the budget.
			slog.Warn("backup failed to delete old backup", "filename", backups[i].Filename, "error", err)
			continue
		}
		remaining--
		if backups[i].Size > 0 {
			total -= uint64(backups[i].Size)
		}
		slog.Info("backup retention removed old archive",
			"filename", backups[i].Filename, "size", backups[i].Size, "remaining_bytes", total)
	}

	// Say so plainly. A silent "retention ran" hides the fact that the bound is
	// unsatisfiable, which is a configuration problem a human has to resolve --
	// either a larger budget or a backup directory that is not on the database's
	// own filesystem.
	if floor > 0 && len(backups) > 0 && overBudget(remaining) {
		slog.Warn("backup retention cannot satisfy its budget without deleting the last archive; keeping it",
			"max_total_bytes", maxTotalBytes, "incoming_bytes", incomingBytes, "retained_bytes", total)
	}

	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ScheduleBackup schedules automatic backups (placeholder for future implementation)
func ScheduleBackup(interval time.Duration, config BackupConfig) error {
	// TODO: Implement scheduled backups using a ticker
	// This would run in a goroutine and create backups at regular intervals
	return fmt.Errorf("scheduled backups not yet implemented")
}

// BackupDatabase is a convenience function that backs up the database
// referenced by the supplied store. Currently a stub — kept around because
// tests exercise the nil-store error path, and a future implementation
// will add Path()/Type() accessors to the Store interface.
//
// Signature takes an explicit database.Store (SERVER-GLOBAL-STORE-AUDIT
// phase 2). Pass nil to exercise the "database not initialized" path.
// The store parameter is `any` because this stub only nil-checks it. It took
// database.Store -- 398 methods -- to call none of them. Give it the interface
// it actually needs if the real implementation lands.
func BackupDatabase(store any, config BackupConfig) (*BackupInfo, error) {
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// TODO: Add methods to Store interface to get database path and type.
	// For now, we'll need to pass these as parameters via BackupConfig.

	return nil, fmt.Errorf("backup requires database path and type information")
}
