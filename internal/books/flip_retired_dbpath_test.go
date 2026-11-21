// file: internal/books/flip_retired_dbpath_test.go
// version: 0.1.0
// guid: 77bd654b-f34c-4124-be48-381a12f7be56
// last-edited: 2026-11-21

package books

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestRetiredDatabasePathRefusesStoreCreate (14 K15, E6; 05-config 1.3.6): an
// install whose unit still names its store through HEAD's DATABASE_PATH (or the
// flat database_path key of an operator file) and names no canonical
// core.storage.path would, if the retired name were merely ignored, open a new,
// empty store at core's default under the state directory and serve an empty
// library looking healthy. With a retired database-path name set, the boot is
// refused before the kernel's P2 can create a store, on a first install, in two
// places. Serve's entry (books.Options, through ResolveBootPaths and core's step
// 0, over the real process environment) refuses with *config.RetiredNameError
// naming ABO_CORE_STORAGE_PATH (for the file key, with books' own refusal of a
// HEAD-keyed operator file) and creates nothing: no state directory, no store
// directory, no file. app.New over the same environment (core's P0 config boot)
// refuses the same way; it runs after core has opened its log file, so the state
// directory exists afterwards holding only that file. In neither is a store
// directory created, the one the unit's value named or core's default one. The
// control boots, with the canonical name instead, create the store and the
// state, so the absence is the refusal's and not the harness's.
func TestRetiredDatabasePathRefusesStoreCreate(t *testing.T) {
	// fresh is a first install: a parent holding neither the state directory nor
	// any store directory. composedOptions makes the directories its fixture
	// needs; they are removed again before the boot, which is the install a
	// first boot starts from.
	type install struct{ parent, state, storeDir, db string }
	fresh := func(t *testing.T) install {
		t.Helper()
		parent := t.TempDir()
		in := install{parent: parent, state: filepath.Join(parent, "state"), storeDir: filepath.Join(parent, "batcave")}
		in.db = filepath.Join(in.storeDir, "gotham.pebble")
		return in
	}
	// boot builds the composition, clears what its fixture made, and runs
	// app.New with the operator environment env and the operator file file ("" for
	// none). The process environment names the canonical store path (the fixture
	// sets it): app.Options.Env replaces it, so the boot sees only env.
	boot := func(t *testing.T, in install, env []string, file string) error {
		t.Helper()
		edit := func(c *config.Config) { c.DatabasePath = in.db }
		return bootOnce(t, in.state, edit, func(o *app.Options) {
			o.StorageFS = storage.FS{} // the real filesystem: the store is on disk, not in memory
			o.StateDir = in.state
			o.Env = append([]string{listenEnv}, env...) // nil would fall back to the process environment
			o.ConfigFile = file
		}, func() {
			require.NoError(t, os.RemoveAll(in.storeDir))
			require.NoError(t, os.RemoveAll(in.state))
		})
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	// serveGround is the ground a first boot's resolution starts from: the state
	// directory named explicitly and absent, a working directory and a home with
	// nothing in them, and no canonical store path in the environment.
	serveGround := func(t *testing.T, state string) {
		t.Helper()
		restoreConfigForTest(t)
		config.ResetStateDirForTest()
		t.Cleanup(config.ResetStateDirForTest)
		t.Chdir(t.TempDir())
		t.Setenv("HOME", t.TempDir())
		t.Setenv(storagePathEnv, "")
		require.NoError(t, os.Unsetenv(storagePathEnv))
		t.Setenv("ABO_CORE_PATHS_STATE_DIR", state)
	}

	for _, c := range []struct {
		name string
		env  []string
		file string // the operator file's content
		// retired and isFile are the use the refusal reports.
		retired string
		isFile  bool
	}{
		{name: "env DATABASE_PATH", env: []string{"DATABASE_PATH=batcave/gotham.pebble"}, retired: "DATABASE_PATH"},
		{name: "operator file key database_path", file: "database_path: batcave/gotham.pebble\n", retired: "database_path", isFile: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := fresh(t)
			file := ""
			if c.file != "" {
				file = filepath.Join(t.TempDir(), "operator.yaml")
				require.NoError(t, os.WriteFile(file, []byte(c.file), 0o600))
			}
			err := boot(t, in, c.env, file)
			require.Error(t, err, "a boot with the retired %s set succeeded", c.retired)

			var retired *coreconfig.RetiredNameError
			require.True(t, errors.As(err, &retired), "the refusal is not a *config.RetiredNameError: %v", err)
			require.Len(t, retired.Uses, 1, "%v", err)
			use := retired.Uses[0]
			require.Equal(t, c.retired, use.Name)
			require.Equal(t, c.isFile, use.File)
			require.True(t, use.Refused, "%+v", use)
			require.Equal(t, coreconfig.Path("core.storage.path"), use.Canonical)
			require.Equal(t, "ABO_CORE_STORAGE_PATH", use.CanonicalEnv)
			require.Contains(t, err.Error(), c.retired)
			require.Contains(t, err.Error(), "ABO_CORE_STORAGE_PATH")

			// Nothing of the install was created: not the unit's store directory, not
			// core's default one under the state directory, and nothing in the state
			// directory but the log file. That one file is core's own: the kernel opens
			// core.log.output=file (state/logs/abo.log) in buildLogging, before P0's
			// config boot judges the retired name (core/kernel/boot.go), so the state
			// directory exists after a refused first boot though it holds no key, no
			// sidecar and no store. Anything else under the parent fails the cell.
			require.False(t, exists(in.storeDir), "the refused boot created the store directory the retired name pointed at")
			require.False(t, exists(filepath.Join(in.state, "storage")), "the refused boot created core's default store directory")
			logs := filepath.Join("state", "logs") + string(filepath.Separator)
			for rel := range dirImage(t, in.parent) {
				require.True(t, strings.HasPrefix(rel, logs), "the refused boot wrote %s, which is not the log file", rel)
			}
		})
	}

	// The serve entry: books.Options resolves the store and the state directory
	// from the real process environment and the operator file through core's step
	// 0 before app.New runs (ResolveBootPaths), and step 0 judges a retired name
	// first (core/kernel/config.go step0), so the refusal there creates nothing at
	// all: not the state directory, not a store directory, not the log file. This
	// is the path serve takes, so it is the strict cell.
	for _, c := range []struct {
		name, env, file string
		retired         string
		isFile          bool
	}{
		{name: "env DATABASE_PATH", env: "batcave/gotham.pebble", retired: "DATABASE_PATH"},
		{name: "operator file key database_path", file: "database_path: batcave/gotham.pebble\n", retired: "database_path", isFile: true},
	} {
		t.Run("serve resolution: "+c.name, func(t *testing.T) {
			in := fresh(t)
			serveGround(t, in.state)
			if c.env != "" {
				t.Setenv("DATABASE_PATH", c.env)
			}
			var file string
			if c.file != "" {
				file = filepath.Join(t.TempDir(), "operator.yaml")
				require.NoError(t, os.WriteFile(file, []byte(c.file), 0o600))
			}
			_, _, err := ResolveBootPaths(t.Context(), Module(), BootInputs{ConfigFile: file})
			require.Error(t, err, "serve's resolution with the retired %s set succeeded", c.retired)
			if c.isFile {
				// A HEAD-keyed operator file is books' own refusal, ahead of core's
				// retired-name check (refuseHEADKeyedConfigFile): it names the file and
				// the key, and the nothing-created assertions below are what it shares
				// with the env cell.
				require.Contains(t, err.Error(), file)
				require.Contains(t, err.Error(), c.retired)
			} else {
				var retired *coreconfig.RetiredNameError
				require.True(t, errors.As(err, &retired), "the refusal is not a *config.RetiredNameError: %v", err)
				require.Len(t, retired.Uses, 1, "%v", err)
				require.Equal(t, c.retired, retired.Uses[0].Name)
				require.False(t, retired.Uses[0].File)
				require.Equal(t, "ABO_CORE_STORAGE_PATH", retired.Uses[0].CanonicalEnv)
				require.Contains(t, err.Error(), c.retired)
				require.Contains(t, err.Error(), "ABO_CORE_STORAGE_PATH")
			}
			// Both refusals are the retired-name class, so the process exits 78
			// and systemd's RestartPreventExitStatus stops a restart loop.
			require.ErrorIs(t, err, coreconfig.ErrRetiredName)
			require.Equal(t, kernel.ExitConfig, kernel.ExitCode(err), "%v", err)
			require.False(t, exists(in.state), "the refused resolution created the state directory")
			require.False(t, exists(in.storeDir), "the refused resolution created the store directory the retired name pointed at")
			require.Empty(t, dirImage(t, in.parent), "the refused resolution wrote under the install's parent directory")
		})
	}

	t.Run("serve resolution control: the canonical name instead resolves and creates the state directory", func(t *testing.T) {
		in := fresh(t)
		serveGround(t, in.state)
		t.Setenv(storagePathEnv, in.db)
		db, dir, err := ResolveBootPaths(t.Context(), Module(), BootInputs{})
		require.NoError(t, err)
		require.Equal(t, in.db, db)
		require.Equal(t, in.state, dir)
		require.True(t, exists(in.state), "the control resolution created no state directory: the refusal cells above prove nothing")
	})

	t.Run("control: the canonical name instead boots and creates the store", func(t *testing.T) {
		in := fresh(t)
		require.NoError(t, boot(t, in, []string{"ABO_CORE_STORAGE_PATH=" + in.db}, ""))
		require.True(t, exists(in.db), "the control boot created no store at %s: the refusal cells above prove nothing", in.db)
		require.True(t, exists(in.state), "the control boot created no state directory")
	})
}
