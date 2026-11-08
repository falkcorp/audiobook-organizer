// file: core/storage/migrate_test.go
// version: 1.0.0
// guid: 25ec79a2-2c84-47a2-8cd6-89d5026a6925
// last-edited: 2026-11-07

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestMigrationStepAtomicWithBookkeeping (05-storage §5S.10): a crash at any
// point of a versioned step leaves nothing of it, or its writes with its
// version and applied record.
func TestMigrationStepAtomicWithBookkeeping(t *testing.T) {
	for _, crash := range []string{"before-apply", "after-commit"} {
		t.Run(crash, func(t *testing.T) {
			fs := vfs.NewCrashableMem()
			e := newEnvFS(t, fs, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true})
			db := e.open()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			two := Migration{Version: 1, Name: "suits", Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
				if err := tx.Set([]byte("suit/1"), []byte("black")); err != nil {
					return err
				}
				return tx.Set([]byte("suit/2"), []byte("grey"))
			}}
			e.opts.Owners = []OwnerSpec{{Owner: "wayne", Spec: Spec{Keyspace: true, Migrations: []Migration{two}}, Enabled: true}}
			var clone *vfs.MemFS
			if crash == "before-apply" {
				e.opts.hooks.beforeApply = crashOnce(fs, &clone)
			}
			e.open()
			if crash == "after-commit" {
				clone = fs.CrashClone(vfs.CrashCloneCfg{})
			}
			rows := rawKeys(t, clone, e.path, "m/wayne/suit/")
			book := rawKeys(t, clone, e.path, "c/storage/"+string(versionKey("wayne")))
			applied := rawKeys(t, clone, e.path, "c/storage/"+string(appliedPrefix("wayne")))
			switch crash {
			case "before-apply":
				if len(rows)+len(book)+len(applied) != 0 {
					t.Fatalf("crash before the apply left rows %v, version %v, records %v", rows, book, applied)
				}
			default:
				if len(rows) != 2 || len(book) != 1 || len(applied) != 1 {
					t.Fatalf("committed step left rows %v, version %v, records %v", rows, book, applied)
				}
			}
		})
	}
}

// dagOwners declares steps across core subsystems and modules.
func dagOwners(record *[]string, robinEnabled bool) []OwnerSpec {
	mk := func(o Owner, v int, after ...StepRef) Migration {
		return Migration{Version: v, Name: fmt.Sprintf("s%d", v), Compat: Additive, After: after,
			Up: func(context.Context, *MigrationTx) error {
				*record = append(*record, StepRef{Owner: o, Version: v}.String())
				return nil
			}}
	}
	return []OwnerSpec{
		{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{mk("core/tasks", 1), mk("core/tasks", 2)}}},
		{Owner: "core/auth", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{mk("core/auth", 1, StepRef{Owner: "robin", Version: 1})}}},
		{Owner: "core/rbac", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{mk("core/rbac", 1)}}},
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{mk("wayne", 1, StepRef{Owner: "core/tasks", Version: 2})}}},
		{Owner: "robin", Enabled: robinEnabled, Spec: Spec{Keyspace: true, Migrations: []Migration{mk("robin", 1)}}},
	}
}

// bare is the owners with their keyspaces and no step, for a first boot.
func bare(owners []OwnerSpec) []OwnerSpec {
	out := slices.Clone(owners)
	for i := range out {
		out[i].Spec = Spec{Keyspace: out[i].Spec.Keyspace, Claims: out[i].Spec.Claims}
	}
	return out
}

// TestMigrationDAGOrdersAcrossOwners (05-storage §5S.10, COR3-14): After
// edges, ties by the fixed core order then module ID, an unordered core
// owner refused, cycle and unknown-step detection, and deferral behind a
// disabled owner.
func TestMigrationDAGOrdersAcrossOwners(t *testing.T) {
	var ran []string
	owners := dagOwners(&ran, true)
	e := newEnv(t, bare(owners)...)
	db := e.open()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = owners
	e.open()
	want := []string{"core/rbac#1", "core/tasks#1", "core/tasks#2", "robin#1", "core/auth#1", "wayne#1"}
	if !slices.Equal(ran, want) {
		t.Fatalf("order = %v, want %v", ran, want)
	}

	refused := func(name string, owners []OwnerSpec, sub string) {
		t.Helper()
		_, err := newEnv(t, owners...).tryOpen()
		mustErrIs(t, err, ErrMigrationCycle)
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("%s: %v does not mention %q", name, err, sub)
		}
	}
	add := func(o Owner, after ...StepRef) Migration {
		return Migration{Version: 1, Name: "s1", Compat: Additive, After: after, Up: func(context.Context, *MigrationTx) error { return nil }}
	}
	refused("unordered", []OwnerSpec{{Owner: "core/batcave", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{add("core/batcave")}}}}, "unordered owner")
	refused("cycle", []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{add("wayne", StepRef{Owner: "robin", Version: 1})}}},
		{Owner: "robin", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{add("robin", StepRef{Owner: "wayne", Version: 1})}}},
	}, "cycle")
	refused("unknown", []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{add("wayne", StepRef{Owner: "robin", Version: 5})}}},
		{Owner: "robin", Enabled: true, Spec: Spec{Keyspace: true}},
	}, "unknown step")

	// Deferral: robin disabled, so core/auth#1 (After robin#1) waits, and the
	// owner does not serve; wayne's chain does not depend on robin and runs.
	ran = nil
	owners = dagOwners(&ran, false)
	e2 := newEnv(t, bare(owners)...)
	db2 := e2.open()
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
	e2.opts.Owners = owners
	db2 = e2.open()
	if want := []string{"core/rbac#1", "core/tasks#1", "core/tasks#2", "wayne#1"}; !slices.Equal(ran, want) {
		t.Fatalf("order with robin disabled = %v, want %v", ran, want)
	}
	if _, deferred := db2.Plan(); len(deferred) != 2 {
		t.Fatalf("deferred = %v, want robin#1 and core/auth#1", deferred)
	}
	if db2.Serves("core/auth") || !db2.Serves("wayne") {
		t.Fatal("serving barrier wrong")
	}
}

// TestMigrationFailureHaltsBoot (05-storage §5S.10, G2): a failing step
// halts boot with ErrMigrationFailed naming it; earlier steps stay
// committed, later ones never run, and the attempt's checkpoint stays open.
func TestMigrationFailureHaltsBoot(t *testing.T) {
	e := newEnv(t, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true})
	db := e.open()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the joker got in")
	ranThree := false
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		step(1, "one", "a", "1"),
		{Version: 2, Name: "two", Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
			if err := tx.Set([]byte("b"), []byte("2")); err != nil {
				return err
			}
			return boom
		}},
		{Version: 3, Name: "three", Compat: Additive, Up: func(context.Context, *MigrationTx) error { ranThree = true; return nil }},
	}}}}
	_, err := e.tryOpen()
	var mf *ErrMigrationFailed
	if !errors.As(err, &mf) || mf.Step.String() != "wayne#2" || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if ranThree {
		t.Fatal("a step after the failure ran")
	}
	if got := rawKeys(t, e.fs, e.path, "m/wayne/"); !slices.Equal(got, []string{"m/wayne/a"}) {
		t.Fatalf("rows = %v, want only step 1's", got)
	}
	ptr := readPointer(t, e)
	if ptr.State != "open" || ptr.Name == "" {
		t.Fatalf("checkpoint pointer = %+v, want an open checkpoint", ptr)
	}
}

func readPointer(t *testing.T, e *env) checkpointPointer {
	t.Helper()
	f, err := e.fs.Open(e.path + ".migration-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var p checkpointPointer
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestFreshInstallStampsLevelsAtomically (05-storage §5S.10): an empty store
// gets the marker, the store generation, every owner's stamp at the build's
// level, every step below each owner's lowest seed applied, `done` for every
// step a declared floor's Requires names, and the claim ledger, in one batch;
// a crash before it leaves an empty store.
func TestFreshInstallStampsLevelsAtomically(t *testing.T) {
	fs := vfs.NewCrashableMem()
	owners := []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 0, Migrations: []Migration{
			step(1, "cave", "cave", "1"),
			{Version: 2, Name: "names", Compat: Additive, Seed: true, Up: func(ctx context.Context, tx *MigrationTx) error {
				return tx.Set([]byte("name/bruce"), []byte("1"))
			}},
		}}},
		{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true}},
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims(), Level: 0, UpgradeFloor: &UpgradeFloor{
			Owner: "gotham", Requires: []StepRef{{Owner: "gotham", DataMigration: "m2"}}, InstallFirst: "r2",
		}}},
	}
	e := newEnvFS(t, fs, owners...)
	var clone *vfs.MemFS
	e.opts.hooks.beforeApply = crashOnce(fs, &clone)
	db := e.open()
	if got := rawKeys(t, clone, e.path, ""); len(got) != 0 {
		t.Fatalf("a crash before the fresh-install batch left %v", got)
	}
	ctx := context.Background()
	ks := db.storageKS()
	for _, k := range [][]byte{installKey(), epochKey(), levelKey("core"), levelKey("wayne"), levelKey("gotham"),
		versionKey("wayne"), appliedKey("wayne", 1), dmKey("gotham#m2"), claimsKey("gotham"), claimsKey("wayne")} {
		if ok, err := ks.Has(ctx, k); err != nil || !ok {
			t.Errorf("fresh install lacks %q (%v)", k, err)
		}
	}
	if ok, _ := ks.Has(ctx, appliedKey("wayne", 2)); !ok {
		t.Error("the seed step did not run in P4b")
	}
	if db.AppliedVersion("wayne") != 2 {
		t.Fatalf("wayne at %d, want 2", db.AppliedVersion("wayne"))
	}
	// The cave step was recorded, not run.
	wayne, _ := db.Keyspace("wayne")
	if ok, _ := wayne.Has(ctx, []byte("cave")); ok {
		t.Fatal("a step below the seed ran on a fresh install")
	}
	if l, ok := stamp(t, db, "wayne"); !ok || l != 0 {
		t.Fatalf("wayne's stamp = %d, %v", l, ok)
	}
	if name := db.CheckpointName(); name != "" {
		t.Fatalf("a fresh install took checkpoint %s", name)
	}
	// The floor passes at the next boot.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.hooks = testHooks{}
	e.open()
}

// TestFreshInstallRunsSeedSteps (05-storage §5S.10, COR5-8, COR6-4): seed
// steps run in P4b on a fresh install; the same step on a store that holds
// its row changes nothing; a later step that reshapes the seeded row
// converts it; Seed without Additive fails registration.
func TestFreshInstallRunsSeedSteps(t *testing.T) {
	ctx := context.Background()
	seed := Migration{Version: 1, Name: "greet", Compat: Additive, Seed: true, Up: func(ctx context.Context, tx *MigrationTx) error {
		if _, err := tx.Keyspace().Get(ctx, []byte("greeting")); !errors.Is(err, ErrNotFound) {
			return err
		}
		return tx.Set([]byte("greeting"), []byte("v1:world"))
	}}
	reshape := Migration{Version: 2, Name: "reshape", Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
		v, err := tx.Keyspace().Get(ctx, []byte("greeting"))
		if err != nil {
			return err
		}
		return tx.Set([]byte("greeting"), []byte("v2:"+strings.TrimPrefix(string(v), "v1:")))
	}}
	db := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{seed}}}).open()
	ks, _ := db.Keyspace("wayne")
	if v, _ := ks.Get(ctx, []byte("greeting")); string(v) != "v1:world" {
		t.Fatalf("fresh install greeting = %q", v)
	}
	// Upgraded store already holding the row: the seed leaves it.
	e := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
	db2 := e.open()
	ks2, _ := db2.Keyspace("wayne")
	if err := ks2.Set(ctx, []byte("greeting"), []byte("v1:bruce")); err != nil {
		t.Fatal(err)
	}
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{seed}}}}
	db2 = e.open()
	ks2, _ = db2.Keyspace("wayne")
	if v, _ := ks2.Get(ctx, []byte("greeting")); string(v) != "v1:bruce" {
		t.Fatalf("seed changed an existing row: %q", v)
	}
	// Reshape on a fresh install.
	db3 := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{seed, reshape}}}).open()
	ks3, _ := db3.Keyspace("wayne")
	if v, _ := ks3.Get(ctx, []byte("greeting")); string(v) != "v2:world" || db3.AppliedVersion("wayne") != 2 {
		t.Fatalf("reshaped fresh install: %q at version %d", v, db3.AppliedVersion("wayne"))
	}
	bad := seed
	bad.Compat, bad.RaisesLevel = Breaking, 1
	_, err := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{bad}}}).tryOpen()
	mustErrIs(t, err, ErrInvalidSpec)
}

// rawSet writes one key into a closed store.
func rawSet(t *testing.T, e *env, k string, v []byte) {
	t.Helper()
	db, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, Logger: quiet{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte(k), v, cpebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestUnreadableVersionFailsClosed (05-storage §5S.10): a version record that
// cannot be read refuses boot (never "assume 0", contrast ABO's
// migrations.go:582), and so does a version source that fails.
func TestUnreadableVersionFailsClosed(t *testing.T) {
	owners := []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step(1, "one", "a", "1")}}}}
	e := newEnv(t, owners...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	rawSet(t, e, "c/storage/"+string(versionKey("wayne")), []byte{1, 2, 3})
	_, err := e.tryOpen()
	mustErrIs(t, err, ErrBookkeepingUnreadable)

	vs := &fakeVersionSource{err: errors.New("riddle me this")}
	_, err = newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamVSClaims(), VersionSource: vs}}).tryOpen()
	mustErrIs(t, err, ErrBookkeepingUnreadable)
}

// TestOlderBuildAcceptsNewerAdditiveSteps (05-storage §5S.10): an older build
// of the same line boots a store a newer build stepped through Additive
// steps it does not know, with a warning.
func TestOlderBuildAcceptsNewerAdditiveSteps(t *testing.T) {
	newer := []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{
		step(1, "one", "a", "1"), step(2, "two", "b", "2"),
	}}}}
	e := newEnv(t, newer...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step(1, "one", "a", "1")}}}}
	e.open()
}

// TestOlderBuildRefusesNewerBreakingStep (05-storage §5S.10): an older build
// refuses a store a newer build migrated through a Breaking step above its
// supported level: ErrKeyspaceTooNew (with the guard's LevelTooNewError),
// and nothing is written.
func TestOlderBuildRefusesNewerBreakingStep(t *testing.T) {
	newer := []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 2, Migrations: []Migration{
		step(1, "one", "a", "1"),
		{Version: 2, Name: "reencode", Compat: Breaking, RaisesLevel: 2, Up: func(context.Context, *MigrationTx) error { return nil }},
	}}}}
	e := newEnv(t, newer...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	before := rawKeys(t, e.fs, e.path, "")
	e.opts.Owners = []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{step(1, "one", "a", "1")}}}}
	_, err := e.tryOpen()
	mustErrIs(t, err, ErrKeyspaceTooNew)
	var lt *LevelTooNewError
	if !errors.As(err, &lt) || lt.Owner != "wayne" || lt.Store != 2 || lt.Supported != 1 {
		t.Fatalf("guard refusal = %v", err)
	}
	if after := rawKeys(t, e.fs, e.path, ""); !slices.Equal(before, after) {
		t.Fatal("a refused boot wrote to the store")
	}
}

// fakeVersionSource keeps an owner's bookkeeping in one row of its claims,
// in the shape of HEAD's migration-version rows: records without a compat
// field are HEAD's.
type fakeVersionSource struct{ err error }

const vsKey = "joker_migrations"

type vsRow struct {
	Version int `json:"version"`
	Records []struct {
		V           int     `json:"v"`
		Name        string  `json:"name"`
		Compat      *string `json:"compat,omitempty"`
		RaisesLevel int     `json:"raises_level,omitempty"`
	} `json:"records"`
}

func (f *fakeVersionSource) read(ctx context.Context, r LegacyRange) (vsRow, error) {
	var row vsRow
	raw, err := r.Get(ctx, []byte(vsKey))
	if errors.Is(err, ErrNotFound) {
		return row, nil
	}
	if err != nil {
		return row, err
	}
	return row, json.Unmarshal(raw, &row)
}

func (f *fakeVersionSource) Applied(ctx context.Context, r LegacyRange) (int, map[int]AppliedStep, error) {
	if f.err != nil {
		return 0, nil, f.err
	}
	row, err := f.read(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	recs := map[int]AppliedStep{}
	for _, rr := range row.Records {
		s := AppliedStep{Version: rr.V, Name: rr.Name, RaisesLevel: rr.RaisesLevel}
		if rr.Compat != nil {
			s.Compat, s.HasCompat = parseCompat(*rr.Compat), true
		}
		recs[rr.V] = s
	}
	return row.Version, recs, nil
}

func (f *fakeVersionSource) Stage(ctx context.Context, r LegacyRange, b *Batch, s AppliedStep) error {
	row, err := f.read(ctx, r)
	if err != nil {
		return err
	}
	c := s.Compat.String()
	row.Version = s.Version
	row.Records = append(row.Records, struct {
		V           int     `json:"v"`
		Name        string  `json:"name"`
		Compat      *string `json:"compat,omitempty"`
		RaisesLevel int     `json:"raises_level,omitempty"`
	}{V: s.Version, Name: s.Name, Compat: &c, RaisesLevel: s.RaisesLevel})
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return b.In(r).Set([]byte(vsKey), raw)
}

func gothamVSClaims() []Claim {
	return append(gothamClaims(), Claim{Key: vsKey, Mode: ClaimReadWrite})
}

// TestCompatlessBooksRecordIsBreaking (05-storage §5S.10): an applied record
// with no compat field (HEAD's) counts as Breaking, so one whose
// raises_level is above the build's level is refused like a Breaking one;
// the same record marked Additive only warns.
func TestCompatlessBooksRecordIsBreaking(t *testing.T) {
	for _, compat := range []string{"", "additive"} {
		t.Run("compat="+compat, func(t *testing.T) {
			spec := Spec{Claims: gothamVSClaims(), Level: 1, VersionSource: &fakeVersionSource{}, Migrations: []Migration{
				{Version: 1, Name: "one", Compat: Additive, Up: func(context.Context, *MigrationTx) error { return nil }},
			}}
			e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: spec})
			rec := fmt.Sprintf(`{"v":2,"name":"two","raises_level":2%s}`, map[string]string{"": "", "additive": `,"compat":"additive"`}[compat])
			e.seedRaw(vsKey, `{"version":2,"records":[{"v":1,"name":"one","compat":"additive"},`+rec+`]}`)
			_, err := e.tryOpen()
			if compat == "" {
				mustErrIs(t, err, ErrKeyspaceTooNew)
			} else if err != nil {
				t.Fatalf("an Additive unknown step refused boot: %v", err)
			}
		})
	}
}

// TestBreakingStepRequiresRaisesLevel (05-storage §5S.10): a Breaking step
// without RaisesLevel, or raising above the build's Level, fails
// registration.
func TestBreakingStepRequiresRaisesLevel(t *testing.T) {
	noop := func(context.Context, *MigrationTx) error { return nil }
	for name, m := range map[string]Migration{
		"no raise":     {Version: 1, Name: "x", Compat: Breaking, Up: noop},
		"above level":  {Version: 1, Name: "x", Compat: Breaking, RaisesLevel: 3, Up: noop},
		"zero compat":  {Version: 1, Name: "x", Up: noop},
		"additive lvl": {Version: 1, Name: "x", Compat: Additive, RaisesLevel: 1, Up: noop},
	} {
		_, err := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{m}}}).tryOpen()
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v, want ErrInvalidSpec", name, err)
		}
	}
}

// TestStepCannotCommitOutsideItsBatch (05-storage §5S.3.5 "Atomicity";
// review MAJOR-1, probe D): a step's handles read and write through its one
// batch. A batch of their own, a Sub's batch, a sequence or a legacy batch
// would commit outside it, and a snapshot would not see the step's writes,
// so each is refused; a failing step then leaves no row behind.
func TestStepCannotCommitOutsideItsBatch(t *testing.T) {
	gotham := func(migs ...Migration) OwnerSpec {
		return OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Keyspace: true, Claims: gothamClaims(), Migrations: migs}}
	}
	e := newEnv(t, gotham(step(1, "cave", "cave", "1")))
	tried := map[string]error{}
	commit := func(b *Batch, key string) error {
		defer func() { _ = b.Close() }()
		if err := b.Set([]byte(key), []byte("escaped")); err != nil {
			return err
		}
		return b.Commit(context.Background(), Sync)
	}
	escape := Migration{Version: 2, Name: "escape", Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
		ks, lr := tx.Keyspace(), tx.Legacy()
		tried["NewBatch"] = commit(ks.NewBatch(), "joker")
		tried["NewIndexedBatch"] = commit(ks.NewIndexedBatch(), "harley")
		tried["Sub.NewBatch"] = commit(ks.Sub("cowl").NewBatch(), "penguin")
		_, tried["Sequence"] = ks.Sequence("ids").Next(ctx)
		tried["Legacy.NewBatch"] = commit(lr.NewBatch(), "joker:escaped")
		if s, err := ks.Snapshot(); err == nil {
			_ = s.Close()
		} else {
			tried["Snapshot"] = err
		}
		if s, err := lr.Snapshot(); err == nil {
			_ = s.Close()
		} else {
			tried["Legacy.Snapshot"] = err
		}
		return errors.New("the step fails after trying")
	}}
	upgrade(t, e, gotham(step(1, "cave", "cave", "1"), escape))
	if _, err := e.tryOpen(); err == nil {
		t.Fatal("the failing step's boot succeeded")
	}
	for _, name := range []string{"NewBatch", "NewIndexedBatch", "Sub.NewBatch", "Sequence", "Legacy.NewBatch"} {
		if !errors.Is(tried[name], errTxKeyspaceWrite) {
			t.Errorf("%s inside a step = %v, want errTxKeyspaceWrite", name, tried[name])
		}
	}
	for _, name := range []string{"Snapshot", "Legacy.Snapshot"} {
		if !errors.Is(tried[name], errTxSnapshot) {
			t.Errorf("%s inside a step = %v, want errTxSnapshot", name, tried[name])
		}
	}
	for _, prefix := range []string{"m/gotham/joker", "m/gotham/harley", "m/gotham/cowl/", "m/gotham/_seq", "joker:escaped"} {
		if got := rawKeys(t, e.fs, e.path, prefix); len(got) != 0 {
			t.Errorf("the failed step left %v behind", got)
		}
	}
}
