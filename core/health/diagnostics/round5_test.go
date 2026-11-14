// file: core/health/diagnostics/round5_test.go
// version: 1.0.0
// guid: 5b1f8c36-92d4-4e07-a3b8-6d70c2e19f54
// last-edited: 2026-11-14

package diagnostics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/tasks"
)

// TestDiagnostics_BuildRefusesAMismatchedActor (round 5, N-7): Build is
// exported, so a caller other than Export can reach it with an actor that is
// not the person the params name. Only Export used to check; Build now refuses
// by itself, and writes no file.
func TestDiagnostics_BuildRefusesAMismatchedActor(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	for name, actor := range map[string]tasks.Actor{
		"run-other":  {UserID: "robin", AuthMethod: "session"},
		"run-system": {System: "core"},
		"run-blank":  {},
	} {
		_, err := e.Build(context.Background(), name, actor, diagnostics.ExportParams{ActorID: "bruce"}, nil)
		if !errors.Is(err, diagnostics.ErrActorMismatch) {
			t.Errorf("%s: err = %v, want ErrActorMismatch", name, err)
		}
		if _, serr := f.root.Stat(name + ".zip"); serr == nil {
			t.Errorf("%s: a refused build wrote a bundle", name)
		}
		if _, serr := f.root.Stat(name + ".zip.tmp"); serr == nil {
			t.Errorf("%s: a refused build left a temp file", name)
		}
	}
	if _, err := e.Build(context.Background(), "run-same", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}, nil); err != nil {
		t.Errorf("the matching actor was refused: %v", err)
	}
}

// TestDiagnostics_RouteQueryRound5 runs the round-5 shapes through a real
// bundle: the value road and the text-line road both, with runPathCases
// failing the test if any input changed the number of log lines.
//
// N-1: a quote, bracket, comma or pipe inside a query does not end the route.
// N-2: a query that does not decode is redacted, not passed through.
// N-3: a rewritten value never writes a decoded line break.
// N-4: symbols between slashes are not a path.
// N-5: a doubled slash inside a route's path starts a path.
func TestDiagnostics_RouteQueryRound5(t *testing.T) {
	const browse = "/api/v1/filesystem/browse?path="
	runPathCases(t, []pathCase{
		same("/api/v1/x?a=O'Brien&p=%2FUsers%2Fbruce", "/api/v1/x?a=O'Brien&p=<path>"),
		same("/api/v1/x?a=b)c&p=%2FUsers%2Fbruce", "/api/v1/x?a=b)c&p=<path>"),
		same("/api/v1/x?a=b,c&p=%2FUsers%2Fbruce", "/api/v1/x?a=b,c&p=<path>"),
		same("/api/v1/x?a=b|c&p=%2FUsers%2Fbruce", "/api/v1/x?a=b|c&p=<path>"),
		same("/api/v1/x?a=b]c&p=%2FUsers%2Fbruce", "/api/v1/x?a=b]c&p=<path>"),
		same("/api/v1/x?a=b;c&p=%2FUsers%2Fbruce", "/api/v1/x?a=b;c&p=<path>"),
		same(browse+"%2FUsers%2Fbruce%2F50%", browse+"<path>"),
		same(browse+"%2FUsers%2Fbruce%zz", browse+"<path>"),
		same("/api/v1/x?a=50%&path=%2FUsers%2Fbruce", "/api/v1/x?a=50%&path=<path>"),
		same("GET "+browse+"%2FUsers%2Fbruce%0Aforged-line 200", "GET "+browse+"<path>%0Aforged-line 200"),
		same("GET "+browse+"%2FUsers%2Fbruce%0D%0Aforged 200", "GET "+browse+"<path>%0D%0Aforged 200"),
		same("/api/v1/x?msg=see%20%2FUsers%2Fbruce%20now", "/api/v1/x?msg=see%20<path>%20now"),
		same("/api/v1/x?m=%E2%80%A8%2FUsers%2Fbruce", "/api/v1/x?m=%E2%80%A8<path>"),
		same("/api/v1/x//Users/bruce", "/api/v1/x/<path>"),
		same("/api/v1/x/%2FUsers%2Fbruce", "/api/v1/x/<path>"),
		same("a*/home/bruce/x", "a*<path>"),
		same("*/15 * * * *", "*/15 * * * *"),
		same("50%/50%", "50%/50%"),
		same("(read)/(write)", "(read)/(write)"),
		same("+/-", "+/-"),
	})
}
