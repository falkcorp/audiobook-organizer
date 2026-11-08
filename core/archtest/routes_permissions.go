// file: core/archtest/routes_permissions.go
// version: 1.0.1
// guid: d27b0d0a-e465-4741-8f11-232cb68e08f5
// last-edited: 2026-11-07

package archtest

import (
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// RoutesHavePermissions is 03 R6's gate over a built router (05-http-api §12;
// the name is the spec's, the signature is coined by R1-37). routes is
// httpapi.Router.Routes(), or App.Routes() for a booted app: native routes,
// the legacy manifest routes and the "/" owner. It fails t, naming every
// offending route, unless each route
//
//   - declares a requirement (a zero rbac.Requirement fails, whatever the
//     kind of route);
//   - is a native or raw route requiring rbac.Public, rbac.AnyAuthenticated
//     or a set of permissions (rbac.Delegated and the legacy kinds are
//     legacy-route only), with any rate class but RateExempt, which belongs
//     to the infra probes and the SPA handler only (05-http-api §3);
//   - is a legacy route from the manifest with its permission: rbac.Public,
//     rbac.AnyAuthenticated, a set of permissions or a legacy atom, or OwnAuth
//     with rbac.Delegated naming its auth method (05-http-api §8.2). Any other
//     requirement kind fails, a future one included. A route the manifest does
//     not cover has no requirement, so it fails the first rule. A legacy route
//     may be RateExempt: LegacyRoute.RateClass records whether HEAD's gin
//     chain rate-limits the route today (§8.2), whatever its requirement;
//   - is in the known infra set: a Router.Infra route, RateExempt, requiring
//     rbac.Public, rbac.AnyAuthenticated or a set of permissions (§4.2), or
//     the "/" owner, which is exactly RouteInfo{Kind: SPA, Perm: rbac.Public,
//     RateClass: RateExempt} (§4.10).
//
// A RouteInfo does not say whether a Public route was allowed to be Public,
// so this gate accepts rbac.Public on any route. For a module's native route
// that is Build's check (ErrPublicNotAllowed against Manifest.PublicRoutes).
// For a legacy route it is the app's own reviewed list: ABO's is
// TestPublicRoutesGolden in internal/server/legacy_manifest_test.go, over
// testdata/public_routes.golden. Router.Build refuses most of the other
// defects at boot already; this gate is the archtest twin that reads what
// was built.
func RoutesHavePermissions(t testing.TB, routes iter.Seq[httpapi.RouteInfo]) {
	t.Helper()
	var findings []string
	for ri := range routes {
		if why := routePermissionProblem(ri); why != "" {
			findings = append(findings, describeRoute(ri)+": "+why)
		}
	}
	if len(findings) > 0 {
		t.Errorf("%d route(s) without an admissible permission (03 R6):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// routePermissionProblem is why ri fails R6, or "".
func routePermissionProblem(ri httpapi.RouteInfo) string {
	req := ri.Perm
	if req.IsZero() {
		return "no permission"
	}
	if ri.RateClass > httpapi.RateExempt {
		return fmt.Sprintf("unknown rate class %s", rateClassName(ri.RateClass))
	}
	switch ri.Kind {
	case httpapi.RouteKindSPA:
		if req.Kind() != rbac.KindPublic || ri.RateClass != httpapi.RateExempt {
			return fmt.Sprintf("the \"/\" owner must be rbac.Public and RateExempt, got %s and %s",
				req, rateClassName(ri.RateClass))
		}
	case httpapi.RouteKindInfra:
		if !nativeRequirement(req) {
			return fmt.Sprintf("requirement %s is for legacy routes only", req)
		}
		if ri.RateClass != httpapi.RateExempt {
			return fmt.Sprintf("an infra route is RateExempt, got %s", rateClassName(ri.RateClass))
		}
	case httpapi.RouteKindNative, httpapi.RouteKindRaw:
		if !nativeRequirement(req) {
			return fmt.Sprintf("requirement %s is for legacy routes only", req)
		}
		if ri.RateClass == httpapi.RateExempt {
			return "RateExempt outside the infra set (the infra probes and the SPA handler only)"
		}
		if ri.OwnAuth || ri.AuthMethod != "" {
			return "OwnAuth on a route that is not a legacy route"
		}
	case httpapi.RouteKindLegacy:
		return legacyPermissionProblem(ri)
	default:
		return fmt.Sprintf("unknown route kind %d", ri.Kind)
	}
	return ""
}

// nativeRequirement reports the requirement kinds a native, raw or infra
// route may carry.
func nativeRequirement(req rbac.Requirement) bool {
	switch req.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindAnyOf:
		return true
	}
	return false
}

// legacyPermissionProblem is 05-http-api §8.2's admission for one manifest
// route. It is an allowlist: a requirement kind it does not name fails.
func legacyPermissionProblem(ri httpapi.RouteInfo) string {
	req := ri.Perm
	if ri.OwnAuth {
		switch {
		case ri.AuthMethod == "":
			return "OwnAuth route names no auth method"
		case req.Kind() != rbac.KindDelegated:
			return fmt.Sprintf("OwnAuth route requires %s, want rbac.Delegated", req)
		}
		return ""
	}
	if ri.AuthMethod != "" {
		return fmt.Sprintf("auth method %q on a route that is not OwnAuth", ri.AuthMethod)
	}
	switch req.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindAnyOf, rbac.KindLegacyAtom:
		return ""
	case rbac.KindDelegated:
		return "rbac.Delegated on a route that is not OwnAuth"
	}
	return fmt.Sprintf("requirement %s is not a legacy route requirement", req)
}

// rateClassName names a rate class in a failure.
func rateClassName(c httpapi.RateClass) string {
	switch c {
	case httpapi.RateDefault:
		return "RateDefault"
	case httpapi.RateAuth:
		return "RateAuth"
	case httpapi.RateExpensive:
		return "RateExpensive"
	case httpapi.RateExempt:
		return "RateExempt"
	}
	return fmt.Sprintf("RateClass(%d)", c)
}

// describeRoute names a route in a failure: "GET /api/x/v1/y (owner, name)".
func describeRoute(ri httpapi.RouteInfo) string {
	return strings.TrimSpace(ri.Method+" "+ri.Pattern) + " (" + ri.Owner + ", " + ri.Name + ")"
}
