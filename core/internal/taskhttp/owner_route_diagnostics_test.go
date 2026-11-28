// file: core/internal/taskhttp/owner_route_diagnostics_test.go
// version: 0.1.0
// guid: 187d1106-d9f6-4d28-81f3-7434ef36a878
// last-edited: 2026-11-28

package taskhttp

import (
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

type fakeExportBackend struct{}

func (fakeExportBackend) Export(*tasks.RC, diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	return diagnostics.ExportResult{}, nil
}

// TestStart_DiagnosticsExportRefusedOnTheGenericRoute: core.diagnostics.export
// reads ExportParams.Perms as a ceiling on what the starter may read, and only
// the diagnostics route stamps it. A caller who holds the Start scope and writes
// a wider snapshot of their own into the generic start's params (every scope, or
// core:admin, for another actor's id) is answered 403 tasks.owner_route_only
// whatever their method, and nothing reaches Client.Start. A caller short of
// core:system.manage still gets the plain permission answer, and the listing
// says the kind cannot be started here.
func TestStart_DiagnosticsExportRefusedOnTheGenericRoute(t *testing.T) {
	const manage = rbac.Permission("core:system.manage")
	g := newRig(t, func(d *Deps) { d.Definitions = append(defs(), diagnostics.ExportTask(fakeExportBackend{})) })
	path := "/definitions/" + string(diagnostics.ExportKind) + "/runs"
	forged := `{"params": {"actor_id": "alfred", "perms": ["core:admin", "core:system.manage", "core:backup.restore"]}}`

	for name, as := range map[string]*auth.Principal{
		"session": session("lucius", manage),
		"api key": who("lucius", auth.MethodAPIKey, manage),
	} {
		t.Run(name, func(t *testing.T) {
			rec := g.do(req{method: "POST", path: path, body: forged, as: as})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403\n%s", rec.Code, rec.Body.String())
			}
			wantProblem(t, rec, http.StatusForbidden, "tasks.owner_route_only")
		})
	}
	wantProblem(t, g.do(req{method: "POST", path: path, body: forged, as: session("lucius", scopeSee)}), http.StatusForbidden, "permission_denied")
	if n := len(g.client.starts); n != 0 {
		t.Fatalf("a refused start reached Client.Start %d times", n)
	}

	var one definitionDTO
	rec := g.do(req{method: "GET", path: "/definitions/" + string(diagnostics.ExportKind), as: session("lucius", manage, "core:tasks.view")})
	decode(t, rec, &one)
	if rec.Code != http.StatusOK || one.CanStart {
		t.Fatalf("listing = %d %+v, want can_start false", rec.Code, one)
	}
}
