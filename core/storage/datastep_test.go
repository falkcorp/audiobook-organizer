// file: core/storage/datastep_test.go
// version: 1.0.1
// guid: 9d690ab6-4cc9-4fd9-b3c2-6b3dc81a8f23
// last-edited: 2026-11-10

package storage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// dataLog records the order the DAG ran steps in.
type dataLog struct {
	mu  sync.Mutex
	got []string
}

func (l *dataLog) add(s string) {
	l.mu.Lock()
	l.got = append(l.got, s)
	l.mu.Unlock()
}

func (l *dataLog) steps() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

// loggedStep is a versioned step that logs its name.
func loggedStep(l *dataLog, v int, name string) Migration {
	return Migration{Version: v, Name: name, Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
		l.add(name)
		return tx.Set([]byte(name), []byte("1"))
	}}
}

// dataStep is a data step that logs its ID, fails with fail when set, and
// otherwise records itself done as the runner would.
func dataStep(l *dataLog, owner Owner, id string, fail error) DataStep {
	return DataStep{Owner: owner, ID: id, Run: func(ctx context.Context, db *DB) error {
		l.add(id)
		if fail != nil {
			return &StepError{Step: StepRef{Owner: owner, DataMigration: id}, Err: fail}
		}
		raw, err := DataMigrationCodec().Encode(DataMigrationRecord{State: DataMigrationDone, Build: "batcomputer-1"})
		if err != nil {
			return err
		}
		return db.storageKS().Set(ctx, DataMigrationKey(id), raw)
	}}
}

// upgradedEnv is a wayne store on which step 1 already ran, so the next boot
// is an upgrade (not a fresh install) with whatever wayne then declares.
func upgradedEnv(t *testing.T) *env {
	t.Helper()
	var l dataLog
	e := newEnvFS(t, vfs.NewMem(), OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{loggedStep(&l, 1, "cave")}}})
	db := e.open()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestDataStepRunsInDAGOrderBeforeStep9: a data step runs after its
// owner's versions below Barrier and before the Barrier version, inside
// Migrate, and a store with a pending data step takes the checkpoint
// (§5S.3.5 "Order", §5S.4 steps 6 and 8).
func TestDataStepRunsInDAGOrderBeforeStep9(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		loggedStep(&l, 1, "cave"), loggedStep(&l, 2, "manor"), loggedStep(&l, 3, "tower"),
	}}}}
	dm := dataStep(&l, "wayne", "wayne.batmobile", nil)
	dm.Barrier = 3
	e.opts.DataSteps = []DataStep{dm}
	db := e.open()
	if got, want := l.steps(), []string{"manor", "wayne.batmobile", "tower"}; !slices.Equal(got, want) {
		t.Fatalf("DAG order = %v, want %v", got, want)
	}
	if db.CheckpointName() == "" {
		t.Fatal("an upgrade with a pending data step took no checkpoint")
	}
	if done, err := db.stepDone(context.Background(), StepRef{Owner: "wayne", DataMigration: "wayne.batmobile"}); err != nil || !done {
		t.Fatalf("stepDone = %v, %v; want done (keyed by the ID alone, c/storage/dm/<id>)", done, err)
	}
	if rec := readRecord(t, e, db.CheckpointName()); rec.open() {
		t.Fatalf("checkpoint record after a clean boot = %+v, want completed by step 9", rec)
	}
}

// TestDataStepWaitsForItsOwnersVersions: a data step runs after its owner's
// versions even when one of them waits on another owner that sorts later,
// so the smallest-ready-first order alone would run the data step first.
func TestDataStepWaitsForItsOwnersVersions(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	manor := loggedStep(&l, 2, "manor")
	manor.After = []StepRef{{Owner: "zsasz", Version: 1}}
	e.opts.Owners = []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{loggedStep(&l, 1, "cave"), manor}}},
		{Owner: "zsasz", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{loggedStep(&l, 1, "tally")}}},
	}
	e.opts.DataSteps = []DataStep{dataStep(&l, "wayne", "wayne.batmobile", nil)}
	e.open()
	if got, want := l.steps(), []string{"tally", "manor", "wayne.batmobile"}; !slices.Equal(got, want) {
		t.Fatalf("DAG order = %v, want %v", got, want)
	}
}

// TestDataStepBarrierValidated: a Barrier naming no registered version of
// its owner is a SpecError at Open, and a pending step whose Barrier
// version the store already applied fails the boot with ErrBarrierApplied
// before any step runs.
func TestDataStepBarrierValidated(t *testing.T) {
	e := upgradedEnv(t) // wayne at version 1
	var l dataLog
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		loggedStep(&l, 1, "cave"), loggedStep(&l, 2, "manor"),
	}}}}
	unknown := dataStep(&l, "wayne", "wayne.batmobile", nil)
	unknown.Barrier = 9
	e.opts.DataSteps = []DataStep{unknown}
	_, err := e.tryOpen()
	if _, ok := errors.AsType[*SpecError](err); !ok || !strings.Contains(err.Error(), "Barrier 9 names no registered version") {
		t.Fatalf("boot with Barrier 9 = %v, want a SpecError naming it", err)
	}
	applied := dataStep(&l, "wayne", "wayne.batmobile", nil)
	applied.Barrier = 1
	e.opts.DataSteps = []DataStep{applied}
	if _, err := e.tryOpen(); !errors.Is(err, ErrBarrierApplied) {
		t.Fatalf("boot with Barrier 1 on a store at version 1 = %v, want ErrBarrierApplied", err)
	}
	if got := l.steps(); len(got) != 0 {
		t.Fatalf("steps ran on a refused boot: %v", got)
	}
}

// TestRewriteSidecarsChecksTheDAG: the sidecar rewrite validates the
// owners' DAG as Open does, before it touches the store.
func TestRewriteSidecarsChecksTheDAG(t *testing.T) {
	var l dataLog
	cave := loggedStep(&l, 1, "cave")
	cave.After = []StepRef{{Owner: "wayne", Version: 7}}
	owners := []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{cave}}}}
	if err := RewriteSidecars(context.Background(), vfs.NewMem(), "/batcave/store", owners); !errors.Is(err, ErrMigrationCycle) {
		t.Fatalf("RewriteSidecars with an After naming no step = %v, want ErrMigrationCycle", err)
	}
}

// TestDataStepFailureLeavesCheckpointOpenAndHoldsBarrier: a failed data step
// halts the DAG with ErrMigrationFailed wrapping its StepError, the Barrier
// version does not run, the attempt's checkpoint stays open, and the next
// boot of a fixed build runs the step and then the Barrier version
// (§5S.3.6 "A failed Verify ... Barrier versions stay blocked").
func TestDataStepFailureLeavesCheckpointOpenAndHoldsBarrier(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	owners := []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		loggedStep(&l, 1, "cave"), loggedStep(&l, 2, "manor"),
	}}}}
	e.opts.Owners = owners
	broken := dataStep(&l, "wayne", "wayne.batmobile", errors.New("the Joker cut the brake lines"))
	broken.Barrier = 2
	e.opts.DataSteps = []DataStep{broken}
	_, err := e.tryOpen()
	var mf *ErrMigrationFailed
	if !errors.As(err, &mf) || mf.Step.DataMigration != "wayne.batmobile" {
		t.Fatalf("boot = %v, want ErrMigrationFailed naming the data step", err)
	}
	if se, ok := errors.AsType[*StepError](err); !ok || se.Step.DataMigration != "wayne.batmobile" {
		t.Fatalf("boot = %v, want the step's StepError inside", err)
	}
	if got := l.steps(); slices.Contains(got, "manor") {
		t.Fatalf("the Barrier version ran before the data step was done: %v", got)
	}
	name := readPointer(t, e).Name
	if rec := readRecord(t, e, name); !rec.open() {
		t.Fatalf("checkpoint record after the failed data step = %+v, want open", rec)
	}
	// A fixed build: the step runs, then the Barrier version.
	l = dataLog{}
	fixed := dataStep(&l, "wayne", "wayne.batmobile", nil)
	fixed.Barrier = 2
	e.opts.DataSteps = []DataStep{fixed}
	db := e.open()
	if got, want := l.steps(), []string{"wayne.batmobile", "manor"}; !slices.Equal(got, want) {
		t.Fatalf("fixed boot order = %v, want %v", got, want)
	}
	if v := db.AppliedVersion("wayne"); v != 2 {
		t.Fatalf("wayne at version %d, want 2", v)
	}
}

// TestDataStepFreshInstallRecordsDone: a fresh install records every
// registered data step done in its one batch and runs none (§5S.4 step 6).
func TestDataStepFreshInstallRecordsDone(t *testing.T) {
	var l dataLog
	e := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
	e.opts.DataSteps = []DataStep{dataStep(&l, "wayne", "wayne.batmobile", errors.New("must not run"))}
	db := e.open()
	if got := l.steps(); len(got) != 0 {
		t.Fatalf("a fresh install ran %v", got)
	}
	if done, err := db.stepDone(context.Background(), StepRef{Owner: "wayne", DataMigration: "wayne.batmobile"}); err != nil || !done {
		t.Fatalf("stepDone = %v, %v; want done", done, err)
	}
}

// TestDataStepPendingRefusesOfflineOpen: a pending data step is a runnable
// step, so an offline open refuses the store, naming it (§5S.4 "Offline
// open" rule 2).
func TestDataStepPendingRefusesOfflineOpen(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	e.opts.DataSteps = []DataStep{dataStep(&l, "wayne", "wayne.batmobile", nil)}
	op, err := Open(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = op.Close() }()
	_, err = op.Offline()
	pe, ok := errors.AsType[*OfflinePendingError](err)
	if !ok || len(pe.Steps) != 1 || pe.Steps[0].DataMigration != "wayne.batmobile" {
		t.Fatalf("Offline = %v, want the pending data step named", err)
	}
}

// TestDataStepDisabledOwner: a disabled owner's data step is deferred, a
// cutover step runs anyway (06 A5).
func TestDataStepDisabledOwner(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: false, Spec: Spec{Keyspace: true, Migrations: []Migration{loggedStep(&l, 1, "cave")}}}}
	cut := dataStep(&l, "wayne", "wayne.cutover", nil)
	cut.Cutover = true
	e.opts.DataSteps = []DataStep{dataStep(&l, "wayne", "wayne.batmobile", nil), cut}
	db := e.open()
	if got, want := l.steps(), []string{"wayne.cutover"}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if _, deferred := db.Plan(); len(deferred) != 1 || deferred[0].DataMigration != "wayne.batmobile" {
		t.Fatalf("deferred = %v, want wayne.batmobile", deferred)
	}
}

// TestDataStepAfterEdges: explicit After edges order data steps against each
// other and against another owner's versions; declaration order breaks ties.
func TestDataStepAfterEdges(t *testing.T) {
	e := upgradedEnv(t)
	var l dataLog
	e.opts.Owners = []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{loggedStep(&l, 1, "cave")}}},
		{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
			{Version: 1, Name: "clocks", Compat: Additive, After: []StepRef{{Owner: "wayne", DataMigration: "wayne.alfred"}},
				Up: func(ctx context.Context, tx *MigrationTx) error { l.add("clocks"); return nil }},
		}}},
	}
	second := dataStep(&l, "wayne", "wayne.alfred", nil)
	second.After = []StepRef{{Owner: "wayne", DataMigration: "wayne.batmobile"}}
	e.opts.DataSteps = []DataStep{second, dataStep(&l, "wayne", "wayne.batmobile", nil), dataStep(&l, "wayne", "wayne.robin", nil)}
	e.open()
	got := l.steps()
	pos := func(s string) int { return slices.Index(got, s) }
	if pos("wayne.batmobile") > pos("wayne.alfred") || pos("wayne.alfred") > pos("clocks") || pos("wayne.robin") < pos("wayne.batmobile") {
		t.Fatalf("order %v breaks an edge or the declaration tie-break", got)
	}
}

// TestDataStepRegistration: every problem of the data step list is refused
// at Open, before anything is written.
func TestDataStepRegistration(t *testing.T) {
	ok := func(context.Context, *DB) error { return nil }
	cases := map[string]struct {
		steps []DataStep
		want  error
		has   string
	}{
		"unknown owner": {[]DataStep{{Owner: "gotham", ID: "gotham.x", Run: ok}}, ErrInvalidSpec, "not a registered"},
		"empty ID":      {[]DataStep{{Owner: "wayne", Run: ok}}, ErrInvalidSpec, "empty ID"},
		"duplicate":     {[]DataStep{{Owner: "wayne", ID: "wayne.x", Run: ok}, {Owner: "wayne", ID: "wayne.x", Run: ok}}, ErrInvalidSpec, "twice"},
		"no Run":        {[]DataStep{{Owner: "wayne", ID: "wayne.x"}}, ErrInvalidSpec, "no Run"},
		"bad Barrier":   {[]DataStep{{Owner: "wayne", ID: "wayne.x", Run: ok, Barrier: -1}}, ErrInvalidSpec, "negative"},
		"bad After":     {[]DataStep{{Owner: "wayne", ID: "wayne.x", Run: ok, After: []StepRef{{Owner: "wayne", Version: 1, DataMigration: "wayne.y"}}}}, ErrInvalidSpec, "neither"},
		"unknown edge":  {[]DataStep{{Owner: "wayne", ID: "wayne.x", Run: ok, After: []StepRef{{Owner: "wayne", DataMigration: "wayne.y"}}}}, ErrMigrationCycle, "unknown step"},
		"cycle":         {[]DataStep{{Owner: "wayne", ID: "wayne.x", Run: ok, Barrier: 1, After: []StepRef{{Owner: "wayne", Version: 1}}}}, ErrMigrationCycle, "cycle"},
		"unordered":     {[]DataStep{{Owner: "core/zzz", ID: "core/zzz.x", Run: ok}}, ErrMigrationCycle, "unordered owner"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t,
				OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step(1, "cave", "cave", "1")}}},
				OwnerSpec{Owner: "core/zzz", Enabled: true, Spec: Spec{Keyspace: true}})
			e.opts.DataSteps = c.steps
			_, err := Open(context.Background(), e.opts)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.has) {
				t.Fatalf("Open = %v, want %v mentioning %q", err, c.want, c.has)
			}
		})
	}
}
