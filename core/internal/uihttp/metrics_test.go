// file: core/internal/uihttp/metrics_test.go
// version: 1.0.0
// guid: a5d3c8e1-6f40-4b92-8c17-9e0b2d7f4a63
// last-edited: 2026-11-10

package uihttp

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/internal/modulehttp"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// emptyView is a modulehttp.ModulesView over nothing: this test registers
// modulehttp only so its counter joins the scope.
type emptyView struct{}

func (emptyView) Snapshot(context.Context) (modulehttp.Snapshot, error) {
	return modulehttp.Snapshot{}, nil
}

// TestUISeries_Golden (05-metrics §5 item 5, §12; 07 §7.16): the core "ui" scope
// serves exactly the families of testdata/series.golden. The scope is made once
// per Provider, so the kernel hands one *metrics.Scope to both route packages;
// this test does the same, which is why the one golden lives here, in the
// package that owns two of the three counters, and modulehttp's own tests check
// its counter by value (metrics_test.go there).
func TestUISeries_Golden(t *testing.T) {
	p, sc := uiScope(t)
	r := newRouter()
	if err := modulehttp.Register(r.CoreGroup(), modulehttp.Deps{
		Modules: emptyView{}, Documents: modulehttp.DocumentMap{}, Scope: sc,
	}); err != nil {
		t.Fatalf("modulehttp.Register: %v", err)
	}
	if err := Register(r.CoreGroup(), Deps{ModuleIDs: []string{"batman", "robin"}, Scope: sc}); err != nil {
		t.Fatalf("uihttp.Register: %v", err)
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)
}

// TestDiagnosticMetric_AttackerInputKeepsSeriesBounded: thousands of diagnostics
// naming distinct kinds and modules leave the family at its seeded series
// count, (kinds + other) x (registered modules + other); no series carries a
// submitted string.
func TestDiagnosticMetric_AttackerInputKeepsSeriesBounded(t *testing.T) {
	p, sc := uiScope(t)
	log, lines := capture()
	a, err := newAPI(Deps{ModuleIDs: []string{"batman", "robin"}, Scope: sc, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	const family = "mediacore_ui_diagnostics_total"
	g := p.Gatherer()
	want := (len(diagnosticKinds()) + 1) * (2 + 1)
	if got := len(seriesOf(t, g, family)); got != want {
		t.Fatalf("seeded %d diagnostic series, want %d", got, want)
	}
	for i := range 4000 {
		req := reportDiagnosticReq{Body: UIDiagnostic{Kind: fmt.Sprintf("riddle-%d", i), Module: fmt.Sprintf("enigma-%d", i)}}
		if err := req.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.reportDiagnostic(t.Context(), in(session(), req)); err != nil {
			t.Fatal(err)
		}
	}
	series := seriesOf(t, g, family)
	if len(series) != want {
		t.Errorf("%d diagnostic series after the attack, want %d", len(series), want)
	}
	kinds := map[string]bool{otherLabel: true}
	for _, k := range diagnosticKinds() {
		kinds[k] = true
	}
	mods := map[string]bool{otherLabel: true, "batman": true, "robin": true}
	for _, s := range series {
		if !kinds[s["kind"]] || !mods[s["module"]] {
			t.Errorf("series with a submitted label: %v", s)
		}
	}
	if v, _ := sample(t, g, family, map[string]string{"kind": otherLabel, "module": otherLabel}); v != "4000" {
		t.Errorf("other/other = %s, want 4000", v)
	}
	metricstest.AssertNoOverflow(t, p)
	if lines.String() == "" {
		t.Error("nothing was logged")
	}
}

// TestRegister_NilScopeRecordsNothing: Deps with no scope still register and
// serve, and a handler call records nowhere.
func TestRegister_NilScopeRecordsNothing(t *testing.T) {
	a, err := newAPI(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if a.metrics != nil {
		t.Fatal("a nil scope built metrics")
	}
	if _, err := a.reportDiagnostic(t.Context(), in(session(), reportDiagnosticReq{Body: UIDiagnostic{Kind: "ui-missing"}})); err != nil {
		t.Error(err)
	}
	a.metrics.recordCSP(t.Context(), "img-src")
}
