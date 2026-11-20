// file: core/internal/journalfs/journalfs_test.go
// version: 1.0.0
// guid: 225f8d7f-0365-4ada-9c56-d1ab93d0a642
// last-edited: 2026-11-19

package journalfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/journalfs"
)

// countingLabeler counts the regions opened through it.
type countingLabeler struct{ n atomic.Int32 }

func (c *countingLabeler) Enter(string) func() { c.n.Add(1); return func() {} }

// noInjection is an errorfs injector that never fails.
type noInjection struct{}

func (noInjection) MaybeError(errorfs.Op) error { return nil }
func (noInjection) String() string              { return "noInjection" }

// TestFrom_ProductionIsOS: with no seam, a seam with no filesystem or one with
// the default filesystem, the journal's filesystem is journal.OSFS(), the
// same value journal's entry points take when given none: no recorder, no
// region and no in-memory filesystem anywhere on the path. A write through it
// lands on the real disk with the mode the journal asks for, and a labeller
// in the seam is never entered, because nothing on the production path opens a
// region the labeller sees.
func TestFrom_ProductionIsOS(t *testing.T) {
	labeller := &countingLabeler{}
	for name, ctx := range map[string]context.Context{
		"no seams":    t.Context(),
		"seams no FS": bootseam.With(t.Context(), &bootseam.Seams{BuildID: "batcomputer"}),
		"default FS":  bootseam.With(t.Context(), &bootseam.Seams{FS: vfs.Default}),
		"labels only": bootseam.With(t.Context(), &bootseam.Seams{Labels: labeller}),
	} {
		t.Run(name, func(t *testing.T) {
			fsys, err := journalfs.From(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if fsys != journal.OSFS() {
				t.Fatalf("From = %T, want journal.OSFS()", fsys)
			}
			dir := t.TempDir()
			l := location("robin")
			if err := journal.WriteLocation(dir, l, journal.WithFS(fsys)); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(filepath.Join(dir, journal.LocationFile))
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("backup.location.json is %v, %v; want a real 0600 file", fi, err)
			}
			if _, err := os.Stat(filepath.Join(dir, journal.LocationFile+".tmp")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the temporary file survived: %v", err)
			}
			got, ok, err := journal.ReadLocation(dir)
			if err != nil || !ok || got != l {
				t.Fatalf("ReadLocation over the default = %+v, %v, %v; want %+v", got, ok, err, l)
			}
		})
	}
	if n := labeller.n.Load(); n != 0 {
		t.Fatalf("the production filesystem entered %d regions of the seam's labeller", n)
	}
}

// TestFrom_RefusesAForeignFilesystem: a seam filesystem that is neither the
// real one nor in memory would make the journal's writes go past it,
// unrecorded, so the boot is refused.
func TestFrom_RefusesAForeignFilesystem(t *testing.T) {
	foreign := errorfs.Wrap(vfs.Default, noInjection{})
	if _, err := journalfs.From(bootseam.With(t.Context(), &bootseam.Seams{FS: foreign})); err == nil {
		t.Fatal("From accepted a filesystem that is not the real one or in memory")
	}
	for _, fsys := range []vfs.FS{vfs.NewMem(), errorfs.Wrap(vfs.NewMem(), noInjection{})} {
		got, err := journalfs.From(bootseam.With(t.Context(), &bootseam.Seams{FS: fsys}))
		if err != nil || got == journal.OSFS() {
			t.Fatalf("From over an in-memory filesystem = %T, %v; want the memory-backed filesystem", got, err)
		}
	}
}

// TestMemFS_LockIsExclusiveUntilClose: the journal's flock is modelled per
// path: a second open of a locked journal is ErrLocked, and the path is free
// again once the first File closes, as flock(2) does between open file
// descriptions.
func TestMemFS_LockIsExclusiveUntilClose(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	j, err := e.open(journalAt)
	if err != nil {
		t.Fatal(err)
	}
	if j2, err := e.open(journalAt); !errors.Is(err, journal.ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Fatalf("a second open of a locked journal: err = %v, want ErrLocked", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j3, err := e.open(journalAt)
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	if err := j3.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMemFS_AppendIsAtTheEnd: the in-memory file behaves as the journal needs
// of an O_APPEND file: lines accumulate at the end, across reopens.
func TestMemFS_AppendIsAtTheEnd(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	j := e.mustOpen(journalAt)
	for range 2 {
		if err := e.appendUncommitted(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
	if err != nil || c.Break != nil || c.IntactEnd() != 2 {
		t.Fatalf("after two appends the chain = %+v, %v; want intact to seq 2", c, err)
	}
	j = e.mustOpen(journalAt)
	if err := e.appendUncommitted(j); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
	if err != nil || c.Break != nil || c.IntactEnd() != 3 {
		t.Fatalf("after a reopen and a third append the chain = %+v, %v; want intact to seq 3", c, err)
	}
}

// TestMemFS_FileOperationsReportAsTheOSDoes: the in-memory filesystem answers
// the two errors the journal branches on the way os does: an exclusive create
// over an existing name is ErrExist, and removing a name that is not there is
// ErrNotExist, never a silent success. It holds through the crash kit's
// recorder too, whose errorfs wrapper reports a remove of a missing name as nil
// before the adapter is asked.
func TestMemFS_FileOperationsReportAsTheOSDoes(t *testing.T) {
	fsys := newEnv(t, newStore(t), noOpts()).fsys
	path := backupDir + "/riddler.txt"

	if err := fsys.Remove(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Remove of a missing file: err = %v, want ErrNotExist", err)
	}
	f, err := fsys.Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("exclusive create of a new file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if g, err := fsys.Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, os.ErrExist) {
		if g != nil {
			_ = g.Close()
		}
		t.Errorf("exclusive create over an existing file: err = %v, want ErrExist", err)
	}
	if err := fsys.Remove(path); err != nil {
		t.Errorf("Remove of an existing file: %v", err)
	}
}
