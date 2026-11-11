// file: core/internal/integrationhttp/hold.go
// version: 1.0.0
// guid: 4b84d061-0506-440f-b52d-4aa675443e56
// last-edited: 2026-11-11

package integrationhttp

import (
	"context"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
)

// HoldReleaser is the one method of providerhttp's registry the hold route
// calls (05-metadata M2.4 ReleaseHold; 04 §4.6: a consumer declares the narrow
// interface it calls). Its signature matches providerhttp.Registry.ReleaseHold
// exactly, so F7-09 hands the route providerhttp's *Registry. Until F7-02
// builds that registry the route runs against a fake.
type HoldReleaser interface {
	ReleaseHold(ctx context.Context, scope string) error
}

// releaseHold is DELETE .../instances/{id}/hold, the one-instance form of the
// core.integrations.release-holds task (M2.4). It reads the instance's
// health, and when a hold is active releases the hold's own scope: the scope
// comes from the Registry's answer, never from the request, so a caller can
// name no scope but its instance's. With no active hold it answers 204 and
// calls nothing.
//
// The probe Health runs is the cost of reading the gate state through the
// Registry's API: the Registry has no cheaper accessor, and a hold makes the
// gate refuse before sending, so the probe sends nothing while one is active.
func (a *api) releaseHold(ctx context.Context, in *httpapi.In[instanceReq]) (httpapi.NoContent, error) {
	if err := a.guard(in.Principal); err != nil {
		return httpapi.NoContent{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	if a.holds == nil {
		return httpapi.NoContent{}, httpapi.Unavailable("releasing a hold is not available", 0)
	}
	h, err := a.reg.Health(ctx, ref)
	if err != nil {
		return httpapi.NoContent{}, mapError(err)
	}
	if h.Hold == nil {
		return httpapi.NoContent{}, nil
	}
	if err := a.holds.ReleaseHold(ctx, h.Hold.Scope); err != nil {
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
