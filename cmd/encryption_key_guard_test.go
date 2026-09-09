// file: cmd/encryption_key_guard_test.go
// version: 1.0.0
// guid: 2f58c3a9-6e41-4b07-90d8-7a13e5b2f846
// last-edited: 2026-09-09

package cmd

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// guardStore is the narrowest thing guardAgainstKeyRegeneration needs. It is a
// hand-rolled stub rather than MockStore because the only interesting axis is
// what GetAllSettings returns, including its error.
type guardStore struct {
	database.Store
	settings []database.Setting
	err      error
}

func (g *guardStore) GetAllSettings() ([]database.Setting, error) { return g.settings, g.err }

func writeKeyFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(database.EncryptionKeyPath(dir), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGuard_RefusesWhenSecretsExistAndNoKeyAnywhere is the case that makes this
// guard worth having.
//
// Without it, startup generates a fresh key and LoadConfigFromDatabase then
// re-encrypts the four secrets recoverable from the config file and
// DeleteSettings every other one. The process exits 0 and looks healthy; the
// secrets are simply gone. Refusing to boot is the last moment at which the
// operator can still put the key file back.
func TestGuard_RefusesWhenSecretsExistAndNoKeyAnywhere(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "")
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	store := &guardStore{settings: []database.Setting{
		{Key: "abs_jwt_secret", Value: "ciphertext", IsSecret: true},
	}}

	err := guardAgainstKeyRegeneration(store, stateDir, legacyDir)
	if err == nil {
		t.Fatal("guard allowed startup with encrypted secrets and no key; the secrets " +
			"would be silently deleted")
	}
	// The message is the entire remedy, so assert it carries both the path to
	// restore and the override. A guard that refuses without saying how to
	// proceed just moves the outage.
	for _, want := range []string{
		database.EncryptionKeyPath(stateDir),
		allowKeyRegenEnv,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestGuard_AllowsAFirstRun — nothing encrypted yet means generating a key is
// exactly right, and must not be blocked.
func TestGuard_AllowsAFirstRun(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "")
	store := &guardStore{settings: []database.Setting{
		{Key: "root_dir", Value: "/srv/books"}, // plaintext only
	}}

	if err := guardAgainstKeyRegeneration(store, t.TempDir(), t.TempDir()); err != nil {
		t.Errorf("guard blocked a first run: %v", err)
	}
}

// TestGuard_SkipsTheCensusWhenAKeyExists — the guard must not consult the store
// at all when a key is present, both because it is pointless and because
// GetAllSettings on a large store is not free on every boot.
func TestGuard_SkipsTheCensusWhenAKeyExists(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "")
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	writeKeyFile(t, stateDir)

	// An error-returning store proves the census was never called: if it had
	// been, the guard would fail closed on this error.
	store := &guardStore{err: errors.New("GetAllSettings must not be called here")}
	if err := guardAgainstKeyRegeneration(store, stateDir, legacyDir); err != nil {
		t.Errorf("guard consulted the store even though a key exists: %v", err)
	}
}

// TestGuard_AcceptsAKeyInTheLegacyDir — production's key is still beside the
// database. That must not trip the guard, or the upgrade cannot boot.
func TestGuard_AcceptsAKeyInTheLegacyDir(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "")
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	writeKeyFile(t, legacyDir)

	store := &guardStore{err: errors.New("census should not run")}
	if err := guardAgainstKeyRegeneration(store, stateDir, legacyDir); err != nil {
		t.Errorf("guard rejected an install whose key is still at the legacy path: %v", err)
	}
}

// TestGuard_FailsClosedOnACensusError — a store we cannot read is precisely the
// case where we must not gamble that there was nothing to lose. It still has to
// name the override, or a broken census becomes an unbootable server.
func TestGuard_FailsClosedOnACensusError(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "")
	store := &guardStore{err: errors.New("pebble: iterator closed")}

	err := guardAgainstKeyRegeneration(store, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("guard proceeded despite being unable to check for secrets")
	}
	if !strings.Contains(err.Error(), allowKeyRegenEnv) {
		t.Errorf("error does not name the override, so there is no way forward: %v", err)
	}
	if !strings.Contains(err.Error(), "pebble: iterator closed") {
		t.Errorf("error loses the underlying cause: %v", err)
	}
}

// TestGuard_OverrideLetsTheOperatorThrough — and it is checked BEFORE the
// census, so it also rescues the broken-store case.
func TestGuard_OverrideLetsTheOperatorThrough(t *testing.T) {
	t.Setenv(allowKeyRegenEnv, "1")
	store := &guardStore{
		settings: []database.Setting{{Key: "openai_api_key", Value: "ct", IsSecret: true}},
		err:      errors.New("would fail closed without the override"),
	}

	if err := guardAgainstKeyRegeneration(store, t.TempDir(), t.TempDir()); err != nil {
		t.Errorf("%s=1 did not allow startup: %v", allowKeyRegenEnv, err)
	}
}
