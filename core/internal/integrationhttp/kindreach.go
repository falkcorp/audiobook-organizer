// file: core/internal/integrationhttp/kindreach.go
// version: 0.1.0
// guid: b8e24c71-6d3f-4a95-9c10-47f5a2d8e603
// last-edited: 2026-11-28

package integrationhttp

import (
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
)

// One route serves every kind, so a route's Perm is one requirement for all of
// them. HEAD's reach differs by kind (docs/plans/platform-core/
// f7-09-integrations-per-kind-reach.md), so the per-kind rule of the health and
// the two test routes is this lookup, which the route's Doc text and the
// handler's backstop both read: the backstop equals the per-kind requirement the
// route documents. The route's Perm is a floor (core:integrations.manage), not
// the rule.
//
// Three 403 shapes come out of this surface, and a client sees one of them:
//
//  1. the router's (stage 13): permission_denied with required_permission set to
//     the route's Perm, the floor here
//     (web/src/services/coreHttp/transport.ts reads required_permission);
//  2. libraryhttp's own all(...) check, whose body carries "required";
//  3. this package's backstop: permission_denied with no required_permission and
//     Detail "this needs <requirement>", because the per-kind requirement can be
//     stronger than the route's Perm.

// probe names which of the two per-kind probe routes is asking, for the 404
// that says where the legacy route is.
type probe uint8

const (
	probeHealth probe = iota
	probeTest
)

// reach is what a kind's health or test route asks.
type reach struct {
	// known is false for a kind this switch has no case for; the exhaustiveness
	// test fails on it, and probeReach treats it as not served.
	known bool
	// served is whether core answers this route for the kind. When false the
	// route is 404 for every principal that passes the floor.
	served bool
	// req is the requirement a caller must satisfy, when served.
	req rbac.Requirement
	// legacy names the legacy route that serves this kind's probe until core
	// does, "" when the kind has none.
	legacy string
}

// probeReach is the per-kind rule of the health and test routes.
//
//   - ai, metadata, notify: the instance requirement (the seven scopes of HEAD's
//     settings.manage atom). HEAD's ai, metadata and notify probes were
//     settings.manage (GET /ai/endpoints/status, POST /metadata-sources/test,
//     GET /plugins/:id/health); the notify test has no HEAD route and asks the
//     same, so the integrations atom, which HEAD never gave a notify route, does
//     not gain one.
//   - download: not served. HEAD's /deluge/status and /deluge/test-connection ask
//     the integrations.manage atom (core and books scopes), which core cannot
//     express per kind until the W6 per-kind co-requirement lands; the legacy
//     routes stay alive for the atom's holders.
//   - indexer: not served. HEAD has no indexer surface and no ruling gives one.
func probeReach(k integrations.Kind, p probe) reach {
	switch k {
	case integrations.KindAI, integrations.KindMetadata, integrations.KindNotify:
		return reach{known: true, served: true, req: instancePerm()}
	case integrations.KindDownload:
		legacy := "GET /api/v1/deluge/status"
		if p == probeTest {
			legacy = "POST /api/v1/deluge/test-connection"
		}
		return reach{known: true, legacy: legacy}
	case integrations.KindIndexer:
		return reach{known: true}
	}
	return reach{}
}

// errNotServed is the 404 of a kind whose probe core does not answer yet.
func errNotServed(k integrations.Kind, r reach) error {
	detail := "core does not serve this for kind " + string(k) + " yet"
	if r.legacy != "" {
		detail = "this is served by the legacy route " + r.legacy + " until the per-kind co-requirement lands"
	}
	return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: detail}
}

// docFor is the per-kind rule as route documentation, generated from the same
// lookup the backstop reads.
func docFor(p probe) string {
	out := ""
	for _, k := range integrations.Kinds() {
		r := probeReach(k, p)
		if out != "" {
			out += "; "
		}
		switch {
		case r.served:
			out += string(k) + " asks " + r.req.String()
		case r.legacy != "":
			out += string(k) + " is not served here (404), use " + r.legacy
		default:
			out += string(k) + " is not served (404)"
		}
	}
	return out + "."
}

// guardProbe is the first lines of a health or test handler that names a kind:
// the principal, the kind (404 for a path value that is none), the per-kind rule
// (404 when core does not serve the pair, for every principal that passed the
// floor) and the backstop for the per-kind requirement. It runs before the
// instance lookup, so a caller without the permission learns nothing about
// which instances exist.
func (a *api) guardProbe(p probe, principal *auth.Principal, kind string) (integrations.Kind, error) {
	if err := a.guard(principal); err != nil {
		return "", err
	}
	k, err := kindRef(kind)
	if err != nil {
		return "", err
	}
	r := probeReach(k, p)
	if !r.served {
		return "", errNotServed(k, r)
	}
	if err := a.guardPerm(principal, r.req); err != nil {
		return "", err
	}
	return k, nil
}
