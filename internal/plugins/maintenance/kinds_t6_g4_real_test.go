// file: internal/plugins/maintenance/kinds_t6_g4_real_test.go
// version: 1.0.3
// guid: 075c2d47-deca-45e6-8742-449c0093b857
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
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
	return &t6g4Host{FakeRun: taskstest.NewFakeRun(ctx, kind), mode: tasks.ModeLive, w: w}, cancel
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
	// transcribe-file-intros through the native shell, which sets the yield
	// margin: attempt 1 runs under a deadline inside it and yields after its first
	// batch with a cursor; attempt 2 restores that cursor and finishes the run.
	t6g4WriteScenarios[fileIntroKind] = func(t *testing.T) {
		fx := newIntroFx(t)
		fileIntroSlots = func() int { return 1 } // sequential: the prefix is exactly the first batch
		paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
		rec := &t6g4Calls{}
		var handed []tasks.Writer
		def := fx.p.fileIntroTranscribeKind(t6g4RealDeps(fx.p, rec, &handed), nil)
		decl := tasks.CommonOf(def).Effects

		run1 := &t6g4RunWriter{name: "attempt 1"}
		h1, cancel := t6g4RealHost(fileIntroKind, time.Minute, run1)
		defer cancel()
		_, err := runT6G4Kind(t, def, tasks.NewRC(h1), "")
		if !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("attempt 1 inside the margin: err = %v, want tasks.ErrContinueInPlace (the native shell must set the yield margin and the checkpoint hook)", err)
		}
		raw, ok := h1.CheckpointJSON("")
		if !ok {
			t.Fatal("attempt 1 yielded without saving a cursor")
		}
		var cur fileIntroCursor
		if err := json.Unmarshal(raw, &cur); err != nil {
			t.Fatal(err)
		}
		if cur.DoneBatches != 1 || cur.Last == nil || cur.Stats.Done {
			t.Fatalf("cursor after the yield = %+v, want one batch done, a last key, counters not done", cur)
		}
		if got := len(fx.whisp.sentPaths()); got != fileIntroBatchGroups {
			t.Fatalf("attempt 1 sent %d files, want exactly the first batch (%d)", got, fileIntroBatchGroups)
		}

		fx.whisp.mu.Lock()
		fx.whisp.calls = nil
		fx.whisp.mu.Unlock()
		run2 := &t6g4RunWriter{name: "attempt 2"}
		h2, cancel2 := t6g4RealHost(fileIntroKind, 0, run2)
		defer cancel2()
		if err := h2.SetCheckpoint("", raw); err != nil {
			t.Fatal(err)
		}
		if _, err := runT6G4Kind(t, def, tasks.NewRC(h2), ""); err != nil {
			t.Fatalf("attempt 2 (restored): %v", err)
		}
		if got := len(fx.whisp.sentPaths()); got != len(paths)-fileIntroBatchGroups {
			t.Fatalf("attempt 2 sent %d files, want the remaining %d", got, len(paths)-fileIntroBatchGroups)
		}
		if len(handed) != 2 || handed[0] != tasks.Writer(run1) || handed[1] != tasks.Writer(run2) {
			t.Errorf("NewWriter was handed %v, want each attempt's own rc.Writer()", handed)
		}
		for _, b := range mustAllBooks(t, fx) {
			for _, f := range mustFiles(t, fx.s, b) {
				if f.IntroSourceHash != f.FileHash {
					t.Fatalf("file %s is not done after the second attempt: %+v", f.ID, f)
				}
			}
		}
		t6g4SameResources(t, rec, ops.ResBookFiles, ops.ResBooks)
		t6g4NoStatsThroughTheWriter(t, rec)
		if st, err := fx.s.GetFileIntroStats(); err != nil || st == nil {
			t.Errorf("the run's counters (stats:transcribe-files) were not persisted: %+v err=%v", st, err)
		}
		for _, r := range decl.WriteSet() {
			if r.Kind != string(ops.ResBookFiles) && r.Kind != string(ops.ResBooks) {
				t.Errorf("declared %q, which the run never wrote", r.Kind)
			}
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
	if err != nil || len(defs) != len(t6g4Expected) {
		t.Fatalf("with a sink the registration must succeed with all %d kinds, got %d err=%v", len(t6g4Expected), len(defs), err)
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
	iface := reflect.TypeOf((*t6g4Writer)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		if n := iface.Method(i).Name; strings.HasSuffix(n, "Stats") {
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
