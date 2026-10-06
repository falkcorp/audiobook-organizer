// file: internal/database/settings_extra_test.go
// version: 1.0.1
// guid: 60e4b0a3-7c08-4a29-9d0f-9a3c6c5a2c77
// last-edited: 2026-10-06

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

func TestGetDecryptedSettingNonSecret(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	plain := "plain-value"
	if err := store.SetSetting("plain", plain, "string", false); err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}
	got, err := GetDecryptedSetting(store, "plain")
	if err != nil {
		t.Fatalf("GetDecryptedSetting failed: %v", err)
	}
	if got != plain {
		t.Fatalf("expected %q, got %q", plain, got)
	}
}

func TestGetDecryptedSettingSecret(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	withCleanKey(t)

	if err := InitEncryption(t.TempDir()); err != nil {
		t.Fatalf("InitEncryption failed: %v", err)
	}

	secret := "secret-value"
	if err := store.SetSetting("secret", secret, "string", true); err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}
	got, err := GetDecryptedSetting(store, "secret")
	if err != nil {
		t.Fatalf("GetDecryptedSetting failed: %v", err)
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}
