// file: core/httpapi/corequirement_test.go
// version: 1.0.0
// guid: c6e8003e-bd97-4285-b5bf-7a3cac5cf795
// last-edited: 2026-11-28

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// App-declared route co-requirements (corequirement.go). Every cell builds a
// real Router with SetAuth and serves requests through the handler Build
// returns, so a co-requirement that is not in the chain fails here and not
// only in a unit test of combineRequirements.

const (
	cqCore  rbac.Permission = "core:integrations.manage"
	cqBooks rbac.Permission = "books:integrations.manage"
)

// cqAuthn is stage 12 for these cells: it binds the principal the X-Test-As
// header names.
type cqAuthn struct{ cat *rbac.Catalog }

func (f *cqAuthn) principal(name string) *auth.Principal {
	switch name {
	case "core-only": // a post-R2 native role: the core scope and nothing of the app's
		return s13Session(f.cat, cqCore)
	case "books-only":
		return s13Session(f.cat, cqBooks)
	case "both": // a migrated principal
		return s13Session(f.cat, cqCore, cqBooks)
	case "viewer":
		return s13Session(f.cat, "core:users.view")
	}
	return nil
}

func (f *cqAuthn) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p := f.principal(r.Header.Get("X-Test-As")); p != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (f *cqAuthn) SessionCookie(_ auth.ClientInfo, _ string, _ time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// cqRig is a router over the routes below.
type cqRig struct {
	r       *Router
	h       http.Handler
	reached atomic.Int64
	audit   *raAudit
}

// cqRoutes registers the routes every cell uses on r's core group:
//
//	POST /manage    manageThing   AllOf(core:integrations.manage)  (Handle)
//	GET  /thing     getThing      AllOf(core:integrations.manage)  (Handle)
//	GET  /rawthing  getRawThing   AllOf(core:integrations.manage)  (HandleRaw)
//	GET  /view      viewThing     AllOf(core:users.view)
//	GET  /either    eitherThing   AnyOf(core:users.view, core:users.manage)
//	GET  /open      openThing     Public
func (rg *cqRig) cqRoutes(r *Router) {
	g := r.CoreGroup()
	ok := func(context.Context, *In[struct{}]) (raOut, error) { rg.reached.Add(1); return raOut{OK: true}, nil }
	Handle(g, Route{Method: http.MethodPost, Path: "/manage", Name: "manageThing", Perm: rbac.Need(cqCore)}, ok)
	Handle(g, Route{Method: http.MethodGet, Path: "/thing", Name: "getThing", Perm: rbac.Need(cqCore)}, ok)
	HandleRaw(g, Route{Method: http.MethodGet, Path: "/rawthing", Name: "getRawThing", Perm: rbac.Need(cqCore)}, RawDoc{},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rg.reached.Add(1); w.WriteHeader(http.StatusOK) }))
	Handle(g, Route{Method: http.MethodGet, Path: "/view", Name: "viewThing", Perm: rbac.Need("core:users.view")}, ok)
	Handle(g, Route{Method: http.MethodGet, Path: "/either", Name: "eitherThing", Perm: rbac.AnyOf("core:users.view", "core:users.manage")}, ok)
	Handle(g, Route{Method: http.MethodGet, Path: "/open", Name: "openThing", Perm: rbac.Public}, ok)
}

// cqBuild builds a router with co as Options.CoRequirements, wiring auth over
// the catalog unless noAuth, and applying extra to the router before Build.
func cqBuild(t *testing.T, co map[string]rbac.Requirement, extra ...func(*cqRig, *Router)) (*cqRig, error) {
	t.Helper()
	return cqBuildWith(t, co, false, s13Catalog(t), extra...)
}

func cqBuildWith(t *testing.T, co map[string]rbac.Requirement, noAuth bool, cat *rbac.Catalog, extra ...func(*cqRig, *Router)) (*cqRig, error) {
	t.Helper()
	rg := &cqRig{audit: &raAudit{}}
	r := NewRouter(Options{CoRequirements: co, OpenAPI: OpenAPIOptions{Version: "1.0.0", CookieName: "hello_session"}}, Deps{})
	rg.r = r
	if !noAuth {
		if err := r.SetAuth(AuthWiring{Service: &cqAuthn{cat: s13Catalog(t)}, Catalog: cat, Audit: rg.audit}); err != nil {
			t.Fatalf("SetAuth: %v", err)
		}
	}
	rg.cqRoutes(r)
	for _, f := range extra {
		f(rg, r)
	}
	h, err := r.Build()
	rg.h = h
	return rg, err
}

// do sends one request as the named principal.
func (rg *cqRig) do(method, path, as string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/core/v1"+path, nil)
	req.RemoteAddr = "192.0.2.10:4000"
	if as != "" {
		req.Header.Set("X-Test-As", as)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	return rec
}

func cqCoReq(name string, req rbac.Requirement) map[string]rbac.Requirement {
	return map[string]rbac.Requirement{name: req}
}

// TestCoRequirement_NativeRoleWithOnlyCoreScopeIsRefused: with books:
// integrations.manage added to a route that needs core:integrations.manage, a
// native role holding only the core scope is refused 403 naming the app's
// scope (and audited as that deny), a principal holding both passes, and one
// holding only the app's scope is refused too: the requirement is an AND.
func TestCoRequirement_NativeRoleWithOnlyCoreScopeIsRefused(t *testing.T) {
	rg, err := cqBuild(t, cqCoReq("manageThing", rbac.Need(cqBooks)))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, c := range []struct {
		name, as, missing string
		status            int
	}{
		{"core scope only", "core-only", string(cqBooks), http.StatusForbidden},
		{"both scopes", "both", "", http.StatusOK},
		{"app scope only", "books-only", string(cqCore), http.StatusForbidden},
		{"neither", "viewer", string(cqCore), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := rg.reached.Load()
			rec := rg.do(http.MethodPost, "/manage", c.as)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, c.status, rec.Body)
			}
			if c.status == http.StatusForbidden {
				if p := s13Problem(t, rec); p.Code != "permission_denied" || p.Required != c.missing {
					t.Fatalf("problem %q requires %q, want permission_denied requiring %q", p.Code, p.Required, c.missing)
				}
			}
			if reached := rg.reached.Load() != before; reached != (c.status == http.StatusOK) {
				t.Fatalf("handler reached = %v with status %d", reached, c.status)
			}
		})
	}
	var denied []string
	for _, e := range rg.audit.all() {
		if e.Action == "core.permission.deny" {
			denied = append(denied, e.Permission)
		}
	}
	if want := []string{string(cqBooks), string(cqCore), string(cqCore)}; strings.Join(denied, ",") != strings.Join(want, ",") {
		t.Fatalf("core.permission.deny events name %v, want %v", denied, want)
	}
	// The route the entry does not name is as it was.
	if rec := rg.do(http.MethodGet, "/thing", "core-only"); rec.Code != http.StatusOK {
		t.Fatalf("GET /thing as core-only = %d, want 200: only the named route gains the co-requirement", rec.Code)
	}
}

// TestCoRequirement_RawRoute: a HandleRaw route is core-owned too.
func TestCoRequirement_RawRoute(t *testing.T) {
	rg, err := cqBuild(t, cqCoReq("getRawThing", rbac.Need(cqBooks)))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rec := rg.do(http.MethodGet, "/rawthing", "core-only"); rec.Code != http.StatusForbidden {
		t.Fatalf("raw route as core-only = %d, want 403", rec.Code)
	}
	if rec := rg.do(http.MethodGet, "/rawthing", "both"); rec.Code != http.StatusOK {
		t.Fatalf("raw route as both = %d, want 200", rec.Code)
	}
}

// TestCoRequirement_HEADAndOPTIONS: a GET route's HEAD is the same registered
// route (ServeMux serves HEAD on "GET /thing", and the co-requirement lives in
// that route's stage 13 wrapper), so it is refused and admitted exactly as GET
// is. OPTIONS never reaches the route's handler at all (a preflight is stage
// 6's, anything else is the mux's 405), so it can neither bypass the
// co-requirement nor be affected by it: it answers the same with and without.
func TestCoRequirement_HEADAndOPTIONS(t *testing.T) {
	with, err := cqBuild(t, cqCoReq("getThing", rbac.Need(cqBooks)))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	without, err := cqBuild(t, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, c := range []struct {
		method, as string
		with       int
		without    int
	}{
		{http.MethodGet, "core-only", http.StatusForbidden, http.StatusOK},
		{http.MethodHead, "core-only", http.StatusForbidden, http.StatusOK},
		{http.MethodGet, "both", http.StatusOK, http.StatusOK},
		{http.MethodHead, "both", http.StatusOK, http.StatusOK},
		{http.MethodHead, "", http.StatusUnauthorized, http.StatusUnauthorized},
	} {
		t.Run(c.method+" as "+c.as, func(t *testing.T) {
			if got := with.do(c.method, "/thing", c.as).Code; got != c.with {
				t.Errorf("with the co-requirement: status %d, want %d", got, c.with)
			}
			if got := without.do(c.method, "/thing", c.as).Code; got != c.without {
				t.Errorf("without it: status %d, want %d", got, c.without)
			}
		})
	}
	for _, as := range []string{"", "core-only", "both", "books-only"} {
		wb, nb := with.reached.Load(), without.reached.Load()
		a, b := with.do(http.MethodOptions, "/thing", as), without.do(http.MethodOptions, "/thing", as)
		if a.Code != b.Code {
			t.Errorf("OPTIONS as %q: status %d with the co-requirement, %d without; it must not matter", as, a.Code, b.Code)
		}
		if a.Code != http.StatusNoContent {
			t.Errorf("OPTIONS as %q answered %d, want stage 6's 204", as, a.Code)
		}
		if with.reached.Load() != wb || without.reached.Load() != nb {
			t.Errorf("OPTIONS as %q reached a route handler", as)
		}
	}
}

// TestCoRequirement_EveryEntryOfANameCarriesIt: there is no second mount of a
// core route (a name is unique per owner, so a versioned or aliased mount
// under the same name is ErrDuplicateRoute and the boot is refused), and
// should one ever slip past that check the co-requirement is on every entry of
// the name, never on the first only. The second route here is the v2 mount of
// the same name: Build refuses it, and both entries hold the co-requirement.
// A mount under a DIFFERENT name is a different route and an entry must name it.
func TestCoRequirement_EveryEntryOfANameCarriesIt(t *testing.T) {
	co := cqCoReq("manageThing", rbac.Need(cqBooks))
	rg, err := cqBuild(t, co, func(rg *cqRig, r *Router) {
		Handle(r.CoreGroup().Version(2), Route{Method: http.MethodPost, Path: "/manage", Name: "manageThing", Perm: rbac.Need(cqCore)},
			func(context.Context, *In[struct{}]) (raOut, error) { rg.reached.Add(1); return raOut{OK: true}, nil })
	})
	if !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Build with a same-name v2 mount: err = %v, want ErrDuplicateRoute", err)
	}
	if rg.h != nil {
		t.Fatal("Build returned a handler for a router with a same-name second mount")
	}
	carried := 0
	for _, e := range rg.r.routes {
		if e.info.Name == "manageThing" {
			carried++
			if e.co.IsZero() || e.perm().String() != "all(core:integrations.manage,books:integrations.manage)" {
				t.Errorf("%s: co = %v, perm = %s: the co-requirement is missing from one entry of the name", describe(e.info), e.co, e.perm())
			}
		}
	}
	if carried != 2 {
		t.Fatalf("%d entries named manageThing, want 2", carried)
	}

	// Under another name the v2 route is not covered: the map is keyed by name.
	rg, err = cqBuild(t, co, func(rg *cqRig, r *Router) {
		Handle(r.CoreGroup().Version(2), Route{Method: http.MethodPost, Path: "/manage", Name: "manageThingV2", Perm: rbac.Need(cqCore)},
			func(context.Context, *In[struct{}]) (raOut, error) { rg.reached.Add(1); return raOut{OK: true}, nil })
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/core/v2/manage", nil)
	req.RemoteAddr = "192.0.2.10:4000"
	req.Header.Set("X-Test-As", "core-only")
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("v2 mount under another name as core-only = %d, want 200 (an entry must name it)", rec.Code)
	}
}

// TestCoRequirement_EmptyMapChangesNothing: a nil map, an empty map and no
// Options field at all serve every route to every principal identically, report
// the same RouteInfo and generate the same OpenAPI document, so an app that
// declares none is unaffected.
func TestCoRequirement_EmptyMapChangesNothing(t *testing.T) {
	base, err := cqBuild(t, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for name, co := range map[string]map[string]rbac.Requirement{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			rg, err := cqBuild(t, co)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			for _, route := range []struct{ method, path string }{
				{http.MethodPost, "/manage"}, {http.MethodGet, "/thing"}, {http.MethodGet, "/rawthing"},
				{http.MethodGet, "/view"}, {http.MethodGet, "/either"}, {http.MethodGet, "/open"},
			} {
				for _, as := range []string{"", "core-only", "books-only", "both", "viewer"} {
					a, b := base.do(route.method, route.path, as), rg.do(route.method, route.path, as)
					if an, bn := cqNoRequestID(a.Body.String()), cqNoRequestID(b.Body.String()); a.Code != b.Code || an != bn {
						t.Errorf("%s %s as %q: %d %s, want %d %s", route.method, route.path, as, b.Code, bn, a.Code, an)
					}
				}
			}
			var want, got []string
			for ri := range base.r.Routes() {
				want = append(want, ri.Name+" "+ri.Perm.String()+" co="+ri.CoPerm.String())
			}
			for ri := range rg.r.Routes() {
				got = append(got, ri.Name+" "+ri.Perm.String()+" co="+ri.CoPerm.String())
				if !ri.CoPerm.IsZero() {
					t.Errorf("%s reports co-requirement %s with no map", ri.Name, ri.CoPerm)
				}
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("Routes() = %v, want %v", got, want)
			}
			if a, b := cqOpenAPI(t, base.r), cqOpenAPI(t, rg.r); a != b {
				t.Errorf("OpenAPI document differs with an empty map:\n%s\nwant\n%s", b, a)
			}
		})
	}
}

// cqRequestID is the one member of an answer that differs between two requests.
var cqRequestID = regexp.MustCompile(`"request_id":"[^"]*"`)

func cqNoRequestID(body string) string { return cqRequestID.ReplaceAllString(body, `"request_id":"-"`) }

func cqOpenAPI(t *testing.T, r *Router) string {
	t.Helper()
	doc, err := r.OpenAPI("core")
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCoRequirement_OutputsShowTheCombinedRequirement: Routes (the audit
// walker's input), the OpenAPI document and the deny audit all show the AND;
// the route as Handle recorded it, which R6 judges, keeps its own.
func TestCoRequirement_OutputsShowTheCombinedRequirement(t *testing.T) {
	rg, err := cqBuild(t, cqCoReq("manageThing", rbac.Need(cqBooks)))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const combined = "all(core:integrations.manage,books:integrations.manage)"
	found := false
	for ri := range rg.r.Routes() {
		if ri.Name != "manageThing" {
			if !ri.CoPerm.IsZero() {
				t.Errorf("%s reports co-requirement %s but no entry names it", ri.Name, ri.CoPerm)
			}
			continue
		}
		found = true
		if ri.Perm.String() != combined || ri.CoPerm.String() != string(cqBooks) {
			t.Errorf("Routes(): Perm %s, CoPerm %s, want %s and %s", ri.Perm, ri.CoPerm, combined, cqBooks)
		}
	}
	if !found {
		t.Fatal("manageThing is not in Routes()")
	}
	doc := cqOpenAPI(t, rg.r)
	if !strings.Contains(doc, `"x-mediacore-permission":"`+combined+`"`) {
		t.Errorf("OpenAPI document does not show %s for manageThing:\n%s", combined, doc)
	}
	for _, e := range rg.r.routes {
		if e.info.Name == "manageThing" && (e.info.Perm.String() != string(cqCore) || !e.info.CoPerm.IsZero()) {
			t.Errorf("the recorded RouteInfo carries %s / %s: R6 must keep judging the route's own requirement", e.info.Perm, e.info.CoPerm)
		}
	}
	// A second Build is the same Build: nothing accumulates on the entry.
	if _, err := rg.r.Build(); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	for ri := range rg.r.Routes() {
		if ri.Name == "manageThing" && ri.Perm.String() != combined {
			t.Errorf("after a second Build, Perm = %s, want %s", ri.Perm, combined)
		}
	}
}

// TestCoRequirement_CannotLaunderAForeignScope: R6 still judges the route's own
// requirement, so a core route that itself names the app's scope is refused
// whatever the map says.
func TestCoRequirement_CannotLaunderAForeignScope(t *testing.T) {
	_, err := cqBuild(t, cqCoReq("launder", rbac.Need("books:view")), func(rg *cqRig, r *Router) {
		Handle(r.CoreGroup(), Route{Method: http.MethodGet, Path: "/launder", Name: "launder", Perm: rbac.AllOf(cqCore, cqBooks)},
			func(context.Context, *In[struct{}]) (raOut, error) { return raOut{OK: true}, nil })
	})
	if !errors.Is(err, ErrForeignScope) {
		t.Fatalf("Build: err = %v, want ErrForeignScope", err)
	}
}

// TestCoRequirement_BootRefusals: every entry Build cannot prove narrows
// refuses the boot, with a named error, whatever else is wrong or right.
func TestCoRequirement_BootRefusals(t *testing.T) {
	ghost := rbac.Need("books:ghost") // a well-formed scope the catalog does not declare
	for _, c := range []struct {
		name string
		co   map[string]rbac.Requirement
		want []error
	}{
		{"AnyOf", cqCoReq("manageThing", rbac.AnyOf(cqBooks, "books:view")), []error{ErrCoRequirementShape}},
		{"unknown route", cqCoReq("manageThingg", rbac.Need(cqBooks)), []error{ErrCoRequirementRoute}},
		{"a core scope", cqCoReq("manageThing", rbac.Need("core:users.view")), []error{ErrCoRequirementScope}},
		{"a core scope beside the app's", cqCoReq("manageThing", rbac.AllOf(cqBooks, cqCore)), []error{ErrCoRequirementScope}},
		{"core:admin", cqCoReq("manageThing", rbac.Need(rbac.CoreAdmin)), []error{ErrCoRequirementScope}},
		{"a custom-namespace scope", cqCoReq("manageThing", rbac.Need("custom:anything")), []error{ErrCoRequirementScope}},
		{"Public", cqCoReq("manageThing", rbac.Public), []error{ErrCoRequirementScope}},
		{"AnyAuthenticated", cqCoReq("manageThing", rbac.AnyAuthenticated), []error{ErrCoRequirementScope}},
		{"the zero requirement", cqCoReq("manageThing", rbac.Requirement{}), []error{ErrCoRequirementShape}},
		{"Delegated", cqCoReq("manageThing", rbac.Delegated("bat-signal")), []error{ErrCoRequirementShape}},
		{"a legacy atom", cqCoReq("manageThing", rbac.LegacyAtom("integrations.manage")), []error{ErrCoRequirementShape}},
		{"an unregistered scope", cqCoReq("manageThing", ghost), []error{ErrCoRequirementUnregistered, ErrUnknownScope}},
		{"a target whose own requirement is AnyOf", cqCoReq("eitherThing", rbac.Need(cqBooks)), []error{ErrCoRequirementShape}},
		{"a target that is Public", cqCoReq("openThing", rbac.Need(cqBooks)), []error{ErrCoRequirementShape}},
		{"a module route's name", cqCoReq("listBooks", rbac.Need(cqBooks)), []error{ErrCoRequirementRoute}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg, err := cqBuild(t, c.co, func(rg *cqRig, r *Router) {
				Handle(r.ModuleGroup("books"), Route{Method: http.MethodGet, Path: "/books", Name: "listBooks", Perm: rbac.Need("books:view")},
					func(context.Context, *In[struct{}]) (raOut, error) { return raOut{OK: true}, nil })
			})
			if err == nil {
				t.Fatal("Build succeeded")
			}
			for _, w := range c.want {
				if !errors.Is(err, w) {
					t.Errorf("Build error does not wrap %v: %v", w, err)
				}
			}
			if rg.h != nil {
				t.Error("Build returned a handler with its error")
			}
		})
	}
	t.Run("a good entry beside a bad one still refuses", func(t *testing.T) {
		_, err := cqBuild(t, map[string]rbac.Requirement{
			"manageThing": rbac.Need(cqBooks), "ghostRoute": rbac.Need(cqBooks),
		})
		if !errors.Is(err, ErrCoRequirementRoute) {
			t.Fatalf("Build: err = %v, want ErrCoRequirementRoute", err)
		}
	})
	t.Run("no catalog to check the scope against", func(t *testing.T) {
		_, err := cqBuildWith(t, cqCoReq("manageThing", rbac.Need(cqBooks)), false, nil)
		if !errors.Is(err, ErrCoRequirementUnregistered) {
			t.Fatalf("Build with no catalog: err = %v, want ErrCoRequirementUnregistered", err)
		}
	})
	t.Run("no auth wiring", func(t *testing.T) {
		_, err := cqBuildWith(t, cqCoReq("manageThing", rbac.Need(cqBooks)), true, nil)
		if !errors.Is(err, ErrCoRequirementUnregistered) {
			t.Fatalf("Build with no auth: err = %v, want ErrCoRequirementUnregistered", err)
		}
	})
}

// TestCoRequirement_AuditEventsAreNotWidened: a refused request's deny is
// recorded as the deny of the missing scope, by the same stage 13 audit, and an
// admitted request records none.
func TestCoRequirement_AuditEventsAreNotWidened(t *testing.T) {
	rg, err := cqBuild(t, cqCoReq("manageThing", rbac.Need(cqBooks)))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rg.do(http.MethodPost, "/manage", "both")
	for _, e := range rg.audit.all() {
		if e.Action == "core.permission.deny" {
			t.Fatalf("an admitted request audited a deny: %+v", e)
		}
	}
	rg.do(http.MethodPost, "/manage", "core-only")
	evs := rg.audit.all()
	if len(evs) != 1 || evs[0].Action != "core.permission.deny" || evs[0].Outcome != audit.Denied || evs[0].Permission != string(cqBooks) {
		t.Fatalf("audit = %+v, want one core.permission.deny of %s", evs, cqBooks)
	}
}
