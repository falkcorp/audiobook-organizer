// file: core/integrations/ai/router_test.go
// version: 1.0.0
// guid: 7d2b5e81-4c36-4a90-b8f3-1e9a6c0d4f27
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// classErr is the error an adapter returns for a failure of class c.
func classErr(c integrations.Class) error {
	if c == integrations.ClassUnknown {
		return errors.New("the Riddler's answer made no sense to anyone")
	}
	return &integrations.Error{Ref: aiRef("arkham"), Op: "generate", Class: c, Err: errors.New("scripted failure")}
}

func wantNoCapable(t *testing.T, err error) *ai.NoCapableEndpointError {
	t.Helper()
	if !errors.Is(err, ai.ErrNoCapableEndpoint) {
		t.Fatalf("err = %v, want ErrNoCapableEndpoint", err)
	}
	var nc *ai.NoCapableEndpointError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %T, want *NoCapableEndpointError", err)
	}
	return nc
}

func refusalFor(nc *ai.NoCapableEndpointError, name string) string {
	for _, r := range nc.Refusals {
		if r.Endpoint == aiRef(name) {
			return r.Reason
		}
	}
	return ""
}

// I2: an endpoint with no ticks serves nothing, and one that ticks another
// capability does not serve this one.
func TestRouter_DefaultDenyEmptyTicks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	f := fake()
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b"), f)
	other := fake()
	h.addEndpoint("blackgate", 2, textEndpoint("joker-7b", "gotham.cipher"), other)
	h.capability("cipher")

	_, err := h.r.Generate(context.Background(), c, text("who?"))
	nc := wantNoCapable(t, err)
	if got := refusalFor(nc, "arkham"); !strings.Contains(got, "default-deny") {
		t.Errorf("arkham refusal = %q, want the default-deny reason", got)
	}
	if got := refusalFor(nc, "blackgate"); !strings.Contains(got, "not ticked") {
		t.Errorf("blackgate refusal = %q, want a not-ticked reason", got)
	}
	if n := len(f.Calls()) + len(other.Calls()); n != 0 {
		t.Errorf("%d call(s) reached an endpoint that was never ticked", n)
	}
	if got := h.r.Capacity(c); got != 0 {
		t.Errorf("Capacity = %d, want 0 with nothing ticked", got)
	}
}

// I2: a wildcard tick is refused on Put and, were one stored anyway, never
// honoured.
func TestRouter_WildcardRejectedOnPut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")

	for _, tick := range []string{"*", "gotham.*", "*.riddle", "go*"} {
		err := h.r.ValidateEndpoint(settingsMap(t, textEndpoint("joker-7b", tick)), nil)
		if !errors.Is(err, ai.ErrInvalidEndpoint) || !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("ValidateEndpoint(%q) = %v, want an ErrInvalidEndpoint naming the wildcard", tick, err)
		}
	}
	if err := h.r.ValidateEndpoint(settingsMap(t, textEndpoint("joker-7b", "gotham.riddle")), nil); err != nil {
		t.Errorf("a plain tick was refused: %v", err)
	}
	zero := textEndpoint("joker-7b")
	zero.Concurrency = 0
	if err := h.r.ValidateEndpoint(settingsMap(t, zero), nil); !errors.Is(err, ai.ErrInvalidEndpoint) {
		t.Errorf("concurrency 0 = %v, want ErrInvalidEndpoint (HEAD read 0 as 1)", err)
	}

	// A row that holds "*" anyway (written before this rule, or by a store that
	// skipped the check) matches no capability.
	f := fake()
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "*"), f)
	_, err := h.r.Generate(context.Background(), c, text("who?"))
	nc := wantNoCapable(t, err)
	if got := refusalFor(nc, "arkham"); !strings.Contains(got, "wildcard") {
		t.Errorf("refusal = %q, want a wildcard-not-honoured reason", got)
	}
	if len(f.Calls()) != 0 {
		t.Error("a wildcard tick served a call")
	}
}

// ValidateEndpoint also refuses a locality an operator cannot set and a
// feature nobody defined, which the settings schema cannot say for an array.
func TestRouter_ValidateEndpointLocalityAndFeatures(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.capability("riddle")
	for _, loc := range []ai.Locality{ai.LocalityAuto, ai.LocalityLocal, ai.LocalityCloud, "auto"} {
		set := textEndpoint("joker-7b", "gotham.riddle")
		set.Locality = loc
		if err := h.r.ValidateEndpoint(settingsMap(t, set), nil); err != nil {
			t.Errorf("locality %q refused: %v", loc, err)
		}
	}
	for _, loc := range []ai.Locality{ai.LocalityUnknown, "Local", "lan"} {
		set := textEndpoint("joker-7b", "gotham.riddle")
		set.Locality = loc
		if err := h.r.ValidateEndpoint(settingsMap(t, set), nil); !errors.Is(err, ai.ErrInvalidEndpoint) {
			t.Errorf("locality %q = %v, want ErrInvalidEndpoint", loc, err)
		}
	}
	good := textEndpoint("joker-7b", "gotham.riddle")
	good.Features = []ai.Feature{ai.FeatureJSONSchema, ai.FeatureBatch, ai.FeatureSharedFS + "library-1"}
	if err := h.r.ValidateEndpoint(settingsMap(t, good), nil); err != nil {
		t.Errorf("declared features refused: %v", err)
	}
	for _, f := range []ai.Feature{"telepathy", ai.FeatureSharedFS} {
		bad := textEndpoint("joker-7b", "gotham.riddle")
		bad.Features = []ai.Feature{f}
		if err := h.r.ValidateEndpoint(settingsMap(t, bad), nil); !errors.Is(err, ai.ErrInvalidEndpoint) {
			t.Errorf("feature %q = %v, want ErrInvalidEndpoint", f, err)
		}
	}
}

// Priority orders the candidates; a call takes the most preferred endpoint that
// has a free slot right now, so a saturated one spills to an idle peer, and the
// preferred one is used again once it frees.
func TestRouter_PrioritySpillToFreeSlot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	release := make(chan struct{})
	first, second := fake(), fake()
	first.ScriptText(coretest.FakeAIStep{Block: release, Text: "first"})
	second.ScriptText(coretest.FakeAIStep{Text: "second"})
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), first)
	h.addEndpoint("blackgate", 2, textEndpoint("joker-7b", "gotham.riddle"), second)

	cands, _, err := h.r.Candidates(c)
	if err != nil || len(cands) != 2 || cands[0].Ref != aiRef("arkham") || cands[1].Ref != aiRef("blackgate") {
		t.Fatalf("Candidates = %+v, %v; want arkham then blackgate", cands, err)
	}
	if got := h.r.Capacity(c); got != 2 {
		t.Errorf("Capacity = %d, want 2", got)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var blocked ai.TextResponse
	go func() {
		defer wg.Done()
		blocked, _ = h.r.Generate(context.Background(), c, text("one"))
	}()
	waitFor(t, func() bool { return len(first.Calls()) == 1 })

	resp, err := h.r.Generate(context.Background(), c, text("two"))
	if err != nil || resp.Text != "second" {
		t.Fatalf("the second call = %q, %v; want it served by the idle blackgate", resp.Text, err)
	}
	if len(first.Calls()) != 1 {
		t.Errorf("arkham received %d calls while saturated, want 1", len(first.Calls()))
	}
	close(release)
	wg.Wait()
	if blocked.Text != "first" {
		t.Errorf("the blocked call = %q, want arkham's answer", blocked.Text)
	}

	first.ScriptText(coretest.FakeAIStep{Text: "first again"})
	resp, err = h.r.Generate(context.Background(), c, text("three"))
	if err != nil || resp.Text != "first again" {
		t.Fatalf("after the release = %q, %v; want the preferred arkham again", resp.Text, err)
	}
}

// waitFor polls cond until it holds; a slot or call that never shows up fails
// the test instead of hanging it.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the condition")
		}
		time.Sleep(time.Millisecond)
	}
}

// I3: one row per class. The classes the endpoint is the problem for fail over
// to the next endpoint; the ones the answer is the problem for are returned as
// they are and the next endpoint is never asked.
func TestRouter_FailoverTable(t *testing.T) {
	t.Parallel()
	rows := []struct {
		class    integrations.Class
		failover bool
		benched  bool
	}{
		{integrations.ClassTransient, true, true},
		{integrations.ClassRateLimited, true, true},
		{integrations.ClassAuth, true, true},
		{integrations.ClassQuota, true, true},
		{integrations.ClassUnsupported, true, true},
		{integrations.ClassRefusedLocal, true, false}, // nothing was sent, so nothing was learned
		{integrations.ClassBadOutput, false, false},
		{integrations.ClassInvalid, false, false},
		{integrations.ClassNotFound, false, false},
		{integrations.ClassUnknown, false, false},
	}
	for _, row := range rows {
		t.Run(row.class.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c := h.capability("riddle")
			bad, good := fake(), fake()
			bad.ScriptText(coretest.FakeAIStep{Err: classErr(row.class)})
			good.ScriptText(coretest.FakeAIStep{Text: "the Penguin"})
			ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), bad)
			h.addEndpoint("blackgate", 2, textEndpoint("joker-7b", "gotham.riddle"), good)

			resp, err := h.r.Generate(context.Background(), c, text("who?"))
			if row.failover {
				if err != nil || resp.Text != "the Penguin" {
					t.Fatalf("%s = %q, %v; want the second endpoint's answer", row.class, resp.Text, err)
				}
			} else {
				if err == nil {
					t.Fatalf("%s failed over and answered %q; it must be returned as is", row.class, resp.Text)
				}
				if len(good.Calls()) != 0 {
					t.Fatalf("%s reached the second endpoint %d time(s)", row.class, len(good.Calls()))
				}
			}
			if got := integrations.ClassOf(context.Background(), err); !row.failover && got != row.class {
				t.Errorf("returned class = %s, want %s", got, row.class)
			}
			if _, benched := h.r.BenchedUntil(ref); benched != row.benched {
				t.Errorf("%s: benched = %v, want %v", row.class, benched, row.benched)
			}
		})
	}
}

// When every candidate fails over the call ends in an ExhaustedError naming
// each attempt in order.
func TestRouter_FailoverExhausted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	a, b := fake(), fake()
	a.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	b.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassRateLimited)})
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), a)
	h.addEndpoint("blackgate", 2, textEndpoint("joker-7b", "gotham.riddle"), b)

	_, err := h.r.Generate(context.Background(), c, text("who?"))
	var ex *ai.ExhaustedError
	if !errors.As(err, &ex) || len(ex.Attempts) != 2 {
		t.Fatalf("err = %v, want an ExhaustedError with 2 attempts", err)
	}
	if ex.Attempts[0].Endpoint != aiRef("arkham") || ex.Attempts[0].Class != integrations.ClassTransient ||
		ex.Attempts[1].Endpoint != aiRef("blackgate") || ex.Attempts[1].Class != integrations.ClassRateLimited {
		t.Errorf("attempts = %+v", ex.Attempts)
	}
	if !errors.Is(err, ex.Attempts[0].Err) || !errors.Is(err, ex.Attempts[1].Err) {
		t.Error("the ExhaustedError does not unwrap to its attempts")
	}
}

// A caller whose own context is done ends the call at once: the next endpoint is
// not tried and no endpoint is benched for the caller's budget.
func TestRouter_CallerCancelStops(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	a, b := fake(), fake()
	a.ScriptText(coretest.FakeAIStep{Block: make(chan struct{})})
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), a)
	h.addEndpoint("blackgate", 2, textEndpoint("joker-7b", "gotham.riddle"), b)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := h.r.Generate(ctx, c, text("who?")); done <- err }()
	waitFor(t, func() bool { return len(a.Calls()) == 1 })
	cancel()
	err := <-done
	if err == nil || integrations.ClassOf(context.Background(), err) != integrations.ClassCanceled && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancellation", err)
	}
	if len(b.Calls()) != 0 {
		t.Error("a canceled call failed over to the next endpoint")
	}
	if _, benched := h.r.BenchedUntil(ref); benched {
		t.Error("the caller's own cancel benched the endpoint")
	}
}

// I3: an embedding call must name its model, and failover stays on that model.
func TestRouter_EmbedRequiresPinnedModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("embed_notes", func(s *ai.CapabilitySpec) { s.Kind = ai.KindEmbed })
	f := fake()
	set := ai.EndpointSettings{Concurrency: 1, Capabilities: []string{"gotham.embed_notes"}, EmbedModel: "bat-embed-1"}
	h.addEndpoint("arkham", 1, set, f)

	_, err := h.r.Embed(context.Background(), c, ai.EmbedRequest{Inputs: []string{"a"}})
	if !errors.Is(err, ai.ErrEmbedModelNotPinned) {
		t.Fatalf("an unpinned embed = %v, want ErrEmbedModelNotPinned", err)
	}
	if len(f.Calls()) != 0 {
		t.Error("an unpinned embed reached the endpoint")
	}
	f.ScriptEmbed(coretest.FakeAIStep{Vectors: [][]float32{{1, 2}}})
	resp, err := h.r.Embed(context.Background(), c, ai.EmbedRequest{Inputs: []string{"a"}}, ai.WithPinnedModel("bat-embed-1"))
	if err != nil || len(resp.Vectors) != 1 {
		t.Fatalf("a pinned embed = %+v, %v", resp, err)
	}
	if got := f.Calls()[0].Model; got != "bat-embed-1" {
		t.Errorf("the endpoint was asked for model %q", got)
	}
}

// I3: vectors of two models are not comparable, so an embedding call fails over
// only to an endpoint of the model it pinned, and never to one of another.
func TestRouter_EmbedFailoverSameModelOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("embed_notes", func(s *ai.CapabilitySpec) { s.Kind = ai.KindEmbed })
	one, same, other := fake(), fake(), fake()
	one.ScriptEmbed(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	same.ScriptEmbed(coretest.FakeAIStep{Vectors: [][]float32{{9}}})
	embed := func(model string) ai.EndpointSettings {
		return ai.EndpointSettings{Concurrency: 1, Capabilities: []string{"gotham.embed_notes"}, EmbedModel: model}
	}
	h.addEndpoint("arkham", 1, embed("bat-embed-1"), one)
	h.addEndpoint("blackgate", 2, embed("bat-embed-2"), other)
	h.addEndpoint("wayne-tower", 3, embed("bat-embed-1"), same)

	resp, err := h.r.Embed(context.Background(), c, ai.EmbedRequest{Inputs: []string{"a"}}, ai.WithPinnedModel("bat-embed-1"))
	if err != nil || len(resp.Vectors) != 1 || resp.Vectors[0][0] != 9 {
		t.Fatalf("embed = %+v, %v; want the same-model endpoint's vector", resp, err)
	}
	if len(other.Calls()) != 0 {
		t.Errorf("the embedding failed over to an endpoint of another model (%d call(s))", len(other.Calls()))
	}
	cands, refusals, err := h.r.Candidates(c, ai.WithPinnedModel("bat-embed-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range cands {
		if cand.Ref == aiRef("blackgate") {
			t.Error("the other-model endpoint is a candidate of a pinned call")
		}
	}
	found := false
	for _, r := range refusals {
		found = found || (r.Endpoint == aiRef("blackgate") && strings.Contains(r.Reason, "pinned"))
	}
	if !found {
		t.Errorf("no pinned-model refusal for the other-model endpoint: %+v", refusals)
	}
}

// A per-attempt deadline fails over once; the next one ends the call, so a
// model that is slow everywhere is not asked by every endpoint in turn.
func TestRouter_DeadlineFailsOverOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	var fs [3]*coretest.FakeAI
	for i, name := range []string{"arkham", "blackgate", "wayne-tower"} {
		fs[i] = fake()
		fs[i].ScriptText(coretest.FakeAIStep{Latency: time.Minute})
		h.addEndpoint(name, i+1, textEndpoint("joker-7b", "gotham.riddle"), fs[i])
	}
	start := time.Now()
	_, err := h.r.Generate(context.Background(), c, text("who?"), ai.WithAttemptTimeout(30*time.Millisecond))
	var ex *ai.ExhaustedError
	if !errors.As(err, &ex) {
		t.Fatalf("err = %v, want an ExhaustedError", err)
	}
	if len(ex.Attempts) != 2 {
		t.Fatalf("%d attempts, want 2: one deadline fails over, the second ends the call (%+v)", len(ex.Attempts), ex.Attempts)
	}
	if len(fs[2].Calls()) != 0 {
		t.Error("the third endpoint was asked after two deadlines")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the call took %v; the attempt timeout did not bound it", time.Since(start))
	}
	for _, a := range ex.Attempts {
		if a.Class != integrations.ClassTransient {
			t.Errorf("a deadline attempt is class %s, want transient", a.Class)
		}
	}

	// core.ai.deadline_failovers = 2 lets one more through.
	h2 := newHarness(t)
	h2.setConfig(ai.Config{DeadlineFailovers: 2})
	c2 := h2.capability("riddle")
	var gs [3]*coretest.FakeAI
	for i, name := range []string{"arkham", "blackgate", "wayne-tower"} {
		gs[i] = fake()
		gs[i].ScriptText(coretest.FakeAIStep{Latency: time.Minute})
		h2.addEndpoint(name, i+1, textEndpoint("joker-7b", "gotham.riddle"), gs[i])
	}
	_, err = h2.r.Generate(context.Background(), c2, text("who?"), ai.WithAttemptTimeout(30*time.Millisecond))
	if !errors.As(err, &ex) || len(ex.Attempts) != 3 {
		t.Fatalf("with deadline_failovers=2: err = %v, want 3 attempts", err)
	}
}

// A failure benches the endpoint for cooldown_base times its consecutive
// failures, capped at cooldown_max; a success resets it.
func TestRouter_CooldownStepsAndCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.setConfig(ai.Config{CooldownBase: 30 * time.Second, CooldownMax: 100 * time.Second})
	c := h.capability("riddle")
	f := fake()
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)

	for i, wantStep := range []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 100 * time.Second, 100 * time.Second} {
		before := h.clk.Now()
		_, err := h.r.Generate(context.Background(), c, text("who?"))
		if !errors.Is(err, ai.ErrNoCapableEndpoint) && i > 0 {
			// Benched endpoints are left out, so the call after the first fails
			// closed; the previous iteration advanced the clock past the bench.
		}
		until, ok := h.r.BenchedUntil(ref)
		if !ok {
			t.Fatalf("failure %d: not benched", i+1)
		}
		if got := until.Sub(before); got != wantStep {
			t.Fatalf("failure %d: benched for %v, want %v", i+1, got, wantStep)
		}
		// Inside the window the endpoint is a refusal, not a candidate.
		_, refusals, _ := h.r.Candidates(c)
		if len(refusals) != 1 || !strings.Contains(refusals[0].Reason, "cooldown") {
			t.Fatalf("failure %d: refusals = %+v, want the cooldown", i+1, refusals)
		}
		_, err = h.r.Generate(context.Background(), c, text("who?"))
		wantNoCapable(t, err)
		h.clk.Advance(wantStep) // out of the window; the next failure is one more consecutive
	}

	// A success resets the count: the next failure starts at the base again.
	f.ScriptText(coretest.FakeAIStep{Text: "fine"})
	if _, err := h.r.Generate(context.Background(), c, text("who?")); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("a success left the endpoint benched")
	}
	f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	before := h.clk.Now()
	_, _ = h.r.Generate(context.Background(), c, text("who?"))
	until, _ := h.r.BenchedUntil(ref)
	if got := until.Sub(before); got != 30*time.Second {
		t.Errorf("after a success the bench is %v, want the base 30s", got)
	}

	// ClearBench ends the bench now.
	h.r.ClearBench(ref)
	if _, ok := h.r.BenchedUntil(ref); ok {
		t.Error("ClearBench left the endpoint benched")
	}
}

// A 429 is a Quota, benched for core.ai.quota_cooldown, when the body's type OR
// its code carries a marker; the captured production payload sets the two to
// different values, and reading one field only is what broke HEAD.
func TestRouter_QuotaMarkerTypeOrCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		status     int
		typ, code  string
		wantMarker bool // IsQuotaMarker reads the body only; the status is StatusClass's
		wantClass  integrations.Class
	}{
		{"type only", 429, "insufficient_quota", "rate_limit_exceeded", true, integrations.ClassQuota},
		{"code only", 429, "invalid_request_error", "insufficient_quota", true, integrations.ClassQuota},
		{"the other marker in the code", 429, "error", "credit_balance_exhausted", true, integrations.ClassQuota},
		{"the other marker in the type", 429, "credit_balance_exhausted", "", true, integrations.ClassQuota},
		{"both, different markers", 429, "insufficient_quota", "credit_balance_exhausted", true, integrations.ClassQuota},
		{"a plain rate limit", 429, "rate_limit_error", "rate_limit_exceeded", false, integrations.ClassRateLimited},
		{"a marker on another status is not a quota", 400, "insufficient_quota", "insufficient_quota", true, integrations.ClassInvalid},
		{"a 5xx", 503, "", "", false, integrations.ClassTransient},
		{"auth", 401, "", "", false, integrations.ClassAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ai.StatusClass(tc.status, tc.typ, tc.code); got != tc.wantClass {
				t.Errorf("StatusClass(%d, %q, %q) = %s, want %s", tc.status, tc.typ, tc.code, got, tc.wantClass)
			}
			if got := ai.IsQuotaMarker(tc.typ, tc.code); got != tc.wantMarker {
				t.Errorf("IsQuotaMarker(%q, %q) = %v, want %v", tc.typ, tc.code, got, tc.wantMarker)
			}
		})
	}

	// Through the router: a 429 error whose Detail carries the marker in the
	// code only benches for the quota window, not the ordinary cooldown.
	h := newHarness(t)
	h.setConfig(ai.Config{CooldownBase: 30 * time.Second, QuotaCooldown: 45 * time.Minute})
	c := h.capability("riddle")
	f := fake()
	f.ScriptText(coretest.FakeAIStep{Err: &integrations.Error{
		Ref: aiRef("arkham"), Op: "generate", Class: integrations.ClassRateLimited, Status: 429,
		Detail: `{"error":{"type":"invalid_request_error","code":"insufficient_quota","message":"You exceeded your current quota"}}`,
		Err:    errors.New("the server answered 429"),
	}})
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)
	before := h.clk.Now()
	_, err := h.r.Generate(context.Background(), c, text("who?"))
	var ex *ai.ExhaustedError
	if !errors.As(err, &ex) || ex.Attempts[0].Class != integrations.ClassQuota {
		t.Fatalf("err = %v, want one Quota attempt", err)
	}
	until, _ := h.r.BenchedUntil(ref)
	if got := until.Sub(before); got != 45*time.Minute {
		t.Errorf("benched for %v, want the quota cooldown 45m", got)
	}

	// The marker in the type of the flat body shape, and a plain 429, for contrast.
	for detail, want := range map[string]bool{
		`{"type":"insufficient_quota","code":"x"}`:                      true,
		`{"type":"x","code":"credit_balance_exhausted"}`:                true,
		`{"error":{"type":"x","code":"insufficient_quota"}}`:            true,
		`{"error":{"type":"insufficient_quota"}}`:                       true,
		`{"error":{"type":"rate_limit_error","code":"rate_limit"}}`:     false,
		`HTTP 429: insufficient_quota (body cut off at 200 bytes: {"er`: true,
		``: false,
	} {
		if got := ai.QuotaMarkerIn(detail); got != want {
			t.Errorf("QuotaMarkerIn(%q) = %v, want %v", detail, got, want)
		}
	}
}

// The slot is released by defer, so a Call whose fn panics does not leave a
// concurrency-1 endpoint refusing every later call.
func TestRouter_SlotReleasedOnPanic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	f := fake()
	f.ScriptText(coretest.FakeAIStep{Text: "fine"})
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate to the caller")
			}
		}()
		_, _ = ai.Call(context.Background(), h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
			panic("the Joker pulled the fire alarm")
		})
	}()

	cands, _, _ := h.r.Candidates(c)
	if len(cands) != 1 || cands[0].InFlight != 0 {
		t.Fatalf("after the panic: candidates = %+v, want one with nothing in flight", cands)
	}
	done := make(chan error, 1)
	go func() { _, err := h.r.Generate(context.Background(), c, text("who?")); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the call after the panic failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call after the panic never got a slot: the panic leaked it")
	}
}

// A kind cap of 1 is given back by every way a call can end once its slot is
// taken. The endpoints have room for four each, so only the kind's own count can
// refuse the probe call that follows each exit: a slot kept by a panic, by a
// failure, by a failover or by a canceled caller would starve every later call
// of the kind, and that must fail here in seconds (the probe's deadline) rather
// than hang a suite.
func TestRouter_KindSlotReleasedOnEveryExit(t *testing.T) {
	t.Parallel()
	ok := func(context.Context, ai.Target) (string, ai.Usage, error) { return "fine", ai.Usage{}, nil }
	exits := []struct {
		name string
		// run makes one call end in the way under test.
		run func(t *testing.T, ctx context.Context, h *harness, c ai.Capability)
	}{
		{"a panic", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			defer func() {
				if recover() == nil {
					t.Error("the panic did not propagate to the caller")
				}
			}()
			_, _ = ai.Call(ctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
				panic("the Joker pulled the fire alarm")
			})
		}},
		{"a failure returned as is", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			ran := false
			_, err := ai.Call(ctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
				ran = true
				return "", ai.Usage{}, errors.New("the Riddler answered with a riddle")
			})
			if !ran || err == nil {
				t.Fatalf("ran = %v, err = %v; want a failing call that ran", ran, err)
			}
		}},
		{"a failover to the next endpoint", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			// The second attempt needs the kind's one slot, which only the first
			// attempt's release can have freed.
			var tried []integrations.Ref
			got, err := ai.Call(ctx, h.r, c, func(_ context.Context, tg ai.Target) (string, ai.Usage, error) {
				tried = append(tried, tg.Endpoint)
				if len(tried) == 1 {
					return "", ai.Usage{}, classErr(integrations.ClassTransient)
				}
				return "the Penguin", ai.Usage{}, nil
			})
			if err != nil || got != "the Penguin" || len(tried) != 2 {
				t.Fatalf("got %q, %v after trying %v; want the second endpoint's answer", got, err, tried)
			}
		}},
		{"every endpoint failing over", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			_, err := ai.Call(ctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
				return "", ai.Usage{}, classErr(integrations.ClassTransient)
			})
			if _, ok := errors.AsType[*ai.ExhaustedError](err); !ok {
				t.Fatalf("err = %v, want ExhaustedError", err)
			}
		}},
		{"a lease that fails after a failover", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			// The first attempt fails over and, while it runs, the next endpoint
			// (already selected) is deleted: the second attempt takes its slot
			// and then cannot lease the instance.
			attempts := 0
			_, err := ai.Call(ctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
				attempts++
				if derr := h.reg.Delete(admin(), aiRef("blackgate")); derr != nil {
					t.Errorf("Delete blackgate: %v", derr)
				}
				return "", ai.Usage{}, classErr(integrations.ClassTransient)
			})
			if attempts != 1 || err == nil {
				t.Fatalf("%d attempt(s), err = %v; want one attempt, then the second endpoint's lease failing", attempts, err)
			}
		}},
		{"a caller that gives up mid-call", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			in := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := ai.Call(cctx, h.r, c, func(fctx context.Context, _ ai.Target) (string, ai.Usage, error) {
					close(in)
					<-fctx.Done()
					return "", ai.Usage{}, fctx.Err()
				})
				done <- err
			}()
			<-in
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the caller's cancellation", err)
			}
		}},
		{"a caller that gives up while waiting for the slot", func(t *testing.T, ctx context.Context, h *harness, c ai.Capability) {
			hold, in, held := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(held)
				_, _ = ai.Call(ctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
					close(in)
					<-hold
					return "fine", ai.Usage{}, nil
				})
			}()
			<-in // the kind's one slot is now taken
			wctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			ran := false
			_, err := ai.Call(wctx, h.r, c, func(context.Context, ai.Target) (string, ai.Usage, error) {
				ran = true
				return "fine", ai.Usage{}, nil
			})
			if ran || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("ran = %v, err = %v; want the waiter to give up without running", ran, err)
			}
			close(hold)
			<-held
		}},
	}
	for _, exit := range exits {
		t.Run(exit.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.setConfig(ai.Config{KindLimits: map[ai.Kind]int{ai.KindText: 1}})
			c := h.capability("riddle")
			for i, name := range []string{"arkham", "blackgate"} {
				set := textEndpoint("joker-7b", "gotham.riddle")
				set.Concurrency = 4
				h.addEndpoint(name, i+1, set, fake())
			}
			// Every call carries the deadline: a leaked kind slot makes the NEXT
			// call wait for it, so it is that call that has to give up.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			exit.run(t, ctx, h, c)
			h.clk.Advance(time.Hour) // out of any bench the exit caused

			if _, err := ai.Call(ctx, h.r, c, ok); err != nil {
				t.Fatalf("the call after %s never got the kind's one slot: %v", exit.name, err)
			}
		})
	}
}

// core.ai.kind_limits.<kind> is the pool-wide in-flight cap of a kind, and it
// is read from the live config on every call (HEAD had no production setter for
// whisper_max_in_flight).
//
// No assertion here depends on a fixed sleep. A call the cap allows is awaited
// by its own entry (waitFor, bounded), so a slow machine only makes the test
// longer. A call the cap must refuse is proved refused by its outcome: the first
// call is held in flight while the others run under a deadline, and a cap that
// works leaves each of them waiting for the slot until that deadline, so none
// ever runs, while a cap that does not work lets them into the gate beside the
// first call, where the peak shows it. A loaded machine can only make a refused
// call give up before it reaches the slot, never make it run, so slowness can
// hide a broken cap from this test but cannot fail a working one; the slot
// accounting itself is pinned without any goroutine in slots_test.go.
func TestRouter_KindCapProductionSetter(t *testing.T) {
	t.Parallel()
	const callers = 4
	run := func(t *testing.T, limits map[ai.Kind]int, capped bool) (peak int) {
		h := newHarness(t)
		h.setConfig(ai.Config{KindLimits: limits})
		c := h.capability("riddle")
		release := make(chan struct{})
		var mu sync.Mutex
		inFlight, entered := 0, 0
		gate := func(ctx context.Context, tg ai.Target) (string, ai.Usage, error) {
			mu.Lock()
			inFlight++
			entered++
			peak = max(peak, inFlight)
			mu.Unlock()
			defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
			select {
			case <-release:
			case <-ctx.Done():
			}
			return "ok", ai.Usage{}, nil
		}
		for i, name := range []string{"arkham", "blackgate"} {
			set := textEndpoint("joker-7b", "gotham.riddle")
			set.Concurrency = 4
			h.addEndpoint(name, i+1, set, fake())
		}
		if got := h.r.Capacity(c); got != min(8, firstPositive(limits[ai.KindText], 8)) {
			t.Errorf("Capacity = %d, with limits %v", got, limits)
		}
		current := func() int {
			mu.Lock()
			defer mu.Unlock()
			return inFlight
		}
		var first, others sync.WaitGroup
		first.Go(func() { _, _ = ai.Call(context.Background(), h.r, c, gate) })
		waitFor(t, func() bool { return current() >= 1 })

		wctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if capped {
			wctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
		}
		for range callers - 1 {
			others.Go(func() { _, _ = ai.Call(wctx, h.r, c, gate) })
		}
		if capped {
			others.Wait() // each ended on its deadline, still waiting for the slot
			mu.Lock()
			if entered != 1 {
				t.Errorf("%d calls ran with kind_limits.text=1 while the first was in flight, want only the first", entered)
			}
			mu.Unlock()
		} else {
			waitFor(t, func() bool { return current() == callers })
		}
		mu.Lock()
		observed := peak
		mu.Unlock()
		close(release)
		first.Wait()
		others.Wait()
		return observed
	}
	if got := run(t, nil, false); got != callers {
		t.Errorf("with no kind limit the peak in flight was %d, want all %d: the endpoints' own capacity lets them in", got, callers)
	}
	if got := run(t, map[ai.Kind]int{ai.KindText: 1}, true); got != 1 {
		t.Errorf("with kind_limits.text=1 the peak in flight was %d, want 1 across both endpoints", got)
	}
	if got := run(t, map[ai.Kind]int{ai.KindEmbed: 1}, false); got != callers {
		t.Errorf("a limit on embed capped text work: peak %d, want %d", got, callers)
	}
}

func firstPositive(n, otherwise int) int {
	if n >= 1 {
		return n
	}
	return otherwise
}

// A local-only floor (the capability's, or the call's) refuses every endpoint
// not provably local; the cloud-only narrowing never widens it.
func TestRouter_LocalOnlyFloor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	floor := h.capability("library_summary", func(s *ai.CapabilitySpec) { s.Locality = ai.LocalOnly })
	open := h.capability("riddle")
	cloud, local, unproven := fake(), fake(), fake()
	for _, f := range []*coretest.FakeAI{cloud, local, unproven} {
		f.ScriptText(coretest.FakeAIStep{Text: "ok"})
	}
	ticks := []string{"gotham.library_summary", "gotham.riddle"}
	withLocality := func(l ai.Locality) ai.EndpointSettings {
		s := textEndpoint("joker-7b", ticks...)
		s.Locality = l
		return s
	}
	h.addEndpoint("blackgate", 1, withLocality(ai.LocalityCloud), cloud)
	h.addEndpoint("wayne-tower", 2, withLocality(ai.LocalityLocal), local)
	h.addEndpoint("arkham", 3, withLocality(ai.LocalityAuto), unproven.SetCaps(ai.BackendCaps{Locality: ai.LocalityUnknown}))

	cands, refusals, err := h.r.Candidates(floor)
	if err != nil || len(cands) != 1 || cands[0].Ref != aiRef("wayne-tower") || cands[0].Locality != ai.LocalityLocal {
		t.Fatalf("floor candidates = %+v, %v; want only wayne-tower", cands, err)
	}
	for _, name := range []string{"blackgate", "arkham"} {
		found := false
		for _, r := range refusals {
			found = found || (r.Endpoint == aiRef(name) && strings.Contains(r.Reason, "local-only"))
		}
		if !found {
			t.Errorf("%s: no local-only refusal in %+v (an endpoint that cannot be shown local is cloud)", name, refusals)
		}
	}
	if _, err := h.r.Generate(context.Background(), floor, text("summarise")); err != nil {
		t.Fatal(err)
	}
	if len(cloud.Calls()) != 0 || len(unproven.Calls()) != 0 {
		t.Error("a local-only call reached a cloud or unproven endpoint")
	}

	// The call narrows an open capability; it can never widen a floor.
	cands, _, _ = h.r.Candidates(open, ai.WithLocalOnly())
	if len(cands) != 1 || cands[0].Ref != aiRef("wayne-tower") {
		t.Errorf("WithLocalOnly candidates = %+v", cands)
	}
	cands, _, _ = h.r.Candidates(open, ai.WithCloudOnly())
	if len(cands) != 2 {
		t.Errorf("WithCloudOnly candidates = %+v, want the cloud and the unproven endpoint", cands)
	}
	cands, _, _ = h.r.Candidates(floor, ai.WithCloudOnly())
	if len(cands) != 0 {
		t.Errorf("WithCloudOnly on a local-only capability = %+v, want none: the call cannot widen the floor", cands)
	}
	_, err = h.r.Generate(context.Background(), floor, text("summarise"), ai.WithCloudOnly())
	wantNoCapable(t, err)

	// A measured locality the adapter can prove counts when the operator says
	// nothing.
	h.edit(aiRef("arkham"), withLocality(ai.LocalityAuto))
	proven := fake().SetCaps(ai.BackendCaps{Locality: ai.LocalityLocal})
	h.addEndpoint("gordon-hq", 4, withLocality(ai.LocalityAuto), proven)
	cands, _, _ = h.r.Candidates(floor)
	if len(cands) != 2 {
		t.Errorf("an adapter-proven local endpoint is not a floor candidate: %+v", cands)
	}
}

// I1: two Routers share nothing. The same instance name, the same capability
// ID, a bench and a handle in one are invisible to the other.
func TestRouter_TwoAppsIsolated(t *testing.T) {
	t.Parallel()
	a, b := newHarness(t), newHarness(t)
	ca := a.capability("riddle")
	cb := b.capability("riddle")
	fa, fb := fake(), fake()
	fa.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	fb.ScriptText(coretest.FakeAIStep{Text: "from b"})
	refA := a.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), fa)
	b.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), fb)

	if _, err := a.r.Generate(context.Background(), ca, text("who?")); err == nil {
		t.Fatal("app A's endpoint should have failed")
	}
	if _, benched := a.r.BenchedUntil(refA); !benched {
		t.Fatal("app A's endpoint was not benched")
	}
	if _, benched := b.r.BenchedUntil(refA); benched {
		t.Error("app A's bench leaked into app B")
	}
	resp, err := b.r.Generate(context.Background(), cb, text("who?"))
	if err != nil || resp.Text != "from b" {
		t.Fatalf("app B = %q, %v; app A's failure must not reach it", resp.Text, err)
	}

	// A handle is bound to the Router that minted it.
	if _, err := b.r.Generate(context.Background(), ca, text("who?")); !errors.Is(err, ai.ErrUnknownCapability) {
		t.Errorf("app A's handle on app B's router = %v, want ErrUnknownCapability", err)
	}
	// A capability registered in one catalog is unknown to the other.
	a.capability("only_a")
	if b.r.HasCapability("gotham.only_a") {
		t.Error("app A's catalog entry is visible in app B")
	}
	// A's only endpoint is benched, so its capacity is 0; B's identically named
	// endpoint still serves.
	if cfg := fmt.Sprint(a.r.Capacity(ca), b.r.Capacity(cb)); cfg != "0 1" {
		t.Errorf("capacities = %s, want %q", cfg, "0 1")
	}
}

// I5: core.integrations.disabled refuses every endpoint before any call, stores
// no job, and the poll changes nothing and calls nothing but still succeeds, so
// its clock advances.
func TestRouter_IntegrationsDisabledRefusesEveryEndpoint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle", func(s *ai.CapabilitySpec) { s.RequiredFeatures = []ai.Feature{ai.FeatureBatch} })
	registerApply(t, c, noopHandler)
	f := fake()
	set := textEndpoint("joker-7b", "gotham.riddle")
	set.Features = []ai.Feature{ai.FeatureBatch}
	h.addEndpoint("arkham", 1, set, f)

	// An open job from before the flag, and a remote batch it must not touch.
	id, err := h.r.SubmitBatch(context.Background(), c, []ai.BatchItem{{CustomID: "r1", Request: text("who?")}}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	remote := f.RemoteBatches()
	if len(remote) != 1 {
		t.Fatalf("%d remote batches, want 1", len(remote))
	}
	f.CompleteBatch(remote[0])
	baseline := f.BatchCallCount()

	h.disabled.Store(true)
	_, err = h.r.Generate(context.Background(), c, text("who?"))
	nc := wantNoCapable(t, err)
	if got := refusalFor(nc, "arkham"); !strings.Contains(got, "integrations disabled") {
		t.Errorf("refusal = %q, want the disabled reason", got)
	}
	if got := h.r.Capacity(c); got != 0 {
		t.Errorf("Capacity = %d while disabled", got)
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("%d call(s) reached an endpoint while disabled", n)
	}
	if _, err := h.r.SubmitBatch(context.Background(), c, []ai.BatchItem{{CustomID: "r2", Request: text("who?")}}, []byte(`{}`)); !errors.Is(err, ai.ErrNoCapableEndpoint) {
		t.Errorf("SubmitBatch while disabled = %v, want ErrNoCapableEndpoint", err)
	}
	open := 0
	for _, err := range h.jobs.Open(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		open++
	}
	if open != 1 {
		t.Errorf("%d open jobs, want only the one from before the flag: SubmitBatch must store nothing", open)
	}

	res, err := h.r.PollBatches(newRC(t), ai.BatchPollParams{})
	if err != nil || res != (ai.BatchPollResult{}) {
		t.Fatalf("PollBatches while disabled = %+v, %v; want a successful no-op so its clock advances", res, err)
	}
	if n := f.BatchCallCount() - baseline; n != 0 {
		t.Errorf("the poll made %d provider batch call(s) while disabled", n)
	}
	if len(h.stager.Started()) != 0 {
		t.Error("the poll staged an apply run while disabled")
	}
	j, err := h.r.BatchJob(context.Background(), id)
	if err != nil || j.State != ai.JobSubmitted {
		t.Errorf("the job = %+v, %v; the poll must not move it while disabled", j, err)
	}

	// Lifting the flag restores service and the poll.
	h.disabled.Store(false)
	f.ScriptText(coretest.FakeAIStep{Text: "ok"})
	if _, err := h.r.Generate(context.Background(), c, text("who?")); err != nil {
		t.Errorf("after the flag is lifted: %v", err)
	}
}
