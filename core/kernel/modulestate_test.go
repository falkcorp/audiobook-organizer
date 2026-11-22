// file: core/kernel/modulestate_test.go
// version: 0.1.0
// guid: 7e41c0d8-92b5-4a36-b8f7-0d5a3c61e92b
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// stateFixture is a state directory and the directory beside the store, each
// holding what a legacy cleanup removes, and a directory outside both.
type stateFixture struct {
	state, dbParent, outside string
}

func newStateFixture(t *testing.T) stateFixture {
	t.Helper()
	f := stateFixture{state: t.TempDir(), dbParent: t.TempDir(), outside: t.TempDir()}
	for _, p := range []string{
		filepath.Join(f.state, ".batcave_key"),
		filepath.Join(f.dbParent, "batcave.idx", "store", "root.bolt"),
		filepath.Join(f.dbParent, "batcave.idx", "index_meta.json"),
		filepath.Join(f.outside, "joker"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("gotham"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func gone(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	return errors.Is(err, fs.ErrNotExist)
}

// TestModuleStateFS_RemovesInsideItsRoots: over the real disk the handle
// removes a file of the state directory and a directory tree beside the
// store, each by its absolute path, and an absent path is done: success for
// RemoveAll, fs.ErrNotExist for Remove.
func TestModuleStateFS_RemovesInsideItsRoots(t *testing.T) {
	f := newStateFixture(t)
	s := newModuleState(nil, f.state, f.dbParent)
	ctx := context.Background()
	if err := s.Remove(ctx, "bat.key", filepath.Join(f.state, ".batcave_key")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAll(ctx, "bat.index", filepath.Join(f.dbParent, "batcave.idx")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(f.state, ".batcave_key"), filepath.Join(f.dbParent, "batcave.idx")} {
		if !gone(t, p) {
			t.Errorf("%s survived", p)
		}
	}
	if err := s.RemoveAll(ctx, "bat.index", filepath.Join(f.dbParent, "batcave.idx")); err != nil {
		t.Fatalf("RemoveAll of an absent tree = %v, want nil", err)
	}
	if err := s.Remove(ctx, "bat.key", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove of an absent file = %v, want fs.ErrNotExist", err)
	}
	if err := s.RemoveAll(ctx, "bat.index", filepath.Join(f.state, "no-such-dir", "x")); err != nil {
		t.Fatalf("RemoveAll under an absent directory = %v, want nil", err)
	}
	if err := s.RemoveAll(ctx, "bat.index", filepath.Join(t.TempDir(), "gone", "x")); !errors.Is(err, module.ErrStateConfined) {
		t.Fatalf("a path outside the roots = %v, want ErrStateConfined", err)
	}
	if !errors.Is(s.RemoveAll(ctx, "bat.index", filepath.Join(f.state, "..", "x")), module.ErrStateConfined) {
		t.Error("a path that leaves a root through .. is not confined")
	}
}

// TestModuleStateFS_Confinement: a relative path, a path outside the state
// directory and the store's directory, a path that leaves a root through
// "..", a root itself, a symbolic link out of a root, and a handle with no
// root are refused, and nothing outside is removed. An empty label is
// refused before any of that; a cancelled context removes nothing.
func TestModuleStateFS_Confinement(t *testing.T) {
	f := newStateFixture(t)
	if err := os.Symlink(f.outside, filepath.Join(f.state, "way-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outside, "joker"), filepath.Join(f.state, "link")); err != nil {
		t.Fatal(err)
	}
	s := newModuleState(nil, f.state, f.dbParent, "relative/dir", "")
	ctx := context.Background()
	for name, p := range map[string]string{
		"relative":        ".batcave_key",
		"outside":         filepath.Join(f.outside, "joker"),
		"dotdot":          filepath.Join(f.state, "..", filepath.Base(f.outside), "joker"),
		"state root":      f.state,
		"db parent root":  f.dbParent,
		"through a link":  filepath.Join(f.state, "way-out", "joker"),
		"filesystem root": string(filepath.Separator),
	} {
		for _, all := range []bool{false, true} {
			var err error
			if all {
				err = s.RemoveAll(ctx, "bat.x", p)
			} else {
				err = s.Remove(ctx, "bat.x", p)
			}
			if err == nil {
				t.Errorf("%s (all=%v): %s was removed", name, all, p)
			}
			if name != "through a link" && !errors.Is(err, module.ErrStateConfined) {
				t.Errorf("%s (all=%v): %v, want ErrStateConfined", name, all, err)
			}
		}
	}
	if gone(t, filepath.Join(f.outside, "joker")) || gone(t, f.state) || gone(t, f.dbParent) {
		t.Fatal("a refused call removed something outside the confinement")
	}
	// A link as the last element is removed as a link; its target stays.
	if err := s.Remove(ctx, "bat.x", filepath.Join(f.state, "link")); err != nil {
		t.Fatal(err)
	}
	if !gone(t, filepath.Join(f.state, "link")) || gone(t, filepath.Join(f.outside, "joker")) {
		t.Fatal("a symbolic link was followed")
	}
	// No roots: every path is refused.
	if err := newModuleState(nil).RemoveAll(ctx, "bat.x", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, module.ErrStateConfined) {
		t.Fatalf("a handle with no root = %v, want ErrStateConfined", err)
	}
	if err := s.RemoveAll(ctx, "", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, module.ErrStateUnlabelled) {
		t.Fatalf("an empty label = %v, want ErrStateUnlabelled", err)
	}
	if err := s.Remove(ctx, "", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, module.ErrStateUnlabelled) {
		t.Fatalf("an empty label = %v, want ErrStateUnlabelled", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.RemoveAll(cctx, "bat.x", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context = %v", err)
	}
	if gone(t, filepath.Join(f.state, ".batcave_key")) {
		t.Fatal("a cancelled call removed the file")
	}
}

// TestModuleStateFS_NestedRootsUseTheInnermost: with <db parent> inside the
// state directory (the default layout: <state>/storage), a path under both is
// served by the inner root, and a path under only the outer one by the outer.
func TestModuleStateFS_NestedRootsUseTheInnermost(t *testing.T) {
	state := t.TempDir()
	inner := filepath.Join(state, "storage")
	for _, p := range []string{filepath.Join(inner, "batcave.idx", "a"), filepath.Join(state, ".batcave_key")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := newModuleState(nil, state, inner)
	root, rel, err := s.resolve("l", filepath.Join(inner, "batcave.idx"))
	if err != nil || root != inner || rel != "batcave.idx" {
		t.Fatalf("resolve = %q, %q, %v; want the inner root", root, rel, err)
	}
	root, rel, err = s.resolve("l", filepath.Join(state, ".batcave_key"))
	if err != nil || root != state || rel != ".batcave_key" {
		t.Fatalf("resolve = %q, %q, %v; want the outer root", root, rel, err)
	}
	if err := s.RemoveAll(context.Background(), "l", filepath.Join(inner, "batcave.idx")); err != nil {
		t.Fatal(err)
	}
	if !gone(t, filepath.Join(inner, "batcave.idx")) {
		t.Fatal("the tree survived")
	}
}

// TestModuleStateFS_LabelsEveryOperation: under a boot's in-memory seam the
// handle writes to that filesystem and not the real disk, every durable
// operation of a call (each entry's removal and the parent directory's
// fsync) carries the label the caller gave, and a call that finds nothing
// records none: a finished cleanup writes nothing at the next boot.
func TestModuleStateFS_LabelsEveryOperation(t *testing.T) {
	f := newStateFixture(t) // real files that must stay
	mem := vfs.NewCrashableMem()
	for _, p := range []string{filepath.Join(f.dbParent, "batcave.idx", "store"), f.state} {
		if err := mem.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(f.dbParent, "batcave.idx", "store", "root.bolt"), filepath.Join(f.dbParent, "batcave.idx", "index_meta.json"), filepath.Join(f.state, ".batcave_key")} {
		w, err := mem.Create(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{})
	s := newModuleState(rec.Seams(), f.state, f.dbParent)
	ctx := context.Background() // carries no seams: the handle holds the boot's
	if err := s.RemoveAll(ctx, "cave.legacy.index", filepath.Join(f.dbParent, "batcave.idx")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "cave.legacy.key", filepath.Join(f.state, ".batcave_key")); err != nil {
		t.Fatal(err)
	}
	var removes, syncs int
	for _, ev := range rec.Events() {
		want := "cave.legacy.index"
		if filepath.Base(ev.Path) == ".batcave_key" || ev.Path == f.state {
			want = "cave.legacy.key"
		}
		if ev.Label != want {
			t.Errorf("%v %s carries label %q, want %q", ev.Kind, ev.Path, ev.Label, want)
		}
		switch ev.Kind {
		case errorfs.OpRemove:
			removes++
		case errorfs.OpFileSync:
			syncs++
		}
	}
	if removes != 5 {
		t.Errorf("%d removals recorded, want 5 (root.bolt, store, index_meta.json, batcave.idx, .batcave_key)", removes)
	}
	if syncs != 2 {
		t.Errorf("%d syncs recorded, want 2 (the parent of each removal)", syncs)
	}
	before := len(rec.Events())
	if err := s.RemoveAll(ctx, "cave.legacy.index", filepath.Join(f.dbParent, "batcave.idx")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "cave.legacy.key", filepath.Join(f.state, ".batcave_key")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove of the absent key = %v", err)
	}
	if n := len(rec.Events()) - before; n != 0 {
		t.Fatalf("calls over what is already gone recorded %d operations", n)
	}
	for _, p := range []string{filepath.Join(f.state, ".batcave_key"), filepath.Join(f.dbParent, "batcave.idx", "index_meta.json")} {
		if gone(t, p) {
			t.Errorf("the handle removed the real file %s past the boot filesystem", p)
		}
	}
}

// TestKeyringExported_FailsClosed: the query is true only when every key of
// the open keyring is in an export. A fresh keyring, a keyring a rotation
// added a key to, a closed store and no store at all are not exported; and
// while core.secrets reports secrets_undecryptable, which hides
// secrets_keyring_not_exported behind it in the one Reason a Result carries,
// the query still says not exported.
func TestKeyringExported_FailsClosed(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, secretsOwnerSpec())
	state := t.TempDir()
	s := openTestSecrets(t, db, state, nil)
	q := keyringExportedQuery(s.store)

	if q() {
		t.Fatal("a fresh keyring reads as exported")
	}
	if r := secretsHealth(ctx, s.store); r.Reason != reasonKeyringNotExported {
		t.Fatalf("fresh keyring: health reads %+v, want %s", r, reasonKeyringNotExported)
	}

	// An unreadable marker raises secrets_undecryptable, the most urgent token,
	// which is the Reason the Result shows; the keyring is still unexported.
	ks, _ := db.Keyspace(secretsOwnerID)
	b := ks.NewBatch()
	if err := s.store.StageUnreadable(ctx, b, "core.http.basic_gate.password", "key", "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if r := secretsHealth(ctx, s.store); r.Reason != reasonSecretsUndecryptable || r.Status != health.StatusDegraded {
		t.Fatalf("with an unreadable marker health reads %+v, want %s", r, reasonSecretsUndecryptable)
	}
	if q() {
		t.Fatal("secrets_undecryptable hid the unexported keyring: the query read the health Result, not the export record")
	}

	exportFrom(t, state)
	if !q() {
		t.Fatal("an export covering every key does not read as exported")
	}
	if keyringExportedQuery(nil)() {
		t.Fatal("no secret store reads as exported")
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if q() {
		t.Fatal("a closed store reads as exported")
	}
}

// memCave is an in-memory state directory holding a key and a directory
// tree beside the store, as the legacy cleanup finds them.
func memCave(t *testing.T, f stateFixture) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewCrashableMem()
	for _, p := range []string{filepath.Join(f.dbParent, "batcave.idx", "store"), f.state} {
		if err := mem.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(f.dbParent, "batcave.idx", "store", "root.bolt"), filepath.Join(f.dbParent, "batcave.idx", "index_meta.json"), filepath.Join(f.state, ".batcave_key")} {
		w, err := mem.Create(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncMemTree(mem, "/"); err != nil {
		t.Fatal(err)
	}
	return mem
}

// syncMemTree fsyncs every entry under root, so a crash clone keeps them.
func syncMemTree(mem vfs.FS, root string) error {
	names, err := mem.List(root)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := filepath.Join(root, n)
		fi, err := mem.Stat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if err := syncMemTree(mem, p); err != nil {
				return err
			}
			d, err := mem.OpenDir(p)
			if err != nil {
				return err
			}
			if err := errors.Join(d.Sync(), d.Close()); err != nil {
				return err
			}
			continue
		}
		w, err := mem.OpenReadWrite(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			return err
		}
		if err := errors.Join(w.Sync(), w.Close()); err != nil {
			return err
		}
	}
	return nil
}

// cleanup is the two removals of a legacy cleanup through s, the index tree
// first and then the key, tolerating a key already gone.
func cleanup(ctx context.Context, s module.StateFS, f stateFixture) error {
	err := s.RemoveAll(ctx, "cave.legacy.index", filepath.Join(f.dbParent, "batcave.idx"))
	if kerr := s.Remove(ctx, "cave.legacy.key", filepath.Join(f.state, ".batcave_key")); kerr != nil && !errors.Is(kerr, fs.ErrNotExist) {
		err = errors.Join(err, kerr)
	}
	return err
}

func memHas(mem vfs.FS, p string) bool {
	_, err := mem.Stat(p)
	return err == nil
}

// TestModuleStateFS_KillAndErrorLeaveAStateTheNextCallFinishes: a crash
// before any durable operation of the cleanup, and an ENOSPC or EACCES from
// any one of them, leaves a tree that the same calls, made again over what
// the crash kept, remove completely and cleanly (the kit's I4 for a start
// hook: the next boot completes what the broken one began).
func TestModuleStateFS_KillAndErrorLeaveAStateTheNextCallFinishes(t *testing.T) {
	f := newStateFixture(t)
	census := bootseamtest.New(memCave(t, f), bootseamtest.Options{Clones: true})
	if err := cleanup(t.Context(), newModuleState(census.Seams(), f.state, f.dbParent), f); err != nil {
		t.Fatal(err)
	}
	events, clones := census.Events(), census.Clones()
	if len(events) != 7 || len(clones) != 7 {
		t.Fatalf("the census recorded %d operations and %d kill points, want 7 and 7 (four removals of the tree, one of the key, a sync of each parent)", len(events), len(clones))
	}
	finish := func(name string, mem vfs.FS) {
		t.Helper()
		rec := bootseamtest.New(mem.(*vfs.MemFS), bootseamtest.Options{})
		if err := cleanup(t.Context(), newModuleState(rec.Seams(), f.state, f.dbParent), f); err != nil {
			t.Errorf("%s: the next boot's cleanup: %v", name, err)
		}
		for _, p := range []string{filepath.Join(f.dbParent, "batcave.idx"), filepath.Join(f.state, ".batcave_key")} {
			if memHas(mem, p) {
				t.Errorf("%s: %s survived the next boot's cleanup", name, p)
			}
		}
	}
	for _, c := range clones {
		finish("kill before "+fmt.Sprint(c.Event.Kind)+" "+c.Event.Path, c.FS)
	}
	for _, errno := range []error{errors.New("no space left on device"), fs.ErrPermission} {
		for _, ev := range events {
			mem := memCave(t, f)
			rec := bootseamtest.New(mem, bootseamtest.Options{Fail: func(e bootseamtest.Event) error {
				if e.Seq == ev.Seq {
					return errno
				}
				return nil
			}})
			if err := cleanup(t.Context(), newModuleState(rec.Seams(), f.state, f.dbParent), f); err == nil {
				t.Errorf("%v at %v %s: the cleanup reported success", errno, ev.Kind, ev.Path)
			}
			finish(errno.Error()+" at "+fmt.Sprint(ev.Kind)+" "+ev.Path, mem)
		}
	}
}
