// file: core/integrations/ai/validate_probe_test.go
// version: 1.0.0
// guid: d4b7e20a-8f13-4c59-b6a2-91e5c3f07d48
// last-edited: 2026-11-19

package ai_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// aiPut is a create (version 0) of an ai instance named name with set.
func aiPut(h *harness, name string, set ai.EndpointSettings) (integrations.InstanceConfig, error) {
	return h.reg.Put(admin(), integrations.InstanceConfig{
		Ref: aiRef(name), Adapter: fakeAdapter, Name: name, Enabled: true, Settings: settingsMap(h.t, set),
	}, nil)
}

// TestAIInstancePut_WildcardCapabilityRefused: NewRouter installs
// ValidateEndpoint as the ai kind's validator, so a Put through the Registry
// is refused (ErrInvalid, the 422) without the kernel calling anything, and
// nothing is stored.
func TestAIInstancePut_WildcardCapabilityRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEndpointValidation())
	h.capability("riddle")

	_, err := aiPut(h, "arkham", textEndpoint("joker-7b", "*"))
	if !errors.Is(err, integrations.ErrInvalid) || !errors.Is(err, ai.ErrInvalidEndpoint) {
		t.Fatalf("Put of a wildcard tick: err = %v, want ErrInvalid wrapping ErrInvalidEndpoint", err)
	}
	if _, ok := h.reg.Config(aiRef("arkham")); ok {
		t.Error("a refused Put stored an instance")
	}
	for _, tick := range []string{"gotham.*", "gotham.nope"} {
		if _, err := aiPut(h, "arkham", textEndpoint("joker-7b", tick)); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("Put of tick %q: err = %v, want ErrInvalid", tick, err)
		}
	}
	zero := textEndpoint("joker-7b", "gotham.riddle")
	zero.Concurrency = 0
	if _, err := aiPut(h, "arkham", zero); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("Put of concurrency 0: err = %v, want ErrInvalid", err)
	}

	// A valid Put goes in; an update that adds an unknown tick is refused and
	// leaves the stored row alone.
	got, err := aiPut(h, "arkham", textEndpoint("joker-7b", "gotham.riddle"))
	if err != nil {
		t.Fatalf("a valid Put: %v", err)
	}
	got.Settings = settingsMap(t, textEndpoint("joker-7b", "gotham.riddle", "gotham.nope"))
	if _, err := h.reg.Put(admin(), got, nil); !errors.Is(err, integrations.ErrInvalid) {
		t.Fatalf("an update adding an unknown tick: err = %v, want ErrInvalid", err)
	}
	if cur, _ := h.reg.Config(aiRef("arkham")); cur.Version != got.Version {
		t.Errorf("a refused update moved the version to %d", cur.Version)
	}
	// Test applies the same rule.
	if _, err := h.reg.Test(admin(), integrations.InstanceConfig{
		Ref: aiRef("arkham"), Adapter: fakeAdapter, Settings: settingsMap(t, textEndpoint("joker-7b", "*")),
	}, nil); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("Test of a wildcard tick: err = %v, want ErrInvalid", err)
	}
}

// TestNewRouter_ValidationIsOptIn: without ValidateEndpoints a Router
// installs no validator, so a Put stores what it stored before F7-09 (a
// wildcard tick is kept and never honoured); with it, a second Router over one
// Registry would own the ai kind's rule twice and is refused.
func TestNewRouter_ValidationIsOptIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.capability("riddle")
	if _, err := aiPut(h, "arkham", textEndpoint("joker-7b", "*")); err != nil {
		t.Errorf("Put of a wildcard tick without ValidateEndpoints: %v, want it stored as before", err)
	}
	// The first ValidateEndpoints Router takes the rule, the next is refused.
	first, err := ai.NewRouter(ai.RouterOptions{Registry: h.reg, ValidateEndpoints: true})
	if err != nil {
		t.Fatalf("NewRouter with ValidateEndpoints: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if _, err := ai.NewRouter(ai.RouterOptions{Registry: h.reg, ValidateEndpoints: true}); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a second validating Router on one Registry: err = %v, want ErrInvalid", err)
	}
	// A Router that does not validate never conflicts.
	plain, err := ai.NewRouter(ai.RouterOptions{Registry: h.reg})
	if err != nil {
		t.Fatalf("a non-validating Router beside a validating one: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
}

// quotaBench benches ref for core.ai.quota_cooldown through a scripted 429
// quota failure.
func quotaBench(t *testing.T, h *harness, f *coretest.FakeAI, c ai.Capability) {
	t.Helper()
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassQuota)})
	if _, err := h.r.Generate(context.Background(), c, text("who?")); err == nil {
		t.Fatal("the scripted quota failure succeeded")
	}
}

// TestRouter_ClearBenchLeavesQuotaBench: ClearBench ends a transient bench
// and leaves a quota bench alone, because a health probe (GET /v1/models)
// succeeds on a spent balance and says nothing about the quota.
func TestRouter_ClearBenchLeavesQuotaBench(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.setConfig(ai.Config{CooldownBase: 30 * time.Second, QuotaCooldown: 4 * time.Hour})
	c := h.capability("riddle")
	f := fake()
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)

	// A transient bench is cleared.
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	_, _ = h.r.Generate(context.Background(), c, text("who?"))
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Fatal("the transient failure did not bench the endpoint")
	}
	h.r.ClearBench(ref)
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("ClearBench left a transient bench in force")
	}

	// A quota bench is not.
	quotaBench(t, h, f, c)
	until, ok := h.r.BenchedUntil(ref)
	if !ok || until.Sub(h.clk.Now()) != 4*time.Hour {
		t.Fatalf("quota bench = %v, %v; want 4h", until, ok)
	}
	h.r.ClearBench(ref)
	if got, ok := h.r.BenchedUntil(ref); !ok || !got.Equal(until) {
		t.Errorf("ClearBench changed a quota bench: %v, %v (was %v)", got, ok, until)
	}

	// A transient failure after the quota window replaces the class, so a
	// later ClearBench ends it.
	h.clk.Advance(4*time.Hour + time.Second)
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	_, _ = h.r.Generate(context.Background(), c, text("who?"))
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Fatal("not benched after the second failure")
	}
	h.r.ClearBench(ref)
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("a transient bench that followed a quota bench survived ClearBench")
	}

	// A quota bench whose window has passed no longer holds anything back, and
	// ClearBench resets the endpoint's failure count.
	quotaBench(t, h, f, c)
	h.clk.Advance(4*time.Hour + time.Second)
	h.r.ClearBench(ref)
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	before := h.clk.Now()
	_, _ = h.r.Generate(context.Background(), c, text("who?"))
	if until, _ := h.r.BenchedUntil(ref); until.Sub(before) != 30*time.Second {
		t.Errorf("after an expired quota bench was cleared, the next failure benches for %v, want the base 30s", until.Sub(before))
	}
}

// TestAIHealthTaskClearsTheBench: with Router.ObserveProbe on the Registry's
// Probed hook, an ai endpoint that keeps probing OK leaves its transient
// bench at once, though HealthChanged never fires for it again; a quota bench
// stays; a probe that is not OK, or of another kind, clears nothing.
func TestAIHealthTaskClearsTheBench(t *testing.T) {
	t.Parallel()
	var h *harness
	h = newHarness(t, withProbed(func(ref integrations.Ref, hh integrations.Health) { h.r.ObserveProbe(ref, hh) }))
	h.setConfig(ai.Config{CooldownBase: time.Hour, QuotaCooldown: 4 * time.Hour})
	c := h.capability("riddle")
	f := fake()
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)
	ctx := context.Background()

	if _, err := h.reg.Health(ctx, ref); err != nil { // the first probe: ok, no bench
		t.Fatal(err)
	}
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	_, _ = h.r.Generate(ctx, c, text("who?"))
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Fatal("not benched")
	}
	if err := h.reg.ProbeAll(ctx); err != nil { // the health task: the status stays ok
		t.Fatal(err)
	}
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("an OK probe left a transient bench in force")
	}

	quotaBench(t, h, f, c)
	if err := h.reg.ProbeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Error("an OK probe cleared a quota bench")
	}

	// ObserveProbe itself: only an OK answer of an ai ref clears.
	h.clk.Advance(4*time.Hour + time.Second)
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	_, _ = h.r.Generate(ctx, c, text("who?"))
	h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthDown})
	h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthDegraded})
	h.r.ObserveProbe(integrations.Ref{Kind: integrations.KindNotify, Instance: ref.Instance}, integrations.Health{Status: integrations.HealthOK})
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Error("a probe that was not an OK ai answer cleared the bench")
	}
	h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthOK})
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("an OK ai probe left the bench in force")
	}
}
