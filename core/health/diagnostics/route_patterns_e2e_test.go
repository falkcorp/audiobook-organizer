// file: core/health/diagnostics/route_patterns_e2e_test.go
// version: 1.0.0
// guid: c4a9e1d7-3b58-4f26-8e0a-91d5b7c2f630
// last-edited: 2026-11-16

package diagnostics_test

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
)

// routeBundle builds one bundle whose section holds the given route and
// pattern values and returns them as written.
func routeBundle(t *testing.T, patterns func() []string, run string, route, pattern string) map[string]any {
	t.Helper()
	f := newFixture(t)
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.RoutePatterns = patterns
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.routes", map[string]any{
			"route": route, "pattern": pattern, "msg": "GET /api/v1/books/{id}",
		})}
	})
	res, err := f.build(e, run, diagnostics.ExportParams{ActorID: "bruce"})
	if err != nil {
		t.Fatal(err)
	}
	files := scanBundle(t, f, res.File)
	var d map[string]any
	if err := json.Unmarshal(files["batcave.routes/d.json"], &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRoutePatterns_ConfigWiring: Config.RoutePatterns decides what a bundle
// keeps in `route` and `pattern`. Unset, a template is wiped like any location;
// set, exactly a registered pattern is kept and a near miss with a name in a
// parameter position is not.
func TestRoutePatterns_ConfigWiring(t *testing.T) {
	const tmpl = "GET /api/v1/books/{id}"
	const files = "/api/v1/files/{path...}"
	d := routeBundle(t, nil, "run-rp-nil", tmpl, files)
	if d["route"] != "GET <path>" || d["pattern"] != "<path>" {
		t.Errorf("nil RoutePatterns kept a route: route=%v pattern=%v", d["route"], d["pattern"])
	}
	d = routeBundle(t, func() []string { return nil }, "run-rp-empty", tmpl, files)
	if d["route"] != "GET <path>" || d["pattern"] != "<path>" {
		t.Errorf("an empty list kept a route: route=%v pattern=%v", d["route"], d["pattern"])
	}
	both := func() []string { return []string{tmpl, files} }
	d = routeBundle(t, both, "run-rp-set", tmpl, files)
	if d["route"] != tmpl || d["pattern"] != files {
		t.Errorf("registered patterns were not kept: route=%v pattern=%v", d["route"], d["pattern"])
	}
	if d["msg"] != "GET <path>" {
		t.Errorf("msg = %v: the patterns apply to route and pattern only", d["msg"])
	}
	d = routeBundle(t, both, "run-rp-near", "GET /api/v1/books/Hush-Bruce-Wayne", "/api/v1/files/Users/bruce/Hush")
	if d["route"] != "GET <path>" || d["pattern"] != "<path>" {
		t.Errorf("a near miss was kept: route=%v pattern=%v", d["route"], d["pattern"])
	}
}

// TestRoutePatterns_ReadPerRun: the list is read once per bundle, at the start
// of the run, so a route registered after the exporter was built is known.
func TestRoutePatterns_ReadPerRun(t *testing.T) {
	const tmpl = "GET /api/v1/books/{id}"
	f := newFixture(t)
	calls := 0
	list := []string{}
	e := f.exporterWith(viewerOf("core:system.manage"), func(cfg *diagnostics.Config) {
		cfg.RoutePatterns = func() []string { calls++; return list }
		cfg.Contributors = []diagnostics.Contributor{jsonSection("batcave.routes", map[string]any{"route": tmpl})}
	})
	read := func(run string) any {
		res, err := f.build(e, run, diagnostics.ExportParams{ActorID: "bruce"})
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(scanBundle(t, f, res.File)["batcave.routes/d.json"], &d); err != nil {
			t.Fatal(err)
		}
		return d["route"]
	}
	if got := read("run-rpr-1"); got != "GET <path>" || calls != 1 {
		t.Errorf("first run: route=%v calls=%d, want a wiped route and 1 call", got, calls)
	}
	list = []string{tmpl}
	if got := read("run-rpr-2"); got != tmpl || calls != 2 {
		t.Errorf("second run: route=%v calls=%d, want the route kept and 2 calls", got, calls)
	}
}
