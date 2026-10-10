// file: internal/opsmetrics/opsmetrics_test.go
// version: 1.1.0
// guid: 91c2e7d5-3a48-4b0f-8e16-d5a70c3f2b84
// last-edited: 2026-10-10

package opsmetrics

import (
	"context"
	"slices"
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

func TestRuns_OutcomeMapping(t *testing.T) {
	RegisterDefID("t.mapping")
	cases := []struct {
		status string
		want   string // "" = records nothing
	}{
		{"completed", "completed"},
		{"failed", "failed"},
		{"canceled", "canceled"},
		{"timeout", ""},
		{"interrupted_dropped", ""},
		{"interrupted_quiesced", ""},
		{"interrupted_ask", ""},
		{"running", ""},
		{"queued", ""},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			rec, collect := newRig(t)
			got := rec.Finished("t.mapping", tc.status, 2*time.Second)
			if got != (tc.want != "") {
				t.Fatalf("Finished(%q) = %v, want %v", tc.status, got, tc.want != "")
			}
			m := collect()
			pts := runPoints(m["audiobook_organizer.ops.runs"])
			if tc.want == "" {
				if len(pts) != 0 {
					t.Errorf("status %q recorded runs %v, want nothing", tc.status, pts)
				}
				if _, ok := m["audiobook_organizer.ops.run.duration"]; ok {
					t.Errorf("status %q recorded a duration, want nothing", tc.status)
				}
				return
			}
			if pts["t.mapping/"+tc.want] != 1 || len(pts) != 1 {
				t.Errorf("status %q recorded runs %v, want only t.mapping/%s=1", tc.status, pts, tc.want)
			}
		})
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
	rec.Finished("t.keys", "failed", time.Second)
	rec.Items("t.keys", ItemsProcessed, 1)
	unreg := rec.RegisterInflight(func() map[InflightKey]InflightStat {
		return map[InflightKey]InflightStat{{DefID: "t.keys", Plugin: "p"}: {Runs: 1, ProgressAge: time.Second}}
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
	cur := map[InflightKey]InflightStat{{DefID: "t.inflight", Plugin: "p"}: {Runs: 2, ProgressAge: 90 * time.Second}}
	unreg := rec.RegisterInflight(func() map[InflightKey]InflightStat { return cur })
	read := func() (int64, float64) {
		m := collect()
		g, ok := m["audiobook_organizer.ops.inflight"].Data.(metricdata.Gauge[int64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("want one inflight point, got %+v", g)
		}
		a, ok := m["audiobook_organizer.ops.progress_age"].Data.(metricdata.Gauge[float64])
		if !ok || len(a.DataPoints) != 1 {
			t.Fatalf("want one progress_age point, got %+v", a)
		}
		return g.DataPoints[0].Value, a.DataPoints[0].Value
	}
	if runs, age := read(); runs != 2 || age != 90 {
		t.Fatalf("inflight = %d, progress_age = %v; want 2 and 90", runs, age)
	}
	cur = map[InflightKey]InflightStat{}
	if runs, age := read(); runs != 0 || age != 0 {
		t.Errorf("after the runs ended inflight = %d, progress_age = %v; want 0 and 0", runs, age)
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
			{DefID: "unregistered-a", Plugin: "p"}: {Runs: 1, ProgressAge: 10 * time.Second},
			{DefID: "unregistered-b", Plugin: "p"}: {Runs: 2, ProgressAge: 40 * time.Minute},
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
}

// TestDeclaredButUnwiredAreListed pins the six spec 11 §3.7 instruments that
// are declared but not created here. Creating one needs an ourInstruments
// declaration, a golden row and a seed in internal/telemetry/contract, which
// is how the task that wires it (05-PR5) moves it out of Unwired.
func TestDeclaredButUnwiredAreListed(t *testing.T) {
	want := []string{
		"audiobook_organizer.ops.queue_depth",
		"audiobook_organizer.ops.fenced_writes",
		"audiobook_organizer.ops.zombies",
		"audiobook_organizer.ops.checkpoint_age",
		"audiobook_organizer.ops.schedule_lag",
		"audiobook_organizer.ops.schedule_missed",
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
	// None of them may be created by this package: the wired set is exactly five.
	rec, collect := newRig(t)
	RegisterDefID("t.unwired")
	rec.Started("t.unwired")
	rec.Finished("t.unwired", "completed", time.Second)
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
