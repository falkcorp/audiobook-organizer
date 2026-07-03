// file: internal/remux/remux_test.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-03

package remux

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	remuxer.RemuxMalformedFiles(context.Background()) // Should not panic
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
	remuxer.RemuxMalformedFiles(context.Background()) // Should skip due to already completed
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
