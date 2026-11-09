// file: core/storage/restorejournal/delete_test.go
// version: 1.0.1
// guid: 4a7feb16-7fa9-4e12-811f-691eb15b5b55
// last-edited: 2026-11-09

package restorejournal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInTree(t *testing.T) {
	for _, tc := range []struct {
		p, root string
		want    bool
	}{
		{"/gotham/state", "/gotham/state", true},
		{"/gotham/state/restore", "/gotham/state", true},
		{"/gotham/state/", "/gotham/state", true},
		{"/gotham/statefile", "/gotham/state", false},
		{"/gotham", "/gotham/state", false},
		{"/gotham/state/../arkham", "/gotham/state", false},
		{"relative/state", "/gotham/state", false},
	} {
		if got := InTree(tc.p, tc.root); got != tc.want {
			t.Errorf("InTree(%q, %q) = %v, want %v", tc.p, tc.root, got, tc.want)
		}
	}
}

// resetIntent is a factory reset of a store under its own tree, with the
// store, its level sidecar and a boot record set aside.
func resetIntent(state, db string) Intent {
	in := Intent{ID: "z-reset", Kind: KindReset, Reset: ResetFactory, DBPath: db}
	for _, p := range []string{db, db + LevelsSuffix, filepath.Join(state, "config.boot.json"), "/media/arkham/oldb"} {
		in.Targets = append(in.Targets, Target{From: p, To: SetAsideDest(state, db, in.ID, p)})
	}
	return in
}

// TestKeptPaths is rule (4) of the reset hook (05-storage §5S.3.15, R1-21
// review M1): an entry that is or contains <db parent> or the state
// directory, or is, contains or lies inside restore/, a set-aside root or
// destination, or the live store and its sidecars, is kept. Paths inside
// the state directory or <db parent> but outside those trees are not.
func TestKeptPaths(t *testing.T) {
	state, db := "/gotham/state", "/batcave/db/main.pebble"
	in := resetIntent(state, db)
	prev := SetAsideDest(state, db, in.ID, db)
	for _, p := range []string{
		"/", "/batcave", "/batcave/db", "/gotham", state,
		Dir(state), JournalDir(state, in.ID), JournalDir(state, "r-older"), filepath.Join(Dir(state), "pending.json"),
		"/batcave/db/.mediacore-restore", filepath.Dir(prev), prev, filepath.Join(prev, "000001.sst"),
		"/batcave/db/.mediacore-restore/r-older/previous/main.pebble",
		"/media/arkham/.mediacore-restore", "/media/arkham/.mediacore-restore/z-reset/previous/oldb",
		db, filepath.Join(db, "MANIFEST-000001"), db + LevelsSuffix, db + CheckpointSuffix, db + ".storage-format",
		// The swap lock (R1-25 review B1): removing it would let a later
		// offline swap and a boot each take a lock the other never sees.
		filepath.Join(state, "swap.lock"),
	} {
		if !Kept(state, in, []string{".storage-format"}, p) {
			t.Errorf("Kept(%s) = false, want kept", p)
		}
	}
	for _, p := range []string{
		filepath.Join(state, "batcave-index"), "/batcave/db/aux/books/ol", "/media/arkham/oldb", "/media/arkham/openlibrary-dumps",
		"/batcave/db/main.pebble-old", "/batcave/db/.mediacore-checkpoints",
	} {
		if Kept(state, in, []string{".storage-format"}, p) {
			t.Errorf("Kept(%s) = true, want removable", p)
		}
	}
}

// devProbe is a Probe whose devices are by path prefix: an item under one
// of other is on device 2, everything else on device 1.
type devProbe struct{ other []string }

func (p devProbe) dev(path string) (uint64, error) {
	if strings.Contains(path, "absent") {
		return 0, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	}
	for _, o := range p.other {
		if InTree(path, o) {
			return 2, nil
		}
	}
	return 1, nil
}
func (p devProbe) ItemDev(path string) (uint64, error) { return p.dev(path) }
func (p devProbe) DirDev(path string) (uint64, error)  { return p.dev(path) }
func (devProbe) Writable(string) error                 { return nil }

// TestValidateDeleteRefuses: a reset's delete entries are absolute, never
// a kept path, and off the DB's filesystem (05-storage §5S.3.15 "Stage":
// only a Recreatable auxiliary DB outside the DB's filesystem is listed in
// delete, the rest is set aside); an absent entry and a nil probe skip the
// device check; a restore intent deletes nothing.
func TestValidateDeleteRefuses(t *testing.T) {
	state, db := "/gotham/state", "/batcave/db/main.pebble"
	probe := devProbe{other: []string{"/media/arkham"}}
	ok := func(name string, p Probe, in Intent) {
		t.Helper()
		if err := ValidateDelete(p, state, in, nil); err != nil {
			t.Errorf("%s: %v, want accepted", name, err)
		}
	}
	refused := func(name string, p Probe, in Intent) {
		t.Helper()
		if err := ValidateDelete(p, state, in, nil); !errors.Is(err, ErrDeleteRefused) {
			t.Errorf("%s: %v, want ErrDeleteRefused", name, err)
		}
	}
	with := func(entries ...string) Intent {
		in := resetIntent(state, db)
		in.Delete = entries
		return in
	}
	ok("off the DB's filesystem", probe, with("/media/arkham/openlibrary-dumps"))
	ok("absent", probe, with("/batcave/db/absent-index"))
	ok("no probe", nil, with(filepath.Join(state, "batcave-index")))
	ok("none", probe, with())
	refused("relative", probe, with("openlibrary-dumps"))
	refused("relative, no probe", nil, with("openlibrary-dumps"))
	refused("the DB's parent", nil, with("/batcave/db"))
	refused("the state directory's parent", nil, with("/gotham"))
	refused("the set-aside store", nil, with(SetAsideDest(state, db, "z-reset", db)))
	refused("on the DB's filesystem", probe, with(filepath.Join(state, "batcave-index")))
	restore := Intent{ID: "r-archive", Kind: KindArchive, DBPath: db, Delete: []string{"/media/arkham/openlibrary-dumps"}}
	refused("a restore", probe, restore)
}
