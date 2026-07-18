// file: internal/remux/transcode_test.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-07-18

package remux

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	if err := transcoder.TranscodeMalformedFiles(context.Background(), nil); err != nil {
		t.Errorf("TranscodeMalformedFiles() with nil store should return nil, got %v", err)
	}
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
	if err := transcoder.TranscodeMalformedFiles(context.Background(), nil); err != nil {
		t.Errorf("TranscodeMalformedFiles() already-completed should return nil, got %v", err)
	}
}

// TestTranscodeMalformedFilesRootDirNotConfigured proves the C2 fix: a fatal
// setup failure (no RootDir configured) now returns an error instead of
// being swallowed as a Warn log, so the op can actually fail.
func TestTranscodeMalformedFilesRootDirNotConfigured(t *testing.T) {
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = ""
	defer func() { config.AppConfig.RootDir = origRoot }()

	store := &MockStore{}
	transcoder := NewTranscoder(store)
	err := transcoder.TranscodeMalformedFiles(context.Background(), nil)
	if err == nil {
		t.Fatal("TranscodeMalformedFiles() expected error when RootDir is not configured, got nil")
	}
	if !strings.Contains(err.Error(), "RootDir") {
		t.Errorf("TranscodeMalformedFiles() error = %q, want it to mention RootDir", err.Error())
	}
}

// TestTranscodeMalformedFilesProgressCallback proves the C2 fix: progress is
// threaded through with an accurate total from the pre-count pass.
func TestTranscodeMalformedFilesProgressCallback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	origRoot := config.AppConfig.RootDir
	tmpDir := t.TempDir()
	config.AppConfig.RootDir = tmpDir
	defer func() { config.AppConfig.RootDir = origRoot }()

	for i := 0; i < 3; i++ {
		p := filepath.Join(tmpDir, fmt.Sprintf("test%d.m4b", i))
		if err := os.WriteFile(p, []byte("not a real m4b"), 0o644); err != nil {
			t.Fatalf("write test file: %v", err)
		}
	}

	store := &MockStore{}
	transcoder := NewTranscoder(store)

	var calls int
	var lastProcessed, lastTotal int
	err := transcoder.TranscodeMalformedFiles(context.Background(), func(processed, total int, _ string) {
		calls++
		lastProcessed = processed
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("TranscodeMalformedFiles() unexpected error: %v", err)
	}
	if calls == 0 {
		t.Fatal("TranscodeMalformedFiles() progress callback was never called")
	}
	if lastTotal != 3 {
		t.Errorf("TranscodeMalformedFiles() final total = %d, want 3", lastTotal)
	}
	if lastProcessed != 3 {
		t.Errorf("TranscodeMalformedFiles() final processed = %d, want 3", lastProcessed)
	}
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
