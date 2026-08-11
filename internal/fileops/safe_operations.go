// file: internal/fileops/safe_operations.go
// version: 1.1.0
// guid: 8f7e6d5c-4b3a-2918-7f6e-5d4c3b2a1908
// last-edited: 2026-08-11

package fileops

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

// OperationConfig configures safe file operation behavior
type OperationConfig struct {
	// BackupDir is the directory where backups are stored
	BackupDir string
	// VerifyChecksums enables SHA256 verification after operations
	VerifyChecksums bool
	// PreserveOriginal keeps the original file even after successful operation
	PreserveOriginal bool
	// MaxBackups limits the number of backups to keep per file
	MaxBackups int
}

// DefaultConfig returns the default safe operation configuration
func DefaultConfig() OperationConfig {
	return OperationConfig{
		BackupDir:        ".audiobook-backups",
		VerifyChecksums:  true,
		PreserveOriginal: false,
		MaxBackups:       5,
	}
}

// FileOperation represents a file operation with rollback capability
type FileOperation struct {
	config       OperationConfig
	originalPath string
	targetPath   string
	backupPath   string
	completed    bool
	originalHash string
	targetHash   string
}

// NewFileOperation creates a new safe file operation
func NewFileOperation(originalPath, targetPath string, config OperationConfig) (*FileOperation, error) {
	// Ensure backup directory exists
	backupDir := config.BackupDir
	if !filepath.IsAbs(backupDir) {
		// Make it absolute relative to the target directory
		sp, err := safepath.Join(filepath.Dir(targetPath), backupDir)
		if err != nil {
			return nil, fmt.Errorf("invalid backup directory: %w", err)
		}
		backupDir = sp.String()
	}
	if err := os.MkdirAll(backupDir, 0775); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate backup path with timestamp
	timestamp := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("%s.%s.backup", filepath.Base(targetPath), timestamp)
	spBackupPath, err := safepath.Join(backupDir, backupName)
	if err != nil {
		return nil, fmt.Errorf("invalid backup path: %w", err)
	}
	backupPath := spBackupPath.String()

	op := &FileOperation{
		config:       config,
		originalPath: originalPath,
		targetPath:   targetPath,
		backupPath:   backupPath,
		completed:    false,
	}

	// Calculate original file checksum if verification enabled
	if config.VerifyChecksums {
		hash, err := calculateChecksum(originalPath)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate checksum: %w", err)
		}
		op.originalHash = hash
	}

	return op, nil
}

// Execute performs the file operation with copy-first logic
func (op *FileOperation) Execute() error {
	// Step 1: If target exists, back it up
	targetExists := false
	if _, err := os.Stat(op.targetPath); err == nil {
		targetExists = true
		if err := copyFile(op.targetPath, op.backupPath); err != nil {
			return fmt.Errorf("failed to backup existing file: %w", err)
		}
	}

	// Step 2: Copy source to target
	if err := copyFile(op.originalPath, op.targetPath); err != nil {
		// Rollback: restore from backup if it exists.
		//
		// A discarded rollback error is WORSE than no rollback, because the
		// error we return then tells the caller a different story than what is
		// on disk. By this point copyFile has already begun writing
		// op.targetPath, so on failure the target is partially overwritten. If
		// the restore ALSO fails and we swallow it, the caller (organizer,
		// iTunes importer) reads "failed to copy file" — which sounds like
		// nothing happened — and moves on, leaving a truncated audiobook at
		// targetPath and the user's only intact copy orphaned at backupPath
		// with nothing pointing at it.
		if _, statErr := os.Stat(op.backupPath); statErr == nil {
			if rbErr := copyFile(op.backupPath, op.targetPath); rbErr != nil {
				slog.Error("ROLLBACK FAILED: target may be corrupt and the only intact copy is the backup",
					"target", op.targetPath, "backup", op.backupPath,
					"copy_error", err, "rollback_error", rbErr)
				return fmt.Errorf("failed to copy file: %w; ROLLBACK ALSO FAILED: %s may be corrupt — the intact copy is at %s: %w",
					err, op.targetPath, op.backupPath, rbErr)
			}
		}
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Step 2.5: Create a backup of the source if PreserveOriginal is true and target didn't exist
	if op.config.PreserveOriginal && !targetExists {
		if err := copyFile(op.originalPath, op.backupPath); err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}
	}

	// Step 3: Verify checksums if enabled
	if op.config.VerifyChecksums {
		targetHash, err := calculateChecksum(op.targetPath)
		if err != nil {
			return fmt.Errorf("failed to verify target checksum: %w", err)
		}
		op.targetHash = targetHash

		if op.originalHash != op.targetHash {
			// Rollback: restore from backup.
			//
			// This branch is reached having just PROVEN the target is corrupt.
			// Discarding the restore error here meant the code could establish
			// corruption, attempt recovery, ignore whether recovery worked, and
			// then return "operation failed integrity check" — which reads as
			// "we refused to do it", not "there is a known-bad file on disk".
			if _, statErr := os.Stat(op.backupPath); statErr == nil {
				if rbErr := copyFile(op.backupPath, op.targetPath); rbErr != nil {
					slog.Error("ROLLBACK FAILED after checksum mismatch: a known-corrupt file is left in place",
						"target", op.targetPath, "backup", op.backupPath,
						"original_hash", op.originalHash, "target_hash", op.targetHash,
						"rollback_error", rbErr)
					return fmt.Errorf("checksum mismatch: operation failed integrity check; ROLLBACK ALSO FAILED: %s is known-corrupt and was NOT restored — the intact copy is at %s: %w",
						op.targetPath, op.backupPath, rbErr)
				}
			}
			return fmt.Errorf("checksum mismatch: operation failed integrity check")
		}
	}

	op.completed = true

	// Step 4: Clean up based on config
	if !op.config.PreserveOriginal && op.originalPath != op.targetPath {
		// Only remove original if it's different from target
		if err := os.Remove(op.originalPath); err != nil {
			// Non-fatal: log but don't fail the operation
			slog.Warn("failed to remove original file", "path", op.originalPath, "error", err)
		}
	}

	// Step 5: Cleanup old backups if limit exceeded
	if err := op.cleanupOldBackups(); err != nil {
		// Non-fatal: log but don't fail the operation
		slog.Warn("failed to cleanup old backups", "error", err)
	}

	return nil
}

// Rollback restores the original state from backup
func (op *FileOperation) Rollback() error {
	if !op.completed {
		return fmt.Errorf("operation not completed, nothing to rollback")
	}

	// Restore from backup if it exists
	if _, err := os.Stat(op.backupPath); err == nil {
		if err := copyFile(op.backupPath, op.targetPath); err != nil {
			return fmt.Errorf("failed to restore from backup: %w", err)
		}
	}

	return nil
}

// Commit finalizes the operation and removes the backup
func (op *FileOperation) Commit() error {
	if !op.completed {
		return fmt.Errorf("operation not completed, cannot commit")
	}

	// Remove backup file
	if _, err := os.Stat(op.backupPath); err == nil {
		if err := os.Remove(op.backupPath); err != nil {
			return fmt.Errorf("failed to remove backup: %w", err)
		}
	}

	return nil
}

// cleanupOldBackups removes excess backup files
func (op *FileOperation) cleanupOldBackups() error {
	if op.config.MaxBackups <= 0 {
		return nil // No limit
	}

	backupDir := filepath.Dir(op.backupPath)
	baseName := filepath.Base(op.targetPath)

	// Find all backups for this file
	patternSP, err := safepath.Join(backupDir, fmt.Sprintf("%s.*.backup", baseName))
	if err != nil {
		return err
	}
	matches, err := filepath.Glob(patternSP.String())
	if err != nil {
		return err
	}

	// Sort by modification time (oldest first)
	if len(matches) <= op.config.MaxBackups {
		return nil // Within limit
	}

	// Remove oldest backups
	toRemove := len(matches) - op.config.MaxBackups
	for i := 0; i < toRemove; i++ {
		if err := os.Remove(matches[i]); err != nil {
			slog.Warn("failed to remove old backup", "path", matches[i], "error", err)
		}
	}

	return nil
}

// Helper functions

// copyFile copies a file from src to dst with verification
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0775); err != nil {
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	// Sync to ensure data is written to disk
	if err := destFile.Sync(); err != nil {
		return err
	}

	// Copy file permissions
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, sourceInfo.Mode())
}

// calculateChecksum computes SHA256 hash of a file
func calculateChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// SafeMove performs a safe file move operation with backup and verification
func SafeMove(src, dst string, config OperationConfig) error {
	op, err := NewFileOperation(src, dst, config)
	if err != nil {
		return err
	}

	if err := op.Execute(); err != nil {
		return err
	}

	// Auto-commit if successful
	return op.Commit()
}

// SafeCopy performs a safe file copy operation with verification
func SafeCopy(src, dst string, config OperationConfig) error {
	// For copy, always preserve original
	copyConfig := config
	copyConfig.PreserveOriginal = true

	op, err := NewFileOperation(src, dst, copyConfig)
	if err != nil {
		return err
	}

	if err := op.Execute(); err != nil {
		return err
	}

	// Auto-commit if successful
	return op.Commit()
}

// VerifyFileIntegrity checks if a file matches its expected checksum
func VerifyFileIntegrity(path, expectedHash string) (bool, error) {
	actualHash, err := calculateChecksum(path)
	if err != nil {
		return false, err
	}
	return actualHash == expectedHash, nil
}
