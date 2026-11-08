// file: core/coretest/crashmatrix/fixture.go
// version: 1.0.0
// guid: 127402ea-7c81-40e4-a90a-159f4d64dcdc
// last-edited: 2026-11-08

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
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

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
	// WantRefused, when set, is the fixture's legal end: every boot of the
	// current build refuses with an error that errors.Is it (a checkpoint
	// restore whose migrations failed refuses the binary that failed them,
	// DATA7-12), and I5 asserts that refusal instead of readiness.
	WantRefused error
	// Check, when set, asserts the fixture's own invariants on the
	// filesystem after recovery (which store the DB path holds, which rows a
	// checkpoint holds); fs is read-only to it.
	Check func(fs vfs.FS) error
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
		if slices.ContainsFunc(out, func(q string) bool { return within(q, p) }) {
			continue
		}
		out = slices.DeleteFunc(out, func(q string) bool { return within(p, q) })
		out = append(out, p)
	}
	return out
}

// within reports whether p is dir or under it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// diskUsage is what every in-memory filesystem of the kit reports: plenty,
// so the checkpoint's headroom check passes (a MemFS reports none by
// default, and a crash clone does not keep the setting).
var diskUsage = vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}

// load copies the fixture's directories into a fresh crashable MemFS under
// the same absolute paths, every file and directory synced, so a crash
// clone keeps all of it.
func load(f Fixture) (*vfs.MemFS, error) {
	if !filepath.IsAbs(f.StateDir) || (f.DBDir != "" && !filepath.IsAbs(f.DBDir)) {
		return nil, fmt.Errorf("crashmatrix: fixture %s: StateDir and DBDir must be absolute", f.Name)
	}
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(diskUsage)
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
	return mem, nil
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

// Boot runs build once over f's real directories, with the kit's binary ID
// and clock and no fault injection, to readiness: how a test writes a
// fixture's starting store (the current build's, or a variant's).
func Boot(t testing.TB, build func(testing.TB, *Injector) error, f Fixture) error {
	t.Helper()
	inj := newInjector(f, nil)
	if err := build(t, inj); err != nil {
		return err
	}
	return inj.finish(context.Background())
}

// Stage stages in into f's swap slot as the kernel's stager does, under the
// kit's binary ID: StagedByBuild defaults to BuildID, RequestedAt to Now,
// ExpiresAt to an hour after it and RequestedBy to "crashmatrix". The
// staged store and the swap's targets are the caller's.
func Stage(f Fixture, in restorejournal.Intent) error {
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
	s, err := restoreswap.New(restoreswap.Options{StateDir: f.StateDir, BuildID: BuildID, Clock: Now,
		Probe: restorejournal.OSProbe()})
	if err != nil {
		return err
	}
	return s.Stage(context.Background(), in)
}
