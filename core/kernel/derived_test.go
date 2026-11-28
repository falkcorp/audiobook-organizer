// file: core/kernel/derived_test.go
// version: 0.1.0
// guid: 2d9c5e70-b1a4-4f83-96e2-0a7d3c8f5b19
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// gothamOwners is the store the derived tests boot: module "gotham" declares a
// plain index (signal, version 3) and a Blocking one (cave, version 7).
func gothamOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{{Owner: "gotham", Enabled: true, Spec: gothamSpec()}}
}

func gothamSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Derived: []storage.DerivedIndex{
		{Name: "signal", Version: "3"},
		{Name: "cave", Version: "7", Blocking: true},
	}}
}

// bootDerivedOn boots a store over fs (a crashable in-memory file system the
// test may clone) with ctx's seams, and closes it with the test.
func bootDerivedOn(t *testing.T, ctx context.Context, fs vfs.FS, owners []storage.OwnerSpec) *storage.DB {
	t.Helper()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join("/batcave", "main.pebble"), Owners: owners,
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(fs),
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
	return db
}

// buildTask is the build task a module hands Registrar.DerivedIndex.
func buildTask(module, name string) tasks.Definition {
	return datamigrate.DerivedBuildTask(module, name, datamigrate.DerivedBuildSpec{
		Title: "Rebuild " + name, PermStart: module + ".reindex",
		Run: func(*tasks.RC) error { return nil },
	})
}

// loader is a Load hook that answers as told and records what it was given.
type loader struct {
	mu     sync.Mutex
	accept bool
	err    error
	calls  int
	dirs   []string
	gens   []storage.Generation
}

func (l *loader) hooks() storage.DerivedHooks {
	return storage.DerivedHooks{Load: func(_ context.Context, dir string, cur storage.Generation) (bool, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.calls++
		l.dirs = append(l.dirs, dir)
		l.gens = append(l.gens, cur)
		return l.accept, l.err
	}}
}

// queue is the start function: it records the kinds it was asked to queue.
type queue struct {
	mu    sync.Mutex
	kinds []tasks.Kind
	err   error
	// onStart runs inside start, before it returns.
	onStart func()
}

func (q *queue) start(_ context.Context, kind tasks.Kind) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.onStart != nil {
		q.onStart()
	}
	if q.err != nil {
		return q.err
	}
	q.kinds = append(q.kinds, kind)
	return nil
}

func (q *queue) got() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, k := range q.kinds {
		out = append(out, string(k))
	}
	return out
}

var fixedNow = func() time.Time { return time.Date(2026, 11, 28, 9, 30, 0, 0, time.UTC) }

func gothamDecls(signal, cave *loader) []derivedDecl {
	return []derivedDecl{
		{Module: "gotham", Name: "signal", Hooks: signal.hooks(), Build: buildTask("gotham", "signal")},
		{Module: "gotham", Name: "cave", Hooks: cave.hooks(), Build: buildTask("gotham", "cave")},
	}
}

func readRecord(t *testing.T, db *storage.DB, name string) storage.DerivedRecord {
	t.Helper()
	rec, found, err := db.DerivedRecord(context.Background(), "gotham", name)
	if err != nil || !found {
		t.Fatalf("record of %s = found %t, err %v", name, found, err)
	}
	return rec
}

// TestDerivedIndexRebuildTriggers is the function-level pin of §5S.3.9's
// "rebuild triggers": a version change, a Load refusal and a restore each
// queue the build; a stored, ready record at the declared version over an
// accepted Load queues nothing. It adds the cases a crash makes (a record left
// building, an unreadable record) and the one the contract is silent on (a Load
// that errors counts as a refusal, never as acceptance).
func TestDerivedIndexRebuildTriggers(t *testing.T) {
	ctx := context.Background()
	const kind = "gotham.derived.signal.build"
	ready := func(version string) *storage.DerivedRecord {
		return &storage.DerivedRecord{Version: version, State: storage.DerivedReady, BuiltAt: fixedNow().Add(-time.Hour)}
	}
	for _, c := range []struct {
		name     string
		seed     *storage.DerivedRecord // nil: no record
		corrupt  bool                   // seed an undecodable record instead
		accept   bool
		loadErr  error
		restored bool
		build    bool
	}{
		{name: "the first boot: no record", accept: true, build: true},
		{name: "a ready record at the declared version over an accepted Load", seed: ready("3"), accept: true, build: false},
		{name: "a version change", seed: ready("2"), accept: true, build: true},
		{name: "a Load refusal", seed: ready("3"), accept: false, build: true},
		{name: "a Load error is a refusal", seed: ready("3"), accept: true, loadErr: errors.New("bleve: index is corrupt"), build: true},
		{name: "a restore", seed: ready("3"), accept: true, restored: true, build: true},
		{name: "a build a crash left unfinished", seed: &storage.DerivedRecord{Version: "3", State: storage.DerivedBuilding}, accept: true, build: true},
		{name: "an unreadable record", corrupt: true, accept: true, build: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
			if c.seed != nil {
				if err := db.PutDerivedRecord(ctx, "gotham", "signal", *c.seed); err != nil {
					t.Fatal(err)
				}
			}
			if c.corrupt {
				seedUnreadable(t, db)
			}
			sig := &loader{accept: c.accept, err: c.loadErr}
			// The other index is ready and accepted, so only signal can queue.
			cave := &loader{accept: true}
			if err := db.PutDerivedRecord(ctx, "gotham", "cave", storage.DerivedRecord{Version: "7", State: storage.DerivedReady}); err != nil {
				t.Fatal(err)
			}
			q := &queue{}
			var logs strings.Builder
			log := slog.New(slog.NewTextHandler(&logs, nil))
			blocking, err := runDerived(ctx, db, "/batcave/derived", gothamDecls(sig, cave), c.restored, q.start, fixedNow, derivedLog(log))
			if err != nil {
				t.Fatal(err)
			}
			want := []string(nil)
			if c.build {
				want = []string{kind}
			}
			if restoredCave := c.restored; restoredCave {
				want = append(want, "gotham.derived.cave.build")
				slices.Sort(want)
			}
			got := q.got()
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("queued %q, want %q", got, want)
			}
			rec := readRecord(t, db, "signal")
			if rec.Version != "3" {
				t.Errorf("the record's version = %q, want the declared 3", rec.Version)
			}
			if c.build {
				if rec.State != storage.DerivedBuilding {
					t.Errorf("a build owed left state %q, want building", rec.State)
				}
			} else if rec.State != storage.DerivedReady || !rec.BuiltAt.Equal(fixedNow()) {
				t.Errorf("no build owed left %+v, want ready at %s", rec, fixedNow())
			}
			if c.loadErr != nil && !strings.Contains(logs.String(), "bleve: index is corrupt") {
				t.Errorf("a Load error was dropped; the log reads %q", logs.String())
			}
			_ = blocking
		})
	}
}

// seedUnreadable writes bytes that do not decode as the record of signal.
func seedUnreadable(t *testing.T, db *storage.DB) {
	t.Helper()
	ks, ok := db.Keyspace("core/storage")
	if !ok {
		t.Fatal("no storage keyspace")
	}
	key := keys.Join(keys.Str("derived"), keys.Str("gotham"), keys.Str("signal"))
	if err := ks.Set(context.Background(), key, []byte("not a record")); err != nil {
		t.Fatal(err)
	}
}

func TestRunDerived_LoadIsGivenTheArtifactDirAndTheCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	h, err := db.Derived("gotham", "signal")
	if err != nil {
		t.Fatal(err)
	}
	gotham, _ := db.Keyspace("gotham")
	b := gotham.NewBatch()
	if err := h.Touch(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	want, _ := h.Current(ctx)
	sig, cave := &loader{accept: true}, &loader{accept: true}
	if _, err := runDerived(ctx, db, "/batcave/derived", gothamDecls(sig, cave), false, (&queue{}).start, fixedNow); err != nil {
		t.Fatal(err)
	}
	if got, w := sig.dirs, []string{filepath.FromSlash("/batcave/derived/gotham/signal")}; !slices.Equal(got, w) {
		t.Errorf("signal's Load dir = %q, want %q", got, w)
	}
	if len(sig.gens) != 1 || sig.gens[0] != want || sig.gens[0].IsZero() {
		t.Errorf("signal's Load generation = %v, want Current %v", sig.gens, want)
	}
	if got, w := cave.dirs, []string{filepath.FromSlash("/batcave/derived/gotham/cave")}; !slices.Equal(got, w) {
		t.Errorf("cave's Load dir = %q, want %q", got, w)
	}
}

// TestRunDerived_RecordIsWrittenAfterLoad: while Load runs, the record is
// still the one the previous boot left. A record written first would make a
// crash inside Load look like a finished decision.
func TestRunDerived_RecordIsWrittenAfterLoad(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	old := storage.DerivedRecord{Version: "2", State: storage.DerivedReady, BuiltAt: fixedNow().Add(-time.Hour)}
	for _, n := range []string{"signal", "cave"} {
		if err := db.PutDerivedRecord(ctx, "gotham", n, old); err != nil {
			t.Fatal(err)
		}
	}
	var during []storage.DerivedRecord
	spy := func(name string) storage.DerivedHooks {
		return storage.DerivedHooks{Load: func(ctx context.Context, _ string, _ storage.Generation) (bool, error) {
			rec, _, _ := db.DerivedRecord(ctx, "gotham", name)
			during = append(during, rec)
			return true, nil
		}}
	}
	decls := []derivedDecl{
		{Module: "gotham", Name: "signal", Hooks: spy("signal"), Build: buildTask("gotham", "signal")},
		{Module: "gotham", Name: "cave", Hooks: spy("cave"), Build: buildTask("gotham", "cave")},
	}
	if _, err := runDerived(ctx, db, "/d", decls, false, (&queue{}).start, fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(during) != 2 || during[0] != old || during[1] != old {
		t.Errorf("the records seen inside Load = %+v, want the old %+v twice", during, old)
	}
	if rec := readRecord(t, db, "signal"); rec.Version != "3" {
		t.Errorf("after the run the record = %+v, want version 3", rec)
	}
}

// TestRunDerived_RecordIsDurableBeforeTheRunIsQueued: at the moment start is
// called the record already says building, at the declared version.
func TestRunDerived_RecordIsDurableBeforeTheRunIsQueued(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	var seen []storage.DerivedRecord
	q := &queue{}
	q.onStart = func() {
		rec, found, err := db.DerivedRecord(ctx, "gotham", "signal")
		if err != nil || !found {
			t.Errorf("at the enqueue the record = found %t, err %v", found, err)
		}
		seen = append(seen, rec)
	}
	sig, cave := &loader{accept: false}, &loader{accept: true}
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", storage.DerivedRecord{Version: "7", State: storage.DerivedReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := runDerived(ctx, db, "/d", gothamDecls(sig, cave), false, q.start, fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].State != storage.DerivedBuilding || seen[0].Version != "3" {
		t.Fatalf("the record at the enqueue = %+v, want one building@3", seen)
	}
}

// TestRunDerived_StartFailureLeavesTheBuildOwed: an enqueue that fails returns
// the error and leaves the record building, so the next boot queues the run.
func TestRunDerived_StartFailureLeavesTheBuildOwed(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	sig, cave := &loader{accept: true}, &loader{accept: true}
	boom := errors.New("tasks: executor closed")
	_, err := runDerived(ctx, db, "/d", gothamDecls(sig, cave), false, (&queue{err: boom}).start, fixedNow)
	if !errors.Is(err, boom) {
		t.Fatalf("runDerived = %v, want it to wrap the start error", err)
	}
	if st := readRecord(t, db, "signal").State; st != storage.DerivedBuilding {
		t.Fatalf("after a failed enqueue the record is %q, want building", st)
	}
	// The failing index stopped the loop: cave was never reached.
	if _, found, _ := db.DerivedRecord(ctx, "gotham", "cave"); found {
		t.Error("the loop went on past the failed enqueue")
	}
	q := &queue{}
	if _, err := runDerived(ctx, db, "/d", gothamDecls(sig, cave), false, q.start, fixedNow); err != nil {
		t.Fatal(err)
	}
	if got := q.got(); !slices.Contains(got, "gotham.derived.signal.build") {
		t.Errorf("the next boot queued %q, want the owed signal build", got)
	}
}

func TestRunDerived_BlockingNamesAndMarkBuilt(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	sig, cave := &loader{accept: false}, &loader{accept: false}
	decls := gothamDecls(sig, cave)
	q := &queue{}
	blocking, err := runDerived(ctx, db, "/d", decls, false, q.start, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gotham/cave"}; !slices.Equal(blocking, want) {
		t.Errorf("blocking = %q, want %q (signal is not Blocking)", blocking, want)
	}
	if got, _ := unbuiltBlocking(ctx, db, decls); !slices.Equal(got, []string{"gotham/cave"}) {
		t.Errorf("unbuiltBlocking = %q, want [gotham/cave]", got)
	}
	// The build finishes: the record turns ready and readiness is released.
	if err := markDerivedBuilt(ctx, db, "gotham", "cave", fixedNow); err != nil {
		t.Fatal(err)
	}
	if rec := readRecord(t, db, "cave"); rec.State != storage.DerivedReady || rec.Version != "7" || !rec.BuiltAt.Equal(fixedNow()) {
		t.Errorf("after markDerivedBuilt = %+v", rec)
	}
	if got, _ := unbuiltBlocking(ctx, db, decls); len(got) != 0 {
		t.Errorf("unbuiltBlocking after the build = %q, want none", got)
	}
	// A boot whose Load now accepts queues nothing and blocks nothing.
	sig.accept, cave.accept = true, true
	if err := markDerivedBuilt(ctx, db, "gotham", "signal", fixedNow); err != nil {
		t.Fatal(err)
	}
	q2 := &queue{}
	blocking, err = runDerived(ctx, db, "/d", decls, false, q2.start, fixedNow)
	if err != nil || len(blocking) != 0 || len(q2.got()) != 0 {
		t.Errorf("the boot after the builds = blocking %q, queued %q, err %v; want none", blocking, q2.got(), err)
	}
	// A declared-version bump makes a ready record stale again.
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", storage.DerivedRecord{Version: "6", State: storage.DerivedReady}); err != nil {
		t.Fatal(err)
	}
	if got, _ := unbuiltBlocking(ctx, db, decls); !slices.Equal(got, []string{"gotham/cave"}) {
		t.Errorf("unbuiltBlocking over a stale ready record = %q, want [gotham/cave]", got)
	}
}

func TestRunDerived_RefusesWhatTheStoreDoesNotDeclare(t *testing.T) {
	ctx := context.Background()
	db := bootDerivedOn(t, ctx, vfs.NewCrashableMem(), gothamOwners())
	ld := &loader{accept: true}
	bad := []derivedDecl{{Module: "gotham", Name: "batmobile", Hooks: ld.hooks(), Build: buildTask("gotham", "batmobile")}}
	if _, err := runDerived(ctx, db, "/d", bad, false, (&queue{}).start, fixedNow); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Errorf("an undeclared index = %v, want ErrDerivedUndeclared", err)
	}
	noBuild := []derivedDecl{{Module: "gotham", Name: "signal", Hooks: ld.hooks()}}
	if _, err := runDerived(ctx, db, "/d", noBuild, false, (&queue{}).start, fixedNow); err == nil {
		t.Error("an index with no build task ran")
	}
	if ld.calls != 0 {
		t.Errorf("Load ran %d times for refused declarations", ld.calls)
	}
}

// labelRecorder is a bootseam.Labeler that notes each region and, for the
// labels it watches, takes the two crash clones a kill at that instant would
// leave: exactly what was synced, and that plus a random half of the rest.
type labelRecorder struct {
	mu      sync.Mutex
	fs      *vfs.MemFS
	watch   map[string]bool
	entered []string
	clones  map[string][]*vfs.MemFS
}

func newLabelRecorder(fs *vfs.MemFS, watch ...string) *labelRecorder {
	w := map[string]bool{}
	for _, l := range watch {
		w[l] = true
	}
	return &labelRecorder{fs: fs, watch: w, clones: map[string][]*vfs.MemFS{}}
}

func (l *labelRecorder) Enter(label string) func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entered = append(l.entered, label)
	if l.watch[label] && len(l.clones[label]) == 0 {
		l.clones[label] = []*vfs.MemFS{
			l.fs.CrashClone(vfs.CrashCloneCfg{}),
			l.fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 50, RNG: rand.New(rand.NewPCG(7, 11))}),
		}
	}
	return func() {}
}

func (l *labelRecorder) regions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entered)
}

func (l *labelRecorder) at(t *testing.T, label string) []*vfs.MemFS {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clones[label]) == 0 {
		t.Fatalf("label %q was never entered (entered %v)", label, l.entered)
	}
	return l.clones[label]
}

func seamCtx(l bootseam.Labeler) context.Context {
	return bootseam.With(context.Background(), &bootseam.Seams{Labels: l})
}

// TestDerivedLabels_RegionsAreEnteredInOrder: a build owed enters the record
// region and then the enqueue region; no build owed enters only the record.
func TestDerivedLabels_RegionsAreEnteredInOrder(t *testing.T) {
	if got, want := DerivedLabels(), []string{"storage.derived.record", "kernel.derived.enqueue"}; !slices.Equal(got, want) {
		t.Fatalf("DerivedLabels = %q, want %q", got, want)
	}
	fs := vfs.NewCrashableMem()
	rec := newLabelRecorder(fs)
	ctx := seamCtx(rec)
	db := bootDerivedOn(t, ctx, fs, gothamOwners())
	// Boot writes its own labels; only the derived ones are compared.
	derived := func() []string {
		return slices.DeleteFunc(rec.regions(), func(l string) bool { return !slices.Contains(DerivedLabels(), l) })
	}
	sig, cave := &loader{accept: false}, &loader{accept: true}
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", storage.DerivedRecord{Version: "7", State: storage.DerivedReady}); err != nil {
		t.Fatal(err)
	}
	pre := len(derived())
	if _, err := runDerived(ctx, db, "/d", gothamDecls(sig, cave), false, (&queue{}).start, fixedNow); err != nil {
		t.Fatal(err)
	}
	// signal: owed (record, enqueue); cave: accepted over a ready record (record only).
	want := []string{"storage.derived.record", "kernel.derived.enqueue", "storage.derived.record"}
	if got := derived()[pre:]; !slices.Equal(got, want) {
		t.Errorf("regions entered = %q, want %q", got, want)
	}
}

// TestRunDerived_CrashCells: a kill at each instant of step (3a) loses no
// build. Each cell takes the crash clones at a label, boots a store over each
// clone and runs step (3a) again with a Load that ACCEPTS the artifact (the
// worst case: an artifact left by the previous build, at the declared
// generation, which Load cannot tell is stale).
func TestRunDerived_CrashCells(t *testing.T) {
	const signalKind = "gotham.derived.signal.build"
	fs := vfs.NewCrashableMem()
	rec := newLabelRecorder(fs, "storage.derived.record", "kernel.derived.enqueue")
	ctx := seamCtx(rec)
	db := bootDerivedOn(t, ctx, fs, gothamOwners())

	// The cave index is ready and accepted throughout, so only signal queues.
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", storage.DerivedRecord{Version: "7", State: storage.DerivedReady}); err != nil {
		t.Fatal(err)
	}
	// The hit above already entered the record label; reset so the first
	// signal write is the one watched.
	rec.mu.Lock()
	rec.clones = map[string][]*vfs.MemFS{}
	rec.mu.Unlock()

	sig, cave := &loader{accept: false}, &loader{accept: true}
	q := &queue{}
	if _, err := runDerived(ctx, db, "/d", gothamDecls(sig, cave), false, q.start, fixedNow); err != nil {
		t.Fatal(err)
	}
	if got := q.got(); !slices.Equal(got, []string{signalKind}) {
		t.Fatalf("the first boot queued %q, want [%s]", got, signalKind)
	}

	cells := []struct {
		name  string
		label string
		// state of signal's record the clone must hold, which pins what the cell is.
		state *storage.DerivedState
	}{
		{"a kill before the record is written", "storage.derived.record", nil},
		{"a kill between the record and the enqueue", "kernel.derived.enqueue", ptr(storage.DerivedBuilding)},
	}
	for _, c := range cells {
		for i, clone := range rec.at(t, c.label) {
			t.Run(c.name+"/"+string(rune('a'+i)), func(t *testing.T) {
				db2 := bootDerivedOn(t, context.Background(), clone, gothamOwners())
				got, found, err := db2.DerivedRecord(context.Background(), "gotham", "signal")
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case c.state == nil && found:
					t.Fatalf("the clone before the record write holds %+v", got)
				case c.state != nil && (!found || got.State != *c.state):
					t.Fatalf("the clone between the writes holds %+v (found %t), want state %q", got, found, *c.state)
				}
				// Reboot: Load accepts, no restore.
				q2 := &queue{}
				if _, err := runDerived(context.Background(), db2, "/d", gothamDecls(&loader{accept: true}, &loader{accept: true}), false, q2.start, fixedNow); err != nil {
					t.Fatal(err)
				}
				if got := q2.got(); !slices.Equal(got, []string{signalKind}) {
					t.Fatalf("after the kill the next boot queued %q, want [%s]: the build was lost", got, signalKind)
				}
			})
		}
	}

	// A kill after the enqueue: the run is queued and the record still says
	// building, so the next boot queues it AGAIN (at-least-once), and the
	// build's own end (markDerivedBuilt) is what stops that.
	t.Run("a kill after the enqueue queues again, until the build marks itself built", func(t *testing.T) {
		after := fs.CrashClone(vfs.CrashCloneCfg{})
		db2 := bootDerivedOn(t, context.Background(), after, gothamOwners())
		decls := gothamDecls(&loader{accept: true}, &loader{accept: true})
		q2 := &queue{}
		if _, err := runDerived(context.Background(), db2, "/d", decls, false, q2.start, fixedNow); err != nil {
			t.Fatal(err)
		}
		if got := q2.got(); !slices.Equal(got, []string{signalKind}) {
			t.Fatalf("queued %q, want one repeat of %s", got, signalKind)
		}
		if err := markDerivedBuilt(context.Background(), db2, "gotham", "signal", fixedNow); err != nil {
			t.Fatal(err)
		}
		q3 := &queue{}
		if _, err := runDerived(context.Background(), db2, "/d", decls, false, q3.start, fixedNow); err != nil || len(q3.got()) != 0 {
			t.Errorf("after the build marked itself built the next boot queued %q (err %v), want none", q3.got(), err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestDerivedProblems(t *testing.T) {
	ld := (&loader{}).hooks()
	specs := map[string]storage.Spec{
		"gotham": gothamSpec(),
		"wayne":  {Keyspace: true},
	}
	good := []derivedDecl{
		{Module: "gotham", Name: "signal", Hooks: ld, Build: buildTask("gotham", "signal")},
		{Module: "gotham", Name: "cave", Hooks: ld, Build: buildTask("gotham", "cave")},
	}
	if got := derivedProblems(specs, good); len(got) != 0 {
		t.Fatalf("a correct registration reported %v", got)
	}
	// A disabled module's declaration is not checked: it is not in specs.
	if got := derivedProblems(map[string]storage.Spec{"wayne": {Keyspace: true}}, nil); len(got) != 0 {
		t.Errorf("no declarations reported %v", got)
	}
	for _, c := range []struct {
		name  string
		decls []derivedDecl
		want  string // a substring of the one problem's Detail
	}{
		{"declared and never registered", good[:1], `"cave" is declared in Manifest.Storage.Derived and was never registered`},
		{"registered and never declared", append(slices.Clone(good), derivedDecl{Module: "gotham", Name: "batmobile", Hooks: ld, Build: buildTask("gotham", "batmobile")}), `registers an index that Manifest.Storage.Derived does not declare`},
		{"registered by a module that declares none", append(slices.Clone(good), derivedDecl{Module: "wayne", Name: "signal", Hooks: ld, Build: buildTask("wayne", "signal")}), `does not declare`},
		{"registered twice", append(slices.Clone(good), good[0]), `"signal" is registered twice`},
		{"no Load hook", []derivedDecl{good[0], {Module: "gotham", Name: "cave", Build: buildTask("gotham", "cave")}}, `"cave" has no Load hook`},
		{"Snapshot alone is not hooks", []derivedDecl{good[0], {Module: "gotham", Name: "cave", Hooks: storage.DerivedHooks{Snapshot: func(context.Context, string, storage.Generation) error { return nil }}, Build: buildTask("gotham", "cave")}}, `"cave" has no Load hook`},
		{"no build task", []derivedDecl{good[0], {Module: "gotham", Name: "cave", Hooks: ld}}, `"cave" has no build task`},
		{"a build task of another module", []derivedDecl{good[0], {Module: "gotham", Name: "cave", Hooks: ld, Build: buildTask("wayne", "cave")}}, `build task of kind "wayne.derived.cave.build", want exactly "gotham.derived.cave.build"`},
		{"a build task of another kind", []derivedDecl{good[0], {Module: "gotham", Name: "cave", Hooks: ld, Build: buildTask("gotham", "signal")}}, `build task of kind "gotham.derived.signal.build", want exactly "gotham.derived.cave.build"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := derivedProblems(specs, c.decls)
			if len(got) != 1 {
				t.Fatalf("problems = %v, want exactly one", got)
			}
			if got[0].Kind != module.ProblemDerivedIndex || !strings.Contains(got[0].Detail, c.want) {
				t.Errorf("problem = %v, want kind ProblemDerivedIndex with %q", got[0], c.want)
			}
		})
	}
	// Problems come back sorted by module, then by what they say.
	many := derivedProblems(specs, nil)
	if len(many) != 2 || !strings.Contains(many[0].Detail, `"cave"`) || !strings.Contains(many[1].Detail, `"signal"`) {
		t.Errorf("two missing registrations = %v, want cave then signal", many)
	}
}
