// file: internal/books/boot_test.go
// version: 1.0.0
// guid: 4e1b7c92-3a6d-4f08-8b25-9c0d7e5a1f36
// last-edited: 2026-11-08

package books

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// defaults. loadConfigFromDB is the one whose loss is silent: the first
// settings save would write the defaults over the stored config (whole-struct
// SaveConfigToDatabase). It was a printed warning until 2026-10-10.
func TestSettingsBoot_StepFailuresAreFatal(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		breakStep  func()
	}{
		{"initEncryption", "failed to initialize encryption", func() {
			initEncryption = func(string, ...string) error { return errors.New("Joker swapped the key") }
		}},
		{"loadConfigFromDB", "failed to load config from database", func() {
			loadConfigFromDB = func(database.SettingsStore) error { return errors.New("read settings: Riddler") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, opts, _ := composedOptions(t, false, nil, nil)
			origEnc := initEncryption
			t.Cleanup(func() { initEncryption = origEnc })
			tc.breakStep()
			err := bootFails(t, mod, opts)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A real Pebble store whose config_blob row is not a valid setting record
// (PR #3930 SF1, moved from cmd with the settings boot): the real loader
// fails, so the boot fails before the server exists, and the error names the
// row. This is also a first R1 boot of a store HEAD wrote (no core keys).
func TestSettingsBoot_CorruptConfigBlobRowIsFatal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "arkham.pebble")
	raw, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	require.NoError(t, raw.Set([]byte("setting:config_blob"), []byte("{truncated"), pebble.Sync))
	require.NoError(t, raw.Close())

	mod, opts, _ := composedOptions(t, false, func(c *config.Config) { c.DatabasePath = dbPath }, nil)
	loadConfigFromDB = config.LoadConfigFromDatabase
	opts.StorageFS = storage.FS{}
	err = bootFails(t, mod, opts)
	assert.Contains(t, err.Error(), "config_blob", "the error does not name the corrupt row")
}

// The manifest's legacy prefixes are fixed from the environment before the
// store opens; stored settings that turn the ABS surface the other way
// refuse the boot instead of failing later on a route outside the prefixes.
func TestSettingsBoot_ABSFlagDriftRefuses(t *testing.T) {
	for _, abs := range []bool{false, true} {
		mod, opts, _ := composedOptions(t, abs, nil, nil)
		loadConfigFromDB = func(database.SettingsStore) error {
			config.Mutate(func(c *config.Config) { c.ABSAPIEnabled = !abs })
			return nil
		}
		err := bootFails(t, mod, opts)
		assert.ErrorIs(t, err, errABSFlagDrift, "abs=%v", abs)
	}
}

// Options refuses what would make core open a store HEAD never named: a
// database_type other than Pebble, and no database path (core would open a
// new, empty store at its own default and books would serve an empty
// library).
func TestOptions_RefusesAStoreHEADWouldNotOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{"sqlite", func(c *config.Config) { c.DatabaseType = "sqlite" }, "SQLite3 support has been removed"},
		{"no path", func(c *config.Config) { c.DatabasePath = "" }, errNoDatabasePath.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Config{DatabaseType: "pebble", DatabasePath: filepath.Join(tempDir(t), "books.pebble")}
			tc.edit(&c)
			withConfig(t, c)
			sandboxStateDir(t)
			_, err := Options(Module(), ServeInputs{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Options hands core HEAD's database path, made absolute, the state
// directory ABO resolved, and the state-file table naming HEAD's key where
// InitEncryption reads it and the gap-fill file beside the database.
func TestOptions_StorageInputs(t *testing.T) {
	db := filepath.Join(tempDir(t), "books.pebble")
	withConfig(t, config.Config{DatabaseType: "pebble", DatabasePath: db})
	stateDir := sandboxStateDir(t)
	mod := Module()
	opts, err := Options(mod, ServeInputs{})
	require.NoError(t, err)
	assert.Equal(t, db, opts.R1StoragePath)
	assert.Equal(t, stateDir, opts.StateDir)
	assert.Equal(t, []storage.StateFile{
		{Label: "encryption_key", Path: database.EncryptionKeyPath(stateDir)},
		{Label: "gap_fill", Path: filepath.Join(filepath.Dir(db), "config.yaml")},
	}, opts.StorageStateFiles)
	m := mod.(*booksModule)
	require.NotNil(t, m.steps, "Options made no startup runner")
	assert.Same(t, m.steps, m.serveConfig().StartupSteps, "the server does not get books' startup runner")
}
