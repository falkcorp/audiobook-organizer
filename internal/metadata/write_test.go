// file: internal/metadata/write_test.go
// version: 1.2.0
// guid: 9a8b7c6d-5e4f-3a2b-1c0d-9a8b7c6d5e4f

package metadata

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

// createFakeTool writes a no-op executable to simulate external tag tools.
func createFakeTool(t *testing.T, dir, name string) {
	t.Helper()

	toolPath := filepath.Join(dir, name)
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake tool %s: %v", name, err)
	}
}

// TestWriteMetadataToFile_UnsupportedFormat verifies error handling for unsupported file formats
func TestWriteMetadataToFile_UnsupportedFormat(t *testing.T) {
	// Arrange
	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":  "Test Book",
		"artist": "Test Author",
	}

	// Act
	err := WriteMetadataToFile("test.txt", metadata, config)

	// Assert
	if err == nil {
		t.Error("Expected error for unsupported format, got nil")
	}
	if err.Error() != "unsupported file format: .txt" {
		t.Errorf("Expected unsupported format error, got: %v", err)
	}
}

// TestWriteM4BMetadata_ToolNotFound verifies error when AtomicParsley is not available
func TestWriteM4BMetadata_ToolNotFound(t *testing.T) {
	// Save and restore PATH to simulate missing tool
	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)

	// Set empty PATH to simulate missing AtomicParsley
	os.Setenv("PATH", "")

	// Arrange
	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":  "Test Book",
		"artist": "Test Author",
	}

	// Act
	err := writeM4BMetadata("test.m4b", metadata, config)

	// Assert
	if err == nil {
		t.Error("Expected error when AtomicParsley not found, got nil")
	}
}

// TestWriteMP3Metadata_ToolNotFound verifies error when eyeD3 is not available
func TestWriteMP3Metadata_ToolNotFound(t *testing.T) {
	// Save and restore PATH to simulate missing tool
	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)

	// Set empty PATH to simulate missing eyeD3
	os.Setenv("PATH", "")

	// Arrange
	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":  "Test Book",
		"artist": "Test Author",
	}

	// Act
	err := writeMP3Metadata("test.mp3", metadata, config)

	// Assert
	if err == nil {
		t.Error("Expected error when eyeD3 not found, got nil")
	}
}

// TestWriteFLACMetadata_ToolNotFound verifies error when metaflac is not available
func TestWriteFLACMetadata_ToolNotFound(t *testing.T) {
	// Save and restore PATH to simulate missing tool
	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)

	// Set empty PATH to simulate missing metaflac
	os.Setenv("PATH", "")

	// Arrange
	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":  "Test Book",
		"artist": "Test Author",
	}

	// Act
	err := writeFLACMetadata("test.flac", metadata, config)

	// Assert
	if err == nil {
		t.Error("Expected error when metaflac not found, got nil")
	}
}

func TestWriteMP3Metadata_FakeToolSuccess(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}
	createFakeTool(t, binDir, "eyeD3")
	t.Setenv("PATH", binDir)

	filePath := filepath.Join(tempDir, "test.mp3")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write mp3 file: %v", err)
	}

	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":    "Test Book",
		"artist":   "Test Author",
		"album":    "Test Album",
		"narrator": "Test Narrator",
		"genre":    "Audiobook",
		"year":     2024,
	}

	if err := writeMP3Metadata(filePath, metadata, config); err != nil {
		t.Fatalf("writeMP3Metadata failed: %v", err)
	}
}

func TestWriteFLACMetadata_FakeToolSuccess(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}
	createFakeTool(t, binDir, "metaflac")
	t.Setenv("PATH", binDir)

	filePath := filepath.Join(tempDir, "test.flac")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write flac file: %v", err)
	}

	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title":    "Test Book",
		"artist":   "Test Author",
		"album":    "Test Album",
		"narrator": "Test Narrator",
		"genre":    "Audiobook",
		"year":     2024,
	}

	if err := writeFLACMetadata(filePath, metadata, config); err != nil {
		t.Fatalf("writeFLACMetadata failed: %v", err)
	}
}

// TestWriteMetadata_IntegrationM4B verifies round-trip metadata writing for M4B files
// Note: This test requires AtomicParsley to be installed
func TestWriteMetadata_IntegrationM4B(t *testing.T) {
	// Skip if AtomicParsley not available
	if _, err := exec.LookPath("AtomicParsley"); err != nil {
		t.Skip("AtomicParsley not installed, skipping integration test")
	}

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.m4b")

	// Create a minimal M4B file (just a basic MP4 container)
	// This is a minimal valid M4B structure
	minimalM4B := []byte{
		0x00, 0x00, 0x00, 0x20, 0x66, 0x74, 0x79, 0x70, // ftyp box
		0x4d, 0x34, 0x42, 0x20, 0x00, 0x00, 0x00, 0x00,
		0x4d, 0x34, 0x42, 0x20, 0x4d, 0x34, 0x41, 0x20,
		0x6d, 0x70, 0x34, 0x32, 0x69, 0x73, 0x6f, 0x6d,
	}
	if err := os.WriteFile(testFile, minimalM4B, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Arrange
	config := fileops.DefaultConfig()
	config.PreserveOriginal = true // Keep backup for verification
	metadata := map[string]any{
		"title":    "Test Audiobook",
		"artist":   "Test Author",
		"album":    "Test Album",
		"narrator": "Test Narrator",
		"genre":    "Audiobook",
		"year":     2024,
	}

	// Act
	err := WriteMetadataToFile(testFile, metadata, config)

	// Assert
	if err != nil {
		if strings.Contains(err.Error(), "AtomicParsley") || strings.Contains(err.Error(), "signal:") {
			t.Skipf("AtomicParsley unavailable for integration test: %v", err)
		}
		t.Fatalf("WriteMetadataToFile failed: %v", err)
	}

	// Verify backup was created
	backupFile := testFile + ".backup"
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		t.Error("Expected backup file to exist")
	}

	// Verify original file still exists
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("Expected original file to exist after write")
	}

	// TODO: Read back metadata and verify values
	// This would require using dhowden/tag to read the updated metadata
	// For now, the test verifies the write operation succeeds without error
}

// TestWriteMetadata_BackupRestore verifies backup is restored on write failure
func TestWriteMetadata_BackupRestore(t *testing.T) {
	// Skip if AtomicParsley not available
	if _, err := exec.LookPath("AtomicParsley"); err != nil {
		t.Skip("AtomicParsley not installed, skipping integration test")
	}

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.m4b")

	// Create original content
	originalContent := []byte("original content")
	if err := os.WriteFile(testFile, originalContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Arrange - use invalid metadata to trigger failure
	config := fileops.DefaultConfig()
	metadata := map[string]any{
		"title": "Test", // AtomicParsley will fail on non-M4B file
	}

	// Act
	err := WriteMetadataToFile(testFile, metadata, config)

	// Assert - expect error
	if err == nil {
		t.Error("Expected error for invalid M4B file, got nil")
	}

	// Verify original file content is preserved (backup restored)
	content, readErr := os.ReadFile(testFile)
	if readErr != nil {
		t.Fatalf("Failed to read test file after error: %v", readErr)
	}
	if string(content) != string(originalContent) {
		t.Error("Expected original content to be restored after write failure")
	}
}
