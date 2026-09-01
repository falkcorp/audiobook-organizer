// file: internal/fileops/hash_test.go
// version: 1.1.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-09-01

package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeFileHashAndSize(t *testing.T) {
	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("Hello, World!")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Compute hash
	hash, _, err := ComputeFileHashAndSize(testFile)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize failed: %v", err)
	}

	// Verify hash is not empty
	if hash == "" {
		t.Error("Expected non-empty hash")
	}

	// Verify hash has correct length (SHA256 = 64 hex characters)
	if len(hash) != 64 {
		t.Errorf("Expected hash length 64, got %d", len(hash))
	}

	// Known hash for "Hello, World!"
	expectedHash := "dffd6021bb2bd5b0af676290809ec3a53191dd81c7f70a4b28688a362182986f"
	if hash != expectedHash {
		t.Errorf("Hash mismatch:\nExpected: %s\nGot:      %s", expectedHash, hash)
	}
}

func TestComputeFileHashAndSize_SameContentSameHash(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two files with identical content
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	content := []byte("Test content for hashing")

	if err := os.WriteFile(file1, content, 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	if err := os.WriteFile(file2, content, 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	hash1, _, err := ComputeFileHashAndSize(file1)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize(file1) failed: %v", err)
	}

	hash2, _, err := ComputeFileHashAndSize(file2)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize(file2) failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Expected identical hashes for identical content:\nHash1: %s\nHash2: %s", hash1, hash2)
	}
}

func TestComputeFileHashAndSize_DifferentContentDifferentHash(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two files with different content
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")

	if err := os.WriteFile(file1, []byte("Content A"), 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("Content B"), 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	hash1, _, err := ComputeFileHashAndSize(file1)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize(file1) failed: %v", err)
	}

	hash2, _, err := ComputeFileHashAndSize(file2)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize(file2) failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hashes for different content")
	}
}

func TestComputeFileHashAndSize_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "nonexistent.txt")

	_, _, err := ComputeFileHashAndSize(nonExistent)
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

func TestComputeFileHashAndSize_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.txt")

	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	hash, _, err := ComputeFileHashAndSize(emptyFile)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize failed: %v", err)
	}

	// SHA256 hash of empty file
	expectedHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hash != expectedHash {
		t.Errorf("Hash mismatch for empty file:\nExpected: %s\nGot:      %s", expectedHash, hash)
	}
}

func TestComputeFileHashAndSize_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.dat")

	// Create a 1MB file
	largeContent := make([]byte, 1024*1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	if err := os.WriteFile(largeFile, largeContent, 0644); err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}

	hash, _, err := ComputeFileHashAndSize(largeFile)
	if err != nil {
		t.Fatalf("ComputeFileHashAndSize failed: %v", err)
	}

	if hash == "" {
		t.Error("Expected non-empty hash for large file")
	}
	if len(hash) != 64 {
		t.Errorf("Expected hash length 64, got %d", len(hash))
	}
}

func TestGetFileSize(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("Test content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	size, err := GetFileSize(testFile)
	if err != nil {
		t.Fatalf("GetFileSize failed: %v", err)
	}

	expectedSize := int64(len(content))
	if size != expectedSize {
		t.Errorf("Expected size %d, got %d", expectedSize, size)
	}
}

func TestGetFileSize_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.txt")

	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	size, err := GetFileSize(emptyFile)
	if err != nil {
		t.Fatalf("GetFileSize failed: %v", err)
	}

	if size != 0 {
		t.Errorf("Expected size 0 for empty file, got %d", size)
	}
}

func TestGetFileSize_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "nonexistent.txt")

	_, err := GetFileSize(nonExistent)
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

func TestGetFileSize_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	size, err := GetFileSize(tmpDir)
	if err != nil {
		t.Fatalf("GetFileSize failed for directory: %v", err)
	}

	// Directories have a size (typically small, representing metadata)
	if size < 0 {
		t.Errorf("Expected non-negative size, got %d", size)
	}
}
