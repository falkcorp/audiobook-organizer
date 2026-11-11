// file: core/storage/dryrun_test.go
// version: 1.0.0
// guid: f49dae4b-5ceb-4d5a-8163-8669e49460a7
// last-edited: 2026-11-10

package storage

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// filesOf lists dir's regular files with their sizes, LOCK aside.
func filesOf(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "LOCK" {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, dir)] = fi.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDryRunCopyModesAndFreeSpace (05-storage §5S.3.6 "Dry run", §5S.3.12
// "Scratch copies"; the dry run's half of TestDryRunScratchModeAndSweep,
// whose sweeper half deletes a scratch entry 7 days after its mtime): on
// the real filesystem the dry run's directory is a new dryrun-<time>-<id>
// entry of the checkpoint area's scratch/, both 0700; the copy of the store
// opened ReadOnly holds its engine files at 0600 in 0700 directories and
// opens as a full store; the live directory's files are unchanged; a
// writable open is refused; and with less free space than the store's size
// plus the margin (twice the size for M8's copy) the copy is refused with
// ErrDryRunFreeSpace before it writes anything.
func TestDryRunCopyModesAndFreeSpace(t *testing.T) {
	ctx := context.Background()
	e := headStore(t, vfs.Default, cpOwners()...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	ro := e.opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	dir, err := o.NewDryRunScratch()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), dryRunPrefix) || filepath.Base(filepath.Dir(dir)) != checkpointScratch {
		t.Fatalf("scratch directory %s is not a dryrun- entry of the scratch area", dir)
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, %v; want 0700", d, fi.Mode().Perm(), err)
		}
	}
	before := filesOf(t, e.path)
	dst := filepath.Join(dir, "db")
	if err := o.CopyForDryRun(ctx, dst, false); err != nil {
		t.Fatal(err)
	}
	if after := filesOf(t, e.path); !maps.Equal(before, after) {
		t.Errorf("the copy changed the live directory:\nbefore %v\nafter  %v", before, after)
	}
	var manifest bool
	err = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
		manifest = manifest || strings.HasPrefix(d.Name(), "MANIFEST-")
		return nil
	})
	if err != nil || !manifest {
		t.Fatalf("copy walk = %v, MANIFEST copied %t", err, manifest)
	}
	copyOpts := e.opts
	copyOpts.Path, copyOpts.CheckpointDir, copyOpts.MustExist = dst, dir, true
	op, err := Open(ctx, copyOpts)
	if err != nil {
		t.Fatalf("the copy does not open as a store: %v", err)
	}
	cdb, err := op.Prepare(ctx)
	if err == nil {
		err = cdb.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(errors.Join(err, op.Close()))
	}
	lr, _ := cdb.Legacy("gotham")
	if v, err := lr.Get(ctx, []byte("joker:1")); err != nil || string(v) != "ha" {
		t.Errorf("the copy's joker:1 = %q, %v", v, err)
	}
	if err := cdb.Close(); err != nil {
		t.Fatal(err)
	}

	// Free space: the store's size plus the margin, twice the size for M8.
	o.db.boot.opts.freeSpace = func(string) (uint64, error) { return o.db.boot.opts.FreeSpaceMargin, nil }
	for _, rewrite := range []bool{false, true} {
		target := filepath.Join(dir, "db-tight")
		if err := o.CopyForDryRun(ctx, target, rewrite); !errors.Is(err, ErrDryRunFreeSpace) {
			t.Errorf("copy (rewrite %t) with only the margin free = %v, want ErrDryRunFreeSpace", rewrite, err)
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused copy wrote %s: %v", target, err)
		}
	}
	// A writable open is not a dry run's source.
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	rw, err := Open(ctx, e.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.CopyForDryRun(ctx, filepath.Join(dir, "db2"), false); err == nil {
		t.Error("CopyForDryRun copied a writable open")
	}
	_ = rw.Close()

}
