// file: core/internal/authhttp/guard.go
// version: 1.0.1
// guid: 7a3f9d12-6c85-4e0b-a1d7-93b8e5c2f046
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"net/http"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// spec is one route of the table: the httpapi.Route it registers and what the
// handler enforces for itself.
type spec struct {
	method, path, name, summary, doc string
	perm                             rbac.Requirement
	guards                           httpapi.Guards
	rate                             httpapi.RateClass
	success                          int
	// needUser: the route reads or changes credential metadata, so the
	// auth-disabled principal (no user) is refused 401 (05-authn §4.11).
	needUser bool
}

func (s spec) route() httpapi.Route {
	return httpapi.Route{
		Method: s.method, Path: s.path, Name: s.name, Perm: s.perm, Summary: s.summary, Doc: s.doc,
		Tags: []string{"auth"}, Guards: s.guards, MaxBody: maxBody, RateClass: s.rate, Success: s.success,
	}
}

func (s spec) public() bool { return s.perm.Kind() == rbac.KindPublic }

// admit applies the route's own requirement to the caller, in the order stage
// 13 will: a caller must exist, hold a user where the route needs one, come
// through a method that may change credentials where the route is Interactive,
// carry owner assurance where it is Owner, and satisfy Perm. It returns the
// refusal, or nil.
func (s spec) admit(p *auth.Principal) error {
	if s.public() {
		return nil
	}
	if p == nil || (s.needUser && p.User == nil) {
		return errUnauthenticated()
	}
	if s.guards.Interactive && !p.Method.MayChangeCredentials() {
		return errInteractive()
	}
	if s.guards.Owner && p.Assurance&auth.AssuranceOwner == 0 {
		return errOwner()
	}
	if !holds(p, s.perm) {
		return forbidden(s.perm)
	}
	return nil
}

// holds reports whether p's checker satisfies need. A principal with no
// checker holds nothing, and AnyAuthenticated is no exception: it is "core:self
// or a non-Confining scope" (05-rbac 4.10), so a Confined principal (a
// fingerprint-worker or arr-facade key) never satisfies it.
func holds(p *auth.Principal, need rbac.Requirement) bool {
	if need.Kind() == rbac.KindPublic {
		return true
	}
	return p != nil && p.Perms != nil && p.Perms.Satisfies(need)
}

// guard wraps fn so it runs only for a caller admit accepts. fn receives the
// admitted principal (nil only on a Public route with no credential).
func guard[Req, Resp any](s spec, fn func(ctx context.Context, p *auth.Principal, in *httpapi.In[Req]) (Resp, error)) func(ctx context.Context, in *httpapi.In[Req]) (Resp, error) {
	return func(ctx context.Context, in *httpapi.In[Req]) (Resp, error) {
		if err := s.admit(in.Principal); err != nil {
			var zero Resp
			return zero, err
		}
		return fn(ctx, in.Principal, in)
	}
}

// typed registers a JSON route whose handler runs after admit.
func typed[Req, Resp any](g *httpapi.Group, s spec, fn func(ctx context.Context, p *auth.Principal, in *httpapi.In[Req]) (Resp, error)) {
	httpapi.Handle(g, s.route(), guard(s, fn))
}

// rawHandler wraps h so it runs only for a caller admit accepts, and so an
// error it returns is answered as a problem. A handler returns the error to
// answer when it has written nothing.
func (a *api) rawHandler(s spec, h func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		err := s.admit(p)
		if err == nil {
			err = h(w, r, p)
		}
		if err != nil {
			httpapi.WriteError(w, r, err, a.log)
		}
	})
}

// route is one entry of the table: its spec and how it is registered on the
// group it belongs to. Register registers every entry; a test registers the
// Public ones alone, which Build accepts before stage 13 exists.
type route struct {
	s spec
	// fixed puts the route on the router's CoreFixedGroup(OIDCCallbackBase)
	// instead of the core group.
	fixed bool
	reg   func(g *httpapi.Group)
}

// jsonRoute is a route whose handler is a typed JSON handler behind admit.
func jsonRoute[Req, Resp any](s spec, fn func(ctx context.Context, p *auth.Principal, in *httpapi.In[Req]) (Resp, error)) route {
	return route{s: s, reg: func(g *httpapi.Group) { typed(g, s, fn) }}
}

// rawRoute is a route whose handler writes its own response, behind admit.
func (a *api) rawRoute(s spec, doc httpapi.RawDoc, h func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error) route {
	return route{s: s, reg: func(g *httpapi.Group) { httpapi.HandleRaw(g, s.route(), doc, a.rawHandler(s, h)) }}
}

// routes is every route of 05-authn §4.13, in table order.
func (a *api) routes() []route {
	var out []route
	out = append(out, a.entryRoutes()...)
	out = append(out, a.oidcRoutes()...)
	out = append(out, a.selfRoutes()...)
	out = append(out, a.keyRoutes()...)
	out = append(out, a.userRoutes()...)
	return out
}

// denyDominance records core.permission.deny for a 403 dominance_required,
// where the 403 is written (05-rbac §8; seams.d/F4-02.md). target is the user
// the actor does not dominate.
func (a *api) denyDominance(ctx context.Context, s spec, p *auth.Principal, target string) {
	if a.Audit == nil {
		return
	}
	e := audit.Event{
		Action: "core.permission.deny", Outcome: audit.Denied, Reason: "dominance_required",
		Route: s.method + " " + routeBase + s.path, Permission: s.perm.String(),
	}
	if p != nil && p.User != nil {
		e.Actor = audit.Actor{UserID: string(p.User.ID), Username: p.User.Username, Method: string(p.Method), CredentialID: p.CredentialID}
	}
	if target != "" {
		e.Target = entity.Ref{Module: "core", Kind: "user", ID: target}
	}
	a.Audit.Emit(ctx, e)
}

// routeBase is the core group's base path, for the audit route.
const routeBase = "/api/core/v1"
