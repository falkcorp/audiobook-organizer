// file: core/internal/journalfs/journalfs_test.go
// version: 1.1.0
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
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
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
		got, err := journalfs.From(bootseam.With(t.Context(), &bootseam.Seams{FS: fsys, Shared: &bootseam.Shared{}}))
		if err != nil || got == journal.OSFS() {
			t.Fatalf("From over an in-memory filesystem = %T, %v; want the memory-backed filesystem", got, err)
		}
	}
}

// TestFrom_RefusesAnInMemorySeamWithoutShared: the adapters of a boot share one
// lock set through Seams.Shared, so a seam with an in-memory filesystem and no
// Shared is refused (each From would get a lock set of its own and two opens of
// one journal would not exclude each other), while the real filesystem needs no
// Shared.
func TestFrom_RefusesAnInMemorySeamWithoutShared(t *testing.T) {
	for _, fsys := range []vfs.FS{vfs.NewMem(), errorfs.Wrap(vfs.NewMem(), noInjection{})} {
		if got, err := journalfs.From(bootseam.With(t.Context(), &bootseam.Seams{FS: fsys})); err == nil {
			t.Fatalf("From over an in-memory filesystem with no Shared = %T, nil; want an error", got)
		}
	}
	got, err := journalfs.From(bootseam.With(t.Context(), &bootseam.Seams{FS: vfs.Default}))
	if err != nil || got != journal.OSFS() {
		t.Fatalf("From over the real filesystem with no Shared = %T, %v; want journal.OSFS()", got, err)
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

// TestMemFS_LockIsOneSetPerFilesystem: the lock is a property of the
// filesystem, not of one From call: two journalfs.From values built from two
// different bootseam.Seams over the same seam filesystem exclude each other
// (the kernel's two sites that open the journal in one boot may each call From;
// a lock set per call would let both hold the journal), while a second
// filesystem is its own world with its own locks.
func TestMemFS_LockIsOneSetPerFilesystem(t *testing.T) {
	st := newStore(t)
	e := newEnv(t, st, noOpts())
	// A second adapter over the same filesystem, built from a second Seams value.
	other, err := journalfs.From(bootseam.With(context.Background(), e.rec.Seams()))
	if err != nil {
		t.Fatal(err)
	}
	if other == e.fsys {
		t.Fatal("the test needs two adapter values")
	}
	j, err := e.open(journalAt)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := e.openOn(other, journalAt)
	if !errors.Is(err, journal.ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Fatalf("an open through a second From over the same filesystem: err = %v, want ErrLocked", err)
	}
	// Another filesystem holds the same path independently.
	e2 := newEnv(t, st, noOpts())
	j3, err := e2.open(journalAt)
	if err != nil {
		t.Fatalf("the same path on another filesystem is locked by this one: %v", err)
	}
	if err := errors.Join(j3.Close(), j.Close()); err != nil {
		t.Fatal(err)
	}
	// Closed, the path is free to the second adapter.
	j4, err := e.openOn(other, journalAt)
	if err != nil {
		t.Fatalf("reopen through the second adapter after Close: %v", err)
	}
	if err := j4.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMemFS_TruncateIsDurableAtTheFileSync: a truncation (ftruncate) is not a
// directory operation: it is durable at the file's own fsync and not before,
// and never fsyncs the directory (the old file stays the durable one after a
// minimal crash until the Sync); an O_TRUNC open of an existing file is the
// same. The adapter replaces the file to truncate it, so the replacement's
// directory entry must follow the file's Sync, unrecorded, or a kill cell
// would see a durability no production run has.
func TestMemFS_TruncateIsDurableAtTheFileSync(t *testing.T) {
	const old = "abcdef\n"
	e := newEnv(t, newStore(t), bootseamtest.Options{Clones: true})
	path := stateDir + "/file"
	f, err := e.fsys.Open(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(old)); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), e.fsys.SyncDir(stateDir)); err != nil {
		t.Fatal(err)
	}
	durable := func() string {
		raw, err := readAll(e.rec.Settled().FS, path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if got := durable(); got != old {
		t.Fatalf("after the create, fsync and directory fsync the durable file is %q, want %q", got, old)
	}
	before := len(e.rec.Events())
	if err := f.Truncate(3); err != nil {
		t.Fatal(err)
	}
	if got := durable(); got != old {
		t.Fatalf("after Truncate and before the file's Sync the durable file is %q, want the old %q: a truncation does not fsync the directory", got, old)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := durable(); got != "abc" {
		t.Fatalf("after the file's Sync the durable file is %q, want %q", got, "abc")
	}
	for _, ev := range e.since(before) {
		if ev.Kind == errorfs.OpFileSync && ev.Path == stateDir {
			t.Errorf("the truncation recorded a directory fsync %+v; it makes none", ev)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// An O_TRUNC open: the old file is the durable one until the new file's
	// Sync.
	g, err := e.fsys.Open(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write([]byte("xy")); err != nil {
		t.Fatal(err)
	}
	if got := durable(); got != "abc" {
		t.Fatalf("after an O_TRUNC open and a write, before its Sync, the durable file is %q, want the old %q", got, "abc")
	}
	if err := g.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := durable(); got != "xy" {
		t.Fatalf("after the O_TRUNC file's Sync the durable file is %q, want %q", got, "xy")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMemFS_RollbackOfANewJournalDoesNotMakeItsNameDurable: on a first open a
// header whose write succeeds and whose fsync fails is cut back through a
// truncation. The truncation's Sync must not fsync the directory of a file the
// journal created and has not yet synced the directory for: the name becomes
// durable at the journal's own journal.dirsync, which never ran here, and a
// crash after the failed open must find no journal.
func TestMemFS_RollbackOfANewJournalDoesNotMakeItsNameDurable(t *testing.T) {
	var armed atomic.Bool
	fail := failAt(journal.LabelHeader, errorfs.OpFileSync)
	e := newEnv(t, newStore(t), bootseamtest.Options{Clones: true, Fail: func(ev bootseamtest.Event) error {
		if !armed.Load() {
			return nil
		}
		return fail(ev)
	}})
	armed.Store(true)
	if j, err := e.open(journalAt); err == nil {
		_ = j.Close()
		t.Fatal("the open succeeded over a failed header fsync")
	}
	armed.Store(false)
	if exists(t, e.rec.Settled().FS, journalAt) {
		t.Fatal("a crash after the failed first open finds the journal: the rollback's Sync made a name durable that no directory fsync had")
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
