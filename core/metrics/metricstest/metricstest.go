// file: core/metrics/metricstest/metricstest.go
// version: 1.0.0
// guid: 4f2dfcb7-ca71-42d6-abb6-db170b75d0d0
// last-edited: 2026-11-07

// Package metricstest is the test kit for core metrics scopes (05-metrics §5
// rules 3 and 5, §6 items 3 and 5):
//
//   - AssertSeries compares the families a scope serves (name, type, label
//     set) with that scope's testdata/series.golden and fails with the diff;
//   - AssertNoOverflow fails when any instrument reached the SDK's
//     per-instrument cardinality limit, whose signal is the overflow series
//     the SDK records under otel.metric.overflow=true.
//
// Every row that adds a core metrics scope adds its golden and calls
// AssertSeries from that scope's tests.
package metricstest

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
)

// OverflowLabel is the Prometheus label of the SDK's overflow series
// (attribute otel.metric.overflow, translated).
const OverflowLabel = "otel_metric_overflow"

// Series is one golden row: a family, its Prometheus type and its sorted
// label names (le and quantile excluded).
type Series struct {
	Family string
	Type   string
	Labels []string
}

// String is the golden line: family<TAB>type<TAB>label,label.
func (s Series) String() string {
	return s.Family + "\t" + s.Type + "\t" + strings.Join(s.Labels, ",")
}

// FamilyPrefix is the Prometheus family-name prefix of a scope's instruments:
// its Prefix with dots translated, plus "_".
func FamilyPrefix(s *metrics.Scope) string {
	return strings.ReplaceAll(s.Prefix(), ".", "_") + "_"
}

// Gather returns the series g serves whose family starts with prefix, sorted
// by family.
func Gather(g prometheus.Gatherer, prefix string) ([]Series, error) {
	mfs, err := g.Gather()
	if err != nil {
		return nil, err
	}
	var out []Series
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), prefix) {
			continue
		}
		labels := map[string]bool{}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = true
			}
		}
		names := make([]string, 0, len(labels))
		for l := range labels {
			names = append(names, l)
		}
		slices.Sort(names)
		out = append(out, Series{Family: mf.GetName(), Type: typeName(mf.GetType()), Labels: names})
	}
	slices.SortFunc(out, func(a, b Series) int { return strings.Compare(a.Family, b.Family) })
	return out, nil
}

func typeName(t dto.MetricType) string {
	switch t {
	case dto.MetricType_COUNTER:
		return "counter"
	case dto.MetricType_GAUGE:
		return "gauge"
	case dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
		return "histogram"
	case dto.MetricType_SUMMARY:
		return "summary"
	}
	return "untyped"
}

// ReadGolden reads a series golden: one family<TAB>type<TAB>labels line per
// family, labels comma-separated and sorted; blank lines and # comments are
// ignored.
func ReadGolden(path string) ([]Series, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Series
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 {
			return nil, fmt.Errorf("%s:%d: want family<TAB>type<TAB>labels, got %d columns", path, i+1, len(cols))
		}
		s := Series{Family: cols[0], Type: cols[1]}
		if cols[2] != "" {
			s.Labels = strings.Split(cols[2], ",")
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Series) int { return strings.Compare(a.Family, b.Family) })
	return out, nil
}

// Diff lists every difference between want (the golden) and got (the
// scrape): a missing family, an extra family, a changed type or label set.
func Diff(want, got []Series) []string {
	g := map[string]Series{}
	for _, s := range got {
		g[s.Family] = s
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range want {
		seen[w.Family] = true
		s, ok := g[w.Family]
		switch {
		case !ok:
			out = append(out, "missing family: "+w.String())
		case s.Type != w.Type || !slices.Equal(s.Labels, w.Labels):
			out = append(out, "changed family: golden "+w.String()+" | served "+s.String())
		}
	}
	for _, s := range got {
		if !seen[s.Family] {
			out = append(out, "family not in golden: "+s.String())
		}
	}
	return out
}

// AssertSeries fails t unless the families of scope s that p serves equal the
// golden at goldenPath, reporting the diff. A family with no sample exports
// nothing, so the caller records one sample per golden row first. Families
// are selected by FamilyPrefix(s); a module scope of a single-module app has
// the namespace alone as its prefix, so build its Provider without core
// scopes when asserting it.
func AssertSeries(t testing.TB, p *metrics.Provider, s *metrics.Scope, goldenPath string) {
	t.Helper()
	want, err := ReadGolden(goldenPath)
	if err != nil {
		t.Fatalf("metricstest: read golden: %v", err)
		return
	}
	got, err := Gather(p.Gatherer(), FamilyPrefix(s))
	if err != nil {
		t.Fatalf("metricstest: gather: %v", err)
		return
	}
	if d := Diff(want, got); len(d) > 0 {
		t.Errorf("metricstest: scope %s does not match %s:\n\t%s", s.Prefix(), goldenPath, strings.Join(d, "\n\t"))
	}
}

// Overflowed lists the families of g that carry the SDK's overflow series:
// an instrument whose series count reached the cardinality limit.
func Overflowed(g prometheus.Gatherer) ([]string, error) {
	mfs, err := g.Gather()
	var out []string
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			if slices.ContainsFunc(m.GetLabel(), func(lp *dto.LabelPair) bool {
				return lp.GetName() == OverflowLabel && lp.GetValue() == "true"
			}) {
				out = append(out, mf.GetName())
				break
			}
		}
	}
	return out, err
}

// AssertNoOverflow fails t when any instrument of p reached the per-instrument
// cardinality limit (core.metrics.cardinality_limit): the overflow series
// means data for that instrument is being merged and lost (05-metrics §5 rule
// 3).
func AssertNoOverflow(t testing.TB, p *metrics.Provider) {
	t.Helper()
	fams, err := Overflowed(p.Gatherer())
	if err != nil {
		t.Fatalf("metricstest: gather: %v", err)
		return
	}
	if len(fams) > 0 {
		t.Errorf("metricstest: cardinality limit reached (overflow series present) in %s", strings.Join(fams, ", "))
	}
}
