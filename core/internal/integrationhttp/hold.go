// file: core/internal/integrationhttp/hold.go
// version: 1.3.0
// guid: 4b84d061-0506-440f-b52d-4aa675443e56
// last-edited: 2026-11-11

package integrationhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
)

// HoldReleaser is what the hold route needs of providerhttp's registry
// (05-metadata M2.4; 04 §4.6: a consumer declares the narrow interface it
// calls). It is the atomic pair F7-02 builds on providerhttp.Registry:
//
//   - HoldFor reads ref's active hold itself, from the gate alone: no client
//     built, nothing probed or sent, so a disabled or misconfigured instance
//     works. (Registry.Health probes, and for such an instance answers a static
//     finding with a nil Hold, so a route built on it could not release the
//     hold of one.)
//   - ReleaseHoldIf ends expect only while it is still the hold in force. No
//     hold in force (ended, or already released) is nil, the goal being met; a
//     different hold in force releases nothing and returns an error for which
//     errors.Is(err, ErrHoldChanged) holds.
//
// The two methods have providerhttp.Registry's signatures (the hold is
// integrations.Hold), so F7-09 hands the route that *Registry, with one
// mapping: ErrHoldChanged here stands for providerhttp.ErrHoldChanged, which
// this package cannot import until F7-02 is merged (see ErrHoldChanged).
type HoldReleaser interface {
	HoldFor(ref integrations.Ref) (integrations.Hold, bool)
	ReleaseHoldIf(ctx context.Context, expect integrations.Hold) error
}

// ErrHoldChanged is what HoldReleaser.ReleaseHoldIf returns, wrapped or not,
// when a different hold than the one expected is in force. It mirrors
// providerhttp.ErrHoldChanged. F7-09 either wraps the providerhttp registry
// so that error maps to this one, or, once F7-02 has merged, redeclares this
// as `var ErrHoldChanged = providerhttp.ErrHoldChanged` (one line, same
// semantics).
var ErrHoldChanged = errors.New("integrationhttp: the hold changed since it was read")

// errHoldChanged is the 409 of a release that found a different hold in
// force: the answer carries no hold text, and the operator reads the
// instance's health again and decides again.
func errHoldChanged(cause error) error {
	return &httpapi.Error{Status: http.StatusConflict, Code: "hold_changed",
		Detail: "the instance's hold changed since it was read; read it again before releasing", Err: cause}
}

// releaseHold is DELETE .../instances/{id}/hold, the one-instance form of the
// core.integrations.release-holds task (M2.4). It reads the instance's hold
// with HoldFor and releases exactly that hold with ReleaseHoldIf: the hold
// comes from the holds registry, never from the request, so a caller can name
// no scope but its instance's, and a hold that changed between the read and
// the release is not released unseen (409 hold_changed).
//
// An instance with no hold is 204 and releases nothing: HoldFor does not
// probe, so 204 cannot hide a hold that was not released, and the call is
// idempotent; a released hold is 204 too. A missing instance is 404. The route
// never calls Registry.Health and sends nothing to the remote, whatever the
// instance's state.
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
	h, ok := a.holds.HoldFor(ref)
	if !ok || h.Scope == "" {
		// No hold; and a hold with no scope is none (fail closed: nothing
		// that names no scope is released).
		return httpapi.NoContent{}, nil
	}
	if err := a.holds.ReleaseHoldIf(ctx, h); err != nil {
		if errors.Is(err, ErrHoldChanged) {
			return httpapi.NoContent{}, errHoldChanged(err)
		}
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
