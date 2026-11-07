// file: core/metrics/metricstest/metricstest_test.go
// version: 1.0.0
// guid: 931f19fd-0737-46bf-b1a1-8d1d6ffdb017
// last-edited: 2026-11-07

package metricstest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// fakeTB records failures instead of failing the real test, so the
// assertions can be shown to fail.
type fakeTB struct {
	testing.TB
	msgs []string
}

func (f *fakeTB) Helper()                   {}
func (f *fakeTB) Errorf(s string, a ...any) { f.msgs = append(f.msgs, fmt.Sprintf(s, a...)) }
func (f *fakeTB) Fatalf(s string, a ...any) { f.msgs = append(f.msgs, fmt.Sprintf(s, a...)) }
func (f *fakeTB) failed() bool              { return len(f.msgs) > 0 }
func (f *fakeTB) joined() string            { return strings.Join(f.msgs, "\n") }
func newFake(t *testing.T) *fakeTB          { return &fakeTB{TB: t} }
func ctx() context.Context                  { return context.Background() }
func villain(v string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("villain", v))
}

// provider builds a Provider for the one module "books" under the gotham
// namespace, and that module's scope.
func provider(t *testing.T, o metrics.Options) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	o.AppID, o.MetricNamespace, o.Modules, o.Prometheus = "gotham", "gotham", []string{"books"}, true
	p, err := metrics.New(ctx(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx()) })
	s, err := p.Module("books", "villain")
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func golden(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "series.golden")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAssertSeries_MatchesAndCatchesDrift(t *testing.T) {
	p, s := provider(t, metrics.Options{})
	c, err := s.Meter().Int64Counter("gotham.arrests")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx(), 1, villain("joker"))

	metricstest.AssertSeries(t, p, s, golden(t, "# the module's one family\ngotham_arrests_total\tcounter\tvillain\n"))

	for name, body := range map[string]string{
		"changed label set": "gotham_arrests_total\tcounter\tvillain,precinct\n",
		"changed type":      "gotham_arrests_total\tgauge\tvillain\n",
		"missing family":    "gotham_arrests_total\tcounter\tvillain\ngotham_escapes_total\tcounter\tvillain\n",
		"unlisted family":   "",
	} {
		f := newFake(t)
		metricstest.AssertSeries(f, p, s, golden(t, body))
		if !f.failed() {
			t.Errorf("%s: AssertSeries passed, want a failure", name)
		}
	}
}

// TestCardinality_OverflowFailsMetricstest: past CardinalityLimit the SDK
// folds new attribute sets into one otel.metric.overflow series, which
// AssertNoOverflow reports.
func TestCardinality_OverflowFailsMetricstest(t *testing.T) {
	p, s := provider(t, metrics.Options{CardinalityLimit: 3})
	c, err := s.Meter().Int64Counter("gotham.sightings")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx(), 1, villain("joker"))
	metricstest.AssertNoOverflow(t, p)

	for _, v := range []string{"penguin", "riddler", "bane", "scarecrow"} {
		c.Add(ctx(), 1, villain(v))
	}
	fams, err := metricstest.Overflowed(p.Gatherer())
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || fams[0] != "gotham_sightings_total" {
		t.Fatalf("Overflowed = %v, want [gotham_sightings_total]", fams)
	}
	f := newFake(t)
	metricstest.AssertNoOverflow(f, p)
	if !f.failed() || !strings.Contains(f.joined(), "gotham_sightings_total") {
		t.Errorf("AssertNoOverflow did not fail naming the family: %q", f.joined())
	}
}
