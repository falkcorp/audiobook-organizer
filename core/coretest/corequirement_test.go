// file: core/coretest/corequirement_test.go
// version: 1.0.0
// guid: 3f8a1c64-7d2b-4e95-b0a3-6c19e5d84f27
// last-edited: 2026-11-28

package coretest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// Route co-requirements through the REAL kernel (W6, review round 1). The
// httpapi cells build a Router by hand; these boot the App with the identity
// boot, so a co-requirement that the kernel drops on the way to the router, or
// a Describe that cannot check it, fails here. Batman names only; the co-scope
// is the greeter fixture's hello:admin.

const coreUsersPath = "/api/core/v1/users"

// TestRouteCoRequirement_ReachesTheRouterThroughTheKernel: Options map ->
// kernel.Config -> httpapi.Options.CoRequirements -> stage 13. A principal
// holding only the core scope is refused on the co-required route and
// admitted on the same route of a boot with no map, the principal holding
// both is admitted, and Routes() reports the combined requirement.
func TestRouteCoRequirement_ReachesTheRouterThroughTheKernel(t *testing.T) {
	co := map[string]rbac.Requirement{"listUsers": rbac.Need(permAdmin)}
	users := []Option{
		WithModules(greeter{}),
		WithUser("core-only", "core:users.view"),
		WithUser("both", "core:users.view", permAdmin),
		WithUser("co-only", permAdmin),
	}
	base := New(t, users...)
	with := New(t, append(users, WithRouteCoRequirements(co))...)

	if got := base.As("core-only").GetJSON(t, coreUsersPath, nil); got != http.StatusOK {
		t.Fatalf("with no map, core-only GET users = %d, want 200 (the premise of the cell)", got)
	}
	var p httpapi.Problem
	if got := with.As("core-only").GetJSON(t, coreUsersPath, &p); got != http.StatusForbidden {
		t.Fatalf("with the map, core-only GET users = %d, want 403: the kernel did not hand the co-requirement to the router", got)
	}
	if p.Code != "permission_denied" || p.Required != string(permAdmin) {
		t.Errorf("problem %q requires %q, want permission_denied requiring %q", p.Code, p.Required, permAdmin)
	}
	if got := with.As("both").GetJSON(t, coreUsersPath, nil); got != http.StatusOK {
		t.Errorf("with the map, both-scopes GET users = %d, want 200", got)
	}
	if got := with.As("co-only").GetJSON(t, coreUsersPath, &p); got != http.StatusForbidden || p.Required != "core:users.view" {
		t.Errorf("with the map, co-only GET users = %d requiring %q, want 403 requiring core:users.view", got, p.Required)
	}
	if got := with.Anonymous().GetJSON(t, coreUsersPath, nil); got != http.StatusUnauthorized {
		t.Errorf("anonymous GET users = %d, want 401", got)
	}
	const combined = "all(core:users.view," + string(permAdmin) + ")"
	found := false
	for ri := range with.App().Routes() {
		if ri.Name != "listUsers" {
			if !ri.CoPerm.IsZero() {
				t.Errorf("%s reports co-requirement %s but no entry names it", ri.Name, ri.CoPerm)
			}
			continue
		}
		found = true
		if ri.Perm.String() != combined || ri.CoPerm.String() != string(permAdmin) {
			t.Errorf("Routes(): Perm %s, CoPerm %s, want %s and %s", ri.Perm, ri.CoPerm, combined, permAdmin)
		}
	}
	if !found {
		t.Fatal("listUsers is not in Routes()")
	}
}

// TestRouteCoRequirement_DescribeDocumentsIt: Describe over the same options
// succeeds (it builds the catalog the co-requirement is checked against) and
// the core document shows the combined requirement; a scope no manifest
// declares refuses it, so Describe cannot generate a document for a boot that
// would refuse.
func TestRouteCoRequirement_DescribeDocumentsIt(t *testing.T) {
	opts := func(co map[string]rbac.Requirement) app.Options {
		return app.Options{AppID: harnessAppID, Args: []string{}, Env: []string{}, IdentityBoot: true, RouteCoRequirements: co}
	}
	d, err := app.Describe(context.Background(), opts(map[string]rbac.Requirement{"listUsers": rbac.Need(permAdmin)}), greeter{})
	if err != nil {
		t.Fatalf("Describe with a co-requirement: %v", err)
	}
	doc := string(d.Core)
	if want := `"x-mediacore-permission":"all(core:users.view,` + string(permAdmin) + `)"`; !strings.Contains(compact(doc), want) {
		t.Errorf("the core document does not show listUsers requiring %s", want)
	}
	if _, err := app.Describe(context.Background(), opts(map[string]rbac.Requirement{"listUsers": rbac.Need("hello:ghost")}), greeter{}); err == nil {
		t.Error("Describe accepted a co-scope no manifest declares")
	} else if !errors.Is(err, httpapi.ErrCoRequirementUnregistered) {
		t.Errorf("Describe error is not ErrCoRequirementUnregistered: %v", err)
	}
}

func compact(s string) string { return strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(s) }

// TestRouteCoRequirement_AuthDisabledOnEveryAuthRequiredOnlyCoreRoute is the
// real-route-table cell of the auth-off blocker. The list is built from the
// routes a boot with NO map actually mounts: every core native or raw route
// whose own requirement is an AllOf of scopes the real catalog flags
// AuthRequired. For each, a co-scope that is not AuthRequired must leave the
// auth-disabled principal's answer where it was (refused), judged by
// httpapi.Authorize over the route's real Perm and Guards with
// authz.AuthDisabled over the real catalog.
//
// This is Authorize over the real table, not a served auth-off boot: the
// harness has no way to serve auth-off (core.auth.mode is core config, which
// WithConfig does not accept yet). The served path of the same decision is
// covered by httpapi's TestCoRequirement_AuthDisabledIsNotWidened.
func TestRouteCoRequirement_AuthDisabledOnEveryAuthRequiredOnlyCoreRoute(t *testing.T) {
	h := New(t, WithModules(greeter{}))
	cat := h.id.rbac.Catalog()
	off := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: authz.AuthDisabled(cat)}

	var routes []httpapi.RouteInfo
	for ri := range h.App().Routes() {
		if ri.Owner != "core" || (ri.Kind != httpapi.RouteKindNative && ri.Kind != httpapi.RouteKindRaw) || ri.Perm.Kind() != rbac.KindAllOf {
			continue
		}
		onlyAuthRequired := true
		for _, p := range ri.Perm.Permissions() {
			if spec, ok := cat.Lookup(p); !ok || !spec.AuthRequired {
				onlyAuthRequired = false
			}
		}
		if onlyAuthRequired {
			routes = append(routes, ri)
		}
	}
	if len(routes) == 0 {
		t.Fatal("no core route has an entirely AuthRequired own requirement: the cell found nothing to judge")
	}
	spec, ok := cat.Lookup(permRead)
	if !ok || spec.AuthRequired || spec.Sensitive || spec.Confining {
		t.Fatalf("%s must be a plain scope for the cell (%+v)", permRead, spec)
	}
	unguarded := 0
	var names []string
	for _, ri := range routes {
		names = append(names, ri.Name)
		if ri.Guards == (httpapi.Guards{}) {
			unguarded++
		}
		for _, co := range []rbac.Requirement{rbac.Need(permRead), rbac.AllOf(permRead, permAdmin)} {
			req := httpapi.AuthzRequest{Perm: ri.Perm, Guards: ri.Guards, Principal: off}
			want := httpapi.Authorize(req, httpapi.AuthzOptions{Catalog: cat})
			if want.Refusal == nil {
				t.Fatalf("%s: with no co-requirement the auth-disabled principal is admitted: the premise of the cell is wrong", ri.Name)
			}
			req.CoPerm = co
			got := httpapi.Authorize(req, httpapi.AuthzOptions{Catalog: cat})
			if got.Refusal == nil {
				t.Errorf("%s (own %s) with co %s: the auth-disabled principal is ADMITTED; it is refused with no co-requirement", ri.Name, ri.Perm, co)
				continue
			}
			if got.Refusal.Status != want.Refusal.Status || got.Refusal.Code != want.Refusal.Code {
				t.Errorf("%s with co %s: refused %d %s, want the no-map answer %d %s", ri.Name, co,
					got.Refusal.Status, got.Refusal.Code, want.Refusal.Status, want.Refusal.Code)
			}
		}
	}
	if unguarded == 0 {
		t.Fatalf("every AuthRequired-only route (%v) is guarded, and a guard refuses before Perm is judged: the cell cannot tell a merged check from a separate one", names)
	}
	t.Logf("judged %d AuthRequired-only core routes (%d unguarded): %v", len(routes), unguarded, names)
}
