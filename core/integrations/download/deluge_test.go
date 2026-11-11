// file: core/integrations/download/deluge_test.go
// version: 1.0.0
// guid: 03c433a1-166f-4a72-b8e2-669e1899056a
// last-edited: 2026-11-11

package download_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download/deluge"
	"github.com/falkcorp/mediacore/secrets"
)

// TestDeluge_PrivateHostNeedsLocal (05-download §4.1): a Deluge on a private
// address is reachable only through the instance's integrations.HTTP, whose
// egress guard (providerhttp, F7-02, not yet built) admits a private origin
// only when the Registry lists it in Trust.PrivateOrigins, and the Registry
// lists it only when Local holds, which only an interactive core:admin may
// set. Until providerhttp lands this test pins the half F7-04 owns: the
// adapter declares EgressConfigured, asks for its client through the
// Registry's Outbound spec, and sends every request to the configured origin
// through that client and nowhere else. The guard's refusal itself is
// providerhttp's test.
func TestDeluge_PrivateHostNeedsLocal(t *testing.T) {
	d, err := deluge.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, d)
	ref := integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}
	const origin = "http://192.0.2.40:8112"
	inst := integrations.InstanceConfig{
		Ref: ref, Adapter: deluge.AdapterID, Name: "Deluge", Enabled: true,
		Settings: map[string]any{"url": origin + "/"},
	}
	pw := map[string]secrets.Value{"password": secrets.NewValue("alfred-1")}
	ctx := context.Background()

	// A manager cannot grant Local; the instance's private origin is not trusted.
	if _, err := h.reg.Put(manager(), inst, pw); err != nil {
		t.Fatal(err)
	}
	probe := func() {
		t.Helper()
		if _, err := h.reg.Health(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	probe()
	spec := lastSpec(t, h, ref)
	if spec.Egress != integrations.EgressConfigured || spec.Adapter != deluge.AdapterID {
		t.Errorf("spec = %+v: a download client is EgressConfigured", spec)
	}
	if len(spec.Trust.PrivateOrigins) != 0 {
		t.Fatalf("a private origin is trusted without Local: %v", spec.Trust.PrivateOrigins)
	}

	// An interactive admin grants Local: now the origin, and only it, is trusted.
	cur, _ := h.reg.Config(ref)
	inst.Local, inst.Version = true, cur.Version
	if _, err := h.reg.Put(admin(), inst, nil); err != nil {
		t.Fatal(err)
	}
	probe()
	if got := lastSpec(t, h, ref).Trust.PrivateOrigins; !slices.Equal(got, []string{origin}) {
		t.Fatalf("Trust.PrivateOrigins = %v, want [%s]", got, origin)
	}

	// Every request the adapter made went through the injected client, to the
	// configured origin's /json, carrying no credential in the URL.
	sent := h.out.Sent()
	if len(sent) == 0 {
		t.Fatal("the adapter sent nothing through the Registry's HTTP")
	}
	for _, s := range sent {
		if s.Ref != ref || s.URL != origin+"/json" {
			t.Errorf("request %+v: want %s/json for %s", s, origin, ref)
		}
		if strings.Contains(s.URL, "alfred-1") {
			t.Errorf("the password is in the URL: %s", s.URL)
		}
	}
}

// lastSpec is the newest OutboundSpec asked for ref.
func lastSpec(t testing.TB, h *harness, ref integrations.Ref) integrations.OutboundSpec {
	t.Helper()
	specs := h.out.Specs()
	for _, spec := range slices.Backward(specs) {
		if spec.Ref == ref {
			return spec
		}
	}
	t.Fatalf("no outbound spec for %s", ref)
	return integrations.OutboundSpec{}
}
