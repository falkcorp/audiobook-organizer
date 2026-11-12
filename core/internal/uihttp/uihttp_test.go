// file: core/internal/uihttp/uihttp_test.go
// version: 1.0.1
// guid: 4d9f1b27-8c3a-4e56-b7d0-2a6e5c8f1b93
// last-edited: 2026-11-11

package uihttp

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

func register(t testing.TB, d Deps) *httpapi.Router {
	t.Helper()
	r := newRouter()
	if err := Register(r.CoreGroup(), d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r
}

// TestRegister_RouteTable pins 05-http-api §5 item 2 and 07 §7.5.2, §7.6.2:
// each route's method, pattern, kind, permission, guards and rate class, and the
// explicit body limits. The CSP route is the one Public route, at the RateAuth
// class; the diagnostics route is AnyAuthenticated at the default class.
func TestRegister_RouteTable(t *testing.T) {
	want := []struct {
		pattern, name string
		kind          httpapi.RouteKind
		perm          rbac.RequirementKind
		rate          httpapi.RateClass
	}{
		{"/api/core/v1/ui/diagnostics", "reportUIDiagnostic", httpapi.RouteKindNative, rbac.KindAuthenticated, httpapi.RateDefault},
		{"/api/core/v1/ui/csp-report", "reportCSPViolation", httpapi.RouteKindRaw, rbac.KindPublic, httpapi.RateAuth},
	}
	got := routes(register(t, Deps{}))
	if len(got) != len(want) {
		t.Fatalf("%d routes registered, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		ri := got[i]
		if ri.Owner != "core" || ri.Method != http.MethodPost || ri.Pattern != w.pattern || ri.Name != w.name || ri.Kind != w.kind {
			t.Errorf("route %d = %s %s %s %s kind %d, want core POST %s %s kind %d", i, ri.Owner, ri.Method, ri.Pattern, ri.Name, ri.Kind, w.pattern, w.name, w.kind)
		}
		if ri.Perm.IsZero() || ri.Perm.Kind() != w.perm {
			t.Errorf("%s requires %s (kind %d), want kind %d", w.name, ri.Perm, ri.Perm.Kind(), w.perm)
		}
		if ri.RateClass != w.rate {
			t.Errorf("%s rate class = %d, want %d", w.name, ri.RateClass, w.rate)
		}
		// Neither route changes a credential: a Public route has no principal to
		// guard, and the diagnostics route must stay reachable for the
		// auth-disabled principal and a key, so no Interactive or Owner guard.
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v, want none", w.name, ri.Guards)
		}
	}
	if m := diagnosticsRoute().MaxBody; m != 4<<10 {
		t.Errorf("diagnostics MaxBody = %d, want 4 KiB (07 §7.5.2)", m)
	}
	if m := cspRoute().MaxBody; m != 8<<10 {
		t.Errorf("csp-report MaxBody = %d, want 8 KiB (07 §7.6.2)", m)
	}
	if !diagnosticsRoute().Lenient {
		t.Error("diagnostics is strict: a later shell member would be a 400")
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public native route with ErrNoAuthService. The CSP route is Public and
// builds, so exactly one is expected (diagnostics); a bad binding, schema,
// name or conflict would each be a further error.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := register(t, Deps{}).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		if !errors.Is(err, httpapi.ErrNoAuthService) {
			t.Fatalf("Build reported %v, want only ErrNoAuthService", err)
		}
		return
	}
	problems := joined.Unwrap()
	if len(problems) != 1 {
		t.Errorf("Build reported %d problems, want 1 ErrNoAuthService (the diagnostics route): %v", len(problems), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// TestModuleGroup_IsAuthenticated (05-http-api §12): every non-Public route
// requires at least AnyAuthenticated and the diagnostics handler refuses a
// request with no principal (stage 13 is not built yet); the one Public route
// is csp-report, which reads no identity at all.
func TestModuleGroup_IsAuthenticated(t *testing.T) {
	var public []string
	for _, ri := range routes(register(t, Deps{})) {
		switch ri.Perm.Kind() {
		case rbac.KindPublic:
			public = append(public, ri.Name)
		case rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindAnyOf:
		default:
			t.Errorf("%s requires %s, which is not an authenticated requirement", ri.Name, ri.Perm)
		}
	}
	if !reflect.DeepEqual(public, []string{"reportCSPViolation"}) {
		t.Errorf("Public routes = %v, want only reportCSPViolation", public)
	}

	a, err := newAPI(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.reportDiagnostic(context.Background(), in[reportDiagnosticReq](nil, reportDiagnosticReq{Body: UIDiagnostic{Kind: "ui-missing"}}))
	wantStatus(t, err, http.StatusUnauthorized, "authentication_required")
}

// TestBuild_RejectsCamelCaseJSON (05-http-api §12): a native type with a
// createdAt tag is ErrJSONName at Build, over a twin of the diagnostics route.
func TestBuild_RejectsCamelCaseJSON(t *testing.T) {
	type camelBody struct {
		CreatedAt string `json:"createdAt"`
	}
	type camelReq struct {
		Body camelBody `body:"json"`
	}
	r := newRouter()
	httpapi.Handle(r.CoreGroup(), diagnosticsRoute(), func(context.Context, *httpapi.In[camelReq]) (httpapi.NoContent, error) {
		return httpapi.NoContent{}, nil
	})
	if _, err := r.Build(); !errors.Is(err, httpapi.ErrJSONName) {
		t.Fatalf("Build = %v, want ErrJSONName", err)
	}
}

// TestDiagnostic_Validate: a kind is required and bounded, a module is bounded,
// and a refusal names the field and never the value.
func TestDiagnostic_Validate(t *testing.T) {
	long := strings.Repeat("x", labelMax+1)
	for name, c := range map[string]struct {
		body   UIDiagnostic
		fields []string
	}{
		"ok":                {UIDiagnostic{Kind: "ui-missing", Module: "robin"}, nil},
		"ok without module": {UIDiagnostic{Kind: "core-version-incompatible"}, nil},
		"no kind":           {UIDiagnostic{Module: "robin"}, []string{"/body/kind"}},
		"kind too long":     {UIDiagnostic{Kind: long}, []string{"/body/kind"}},
		"module too long":   {UIDiagnostic{Kind: "ui-missing", Module: long}, []string{"/body/module"}},
		"both bad":          {UIDiagnostic{Kind: long, Module: long}, []string{"/body/kind", "/body/module"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := (&reportDiagnosticReq{Body: c.body}).Validate()
			if c.fields == nil {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			he, ok := errors.AsType[*httpapi.Error](err)
			if !ok || he.Status != http.StatusUnprocessableEntity {
				t.Fatalf("Validate = %v, want a 422", err)
			}
			var got []string
			for _, f := range he.Fields {
				got = append(got, f.Field)
				if strings.Contains(f.Message, "xxx") {
					t.Errorf("the refusal echoes the value: %q", f.Message)
				}
			}
			if !reflect.DeepEqual(got, c.fields) {
				t.Errorf("refused fields = %v, want %v", got, c.fields)
			}
		})
	}
}

// TestReportDiagnostic_LabelsAreClosedSets (07 §7.16): a known kind and a
// registered module are counted as themselves, anything else as other, the
// log carries only those labels, and the answer is the same 204 either way.
func TestReportDiagnostic_LabelsAreClosedSets(t *testing.T) {
	p, sc := uiScope(t)
	log, lines := capture()
	a, err := newAPI(Deps{ModuleIDs: []string{"batman", "robin"}, Scope: sc, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	report := func(kind, module string) {
		t.Helper()
		if _, err := a.reportDiagnostic(ctx, in(session(), reportDiagnosticReq{Body: UIDiagnostic{Kind: kind, Module: module}})); err != nil {
			t.Fatalf("report(%q, %q) = %v", kind, module, err)
		}
	}
	const family = "mediacore_ui_diagnostics_total"
	report("ui-missing", "robin")
	report("ui-missing", "robin")
	report("version-incompatible", "")
	report("app-id-mismatch", "batman")
	report("Joker'sPlan", "robin")
	report("ui-missing", "scarecrow-<script>")
	report("mystery", "scarecrow")

	g := p.Gatherer()
	for labels, want := range map[[2]string]string{
		{"ui-missing", "robin"}:           "2",
		{"version-incompatible", "other"}: "1",
		{"app-id-mismatch", "batman"}:     "1",
		{"other", "robin"}:                "1",
		{"ui-missing", "other"}:           "1",
		{"other", "other"}:                "1",
	} {
		if got, ok := sample(t, g, family, map[string]string{"kind": labels[0], "module": labels[1]}); !ok || got != want {
			t.Errorf("%v = %q, %v, want %s", labels, got, ok, want)
		}
	}
	for _, leak := range []string{"Joker", "scarecrow", "script", "mystery"} {
		if strings.Contains(lines.String(), leak) {
			t.Errorf("the log carries submitted text %q:\n%s", leak, lines)
		}
	}
	if !strings.Contains(lines.String(), "kind=ui-missing") || !strings.Contains(lines.String(), "module=robin") {
		t.Errorf("the log has no bounded labels:\n%s", lines)
	}
}

// TestReportDiagnostic_AcceptOff (07 §7.15 core.ui.diagnostics.accept): with the
// key false the answer is unchanged and nothing is counted or logged; the
// key is read at each request.
func TestReportDiagnostic_AcceptOff(t *testing.T) {
	p, sc := uiScope(t)
	log, lines := capture()
	accept := false
	a, err := newAPI(Deps{Scope: sc, Log: log, AcceptDiagnostics: func() bool { return accept }})
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		t.Helper()
		out, err := a.reportDiagnostic(context.Background(), in(session(), reportDiagnosticReq{Body: UIDiagnostic{Kind: "ui-missing"}}))
		if err != nil || out != (httpapi.NoContent{}) {
			t.Fatalf("report = %v, %v", out, err)
		}
	}
	send()
	if v, _ := sample(t, p.Gatherer(), "mediacore_ui_diagnostics_total", map[string]string{"kind": "ui-missing", "module": "other"}); v != "0" {
		t.Errorf("a refused diagnostic was counted: %s", v)
	}
	if lines.String() != "" {
		t.Errorf("a refused diagnostic was logged: %s", lines)
	}
	accept = true
	send()
	if v, _ := sample(t, p.Gatherer(), "mediacore_ui_diagnostics_total", map[string]string{"kind": "ui-missing", "module": "other"}); v != "1" {
		t.Errorf("an accepted diagnostic = %s, want 1", v)
	}
}

// TestReportDiagnostic_NeedsAnAuthenticatedPrincipal: the handler applies
// AnyAuthenticated itself until stage 13 exists. No principal is 401; a
// principal with no compiled permission set, and a Confined key, are 403, and
// neither is counted nor logged.
func TestReportDiagnostic_NeedsAnAuthenticatedPrincipal(t *testing.T) {
	log, lines := capture()
	a, err := newAPI(Deps{ModuleIDs: []string{"batman"}, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		p      *auth.Principal
		status int
	}{
		"no principal":               {nil, http.StatusUnauthorized},
		"no compiled permission set": {&auth.Principal{Method: auth.MethodSession}, http.StatusForbidden},
		"confined key":               {confinedKey(), http.StatusForbidden},
	} {
		_, err := a.reportDiagnostic(context.Background(), in(c.p, reportDiagnosticReq{Body: UIDiagnostic{Kind: "ui-missing"}}))
		he, ok := errors.AsType[*httpapi.Error](err)
		if !ok || he.Status != c.status {
			t.Errorf("%s: err = %v, want status %d", name, err, c.status)
		}
	}
	if got := lines.String(); strings.Contains(got, "ui diagnostic") {
		t.Errorf("a refused report was logged: %q", got)
	}
}
