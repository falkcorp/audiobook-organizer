// file: core/internal/integrationhttp/validate_hold_test.go
// version: 1.0.0
// guid: 91c3f6b8-2e07-4a15-8d4c-b7e5a09f3c26
// last-edited: 2026-11-19

package integrationhttp

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// TestIntegrationHTTP_KindValidatorRefusalIs422: a settings block a kind's
// validator refuses is a 422 validation_failed on create and on replace,
// nothing is stored, and the validator's text is not in the answer.
func TestIntegrationHTTP_KindValidatorRefusalIs422(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	if err := e.reg.SetValidator(integrations.KindNotify, func(_ integrations.AdapterID, settings, _ map[string]any) error {
		if c, _ := settings["channel"].(string); c == "*" {
			return errors.New("the Riddler's wildcard channel")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bad := signalBody(roofID, "http://192.0.2.20/", "alfred-1")
	bad.Settings["channel"] = "*"
	req := createReq{Kind: kindNotify, Body: bad}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	_, err := e.api.createInstance(ctx, in(ctx, req))
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	assertNoLeak(t, "the 422", errBlob(t, err), "Riddler", "wildcard")
	if _, ok := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}); ok {
		t.Error("a refused create stored an instance")
	}
	if e.secretValue(t, kindNotify, roofID, "password") != "" {
		t.Error("a refused create stored a secret")
	}

	// A valid one goes in; the same refusal on replace leaves it unchanged.
	created := e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	edit := signalBody(roofID, "http://192.0.2.20/", "")
	edit.Settings["channel"] = "*"
	edit.Version = created.Version
	_, err = e.api.updateInstance(ctx, in(ctx, updateReq{Kind: kindNotify, ID: roofID, Body: replaceOf(edit)}))
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if got := e.get(t, ctx, kindNotify, roofID); got.Version != created.Version || got.Settings["channel"] != "gcpd" {
		t.Errorf("a refused replace changed the instance: %+v", got)
	}
}

// TestReleaseHold_ProviderHTTPErrHoldChangedIs409: the route recognises
// providerhttp's own ErrHoldChanged, so the real *providerhttp.Registry's
// ReleaseHoldIf answer maps to the 409 with no translation layer.
func TestReleaseHold_ProviderHTTPErrHoldChangedIs409(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx()
	e.create(t, ctx, kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}
	e.holds.holdOn(ref, e.hold("gotham-quota", 0))
	e.holds.err = fmt.Errorf("providerhttp registry: %w", providerhttp.ErrHoldChanged)

	_, err := e.api.releaseHold(ctx, in(ctx, instanceReq{Kind: kindNotify, ID: roofID}))
	wantStatus(t, err, http.StatusConflict, "hold_changed")
}
