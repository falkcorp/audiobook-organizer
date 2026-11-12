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
	"testing"

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
		EndpointSettings: ai.EndpointSettings{Concurrency: 1, TextModel: "gpt-batmobile"},
		URL:              url,
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
		!strings.Contains(hl.Message, "refused before it was sent") || strings.Contains(hl.Message, integrations.ReasonSecretMissing) {
		t.Errorf("Health = %+v, %v; want misconfigured, naming the refusal and not a missing secret", hl, err)
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
