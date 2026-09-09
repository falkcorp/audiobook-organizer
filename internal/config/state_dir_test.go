// file: internal/config/state_dir_test.go
// version: 1.0.0
// guid: 9e27b1f4-5a83-4d60-b2c7-18f4a0e96d35
// last-edited: 2026-09-09

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecureStateDir_DefaultIsTheConstant pins the whole point of this change:
// the credential directory does NOT move when the database moves.
//
// It used to be filepath.Dir(database_path) at three separate call sites, which
// is how the bootstrap token ended up inside a 0700 directory on another pool
// on 2026-09-09 and broke the runbook's fixed `sudo cat` path.
func TestSecureStateDir_DefaultIsTheConstant(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	t.Setenv(SecureStateDirEnv, "")

	if got := SecureStateDir(); got != DefaultSecureStateDir {
		t.Errorf("SecureStateDir() = %q, want the constant %q", got, DefaultSecureStateDir)
	}

	// And it must be independent of database_path. This is the regression:
	// setting a database path anywhere must not move the credentials.
	prev := AppConfig.DatabasePath
	t.Cleanup(func() { AppConfig.DatabasePath = prev })
	for _, dbPath := range []string{
		"/mnt/bigdata/books/audiobook-organizer/.appdata/audiobooks.pebble",
		"/var/lib/audiobook-organizer/audiobooks.pebble",
		"audiobooks.pebble",
		"",
	} {
		AppConfig.DatabasePath = dbPath
		if got := SecureStateDir(); got != DefaultSecureStateDir {
			t.Errorf("database_path=%q moved the state dir to %q", dbPath, got)
		}
	}
}

func TestSecureStateDir_EnvOverrideWinsAndIsCleaned(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	t.Setenv(SecureStateDirEnv, "/tmp/abk-state/../abk-state/")
	if got, want := SecureStateDir(), filepath.Clean("/tmp/abk-state"); got != want {
		t.Errorf("SecureStateDir() = %q, want %q", got, want)
	}
}

// TestSecureStateDir_BlankEnvFallsBackToTheConstant — an override set to empty
// or whitespace (a drop-in with `Environment=ABK_STATE_DIR=`) must not resolve
// to "", which would put credentials in the process working directory.
func TestSecureStateDir_BlankEnvFallsBackToTheConstant(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	for _, v := range []string{"", "   ", "\t"} {
		t.Setenv(SecureStateDirEnv, v)
		if got := SecureStateDir(); got != DefaultSecureStateDir {
			t.Errorf("ABK_STATE_DIR=%q gave %q, want the constant", v, got)
		}
	}
}

// TestEnsureSecureStateDir_Creates0700 — every file in here is a credential,
// and systemd's StateDirectory= would have created it 0755.
func TestEnsureSecureStateDir_Creates0700(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	base := t.TempDir()
	target := filepath.Join(base, "nested", "state")
	t.Setenv(SecureStateDirEnv, target)

	got, err := EnsureSecureStateDir()
	if err != nil {
		t.Fatalf("EnsureSecureStateDir: %v", err)
	}
	if got != target {
		t.Errorf("returned %q, want %q", got, target)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 0700", perm)
	}
}

// TestEnsureSecureStateDir_IsIdempotentAndKeepsAnExistingMode — MkdirAll does
// not chmod an existing directory, and it must not: an operator who has
// deliberately widened or tightened it keeps their choice, and a restart must
// not fail just because the directory is already there.
func TestEnsureSecureStateDir_IsIdempotentAndKeepsAnExistingMode(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SecureStateDirEnv, target)

	for i := range 2 {
		if _, err := EnsureSecureStateDir(); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o750 {
		t.Errorf("mode = %o, want the pre-existing 0750 to be preserved", perm)
	}
}

// TestEnsureSecureStateDir_ReportsThePathItFailedOn — the error is the only
// thing the operator sees when startup aborts, so it has to name the directory.
func TestEnsureSecureStateDir_ReportsThePathItFailedOn(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "state")
	t.Setenv(SecureStateDirEnv, target)

	dir, err := EnsureSecureStateDir()
	if err == nil {
		t.Fatal("expected an error when the parent is a regular file")
	}
	if dir != target {
		t.Errorf("returned dir = %q, want %q even on failure, so the caller can name it", dir, target)
	}
}

// TestEnsureSecureStateDir_FallsBackWhenTheDefaultIsNotCreatable covers the
// developer-machine and bare-container case. `mkdir /var/lib/audiobook-organizer`
// fails with EACCES for an unprivileged process, and `serve` has to still run.
//
// This test only means anything where the default is genuinely not creatable, so
// it skips when it is (a root CI container, or a machine where the directory
// already exists) rather than pretending to have tested the branch.
func TestEnsureSecureStateDir_FallsBackWhenTheDefaultIsNotCreatable(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)
	t.Setenv(SecureStateDirEnv, "")
	if err := os.MkdirAll(DefaultSecureStateDir, 0o700); err == nil {
		t.Skipf("%s is creatable here, so the fallback branch is unreachable", DefaultSecureStateDir)
	}

	prev := AppConfig.DatabasePath
	t.Cleanup(func() { AppConfig.DatabasePath = prev })
	dbDir := t.TempDir()
	AppConfig.DatabasePath = filepath.Join(dbDir, "audiobooks.pebble")

	got, err := EnsureSecureStateDir()
	if err != nil {
		t.Fatalf("EnsureSecureStateDir returned an error instead of falling back: %v", err)
	}
	if got != dbDir {
		t.Errorf("fell back to %q, want the database directory %q", got, dbDir)
	}

	// THE POINT: the memoised value must now be what every other caller sees,
	// including the bootstrap consume side. If SecureStateDir() still reported
	// the constant, the token would be written to one directory and deleted from
	// another.
	if got := SecureStateDir(); got != dbDir {
		t.Errorf("SecureStateDir() = %q after a fallback, want %q — the write and "+
			"consume sides of the bootstrap token would disagree", got, dbDir)
	}
}

// TestEnsureSecureStateDir_ExplicitOverrideNeverFallsBack — a named directory
// that cannot be used is an error, not an invitation to write credentials
// somewhere else.
func TestEnsureSecureStateDir_ExplicitOverrideNeverFallsBack(t *testing.T) {
	ResetStateDirForTest()
	t.Cleanup(ResetStateDirForTest)

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SecureStateDirEnv, filepath.Join(blocker, "state"))

	prev := AppConfig.DatabasePath
	t.Cleanup(func() { AppConfig.DatabasePath = prev })
	// A perfectly usable fallback is available; it must NOT be taken.
	AppConfig.DatabasePath = filepath.Join(t.TempDir(), "audiobooks.pebble")

	if _, err := EnsureSecureStateDir(); err == nil {
		t.Fatal("an explicitly requested but unusable ABK_STATE_DIR silently fell back")
	}
}
