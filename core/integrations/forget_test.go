// file: core/integrations/forget_test.go
// version: 1.0.0
// guid: 6bf66469-b95c-4548-ae49-98d7fd69dc13
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
)

// TestDelete_ForgetsTheOutboundState (round-2 MINOR 3): a committed Delete calls
// Options.Forget once with the ref; a failed Delete (unknown ref, forbidden
// caller) does not; and a Forget that fails is logged, not returned, because
// the delete has already happened.
func TestDelete_ForgetsTheOutboundState(t *testing.T) {
	e := newEnv(t)
	var forgotten []integrations.Ref
	fail := false
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Forget = func(_ context.Context, ref integrations.Ref) error {
			forgotten = append(forgotten, ref)
			if fail {
				return errors.New("the gate store is down")
			}
			return nil
		}
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if err := r.Delete(manager(), integrations.Ref{Kind: integrations.KindNotify, Instance: "nobody"}); err == nil {
		t.Fatal("deleting an unknown ref succeeded")
	}
	if len(forgotten) != 0 {
		t.Fatalf("Forget ran for a Delete that did nothing: %v", forgotten)
	}
	fail = true
	if err := r.Delete(admin(), signalRef); err != nil {
		t.Fatalf("a failing Forget must not fail the Delete: %v", err)
	}
	if !slices.Equal(forgotten, []integrations.Ref{signalRef}) {
		t.Fatalf("Forget calls = %v, want one for %s", forgotten, signalRef)
	}
	if _, ok := r.Config(signalRef); ok {
		t.Fatal("the instance is still configured after Delete")
	}
}

// TestOutboundSpec_CarriesEveryConfiguredOrigin (round-2 MAJOR 1): the spec lists
// every effective destination, trusted or not, sorted, so an outbound layer
// can tell an instance's own hosts from the others lan_allow admits.
func TestOutboundSpec_CarriesEveryConfiguredOrigin(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	_, release := lease(t, r, signalRef)
	release()
	spec := lastSpec(t, e, signalRef)
	if len(spec.Trust.PrivateOrigins) != 0 {
		t.Fatalf("a stored destination without Local is trusted: %v", spec.Trust)
	}
	if !slices.Equal(spec.ConfiguredOrigins, []string{"http://192.0.2.20:80"}) {
		t.Fatalf("ConfiguredOrigins = %v, want the stored destination though it is not trusted", spec.ConfiguredOrigins)
	}

	o := integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Local: true,
		Settings: map[string]any{"host": "192.0.2.60", "port": 9000},
	}
	mustPut(t, admin(), r, o, nil)
	_, release = lease(t, r, oracleRef)
	release()
	spec = lastSpec(t, e, oracleRef)
	if !slices.Equal(spec.ConfiguredOrigins, []string{"smtp://192.0.2.60:9000"}) || !slices.Equal(spec.Trust.PrivateOrigins, spec.ConfiguredOrigins) {
		t.Fatalf("ConfiguredOrigins = %v, Trust = %v", spec.ConfiguredOrigins, spec.Trust)
	}
}
