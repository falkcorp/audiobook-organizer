// file: core/internal/storagemigrate/runner_test.go
// version: 1.0.0
// guid: f7ed22ee-8a79-4b95-9d02-ccbdad75c65c
// last-edited: 2026-11-10

package storagemigrate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// seedItems writes rows item rows of value "0" on fs (a fresh install of
// owners first).
func seedItems(t testing.TB, fs vfs.FS, owners []storage.OwnerSpec, rows int) {
	t.Helper()
	e := &env{t: t.(*testing.T), owners: owners}
	db, err := e.bootFS(context.Background(), fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	ks, _ := db.Keyspace("gotham")
	b := ks.NewBatch()
	for n := 1; n <= rows; n++ {
		if err := b.Set(item(n), []byte("0")); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(b.Commit(context.Background(), storage.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
}

// TestDataMigrationResumesExactly (05-storage §5S.10): the census runs the
// recount on a plan of 3 shards over 3 workers; every kill point of its
// chunks, every chunk boundary and a point inside every chunk is rebooted
// on a build whose parallelism is now 7 workers and whose new plans would
// have 7 shards. Each resume reuses the persisted 3-shard plan, and every
// item reads exactly 1: none processed twice, none skipped.
func TestDataMigrationResumesExactly(t *testing.T) {
	const rows = 900
	e := &env{t: t, owners: []storage.OwnerSpec{gotham()}}
	resumes := atomic.Int64{}
	s := crashmatrix.ChunkScenario{
		Path:       storePath,
		Migrations: []datamigrate.DataMigration{recount(tasks.Fixed(3, "test: three workers at first"))},
		Seed:       func(t testing.TB, fs vfs.FS) { seedItems(t, fs, e.owners, rows) },
		Boot: func(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error {
			shards := 3
			if bootseam.From(ctx) == nil { // a reboot after a kill: more CPUs now
				shards = 7
				migs = slices.Clone(migs)
				migs[0].Parallelism = tasks.Fixed(7, "test: seven workers on resume")
				resumes.Add(1)
			}
			r := e.runner(storagemigrate.Options{ChunkItems: 40, PlanShards: func(tasks.Parallelism) int { return shards }}, migs...)
			db, err := e.bootFS(ctx, fs, r)
			if db != nil {
				err = errors.Join(err, db.Close())
			}
			return err
		},
		Check: func(t testing.TB, fs vfs.FS) {
			ks, sks := e.view(fs)
			got := values(t.(*testing.T), ks)
			for n := 1; n <= rows; n++ {
				if got[n] != "1" {
					t.Errorf("item %d = %q after the resume, want 1 (processed exactly once)", n, got[n])
				}
			}
			if len(got) != rows {
				t.Errorf("%d items, want %d", len(got), rows)
			}
			shards, _, found, err := storagemigrate.PlanOf(context.Background(), sks, "gotham.recount")
			if err != nil || !found || shards != 3 {
				t.Errorf("plan = %d shards (found %v, %v), want the first start's 3", shards, found, err)
			}
			if st := state(t.(*testing.T), sks, "gotham.recount"); st != datamigrate.StateDone {
				t.Errorf("state %q, want done", st)
			}
		},
		Labels: []string{storagemigrate.LabelChunk},
		Inside: true,
	}
	rep := crashmatrix.RunChunks(t, s)
	if rep.ChunkStarts < 900/40 || resumes.Load() == 0 {
		t.Fatalf("%d chunk boundaries broken, %d resumes; want every chunk's", rep.ChunkStarts, resumes.Load())
	}
}

// TestDataMigrationShardsDisjoint (05-storage §5S.10, run with -race): a
// CPU() migration over 8 planned shards steps every item exactly once, in
// parallel, and writes each exactly once; the race detector sees no shared
// mutable state between the workers.
func TestDataMigrationShardsDisjoint(t *testing.T) {
	const rows = 2000
	e := newEnv(t, rows)
	var mu sync.Mutex
	steps := map[string]int{}
	var running, peak atomic.Int64
	m := recount(tasks.CPU())
	inner := m.Step
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		n := running.Add(1)
		defer running.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		mu.Lock()
		steps[string(kv.Key)]++
		mu.Unlock()
		runtime.Gosched()
		return inner(ctx, kv, w)
	}
	r := e.runner(storagemigrate.Options{ChunkItems: 64, PlanShards: func(tasks.Parallelism) int { return 8 }}, m)
	db, err := e.boot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.Report(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= rows; n++ {
		if c := steps[string(item(n))]; c != 1 {
			t.Errorf("item %d stepped %d times, want 1", n, c)
		}
	}
	got := values(t, func() storage.Keyspace { ks, _ := e.view(e.fs); return ks }())
	for n := 1; n <= rows; n++ {
		if got[n] != "1" {
			t.Errorf("item %d = %q, want 1", n, got[n])
		}
	}
	if len(stats) != 1 || stats[0].Shards != 8 || stats[0].Items["converted"] != rows || stats[0].State != datamigrate.StateDone {
		t.Fatalf("stats = %+v, want 8 shards, %d converted, done", stats, rows)
	}
	if runtime.NumCPU() > 1 && peak.Load() < 2 {
		t.Fatalf("at most %d step ran at once on %d CPUs: the shards did not run in parallel", peak.Load(), runtime.NumCPU())
	}
}

// TestDataMigrationVerifyBlocksBarrier (05-storage §5S.10): a failed Verify
// leaves state failed with its report, fails the boot (ErrMigrationFailed
// wrapping the step's StepError) with the checkpoint open, and the owner's
// Barrier version does not run, on that boot or the next; once Verify
// passes, the Barrier version runs and no item is stepped again.
func TestDataMigrationVerifyBlocksBarrier(t *testing.T) {
	const rows = 120
	e := newEnv(t, rows)
	var manorRan atomic.Bool
	manor := storage.Migration{Version: 2, Name: "wayne-manor", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { manorRan.Store(true); return nil }}
	e.owners = []storage.OwnerSpec{gotham(manor)}
	broken := true
	m := recount(sequential())
	m.Barrier = 2
	m.Verify = func(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
		if broken {
			return datamigrate.VerifyReport{Read: rows, Failures: []string{"the Riddler's case file does not add up"}}, nil
		}
		return datamigrate.VerifyReport{Read: rows}, nil
	}
	for boot := 1; boot <= 2; boot++ {
		r := e.runner(storagemigrate.Options{ChunkItems: 50}, m)
		db, err := e.boot(context.Background(), r)
		var mf *storage.ErrMigrationFailed
		var se *storage.StepError
		if !errors.As(err, &mf) || !errors.As(err, &se) || !errors.Is(err, storagemigrate.ErrVerifyFailed) || mf.Step.DataMigration != "gotham.recount" {
			t.Fatalf("boot %d = %v, want ErrMigrationFailed with the step's ErrVerifyFailed", boot, err)
		}
		if v := db.AppliedVersion("gotham"); v != 1 || manorRan.Load() {
			t.Fatalf("boot %d: gotham at version %d (manor ran %v): the Barrier version ran past a failed Verify", boot, v, manorRan.Load())
		}
		if open, _, err := db.Admin().CheckpointBytes(); err != nil || open == 0 {
			t.Fatalf("boot %d: open checkpoint bytes = %d, %v; want the attempt's checkpoint open", boot, open, err)
		}
		sks, _ := db.Keyspace("core/storage")
		if st := state(t, sks, "gotham.recount"); st != datamigrate.StateFailed {
			t.Fatalf("boot %d: state %q, want failed", boot, st)
		}
		if st, _, fails, ok, err := storagemigrate.ReportOf(context.Background(), sks, "gotham.recount"); err != nil || !ok || st != datamigrate.StateFailed || len(fails) != 1 {
			t.Fatalf("boot %d: report = %q %v %v %v, want the failed report kept", boot, st, fails, ok, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	broken = false
	e.mustBoot(e.runner(storagemigrate.Options{ChunkItems: 50}, m))
	if !manorRan.Load() {
		t.Fatal("the Barrier version did not run once Verify passed")
	}
	ks, sks := e.view(e.fs)
	if st := state(t, sks, "gotham.recount"); st != datamigrate.StateDone {
		t.Fatalf("state %q, want done", st)
	}
	for n, v := range values(t, ks) {
		if v != "1" {
			t.Fatalf("item %d = %q: a re-verify stepped items again", n, v)
		}
	}
}

// copier writes, for each item, a copy key just after it (in the shard's
// unvisited part) or, with cross, into another part of the source; it
// records every copy it is asked to step, which must be none.
func copier(par tasks.Parallelism, inPlace, cross bool, steppedCopies *atomic.Int64) datamigrate.DataMigration {
	m := recount(par)
	m.ID, m.RewritesInPlace = "gotham.copy", inPlace
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if bytes.HasSuffix(kv.Key, []byte("-copy")) {
			steppedCopies.Add(1)
			return nil
		}
		var n int
		if _, err := fmt.Sscanf(string(kv.Key), "item/%05d", &n); err != nil {
			return err
		}
		target := n
		if cross {
			target = (n+50)%100 + 1
		}
		if err := w.Set([]byte(fmt.Sprintf("item/%05d-copy", target)), []byte("copy")); err != nil {
			return err
		}
		return w.Set(kv.Key, []byte("1"))
	}
	return m
}

// TestDataMigrationRewritesInPlaceNotRevisited (05-storage §5S.10, runner
// rule 5): a RewritesInPlace migration whose writes land in the unvisited
// part of the source never steps its own writes, though every chunk
// re-opens its iterator after the previous commit; without RewritesInPlace
// such a write is refused and nothing of the chunk commits.
func TestDataMigrationRewritesInPlaceNotRevisited(t *testing.T) {
	cases := []struct {
		name  string
		par   tasks.Parallelism
		cross bool
	}{
		{"own shard", sequential(), false},
		{"other shards", tasks.Fixed(4, "test: four shards"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, 100)
			var stepped atomic.Int64
			r := e.runner(storagemigrate.Options{ChunkItems: 7, PlanShards: func(tasks.Parallelism) int { return 4 }}, copier(c.par, true, c.cross, &stepped))
			e.mustBoot(r)
			if n := stepped.Load(); n != 0 {
				t.Fatalf("the migration stepped %d of its own writes", n)
			}
			ks, _ := e.view(e.fs)
			copies := 0
			for kv, err := range ks.Scan(context.Background(), storage.ScanOptions{Start: []byte("item/"), End: []byte("item0")}) {
				if err != nil {
					t.Fatal(err)
				}
				if bytes.HasSuffix(kv.Key, []byte("-copy")) {
					copies++
				} else if string(kv.Value) != "1" {
					t.Fatalf("%s = %q, want 1", kv.Key, kv.Value)
				}
			}
			if copies != 100 {
				t.Fatalf("%d copies, want 100", copies)
			}
		})
	}
	t.Run("refused without RewritesInPlace", func(t *testing.T) {
		e := newEnv(t, 100)
		var stepped atomic.Int64
		r := e.runner(storagemigrate.Options{ChunkItems: 7}, copier(sequential(), false, false, &stepped))
		db, err := e.boot(context.Background(), r)
		var se *storage.StepError
		if !errors.Is(err, datamigrate.ErrUnvisitedWrite) || !errors.As(err, &se) {
			t.Fatalf("boot = %v, want the step's ErrUnvisitedWrite", err)
		}
		sks, _ := db.Keyspace("core/storage")
		if _, ok, err := storagemigrate.CursorOf(context.Background(), sks, "gotham.copy", 0); ok || err != nil {
			t.Fatalf("a cursor committed (%v, %v): the refused chunk committed", ok, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		ks, _ := e.view(e.fs)
		for n, v := range values(t, ks) {
			if v != "0" {
				t.Fatalf("item %d = %q after the refused chunk, want 0", n, v)
			}
		}
	})
}

// TestDataMigration_UnregisteredDoneRecordIgnored (runner rule 2): a done
// record whose ID the build no longer registers is history: the next build
// neither runs it nor fails on it, and its other migrations run.
func TestDataMigration_UnregisteredDoneRecordIgnored(t *testing.T) {
	e := newEnv(t, 30)
	old := recount(sequential())
	old.ID = "gotham.signal"
	e.mustBoot(e.runner(storagemigrate.Options{}, old))
	ran := atomic.Int64{}
	next := recount(sequential())
	inner := next.Step
	next.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		ran.Add(1)
		return inner(ctx, kv, w)
	}
	r := e.runner(storagemigrate.Options{}, next)
	db, err := e.boot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.Report(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ID != "gotham.recount" || ran.Load() != 30 {
		t.Fatalf("stats = %+v after %d steps, want only gotham.recount, run over 30 items", stats, ran.Load())
	}
	e.mustBoot(nil) // a build with no data migration at all
	ks, sks := e.view(e.fs)
	if st := state(t, sks, "gotham.signal"); st != datamigrate.StateDone {
		t.Fatalf("the retired step's record is %q, want it kept done", st)
	}
	for n, v := range values(t, ks) {
		if v != "2" {
			t.Fatalf("item %d = %q, want 2 (each migration once)", n, v)
		}
	}
}

// TestDataMigrationHoldListed (runner rule 8): a held item is recorded with
// its reason in the chunk's batch and counted, never dropped.
func TestDataMigrationHoldListed(t *testing.T) {
	e := newEnv(t, 50)
	m := recount(sequential())
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if bytes.HasSuffix(kv.Key, []byte("0")) {
			w.Hold(kv.Key, "Two-Face flipped the coin")
			return nil
		}
		w.Count("converted")
		return w.Set(kv.Key, []byte("1"))
	}
	r := e.runner(storagemigrate.Options{ChunkItems: 8}, m)
	db, err := e.boot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.Report(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := stats[0].Items; got[datamigrate.OutcomeHeld] != 5 || got["converted"] != 45 {
		t.Fatalf("counts = %v, want 5 held and 45 converted", got)
	}
	_, sks := e.view(e.fs)
	var held []string
	prefix := storagemigrate.HeldPrefix("gotham.recount")
	for kv, err := range sks.ScanPrefix(context.Background(), prefix) {
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(kv.Value), "Two-Face") {
			t.Fatalf("held record %q lost its reason", kv.Value)
		}
		held = append(held, string(kv.Key))
	}
	if len(held) != 5 {
		t.Fatalf("%d held records, want 5", len(held))
	}
}

// TestDataMigrationCancelStopsAtChunkBoundary (runner rule 11): a boot
// cancelled mid-chunk commits nothing of that chunk and fails nothing; the
// next boot resumes after the last committed chunk.
func TestDataMigrationCancelStopsAtChunkBoundary(t *testing.T) {
	e := newEnv(t, 100)
	ctx, cancel := context.WithCancel(context.Background())
	m := recount(sequential())
	inner := m.Step
	var n atomic.Int64
	m.Step = func(c context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if n.Add(1) == 25 {
			cancel()
		}
		return inner(c, kv, w)
	}
	r := e.runner(storagemigrate.Options{ChunkItems: 10}, m)
	db, err := e.boot(ctx, r)
	var se *storage.StepError
	if !errors.Is(err, context.Canceled) || errors.As(err, &se) {
		t.Fatalf("boot = %v, want the cancellation, not a step failure", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ks, _ := e.view(e.fs)
	got := values(t, ks)
	for i := 1; i <= 100; i++ {
		if want := map[bool]string{true: "1", false: "0"}[i <= 20]; got[i] != want {
			t.Fatalf("item %d = %q after the cancel, want %q (the chunk in progress dropped whole)", i, got[i], want)
		}
	}
	e.mustBoot(e.runner(storagemigrate.Options{ChunkItems: 10}, recount(sequential())))
	ks, _ = e.view(e.fs)
	for i, v := range values(t, ks) {
		if v != "1" {
			t.Fatalf("item %d = %q after the resume, want 1", i, v)
		}
	}
}

// TestDataMigrationStepFailure: a Step error or panic fails the boot with
// the step's StepError and commits nothing of its chunk; a fixed build
// resumes from the last committed chunk.
func TestDataMigrationStepFailure(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			e := newEnv(t, 40)
			m := recount(sequential())
			inner := m.Step
			m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				if string(kv.Key) == string(item(23)) {
					if panics {
						panic("the Joker's gas")
					}
					return errors.New("the Joker's gas")
				}
				return inner(ctx, kv, w)
			}
			db, err := e.boot(context.Background(), e.runner(storagemigrate.Options{ChunkItems: 10}, m))
			var se *storage.StepError
			if !errors.As(err, &se) || se.Step.DataMigration != "gotham.recount" || !strings.Contains(err.Error(), "Joker") {
				t.Fatalf("boot = %v, want the step's StepError", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			e.mustBoot(e.runner(storagemigrate.Options{ChunkItems: 10}, recount(sequential())))
			ks, _ := e.view(e.fs)
			for i, v := range values(t, ks) {
				if v != "1" {
					t.Fatalf("item %d = %q, want 1", i, v)
				}
			}
		})
	}
}

// TestDataMigrationLevelStep: a migration that raises its owner's level
// above the store's stamp needs the kernel's level step; without one the
// boot fails before it writes anything.
func TestDataMigrationLevelStep(t *testing.T) {
	e := newEnv(t, 10) // stamped at level 0
	level1 := gotham()
	level1.Spec.Level = 1
	e.owners = []storage.OwnerSpec{level1}
	m := recount(sequential())
	m.Compat, m.RaisesLevel = storage.Breaking, 1
	db, err := e.boot(context.Background(), e.runner(storagemigrate.Options{}, m))
	if !errors.Is(err, datamigrate.ErrLevelStepNotWired) {
		t.Fatalf("boot = %v, want ErrLevelStepNotWired", err)
	}
	sks, _ := db.Keyspace("core/storage")
	if st := state(t, sks, "gotham.recount"); st != "" {
		t.Fatalf("state %q written before the level step", st)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var raised []string
	e.mustBoot(e.runner(storagemigrate.Options{LevelStep: func(_ context.Context, _ *storage.DB, o storage.Owner, level int) error {
		raised = append(raised, fmt.Sprintf("%s:%d", o, level))
		return nil
	}}, m))
	if !slices.Equal(raised, []string{"gotham:1"}) {
		t.Fatalf("level step calls = %v, want gotham:1", raised)
	}
}

// TestDataMigrationReportStates: Report gives a migration storage deferred
// this boot (its owner disabled) as held, and a migration not yet started
// as pending; ExtendStart is called at the step's start and every group of
// chunks.
func TestDataMigrationReportStates(t *testing.T) {
	e := newEnv(t, 64)
	disabled := gotham()
	disabled.Enabled = false
	e.owners = []storage.OwnerSpec{disabled}
	r := e.runner(storagemigrate.Options{}, recount(sequential()))
	db, err := e.boot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.Report(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].State != datamigrate.StateHeld || stats[0].Ran {
		t.Fatalf("stats = %+v, want held and not run", stats)
	}
	e.owners = []storage.OwnerSpec{gotham()}
	var calls []string
	r = e.runner(storagemigrate.Options{ChunkItems: 4, ExtendEvery: 4, ExtendStart: func(s string) { calls = append(calls, s) }}, recount(sequential()))
	e.mustBoot(r)
	// 16 chunks: the start, then chunks 4, 8, 12 and 16.
	if len(calls) != 5 || !strings.Contains(calls[0], "gotham.recount") {
		t.Fatalf("ExtendStart calls = %q, want the start and every 4th chunk", calls)
	}
	r2 := e.runner(storagemigrate.Options{}, recount(sequential()), func() datamigrate.DataMigration {
		m := recount(sequential())
		m.ID = "gotham.later"
		return m
	}())
	db, err = e.boot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = r2.Report(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if stats[0].State != datamigrate.StateDone || stats[1].State != datamigrate.StatePending {
		t.Fatalf("stats = %+v, want done then pending", stats)
	}
}

// TestDataMigrationChunkBytes (runner rules 3 and 4): chunk_bytes bounds a
// chunk on both sides. A chunk reads at most ChunkBytes of source pairs
// past its first, and it commits before its next item once its batch
// reaches ChunkBytes; either way every item is stepped exactly once, so the
// cursor never passes an item its chunk did not step.
func TestDataMigrationChunkBytes(t *testing.T) {
	const rows = 40
	t.Run("read side", func(t *testing.T) {
		e := newEnv(t, rows)
		db, err := e.boot(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ks, _ := db.Keyspace("gotham")
		b := ks.NewBatch()
		for n := 1; n <= rows; n++ {
			// 10-byte key + 1014-byte value: four rows reach 4 KiB.
			if err := b.Set(item(n), bytes.Repeat([]byte("j"), 1014)); err != nil {
				t.Fatal(err)
			}
		}
		if err := errors.Join(b.Commit(context.Background(), storage.Sync), db.Close()); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		steps := map[string]int{}
		m := recount(sequential())
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			mu.Lock()
			steps[string(kv.Key)]++
			mu.Unlock()
			w.Count("seen")
			return nil
		}
		r := e.runner(storagemigrate.Options{ChunkBytes: 4096, ChunkItems: 1000}, m)
		db, err = e.boot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := r.Report(context.Background(), db)
		if err := errors.Join(err, db.Close()); err != nil {
			t.Fatal(err)
		}
		for n := 1; n <= rows; n++ {
			if c := steps[string(item(n))]; c != 1 {
				t.Fatalf("item %d stepped %d times, want 1", n, c)
			}
		}
		if len(stats) != 1 || stats[0].Chunks != rows/4 {
			t.Fatalf("stats = %+v, want %d chunks of four 1 KiB rows", stats, rows/4)
		}
	})
	t.Run("write side", func(t *testing.T) {
		e := newEnv(t, rows)
		m := recount(sequential())
		inner := m.Step
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			// A 1 KiB note outside the source per item: the batch, not the
			// read, reaches ChunkBytes.
			if err := w.Set(append([]byte("note/"), kv.Key...), bytes.Repeat([]byte("n"), 1024)); err != nil {
				return err
			}
			return inner(ctx, kv, w)
		}
		r := e.runner(storagemigrate.Options{ChunkBytes: 4096, ChunkItems: 1000}, m)
		db, err := e.boot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := r.Report(context.Background(), db)
		if err := errors.Join(err, db.Close()); err != nil {
			t.Fatal(err)
		}
		ks, _ := e.view(e.fs)
		got := values(t, ks)
		for n := 1; n <= rows; n++ {
			if got[n] != "1" {
				t.Fatalf("item %d = %q, want 1 (stepped once; a cursor past it skips it)", n, got[n])
			}
		}
		if len(stats) != 1 || stats[0].Chunks < rows/5 {
			t.Fatalf("stats = %+v, want at least %d chunks (the batch closes each near 4 KiB)", stats, rows/5)
		}
	})
}

// TestDataMigrationDoneNeverReentered (runner rule 2, DATA2-15): Run on a
// migration whose record is done steps nothing, verifies nothing and
// rewrites nothing, even when called directly. Storage never calls a done
// step; this is the runner's own guard.
func TestDataMigrationDoneNeverReentered(t *testing.T) {
	e := newEnv(t, 30)
	var stepped, verified atomic.Int64
	m := recount(sequential())
	inner := m.Step
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		stepped.Add(1)
		return inner(ctx, kv, w)
	}
	m.Verify = func(context.Context, storage.Reader) (datamigrate.VerifyReport, error) {
		verified.Add(1)
		return datamigrate.VerifyReport{}, nil
	}
	e.mustBoot(e.runner(storagemigrate.Options{}, m))
	if stepped.Load() != 30 || verified.Load() != 1 {
		t.Fatalf("first boot stepped %d and verified %d times, want 30 and 1", stepped.Load(), verified.Load())
	}
	ctx := context.Background()
	db, err := e.boot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sks, _ := db.Keyspace("core/storage")
	before, err := sks.Get(ctx, storage.DataMigrationKey(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runner(storagemigrate.Options{}, m).Run(ctx, db, m.ID); err != nil {
		t.Fatalf("Run on a done migration = %v", err)
	}
	after, err := sks.Get(ctx, storage.DataMigrationKey(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stepped.Load() != 30 || verified.Load() != 1 || !bytes.Equal(before, after) {
		t.Fatalf("Run on a done migration stepped %d, verified %d times, record rewritten %v; want 30, 1, false",
			stepped.Load(), verified.Load(), !bytes.Equal(before, after))
	}
}

// TestDataMigrationVerifyStoppedFailsNothing: a Verify cut short because the
// boot is stopping fails nothing. The boot returns the stop's cause, not a
// StepError; the record stays verifying with no report; and the next boot
// verifies again and records done without stepping an item twice.
func TestDataMigrationVerifyStoppedFailsNothing(t *testing.T) {
	e := newEnv(t, 20)
	stop := errors.New("Alfred pulled the Batcave's breaker")
	ctx, cancel := context.WithCancelCause(context.Background())
	m := recount(sequential())
	m.Verify = func(c context.Context, _ storage.Reader) (datamigrate.VerifyReport, error) {
		cancel(stop)
		return datamigrate.VerifyReport{}, c.Err()
	}
	db, err := e.boot(ctx, e.runner(storagemigrate.Options{}, m))
	var se *storage.StepError
	if !errors.Is(err, stop) || errors.As(err, &se) {
		t.Fatalf("boot = %v, want the stop's cause, not a step failure", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, sks := e.view(e.fs)
	if st := state(t, sks, m.ID); st != datamigrate.StateVerifying {
		t.Fatalf("state after a stopped Verify = %q, want verifying", st)
	}
	if _, _, _, found, err := storagemigrate.ReportOf(context.Background(), sks, m.ID); found || err != nil {
		t.Fatalf("a stopped Verify left a report (%v, %v)", found, err)
	}
	m.Verify = nil
	e.mustBoot(e.runner(storagemigrate.Options{}, m))
	ks, sks := e.view(e.fs)
	if st := state(t, sks, m.ID); st != datamigrate.StateDone {
		t.Fatalf("state after the next boot = %q, want done", st)
	}
	for n, v := range values(t, ks) {
		if v != "1" {
			t.Fatalf("item %d = %q, want 1", n, v)
		}
	}
}
