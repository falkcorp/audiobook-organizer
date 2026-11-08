// file: core/kernel/logmetrics_test.go
// version: 1.0.1
// guid: 2b9d5e71-4c08-4a3f-8e6b-d0a7c3f15e82
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// sample returns the value of the one series of family whose labels include
// every want pair, and whether it exists.
func sample(t *testing.T, k *Kernel, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := k.provider.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for name, val := range want {
				if !slices.ContainsFunc(m.GetLabel(), func(lp *dto.LabelPair) bool {
					return lp.GetName() == name && lp.GetValue() == val
				}) {
					continue next
				}
			}
			return m.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

func mustSample(t *testing.T, k *Kernel, family string, want map[string]string) float64 {
	t.Helper()
	v, ok := sample(t, k, family, want)
	if !ok {
		t.Fatalf("no %s series with labels %v", family, want)
	}
	return v
}

// TestLogMetrics_SeriesGolden (05-logging §9): the mediacore.log scope serves
// the three families of testdata/log/series.golden, a record, a tap drop and
// a redaction each raise their counter by one, and a level below debug is
// labelled as the pipeline renders it. One cell reads the families through
// the Provider's served /metrics handler, not through the bridge.
func TestLogMetrics_SeriesGolden(t *testing.T) {
	var out teeBuffer
	s := log.DefaultSettings()
	s.Level = "trace"
	k := mustNew(t, Config{AppID: "gotham", LogOutput: &out, Log: &s})
	if k.logScope == nil {
		t.Fatal("the kernel registered no mediacore.log scope")
	}
	l := k.CoreLogger("kernel")
	// A family with no sample exports nothing: one record gives records a row.
	l.Info("the lights are out")
	metricstest.AssertSeries(t, k.provider, k.logScope, "testdata/log/series.golden")

	rec := func() float64 {
		return mustSample(t, k, "mediacore_log_records_total", map[string]string{"module": "core", "level": "INFO"})
	}
	before := rec()
	l.Info("the bat signal")
	if got := rec(); got != before+1 {
		t.Errorf("records{core,INFO} = %v after one record, want %v", got, before+1)
	}

	red := func() float64 {
		return mustSample(t, k, "mediacore_log_redactions_total", map[string]string{"kind": "key"})
	}
	before = red()
	l.Info("login", "password", "Bruce")
	if got := red(); got != before+1 {
		t.Errorf("redactions{key} = %v after one redacted attr, want %v", got, before+1)
	}
	if strings.Contains(out.String(), "Bruce") {
		t.Errorf("the redacted value reached the output: %q", out.String())
	}

	// The process default logger counts under the empty module, served as the
	// label "process": an empty label value would read as absent.
	k.logging.Root().Info("the signal is lit")
	if got := mustSample(t, k, "mediacore_log_records_total", map[string]string{"module": "process", "level": "INFO"}); got != 1 {
		t.Errorf("records{process,INFO} = %v after one process record, want 1", got)
	}
	mfs, err := k.provider.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "mediacore_log_records_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if !slices.ContainsFunc(m.GetLabel(), func(lp *dto.LabelPair) bool {
				return lp.GetName() == "module" && lp.GetValue() != ""
			}) {
				t.Errorf("a records series has no module label (an empty one reads as absent): %v", m.GetLabel())
			}
		}
	}

	// A tap whose function never returns: the queue (1) fills and the rest of
	// the records are dropped, counted under the tap's name.
	release := make(chan struct{})
	stop := k.logging.Tap("riddler", slog.LevelInfo, 1, func(context.Context, log.Record) { <-release })
	for range 6 {
		l.Info("riddle me this")
	}
	dropped := mustSample(t, k, "mediacore_log_dropped_total", map[string]string{"sink": "riddler"})
	if want := float64(k.logging.Counters().Dropped["riddler"]); dropped != want || dropped < 1 {
		t.Errorf("dropped{riddler} = %v, want the pipeline's %v (and at least 1)", dropped, want)
	}
	before = dropped
	l.Info("one more")
	if got := mustSample(t, k, "mediacore_log_dropped_total", map[string]string{"sink": "riddler"}); got != before+1 {
		t.Errorf("dropped{riddler} = %v after one more drop, want %v", got, before+1)
	}
	if _, ok := sample(t, k, "mediacore_log_dropped_total", map[string]string{"sink": "primary"}); !ok {
		t.Error("dropped{primary} is not served; it is always present")
	}
	close(release)
	stop()

	// The label of a trace record is the one the line carries.
	l.Log(context.Background(), log.LevelTrace, "listening")
	if !strings.Contains(out.String(), "level=TRACE") {
		t.Fatalf("the pipeline did not render level=TRACE: %q", out.String())
	}
	mustSample(t, k, "mediacore_log_records_total", map[string]string{"module": "core", "level": "TRACE"})
	l.Log(context.Background(), log.LevelTrace+2, "closer")
	mustSample(t, k, "mediacore_log_records_total", map[string]string{"module": "core", "level": "TRACE+2"})

	// Through the served handler: the three families, as a scraper sees them.
	srv := httptest.NewServer(k.provider.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	families, err := scrapeFamilies(resp, "mediacore_log_")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mediacore_log_dropped_total", "mediacore_log_records_total", "mediacore_log_redactions_total"}
	if !slices.Equal(families, want) {
		t.Errorf("/metrics serves %v, want %v", families, want)
	}
}

// TestLogMetrics_NoScopeWhenLoggerInjected: a kernel that adopted
// Config.Logger has no pipeline to read, so no scope is registered.
func TestLogMetrics_NoScopeWhenLoggerInjected(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: slog.New(slog.DiscardHandler)})
	if k.logScope != nil {
		t.Error("a mediacore.log scope was registered with no pipeline")
	}
}
