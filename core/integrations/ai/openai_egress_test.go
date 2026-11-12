// file: core/integrations/ai/openai_egress_test.go
// version: 1.0.0
// guid: 0e6b3d94-7a15-4c82-9f3b-5d1c8a2e4f67
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/ai/openai"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// openaiHarness is the Registry with the real OpenAI-compatible adapter, whose
// instances reach a loopback upstream through a real providerhttp gate that
// has no lan_allow: a private address is reachable only through the
// instance's own Local grant.
func openaiHarness(t *testing.T) (*harness, *coretest.ProviderHTTPKit) {
	t.Helper()
	kit := coretest.ProviderHTTP(t, func(o *providerhttp.Options) { o.LANAllow = nil })
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-batmobile","object":"model","created":1,"owned_by":"wayne"}]}`))
	})
	d, err := openai.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	return newHarness(t, withOutbound(kit.Registry.Outbound), withAdapter("core", d)), kit
}

// compatConfig is an instance of the real adapter pointed at url.
func compatConfig(t *testing.T, ref integrations.Ref, url string, local bool, version uint64, edit ...func(map[string]any)) integrations.InstanceConfig {
	t.Helper()
	raw, err := json.Marshal(openai.Settings{
		Concurrency: 1, TextModel: "gpt-batmobile",
		URL: url,
	})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	for _, e := range edit {
		e(settings)
	}
	return integrations.InstanceConfig{Ref: ref, Adapter: openai.AdapterID, Name: string(ref.Instance), Enabled: true, Local: local, Settings: settings, Version: version}
}

func manager() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage)
}

// An instance reaches a private or loopback server only when its Local is set
// for the origin it is configured with, and only an admin on an interactive
// session may set it (M2.4, SEC-15, SEC2-7). A refusal happens before a byte
// is sent.
func TestOpenAICompat_PrivateHostNeedsLocal(t *testing.T) {
	t.Parallel()
	h, kit := openaiHarness(t)
	ref := aiRef("ollama")
	url := kit.URL() + "/v1"
	ctx := context.Background()

	if _, err := h.reg.Put(admin(), compatConfig(t, ref, url, false, 0), nil); err != nil {
		t.Fatalf("Put without Local: %v", err)
	}
	cfg, _ := h.reg.Config(ref)

	res, err := h.reg.Test(admin(), cfg, nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	// The probe reports a settings problem (set Local or fix the url); the
	// call itself, below, is the refusal.
	if res.OK || res.Class != integrations.ClassInvalid {
		t.Errorf("a private server without Local: Test = %+v, want a failed test classed invalid", res)
	}
	if hl, err := h.reg.Health(ctx, ref); err != nil || hl.Status != integrations.HealthMisconfigured ||
		!strings.Contains(hl.Message, "egress guard") || strings.Contains(hl.Message, integrations.ReasonSecretMissing) {
		t.Errorf("Health = %+v, %v; want misconfigured, naming the egress guard and not a missing secret", hl, err)
	}
	b, release, err := integrations.Lease[ai.Backend](ctx, h.reg, ref)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	_, err = b.Caps(ctx)
	release()
	if c := integrations.ClassOf(ctx, err); c != integrations.ClassRefusedLocal {
		t.Errorf("Caps = %v (class %v), want RefusedLocal: the egress guard must stop it", err, c)
	}
	if kit.Hits() != 0 {
		t.Fatalf("%d request(s) reached a private server without Local", kit.Hits())
	}

	// Local is not for just anyone: a manager who is not an interactive admin
	// cannot set it.
	_, err = h.reg.Put(manager(), compatConfig(t, ref, url, true, cfg.Version), nil)
	if !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("Put of Local by a non-admin = %v, want ErrForbidden", err)
	}
	if kit.Hits() != 0 {
		t.Fatalf("%d request(s) after a refused Put", kit.Hits())
	}

	// An admin grants it, and the instance reaches the server.
	if _, err := h.reg.Put(admin(), compatConfig(t, ref, url, true, cfg.Version), nil); err != nil {
		t.Fatalf("Put with Local by an admin: %v", err)
	}
	cfg, _ = h.reg.Config(ref)
	if res, err := h.reg.Test(admin(), cfg, nil); err != nil || !res.OK {
		t.Fatalf("Test with Local = %+v, %v; want OK", res, err)
	}
	b, release, err = integrations.Lease[ai.Backend](ctx, h.reg, ref)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	caps, err := b.Caps(ctx)
	release()
	if err != nil || len(caps.Models) != 1 || caps.Models[0] != "gpt-batmobile" {
		t.Fatalf("Caps = %+v, %v", caps, err)
	}
	if kit.Hits() < 2 {
		t.Errorf("%d request(s) reached the server with Local set, want the Test's and the Caps'", kit.Hits())
	}

	// The grant belongs to the origin it was made for: keeping Local across a
	// change of the url is the same admin act (this proves the adapter's url
	// setting is what the Registry treats as the destination).
	_, err = h.reg.Put(manager(), compatConfig(t, ref, "http://127.0.0.1:1/v1", true, cfg.Version), nil)
	if !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("Put of a new url keeping Local by a non-admin = %v, want ErrForbidden", err)
	}
}

// The settings the real adapter's schema accepts are the ones an operator
// writes: the locality is a string enum (a uint8 locality rendered "integer
// 0..255" and refused "local"), the shared-filesystem roots go by root_paths,
// and the Registry takes the adapter's schema at all (it refused host_roots).
func TestOpenAICompat_SettingsValidation(t *testing.T) {
	t.Parallel()
	h, kit := openaiHarness(t)
	url := kit.URL() + "/v1"
	put := func(name string, edit func(map[string]any)) error {
		_, err := h.reg.Put(admin(), compatConfig(t, aiRef(name), url, true, 0, edit), nil)
		return err
	}
	for i, tc := range []struct {
		name string
		edit func(map[string]any)
		ok   bool
	}{
		{"defaults", func(map[string]any) {}, true},
		{"locality local", func(m map[string]any) { m["locality"] = "local" }, true},
		{"locality cloud", func(m map[string]any) { m["locality"] = "cloud" }, true},
		{"locality auto", func(m map[string]any) { m["locality"] = "auto" }, true},
		{"locality unknown word", func(m map[string]any) { m["locality"] = "nearby" }, false},
		{"locality as a number", func(m map[string]any) { m["locality"] = 1 }, false},
		{"root_paths", func(m map[string]any) { m["root_paths"] = map[string]any{"books": "/mnt/books"} }, true},
		{"concurrency zero", func(m map[string]any) { m["concurrency"] = 0 }, false},
		{"not a url", func(m map[string]any) { m["url"] = "not a url" }, false},
	} {
		err := put("ep"+string(rune('a'+i)), tc.edit)
		if tc.ok && err != nil {
			t.Errorf("%s: Put = %v, want accepted", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: Put accepted a settings value the schema must refuse", tc.name)
		}
	}
}

// upstreamScript is what the recovery rig's chat endpoint answers next.
type upstreamScript struct {
	mu         sync.Mutex
	status     int
	retryAfter string
	body       string
	chats      int // chat requests that reached the server
}

func (u *upstreamScript) set(status int, retryAfter, body string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status, u.retryAfter, u.body = status, retryAfter, body
}

func (u *upstreamScript) chatCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.chats
}

// recoveryRig is the router over the REAL adapter, the REAL Registry and a REAL
// providerhttp gate, with one Local endpoint whose chat answers are scripted:
// the whole stack a 503 passes through, which is what a test with a fake
// backend cannot see (the gate's hold sat under the router's bench there).
type recoveryRig struct {
	t   *testing.T
	h   *harness
	kit *coretest.ProviderHTTPKit
	ref integrations.Ref
	c   ai.Capability
	up  *upstreamScript
}

func newRecoveryRig(t *testing.T) *recoveryRig {
	t.Helper()
	h, kit := openaiHarness(t)
	up := &upstreamScript{status: 200}
	kit.Handle(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path != "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-batmobile","object":"model","created":1,"owned_by":"wayne"}]}`))
			return
		}
		up.mu.Lock()
		up.chats++
		status, retryAfter, body := up.status, up.retryAfter, up.body
		up.mu.Unlock()
		if status == 200 {
			_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","created":1,"model":"gpt-batmobile","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"the Penguin"}}]}`))
			return
		}
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	ref := aiRef("arkham")
	cfg := compatConfig(t, ref, kit.URL()+"/v1", true, 0, func(m map[string]any) { m["capabilities"] = []any{"gotham.riddle"} })
	if _, err := h.reg.Put(admin(), cfg, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return &recoveryRig{t: t, h: h, kit: kit, ref: ref, c: h.capability("riddle"), up: up}
}

func (r *recoveryRig) generate() (ai.TextResponse, error) {
	r.t.Helper()
	return r.h.r.Generate(context.Background(), r.c, text("who?"))
}

// advance moves the router's clock (the bench) and the gate's (the hold) together.
func (r *recoveryRig) advance(d time.Duration) {
	r.h.clk.Advance(d)
	r.kit.Clock.Advance(d)
}

// holdLeft is how long the gate's hold on the endpoint has left.
func (r *recoveryRig) holdLeft() (integrations.Hold, time.Duration, bool) {
	hold, ok := r.kit.Registry.HoldFor(r.ref)
	return hold, hold.Until.Sub(r.kit.Clock.Now()), ok
}

// One 503 benches the endpoint in the router for its first cooldown rung, and
// the call right after that rung reaches the server. The adapter once left the
// gate's own defaults in force, so the same 503 held the endpoint for 30
// minutes: the router's ladder never ran, and a call the router had cleared was
// refused locally and failed over to nothing.
func TestOpenAICompat_ARouterCooldownEndsWithTheHold(t *testing.T) {
	t.Parallel()
	r := newRecoveryRig(t)
	r.up.set(503, "", `{"error":{"message":"overloaded","type":"server_error","code":"overloaded"}}`)
	if _, err := r.generate(); err == nil {
		t.Fatal("a 503 answered")
	}
	if _, left, ok := r.holdLeft(); !ok || left > ai.DefaultCooldownBase {
		t.Fatalf("hold after a 503: in force = %v, %v left; want one of at most %v, the router's first rung", ok, left, ai.DefaultCooldownBase)
	}
	until, benched := r.h.r.BenchedUntil(r.ref)
	if !benched || until.Sub(r.h.clk.Now()) != ai.DefaultCooldownBase {
		t.Fatalf("the router's bench = %v, %v; want the first rung (%v)", until, benched, ai.DefaultCooldownBase)
	}
	if got := r.up.chatCount(); got != 1 {
		t.Fatalf("%d chat request(s), want 1", got)
	}

	// The cooldown ends: the router may try the endpoint again, and so may the gate.
	r.advance(ai.DefaultCooldownBase + time.Second)
	r.up.set(200, "", "")
	resp, err := r.generate()
	if err != nil || resp.Text != "the Penguin" {
		t.Fatalf("the call after the cooldown = %q, %v; want the server's answer", resp.Text, err)
	}
	if got := r.up.chatCount(); got != 2 {
		t.Errorf("%d chat request(s), want 2: the call after the cooldown must reach the server", got)
	}
	if hold, ok := r.kit.Registry.HoldFor(r.ref); ok {
		t.Errorf("a success left the hold in force: %+v", hold)
	}
	if _, benched := r.h.r.BenchedUntil(r.ref); benched {
		t.Error("a success left the endpoint benched")
	}
}

// The gate honours Retry-After for a rate limit and never clamps it itself, so
// the adapter's Holds.Max (the router's cooldown cap) is what keeps a provider's
// "come back in an hour" from locking the endpoint out for an hour: the router
// would retry it after 30 seconds, and the gate would refuse.
func TestOpenAICompat_RetryAfterIsHonouredUpToTheRoutersCap(t *testing.T) {
	t.Parallel()
	rate := `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	for _, tc := range []struct {
		name       string
		retryAfter string
		want       time.Duration
	}{
		{"none", "", ai.DefaultCooldownBase},
		{"shorter than the first rung", "5", ai.DefaultCooldownBase},
		{"longer, within the cap", "120", 2 * time.Minute},
		{"an hour, capped", "3600", ai.DefaultCooldownMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRecoveryRig(t)
			r.up.set(429, tc.retryAfter, rate)
			if _, err := r.generate(); err == nil {
				t.Fatal("a 429 answered")
			}
			hold, left, ok := r.holdLeft()
			if !ok || left != tc.want || hold.Class != integrations.ClassRateLimited {
				t.Errorf("hold = %+v, %v left, in force %v; want a rate_limited hold of %v", hold, left, ok, tc.want)
			}
		})
	}
}

// The gate and the adapter agree on what a spent balance is: the words the
// router's quota classifier knows (insufficient_quota, credit_balance_exhausted)
// make the gate hold the endpoint as quota too, not as a burst limit, and for no
// longer than the router's first rung.
func TestOpenAICompat_GateAndAdapterAgreeOnQuota(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"insufficient_quota as type":     `{"error":{"message":"no credit","type":"insufficient_quota","code":null}}`,
		"insufficient_quota as code":     `{"error":{"message":"no credit","type":"x","code":"insufficient_quota"}}`,
		"credit_balance_exhausted":       `{"error":{"message":"no credit","type":"credit_balance_exhausted"}}`,
		"both markers, different fields": `{"error":{"message":"no credit","type":"insufficient_quota","code":"credit_balance_exhausted"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRecoveryRig(t)
			r.up.set(429, "", body)
			_, err := r.generate()
			if err == nil {
				t.Fatal("a 429 answered")
			}
			hold, left, ok := r.holdLeft()
			if !ok || hold.Class != integrations.ClassQuota || left > ai.DefaultCooldownBase {
				t.Errorf("hold = %+v, %v left, in force %v; want a quota hold of at most %v", hold, left, ok, ai.DefaultCooldownBase)
			}
			// The router benches it as quota too: the same word, the same class.
			if until, benched := r.h.r.BenchedUntil(r.ref); !benched || until.Sub(r.h.clk.Now()) <= ai.DefaultCooldownMax {
				t.Errorf("the router's bench = %v, %v; want the quota window, longer than any failure rung", until, benched)
			}
		})
	}
}

// A hold on an endpoint does not hide that it has recovered: the health probe
// passes through it, and a probe that succeeds ends the hold.
func TestOpenAICompat_HealthProbeEndsAHold(t *testing.T) {
	t.Parallel()
	r := newRecoveryRig(t)
	r.up.set(503, "", `{"error":{"message":"overloaded","type":"server_error"}}`)
	if _, err := r.generate(); err == nil {
		t.Fatal("a 503 answered")
	}
	if _, ok := r.kit.Registry.HoldFor(r.ref); !ok {
		t.Fatal("a 503 did not put the endpoint on hold")
	}
	hl, err := r.h.reg.Health(context.Background(), r.ref)
	if err != nil || hl.Status != integrations.HealthOK {
		t.Fatalf("Health = %+v, %v; want ok: GET /v1/models answers and the probe passes through the hold", hl, err)
	}
	if hold, ok := r.kit.Registry.HoldFor(r.ref); ok {
		t.Errorf("a successful probe left the hold in force: %+v", hold)
	}
}
