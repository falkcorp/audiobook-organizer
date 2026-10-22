// file: internal/opsmetrics/opsmetrics_test.go
// version: 1.5.0
// guid: 91c2e7d5-3a48-4b0f-8e16-d5a70c3f2b84
// last-edited: 2026-10-22

package opsmetrics

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

func newRig(t *testing.T) (*Recorder, func() map[string]metricdata.Metrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	collect := func() map[string]metricdata.Metrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
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
	return New(mp), collect
}

func runPoints(m metricdata.Metrics) map[string]int64 {
	out := map[string]int64{}
	if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
		for _, dp := range sum.DataPoints {
			d, _ := dp.Attributes.Value(telemetry.DefID)
			o, _ := dp.Attributes.Value(telemetry.Outcome)
			out[d.AsString()+"/"+o.AsString()] += dp.Value
		}
	}
	return out
}

// TestOutcomeFor pins the v2 status (and message) to outcome table: a
// canceled row with the timeout prefix is timed_out, interrupted_dropped is
// dropped, and the pauses and live statuses are no outcome.
func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		status, msg string
		want        string // "" = no outcome
	}{
		{"completed", "", "completed"},
		{"failed", "boom", "failed"},
		{"canceled", "", "canceled"},
		{"canceled", "canceled by user", "canceled"},
		{"canceled", "timeout: run exceeded 10ms", "timed_out"},
		{"interrupted_dropped", "ResumePolicy=drop", "dropped"},
		{"interrupted_dropped", "requeued: original op replaced", ""},
		{"interrupted_restart", "", ""},
		{"timeout", "", ""},
		{"interrupted_quiesced", "", ""},
		{"interrupted_ask", "", ""},
		{"running", "", ""},
		{"queued", "", ""},
	}
	for _, tc := range cases {
		got, ok := OutcomeFor(tc.status, tc.msg)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("OutcomeFor(%q, %q) = %q, %v; want %q, %v", tc.status, tc.msg, got, ok, tc.want, tc.want != "")
		}
	}
}

// TestRuns_OutcomeRecording: Finished records a run end with its duration,
// Ended records it without one, and neither records started, an interrupted
// pause or an unknown value.
func TestRuns_OutcomeRecording(t *testing.T) {
	RegisterDefID("t.mapping")
	cases := []struct {
		outcome string
		ok      bool
	}{
		{OutcomeCompleted, true},
		{OutcomeFailed, true},
		{OutcomeCanceled, true},
		{OutcomeTimedOut, true},
		{OutcomeDropped, true},
		{OutcomeStarted, false},
		{"interrupted", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run("finished/"+tc.outcome, func(t *testing.T) {
			rec, collect := newRig(t)
			if got := rec.Finished("t.mapping", tc.outcome, 2*time.Second); got != tc.ok {
				t.Fatalf("Finished(%q) = %v, want %v", tc.outcome, got, tc.ok)
			}
			m := collect()
			pts := runPoints(m["audiobook_organizer.ops.runs"])
			if !tc.ok {
				if len(pts) != 0 {
					t.Errorf("outcome %q recorded runs %v, want nothing", tc.outcome, pts)
				}
				if _, ok := m["audiobook_organizer.ops.run.duration"]; ok {
					t.Errorf("outcome %q recorded a duration, want nothing", tc.outcome)
				}
				return
			}
			if pts["t.mapping/"+tc.outcome] != 1 || len(pts) != 1 {
				t.Errorf("outcome %q recorded runs %v, want only t.mapping/%s=1", tc.outcome, pts, tc.outcome)
			}
			if _, ok := m["audiobook_organizer.ops.run.duration"]; !ok {
				t.Errorf("outcome %q recorded no duration", tc.outcome)
			}
		})
		t.Run("ended/"+tc.outcome, func(t *testing.T) {
			rec, collect := newRig(t)
			if got := rec.Ended("t.mapping", tc.outcome); got != tc.ok {
				t.Fatalf("Ended(%q) = %v, want %v", tc.outcome, got, tc.ok)
			}
			m := collect()
			pts := runPoints(m["audiobook_organizer.ops.runs"])
			want := 0
			if tc.ok {
				want = 1
			}
			if pts["t.mapping/"+tc.outcome] != int64(want) || len(pts) != want {
				t.Errorf("Ended(%q) recorded runs %v, want %d point(s)", tc.outcome, pts, want)
			}
			if _, ok := m["audiobook_organizer.ops.run.duration"]; ok {
				t.Errorf("Ended(%q) recorded a duration; it must never", tc.outcome)
			}
		})
	}
}

func zombiesValue(t *testing.T, m map[string]metricdata.Metrics) int64 {
	t.Helper()
	g, ok := m["audiobook_organizer.ops.zombies"].Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 {
		t.Fatalf("want one ops.zombies point, got %+v", m["audiobook_organizer.ops.zombies"].Data)
	}
	if n := g.DataPoints[0].Attributes.Len(); n != 0 {
		t.Errorf("ops.zombies carries %d attributes, want none", n)
	}
	return g.DataPoints[0].Value
}

// TestZombies_SumsSources: the gauge reads 0 with no source, the sum of every
// registered source, follows each source's current value, and drops a source
// once it is unregistered (idempotently). Run with -race: the sources are read
// from the collecting goroutine while other goroutines change them.
func TestZombies_SumsSources(t *testing.T) {
	rec, collect := newRig(t)
	if got := zombiesValue(t, collect()); got != 0 {
		t.Fatalf("ops.zombies with no source = %d, want 0", got)
	}
	var a, b atomic.Int64
	unregA := rec.RegisterZombies(a.Load)
	unregB := rec.RegisterZombies(b.Load)
	a.Store(2)
	b.Store(3)
	if got := zombiesValue(t, collect()); got != 5 {
		t.Errorf("ops.zombies = %d, want 5 (2 + 3)", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				a.Add(1)
				a.Add(-1)
			}
		})
	}
	for i := 0; i < 10; i++ {
		collect()
	}
	wg.Wait()
	unregA()
	unregA() // idempotent
	if got := zombiesValue(t, collect()); got != 3 {
		t.Errorf("ops.zombies after unregistering a = %d, want 3", got)
	}
	unregB()
	if got := zombiesValue(t, collect()); got != 0 {
		t.Errorf("ops.zombies after unregistering both = %d, want 0", got)
	}
}

func TestRunDuration_UsesTheView(t *testing.T) {
	RegisterDefID("t.dur")
	rec, collect := newRig(t)
	rec.Finished("t.dur", "completed", 90*time.Second)
	h, ok := collect()["audiobook_organizer.ops.run.duration"].Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 {
		t.Fatalf("no duration histogram point: %+v", h)
	}
	want := telemetry.HistogramBuckets()["audiobook_organizer.ops.run.duration"]
	if !slices.Equal(h.DataPoints[0].Bounds, want) {
		t.Errorf("bounds %v, want the view's %v", h.DataPoints[0].Bounds, want)
	}
	if len(want) != 18 || want[len(want)-1] != 86400 {
		t.Errorf("view has %d buckets ending at %v, want 18 ending at 86400", len(want), want[len(want)-1])
	}
}

func TestUnknownDefIDFoldsToOther(t *testing.T) {
	RegisterDefID("t.known")
	rec, collect := newRig(t)
	rec.Started("t.known")
	rec.Started("not-registered-" + "abc")
	rec.Started("not-registered-" + "def")
	rec.Items("op-0123456789", ItemsProcessed, 3)
	m := collect()
	runs := runPoints(m["audiobook_organizer.ops.runs"])
	if runs["t.known/started"] != 1 || runs["other/started"] != 2 || len(runs) != 2 {
		t.Errorf("runs = %v, want t.known/started=1 and other/started=2", runs)
	}
	items := runPoints(m["audiobook_organizer.ops.items"])
	if items["other/processed"] != 3 || len(items) != 1 {
		t.Errorf("items = %v, want other/processed=3", items)
	}
	if labelFor("") != OtherDefID {
		t.Errorf("labelFor(\"\") = %q, want %q", labelFor(""), OtherDefID)
	}
}

// TestNoRunIDAttribute: the declared key list excludes op_id, and every
// attribute that actually reaches a data point is in that list.
func TestNoRunIDAttribute(t *testing.T) {
	for _, k := range EmittedKeys {
		if k == telemetry.OpID {
			t.Errorf("EmittedKeys has the per-run key %q", k)
		}
	}
	RegisterDefID("t.keys")
	rec, collect := newRig(t)
	rec.Started("t.keys")
	rec.Finished("t.keys", OutcomeFailed, time.Second)
	rec.Items("t.keys", ItemsProcessed, 1)
	unreg := rec.RegisterInflight(func() map[InflightKey]InflightStat {
		return map[InflightKey]InflightStat{{DefID: "t.keys", Plugin: "p"}: {Runs: 1, ProgressAge: time.Second, ProgressTimeout: time.Minute}}
	})
	defer unreg()
	for name, m := range collect() {
		var sets []attribute.Set
		switch d := m.Data.(type) {
		case metricdata.Sum[int64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		case metricdata.Histogram[float64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		case metricdata.Gauge[int64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		case metricdata.Gauge[float64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		default:
			t.Errorf("%s has unhandled data type %T; extend this walk", name, m.Data)
		}
		if len(sets) == 0 {
			t.Errorf("%s has no points", name)
		}
		for _, set := range sets {
			for _, kv := range set.ToSlice() {
				if !slices.Contains(EmittedKeys, kv.Key) {
					t.Errorf("%s emitted attribute %q, not in EmittedKeys", name, kv.Key)
				}
			}
		}
	}
}

// TestInflightReportsZeroAfterRunsEnd: a key that was observed once and is
// gone from the source reads 0 on both gauges rather than keeping its old
// value or vanishing.
func TestInflightReportsZeroAfterRunsEnd(t *testing.T) {
	RegisterDefID("t.inflight")
	rec, collect := newRig(t)
	cur := map[InflightKey]InflightStat{{DefID: "t.inflight", Plugin: "p"}: {Runs: 2, ProgressAge: 90 * time.Second, ProgressTimeout: time.Hour}}
	unreg := rec.RegisterInflight(func() map[InflightKey]InflightStat { return cur })
	read := func() (int64, float64, float64) {
		m := collect()
		g, ok := m["audiobook_organizer.ops.inflight"].Data.(metricdata.Gauge[int64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("want one inflight point, got %+v", g)
		}
		a, ok := m["audiobook_organizer.ops.progress_age"].Data.(metricdata.Gauge[float64])
		if !ok || len(a.DataPoints) != 1 {
			t.Fatalf("want one progress_age point, got %+v", a)
		}
		to, ok := m["audiobook_organizer.ops.progress_timeout"].Data.(metricdata.Gauge[float64])
		if !ok || len(to.DataPoints) != 1 {
			t.Fatalf("want one progress_timeout point, got %+v", to)
		}
		return g.DataPoints[0].Value, a.DataPoints[0].Value, to.DataPoints[0].Value
	}
	if runs, age, budget := read(); runs != 2 || age != 90 || budget != 3600 {
		t.Fatalf("inflight = %d, progress_age = %v, progress_timeout = %v; want 2, 90 and 3600", runs, age, budget)
	}
	cur = map[InflightKey]InflightStat{}
	if runs, age, budget := read(); runs != 0 || age != 0 || budget != 0 {
		t.Errorf("after the runs ended inflight = %d, progress_age = %v, progress_timeout = %v; want 0, 0 and 0", runs, age, budget)
	}
	before := ActiveInflightCallbacks()
	unreg()
	unreg() // idempotent
	if got := ActiveInflightCallbacks(); got != before-1 {
		t.Errorf("callbacks after unregister = %d, want %d", got, before-1)
	}
}

// TestInflightFoldsKeys: two unregistered defs on one plugin fold into one
// "other" series; runs add up and the progress age is the larger one, so the
// fold cannot hide the most idle run behind a fresher sibling.
func TestInflightFoldsKeys(t *testing.T) {
	rec, collect := newRig(t)
	unreg := rec.RegisterInflight(func() map[InflightKey]InflightStat {
		return map[InflightKey]InflightStat{
			{DefID: "unregistered-a", Plugin: "p"}: {Runs: 1, ProgressAge: 10 * time.Second, ProgressTimeout: 2 * time.Hour},
			{DefID: "unregistered-b", Plugin: "p"}: {Runs: 2, ProgressAge: 40 * time.Minute, ProgressTimeout: 5 * time.Minute},
		}
	})
	defer unreg()
	m := collect()
	g := m["audiobook_organizer.ops.inflight"].Data.(metricdata.Gauge[int64])
	a := m["audiobook_organizer.ops.progress_age"].Data.(metricdata.Gauge[float64])
	if len(g.DataPoints) != 1 || len(a.DataPoints) != 1 {
		t.Fatalf("want one folded point per gauge, got %d and %d", len(g.DataPoints), len(a.DataPoints))
	}
	if d, _ := g.DataPoints[0].Attributes.Value(telemetry.DefID); d.AsString() != OtherDefID {
		t.Errorf("def_id = %q, want %q", d.AsString(), OtherDefID)
	}
	if g.DataPoints[0].Value != 3 {
		t.Errorf("folded inflight = %d, want 3", g.DataPoints[0].Value)
	}
	if a.DataPoints[0].Value != 2400 {
		t.Errorf("folded progress_age = %v, want 2400 (the max)", a.DataPoints[0].Value)
	}
	to := m["audiobook_organizer.ops.progress_timeout"].Data.(metricdata.Gauge[float64])
	if len(to.DataPoints) != 1 || to.DataPoints[0].Value != 7200 {
		t.Errorf("folded progress_timeout = %+v, want one point of 7200 (the max)", to.DataPoints)
	}
}

// TestDeclaredButUnwiredAreListed pins the three spec 11 §3.7 instruments that
// are declared but not created here. Creating one needs an ourInstruments
// declaration, a golden row and a seed in internal/telemetry/contract, which
// is how the task that wires it (05-PR5) moves it out of Unwired. 05-PR2 moved
// ops.zombies out that way.
func TestDeclaredButUnwiredAreListed(t *testing.T) {
	want := []string{
		"audiobook_organizer.ops.queue_depth",
		"audiobook_organizer.ops.fenced_writes",
		"audiobook_organizer.ops.checkpoint_age",
	}
	var got []string
	for _, u := range Unwired {
		got = append(got, u.Name)
		if u.Owner == "" || u.Kind == "" {
			t.Errorf("%s: Unwired entry needs a kind and an owner", u.Name)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("Unwired = %v, want %v", got, want)
	}
	// None of them may be created by this package.
	rec, collect := newRig(t)
	RegisterDefID("t.unwired")
	rec.Started("t.unwired")
	rec.Finished("t.unwired", OutcomeCompleted, time.Second)
	rec.Items("t.unwired", ItemsProcessed, 1)
	rec.RegisterInflight(func() map[InflightKey]InflightStat {
		return map[InflightKey]InflightStat{{DefID: "t.unwired"}: {Runs: 1}}
	})()
	for name := range collect() {
		if slices.Contains(want, name) {
			t.Errorf("%s is created although it is listed as unwired", name)
		}
	}
}

func TestScheduleInstruments(t *testing.T) {
	rec, collect := newRig(t)
	RegisterDefID("t.sched")
	rec.ScheduleLag("t.sched", 90*time.Second)
	rec.ScheduleMissed("t.sched", 3)
	rec.ScheduleMissed("t.sched", 0) // records nothing
	m := collect()
	h, ok := m["audiobook_organizer.ops.schedule_lag"].Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 || h.DataPoints[0].Sum != 90 {
		t.Fatalf("schedule_lag = %+v, want one point summing 90", m["audiobook_organizer.ops.schedule_lag"].Data)
	}
	c, ok := m["audiobook_organizer.ops.schedule_missed"].Data.(metricdata.Sum[int64])
	if !ok || len(c.DataPoints) != 1 || c.DataPoints[0].Value != 3 {
		t.Fatalf("schedule_missed = %+v, want one point of 3", m["audiobook_organizer.ops.schedule_missed"].Data)
	}
}

// abandonedPoints returns source -> value for ops.abandoned_calls.
func abandonedPoints(t *testing.T, m map[string]metricdata.Metrics) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	g, ok := m["audiobook_organizer.ops.abandoned_calls"].Data.(metricdata.Gauge[int64])
	if !ok {
		return out
	}
	for _, dp := range g.DataPoints {
		src, _ := dp.Attributes.Value(telemetry.Source)
		out[src.AsString()] += dp.Value
	}
	return out
}

// TestAbandonedCalls_ReportsEachSource: a registered counter is on the gauge
// from registration (0), follows the counter, is labeled by its source name,
// sums counters that share a name, and disappears once unregistered
// (idempotently). Run with -race: the counters change while collecting.
func TestAbandonedCalls_ReportsEachSource(t *testing.T) {
	rec, collect := newRig(t)
	if got := abandonedPoints(t, collect()); len(got) != 0 {
		t.Fatalf("no source registered, got points %v", got)
	}
	var resolver, stat, stat2 atomic.Int64
	unregResolver := rec.RegisterAbandoned(SourceRepairsResolver, &resolver)
	unregStat := rec.RegisterAbandoned(SourceRetiredVersionsStat, &stat)
	rec.RegisterAbandoned(SourceRetiredVersionsStat, &stat2)
	if got := abandonedPoints(t, collect()); got[SourceRepairsResolver] != 0 || got[SourceRetiredVersionsStat] != 0 || len(got) != 2 {
		t.Fatalf("registered sources must read 0 from registration, got %v", got)
	}
	resolver.Store(3)
	stat.Store(2)
	stat2.Store(4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				resolver.Add(1)
				resolver.Add(-1)
			}
		})
	}
	for i := 0; i < 10; i++ {
		collect()
	}
	wg.Wait()
	got := abandonedPoints(t, collect())
	if got[SourceRepairsResolver] != 3 || got[SourceRetiredVersionsStat] != 6 {
		t.Errorf("points = %v, want repairs_resolver 3 and retired_versions_stat 2+4=6", got)
	}
	unregResolver()
	unregResolver() // idempotent
	got = abandonedPoints(t, collect())
	if _, still := got[SourceRepairsResolver]; still || got[SourceRetiredVersionsStat] != 6 {
		t.Errorf("after unregistering the resolver, points = %v", got)
	}
	unregStat()
	if got := abandonedPoints(t, collect()); got[SourceRetiredVersionsStat] != 4 {
		t.Errorf("after unregistering one stat counter, points = %v, want 4 left", got)
	}
}
