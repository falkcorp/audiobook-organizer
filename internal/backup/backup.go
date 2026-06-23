// file: internal/backup/backup.go
// version: 1.5.0
// guid: 8f9e0a1b-2c3d-4e5f-6a7b-8c9d0e1f2a3b
// last-edited: 2026-06-23

package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// BackupConfig holds backup configuration
type BackupConfig struct {
	BackupDir        string
	MaxBackups       int
	CompressionLevel int
}

// DefaultBackupConfig returns default backup configuration
func DefaultBackupConfig() BackupConfig {
	return BackupConfig{
		BackupDir:        "backups",
		MaxBackups:       10,
		CompressionLevel: gzip.BestCompression,
	}
}

// CreateBackup creates a compressed backup of the database
func CreateBackup(databasePath, databaseType string, config BackupConfig) (*BackupInfo, error) {
	// Ensure backup directory exists
	if err := os.MkdirAll(config.BackupDir, 0775); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
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
	if err := addToArchive(tarWriter, databasePath, databaseType); err != nil {
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
	checksum, err := calculateFileChecksum(backupPath)
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
	if err := cleanupOldBackups(config.BackupDir, config.MaxBackups); err != nil {
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
func CreateBackupWithCheckpoint(store Checkpointable, databaseType string, config BackupConfig) (*BackupInfo, error) {
	if err := os.MkdirAll(config.BackupDir, 0775); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
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
	defer os.RemoveAll(tmpDir)

	if err := store.Checkpoint(tmpDir); err != nil {
		return nil, fmt.Errorf("pebble checkpoint: %w", err)
	}

	return CreateBackup(tmpDir, databaseType, config)
}

// isPathWithinTarget reports whether entryPath, when joined with targetPath,
// stays inside targetPath. Used by unit tests to verify zipslip-rejection
// logic; RestoreBackup uses safepath.Join for the same guarantee.
func isPathWithinTarget(targetPath, entryPath string) (bool, error) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false, err
	}
	candidate := filepath.Clean(filepath.Join(absTarget, entryPath))
	rel, err := filepath.Rel(absTarget, candidate)
	if err != nil {
		return false, err
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return false, nil
	}
	return true, nil
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

// ListBackups lists all available backups
func ListBackups(backupDir string) ([]BackupInfo, error) {
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
		checksum, _ := calculateFileChecksum(backupPath)

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
func addToArchive(tarWriter *tar.Writer, path, dbType string) error {
	// Check if path is a directory (PebbleDB) or file (SQLite)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat database path: %w", err)
	}

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

				if _, err := io.Copy(tarWriter, f); err != nil {
					return err
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

		_, err = io.Copy(tarWriter, file)
		return err
	}
}

// calculateFileChecksum calculates SHA256 checksum of a file
func calculateFileChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// cleanupOldBackups removes old backups exceeding the maximum count
func cleanupOldBackups(backupDir string, maxBackups int) error {
	backups, err := ListBackups(backupDir)
	if err != nil {
		return err
	}

	if len(backups) <= maxBackups {
		return nil
	}

	// Sort backups by creation time (oldest first)
	// Simple bubble sort since list is typically small
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].CreatedAt.After(backups[j].CreatedAt) {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// Delete oldest backups
	deleteCount := len(backups) - maxBackups
	for i := 0; i < deleteCount; i++ {
		if err := os.Remove(backups[i].Path); err != nil {
			slog.Warn("backup failed to delete old backup", "filename", backups[i].Filename, "error", err)
		}
	}

	return nil
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
func BackupDatabase(store database.Store, config BackupConfig) (*BackupInfo, error) {
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// TODO: Add methods to Store interface to get database path and type.
	// For now, we'll need to pass these as parameters via BackupConfig.

	return nil, fmt.Errorf("backup requires database path and type information")
}
