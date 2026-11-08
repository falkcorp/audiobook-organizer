// file: core/internal/restoreswap/helpers_test.go
// version: 1.0.2
// guid: 423994b8-e639-4aa4-b358-afa195f91d63
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const (
	testBuild = "batcomputer-r1"
	stateDir  = "/gotham/state"
)

// testNow is the fixtures' clock: every intent expires an hour after it.
func testNow() time.Time { return time.Date(1939, 5, 1, 12, 0, 0, 0, time.UTC) }

// wayne is the build's one storage owner: a keyspace at level 1, raised by
// one Breaking step, so a level-0 store (the live one) and a level-1 store
// (the staged one) both pass the guard and their sidecars differ.
func wayne(level int) storage.OwnerSpec {
	spec := storage.Spec{Keyspace: true}
	if level > 0 {
		spec.Level = level
		spec.Migrations = []storage.Migration{{Version: 1, Name: "raise", Compat: storage.Breaking, RaisesLevel: level,
			Up: func(context.Context, *storage.MigrationTx) error { return nil }}}
	}
	return storage.OwnerSpec{Owner: "wayne", Enabled: true, Spec: spec}
}

// makeStore boots a fresh store at path on fs at the given level and tags
// it, so its directory has its own content hash.
func makeStore(t testing.TB, fs vfs.FS, path string, level int, tag string) {
	t.Helper()
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs})
	op, err := storage.Open(ctx, storage.OpenOptions{Path: path, Build: testBuild, Owners: []storage.OwnerSpec{wayne(level)}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := op.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ks, _ := db.Keyspace("wayne")
	if err := errors.Join(ks.Set(ctx, []byte("tag"), []byte(tag)), db.Close()); err != nil {
		t.Fatal(err)
	}
}

// treeHash is a directory tree's (or a file's) content hash; "" when absent.
func treeHash(t testing.TB, fs vfs.FS, path string) string {
	t.Helper()
	fi, err := fs.Stat(path)
	if err != nil {
		return ""
	}
	h := sha256.New()
	var walk func(p, rel string)
	walk = func(p, rel string) {
		fi, err := fs.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !fi.IsDir() {
			f, err := fs.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(f)
			if err := errors.Join(err, f.Close()); err != nil {
				t.Fatal(err)
			}
			_, _ = h.Write([]byte("f " + rel + "\n"))
			_, _ = h.Write(b)
			return
		}
		names, err := fs.List(p)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(names)
		_, _ = h.Write([]byte("d " + rel + "\n"))
		for _, n := range names {
			if n == "LOCK" {
				continue // Pebble's lock file, created by any open
			}
			walk(filepath.Join(p, n), filepath.Join(rel, n))
		}
	}
	_ = fi
	walk(path, ".")
	return hex.EncodeToString(h.Sum(nil))
}

func writeFile(t testing.TB, fs vfs.FS, path, content string) {
	t.Helper()
	if err := writeFileAtomic(fs, path, []byte(content), privateFile); err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, fs vfs.FS, path string) (string, bool) {
	t.Helper()
	b, ok, err := ipebble.ReadFile(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), ok
}

// copyMem is a full copy of a synced fixture.
func copyMem(fs *vfs.MemFS) *vfs.MemFS {
	return fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 100, RNG: rand.New(rand.NewPCG(1, 2))})
}

// fixture is one starting state of the swap's crash tests.
type fixture struct {
	name     string
	mem      *vfs.MemFS
	in       restorejournal.Intent
	live     string // the live store's tree hash
	staged   string // the staged store's tree hash ("" for a reset)
	location string // backup.location.json's content, "" when absent
}

// newSwapper is a Swapper over fs with the build's owners.
func newSwapper(t testing.TB, fs vfs.FS, build string, clock func() time.Time) *Swapper {
	t.Helper()
	s, err := New(Options{FS: fs, StateDir: stateDir, BuildID: build, Clock: clock, Owners: []storage.OwnerSpec{wayne(1)}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// layout is where a fixture keeps its store: under the state directory
// (the compiled default) or beside it on its own tree.
func dbPathOf(layout string) string {
	if layout == "outside" {
		return "/batcave/db/main.pebble"
	}
	return stateDir + "/storage/main.pebble"
}

// restoreFixture is a live level-0 store tagged "live" and a staged level-1
// store tagged "staged", staged through Stage as kind.
func restoreFixture(t testing.TB, kind restorejournal.Kind, layout string, withLocation bool) fixture {
	t.Helper()
	f := restoreSetup(t, kind, layout, withLocation)
	if err := newSwapper(t, f.mem, testBuild, testNow).Stage(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	return f
}

// restoreSetup is restoreFixture's stores and intent, not yet staged.
func restoreSetup(t testing.TB, kind restorejournal.Kind, layout string, withLocation bool) fixture {
	t.Helper()
	mem := vfs.NewCrashableMem()
	db := dbPathOf(layout)
	id := "r-" + string(kind) + "-" + layout
	makeStore(t, mem, db, 0, "live")
	staging := filepath.Join(restorejournal.StagingDir(db, id), "main.pebble")
	makeStore(t, mem, staging, 1, "staged")
	if err := mem.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := fixture{name: string(kind) + "/" + layout, mem: mem}
	if withLocation {
		f.location = `{"generation":"robin"}`
		writeFile(t, mem, filepath.Join(stateDir, locationFile), f.location)
		f.name += "/location"
	}
	f.live, f.staged = treeHash(t, mem, db), treeHash(t, mem, staging)
	f.in = restorejournal.Intent{ID: id, Kind: kind, DBPath: db, Targets: []restorejournal.Target{{From: staging, To: db}},
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow(), InstallID: "wayne-manor"}
	return f
}

// resetFixture is a live store with its sidecars, a boot record in the state
// directory and (factory) an auxiliary DB to delete, staged as a reset.
func resetFixture(t testing.TB, kind restorejournal.ResetKind, layout string) fixture {
	t.Helper()
	f := resetSetup(t, kind, layout)
	if err := newSwapper(t, f.mem, testBuild, testNow).Stage(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	return f
}

// resetSetup is resetFixture's store and intent, not yet staged.
func resetSetup(t testing.TB, kind restorejournal.ResetKind, layout string) fixture {
	t.Helper()
	mem := vfs.NewCrashableMem()
	db := dbPathOf(layout)
	id := "reset-" + string(kind) + "-" + layout
	makeStore(t, mem, db, 0, "live")
	writeFile(t, mem, filepath.Join(stateDir, "config.boot.json"), `{"owner":"bruce"}`)
	f := fixture{name: "reset-" + string(kind) + "/" + layout, mem: mem, live: treeHash(t, mem, db)}
	var targets []restorejournal.Target
	for _, p := range []string{db, db + restorejournal.LevelsSuffix, db + restorejournal.CheckpointSuffix, filepath.Join(stateDir, "config.boot.json")} {
		targets = append(targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, db, id, p)})
	}
	f.in = restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: kind, DBPath: db, Targets: targets,
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow(), InstallID: "wayne-manor"}
	if kind == restorejournal.ResetFactory {
		aux := "/arkham/aux/ol"
		writeFile(t, mem, filepath.Join(aux, "000001.sst"), "penguin")
		f.in.Delete = []string{aux}
	}
	return f
}

// bootMode is how the fake kernel boot ends a restore boot.
type bootMode int

const (
	bootSucceeds   bootMode = iota // P2..P4b succeed: marker_written, applied
	bootFailsAfter                 // P4b fails on the restored store: step x
)

// fakeBoot is the kernel's part of one boot around the swap: P0a, then the
// restore boot's or reset boot's journal steps. It returns done once a boot
// reached readiness.
func fakeBoot(ctx context.Context, s *Swapper, mode bootMode) (bool, error) {
	out, err := s.RunP0a(ctx)
	if err != nil {
		return false, err
	}
	switch out.Boot {
	case BootRestore:
		if err := s.Opened(ctx, out.ID); err != nil {
			return false, err
		}
		if mode == bootFailsAfter {
			// P4b's checkpoint pointer, which the original store never had,
			// then a failure: step x must not leave it beside the original.
			if err := writeFileAtomic(s.fs, out.Intent.DBPath+restorejournal.CheckpointSuffix, []byte("pointer"), privateFile); err != nil {
				return false, err
			}
			return false, s.SwapBack(ctx, out.ID, errors.New("P4b failed on the restored store"))
		}
		return true, s.RestoreApplied(ctx, out.ID)
	case BootReset:
		// P4a's fresh install on the empty path.
		if err := mkdirAllSynced(s.fs, out.Intent.DBPath); err != nil {
			return false, err
		}
		if err := writeFileAtomic(s.fs, filepath.Join(out.Intent.DBPath, "fresh"), []byte("new install"), privateFile); err != nil {
			return false, err
		}
		if err := s.ResetApplied(ctx, out.ID); err != nil {
			return false, err
		}
		return true, s.ResetDone(ctx, out.ID)
	case BootResetApplied:
		return true, s.ResetDone(ctx, out.ID)
	}
	return true, nil
}

// bootUntilReady runs fakeBoot up to three times, the automatic restarts.
func bootUntilReady(ctx context.Context, s func() *Swapper, mode bootMode) (int, error) {
	var err error
	for n := 1; n <= 3; n++ {
		var done bool
		if done, err = fakeBoot(ctx, s(), mode); done && err == nil {
			return n, nil
		}
		if mode == bootFailsAfter && errors.Is(err, ErrSwappedBack) {
			mode = bootSucceeds // the restart boots the original store
		}
	}
	return 0, err
}

// terminal reports machine 1's terminal steps (and failed, for a
// KindCheckpoint journal).
func terminal(step string) bool {
	switch step {
	case StepApplied, StepSwappedBack, StepRejected, StepHookDone:
		return true
	}
	return false
}
