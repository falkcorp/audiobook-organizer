// file: internal/books/boot_test.go
// version: 1.1.1
// guid: 4e1b7c92-3a6d-4f08-8b25-9c0d7e5a1f36
// last-edited: 2026-11-09

package books

import (
	"errors"
	"path/filepath"
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
		// The drift arrives after the store opens (the stored config the
		// Facade binds); beforeSettings stands in for it.
		mod, opts, _ := composedOptions(t, abs, "", nil, nil)
		pre := mod.beforeSettings
		mod.beforeSettings = func(s *database.PebbleStore) {
			if pre != nil {
				pre(s)
			}
			config.Mutate(func(c *config.Config) { c.ABSAPIEnabled = !abs })
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
