// file: core/integrations/ai/validate_probe_test.go
// version: 1.0.1
// guid: d4b7e20a-8f13-4c59-b6a2-91e5c3f07d48
// last-edited: 2026-11-19

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/storage"
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
	// Documents rather than tests: the bench is keyed by the full Ref, so a notify
	// ref can never match an ai bench and ObserveProbe's kind guard is defensive.
	h.r.ObserveProbe(integrations.Ref{Kind: integrations.KindNotify, Instance: ref.Instance}, integrations.Health{Status: integrations.HealthOK})
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Error("a probe that was not an OK ai answer cleared the bench")
	}
	h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthOK})
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("an OK ai probe left the bench in force")
	}
}

// TestRouter_QuotaBenchSurvivesALaterFailureAndAProbe: a quota bench stays a
// quota bench, with its end time, when a call that was already in flight
// when the quota answer landed then fails some other benching way, so a probe
// (GET /v1/models, which succeeds on a spent balance) cannot clear it. The
// endpoint has two slots; call B is sent first and held at the provider, call
// A gets the quota 429, then B's own failure arrives.
func TestRouter_QuotaBenchSurvivesALaterFailureAndAProbe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		class integrations.Class
	}{
		{"a timeout", integrations.ClassTransient},
		{"a rate limit", integrations.ClassRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.setConfig(ai.Config{CooldownBase: 30 * time.Second, QuotaCooldown: 4 * time.Hour})
			c := h.capability("riddle")
			f := fake()
			set := textEndpoint("joker-7b", "gotham.riddle")
			set.Concurrency = 2
			ref := h.addEndpoint("arkham", 1, set, f)
			ctx := context.Background()

			release := make(chan struct{})
			f.ScriptText(
				coretest.FakeAIStep{Block: release, Err: classErr(tc.class)}, // B, taken first
				coretest.FakeAIStep{Err: classErr(integrations.ClassQuota)},  // A
			)
			var wg sync.WaitGroup
			wg.Go(func() { _, _ = h.r.Generate(ctx, c, text("who?")) })
			for deadline := time.Now().Add(5 * time.Second); len(f.Calls()) < 1; {
				if time.Now().After(deadline) {
					t.Fatal("call B never reached the provider")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := h.r.Generate(ctx, c, text("who?")); err == nil {
				t.Fatal("the quota failure succeeded")
			}
			quotaUntil, ok := h.r.BenchedUntil(ref)
			if !ok || quotaUntil.Sub(h.clk.Now()) != 4*time.Hour {
				t.Fatalf("quota bench = %v, %v; want 4h", quotaUntil, ok)
			}
			close(release)
			wg.Wait()

			// B's failure neither shortened the bench nor made it clearable.
			if got, ok := h.r.BenchedUntil(ref); !ok || !got.Equal(quotaUntil) {
				t.Fatalf("after %s landed on a quota bench: until = %v, %v; want it kept at %v", tc.name, got, ok, quotaUntil)
			}
			h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthOK})
			if got, ok := h.r.BenchedUntil(ref); !ok || !got.Equal(quotaUntil) {
				t.Errorf("an OK probe cleared a quota bench still in force: %v, %v", got, ok)
			}
			// Past B's own failure window and inside the quota window it holds.
			h.clk.Advance(time.Minute)
			h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthOK})
			if _, ok := h.r.BenchedUntil(ref); !ok {
				t.Error("an OK probe a minute later cleared the quota bench")
			}
			// Once the quota window has passed a probe clears it.
			h.clk.Advance(4 * time.Hour)
			h.r.ObserveProbe(ref, integrations.Health{Status: integrations.HealthOK})
			if _, ok := h.r.BenchedUntil(ref); ok {
				t.Error("an OK probe left the bench in force after the quota window")
			}
		})
	}
}

// renamed is the ai instance called name as Config reports it, with its display
// name changed: a Put that touches no routing field.
func renamed(t *testing.T, h *harness, name string) integrations.InstanceConfig {
	t.Helper()
	cur, ok := h.reg.Config(aiRef(name))
	if !ok {
		t.Fatalf("no instance %s", name)
	}
	cur.Name = name + " (renamed)"
	return cur
}

// capsOverlay is an operator overlay that locks the capabilities of the ai
// instances named in locks to the ticks given.
func capsOverlay(locks map[string][]string) iter.Seq2[config.Path, config.OperatorValue] {
	return func(yield func(config.Path, config.OperatorValue) bool) {
		for name, ticks := range locks {
			raw, _ := json.Marshal(ticks)
			p := config.Path("integrations.ai." + name + ".capabilities")
			if !yield(p, config.OperatorValue{Value: raw, Src: config.SourceEnv, By: "env " + string(p)}) {
				return
			}
		}
	}
}

// retiredTicks is the ticks of a row stored before the rule: a retired
// capability beside a live one.
var retiredTicks = []string{"gotham.riddle", "retired.capability"}

// TestAIEndpoint_RetiredTickKeptOnPutRefusedOnImport: the ai rule treats a tick
// the catalog no longer knows differently on the two write paths, and the
// difference is the one the seams file records. A Put of a row that already
// carries the tick keeps it (the stored row is passed as "stored"); an import
// has no stored row and is refused, which M6 quarantines. A wildcard is refused
// on both.
func TestAIEndpoint_RetiredTickKeptOnPutRefusedOnImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.capability("riddle")
	// Rows stored before the rule existed: no validator is installed yet.
	if _, err := aiPut(h, "arkham", textEndpoint("joker-7b", retiredTicks...)); err != nil {
		t.Fatalf("storing the retired tick: %v", err)
	}
	if _, err := aiPut(h, "blackgate", textEndpoint("joker-7b", "gotham.riddle", "*")); err != nil {
		t.Fatalf("storing the wildcard tick: %v", err)
	}
	// The rule is installed by a Router whose catalog knows riddle only.
	v, err := ai.NewRouter(ai.RouterOptions{Registry: h.reg, ValidateEndpoints: true})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.RegisterCapability("gotham", capSpec("riddle")); err != nil {
		t.Fatal(err)
	}

	// Put: the retired tick is kept; the wildcard is refused.
	if _, err := h.reg.Put(admin(), renamed(t, h, "arkham"), nil); err != nil {
		t.Errorf("a Put of a row that already ticks a retired capability: %v, want it kept", err)
	}
	if _, err := h.reg.Put(admin(), renamed(t, h, "blackgate"), nil); !errors.Is(err, integrations.ErrInvalid) || !errors.Is(err, ai.ErrInvalidEndpoint) {
		t.Errorf("a Put of a row that ticks a wildcard: err = %v, want ErrInvalid wrapping ErrInvalidEndpoint", err)
	}

	// Import: the same retired tick is refused, nothing staged.
	im := integrations.NativeImporter(h.iks, integrations.ImportAudit(h.au), integrations.ImportValidator(h.reg))
	record := func(name string, ticks ...string) integrations.StoredInstance {
		return integrations.StoredInstance{Config: integrations.InstanceConfig{
			Ref: aiRef(name), Adapter: fakeAdapter, Name: name, Enabled: true,
			Origin: integrations.OriginUI, Version: 3, Settings: settingsMap(t, textEndpoint("joker-7b", ticks...)),
		}}
	}
	for _, tc := range []struct {
		name  string
		ticks []string
		want  error
	}{
		{"wayne-tower", retiredTicks, ai.ErrUnknownCapability},
		{"wayne-annex", []string{"gotham.riddle", "*"}, ai.ErrInvalidEndpoint},
	} {
		b := h.iks.NewBatch()
		err := im.ImportInstance(ctx, b, record(tc.name, tc.ticks...))
		if !errors.Is(err, integrations.ErrInvalid) || !errors.Is(err, tc.want) {
			t.Errorf("import of %v: err = %v, want ErrInvalid wrapping %v", tc.ticks, err, tc.want)
		}
		if n := b.Count(); n != 0 {
			t.Errorf("a refused import of %v staged %d writes", tc.ticks, n)
		}
		_ = b.Close()
	}
	// A clean record imports.
	b := h.iks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := im.ImportInstance(ctx, b, record("wayne-manor", "gotham.riddle")); err != nil {
		t.Fatalf("a clean import: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// TestAIInstancePut_OperatorLockedTickIsGrandfathered: the ai capabilities
// field can be locked by an operator value (integrations.ai.<id>.capabilities),
// and the rule is handed what the row serves as its "stored" layer, so a tick
// the operator locks, retired since, is kept like a stored one and does not
// block every other Put of the instance. A locked wildcard is still refused,
// as the rule never accepts one.
func TestAIInstancePut_OperatorLockedTickIsGrandfathered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t, withEndpointValidation())
	h.capability("riddle")
	if _, err := aiPut(h, "arkham", textEndpoint("joker-7b", "gotham.riddle")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := aiPut(h, "blackgate", textEndpoint("joker-7b", "gotham.riddle")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := h.reg.SetOverlay(ctx, capsOverlay(map[string][]string{
		"arkham":    retiredTicks,
		"blackgate": {"gotham.riddle", "*"},
	})); err != nil {
		t.Fatalf("SetOverlay: %v", err)
	}
	cur, _ := h.reg.Config(aiRef("arkham"))
	if cur.Locked["capabilities"] == "" {
		t.Fatalf("the operator overlay did not lock capabilities: %+v", cur.Locked)
	}

	if _, err := h.reg.Put(admin(), renamed(t, h, "arkham"), nil); err != nil {
		t.Errorf("a Put that changes only the name, over a locked retired tick: %v, want it accepted", err)
	}
	if _, err := h.reg.Put(admin(), renamed(t, h, "blackgate"), nil); !errors.Is(err, ai.ErrInvalidEndpoint) {
		t.Errorf("a Put over a locked wildcard: err = %v, want ErrInvalidEndpoint", err)
	}
}

// TestNewRouter_FailedValidatorInstallReleasesTheGauge: a second validating
// Router on one Registry is refused, and the gauge callback it had registered
// is released with it. The leaked callback would report the refused Router's
// own empty bench for every instance, over the live Router's.
func TestNewRouter_FailedValidatorInstallReleasesTheGauge(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withMetrics(), withEndpointValidation())
	h.setConfig(ai.Config{CooldownBase: time.Hour})
	c := h.capability("riddle")
	f := fake()
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	_, _ = h.r.Generate(context.Background(), c, text("who?"))
	if _, ok := h.r.BenchedUntil(ref); !ok {
		t.Fatal("not benched")
	}

	if _, err := ai.NewRouter(ai.RouterOptions{Registry: h.reg, Metrics: h.scope, ValidateEndpoints: true}); !errors.Is(err, integrations.ErrInvalid) {
		t.Fatalf("a second validating Router: err = %v, want ErrInvalid", err)
	}
	family := metricstest.FamilyPrefix(h.scope) + "endpoint_benched"
	mfs, err := h.prov.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			seen = true
			if got := m.GetGauge().GetValue(); got != 1 {
				t.Errorf("%s = %v for the benched instance, want 1: a refused Router's gauge callback is still registered", family, got)
			}
		}
	}
	if !seen {
		t.Fatalf("no %s series", family)
	}
}
