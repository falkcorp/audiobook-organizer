// file: internal/database/settings_extra_test.go
// version: 1.0.2
// guid: 60e4b0a3-7c08-4a29-9d0f-9a3c6c5a2c77
// last-edited: 2026-10-19

package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitEncryptionWithExistingKey(t *testing.T) {
	withCleanKey(t)
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, ".encryption_key")
	if err := os.WriteFile(keyPath, make([]byte, 32), 0o600); err != nil {
		t.Fatalf("failed to write key: %v", err)
	}
	if err := InitEncryption(tempDir); err != nil {
		t.Fatalf("InitEncryption failed with existing key: %v", err)
	}

	badDir := t.TempDir()
	badKey := filepath.Join(badDir, ".encryption_key")
	if err := os.WriteFile(badKey, []byte("short"), 0o600); err != nil {
		t.Fatalf("failed to write bad key: %v", err)
	}
	if err := InitEncryption(badDir); err == nil {
		t.Fatal("expected error for invalid key length")
	}
}
