// file: internal/remux/remux_test.go
// version: 1.2.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-18

package remux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MockStore is a test double for Store interface.
type MockStore struct {
	settings map[string]*database.Setting
}

// GetSetting retrieves a setting.
func (m *MockStore) GetSetting(key string) (*database.Setting, error) {
	if v, ok := m.settings[key]; ok {
		return v, nil
	}
	return nil, nil
}

// SetSetting stores a setting.
func (m *MockStore) SetSetting(key, value, typ string, isSecret bool) error {
	if m.settings == nil {
		m.settings = make(map[string]*database.Setting)
	}
	m.settings[key] = &database.Setting{
		Key:      key,
		Value:    value,
		Type:     typ,
		IsSecret: isSecret,
	}
	return nil
}

func TestRemuxerNew(t *testing.T) {
	store := &MockStore{}
	remuxer := New(store)
	if remuxer == nil {
		t.Errorf("New() returned nil")
		return
	}
	if remuxer.store != store {
		t.Errorf("New() store not set correctly")
	}
}

func TestRemuxMalformedFilesWithoutStore(t *testing.T) {
	remuxer := &Remuxer{store: nil}
	if err := remuxer.RemuxMalformedFiles(context.Background(), nil); err != nil {
		t.Errorf("RemuxMalformedFiles() with nil store should return nil, got %v", err)
	}
}

func TestRemuxMalformedFilesAlreadyCompleted(t *testing.T) {
	store := &MockStore{
		settings: map[string]*database.Setting{
			RemuxKey: {
				Key:   RemuxKey,
				Value: "true",
				Type:  "bool",
			},
		},
	}
	remuxer := New(store)
	if err := remuxer.RemuxMalformedFiles(context.Background(), nil); err != nil {
		t.Errorf("RemuxMalformedFiles() already-completed should return nil, got %v", err)
	}
}

// TestRemuxMalformedFilesRootDirNotConfigured proves the C2 fix: a fatal
// setup failure (no RootDir configured) now returns an error instead of
// being swallowed as a Warn log, so the op can actually fail.
func TestRemuxMalformedFilesRootDirNotConfigured(t *testing.T) {
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = ""
	defer func() { config.AppConfig.RootDir = origRoot }()

	store := &MockStore{}
	remuxer := New(store)
	err := remuxer.RemuxMalformedFiles(context.Background(), nil)
	if err == nil {
		t.Fatal("RemuxMalformedFiles() expected error when RootDir is not configured, got nil")
	}
	if !strings.Contains(err.Error(), "RootDir") {
		t.Errorf("RemuxMalformedFiles() error = %q, want it to mention RootDir", err.Error())
	}
}

// TestRemuxMalformedFilesProgressCallback proves the C2 fix: progress is
// threaded through and called every 25 files (and once more at the end),
// with an accurate total from the pre-count pass.
func TestRemuxMalformedFilesProgressCallback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	origRoot := config.AppConfig.RootDir
	tmpDir := t.TempDir()
	config.AppConfig.RootDir = tmpDir
	defer func() { config.AppConfig.RootDir = origRoot }()

	// Files that taglib can't parse are "malformed" candidates; plain text
	// content is enough to exercise the walk + progress path (ffmpeg will
	// fail to remux them, which is fine — we're only proving progress fires
	// with an accurate total, not that remux succeeds).
	for i := 0; i < 3; i++ {
		p := filepath.Join(tmpDir, fmt.Sprintf("test%d.m4b", i))
		if err := os.WriteFile(p, []byte("not a real m4b"), 0o644); err != nil {
			t.Fatalf("write test file: %v", err)
		}
	}

	store := &MockStore{}
	remuxer := New(store)

	var calls int
	var lastProcessed, lastTotal int
	err := remuxer.RemuxMalformedFiles(context.Background(), func(processed, total int, _ string) {
		calls++
		lastProcessed = processed
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("RemuxMalformedFiles() unexpected error: %v", err)
	}
	if calls == 0 {
		t.Fatal("RemuxMalformedFiles() progress callback was never called")
	}
	if lastTotal != 3 {
		t.Errorf("RemuxMalformedFiles() final total = %d, want 3", lastTotal)
	}
	if lastProcessed != 3 {
		t.Errorf("RemuxMalformedFiles() final processed = %d, want 3", lastProcessed)
	}
}

func TestRemuxFileErrorsOnNonexistentFile(t *testing.T) {
	err := RemuxFile("/nonexistent/file.m4b")
	if err == nil {
		t.Errorf("RemuxFile() expected error for nonexistent file")
	}
}

func TestRemuxFileTempFileCleanup(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.m4b")

	// Create a dummy file (will fail on ffmpeg, but we're testing cleanup)
	if err := os.WriteFile(testFile, []byte("dummy m4b content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// This will fail due to invalid m4b, but temp file should still be cleaned up
	_ = RemuxFile(testFile)

	tmpFile := testFile + ".remux.tmp"
	if _, err := os.Stat(tmpFile); err == nil {
		t.Errorf("RemuxFile() left temp file behind: %s", tmpFile)
	}
}
