// file: core/library/helpers_test.go
// version: 1.0.0
// guid: 34ac2131-b6fb-47d9-b049-9f60d823773c
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

// libOwner is the owner of the c/library/ keyspace the kernel registers.
const libOwner storage.Owner = "core/library"

// Module owners of the fixtures: the books module and the tv module of an app
// that has both. The fixtures are Batman-themed.
const (
	books storage.Owner = "books"
	tv    storage.Owner = "tv"
)

// store is a booted c/library/ store over an in-memory filesystem. library is
// reached by the kernel's import graph, so its tests boot storage as the
// kernel does (storage.Open, Prepare, Migrate) and never import coretest.
type store struct {
	db     *storage.DB
	ks     storage.Keyspace
	owners []storage.Owner
}

func bootStore(t testing.TB, fsys vfs.FS, dir string, extra ...storage.Owner) *store {
	t.Helper()
	ctx := context.Background()
	owners := []storage.OwnerSpec{{Owner: libOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true}}
	for _, o := range extra {
		owners = append(owners, storage.OwnerSpec{Owner: o, Spec: storage.Spec{Keyspace: true}, Enabled: true})
	}
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(dir, "main.pebble"),
		Owners:          owners,
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fsys),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(libOwner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", libOwner)
	}
	return &store{db: db, ks: ks, owners: extra}
}

// keyspace is an extra owner's keyspace (a module's own rows, committed in the
// same batch as a TransferIn).
func (s *store) keyspace(t testing.TB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := s.db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

func newStore(t testing.TB) *store { return bootStore(t, vfs.NewMem(), t.TempDir()) }

// faults injects failures and a simulated process death around every mutating
// filesystem call and every durable journal write. After the kill point every
// later mutation fails, as it would in a process that is gone, so a cleanup the
// code would have run is not run either.
type faults struct {
	mu     sync.Mutex
	count  int
	killAt int // 1-based index of the first mutation that does not happen; 0 = never
	dead   bool
	inject func(label string) error // a specific failure (a device boundary, ENOSPC); nil = none
	labels []string
}

var errDead = errors.New("test: the process died here")

func (f *faults) step(label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return errDead
	}
	f.count++
	f.labels = append(f.labels, label)
	if f.killAt > 0 && f.count >= f.killAt {
		f.dead = true
		return errDead
	}
	if f.inject != nil {
		return f.inject(label)
	}
	return nil
}

func (f *faults) steps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

// faultFS wraps a root's filesystem so each mutation passes through faults.
type faultFS struct {
	fsOps
	f  *faults
	id RootID
}

func (w faultFS) lab(op, name string) string { return string(w.id) + ":" + op + ":" + name }

func (w faultFS) CreateExclusive(name string, perm fs.FileMode) (File, error) {
	if err := w.f.step(w.lab("create", name)); err != nil {
		return nil, err
	}
	file, err := w.fsOps.CreateExclusive(name, perm)
	if err != nil {
		return nil, err
	}
	return faultFile{File: file, f: w.f, name: name}, nil
}

func (w faultFS) MkdirAll(name string, perm fs.FileMode) error {
	if err := w.f.step(w.lab("mkdir", name)); err != nil {
		return err
	}
	return w.fsOps.MkdirAll(name, perm)
}

func (w faultFS) Remove(name string) error {
	if err := w.f.step(w.lab("remove", name)); err != nil {
		return err
	}
	return w.fsOps.Remove(name)
}

func (w faultFS) RenameNoReplace(o, n string) error { return w.renameNoReplaceInto(w, o, n) }
func (w faultFS) Link(o, n string) error            { return w.linkInto(w, o, n) }
func (w faultFS) Reflink(o, n string) error         { return w.reflinkInto(w, o, n) }

func (w faultFS) renameNoReplaceInto(dst fsOps, o, n string) error {
	if err := w.f.step(w.lab("rename", o+"->"+n)); err != nil {
		return err
	}
	return w.fsOps.renameNoReplaceInto(dst, o, n)
}

func (w faultFS) linkInto(dst fsOps, o, n string) error {
	if err := w.f.step(w.lab("link", o+"->"+n)); err != nil {
		return err
	}
	return w.fsOps.linkInto(dst, o, n)
}

func (w faultFS) reflinkInto(dst fsOps, o, n string) error {
	if err := w.f.step(w.lab("reflink", o+"->"+n)); err != nil {
		return err
	}
	return w.fsOps.reflinkInto(dst, o, n)
}

func (w faultFS) Symlink(target Path, n string) error {
	if err := w.f.step(w.lab("symlink", n)); err != nil {
		return err
	}
	return w.fsOps.Symlink(target, n)
}

func (w faultFS) SyncDir(name string) error {
	if err := w.f.step(w.lab("syncdir", name)); err != nil {
		return err
	}
	return w.fsOps.SyncDir(name)
}

type faultFile struct {
	File
	f    *faults
	name string
}

func (w faultFile) Write(p []byte) (int, error) {
	if err := w.f.step("write:" + w.name); err != nil {
		return 0, err
	}
	return w.File.Write(p)
}

func (w faultFile) Sync() error {
	if err := w.f.step("sync:" + w.name); err != nil {
		return err
	}
	return w.File.Sync()
}

// faultStore counts and can kill the durable journal writes.
type faultStore struct {
	rowStore
	f    *faults
	name string
}

func (s faultStore) put(ctx context.Context, id string, v any) error {
	if err := s.f.step(s.name + ":put"); err != nil {
		return err
	}
	return s.rowStore.put(ctx, id, v)
}

func (s faultStore) remove(ctx context.Context, id string) error {
	if err := s.f.step(s.name + ":remove"); err != nil {
		return err
	}
	return s.rowStore.remove(ctx, id)
}

// rig is a manager over fixture roots in temp directories.
type rig struct {
	t     *testing.T
	st    *store
	m     *manager
	f     *faults
	dirs  map[RootID]string
	specs []RootSpec
}

type rigOpts struct {
	seams   seams
	enabled func(storage.Owner) bool
	caps    func(storage.Owner, string) bool
	oracles []ProtectionOracle
	faults  *faults
}

const testInstall = "install-gotham-1"

// newRig builds a manager over st with the given roots. Each root gets its own
// temp directory; a root with SentinelRequired is given its sentinel.
func newRig(t *testing.T, st *store, o rigOpts, specs ...RootSpec) *rig {
	t.Helper()
	r := &rig{t: t, st: st, f: o.faults, dirs: map[RootID]string{}}
	for _, s := range specs {
		if s.Path == "" {
			s.Path = t.TempDir()
		}
		r.dirs[s.ID] = s.Path
		r.specs = append(r.specs, s)
	}
	r.m = r.build(o)
	return r
}

// build makes a manager (a fresh "process") over the rig's store and roots.
func (r *rig) build(o rigOpts) *manager {
	r.t.Helper()
	sm := o.seams
	if o.faults != nil {
		fl := o.faults
		sm.wrapFS = func(id RootID, f fsOps) fsOps { return faultFS{fsOps: f, f: fl, id: id} }
		sm.wrapStore = func(name string, s rowStore) rowStore { return faultStore{rowStore: s, f: fl, name: name} }
	}
	m, err := newManager(Options{
		Keyspace: r.st.ks, InstallID: testInstall, Oracles: o.oracles, Enabled: o.enabled, HasCapability: o.caps,
	}, sm)
	if err != nil {
		r.t.Fatalf("newManager: %v", err)
	}
	r.t.Cleanup(func() { _ = m.Close() })
	set := defaultSettings()
	set.roots = r.specs
	if err := m.apply(context.Background(), set); err != nil {
		r.t.Fatalf("apply: %v", err)
	}
	for _, s := range r.specs {
		root, err := m.Root(s.ID)
		if err != nil {
			r.t.Fatalf("root %s: %v", s.ID, err)
		}
		if root.Disabled() != nil || s.Sentinel == SentinelBootstrap || s.Role == RoleExternal {
			continue
		}
		if err := m.WriteSentinel(context.Background(), s.ID); err != nil {
			r.t.Fatalf("sentinel %s: %v", s.ID, err)
		}
	}
	return m
}

// spec is a RootSpec with the fixture defaults.
func spec(id RootID, owner storage.Owner, role Role) RootSpec {
	return RootSpec{ID: id, Owner: owner, Role: role, Watch: WatchOff, MinFree: 0}
}

func (r *rig) abs(id RootID, rel string) string {
	return filepath.Join(r.dirs[id], filepath.FromSlash(rel))
}

// put writes a fixture file and returns its Path.
func (r *rig) put(id RootID, rel, content string) Path {
	r.t.Helper()
	p := r.abs(id, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o664); err != nil {
		r.t.Fatal(err)
	}
	return Path{Root: id, Rel: rel}
}

func (r *rig) read(id RootID, rel string) (string, bool) {
	b, err := os.ReadFile(r.abs(id, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (r *rig) exists(id RootID, rel string) bool {
	_, err := os.Lstat(r.abs(id, rel))
	return err == nil
}

// leftovers lists library's own temp and aside files under a root.
func (r *rig) leftovers(id RootID) []string {
	var out []string
	_ = filepath.WalkDir(r.dirs[id], func(p string, d fs.DirEntry, err error) error {
		if err == nil && (strings.Contains(d.Name(), tmpPrefix) || strings.Contains(d.Name(), asideInfix)) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func move() TransferOptions {
	return NewTransferOptions(Move, PermLibrary, VerifySHA256)
}

func copyOpts() TransferOptions {
	o := NewTransferOptions(Copy, PermLibrary, VerifySHA256)
	o.MkdirAll = true
	return o
}

func ctxbg() context.Context { return context.Background() }
