// file: core/kernel/stateowned_test.go
// version: 0.1.0
// guid: b3c92e5d-6a0f-47d1-9e84-52f7a1d8c03e
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// ownedFixture is a state directory and a store beside it, with a file at
// every path core owns and a few that it does not, all on the real disk.
type ownedFixture struct {
	state, dbParent, db string
	// owned maps a case name to a path the handle must refuse.
	owned map[string]string
	// free maps a case name to a path the handle must remove.
	free map[string]string
}

const gothamSidecar = ".gotham-legacy"

func newOwnedFixture(t *testing.T, keyFile string) (ownedFixture, *coreOwned) {
	t.Helper()
	state, dbParent := t.TempDir(), t.TempDir()
	f := ownedFixture{state: state, dbParent: dbParent, db: filepath.Join(dbParent, "gotham.pebble")}
	s := func(p ...string) string { return filepath.Join(append([]string{state}, p...)...) }
	d := func(p ...string) string { return filepath.Join(append([]string{dbParent}, p...)...) }
	ring := keyFile
	if ring == "" {
		ring = "secrets.keyring"
	}
	f.owned = map[string]string{
		"the store":                      f.db,
		"a file inside the store":        filepath.Join(f.db, "000005.sst"),
		"a levels sidecar":               f.db + restorejournal.LevelsSuffix,
		"a checkpoint sidecar":           f.db + restorejournal.CheckpointSuffix,
		"a build's legacy sidecar":       f.db + gothamSidecar,
		"the checkpoint directory":       d(".mediacore-checkpoints"),
		"a checkpoint":                   d(".mediacore-checkpoints", "cp-1", "MANIFEST"),
		"a swap's private directory":     d(restorejournal.AsideDirName),
		"inside a swap's private dir":    d(restorejournal.AsideDirName, "previous", "x"),
		"the swap lock":                  restorejournal.SwapLockPath(state),
		"the restore slot":               restorejournal.PendingPath(state),
		"the restore directory":          restorejournal.Dir(state),
		"the keyring":                    s(ring),
		"the keyring's export record":    s(ring + ".exported"),
		"a keyring temporary file":       s(ring + ".tmp-0a1b2c3d4e5f"),
		"an export record temporary":     s(ring + ".exported.tmp-0a1b2c3d4e5f"),
		"the boot record":                s("config.boot.json"),
		"the boot record's temporary":    s("config.boot.json.tmp"),
		"the quarantined boot record":    s("config.boot.json.bad"),
		"the bootstrap token":            s(auth.BootstrapTokenFile),
		"the backup location file":       s(journal.LocationFile),
		"the backup location's tempfile": s(journal.LocationFile + ".tmp"),
	}
	if dir := filepath.Dir(ring); dir != "." {
		f.owned["the directory of the keyring"] = s(dir)
	}
	f.free = map[string]string{
		"a stale search index beside the store": d("gotham.bleve"),
		"a file in it":                          d("gotham.bleve", "store", "root.bolt"),
		"a legacy key in the state directory":   s(".batcave_key"),
		"a file named like the keyring":         s(ring + ".bak"),
		"a file named like the boot record":     s("config.boot.jsonx"),
		"a sibling of the store":                d("gotham.pebble-old"),
	}
	var all []string
	for _, m := range []map[string]string{f.owned, f.free} {
		for _, p := range m {
			all = append(all, p)
		}
	}
	// A path another one lies under is a directory; the rest are files.
	isDir := func(p string) bool {
		for _, q := range all {
			if strings.HasPrefix(q, p+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	for _, p := range all {
		if isDir(p) {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("gotham"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	owners := []storage.OwnerSpec{{Spec: storage.Spec{LegacyGuard: &storage.LegacyGuardProjection{SidecarSuffix: gothamSidecar}}}}
	return f, newCoreOwned(state, f.db, keyFile, owners)
}

// TestModuleStateFS_RefusesCoreOwnedPaths: the handle refuses, with
// ErrStateCoreOwned (and so ErrStateConfined), a Remove or RemoveAll of the
// open store, anything inside it, and each file core owns in the state
// directory or beside the store, and removes nothing, for the default keyring
// file and for one core.secrets.key_file moved into a subdirectory.
func TestModuleStateFS_RefusesCoreOwnedPaths(t *testing.T) {
	for _, keyFile := range []string{"", filepath.Join("vault", "batcave.ring")} {
		t.Run("key_file="+keyFile, func(t *testing.T) {
			f, owned := newOwnedFixture(t, keyFile)
			s := newModuleState(nil, owned, f.state, f.dbParent)
			ctx := context.Background()
			for name, p := range f.owned {
				for _, all := range []bool{false, true} {
					var err error
					if all {
						err = s.RemoveAll(ctx, "bat.x", p)
					} else {
						err = s.Remove(ctx, "bat.x", p)
					}
					if !errors.Is(err, module.ErrStateCoreOwned) || !errors.Is(err, module.ErrStateConfined) {
						t.Errorf("%s (all=%v): %v, want ErrStateCoreOwned wrapping ErrStateConfined", name, all, err)
					}
					if _, statErr := os.Lstat(p); statErr != nil {
						t.Errorf("%s (all=%v): the refused call removed %s", name, all, p)
					}
				}
			}
		})
	}
}

// TestModuleStateFS_RefusesAnAncestorOfTheStore: a directory that contains
// the store is refused too, as the store's own removal would be. The store
// sits two levels below the db parent root here, so the directory between is
// a path no root rule refuses.
func TestModuleStateFS_RefusesAnAncestorOfTheStore(t *testing.T) {
	state, dbParent := t.TempDir(), t.TempDir()
	db := filepath.Join(dbParent, "arkham", "wing", "gotham.pebble")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	s := newModuleState(nil, newCoreOwned(state, db, "", nil), state, dbParent)
	for _, p := range []string{filepath.Join(dbParent, "arkham"), filepath.Join(dbParent, "arkham", "wing")} {
		for _, all := range []bool{false, true} {
			var err error
			if all {
				err = s.RemoveAll(context.Background(), "bat.x", p)
			} else {
				err = s.Remove(context.Background(), "bat.x", p)
			}
			if !errors.Is(err, module.ErrStateCoreOwned) {
				t.Errorf("%s (all=%v): %v, want ErrStateCoreOwned", p, all, err)
			}
		}
	}
	if _, err := os.Lstat(db); err != nil {
		t.Fatalf("the store is gone: %v", err)
	}
}

// TestModuleStateFS_RemovesWhatCoreDoesNotOwn: beside every refused path, a
// stale `<db parent>/gotham.bleve`-shaped search index, an app's legacy key and
// files that only share a prefix with a core file are removed, whole and by
// path, so the rule refuses what core owns and nothing around it.
func TestModuleStateFS_RemovesWhatCoreDoesNotOwn(t *testing.T) {
	f, owned := newOwnedFixture(t, "")
	s := newModuleState(nil, owned, f.state, f.dbParent)
	for name, p := range f.free {
		if _, err := os.Lstat(p); err != nil {
			continue // removed with its directory by an earlier case
		}
		if err := s.RemoveAll(context.Background(), "bat.x", p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !gone(t, p) {
			t.Errorf("%s: %s survived", name, p)
		}
	}
	for name, p := range f.owned {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: %s is gone after the cleanup", name, p)
		}
	}
}

// TestModuleStateFS_CoreOwnedFollowsADirectoryLink: a path reached through a
// symbolic link to a directory is judged where it lands, so a link into the
// store's directory is no way round the rule, while a link to the store is
// removed as a link and the store stays.
func TestModuleStateFS_CoreOwnedFollowsADirectoryLink(t *testing.T) {
	f, owned := newOwnedFixture(t, "")
	if err := os.Symlink(f.dbParent, filepath.Join(f.state, "around")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.db, filepath.Join(f.state, "to-store")); err != nil {
		t.Fatal(err)
	}
	s := newModuleState(nil, owned, f.state, f.dbParent)
	ctx := context.Background()
	if err := s.RemoveAll(ctx, "bat.x", filepath.Join(f.state, "around", "gotham.pebble")); !errors.Is(err, module.ErrStateCoreOwned) {
		t.Fatalf("the store through a directory link = %v, want ErrStateCoreOwned", err)
	}
	if err := s.RemoveAll(ctx, "bat.x", filepath.Join(f.state, "to-store")); err != nil {
		t.Fatalf("removing a link to the store = %v, want nil", err)
	}
	if _, err := os.Lstat(f.db); err != nil {
		t.Fatalf("removing the link removed the store: %v", err)
	}
}

// TestModuleStateFS_CoreOwnedRefusalRecordsNothing: under a boot's seam a
// refused call performs no operation at all, so a refused hook leaves nothing
// for the crash kit to cut.
func TestModuleStateFS_CoreOwnedRefusalRecordsNothing(t *testing.T) {
	f, owned := newOwnedFixture(t, "")
	mem := vfs.NewCrashableMem()
	if err := mem.MkdirAll(f.db, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{})
	s := newModuleState(rec.Seams(), owned, f.state, f.dbParent)
	for _, p := range []string{f.db, filepath.Join(f.state, "secrets.keyring")} {
		if err := s.RemoveAll(context.Background(), "bat.x", p); !errors.Is(err, module.ErrStateCoreOwned) {
			t.Fatalf("%s = %v, want ErrStateCoreOwned", p, err)
		}
	}
	if n := len(rec.Events()); n != 0 {
		t.Fatalf("refused calls recorded %d operations", n)
	}
}
