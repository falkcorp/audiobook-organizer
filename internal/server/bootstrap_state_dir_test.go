// file: internal/server/bootstrap_state_dir_test.go
// version: 1.0.0
// guid: 6b03d92e-8f47-4a15-bc60-2e98a5317d4f
// last-edited: 2026-09-09

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestBootstrapToken_WriteAndConsumeResolveTheSameDirectory pins the property
// that used to be maintained by coincidence.
//
// The write side (server_lifecycle.go) and the consume side (handleBootstrap)
// each derived their directory independently with
// filepath.Dir(config.AppConfig.DatabasePath). Two copies of one rule: update
// either alone and the token is written to one directory and looked for -- and
// deleted from -- another. That failure presents as `401 invalid bootstrap
// token` with a perfectly good token file sitting on disk, which is close to
// undiagnosable from the outside.
//
// Both now call config.SecureStateDir(). This test fails if either ever goes
// back to deriving its own.
func TestBootstrapToken_WriteAndConsumeResolveTheSameDirectory(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.SecureStateDirEnv, stateDir)

	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	// Deliberately point the database somewhere COMPLETELY different. If either
	// side still derives from database_path, the two will disagree and the
	// assertions below will catch it.
	config.AppConfig.DatabasePath = filepath.Join(t.TempDir(), "elsewhere", "audiobooks.pebble")

	settings := newFakeSettingsStore()
	if err := InitBootstrapToken(settings, config.SecureStateDir()); err != nil {
		t.Fatalf("InitBootstrapToken: %v", err)
	}

	tokenPath := BootstrapTokenPath(stateDir)
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token was not written to the state dir (%s): %v", tokenPath, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		t.Fatal("token file is empty")
	}

	// It must NOT have been written beside the database.
	dbSide := BootstrapTokenPath(filepath.Dir(config.AppConfig.DatabasePath))
	if _, err := os.Stat(dbSide); err == nil {
		t.Errorf("a token was also written beside the database at %s", dbSide)
	}

	// The consume side must find that exact token and remove that exact file.
	valid, err := ConsumeBootstrapToken(settings, config.SecureStateDir(), token)
	if err != nil {
		t.Fatalf("ConsumeBootstrapToken: %v", err)
	}
	if !valid {
		t.Fatal("the token written by InitBootstrapToken was rejected by ConsumeBootstrapToken — " +
			"the two sides are resolving different directories")
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("consuming the token left the file in place at %s (stat err = %v)", tokenPath, err)
	}
}

// TestBootstrapToken_FileIsNotWorldReadable — it is an emergency admin
// credential; the runbook reads it with sudo precisely because it should not be
// readable by anyone else.
func TestBootstrapToken_FileIsNotWorldReadable(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.SecureStateDirEnv, stateDir)

	settings := newFakeSettingsStore()
	if err := InitBootstrapToken(settings, config.SecureStateDir()); err != nil {
		t.Fatalf("InitBootstrapToken: %v", err)
	}

	info, err := os.Stat(BootstrapTokenPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("token file mode = %o; group/other must have no access", perm)
	}
}
