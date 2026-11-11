// file: core/internal/modulehttp/metrics_test.go
// version: 1.0.0
// guid: d1dd6f4c-abe6-410f-a9ee-488f34afed69
// last-edited: 2026-11-10

package modulehttp

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/metrics/metricstest"
)

const manifestFamily = "mediacore_ui_manifest_requests_total"

// TestManifestMetric_OutcomeCounts (07 §7.16): <ns>_ui_manifest_requests_total
// {outcome} is exported from zero for each of the four outcomes before any
// request, and each answer GET /modules can give moves exactly its own series.
// The "ui" scope's golden (which also lists uihttp's two families) is asserted in
// uihttp, whose test registers both packages on one scope.
func TestManifestMetric_OutcomeCounts(t *testing.T) {
	p, sc := uiScope(t)
	view := &fakeView{snap: gotham()}
	a, err := newAPI(Deps{Modules: view, Documents: DocumentMap{}, Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	g := p.Gatherer()
	series, err := metricstest.Gather(g, "mediacore_ui_")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Family != manifestFamily || series[0].Type != "counter" ||
		len(series[0].Labels) != 1 || series[0].Labels[0] != "outcome" {
		t.Fatalf("the scope serves %v, want only %s{outcome}", series, manifestFamily)
	}
	for _, o := range []string{"ok", "admin", "unauthenticated", "error"} {
		if v, ok := sample(t, g, manifestFamily, map[string]string{"outcome": o}); !ok || v != 0 {
			t.Errorf("outcome %q before any request = %v, %v, want a zero series", o, v, ok)
		}
	}

	ctx := context.Background()
	_, _ = a.listModules(ctx, in(user(), listModulesReq{}))
	_, _ = a.listModules(ctx, in(user(), listModulesReq{}))
	_, _ = a.listModules(ctx, in(admin(), listModulesReq{}))
	_, _ = a.listModules(ctx, in[listModulesReq](nil, listModulesReq{}))
	view.err = errors.New("catalog unavailable")
	_, _ = a.listModules(ctx, in(user(), listModulesReq{}))
	for o, want := range map[string]float64{"ok": 2, "admin": 1, "unauthenticated": 1, "error": 1} {
		if v, _ := sample(t, g, manifestFamily, map[string]string{"outcome": o}); v != want {
			t.Errorf("outcome %q = %v, want %v", o, v, want)
		}
	}
	metricstest.AssertNoOverflow(t, p)
}

// TestManifestMetric_NilScopeRecordsNothing: Deps with no scope still serve.
func TestManifestMetric_NilScopeRecordsNothing(t *testing.T) {
	a, err := newAPI(Deps{Modules: &fakeView{snap: gotham()}, Documents: DocumentMap{}})
	if err != nil {
		t.Fatal(err)
	}
	if a.metrics != nil {
		t.Fatal("a nil scope built a metrics recorder")
	}
	if _, err := a.listModules(context.Background(), in(user(), listModulesReq{})); err != nil {
		t.Errorf("listModules without metrics = %v", err)
	}
}

// TestManifestMetric_SameScopeTwice: two registrations of the counter on one
// scope are not an error (the SDK returns the one instrument), so a kernel
// that registers both route groups on a shared scope cannot trip it.
func TestManifestMetric_SameScopeTwice(t *testing.T) {
	_, sc := uiScope(t)
	for range 2 {
		if _, err := newMetrics(sc); err != nil {
			t.Fatalf("newMetrics on a shared scope = %v", err)
		}
	}
}
