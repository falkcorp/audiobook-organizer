// file: internal/books/boot_test.go
// version: 1.1.1
// guid: 4e1b7c92-3a6d-4f08-8b25-9c0d7e5a1f36
// last-edited: 2026-11-09

package books

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// bootFails runs app.New over books with opts and returns its error, which
// must be non-nil. Nothing may be left built: no server, and the store
// Register built (if any) closed by Release, as serve's deferred Release
// does.
func bootFails(t *testing.T, mod *booksModule, opts app.Options) error {
	t.Helper()
	a, err := app.New(t.Context(), opts, mod)
	require.Error(t, err, "app.New booted")
	require.Nil(t, a)
	Release(mod)
	assert.Nil(t, mod.srv.Load(), "a failed settings boot built the server")
	assert.Nil(t, database.GetGlobalStore(), "a failed boot left the global store set")
	return err
}

// Each settings-boot step is Fatal, as it was in serve (startup.Table): a
// failure stops app.New before the server is built, so nothing serves on
// defaults.
func TestSettingsBoot_StepFailuresAreFatal(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		breakStep  func()
	}{
		{"initEncryption", "failed to initialize encryption", func() {
			initEncryption = func(string, ...string) error { return errors.New("Joker swapped the key") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, opts, _ := composedOptions(t, false, "", nil, nil)
			origEnc := initEncryption
			t.Cleanup(func() { initEncryption = origEnc })
			tc.breakStep()
			err := bootFails(t, mod, opts)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The manifest's legacy prefixes are fixed from the environment before the
// store opens; stored settings that turn the ABS surface the other way
// refuse the boot instead of failing later on a route outside the prefixes.
func TestSettingsBoot_ABSFlagDriftRefuses(t *testing.T) {
	for _, abs := range []bool{false, true} {
		// Module read the flag from the environment when it was built. The
		// drift is the settings core projects at boot saying the opposite: the
		// environment layer changed since (a unit edited between Module and
		// New), so the projection and the manifest's prefixes disagree.
		mod, opts, _ := composedOptions(t, abs, "", nil, nil)
		t.Setenv(absEnabledEnv, strconv.FormatBool(!abs))
		err := bootFails(t, mod, opts)
		assert.ErrorIs(t, err, errABSFlagDrift, "abs=%v", abs)
	}
}

// absMisconfigCases are the ABS settings the preflight refuses: each is a boot
// that used to reach wireABSRoutes inside NewServer, which logged and called
// os.Exit(1).
var absMisconfigCases = []struct {
	name, want string
	edit       func(*config.Config)
	modes      string // the ABS auth modes layer (the environment), "" for the default
}{
	{"no secret", "ABS_JWT_SECRET is required", func(c *config.Config) { c.ABSJWTSecret = "" }, ""},
	{"short secret", "at least", func(c *config.Config) { c.ABSJWTSecret = "short" }, ""},
	{"unknown mode", "ABS_AUTH_MODES", nil, "jwt,ldap"},
	{"cf only, no Access settings", "CF_ACCESS_TEAM_DOMAIN", nil, "cf"},
}

const absMisconfigChildEnv = "ABO_TEST_ABS_MISCONFIG_CASE"

// An enabled ABS surface whose settings cannot build it refuses the boot with a
// returned error naming the reason, instead of exiting the process from inside
// NewServer. Each case boots in a child test process: with the preflight gone
// the child is exited by NewServer, and only a parent that watches the child
// can report that as a failure of this test rather than a dead test binary.
func TestSettingsBoot_ABSMisconfiguredRefuses(t *testing.T) {
	if os.Getenv(absMisconfigChildEnv) != "" {
		t.Skip("the child half runs it (TestSettingsBoot_ABSMisconfiguredRefusesChild)")
	}
	for _, tc := range absMisconfigCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSettingsBoot_ABSMisconfiguredRefusesChild$", "-test.v")
			cmd.Env = append(os.Environ(), absMisconfigChildEnv+"="+tc.name)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "the boot did not return an error: the child process was exited by the boot (os.Exit in NewServer) or failed:\n%s", out)
			assert.Contains(t, string(out), "PASS: TestSettingsBoot_ABSMisconfiguredRefusesChild", "the child did not run its assertions:\n%s", out)
		})
	}
}

// TestSettingsBoot_ABSMisconfiguredRefusesChild is one case of the test above,
// chosen by the environment. Alone it is skipped.
func TestSettingsBoot_ABSMisconfiguredRefusesChild(t *testing.T) {
	name := os.Getenv(absMisconfigChildEnv)
	if name == "" {
		t.Skip("run by TestSettingsBoot_ABSMisconfiguredRefuses")
	}
	for _, tc := range absMisconfigCases {
		if tc.name != name {
			continue
		}
		if tc.modes != "" {
			t.Setenv("ABO_BOOKS_ABS_AUTH_MODES", tc.modes)
		}
		mod, opts, _ := composedOptions(t, true, "", tc.edit, nil)
		err := bootFails(t, mod, opts)
		assert.ErrorIs(t, err, errABSMisconfigured)
		assert.Contains(t, err.Error(), tc.want)
		return
	}
	t.Fatalf("no case %q", name)
}

// Options hands core HEAD's database path, made absolute, the state
// directory ABO resolved, and the state-file table naming HEAD's key where
// InitEncryption reads it and the gap-fill file beside the database.
func TestOptions_StorageInputs(t *testing.T) {
	db := filepath.Join(tempDir(t), "books.pebble")
	withConfig(t, config.Config{DatabaseType: "pebble", DatabasePath: db})
	t.Setenv(storagePathEnv, db)
	stateDir := sandboxStateDir(t)
	mod := Module()
	opts, err := Options(mod, ServeInputs{})
	require.NoError(t, err)
	gotDB, err := OfflineStorePath(opts)
	require.NoError(t, err)
	assert.Equal(t, db, gotDB, "no compiled store path: core's step 0 resolves the layers to the one named")
	assert.Equal(t, db, mod.(*booksModule).dbPath)
	assert.Equal(t, stateDir, opts.StateDir)
	assert.Equal(t, []storage.StateFile{
		{Label: "encryption_key", Path: database.EncryptionKeyPath(stateDir)},
		{Label: "gap_fill", Path: filepath.Join(filepath.Dir(db), "config.yaml")},
	}, opts.StorageStateFiles)
	m := mod.(*booksModule)
	require.NotNil(t, m.steps, "Options made no startup runner")
	assert.Same(t, m.steps, m.serveConfig().StartupSteps, "the server does not get books' startup runner")
}

// TestBooksConstructorWritesNothing (05-storage §5S.4 step 12): books' P6
// constructor commits no write. app.New over a store holding a HEAD-shaped
// config_blob leaves the counter on books' handle where it stood and the
// stored blob byte-identical. (Until F3-07 Register also ran HEAD's settings
// load, whose migrations were deferred to the repairs hook; the loader and
// its writes are gone.)
func TestBooksConstructorWritesNothing(t *testing.T) {
	var before uint64
	runComposedWith(t, nil,
		func(s *database.PebbleStore) {
			require.NoError(t, s.SetSetting("config_blob", flatEmbeddingBlob, "json", false))
			before = s.CommittedWrites()
		},
		nil,
		func(c *composed) {
			assert.Equal(t, before, c.store.CommittedWrites(), "books' P6 constructor committed a write")
			blob, err := c.store.GetSetting("config_blob")
			require.NoError(t, err)
			assert.JSONEq(t, flatEmbeddingBlob, blob.Value, "Register rewrote the stored blob")
		})
}
