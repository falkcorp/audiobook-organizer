// file: internal/config/state_dir.go
// version: 1.0.0
// guid: b5e0a4d7-1c63-4f28-9a71-3d06e8c5f294
// last-edited: 2026-09-09

package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SecureStateDirEnv overrides SecureStateDir. It exists so tests, dev boxes and
// containers can put these files somewhere writable; it is NOT a user-facing
// setting and is deliberately absent from Settings > Paths.
//
// Why not a config setting: this directory holds .encryption_key, and changing
// where that file is looked for WITHOUT moving the file destroys secrets (see
// DefaultSecureStateDir). A value stored in the database would be read from the
// very database whose secrets it governs, and a text box in the UI would let
// someone trigger that with one keystroke and no way to move the file.
const SecureStateDirEnv = "ABK_STATE_DIR"

// DefaultSecureStateDir is where the credentials live, on purpose, as a
// constant rather than as anything derived from database_path.
//
// It used to be derived: three separate call sites each computed
// filepath.Dir(database_path). That made the credential location a function of
// where the database happened to be, so moving the database moved the secrets
// with it -- and on 2026-09-09 it did exactly that. The database moved to
// <root_dir>/.appdata (mode 0700, on a different pool) and took the bootstrap
// token with it, which broke the `sudo cat` line in the server-bootstrap
// runbook and the NOPASSWD sudoers rule underneath it, because both name a
// fixed path. A stale token from before the move stayed readable at the old
// path and kept answering, so following the runbook returned a well-formed,
// long-expired token and a 401 with nothing pointing at why.
//
// A constant fixes that permanently: the databases can move between pools as
// often as they like and the operator-facing paths never change.
//
// THE HAZARD, if this value is ever changed: database.InitEncryption generates
// a NEW key when it cannot read one, and config.LoadConfigFromDatabase then
// re-encrypts the four secrets it can recover from the config file and calls
// DeleteSetting on every other secret it cannot decrypt. So repointing this
// directory without physically moving .encryption_key silently destroys
// secrets. InitEncryption takes fallback directories for exactly this reason;
// see its doc comment before touching this.
//
// Linux-only by construction. Making it platform-aware (~/Library/Application
// Support on macOS, %LOCALAPPDATA% on Windows) is filed in todo.d.
const DefaultSecureStateDir = "/var/lib/audiobook-organizer"

// SecureStateDir returns the directory holding the encryption key, the
// bootstrap token and the startup read-only key.
//
// This is a pure lookup: it creates nothing. Call EnsureSecureStateDir once at
// startup if you are going to write into it.
func SecureStateDir() string {
	if v := strings.TrimSpace(os.Getenv(SecureStateDirEnv)); v != "" {
		return filepath.Clean(v)
	}
	return DefaultSecureStateDir
}

// EnsureSecureStateDir creates the directory if it is absent and returns it.
//
// 0700, not 0755: every file in here is a credential. systemd's
// StateDirectory= would create it 0755 before ExecStart, so this also tightens
// that case rather than accepting whatever was there.
//
// MkdirAll is a no-op on an existing directory and does NOT change its mode, so
// an operator who has deliberately set a different mode keeps it.
func EnsureSecureStateDir() (string, error) {
	dir := SecureStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dir, err
	}
	return dir, nil
}
