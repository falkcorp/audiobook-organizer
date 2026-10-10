// file: internal/operations/registry/metrics_otel_test.go
// version: 1.2.0
// guid: 4d7c1e92-a3b8-4f06-8c5d-9e2f0b61a7d3
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// otelRig is a registry wired to a private meter provider, so a test reads
// exactly what the registry recorded and nothing a sibling test did.
type otelRig struct {
	reader *sdkmetric.ManualReader
	reg    *registry.Registry
	store  *fakeStore
}

func newOtelRig(t *testing.T) *otelRig {
	t.Helper()
	return newOtelRigWith(t, registry.Options{})
}

// newOtelRigWith is newOtelRig with extra registry options; OpsMetrics is
// always the rig's private recorder.
func newOtelRigWith(t *testing.T, opts registry.Options) *otelRig {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	store := newFakeStore()
	opts.OpsMetrics = opsmetrics.New(mp)
	reg := registry.NewWithOptions(store, slog.Default(), 1, opts)
	return &otelRig{reader: reader, reg: reg, store: store}
}

func (g *otelRig) collect(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := g.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func attrIs(set attribute.Set, key, want string) bool {
	v, ok := set.Value(attribute.Key(key))
	return ok && v.AsString() == want
}

// counterValue is the ops.runs / ops.items point for (defID, outcome); 0 when
// there is none.
func (g *otelRig) counterValue(t *testing.T, name, defID, outcome string) int64 {
	t.Helper()
	m, ok := g.collect(t)[name]
	if !ok {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s is %T, want Sum[int64]", name, m.Data)
	}
	for _, dp := range sum.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) && attrIs(dp.Attributes, "outcome", outcome) {
			return dp.Value
		}
	}
	return 0
}

func (g *otelRig) inflight(t *testing.T, defID string) int64 {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.inflight"]
	if !ok {
		return 0
	}
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("ops.inflight is %T, want Gauge[int64]", m.Data)
	}
	for _, dp := range gauge.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) {
			return dp.Value
		}
	}
	return 0
}

// progressAge is the ops.progress_age value for defID, and whether a point exists.
func (g *otelRig) progressAge(t *testing.T, defID string) (float64, bool) {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.progress_age"]
	if !ok {
		return 0, false
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("ops.progress_age is %T, want Gauge[float64]", m.Data)
	}
	for _, dp := range gauge.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) {
			return dp.Value, true
		}
	}
	return 0, false
}

// durationCount is the number of ops.run.duration observations for (defID, outcome).
func (g *otelRig) durationCount(t *testing.T, defID, outcome string) uint64 {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.run.duration"]
	if !ok {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("ops.run.duration is %T, want Histogram[float64]", m.Data)
	}
	for _, dp := range h.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) && attrIs(dp.Attributes, "outcome", outcome) {
			return dp.Count
		}
	}
	return 0
}

func waitForOtel(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOpsMetrics_RunToCompletion(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.otel-complete"
	inRun, release := make(chan struct{}), make(chan struct{})
	def := makeValidDef(id)
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(10, 40, "")
		close(inRun)
		<-release
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })

	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-inRun
	if got := g.inflight(t, id); got != 1 {
		t.Errorf("ops.inflight during the run = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "started"); got != 1 {
		t.Errorf("runs{started} = %d, want 1", got)
	}
	close(release)
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	waitForOtel(t, "runs{completed}", func() bool {
		return g.counterValue(t, "audiobook_organizer.ops.runs", id, "completed") == 1
	})
	if got := g.durationCount(t, id, "completed"); got != 1 {
		t.Errorf("run.duration{completed} observations = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.items", id, "processed"); got != 10 {
		t.Errorf("items{processed} = %d, want 10", got)
	}
	waitForOtel(t, "ops.inflight back to 0", func() bool { return g.inflight(t, id) == 0 })
}

func TestOpsMetrics_RunToFailure(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.otel-failed"
	def := makeValidDef(id)
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return errors.New("boom") }
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })

	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, g.store, opID, "failed", 5*time.Second)
	waitForOtel(t, "runs{failed}", func() bool {
		return g.counterValue(t, "audiobook_organizer.ops.runs", id, "failed") == 1
	})
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "started"); got != 1 {
		t.Errorf("runs{started} = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "completed"); got != 0 {
		t.Errorf("runs{completed} = %d after a failed run, want 0", got)
	}
	if got := g.durationCount(t, id, "failed"); got != 1 {
		t.Errorf("run.duration{failed} observations = %d, want 1", got)
	}
}

type progressReport struct{ current, total int }

// runProgress runs one def that reports seq and returns ops.items{processed}.
// preset, when non-nil, is written to the row as persisted progress before
// the registry starts, which is what a resumed or retried row carries.
func runProgress(t *testing.T, id string, seq []progressReport, preset *progressReport) int64 {
	t.Helper()
	g := newOtelRig(t)
	def := makeValidDef(id)
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		for _, p := range seq {
			_ = rep.UpdateProgress(p.current, p.total, "")
		}
		return nil
	}
	if preset != nil {
		// A row left queued with progress at startup is resumed by
		// resumeAfterStartup; ResumeRestart keeps it (ResumeDrop drops it).
		def.ResumePolicy = registry.ResumeRestart
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	var opID string
	if preset != nil {
		var err error
		if opID, err = g.reg.EnqueueOp(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
		if err := g.store.UpdateOpProgressV2(opID, preset.current, preset.total, ""); err != nil {
			t.Fatal(err)
		}
		g.reg.Start(t.Context())
	} else {
		g.reg.Start(t.Context())
		var err error
		if opID, err = g.reg.EnqueueOp(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	return g.counterValue(t, "audiobook_organizer.ops.items", id, "processed")
}

// TestOpsMetrics_ProgressToItems pins how UpdateProgress values become
// ops.items{outcome=processed}. Each case names the progress shape it stands
// for; see processedItems in reporter_db.go.
func TestOpsMetrics_ProgressToItems(t *testing.T) {
	cases := []struct {
		name string
		seq  []progressReport
		want int64
	}{
		// A decrease with the same total (a re-estimate or a late report)
		// adds nothing and does not lower the mark, so climbing back to 25
		// adds nothing either.
		{"same-total decrease", []progressReport{{10, 100}, {25, 100}, {25, 100}, {20, 100}, {25, 100}}, 25},
		// Phase reset to 0 with a new total (organizer: fetch, then scan,
		// then organize). The second phase's 15 items count even though they
		// never pass the first phase's 25. A run-wide high-water mark counted
		// 25 here and 0 for every later phase, which fired OpStalledV2 on
		// healthy runs.
		{"phase reset to zero", []progressReport{{10, 100}, {25, 100}, {0, 50}, {5, 50}, {15, 50}}, 40},
		// Phase reset to 0 over the same total (organizer scan after metadata
		// fetch over the same books).
		{"phase reset same total", []progressReport{{0, 30}, {30, 30}, {0, 30}, {12, 30}}, 42},
		// Concurrent RunItems workers report out of order with one total: 11
		// after 12 is not a new phase.
		{"out of order", []progressReport{{10, 100}, {12, 100}, {11, 100}, {13, 100}}, 13},
		// The library scan reports (processed, discovered): the total grows
		// while current climbs, and every step counts.
		{"growing total", []progressReport{{10, 100}, {20, 110}, {30, 120}}, 30},
		// A new phase that skips the 0 report but has a different total
		// starts a segment at its first report.
		{"phase without zero report", []progressReport{{10, 10}, {3, 40}, {8, 40}}, 15},
		// folder.autoscan: ScanDirectoryParallel interleaves a folder stream
		// (dirsScanned, len(dirs)) with an indeterminate file stream
		// (filesScanned, 0) on one reporter. The file reports are dropped, so
		// the count is the 10 folders. Reading both as one stream counted 295.
		{"interleaved indeterminate stream", []progressReport{{100, 0}, {5, 50}, {200, 0}, {10, 50}}, 10},
		// (0,0) is a phase separator even though it has no total: the
		// organizer reports it before the reload and before the pre-organize
		// backup, and its organize loop's first report can carry the scan
		// phase's total. Every variant of the rule must count 10 here.
		{"zero-zero separator", []progressReport{{5, 5}, {0, 0}, {1, 5}, {5, 5}}, 10},
		// Organizer shape: the scan phase starts at (0,N), the organize phase
		// follows a (0,0) marker and its first report is (50, N) over the
		// same N. Without the (0,0) reset the organize phase counts nothing.
		{"organizer zero markers", []progressReport{{0, 0}, {0, 80}, {80, 80}, {0, 0}, {50, 80}, {80, 80}}, 160},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runProgress(t, fmt.Sprintf("test.otel-items-%d", i), tc.seq, nil)
			if got != tc.want {
				t.Errorf("items{processed} = %d, want %d (reports %v)", got, tc.want, tc.seq)
			}
		})
	}
}

// TestOpsMetrics_ResumedAttemptDoesNotRecountCheckpoint: a row that carries
// 500/1000 from an earlier attempt, resumed at its checkpoint, counts only the
// items this attempt adds.
func TestOpsMetrics_ResumedAttemptDoesNotRecountCheckpoint(t *testing.T) {
	got := runProgress(t, "test.otel-items-resume",
		[]progressReport{{500, 1000}, {510, 1000}, {530, 1000}}, &progressReport{500, 1000})
	if got != 30 {
		t.Errorf("items{processed} = %d, want 30 (the checkpointed 500 must not count again)", got)
	}
}

// TestOpsMetrics_AttemptRestartsBelowSeed: progress is not cleared on
// requeue, so a row can carry 500/1000 into an attempt that starts again from
// the bottom. Its first report starts a segment, so the rerun is counted.
func TestOpsMetrics_AttemptRestartsBelowSeed(t *testing.T) {
	// A manual retry of a ResumeRequeue def reruns from zero; RunItems'
	// first report is offset+1 (here 1), never a 0 marker. 1..30 adds 29.
	var fromZero []progressReport
	for i := 1; i <= 30; i++ {
		fromZero = append(fromZero, progressReport{i, 1000})
	}
	if got := runProgress(t, "test.otel-items-retry-zero", fromZero, &progressReport{500, 1000}); got != 29 {
		t.Errorf("retry from zero: items{processed} = %d, want 29", got)
	}
	// A RunItems resume whose checkpoint (400) trails the flushed progress
	// (500): items 401..500 are processed again and must count.
	trailing := []progressReport{{401, 1000}, {450, 1000}, {520, 1000}}
	if got := runProgress(t, "test.otel-items-trailing-ckpt", trailing, &progressReport{500, 1000}); got != 119 {
		t.Errorf("trailing checkpoint: items{processed} = %d, want 119", got)
	}
}

// progressTimeout is the ops.progress_timeout value for defID, and whether a
// point exists.
func (g *otelRig) progressTimeout(t *testing.T, defID string) (float64, bool) {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.progress_timeout"]
	if !ok {
		return 0, false
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("ops.progress_timeout is %T, want Gauge[float64]", m.Data)
	}
	for _, dp := range gauge.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) {
			return dp.Value, true
		}
	}
	return 0, false
}

// TestOpsMetrics_ProgressTimeoutLongBudget: a LivenessNone def that declares a
// 2h silence budget (scheduler.dedup-llm-review's shape) and is silent for 40m
// is healthy. The alert inputs show it: age 2400 under timeout 7200, so
// OpStalledV2 (age > max(timeout, 1800)) stays quiet. A def with no explicit
// budget reports the watchdog default.
func TestOpsMetrics_ProgressTimeoutLongBudget(t *testing.T) {
	var nowNano atomic.Int64
	nowNano.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNano.Load()) }

	g := newOtelRigWith(t, registry.Options{LivenessNow: clock})
	const id = "test.otel-long-budget"
	started, release := make(chan struct{}), make(chan struct{})
	def := makeValidDef(id)
	def.Liveness = registry.LivenessNone
	def.ProgressTimeout = 2 * time.Hour
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		close(started)
		<-release
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitForOtel(t, "attempt start stamped", func() bool {
		_, ok := g.progressAge(t, id)
		return ok
	})
	nowNano.Add(int64(40 * time.Minute))
	age, _ := g.progressAge(t, id)
	budget, ok := g.progressTimeout(t, id)
	if !ok || budget != 7200 {
		t.Errorf("progress_timeout = %v (point %v), want 7200", budget, ok)
	}
	if age != 2400 {
		t.Errorf("progress_age 40m into a silent run = %v, want 2400", age)
	}
	if age > max(budget, 1800) {
		t.Errorf("OpStalledV2 inputs would fire: age %v > max(timeout %v, 1800)", age, budget)
	}
	close(release)
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	waitForOtel(t, "progress_timeout back to 0", func() bool {
		v, ok := g.progressTimeout(t, id)
		return ok && v == 0
	})
}

// TestOpsMetrics_ProgressAge: ops.progress_age measures time since the run's
// last UpdateProgress on the registry's liveness clock, and a same-value
// heartbeat resets it. This is the liveness signal OpStalledV2 alerts on: a
// phase that reports a constant current (a database backup, a hash pass
// pinned at total/total) is alive as long as it keeps reporting.
func TestOpsMetrics_ProgressAge(t *testing.T) {
	var nowNano atomic.Int64
	nowNano.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNano.Load()) }
	advance := func(d time.Duration) { nowNano.Add(int64(d)) }

	g := newOtelRigWith(t, registry.Options{LivenessNow: clock})
	const id = "test.otel-age"
	beat, beaten, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	reported := make(chan struct{})
	def := makeValidDef(id)
	// Keep the watchdog away from the jumped clock.
	def.ProgressTimeout = 24 * time.Hour
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(5, 10, "")
		close(reported)
		<-beat
		_ = rep.UpdateProgress(5, 10, "still backing up") // heartbeat, same value
		close(beaten)
		<-release
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-reported
	if age, ok := g.progressAge(t, id); !ok || age != 0 {
		t.Errorf("progress_age right after a report = %v (point %v), want 0", age, ok)
	}
	advance(40 * time.Minute)
	if age, _ := g.progressAge(t, id); age != 2400 {
		t.Errorf("progress_age 40m after the last report = %v, want 2400", age)
	}
	close(beat)
	<-beaten
	if age, _ := g.progressAge(t, id); age != 0 {
		t.Errorf("progress_age after a same-value heartbeat = %v, want 0", age)
	}
	advance(10 * time.Minute)
	if age, _ := g.progressAge(t, id); age != 600 {
		t.Errorf("progress_age 10m after the heartbeat = %v, want 600", age)
	}
	close(release)
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	waitForOtel(t, "progress_age back to 0", func() bool {
		age, ok := g.progressAge(t, id)
		return ok && age == 0
	})
}

// TestShutdownUnregistersInflightCallback: a registry started and shut down 20
// times leaves no callback behind.
func TestShutdownUnregistersInflightCallback(t *testing.T) {
	before := opsmetrics.ActiveInflightCallbacks()
	for i := 0; i < 20; i++ {
		g := newOtelRig(t)
		g.reg.Start(context.Background())
		if got := opsmetrics.ActiveInflightCallbacks(); got != before+1 {
			t.Fatalf("round %d: %d callbacks after Start, want %d", i, got, before+1)
		}
		if err := g.reg.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := opsmetrics.ActiveInflightCallbacks(); got != before {
			t.Fatalf("round %d: %d callbacks after Shutdown, want %d", i, got, before)
		}
	}
}
