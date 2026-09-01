// file: internal/fileops/safe_operations_test.go
// version: 1.1.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-09-01

package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.BackupDir != ".audiobook-backups" {
		t.Errorf("Expected BackupDir '.audiobook-backups', got '%s'", config.BackupDir)
	}
	if !config.VerifyChecksums {
		t.Error("Expected VerifyChecksums to be true")
	}
	if config.PreserveOriginal {
		t.Error("Expected PreserveOriginal to be false")
	}
	if config.MaxBackups != 5 {
		t.Errorf("Expected MaxBackups 5, got %d", config.MaxBackups)
	}
}

func TestNewFileOperation(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	// Create source file
	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if op.originalPath != srcFile {
		t.Errorf("Expected originalPath %s, got %s", srcFile, op.originalPath)
	}
	if op.targetPath != dstFile {
		t.Errorf("Expected targetPath %s, got %s", dstFile, op.targetPath)
	}
	if op.completed {
		t.Error("Expected completed to be false initially")
	}
	if op.originalHash == "" {
		t.Error("Expected originalHash to be calculated")
	}
}

func TestFileOperation_Execute_NewFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content for copy operation")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true // Keep source file

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify destination file exists and has correct content
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}
	if string(dstContent) != string(content) {
		t.Error("Destination content doesn't match source")
	}

	// Verify source still exists (PreserveOriginal=true)
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		t.Error("Source file was deleted despite PreserveOriginal=true")
	}
}

func TestFileOperation_Execute_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	// Create source and destination files
	srcContent := []byte("New content")
	oldContent := []byte("Old content")

	if err := os.WriteFile(srcFile, srcContent, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}
	if err := os.WriteFile(dstFile, oldContent, 0644); err != nil {
		t.Fatalf("Failed to create destination file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify destination has new content
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}
	if string(dstContent) != string(srcContent) {
		t.Error("Destination wasn't updated with source content")
	}

	// Verify backup exists with old content
	if _, err := os.Stat(op.backupPath); os.IsNotExist(err) {
		t.Error("Backup file wasn't created")
	}
}

func TestFileOperation_Execute_WithoutChecksumVerification(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.VerifyChecksums = false
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if op.originalHash != "" {
		t.Error("Expected no checksum calculation when VerifyChecksums=false")
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !op.completed {
		t.Error("Expected operation to be completed")
	}
}

func TestFileOperation_Rollback(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	srcContent := []byte("New content")
	oldContent := []byte("Old content")

	if err := os.WriteFile(srcFile, srcContent, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}
	if err := os.WriteFile(dstFile, oldContent, 0644); err != nil {
		t.Fatalf("Failed to create destination file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Rollback the operation
	if err := op.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify destination has old content restored
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}
	if string(dstContent) != string(oldContent) {
		t.Error("Rollback didn't restore old content")
	}
}

func TestFileOperation_Commit(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Backup should exist before commit
	if _, err := os.Stat(op.backupPath); os.IsNotExist(err) {
		t.Error("Backup should exist before commit")
	}

	if err := op.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Backup should be removed after commit
	if _, err := os.Stat(op.backupPath); !os.IsNotExist(err) {
		t.Error("Backup should be removed after commit")
	}
}

func TestSafeMove(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content for move")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	if err := SafeMove(srcFile, dstFile, config); err != nil {
		t.Fatalf("SafeMove failed: %v", err)
	}

	// Verify destination exists
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}
	if string(dstContent) != string(content) {
		t.Error("Destination content doesn't match source")
	}

	// Verify source was removed (move operation)
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Error("Source file should be removed after move")
	}
}

func TestSafeCopy(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content for copy")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	if err := SafeCopy(srcFile, dstFile, config); err != nil {
		t.Fatalf("SafeCopy failed: %v", err)
	}

	// Verify both source and destination exist
	srcContent, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("Failed to read source: %v", err)
	}
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}

	if string(srcContent) != string(content) {
		t.Error("Source content was modified")
	}
	if string(dstContent) != string(content) {
		t.Error("Destination content doesn't match source")
	}
}

func TestVerifyFileIntegrity(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("Test content for integrity check")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Calculate expected hash
	expectedHash, err := calculateChecksum(testFile)
	if err != nil {
		t.Fatalf("Failed to calculate checksum: %v", err)
	}

	// Verify with correct hash
	valid, err := VerifyFileIntegrity(testFile, expectedHash)
	if err != nil {
		t.Fatalf("VerifyFileIntegrity failed: %v", err)
	}
	if !valid {
		t.Error("File should be valid with correct hash")
	}

	// Verify with incorrect hash
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	valid, err = VerifyFileIntegrity(testFile, wrongHash)
	if err != nil {
		t.Fatalf("VerifyFileIntegrity failed: %v", err)
	}
	if valid {
		t.Error("File should be invalid with incorrect hash")
	}
}

func TestVerifyFileIntegrity_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "nonexistent.txt")

	_, err := VerifyFileIntegrity(nonExistent, "somehash")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "subdir", "dest.txt")

	content := []byte("Test content for copy")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	if err := copyFileMkdirAll(srcFile, dstFile); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	// Verify destination exists and has correct content
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination: %v", err)
	}
	if string(dstContent) != string(content) {
		t.Error("Destination content doesn't match source")
	}

	// Verify permissions were copied
	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Error("File permissions weren't copied")
	}
}

func TestCalculateChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("Test content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	hash, err := calculateChecksum(testFile)
	if err != nil {
		t.Fatalf("calculateChecksum failed: %v", err)
	}

	if hash == "" {
		t.Error("Expected non-empty checksum")
	}
	if len(hash) != 64 {
		t.Errorf("Expected SHA256 hex string length 64, got %d", len(hash))
	}
}

// Test Rollback when operation not completed
func TestFileOperation_Rollback_NotCompleted(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	// Try to rollback without executing
	err = op.Rollback()
	if err == nil {
		t.Error("Expected error when rolling back non-completed operation")
	}
}

// Test Commit when operation not completed
func TestFileOperation_Commit_NotCompleted(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	// Try to commit without executing
	err = op.Commit()
	if err == nil {
		t.Error("Expected error when committing non-completed operation")
	}
}

// Test Execute with checksum verification and move (PreserveOriginal=false)
func TestFileOperation_Execute_MoveWithoutPreserve(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content for move")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = false
	config.VerifyChecksums = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Source should be removed (PreserveOriginal=false)
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Error("Source file should be removed when PreserveOriginal=false")
	}

	// Destination should exist
	if _, err := os.Stat(dstFile); os.IsNotExist(err) {
		t.Error("Destination file should exist after Execute")
	}
}

// Test cleanupOldBackups with MaxBackups=0 (no limit)
func TestCleanupOldBackups_NoLimit(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.MaxBackups = 0 // No limit
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Should succeed with no cleanup
	if !op.completed {
		t.Error("Expected operation to be completed")
	}
}

// Test cleanupOldBackups with multiple backups exceeding limit
func TestCleanupOldBackups_ExceedsLimit(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("Failed to create backup dir: %v", err)
	}

	baseName := "test.txt"

	// Create 10 backup files
	for i := range 10 {
		backupName := filepath.Join(backupDir, filepath.Base(baseName)+".20240101_15040"+string(rune('0'+i))+".backup")
		if err := os.WriteFile(backupName, []byte("backup"), 0644); err != nil {
			t.Fatalf("Failed to create backup file: %v", err)
		}
	}

	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, baseName)

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = backupDir
	config.MaxBackups = 3
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Count remaining backups
	pattern := filepath.Join(backupDir, baseName+".*.backup")
	matches, _ := filepath.Glob(pattern)

	// Should have MaxBackups (3) backups remaining (old ones removed)
	if len(matches) > config.MaxBackups+1 {
		t.Errorf("Expected at most %d backups, found %d", config.MaxBackups+1, len(matches))
	}
}

// Test SafeMove with error from NewFileOperation
func TestSafeMove_NewFileOperationError(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentSrc := filepath.Join(tmpDir, "nonexistent.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	// Should fail because source doesn't exist
	err := SafeMove(nonExistentSrc, dstFile, config)
	if err == nil {
		t.Error("Expected error when source file doesn't exist")
	}
}

// Test SafeCopy with error from NewFileOperation
func TestSafeCopy_NewFileOperationError(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentSrc := filepath.Join(tmpDir, "nonexistent.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	// Should fail because source doesn't exist
	err := SafeCopy(nonExistentSrc, dstFile, config)
	if err == nil {
		t.Error("Expected error when source file doesn't exist")
	}
}

// Test copyFile with non-existent source
func TestCopyFile_NonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentSrc := filepath.Join(tmpDir, "nonexistent.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	err := copyFileMkdirAll(nonExistentSrc, dstFile)
	if err == nil {
		t.Error("Expected error when copying non-existent file")
	}
}

// Test calculateChecksum with non-existent file
func TestCalculateChecksum_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "nonexistent.txt")

	_, err := calculateChecksum(nonExistent)
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

// Test NewFileOperation with non-existent source (should fail on checksum)
func TestNewFileOperation_NonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentSrc := filepath.Join(tmpDir, "nonexistent.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.VerifyChecksums = true

	_, err := NewFileOperation(nonExistentSrc, dstFile, config)
	if err == nil {
		t.Error("Expected error when source file doesn't exist")
	}
}

// Test Execute with same source and target paths (no checksum verification)
func TestFileOperation_Execute_SameSourceAndTarget(t *testing.T) {
	tmpDir := t.TempDir()
	sameFile := filepath.Join(tmpDir, "same.txt")

	content := []byte("Test content")
	if err := os.WriteFile(sameFile, content, 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = false
	config.VerifyChecksums = false // Disable checksums for same-file operations

	op, err := NewFileOperation(sameFile, sameFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// File should still exist (not removed because src == dst)
	if _, err := os.Stat(sameFile); os.IsNotExist(err) {
		t.Error("File should not be removed when source equals target")
	}
}

// Test Execute with target directory that can't be created
func TestFileOperation_Execute_CreateDestDirError(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Create a file where we want a directory
	invalidPath := filepath.Join(tmpDir, "file_not_dir")
	if err := os.WriteFile(invalidPath, []byte("block"), 0644); err != nil {
		t.Fatalf("Failed to create blocking file: %v", err)
	}

	// Try to create a file in a path that requires this file to be a directory
	dstFile := filepath.Join(invalidPath, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	// Execute should fail because destination directory can't be created
	err = op.Execute()
	if err == nil {
		t.Error("Expected error when destination directory can't be created")
	}
}

// Test Rollback with missing backup file
func TestFileOperation_Rollback_MissingBackup(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Remove the backup file
	if err := os.Remove(op.backupPath); err != nil {
		t.Fatalf("Failed to remove backup: %v", err)
	}

	// Rollback should succeed (it checks if backup exists before restoring)
	if err := op.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
}

// Test Commit with missing backup file
func TestFileOperation_Commit_MissingBackup(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")
	config.PreserveOriginal = true

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Remove the backup file
	if err := os.Remove(op.backupPath); err != nil {
		t.Fatalf("Failed to remove backup: %v", err)
	}

	// Commit should succeed (it checks if backup exists before removing)
	if err := op.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
}

// Test SafeMove with Execute error
func TestSafeMove_ExecuteError(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Create a file where we want a directory
	invalidPath := filepath.Join(tmpDir, "file_not_dir")
	if err := os.WriteFile(invalidPath, []byte("block"), 0644); err != nil {
		t.Fatalf("Failed to create blocking file: %v", err)
	}

	dstFile := filepath.Join(invalidPath, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	err := SafeMove(srcFile, dstFile, config)
	if err == nil {
		t.Error("Expected error from SafeMove when Execute fails")
	}
}

// Test SafeCopy with Execute error
func TestSafeCopy_ExecuteError(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Create a file where we want a directory
	invalidPath := filepath.Join(tmpDir, "file_not_dir")
	if err := os.WriteFile(invalidPath, []byte("block"), 0644); err != nil {
		t.Fatalf("Failed to create blocking file: %v", err)
	}

	dstFile := filepath.Join(invalidPath, "dest.txt")

	config := DefaultConfig()
	config.BackupDir = filepath.Join(tmpDir, "backups")

	err := SafeCopy(srcFile, dstFile, config)
	if err == nil {
		t.Error("Expected error from SafeCopy when Execute fails")
	}
}

// Test NewFileOperation with relative backup directory
func TestNewFileOperation_RelativeBackupDir(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "subdir", "dest.txt")

	content := []byte("Test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	config := DefaultConfig()
	config.BackupDir = "backups" // Relative path

	op, err := NewFileOperation(srcFile, dstFile, config)
	if err != nil {
		t.Fatalf("NewFileOperation failed: %v", err)
	}

	// Backup path should be made absolute relative to target directory
	expectedBackupDir := filepath.Join(filepath.Dir(dstFile), "backups")
	actualBackupDir := filepath.Dir(op.backupPath)
	if actualBackupDir != expectedBackupDir {
		t.Errorf("Expected backup dir %s, got %s", expectedBackupDir, actualBackupDir)
	}
}
