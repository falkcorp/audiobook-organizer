// file: core/storage/restorejournal/createfs_test.go
// version: 1.0.1
// guid: ff1aae37-5f8d-4461-a1fe-d80e76ea662e
// last-edited: 2026-11-08

package restorejournal

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
)

// durableDir creates dir on a crashable fs with every parent synced, as a
// state directory that exists before any intent is staged.
func durableDir(t *testing.T, fs *vfs.MemFS, dir string) {
	t.Helper()
	if err := mkdirSyncedFS(fs, dir); err != nil {
		t.Fatal(err)
	}
}

// TestCreateIntentFSSurvivesACrash (R1-21 review m2): a crash right after
// CreateIntentFS returns keeps pending.json, because the create of restore/
// is synced into the state directory before the intent is linked into it.
func TestCreateIntentFSSurvivesACrash(t *testing.T) {
	fs := vfs.NewCrashableMem()
	state := "/gotham/state"
	durableDir(t, fs, state)
	if err := CreateIntentFS(fs, state, Intent{ID: "alfred", Kind: KindArchive, DBPath: "/batcave/db/main.pebble"}); err != nil {
		t.Fatal(err)
	}
	clone := fs.CrashClone(vfs.CrashCloneCfg{})
	if _, err := clone.Stat(PendingPath(state)); err != nil {
		t.Fatalf("after a crash pending.json is gone (%v): restore/ was not synced into the state directory", err)
	}
}

// TestCreateIntentFSTakesBackAnUnsyncedLink (R1-21 review m1): once the
// link put pending.json in the slot, the result says what the slot holds. A
// directory fsync that fails after the link is taken back durably and
// reported; when the take-back's own sync fails the intent is put back and
// the result wraps ErrIntentUnsynced. No dot-name is left in either case.
func TestCreateIntentFSTakesBackAnUnsyncedLink(t *testing.T) {
	state := "/gotham/state"
	slot := Dir(state)
	create := func(t *testing.T, failures int) (*vfs.MemFS, error) {
		mem := vfs.NewCrashableMem()
		durableDir(t, mem, slot)
		linked, failed := false, 0
		fs := errorfs.Wrap(mem, errorfs.InjectorFunc(func(op errorfs.Op) error {
			switch {
			case op.Kind == errorfs.OpLink:
				linked = true
			case linked && failed < failures && op.Kind == errorfs.OpFileSync && filepath.Clean(op.Path) == slot:
				failed++
				return syscall.EIO
			}
			return nil
		}))
		return mem, CreateIntentFS(fs, state, Intent{ID: "alfred", Kind: KindArchive, DBPath: "/batcave/db/main.pebble"})
	}
	noDotNames := func(t *testing.T, mem *vfs.MemFS) {
		t.Helper()
		names, err := mem.List(slot)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if strings.HasPrefix(n, ".") {
				t.Fatalf("a temp name was left in restore/: %s", n)
			}
		}
	}
	t.Run("taken-back", func(t *testing.T) {
		mem, err := create(t, 1)
		if !errors.Is(err, syscall.EIO) || errors.Is(err, ErrIntentUnsynced) {
			t.Fatalf("CreateIntentFS = %v, want the fsync's EIO and not ErrIntentUnsynced", err)
		}
		for name, fs := range map[string]vfs.FS{"live": mem, "after a crash": mem.CrashClone(vfs.CrashCloneCfg{})} {
			if _, err := fs.Stat(PendingPath(state)); err == nil {
				t.Fatalf("%s: a failed create left pending.json for P0a to apply", name)
			}
		}
		noDotNames(t, mem)
	})
	t.Run("put-back", func(t *testing.T) {
		mem, err := create(t, 2)
		if !errors.Is(err, ErrIntentUnsynced) {
			t.Fatalf("CreateIntentFS = %v, want ErrIntentUnsynced", err)
		}
		if _, err := mem.Stat(PendingPath(state)); err != nil {
			t.Fatalf("the intent was reported staged and the slot is empty: %v", err)
		}
		noDotNames(t, mem)
	})
}

// TestValidID: an intent id names a directory of its own under restore/.
func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{"r-archive": true, "z-reset.1": true, "": false, ".": false, "..": false, "../joker": false, "a/b": false, "/gotham": false} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}
