// file: internal/remux/transcode_test.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-07-03

package remux

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestTranscoderNew(t *testing.T) {
	store := &MockStore{}
	transcoder := NewTranscoder(store)
	if transcoder == nil {
		t.Errorf("NewTranscoder() returned nil")
		return
	}
	if transcoder.store != store {
		t.Errorf("NewTranscoder() store not set correctly")
	}
}

func TestTranscodeMalformedFilesWithoutStore(t *testing.T) {
	transcoder := &Transcoder{store: nil}
	transcoder.TranscodeMalformedFiles(context.Background()) // Should not panic
}

func TestTranscodeMalformedFilesAlreadyCompleted(t *testing.T) {
	store := &MockStore{
		settings: map[string]*database.Setting{
			TranscodeKey: {
				Key:   TranscodeKey,
				Value: "true",
				Type:  "bool",
			},
		},
	}
	transcoder := NewTranscoder(store)
	transcoder.TranscodeMalformedFiles(context.Background()) // Should skip due to already completed
}

func TestTranscodeSkipKey(t *testing.T) {
	path := "/test/audio.m4b"
	key := TranscodeSkipKey(path)

	// Verify the key format includes the hash prefix
	h := sha256.Sum256([]byte(path))
	expected := fmt.Sprintf("transcode_skip_%x", h[:8])
	if key != expected {
		t.Errorf("TranscodeSkipKey() = %s, want %s", key, expected)
	}

	// Verify consistent hashing
	key2 := TranscodeSkipKey(path)
	if key != key2 {
		t.Errorf("TranscodeSkipKey() not consistent: %s != %s", key, key2)
	}

	// Verify different paths produce different keys
	key3 := TranscodeSkipKey("/other/audio.m4b")
	if key == key3 {
		t.Errorf("TranscodeSkipKey() should differ for different paths")
	}
}

func TestTranscodeFileErrorsOnNonexistentFile(t *testing.T) {
	err := TranscodeFile("/nonexistent/file.m4b")
	if err == nil {
		t.Errorf("TranscodeFile() expected error for nonexistent file")
	}
}

func TestTranscodeFileTempFileCleanup(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.m4b")

	// Create a dummy file (will fail on ffmpeg, but we're testing cleanup)
	if err := os.WriteFile(testFile, []byte("dummy m4b content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// This will fail due to invalid m4b, but temp file should still be cleaned up
	_ = TranscodeFile(testFile)

	tmpFile := testFile + ".remux.tmp"
	if _, err := os.Stat(tmpFile); err == nil {
		t.Errorf("TranscodeFile() left temp file behind: %s", tmpFile)
	}
}
