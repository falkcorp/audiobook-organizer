// file: internal/books/flip_root_dir_test.go
// version: 0.1.0
// guid: e904a857-d9ee-45b1-a58e-85b889d73e96
// last-edited: 2026-11-21

package books

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// blobMember is one top-level member of the settings blob books' store holds
// now, nil when the member (or the blob) is absent.
func blobMember(t *testing.T, s database.SettingsStore, name string) json.RawMessage {
	t.Helper()
	row, err := s.GetSetting("config_blob")
	if err != nil || row == nil {
		return nil
	}
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Value), &m), "the settings blob is not a JSON object")
	return m[name]
}

// viewOf is the Facade's view of one setting.
func viewOf(t *testing.T, f config.Facade, name string) config.KeyView {
	t.Helper()
	for _, k := range f.View().Keys {
		if k.Name == name {
			return k
		}
	}
	t.Fatalf("the view lists no %s", name)
	return config.KeyView{}
}

// TestFlipBlobOnlyRootDirIsTheBootedRoot (05-config 1.12.1 "Class-i fields",
// 05-library 5L.4 step 1 and 5L.10): root_dir and path_aliases are class i. M2
// leaves them in the settings blob for M6, which only F7 ships, and until then
// the booted projection reads and writes them there. A store whose library
// root is only in the blob (no --dir, no ABO_CORE_LIBRARY_ROOTS: how
// production runs) boots with that root and its aliases, unlocked; the blob
// still holds them after the cutover; a save of a new root lands in the blob
// and nowhere in core.library.roots (M6 stages that list from the blob at F7,
// so a root saved anywhere else would be overwritten then); the next boot's
// projection sees the save; and a root M6 would quarantine (not absolute) is
// refused with nothing written. AUDIOBOOK_ROOT_DIR, which an older shipped
// unit sets and no build ever read, does not stop the boot, is not the root,
// and is named in a warning that says to delete it.
func TestFlipBlobOnlyRootDirIsTheBootedRoot(t *testing.T) {
	lib := filepath.Join(tempDir(t), "gotham-library")
	moved := filepath.Join(tempDir(t), "arkham-library")
	blob := fmt.Sprintf(`{"root_dir":%q,"path_aliases":[{"root":%q,"windows":"Z:\\gotham","unc":"","smb_url":""}]}`, lib, lib)
	fx := newFlipFixture(t, func(s *database.PebbleStore) {
		require.NoError(t, s.SetSetting("config_blob", blob, "json", false))
	}, "")
	fx.env = []string{"AUDIOBOOK_ROOT_DIR=" + filepath.Join(tempDir(t), "stale-unit-library")}

	fx.runFlipped(t, func(c *composed) {
		logged := c.newLog.String()
		require.Contains(t, logged, "AUDIOBOOK_ROOT_DIR", "the unread AUDIOBOOK_ROOT_DIR was not warned about")
		require.Contains(t, logged, "delete it: no build ever read it; the library root is the stored setting")

		snap := config.Snapshot()
		require.Equal(t, lib, snap.RootDir, "the flip boot lost the library root the settings blob holds")
		require.Len(t, snap.PathAliases, 1, "the flip boot lost the path aliases the settings blob holds")
		require.Equal(t, lib, snap.PathAliases[0].Root)
		require.Equal(t, `Z:\gotham`, snap.PathAliases[0].Windows)

		f := config.Current()
		require.NotNil(t, f)
		kv := viewOf(t, f, "root_dir")
		require.Equal(t, strconv.Quote(lib), string(kv.Value), "GET /api/v1/config does not show the stored root")
		require.Empty(t, kv.LockedBy, "no operator layer names the root, so nothing locks it")
		require.Equal(t, strconv.Quote(lib), string(blobMember(t, c.store, "root_dir")), "M2 moved root_dir out of the blob: it is class i, M6's to move")

		in := c.mod.gapFillIn.Load()
		require.NotNil(t, in, "the boot bound no Facade")
		rootsBefore, _ := in.acc.Lookup("core.library.roots")

		// A save of a new root is a write of the blob row.
		_, err, _ := putUpdate(t, f, map[string]any{"root_dir": moved})
		require.NoError(t, err)
		require.Equal(t, moved, config.Snapshot().RootDir, "the saved root is not the projected one")
		require.Equal(t, strconv.Quote(moved), string(blobMember(t, c.store, "root_dir")), "the saved root did not reach the settings blob")
		require.Len(t, config.Snapshot().PathAliases, 1, "a root-only save dropped the stored aliases")
		rootsAfter, _ := in.acc.Lookup("core.library.roots")
		require.Equal(t, string(rootsBefore.Effective), string(rootsAfter.Effective),
			"a root save wrote core.library.roots, which M6 overwrites from the blob at F7")

		// The next boot's projection: a new Engine over the same core store and
		// blob builds what was stored.
		fresh := appconfig.New(in.acc, nil,
			appconfig.WithBase(func() config.Config { return appConfigBase(c.mod.dbPath) }),
			appconfig.WithResidual(&configResidual{store: c.store, acc: in.acc}))
		require.NoError(t, fresh.Start(t.Context()))
		require.Equal(t, moved, fresh.Snapshot().RootDir, "a rebuilt projection lost the saved root")

		// A root M6 would quarantine is refused, naming the setting, and writes nothing.
		err = mustRefuse(t, f, map[string]any{"root_dir": "relative/library"})
		require.ErrorIs(t, err, coreconfig.ErrInvalid)
		require.Contains(t, err.Error(), "root_dir")
		require.Equal(t, moved, config.Snapshot().RootDir)
		require.Equal(t, strconv.Quote(moved), string(blobMember(t, c.store, "root_dir")))
	})
}

// TestFlipOperatorLibraryRootOverlaysTheBlob (05-config 1.12.1 with the lock
// rule of C11): an operator layer that names core.library.roots (here
// ABO_CORE_LIBRARY_ROOTS, ROOT_DIR's canonical name) is authoritative over the
// root the blob stores and locks root_dir and path_aliases, so a save of
// either is refused and the blob keeps what it had.
func TestFlipOperatorLibraryRootOverlaysTheBlob(t *testing.T) {
	stored := filepath.Join(tempDir(t), "gotham-library")
	operator := filepath.Join(tempDir(t), "blackgate-library")
	fx := newFlipFixture(t, func(s *database.PebbleStore) {
		require.NoError(t, s.SetSetting("config_blob", fmt.Sprintf(`{"root_dir":%q}`, stored), "json", false))
	}, "")
	roots := fmt.Sprintf(`[{"id":"books-library","path":%q,"role":"library","owner":"books","min_free":0}]`, operator)
	fx.env = []string{"ABO_CORE_LIBRARY_ROOTS=" + roots}

	fx.runFlipped(t, func(c *composed) {
		require.Equal(t, operator, config.Snapshot().RootDir, "the operator's library root does not win over the stored one")
		f := config.Current()
		require.NotNil(t, f)
		require.NotEmpty(t, viewOf(t, f, "root_dir").LockedBy, "an operator-set root is not shown locked")
		require.NotEmpty(t, viewOf(t, f, "path_aliases").LockedBy, "an operator-set root list does not lock the aliases")

		for _, set := range []map[string]any{
			{"root_dir": filepath.Join(tempDir(t), "arkham-library")},
			{"path_aliases": []any{}},
		} {
			_, err, moved := putUpdate(t, f, set)
			var locked *coreconfig.LockedError
			require.ErrorAs(t, err, &locked, "a save of %v over an operator lock", set)
			require.False(t, moved, "a refused save moved the generation")
		}
		require.Equal(t, strconv.Quote(stored), string(blobMember(t, c.store, "root_dir")), "a refused save changed the blob")
		require.Equal(t, operator, config.Snapshot().RootDir)
	})
}
