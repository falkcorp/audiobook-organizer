// file: core/coretest/crashmatrix/fixture.go
// version: 1.0.3
// guid: 127402ea-7c81-40e4-a90a-159f4d64dcdc
// last-edited: 2026-11-12

package crashmatrix

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Fixture is one starting state: a store directory and a state directory
// on the real filesystem, copied into each run's in-memory filesystem under
// the same absolute paths, and the scenario name it stands for.
type Fixture struct {
	Name     string // "fresh", "upgrade-pending-step", "level-step", ...
	DBDir    string // core.storage.path, absolute; "" = <StateDir>/storage/main.pebble
	StateDir string // core.paths.state_dir, absolute; it must exist
	// Refusal, when set, is a legal end besides readiness: a boot of the
	// current build that refuses with an error that errors.Is it ends the
	// chain (a checkpoint restore whose migrations failed refuses the binary
	// that failed them until a fixed build is installed, DATA7-12), and I5
	// accepts it.
	Refusal error
	// Mutate, when set, changes the loaded fixture before every run, in
	// memory only (the real fixture is never written; Boot ignores it): a
	// starting state staging cannot make, such as an intent whose staged
	// record a kill lost, or with KillAt a boot killed at a kill point.
	// What it returns is synced in full and runs as the fixture; it must
	// return a filesystem no other run shares.
	Mutate func(*vfs.MemFS) (*vfs.MemFS, error)
}

// dbPath is the store's path.
func (f Fixture) dbPath() string {
	if f.DBDir != "" {
		return f.DBDir
	}
	return filepath.Join(f.StateDir, "storage", "main.pebble")
}

// roots are the real directories the fixture names: the state directory and
// the store's parent (the checkpoints, the sidecars, a staged copy and the
// set-aside live there).
func (f Fixture) roots() []string {
	return uniq(f.StateDir, filepath.Dir(f.dbPath()))
}

// uniq drops every path under another one of paths.
func uniq(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if slices.ContainsFunc(out, func(q string) bool { return restorejournal.InTree(p, q) }) {
			continue
		}
		out = slices.DeleteFunc(out, func(q string) bool { return restorejournal.InTree(q, p) })
		out = append(out, p)
	}
	return out
}

// diskUsage is what every in-memory filesystem of the kit reports: plenty,
// so the checkpoint's headroom check passes (a MemFS reports none by
// default, and a crash clone does not keep the setting).
func diskUsage() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

// load copies the fixture's directories into a fresh crashable MemFS under
// the same absolute paths, every file and directory synced, so a crash
// clone keeps all of it.
func load(f Fixture) (*vfs.MemFS, error) {
	if !filepath.IsAbs(f.StateDir) || (f.DBDir != "" && !filepath.IsAbs(f.DBDir)) {
		return nil, fmt.Errorf("crashmatrix: fixture %s: StateDir and DBDir must be absolute", f.Name)
	}
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(diskUsage())
	for _, dir := range f.roots() {
		if err := mem.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := mem.RemoveAll(dir); err != nil {
			return nil, err
		}
		if _, err := vfs.Clone(vfs.Default, mem, dir, dir); err != nil {
			return nil, fmt.Errorf("crashmatrix: fixture %s: copy %s: %w", f.Name, dir, err)
		}
	}
	if err := syncTree(mem, string(filepath.Separator)); err != nil {
		return nil, err
	}
	if f.Mutate == nil {
		return mem, nil
	}
	mem, err := f.Mutate(mem)
	if err != nil {
		return nil, fmt.Errorf("crashmatrix: fixture %s: Mutate: %w", f.Name, err)
	}
	mem.TestingSetDiskUsage(diskUsage())
	if err := syncTree(mem, string(filepath.Separator)); err != nil {
		return nil, err
	}
	return mem, nil
}

// KillAt is a Fixture.Mutate for f: the state a boot of build over f leaves
// when it is killed before the nth (from 0) kill point that carries label,
// such as a crash loop's open checkpoint for the next boot to reuse, or an
// upgrade attempt stranded before its completion (R1-21 review M4). That
// boot runs once, here, and fails tb when it reaches fewer such kill
// points; every run then starts from its own copy of the crash clone.
func KillAt(tb testing.TB, f Fixture, build func(testing.TB, *Injector) error, label string, nth int) func(*vfs.MemFS) (*vfs.MemFS, error) {
	tb.Helper()
	f.Mutate = nil
	mem, err := load(f)
	if err != nil {
		tb.Fatal(err)
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{Clones: true, Stores: []string{f.dbPath()}})
	inj := newInjector(f, rec, nil, nil)
	berr := build(tb, inj)
	ferr := inj.finish(context.Background())
	var killed *vfs.MemFS
	n := 0
	for _, c := range rec.Clones() {
		if c.Event.Label != label {
			continue
		}
		if n == nth {
			killed = c.FS
			break
		}
		n++
	}
	if killed == nil {
		tb.Fatalf("crashmatrix: KillAt %s: the boot reached %d kill points labelled %s, not %d (boot: %v)", f.Name, n, label, nth+1, errors.Join(berr, ferr))
	}
	return func(*vfs.MemFS) (*vfs.MemFS, error) { return killed.CrashClone(vfs.CrashCloneCfg{}), nil }
}

// syncTree syncs every file and directory under root, root included.
func syncTree(fsys vfs.FS, root string) error {
	names, err := fsys.List(root)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := fsys.PathJoin(root, n)
		fi, err := fsys.Stat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if err := syncTree(fsys, p); err != nil {
				return err
			}
			continue
		}
		if err := syncPath(fsys, p, false); err != nil {
			return err
		}
	}
	return syncPath(fsys, root, true)
}

func syncPath(fsys vfs.FS, p string, dir bool) error {
	var f vfs.File
	var err error
	if dir {
		f, err = fsys.OpenDir(p)
	} else {
		f, err = fsys.OpenReadWrite(p, vfs.WriteCategoryUnspecified)
	}
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// tree is a snapshot of real directories: every path with its content
// hash ("dir" for a directory).
type tree map[string]string

// snapshot reads the fixture's real directories.
func snapshot(f Fixture) (tree, error) {
	out := tree{}
	for _, root := range f.roots() {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist) && p == root:
				return filepath.SkipDir
			case err != nil:
				return err
			case d.IsDir():
				out[p] = "dir"
				return nil
			}
			h := sha256.New()
			r, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, r)
			if err := errors.Join(err, r.Close()); err != nil {
				return err
			}
			out[p] = fmt.Sprintf("%x", h.Sum(nil))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("crashmatrix: fixture %s: snapshot %s: %w", f.Name, root, err)
		}
	}
	return out, nil
}

// diff lists the paths whose state differs between two snapshots.
func (t tree) diff(after tree) []string {
	var out []string
	for p, h := range after {
		if t[p] != h {
			out = append(out, p)
		}
	}
	for p := range t {
		if _, ok := after[p]; !ok {
			out = append(out, p+" (removed)")
		}
	}
	return out
}

// Boot runs build once over f's directories, with the kit's binary ID and
// clock and no fault injection, to readiness: how a test writes a fixture's
// starting store (the current build's, or a variant's). It boots an
// in-memory copy and writes it back over the real directories, even when
// the boot fails (a failed upgrade's open checkpoint is a starting state
// too), so the store records the in-memory filesystem's directory identity,
// the one every run's boot sees: a store written on the real disk would
// look relocated to them (05-storage §5S.3.7 "Store generation").
func Boot(t testing.TB, build func(testing.TB, *Injector) error, f Fixture) error {
	t.Helper()
	f.Mutate = nil // Boot writes the real fixture; a mutation never reaches it
	mem, err := load(f)
	if err != nil {
		return err
	}
	inj := newInjector(f, nil, mem, nil)
	berr := build(t, inj)
	ferr := inj.finish(context.Background())
	if err := save(mem, f); err != nil {
		return err
	}
	if berr != nil {
		return berr
	}
	return ferr
}

// save replaces f's real directories with their copies in mem.
func save(mem *vfs.MemFS, f Fixture) error {
	for _, root := range f.roots() {
		if err := os.RemoveAll(root); err != nil {
			return err
		}
		if _, err := mem.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := vfs.Clone(mem, vfs.Default, root, root); err != nil {
			return fmt.Errorf("crashmatrix: fixture %s: write back %s: %w", f.Name, root, err)
		}
	}
	return nil
}

// Stage stages in into f's swap slot as the kernel's stager does, under the
// kit's binary ID: StagedByBuild defaults to BuildID, RequestedAt to Now,
// ExpiresAt to an hour after it and RequestedBy to "crashmatrix". The
// staged store and the swap's targets are the caller's. The rename
// pre-check and the delete entries' device check probe the real
// filesystem (restorejournal.OSProbe).
func Stage(f Fixture, in restorejournal.Intent) error {
	return StageWith(f, in, restorejournal.OSProbe())
}

// StageWith is Stage with the pre-check's probe replaced. A test host has one
// filesystem, so a fixture whose intent needs a second one (a reset's
// delete entry, which 05-storage §5S.3.15 lists only off the DB's
// filesystem) passes a probe that reports that entry on another device. The
// entry must still lie under one of the fixture's directories, or no run's
// in-memory copy holds it.
func StageWith(f Fixture, in restorejournal.Intent, probe restorejournal.Probe) error {
	if in.StagedByBuild == "" {
		in.StagedByBuild = BuildID
	}
	if in.RequestedAt.IsZero() {
		in.RequestedAt = Now()
	}
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = Now().Add(time.Hour)
	}
	if in.RequestedBy == "" {
		in.RequestedBy = "crashmatrix"
	}
	if in.DBPath == "" {
		in.DBPath = f.dbPath()
	}
	s, err := restoreswap.New(restoreswap.Options{StateDir: f.StateDir, BuildID: BuildID, Clock: Now, Probe: probe})
	if err != nil {
		return err
	}
	return s.Stage(context.Background(), in)
}
