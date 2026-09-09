// file: internal/database/encryption_key_location_test.go
// version: 1.0.0
// guid: 4d91f6a2-7b35-4c08-8e1d-0a62b9f37c54
// last-edited: 2026-09-09

package database

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withCleanKey saves and restores the package-global key so these tests cannot
// leak a key into each other or into the rest of the package.
func withCleanKey(t *testing.T) {
	t.Helper()
	prev := encryptionKey
	t.Cleanup(func() { encryptionKey = prev })
	encryptionKey = nil
}

func writeKey(t *testing.T, dir string, b byte) []byte {
	t.Helper()
	key := bytes.Repeat([]byte{b}, 32)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EncryptionKeyPath(dir), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestInitEncryption_NeverOverwritesAnUnreadableKey is the most important test
// in this file.
//
// The previous implementation was `if data, err := os.ReadFile(p); err == nil {
// use it }` followed by an unconditional generate-and-os.WriteFile, and
// os.WriteFile TRUNCATES. Every read error took that path. For a key that was
// writable but unreadable -- mode 0200, or a transient EIO -- the write
// succeeded and the real key was gone, with the secrets encrypted under it
// unreadable forever.
//
// This test uses a DIRECTORY at the key path rather than chmod(0200), for two
// reasons: permission bits do not stop root, so a containerised CI run would
// pass a chmod-based test while proving nothing, and EISDIR exercises the same
// branch deterministically on every platform. The assertion is therefore about
// the branch -- "an unreadable key is a hard error, not a reason to generate" --
// not about which errno reaches it.
func TestInitEncryption_NeverOverwritesAnUnreadableKey(t *testing.T) {
	withCleanKey(t)
	dir := t.TempDir()

	// A directory where the key file belongs: present, but not readable as a file.
	unreadable := EncryptionKeyPath(dir)
	if err := os.MkdirAll(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	// Put something inside so a stray os.RemoveAll/WriteFile would have to work
	// to destroy it, and so we can prove afterwards it is untouched.
	canary := filepath.Join(unreadable, "canary")
	if err := os.WriteFile(canary, []byte("still here"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := InitEncryption(dir)
	if err == nil {
		t.Fatal("InitEncryption succeeded on an unreadable key path; it must refuse " +
			"rather than generate a replacement over the top of it")
	}
	if encryptionKey != nil {
		t.Errorf("a failed InitEncryption installed a key in the global anyway (%d bytes)", len(encryptionKey))
	}
	if _, serr := os.Stat(canary); serr != nil {
		t.Errorf("the existing key path was modified: %v", serr)
	}
}

// TestInitEncryption_LoadsFromLegacyDirWithoutCopying pins the production
// upgrade path: the key is still beside the database while the code now looks in
// the secure state directory.
//
// The "without copying" half is deliberate. Writing the key to the new location
// would leave the same secret in two files, owned by whatever user the service
// happens to run as, and the two copies would then be free to diverge.
func TestInitEncryption_LoadsFromLegacyDirWithoutCopying(t *testing.T) {
	withCleanKey(t)
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	want := writeKey(t, legacyDir, 0xAB)

	if err := InitEncryption(stateDir, legacyDir); err != nil {
		t.Fatalf("InitEncryption: %v", err)
	}
	if !bytes.Equal(encryptionKey, want) {
		t.Errorf("loaded key = %x, want the legacy key %x", encryptionKey, want)
	}
	if _, err := os.Stat(EncryptionKeyPath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the key was copied into the state dir; it must be left in place "+
			"for the operator to move (stat err = %v)", err)
	}
}

// TestInitEncryption_PrefersTheStateDirOverLegacy — once the operator has moved
// the file, the old one may still be lying around. The new location must win, or
// moving the file would have no effect.
func TestInitEncryption_PrefersTheStateDirOverLegacy(t *testing.T) {
	withCleanKey(t)
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	want := writeKey(t, stateDir, 0x11)
	writeKey(t, legacyDir, 0x22)

	if err := InitEncryption(stateDir, legacyDir); err != nil {
		t.Fatalf("InitEncryption: %v", err)
	}
	if !bytes.Equal(encryptionKey, want) {
		t.Errorf("loaded key = %x, want the state-dir key %x", encryptionKey, want)
	}
}

// TestInitEncryption_GeneratesOnlyWhenNothingExists covers the legitimate first
// run, and asserts the generated key actually lands in the state dir.
func TestInitEncryption_GeneratesOnlyWhenNothingExists(t *testing.T) {
	withCleanKey(t)
	stateDir, legacyDir := t.TempDir(), t.TempDir()

	if err := InitEncryption(stateDir, legacyDir); err != nil {
		t.Fatalf("InitEncryption: %v", err)
	}
	onDisk, err := os.ReadFile(EncryptionKeyPath(stateDir))
	if err != nil {
		t.Fatalf("no key written to the state dir: %v", err)
	}
	if len(onDisk) != 32 {
		t.Errorf("generated key is %d bytes, want 32", len(onDisk))
	}
	if !bytes.Equal(onDisk, encryptionKey) {
		t.Error("the key on disk differs from the one installed in the global")
	}
	if info, serr := os.Stat(EncryptionKeyPath(stateDir)); serr == nil {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("generated key mode = %o, want 0600", perm)
		}
	}
}

// TestInitEncryption_RejectsAWrongLengthKeyWithoutInstallingIt — the old code
// assigned to the global first and validated second, so a short file left a
// truncated key installed even though an error was returned. Anything that
// ignored the error then encrypted with it.
func TestInitEncryption_RejectsAWrongLengthKeyWithoutInstallingIt(t *testing.T) {
	withCleanKey(t)
	dir := t.TempDir()
	if err := os.WriteFile(EncryptionKeyPath(dir), []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InitEncryption(dir); err == nil {
		t.Fatal("InitEncryption accepted a 9-byte key")
	}
	if encryptionKey != nil {
		t.Errorf("a rejected key was installed in the global anyway: %q", encryptionKey)
	}
}

// TestFindEncryptionKey_TreatsUnreadableAsPresent — the probe exists to answer
// "is InitEncryption about to GENERATE?". An unreadable key means it will not
// (it will hard-fail), so reporting "no key here" would send the caller down
// the destructive-path checks for a situation that cannot reach them.
func TestFindEncryptionKey_TreatsUnreadableAsPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(EncryptionKeyPath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := FindEncryptionKey(dir); got != dir {
		t.Errorf("FindEncryptionKey = %q, want %q", got, dir)
	}
}

func TestFindEncryptionKey_OrderAndEmptyDirs(t *testing.T) {
	stateDir, legacyDir := t.TempDir(), t.TempDir()
	writeKey(t, legacyDir, 0x33)

	if got := FindEncryptionKey("", stateDir, legacyDir); got != legacyDir {
		t.Errorf("FindEncryptionKey = %q, want the legacy dir %q", got, legacyDir)
	}
	if got := FindEncryptionKey("", t.TempDir()); got != "" {
		t.Errorf("FindEncryptionKey = %q, want \"\" when no dir holds a key", got)
	}
}

// TestHasEncryptedSettings_ReadsTheFlagNotTheCiphertext pins the property that
// makes the startup guard possible at all: IsSecret is legible without the key.
// An empty Value does not count -- there is nothing to lose.
func TestHasEncryptedSettings_ReadsTheFlagNotTheCiphertext(t *testing.T) {
	for name, tc := range map[string]struct {
		settings []Setting
		want     bool
	}{
		"no settings":             {nil, false},
		"only plaintext":          {[]Setting{{Key: "root_dir", Value: "/srv"}}, false},
		"secret with a value":     {[]Setting{{Key: "openai_api_key", Value: "ciphertext", IsSecret: true}}, true},
		"secret with empty value": {[]Setting{{Key: "openai_api_key", Value: "", IsSecret: true}}, false},
		"mixed": {[]Setting{
			{Key: "root_dir", Value: "/srv"},
			{Key: "abs_jwt_secret", Value: "ciphertext", IsSecret: true},
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := HasEncryptedSettings(tc.settings); got != tc.want {
				t.Errorf("HasEncryptedSettings = %v, want %v", got, tc.want)
			}
		})
	}
}
