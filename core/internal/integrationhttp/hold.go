// file: core/internal/integrationhttp/hold.go
// version: 1.1.0
// guid: 4b84d061-0506-440f-b52d-4aa675443e56
// last-edited: 2026-11-11

package integrationhttp

import (
	"context"
	"net/http"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
)

// HoldReleaser is what the hold route needs of providerhttp's registry
// (05-metadata M2.4 ReleaseHold; 04 §4.6: a consumer declares the narrow
// interface it calls). It has two methods because the route must read the
// gate state without probing the remote: Registry.Health probes, and for a
// disabled or misconfigured instance it answers its static finding with no
// hold, so a route built on it could not release the hold of such an
// instance. HeldScope answers from the gate alone and sends nothing.
//
// Neither method is on providerhttp.Registry yet: F7-02 adds them (HeldScope
// over its holds table and GateFor), and F7-09 hands the route that
// *Registry. Until then the route runs against a fake.
type HoldReleaser interface {
	// HeldScope is the scope of ref's active hold, ok false when it has
	// none. It must not probe, send, or wait on the network.
	HeldScope(ref integrations.Ref) (scope string, ok bool)
	// ReleaseHold releases the hold on scope.
	ReleaseHold(ctx context.Context, scope string) error
}

// errNoHold is the 404 of a hold release for an instance with none.
func errNoHold() error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "no_hold", Detail: "the instance has no active hold"}
}

// releaseHold is DELETE .../instances/{id}/hold, the one-instance form of the
// core.integrations.release-holds task (M2.4). It asks the holds registry for
// the instance's held scope and releases that scope: the scope comes from the
// registry's answer, never from the request, so a caller can name no scope
// but its instance's. An instance with no hold is 404 no_hold and releases
// nothing, so a 204 always means a hold was released. The route never calls
// Registry.Health: it sends nothing to the remote, whatever the instance's
// state.
//
// The permission is checked here as well as by the router (guardManage): a
// release lifts a protection the remote asked for, and the route has no
// Registry write behind it to re-check the permission.
func (a *api) releaseHold(ctx context.Context, in *httpapi.In[instanceReq]) (httpapi.NoContent, error) {
	if err := a.guardManage(in.Principal); err != nil {
		return httpapi.NoContent{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	if a.holds == nil {
		return httpapi.NoContent{}, httpapi.Unavailable("releasing a hold is not available", 0)
	}
	if _, ok := a.reg.Config(ref); !ok {
		return httpapi.NoContent{}, errNoInstance()
	}
	scope, ok := a.holds.HeldScope(ref)
	if !ok || scope == "" {
		// An empty scope is no scope: releasing it could mean "every hold".
		return httpapi.NoContent{}, errNoHold()
	}
	if err := a.holds.ReleaseHold(ctx, scope); err != nil {
		// A plain error: httpapi answers 500 and withholds its text.
		return httpapi.NoContent{}, err
	}
	return httpapi.NoContent{}, nil
}

// holdView is the hold the health view carries. Detail is left out: it is a
// redacted excerpt of the remote's answer, and an instance's health is read
// with core:integrations.read, which must not make the route a way to read
// remote text (SEC-15).
func holdView(h *integrations.Hold) *IntegrationHold {
	if h == nil {
		return nil
	}
	return &IntegrationHold{Scope: h.Scope, Class: h.Class.String(), Until: h.Until, SetAt: h.SetAt}
}
