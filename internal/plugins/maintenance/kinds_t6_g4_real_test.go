// file: internal/plugins/maintenance/kinds_t6_g4_real_test.go
// version: 1.0.4
// guid: 075c2d47-deca-45e6-8742-449c0093b857
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// The recorder fakes in kinds_t6_g4_test.go answer every read with a zero value,
// so a core run against them writes nothing and proves only the NewWriter
// handoff. The scenarios here put a REAL in-memory store behind both shells: the
// native one reads through headT6G4Store and writes through a spyT6G4Writer that
// records each call and then performs it, so a test asserts the resources the
// run recorded (equal to the kind's expected set, not merely a subset of what it
// declares) and the rows that changed.

var _ t6g4Writer = spyT6G4Writer{}

// t6g4RealDeps is a t6g4Deps over p's own deps: reads through headT6G4Store,
// writes through a spy over headT6G4Writer. handed collects the tasks.Writer each
// NewWriter call received.
func t6g4RealDeps(p *Plugin, rec *t6g4Calls, handed *[]tasks.Writer) t6g4Deps {
	store := p.deps.OpsStore()
	prov := p.deps.FileProvenanceStore()
	return t6g4Deps{
		Store: headT6G4Store{OpsStore: store, deps: p.deps, prov: prov},
		// fs-regroup's live apply parks the scan through the same service HEAD's shell
		// uses: the plugin's own deps.
		Scan: p.deps,
		// The stats keys are written outside the spy on purpose (t6g4StatsSink).
		Stats: headT6G4Stats{store: store},
		NewWriter: func(w tasks.Writer) t6g4Writer {
			*handed = append(*handed, w)
			return spyT6G4Writer{inner: headT6G4Writer{store: store, deps: p.deps, prov: prov}, rec: rec}
		},
	}
}

// t6g4RealHost is a run host whose context carries a deadline (zero = none).
func t6g4RealHost(kind tasks.Kind, within time.Duration, w tasks.Writer) (*t6g4Host, func()) {
	ctx, cancel := context.Background(), func() {}
	if within > 0 {
		ctx, cancel = context.WithTimeout(ctx, within)
	}
	return &t6g4Host{FakeRun: taskstest.NewFakeRun(ctx, kind), mode: tasks.ModeLive, w: w, trigger: t6g4TriggerOf(kind)}, cancel
}

// t6g4SameResources fails unless the resources the recorded calls wrote are
// exactly want, in any order.
func t6g4SameResources(t *testing.T, rec *t6g4Calls, want ...ops.Resource) {
	t.Helper()
	got := t6g4RecordedResources(t, rec.all())
	w := make([]string, len(want))
	for i, r := range want {
		w[i] = string(r)
	}
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Errorf("recorded writes touched %v, want exactly %v (calls %v)", got, w, rec.all())
	}
}

func init() {
	// transcribe-file-intros through the native shells: the parent starts one child
	// run per batch of books and holds nothing itself; each child writes through the
	// writer built from ITS OWN rc.Writer(), under the run id of the parent (so a
	// rollback by clear_run_id finds what every batch wrote), and the parent
	// persists the counters. (The yield and cursor of the pre-F5-18h shell moved to
	// the parent's batch cursor: batch_sweep_test.go.)
	t6g4WriteScenarios[fileIntroKind] = func(t *testing.T) {
		fx := newIntroFx(t)
		fileIntroSlots = func() int { return 1 }
		paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
		rec := &t6g4Calls{}
		var handed []tasks.Writer
		defs := batchDefsOf(t6g4KindsUnchecked(t6g4RealDeps(fx.p, rec, &handed), nil))
		decl := tasks.CommonOf(defs[fileIntroBatchChildKind]).Effects

		parentRun := &t6g4RunWriter{name: "parent"}
		ph, cancel := t6g4RealHost(fileIntroKind, 0, parentRun)
		defer cancel()
		bh, rc := newBatchRunHost(t, ph, tasks.ModeLive, defs)
		bh.child = func(kind tasks.Kind) (*tasks.RC, tasks.Writer) {
			w := &t6g4RunWriter{name: "child of " + string(kind)}
			ch, cancelChild := t6g4RealHost(kind, 0, w)
			t.Cleanup(cancelChild)
			return tasks.NewRC(ch), w
		}
		if _, err := runT6G4Kind(t, defs[fileIntroKind], rc, ""); err != nil {
			t.Fatalf("parent run: %v", err)
		}

		books := mustAllBooks(t, fx)
		if want := (len(books) + batchSweepSize - 1) / batchSweepSize; len(bh.runs) != want || want == 0 {
			t.Fatalf("%d children for %d books, want %d (one per %d books)", len(bh.runs), len(books), want, batchSweepSize)
		}
		if got := len(fx.whisp.sentPaths()); got != len(paths) {
			t.Fatalf("whisper was sent %d files, want %d", got, len(paths))
		}
		// NewWriter was handed each child's own rc.Writer(), in start order, and
		// never the parent's: the parent holds no writer.
		if len(handed) != len(bh.runs) || len(bh.writers) != len(bh.runs) {
			t.Fatalf("NewWriter called %d times for %d children", len(handed), len(bh.runs))
		}
		for i, w := range handed {
			if w != bh.writers[i] || w == tasks.Writer(parentRun) {
				t.Errorf("NewWriter call %d was handed %v, want child %d's rc.Writer()", i, w, i)
			}
		}
		for _, b := range books {
			for _, f := range mustFiles(t, fx.s, b) {
				if f.IntroSourceHash != f.FileHash {
					t.Fatalf("file %s is not done after the sweep: %+v", f.ID, f)
				}
				if f.IntroRunOpID != string(rc.RunID()) {
					t.Errorf("file %s carries run id %q, want the parent's %q (clear_run_id would miss it)", f.ID, f.IntroRunOpID, rc.RunID())
				}
			}
		}
		// The fixture's files all take the path that sets the intro and then
		// stamps the book's aggregate (SetBookFileIntro, ModifyBook); the resources
		// those two calls write are the table's rows, not a retyped list.
		t6g4SameResources(t, rec, t6RowsOf("SetBookFileIntro", "ModifyBook")...)
		t6g4NoStatsThroughTheWriter(t, rec)
		st, err := fx.s.GetFileIntroStats()
		if err != nil || st == nil {
			t.Fatalf("the run's counters (stats:transcribe-files) were not persisted: %+v err=%v", st, err)
		}
		if st.RunOpID != string(rc.RunID()) || st.OK != len(paths) {
			t.Errorf("stats %+v, want the parent's run id %q and %d files transcribed (the children's counters rolled up)", st, rc.RunID(), len(paths))
		}
		// What the child declares is the union of the rows of every writer method
		// its core can call (a file the fixture does not reach clears the intro or
		// records a timeout), nothing the core never calls.
		canCall := []string{"SetBookFileIntro", "ClearBookFileIntro", "RecordIntroTimeout", "ClearIntroTimeouts", "ModifyBook"}
		for _, m := range rec.all() {
			if !slices.Contains(canCall, m) {
				t.Errorf("the run called %s, which is not among the writer methods the kind can call %v", m, canCall)
			}
		}
		var declared []ops.Resource
		for _, r := range decl.WriteSet() {
			declared = append(declared, ops.Resource(r.Kind))
		}
		slices.Sort(declared)
		if want := t6RowsOf(canCall...); !slices.Equal(declared, want) {
			t.Errorf("declared write set %v, want the union of the rows of %v = %v", declared, canCall, want)
		}
	}
}

// TestFileIntroHEADShell_DefRunWritesThroughTheStore drives HEAD's own def.Run
// (the v2 shell, not the core) over a fixture plugin in live mode: the shell must
// resolve the store, decode the params, wire the HEAD writer and reach the rows.
func TestFileIntroHEADShell_DefRunWritesThroughTheStore(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(3)
	rep := &fileIntroRep{id: "run-head-shell"}
	if err := fx.p.fileIntroTranscribeDef().Run(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range mustAllBooks(t, fx) {
		for _, f := range mustFiles(t, fx.s, b) {
			n++
			if f.IntroSourceHash != f.FileHash {
				t.Fatalf("HEAD's def.Run left file %s untranscribed: %+v", f.ID, f)
			}
		}
	}
	if n != 3 {
		t.Fatalf("fixture has %d files, want 3", n)
	}
}

// TestAIDedupBatchKind_LiveRunBuildsItsWriterFromRCAndRecordsReviewItems: the one
// write of ai-dedup-batch is the batch submission, declared as review_items. The
// native run must build its writer from THIS run's rc.Writer() and record exactly
// that resource. HEAD's shell is driven in dedup_ops_batch_test.go through
// runAIDedupBatchHEAD, because def.Run builds a real OpenAI parser from config.
func TestAIDedupBatchKind_LiveRunBuildsItsWriterFromRCAndRecordsReviewItems(t *testing.T) {
	deps, pr := fakeT6G4Deps(t)
	pr.store.aiParsing = true
	pr.store.authors = []database.Author{{ID: 1, Name: "Alfred Pennyworth"}}
	def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), aiDedupBatchKind)
	run := &t6g4RunWriter{name: "run"}
	_, rc := newT6G4RC(aiDedupBatchKind, tasks.ModeLive, run)
	if _, err := runT6G4Kind(t, def, rc, ""); err != nil {
		t.Fatal(err)
	}
	if pr.newWriterCalls != 1 || pr.handed != tasks.Writer(run) {
		t.Errorf("NewWriter called %d time(s) with %v, want once with this run's rc.Writer()", pr.newWriterCalls, pr.handed)
	}
	t6g4SameResources(t, pr.store.rec, ops.ResReviewItems)
	declared := tasks.CommonOf(def).Effects.WriteSet()
	if len(declared) != 1 || declared[0].Kind != string(ops.ResReviewItems) {
		t.Errorf("declared write set = %v, want exactly review_items", declared)
	}
}

// t6g4NoStatsThroughTheWriter fails if the run called a stats method on the
// gated writer: the two stats keys are written outside it (t6g4StatsSink), so a
// recorded call would mean a gated write that F5-18's writer refuses.
func t6g4NoStatsThroughTheWriter(t *testing.T, rec *t6g4Calls) {
	t.Helper()
	for _, name := range rec.all() {
		if strings.HasPrefix(name, "Put") && strings.HasSuffix(name, "Stats") {
			t.Errorf("%s went through the gated writer; the stats keys have no pkg/ops resource and must bypass it", name)
		}
	}
}

// countingStats counts the calls that reach the stats sink and passes them on.
type countingStats struct {
	inner             t6g4StatsSink
	transcribe, intro atomic.Int64
}

func (c *countingStats) PutTranscribeStats(s *database.TranscribeStats) error {
	c.transcribe.Add(1)
	return c.inner.PutTranscribeStats(s)
}

func (c *countingStats) PutFileIntroStats(s *database.FileIntroStats) error {
	c.intro.Add(1)
	return c.inner.PutFileIntroStats(s)
}

// Review N20: a nil t6g4Deps.Stats would let both transcribe kinds run and
// persist none of their counters, with no log and no error. Registration refuses
// it, so a forgotten wiring fails at startup, not silently at the first run.
func TestT6G4Kinds_RegistrationRefusesNilStats(t *testing.T) {
	deps, _ := fakeT6G4Deps(t)
	if deps.Stats != nil {
		t.Fatal("the fake deps must not set Stats for this test")
	}
	defs, err := t6g4Kinds(deps, nil)
	if err == nil || defs != nil || !strings.Contains(err.Error(), "Stats") {
		t.Fatalf("a nil Stats must be a registration error naming it, got defs=%d err=%v", len(defs), err)
	}
	deps.Stats = headT6G4Stats{}
	defs, err = t6g4Kinds(deps, nil)
	if err != nil || len(defs) != t6g4DefCount() {
		t.Fatalf("with a sink the registration must succeed with all %d kinds, got %d err=%v", t6g4DefCount(), len(defs), err)
	}
}

// Review N6: the stats keys are written outside the gated writer. The run below
// is the live transcribe-book-intros parity case: its counters must reach the
// stats sink (t6g4Deps.Stats) and none of them may appear as a writer call. The
// writer interface and the spy map must not name them either.
func TestT6G4Kinds_TranscribeStatsBypassTheGatedWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	iface := reflect.TypeFor[t6g4Writer]()
	for method := range iface.Methods() {
		if n := method.Name; strings.HasSuffix(n, "Stats") {
			t.Errorf("t6g4Writer has %s: a stats key must not be a gated write", n)
		}
	}
	for n := range t6g4WriterResource {
		if strings.HasSuffix(n, "Stats") {
			t.Errorf("the spy map names %s", n)
		}
	}

	var c *t6g4LiveCase
	for i := range t6g4LiveCases {
		if t6g4LiveCases[i].kind == introTranscribeKind {
			c = &t6g4LiveCases[i]
		}
	}
	if c == nil {
		t.Fatal("no live case for transcribe-book-intros")
	}
	env := c.seed(t)
	rec := &t6g4Calls{}
	var handed []tasks.Writer
	deps := t6g4RealDeps(env.p, rec, &handed)
	sink := &countingStats{inner: deps.Stats}
	deps.Stats = sink
	def := t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), c.kind)
	h, cancel := t6g4RealHost(c.kind, 0, &t6g4RunWriter{name: "native"})
	defer cancel()
	if _, err := runT6G4Kind(t, def, tasks.NewRC(h), env.paramsFor(false, *c)); err != nil {
		t.Fatalf("native run: %v", err)
	}
	if sink.transcribe.Load() == 0 {
		t.Error("the run's counters never reached the stats sink")
	}
	t6g4NoStatsThroughTheWriter(t, rec)
}
