// file: internal/config/state_dir.go
// version: 1.0.0
// guid: b5e0a4d7-1c63-4f28-9a71-3d06e8c5f294
// last-edited: 2026-09-09

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
// It returns the directory EnsureSecureStateDir actually resolved, if that has
// run. THIS MATTERS: EnsureSecureStateDir can fall back to a different
// directory than the configured one (see its comment), and the bootstrap token
// is written by one call site and consumed -- and deleted -- by another. If
// those two disagreed, the token would be written to one directory and looked
// for in another, which presents as `401 invalid bootstrap token` with a
// perfectly good token file on disk. Memoising the decision is what makes the
// two sides agree by construction rather than by both happening to recompute
// it the same way.
//
// Before EnsureSecureStateDir has run it is a pure lookup and creates nothing.
func SecureStateDir() string {
	resolvedMu.RLock()
	r := resolvedStateDir
	resolvedMu.RUnlock()
	if r != "" {
		return r
	}
	return configuredStateDir()
}

// configuredStateDir is the requested directory, before any check that it can
// actually be used.
func configuredStateDir() string {
	if v := strings.TrimSpace(os.Getenv(SecureStateDirEnv)); v != "" {
		return filepath.Clean(v)
	}
	return DefaultSecureStateDir
}

var (
	resolvedMu       sync.RWMutex
	resolvedStateDir string
)

func rememberStateDir(dir string) {
	resolvedMu.Lock()
	resolvedStateDir = dir
	resolvedMu.Unlock()
}

// ResetStateDirForTest clears the memoised directory so a test can change
// ABK_STATE_DIR and have it take effect. Tests that call EnsureSecureStateDir
// must call this, or they inherit whichever directory ran first.
func ResetStateDirForTest() {
	resolvedMu.Lock()
	resolvedStateDir = ""
	resolvedMu.Unlock()
}

// EnsureSecureStateDir creates the credential directory if it is absent and
// returns the directory that should actually be used.
//
// 0700, not 0755: every file in here is a credential. systemd's
// StateDirectory= would create it 0755 before ExecStart, so this also tightens
// that case rather than accepting whatever was there. MkdirAll is a no-op on an
// existing directory and does NOT change its mode, so an operator who has
// deliberately set a different mode keeps it.
//
// THE FALLBACK. DefaultSecureStateDir is an absolute Linux path that an
// unprivileged process cannot create: `mkdir /var/lib/audiobook-organizer`
// fails with EACCES on every developer machine and in any container that does
// not pre-create it. Refusing to start there would mean `serve` no longer runs
// locally at all, so when the DEFAULT is not creatable this falls back to
// filepath.Dir(database_path) -- which is exactly where these files lived
// before -- and says so loudly.
//
// That is not the wandering-credentials bug coming back. On the production host
// the directory exists and is owned by the service user, so MkdirAll is a no-op
// and the constant wins; the fallback is reached only where the old behaviour
// was already the behaviour, and it is announced rather than silent.
//
// An EXPLICIT ABK_STATE_DIR never falls back. Someone who named a directory
// gets an error if it cannot be used, not a quiet substitution somewhere else.
func EnsureSecureStateDir() (string, error) {
	dir := configuredStateDir()
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		rememberStateDir(dir)
		return dir, nil
	}
	if strings.TrimSpace(os.Getenv(SecureStateDirEnv)) != "" {
		return dir, err // explicitly requested: report the failure, do not guess
	}

	fallback := filepath.Dir(strings.TrimSpace(AppConfig.DatabasePath))
	if fallback == "" || fallback == "." || !filepath.IsAbs(fallback) {
		return dir, err
	}
	if ferr := os.MkdirAll(fallback, 0o700); ferr != nil {
		return dir, err // report the real target's error, not the fallback's
	}
	rememberStateDir(fallback)
	slog.Warn("credential directory is not creatable — falling back to the database directory",
		"wanted", dir, "wanted_err", err, "using", fallback,
		"note", "set "+SecureStateDirEnv+" to choose this deliberately")
	return fallback, nil
}
