// file: core/httpapi/corequirement_kind_test.go
// version: 1.0.0
// guid: 3a91c5d8-7e24-4b60-9f1d-e08b62a7c453
// last-edited: 2026-11-28

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// Per-value co-requirements (CoRequirement.Param and Value): an entry scoped
// to one value of a path wildcard, for a route that serves one operation per
// kind. Batman names only; "download" and "ai" stand for the integration kinds
// ABO gates and does not.

const (
	cqView rbac.Permission = "books:view"
)

type cqKindIn struct {
	Kind string `path:"kind"`
}

// cqKindRoutes registers, on the core group:
//
//	GET /integrations/{kind}/health  healthThing  AllOf(core:integrations.manage)  (Handle)
//	GET /rawkind/{kind}              rawKind      AllOf(core:integrations.manage)  (HandleRaw)
//	GET /files/{rest...}             restThing    AllOf(core:integrations.manage)  (multi-segment wildcard)
//	GET /audited/{kind}              auditedKind  AllOf(core:audit.view)           (AuthRequired own)
func cqKindRoutes(rg *cqRig, r *Router) {
	g := r.CoreGroup()
	ok := func(context.Context, *In[cqKindIn]) (raOut, error) { rg.reached.Add(1); return raOut{OK: true}, nil }
	Handle(g, Route{Method: http.MethodGet, Path: "/integrations/{kind}/health", Name: "healthThing", Perm: rbac.Need(cqCore)}, ok)
	HandleRaw(g, Route{Method: http.MethodGet, Path: "/rawkind/{kind}", Name: "rawKind", Perm: rbac.Need(cqCore)}, RawDoc{},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rg.reached.Add(1); w.WriteHeader(http.StatusOK) }))
	HandleRaw(g, Route{Method: http.MethodGet, Path: "/files/{rest...}", Name: "restThing", Perm: rbac.Need(cqCore)}, RawDoc{},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rg.reached.Add(1); w.WriteHeader(http.StatusOK) }))
	Handle(g, Route{Method: http.MethodGet, Path: "/audited/{kind}", Name: "auditedKind", Perm: rbac.Need("core:audit.view")}, ok)
}

func cqKind(route, value string, need rbac.Requirement) CoRequirement {
	return CoRequirement{Route: route, Param: "kind", Value: value, Need: need}
}

// TestCoRequirementKind_GatesOnlyTheNamedValue: an entry scoped to
// kind=download refuses a principal holding only the core scope on the
// download kind, naming the app's scope, and leaves every other kind exactly
// as the route's own requirement had it; a principal holding both passes the
// gated kind; the value is matched whatever its case, so a handler that folds
// case is not a way round; and the route's raw and typed forms behave alike.
func TestCoRequirementKind_GatesOnlyTheNamedValue(t *testing.T) {
	rg, err := cqBuild(t, []CoRequirement{
		cqKind("healthThing", "download", rbac.Need(cqBooks)),
		cqKind("rawKind", "download", rbac.Need(cqBooks)),
	}, cqKindRoutes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, c := range []struct {
		name, path, as, missing string
		status                  int
	}{
		{"typed: the gated kind, core scope only", "/integrations/download/health", "core-only", string(cqBooks), http.StatusForbidden},
		{"typed: the gated kind, both scopes", "/integrations/download/health", "both", "", http.StatusOK},
		{"typed: the gated kind, app scope only", "/integrations/download/health", "books-only", string(cqCore), http.StatusForbidden},
		{"typed: another kind, core scope only", "/integrations/ai/health", "core-only", "", http.StatusOK},
		{"typed: another kind that shares a prefix", "/integrations/downloads/health", "core-only", "", http.StatusOK},
		{"typed: the gated kind in another case", "/integrations/DownLoad/health", "core-only", string(cqBooks), http.StatusForbidden},
		{"raw: the gated kind, core scope only", "/rawkind/download", "core-only", string(cqBooks), http.StatusForbidden},
		{"raw: the gated kind, both scopes", "/rawkind/download", "both", "", http.StatusOK},
		{"raw: another kind, core scope only", "/rawkind/metadata", "core-only", "", http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := rg.reached.Load()
			rec := rg.do(http.MethodGet, c.path, c.as)
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
	if want := "books:integrations.manage,core:integrations.manage,books:integrations.manage,books:integrations.manage"; strings.Join(denied, ",") != want {
		t.Errorf("core.permission.deny events name %v, want %s", denied, want)
	}
}

// TestCoRequirementKind_RouteWideAndPerValueBothApply: a route may carry a
// route-wide entry and per-value ones, and a request must pass every one that
// applies. The principal "both" holds core and books:integrations.manage, so
// it passes the route-wide entry; it is refused on the kind whose entry asks
// for books:view as well, and not on the others.
func TestCoRequirementKind_RouteWideAndPerValueBothApply(t *testing.T) {
	rg, err := cqBuild(t, []CoRequirement{
		{Route: "healthThing", Need: rbac.Need(cqBooks)},
		cqKind("healthThing", "download", rbac.Need(cqView)),
		cqKind("healthThing", "metadata", rbac.AllOf(cqView, cqBooks)),
	}, cqKindRoutes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, c := range []struct {
		name, path, as, missing string
		status                  int
	}{
		{"route-wide only: core scope only is refused", "/integrations/ai/health", "core-only", string(cqBooks), http.StatusForbidden},
		{"route-wide only: both passes", "/integrations/ai/health", "both", "", http.StatusOK},
		{"per-value too: both is refused for the per-value scope", "/integrations/download/health", "both", string(cqView), http.StatusForbidden},
		{"per-value too: core only is refused by the route-wide entry first", "/integrations/download/health", "core-only", string(cqBooks), http.StatusForbidden},
		{"a second per-value entry for another kind", "/integrations/metadata/health", "both", string(cqView), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := rg.do(http.MethodGet, c.path, c.as)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, c.status, rec.Body)
			}
			if c.status == http.StatusForbidden {
				if p := s13Problem(t, rec); p.Required != c.missing {
					t.Fatalf("requires %q, want %q", p.Required, c.missing)
				}
			}
		})
	}
}

// TestCoRequirementKind_AuthDisabled: the auth-disabled principal is judged
// by each entry on its own, so a per-value entry cannot widen what the route
// refuses it. Own requirement AuthRequired (core:audit.view): refused on every
// kind with or without an entry, the no-map answer. Own requirement plain: an
// AuthRequired-only per-value entry refuses it on that kind and only that kind.
func TestCoRequirementKind_AuthDisabled(t *testing.T) {
	cat := cqCatalog(t, rbac.PermissionSpec{Name: "books:audit.export", Title: "x", Description: "x", AuthRequired: true})
	off := s13AuthOff(cat)
	build := func(co ...CoRequirement) *cqRig {
		rg, err := cqBuildWith(t, co, false, cat, cqKindRoutes)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return rg
	}
	t.Run("an AuthRequired own requirement is refused as with no map", func(t *testing.T) {
		base := build()
		with := build(cqKind("auditedKind", "download", rbac.Need(cqBooks)))
		for _, kind := range []string{"download", "ai"} {
			want, got := base.doAs(http.MethodGet, "/audited/"+kind, off), with.doAs(http.MethodGet, "/audited/"+kind, off)
			if want.Code != http.StatusForbidden {
				t.Fatalf("with no map the auth-disabled principal got %d on %s, want 403 (the premise of the cell)", want.Code, kind)
			}
			if cqAnswer(got) != cqAnswer(want) {
				t.Errorf("%s: %s, want the no-map answer %s", kind, cqAnswer(got), cqAnswer(want))
			}
		}
		if with.reached.Load() != 0 {
			t.Error("the handler was reached by the auth-disabled principal")
		}
	})
	t.Run("an AuthRequired per-value entry refuses only its kind", func(t *testing.T) {
		with := build(cqKind("healthThing", "download", rbac.Need("books:audit.export")))
		if got := with.doAs(http.MethodGet, "/integrations/ai/health", off); got.Code != http.StatusOK {
			t.Errorf("another kind: auth-disabled got %d, want 200 as with no map", got.Code)
		}
		got := with.doAs(http.MethodGet, "/integrations/download/health", off)
		if got.Code != http.StatusForbidden || s13Problem(t, got).Code != "interactive_required" {
			t.Errorf("the gated kind: auth-disabled got %d %s, want 403 interactive_required", got.Code, got.Body)
		}
	})
	t.Run("a plain per-value entry changes nothing for it", func(t *testing.T) {
		base, with := build(), build(cqKind("healthThing", "download", rbac.Need(cqBooks)))
		for _, kind := range []string{"download", "ai"} {
			want, got := base.doAs(http.MethodGet, "/integrations/"+kind+"/health", off), with.doAs(http.MethodGet, "/integrations/"+kind+"/health", off)
			if cqAnswer(got) != cqAnswer(want) {
				t.Errorf("%s: %s, want the no-map answer %s", kind, cqAnswer(got), cqAnswer(want))
			}
		}
	})
}

// TestCoRequirementKind_AdminIsNotLockedOut: an entry scoped to a kind is an
// ordinary AND, so a principal that holds the scopes passes it: the
// administrator role (core:admin.*, books:*) passes the gated kind and the
// others alike.
func TestCoRequirementKind_AdminIsNotLockedOut(t *testing.T) {
	cat := cqCatalog(t)
	rg, err := cqBuildWith(t, []CoRequirement{cqKind("healthThing", "download", rbac.Need(cqBooks))}, false, cat, cqKindRoutes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	admin := s13Session(cat, "core:*", "books:*")
	for _, kind := range []string{"download", "ai"} {
		if rec := rg.doAs(http.MethodGet, "/integrations/"+kind+"/health", admin); rec.Code != http.StatusOK {
			t.Errorf("admin on kind %s: %d %s, want 200", kind, rec.Code, rec.Body)
		}
	}
}

// TestCoRequirementKind_BootRefusals: an entry whose Param or Value cannot
// scope it, or that repeats another's, refuses the boot with a named error.
func TestCoRequirementKind_BootRefusals(t *testing.T) {
	need := rbac.Need(cqBooks)
	for _, c := range []struct {
		name string
		co   []CoRequirement
		want error
	}{
		{"a Param that is not a wildcard of the route", []CoRequirement{{Route: "healthThing", Param: "nope", Value: "download", Need: need}}, ErrCoRequirementParam},
		{"a Param on a route that has no wildcard", []CoRequirement{{Route: "manageThing", Param: "kind", Value: "download", Need: need}}, ErrCoRequirementParam},
		{"a Param that names a multi-segment wildcard", []CoRequirement{{Route: "restThing", Param: "rest", Value: "a", Need: need}}, ErrCoRequirementParam},
		{"a Param with no Value", []CoRequirement{{Route: "healthThing", Param: "kind", Need: need}}, ErrCoRequirementParam},
		{"a Value with no Param", []CoRequirement{{Route: "healthThing", Value: "download", Need: need}}, ErrCoRequirementParam},
		{"a Value with a slash", []CoRequirement{cqKind("healthThing", "a/b", need)}, ErrCoRequirementParam},
		{"a Value that is itself a wildcard", []CoRequirement{cqKind("healthThing", "{kind}", need)}, ErrCoRequirementParam},
		{"the same (Route, Param, Value) twice", []CoRequirement{cqKind("healthThing", "download", need), cqKind("healthThing", "download", rbac.Need(cqView))}, ErrCoRequirementDuplicate},
		{"the same value in another case", []CoRequirement{cqKind("healthThing", "download", need), cqKind("healthThing", "Download", need)}, ErrCoRequirementDuplicate},
		{"two route-wide entries for one route", []CoRequirement{{Route: "healthThing", Need: need}, {Route: "healthThing", Need: rbac.Need(cqView)}}, ErrCoRequirementDuplicate},
		{"a per-value entry on an AnyOf route", []CoRequirement{{Route: "eitherThing", Param: "kind", Value: "x", Need: need}}, ErrCoRequirementShape},
		{"a per-value entry naming a core scope", []CoRequirement{cqKind("healthThing", "download", rbac.Need("core:users.view"))}, ErrCoRequirementScope},
		{"a per-value entry naming no route", []CoRequirement{cqKind("ghostRoute", "download", need)}, ErrCoRequirementRoute},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg, err := cqBuild(t, c.co, cqKindRoutes)
			if !errors.Is(err, c.want) {
				t.Fatalf("Build err = %v, want %v", err, c.want)
			}
			if rg.h != nil {
				t.Error("Build returned a handler with its error")
			}
		})
	}
	t.Run("a route-wide entry beside per-value entries of other values builds", func(t *testing.T) {
		if _, err := cqBuild(t, []CoRequirement{
			{Route: "healthThing", Need: need}, cqKind("healthThing", "download", rbac.Need(cqView)), cqKind("healthThing", "ai", need),
		}, cqKindRoutes); err != nil {
			t.Fatalf("Build: %v", err)
		}
	})
}

// TestCoRequirementKind_Outputs: Routes() reports the per-value entries beside
// a route's unchanged Perm (no one request carries them all, so they are not
// folded into it), the OpenAPI operation lists them as
// x-mediacore-co-requirements, and the route as Handle recorded it has none.
func TestCoRequirementKind_Outputs(t *testing.T) {
	rg, err := cqBuild(t, []CoRequirement{cqKind("healthThing", "download", rbac.AllOf(cqBooks, cqView))}, cqKindRoutes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for ri := range rg.r.Routes() {
		if ri.Name != "healthThing" {
			if len(ri.CoValuePerms) != 0 {
				t.Errorf("%s reports per-value co-requirements but no entry names it", ri.Name)
			}
			continue
		}
		found = true
		if len(ri.CoValuePerms) != 1 || ri.CoValuePerms[0].Param != "kind" || ri.CoValuePerms[0].Value != "download" ||
			ri.CoValuePerms[0].Perm.String() != "all(books:integrations.manage,books:view)" {
			t.Errorf("Routes(): CoValuePerms = %+v, want kind=download requiring both books scopes", ri.CoValuePerms)
		}
		if ri.Perm.String() != string(cqCore) || !ri.CoPerm.IsZero() {
			t.Errorf("Routes(): Perm %s CoPerm %s, want the route's own and none (an entry for one kind is not the route's)", ri.Perm, ri.CoPerm)
		}
	}
	if !found {
		t.Fatal("healthThing is not in Routes()")
	}
	doc := cqOpenAPI(t, rg.r)
	if want := `"x-mediacore-co-requirements":[{"param":"kind","value":"download","permission":"all(books:integrations.manage,books:view)"}]`; !strings.Contains(doc, want) {
		t.Errorf("the OpenAPI document does not carry %s:\n%s", want, doc)
	}
	if n := strings.Count(doc, "x-mediacore-co-requirements"); n != 1 {
		t.Errorf("x-mediacore-co-requirements appears %d times, want once (only healthThing has an entry)", n)
	}
	for _, e := range rg.r.routes {
		if len(e.info.CoValuePerms) != 0 {
			t.Errorf("the recorded RouteInfo of %s carries per-value co-requirements", e.info.Name)
		}
	}
}

// TestCoRequirementKind_SecondBuildDoesNotStack: Build twice sets each
// per-value entry once, not twice.
func TestCoRequirementKind_SecondBuildDoesNotStack(t *testing.T) {
	rg, err := cqBuild(t, []CoRequirement{cqKind("healthThing", "download", rbac.Need(cqBooks))}, cqKindRoutes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := rg.r.Build(); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	for _, e := range rg.r.routes {
		if e.info.Name == "healthThing" && len(e.coBy) != 1 {
			t.Errorf("healthThing carries %d per-value entries after two Builds, want 1", len(e.coBy))
		}
	}
}
