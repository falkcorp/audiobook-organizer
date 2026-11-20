// file: internal/books/boot_paths_test.go
// version: 1.0.0
// guid: 6c0d93e8-1f52-4b7a-a4e6-d59b8c27f013
// last-edited: 2026-11-20

package books

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// bootPathsSandbox is a state directory the resolution adopts, a working
// directory with nothing in it, a home with no settings file, and no store path
// in the environment: the ground each boot-path cell starts from.
func bootPathsSandbox(t *testing.T) (state, cwd, home string) {
	t.Helper()
	state = sandboxStateDirAt(t, filepath.Join(t.TempDir(), "state"))
	cwd, home = t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", home)
	t.Setenv(storagePathEnv, "")
	require.NoError(t, os.Unsetenv(storagePathEnv))
	return state, cwd, home
}

func resolveBoot(t *testing.T, boot BootInputs) (string, string, error) {
	t.Helper()
	return ResolveBootPaths(context.Background(), Module(), boot)
}

// With no layer naming a store and none left behind, the store is core's
// default under the state directory.
func TestResolveBootPaths_NoLayerNoFileIsTheStateDirDefault(t *testing.T) {
	state, _, _ := bootPathsSandbox(t)
	db, dir, err := resolveBoot(t, BootInputs{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(state, "storage", "main.pebble"), db)
	assert.Equal(t, state, dir)
	assert.Equal(t, state, config.SecureStateDir(), "the resolved directory is the one the key and token are looked for in")
}

// HEAD's --db defaulted to ./audiobooks.pebble. When no layer names a store and
// that one exists, opening core's default would serve an empty library that
// looks healthy: the boot refuses, naming the file and the variable that keeps
// it.
func TestResolveBootPaths_OrphanedStoreInTheWorkingDirectoryIsRefused(t *testing.T) {
	_, cwd, _ := bootPathsSandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(cwd, "audiobooks.pebble"), 0o700))
	_, _, err := resolveBoot(t, BootInputs{})
	require.Error(t, err)
	legacy, aerr := filepath.Abs("audiobooks.pebble")
	require.NoError(t, aerr)
	assert.Contains(t, err.Error(), legacy)
	assert.Contains(t, err.Error(), "ABO_CORE_STORAGE_PATH")
}

// The orphan rule fires only when nothing named a store: any layer (the
// environment here, --db below) is the operator's answer.
func TestResolveBootPaths_ANamedStoreIsNotOrphanRefused(t *testing.T) {
	_, cwd, _ := bootPathsSandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(cwd, "audiobooks.pebble"), 0o700))
	named := filepath.Join(t.TempDir(), "gotham.pebble")
	t.Setenv(storagePathEnv, named)
	db, _, err := resolveBoot(t, BootInputs{})
	require.NoError(t, err)
	assert.Equal(t, named, db)

	other := filepath.Join(t.TempDir(), "arkham.pebble")
	t.Setenv(storagePathEnv, "")
	require.NoError(t, os.Unsetenv(storagePathEnv))
	db, _, err = resolveBoot(t, BootInputs{Flags: map[coreconfig.Path]string{"core.storage.path": other}})
	require.NoError(t, err)
	assert.Equal(t, other, db)
}

// The production unit names the store in the environment only
// (deploy/audiobook-organizer.service: ABO_CORE_STORAGE_PATH, no --db). That
// layer alone, and the same value repeated as --db, resolve to it with none of
// core's "layers differ" refusals.
func TestResolveBootPaths_TheProductionUnitsStorageEnvResolves(t *testing.T) {
	state, cwd, _ := bootPathsSandbox(t)
	// A stale HEAD store beside the working directory must not matter here.
	require.NoError(t, os.Mkdir(filepath.Join(cwd, "audiobooks.pebble"), 0o700))
	prod := filepath.Join(t.TempDir(), "audiobooks.pebble")
	t.Setenv(storagePathEnv, prod)

	db, dir, err := resolveBoot(t, BootInputs{})
	require.NoError(t, err)
	assert.Equal(t, prod, db)
	assert.Equal(t, state, dir)

	db, _, err = resolveBoot(t, BootInputs{Flags: map[coreconfig.Path]string{"core.storage.path": prod}})
	require.NoError(t, err, "the unit's env and an explicit --db of the same path must agree")
	assert.Equal(t, prod, db)
}

// A HEAD-keyed settings file at the place HEAD read one by default is refused
// by name, whichever of the two extensions it has; --config names the
// operator file explicitly and is core's to judge.
func TestResolveBootPaths_HEADHomeSettingsFileIsRefused(t *testing.T) {
	for _, name := range []string{".audiobook-organizer.yaml", ".audiobook-organizer.yml"} {
		t.Run(name, func(t *testing.T) {
			_, _, home := bootPathsSandbox(t)
			p := filepath.Join(home, name)
			require.NoError(t, os.WriteFile(p, []byte("root_dir: /x\n"), 0o600))
			_, _, err := resolveBoot(t, BootInputs{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), p)
			assert.Contains(t, err.Error(), "ABO_CONFIG")

			// An explicit operator file is not this refusal's business.
			op := filepath.Join(t.TempDir(), "operator.yaml")
			require.NoError(t, os.WriteFile(op, []byte("{}\n"), 0o600))
			_, _, err = resolveBoot(t, BootInputs{ConfigFile: op})
			if err != nil {
				assert.NotContains(t, err.Error(), p)
			}
		})
	}
}

// The key is looked for where the checkpoint's state-file table says it is,
// in the default layout (key in the state directory) and in the fallback one
// (key still beside the store, state directory elsewhere): one rule, two
// readers.
func TestEncryptionKeyIsFoundWhereTheCheckpointSavesIt(t *testing.T) {
	key := bytes.Repeat([]byte{5}, 32)
	for _, tc := range []struct {
		name       string
		inStateDir bool
	}{{"default layout: key in the state directory", true}, {"fallback layout: key beside the store", false}} {
		t.Run(tc.name, func(t *testing.T) {
			state, store := t.TempDir(), filepath.Join(t.TempDir(), "gotham.pebble")
			require.NoError(t, os.MkdirAll(filepath.Dir(store), 0o700))
			holder := filepath.Dir(store)
			if tc.inStateDir {
				holder = state
			}
			require.NoError(t, os.WriteFile(database.EncryptionKeyPath(holder), key, 0o600))

			var saved string
			for _, f := range StateFiles(state, store) {
				if f.Label == "encryption_key" {
					saved = f.Path
				}
			}
			assert.Equal(t, database.EncryptionKeyPath(holder), saved, "the checkpoint saves a different file than the one holding the key")
			assert.Equal(t, holder, database.FindEncryptionKey(state, legacyKeyDir(store)), "the boot's guard looks somewhere else")
			require.NoError(t, database.InitEncryption(state, legacyKeyDir(store)))
		})
	}
}

// Standalone openers (the op-runner child, the live-restore verifier) open a
// store the flip has raised only when they plan the flip's levels: the plain
// options refuse it as a store newer than the build, StandaloneOptions open it.
func TestStandaloneOptionsOpenAFlippedStore(t *testing.T) {
	fx := newFlipFixture(t, nil, "")
	fx.runFlipped(t, nil)

	st, err := database.OpenStandalone(fx.dbPath, StandaloneOptions(database.StandaloneOptions{}))
	require.NoError(t, err, "the flip's levels were not planned")
	require.NoError(t, st.Close())

	_, err = database.OpenStandalone(fx.dbPath, database.StandaloneOptions{})
	require.Error(t, err, "a standalone open that does not plan the flip's levels accepted a raised store")
}
