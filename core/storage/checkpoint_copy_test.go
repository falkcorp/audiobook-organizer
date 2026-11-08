// file: core/storage/checkpoint_copy_test.go
// version: 1.0.0
// guid: 6431fe2d-5069-4d2f-ad1a-20c8f5b91baf
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// errKilledBeforeWAL is crashAtWAL's simulated kill.
var errKilledBeforeWAL = errors.New("killed before the new WAL")

// crashAtWAL is a crashable filesystem that dies the first time Pebble
// creates a WAL: it keeps a crash clone of mem, which holds only what was
// synced, and fails the create. Pebble v2.1.7's Open creates its new WAL
// after it has flushed the WAL it replayed, written a new MANIFEST and moved
// the marker to it (open.go), so the clone is a store whose MANIFEST is
// current and whose WALs hold nothing to replay.
type crashAtWAL struct {
	vfs.FS
	mem   *vfs.MemFS
	clone *vfs.MemFS
}

func (c *crashAtWAL) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	if c.clone == nil && strings.HasSuffix(name, ".log") {
		c.clone = c.mem.CrashClone(vfs.CrashCloneCfg{})
		return nil, errKilledBeforeWAL
	}
	return c.FS.Create(name, cat)
}

// syncParents fsyncs dir and every parent of it on mem, so a crash clone
// keeps the directories MkdirAll made.
func syncParents(t *testing.T, mem *vfs.MemFS, dir string) {
	t.Helper()
	for d := dir; ; d = filepath.Dir(d) {
		f, err := mem.OpenDir(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(d) == d {
			return
		}
	}
}

// killedBeforeWAL boots a fresh store at owners on a crashable filesystem,
// then kills the next boot of the same build inside Pebble's Open, after the
// MANIFEST marker moved and before the new WAL exists (F2: R1-21's crash
// matrix kill point "create 000007.log"). It returns an env over the clone,
// whose next Open writes no version edit.
func killedBeforeWAL(t *testing.T, owners []OwnerSpec) *env {
	t.Helper()
	mem := vfs.NewCrashableMem()
	e := newEnvFS(t, mem, owners...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	syncParents(t, mem, filepath.Dir(e.path))
	kill := &crashAtWAL{FS: mem, mem: mem}
	e.opts.FS = FS{fs: kill}
	if _, err := e.tryOpen(); kill.clone == nil || !errors.Is(err, errKilledBeforeWAL) {
		t.Fatalf("the killed boot = %v (crash taken: %v), want the kill inside Pebble's Open", err, kill.clone != nil)
	}
	after := newEnvFS(t, kill.clone, owners...)
	after.path, after.opts.Path = e.path, e.path
	return after
}

// TestCheckpointAfterOpenKilledBeforeWAL (F2; 05-storage §5S.3.12 "How"): on a
// store killed inside Pebble's Open after the MANIFEST marker moved and
// before the new WAL was created, the next Open writes no version edit. Both
// checkpoint call sites (the boot's pre-migration checkpoint and
// Admin.Checkpoint, which backups and the dry run use) still take a copy that
// opens and holds the store's install marker. Before the fix each copy's MANIFEST was
// empty: Pebble copies only what this process's MANIFEST writer wrote.
func TestCheckpointAfterOpenKilledBeforeWAL(t *testing.T) {
	t.Run("upgrade-checkpoint", func(t *testing.T) {
		e := killedBeforeWAL(t, cpOwners())
		e.opts.Owners = fixedOwners()
		db := e.open()
		name := db.CheckpointName()
		if name == "" {
			t.Fatal("the upgrade took no checkpoint")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if got := rawKeys(t, e.fs, filepath.Join(cpDir(e), name, "db"), "c/storage/install"); len(got) != 1 {
			t.Fatalf("the checkpoint copy holds %q, want the store's install marker", got)
		}
	})
	t.Run("admin-checkpoint", func(t *testing.T) {
		e := killedBeforeWAL(t, cpOwners())
		db := e.open()
		if got := db.CheckpointName(); got != "" {
			t.Fatalf("a boot of the same build took checkpoint %q", got)
		}
		dir := filepath.Join(filepath.Dir(e.path), "oracle-copy")
		if err := db.Admin().Checkpoint(context.Background(), dir); err != nil {
			t.Fatalf("Admin.Checkpoint: %v", err)
		}
		if got := rawKeys(t, e.fs, dir, "c/storage/install"); len(got) != 1 {
			t.Fatalf("the Admin checkpoint copy holds %q, want the store's install marker", got)
		}
	})
}

// emptyManifests hands Pebble, for a MANIFEST it creates under dir, a file
// that drops every write: DB.Checkpoint reports success and leaves a copy
// whose MANIFEST is empty, the copy F2 produced.
type emptyManifests struct {
	vfs.FS
	dir string
}

func (f emptyManifests) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	file, err := f.FS.Create(name, cat)
	if err != nil || !strings.HasPrefix(name, f.dir+string(filepath.Separator)) || !strings.HasPrefix(filepath.Base(name), "MANIFEST") {
		return file, err
	}
	return dropWrites{file}, nil
}

type dropWrites struct{ vfs.File }

func (dropWrites) Write(p []byte) (int, error) { return len(p), nil }

// TestUnopenableCheckpointCopyRefused (F2; 05-storage §5S.3.12 "How"): a copy
// that does not open read-only is refused before anything relies on it. The
// boot fails before the rename, the record and the pointer, leaving
// <name>.tmp; the next boot sweeps it and takes a copy that opens, so a
// restart does not loop on the failure. Admin.Checkpoint returns the error
// and removes the copy, as Pebble does when its own Checkpoint fails.
func TestUnopenableCheckpointCopyRefused(t *testing.T) {
	t.Run("upgrade-checkpoint", func(t *testing.T) {
		mem := vfs.NewMem()
		e := headStore(t, mem, cpOwners()...)
		e.opts.FS = FS{fs: emptyManifests{FS: mem, dir: cpDir(e)}}
		if _, err := e.tryOpen(); err == nil || !strings.Contains(err.Error(), "does not open read-only") {
			t.Fatalf("boot with an empty copied MANIFEST = %v, want the read-only check's refusal", err)
		}
		names, err := mem.List(cpDir(e))
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || !strings.HasSuffix(names[0], ".tmp") {
			t.Fatalf("checkpoint directory after the refusal = %v, want one <name>.tmp and no record", names)
		}
		if _, err := mem.Stat(e.path + ".migration-checkpoint"); !oserrorNotExist(err) {
			t.Fatalf("the pointer after the refusal: %v, want none", err)
		}
		e.opts.FS = FS{fs: mem}
		db := e.open()
		name := db.CheckpointName()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if name == "" || name+".tmp" == names[0] {
			t.Fatalf("the next boot's checkpoint = %q, want a new one", name)
		}
		if got := rawKeys(t, mem, filepath.Join(cpDir(e), name, "db"), "joker:"); len(got) != 2 {
			t.Fatalf("the next boot's copy holds %v, want both joker rows", got)
		}
		if _, err := mem.Stat(filepath.Join(cpDir(e), names[0])); !oserrorNotExist(err) {
			t.Fatalf("the refused copy after the next boot: %v, want it swept", err)
		}
	})
	t.Run("admin-checkpoint", func(t *testing.T) {
		mem := vfs.NewMem()
		e := newEnvFS(t, mem, cpOwners()...)
		dir := filepath.Join(filepath.Dir(e.path), "oracle-copy")
		e.opts.FS = FS{fs: emptyManifests{FS: mem, dir: dir}}
		db := e.open()
		if err := db.Admin().Checkpoint(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "does not open read-only") {
			t.Fatalf("Admin.Checkpoint with an empty copied MANIFEST = %v, want the read-only check's refusal", err)
		}
		if _, err := mem.Stat(dir); !oserrorNotExist(err) {
			t.Fatalf("the refused copy: %v, want it removed", err)
		}
	})
}

// TestEmptyFlushRewritesTheManifest pins the Pebble v2.1.7 behaviour
// checkpointInto's forced flush relies on (F2), on a bare Pebble store killed
// where killedBeforeWAL kills one. With no WAL left to replay, Open writes no
// version edit (any replayed WAL, even an empty one, forces a flush that
// writes one: open.go replayWAL), so Pebble has no MANIFEST writer yet and
// DB.Checkpoint copies zero bytes of the MANIFEST (it copies the writer's
// size, checkpoint.go): the copy does not open. A Flush with an empty
// memtable still applies a version edit (flush1 bumps the minimum unflushed
// log number, compaction.go), and a process's first edit writes a new
// MANIFEST with a full snapshot, so a checkpoint after it opens. If an
// upgrade breaks the first half, Pebble's Checkpoint no longer needs the
// flush; if it breaks the second, the flush no longer helps and only the
// read-only check guards the copy.
func TestEmptyFlushRewritesTheManifest(t *testing.T) {
	mem := vfs.NewCrashableMem()
	// A temp-dir name, used only as a name inside the in-memory filesystem.
	arkham := filepath.Join(t.TempDir(), "arkham")
	store, before, after := filepath.Join(arkham, "db"), filepath.Join(arkham, "before"), filepath.Join(arkham, "after")
	open := func(fs vfs.FS, dir string, ro bool) (*cpebble.DB, error) {
		return cpebble.Open(dir, &cpebble.Options{FS: fs, ReadOnly: ro, Logger: quiet{}})
	}
	db, err := open(mem, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(db.Set([]byte("joker"), []byte("ha"), cpebble.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
	syncParents(t, mem, arkham)
	kill := &crashAtWAL{FS: mem, mem: mem}
	if _, err := open(kill, store, false); kill.clone == nil || !errors.Is(err, errKilledBeforeWAL) {
		t.Fatalf("the killed open = %v (crash taken: %v)", err, kill.clone != nil)
	}
	fs := kill.clone
	if db, err = open(fs, store, false); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Checkpoint(before, cpebble.WithFlushedWAL()); err != nil {
		t.Fatal(err)
	}
	if ro, err := open(fs, before, true); err == nil {
		_ = ro.Close()
		t.Fatal("a checkpoint without a version edit opens: Pebble now copies a usable MANIFEST, so re-read checkpointInto's flush")
	}
	if err := db.Flush(); err != nil { // nothing written since the open: the memtable is empty
		t.Fatal(err)
	}
	if err := db.Checkpoint(after, cpebble.WithFlushedWAL()); err != nil {
		t.Fatal(err)
	}
	ro, err := open(fs, after, true)
	if err != nil {
		t.Fatalf("a checkpoint after an empty flush does not open: %v; the flush no longer forces a version edit", err)
	}
	defer func() { _ = ro.Close() }()
	v, closer, err := ro.Get([]byte("joker"))
	if err != nil || string(v) != "ha" {
		t.Fatalf("joker in the copy = %q, %v", v, err)
	}
	_ = closer.Close()
}
