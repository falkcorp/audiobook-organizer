// file: internal/transcribe/capabilities_test.go
// version: 2.0.0
// guid: 8b4e2d17-5c39-4a86-b1f0-9d7e3a25c8f4
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceIsGPUAcceptsOnlyAllowListedDevices(t *testing.T) {
	accepted := []string{
		"cuda", "CUDA", " cuda ", "cuda:0", "cuda:3",
		"metal", "mps", "rocm", "hip",
	}
	for _, d := range accepted {
		if !deviceIsGPU(d) {
			t.Errorf("deviceIsGPU(%q) = false, want true", d)
		}
	}

	// The refused set is the point of the allow-list: every one of these is a
	// string a deny-list of {"cpu"} would have waved through.
	refused := []string{
		"", " ", "cpu", "CPU", "cpu:0", "auto",
		"vulkan", "directml", "opencl", "xpu", // real backends, not yet vouched for
		"cpu-fallback-from-cuda", "cudaa", "notcuda", // substring traps
		"gpu", // a Kind value, not a device value
	}
	for _, d := range refused {
		if deviceIsGPU(d) {
			t.Errorf("deviceIsGPU(%q) = true, want false", d)
		}
	}
}

// TestGPURefusalReasonIsFailClosed pins the decision table. Each case names
// the operational situation, because the whole feature is about telling those
// situations apart.
func TestRequireGPUIsFailClosed(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name    string
		g       gatedEndpoint
		refused bool
		wantIn  string
	}{{
		name: "require_gpu off: a CPU endpoint is allowed through untouched",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://cpu", RequireGPU: false},
			Health:   remoteHealth{Device: "cpu"},
			Probed:   true,
		},
		refused: false,
	}, {
		name: "require_gpu off and unreachable: still not the gate's business",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://dead", RequireGPU: false},
			Probed:   false,
		},
		refused: false,
	}, {
		name: "require_gpu on, device cuda: allowed",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://gpu", RequireGPU: true},
			Health:   remoteHealth{Device: "cuda", BatchPipeline: boolPtr(true)},
			Probed:   true,
		},
		refused: false,
	}, {
		name: "require_gpu on, device cpu: THE case this feature exists for",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://amd-box", RequireGPU: true},
			Health:   remoteHealth{Device: "cpu", ComputeType: "int8"},
			Probed:   true,
		},
		refused: true,
		wantIn:  "missing [gpu]",
	}, {
		name: "require_gpu on, probe failed: refused, not assumed healthy",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://dead", RequireGPU: true},
			Probed:   false,
		},
		refused: true,
		wantIn:  "could not be read",
	}, {
		name: "require_gpu on, old server omits device: refused with an upgrade hint",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://old", RequireGPU: true},
			Health:   remoteHealth{BatchPipeline: boolPtr(true)},
			Probed:   true,
		},
		refused: true,
		wantIn:  "no device",
	}, {
		name: "require_gpu on, unknown device: fail-closed on the unrecognised",
		g: gatedEndpoint{
			Endpoint: Endpoint{URL: "http://exotic", RequireGPU: true},
			Health:   remoteHealth{Device: "vulkan"},
			Probed:   true,
		},
		refused: true,
		wantIn:  "missing [gpu]",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityRefusalReason(tc.g, nil)
			if tc.refused && got == "" {
				t.Fatalf("expected a refusal, got none")
			}
			if !tc.refused && got != "" {
				t.Fatalf("expected no refusal, got %q", got)
			}
			if tc.wantIn != "" && !strings.Contains(got, tc.wantIn) {
				t.Fatalf("reason %q does not mention %q", got, tc.wantIn)
			}
		})
	}
}

// healthServer stands in for a Whisper worker that answers /health and nothing
// else. Transcription requests 404, which is enough: a gated endpoint must
// never reach them.
func healthServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("gated endpoint received a request to %s — the gate did not hold", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPoolRefusesLoneCPUEndpointBeforeTheFastPath is the regression that
// motivated the feature. transcribePool short-circuits when len(endpoints)==1,
// so a gate placed inside the pool loop would never run for the single-endpoint
// deployment that actually exists. A CPU worker here must be refused, and the
// error must say why rather than reading as a missing config.
func TestPoolRefusesLoneCPUEndpointBeforeTheFastPath(t *testing.T) {
	srv := healthServer(t, map[string]any{
		"status": "ok", "batch_pipeline": true,
		"device": "cpu", "compute_type": "int8",
	})

	_, err := transcribePool(
		context.Background(),
		[]Endpoint{{URL: srv.URL, Concurrency: 1, RequireGPU: true, Label: "windows-box"}},
		nil,
		map[string]string{"book-1": "/nonexistent.wav"},
		nil,
	)
	if err == nil {
		t.Fatal("expected a refusal, got nil error — a CPU worker was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"missing [gpu]", `"cpu"`, "windows-box"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "no whisper endpoints configured") {
		t.Error("refusal is being reported as a missing-config error")
	}
}

// TestPoolAcceptsLoneGPUEndpoint is the known-good twin: the same single
// endpoint, same flag, only the device differs. Without it, a gate that
// refused everything unconditionally would pass the test above.
func TestPoolAcceptsLoneGPUEndpoint(t *testing.T) {
	srv := healthServer(t, map[string]any{
		"status": "ok", "batch_pipeline": true,
		"device": "cuda", "compute_type": "float16",
	})

	// No jobs: the endpoint clears the gate and transcribePool returns before
	// any transcription request, so /health is the only call the stub sees.
	gated, refused := gateEndpoints(
		context.Background(),
		[]Endpoint{{URL: srv.URL, Concurrency: 1, RequireGPU: true}},
		nil,
	)
	if len(refused) != 0 {
		t.Fatalf("cuda endpoint refused: %v", refused)
	}
	if len(gated) != 1 {
		t.Fatalf("got %d gated endpoints, want 1", len(gated))
	}
	if !gated[0].Probed || !gated[0].Health.supportsBatch() {
		t.Error("the gate's probe was not carried through for reuse by the batch decision")
	}
}

// TestGateKeepsHealthyEndpointsWhenOneIsRefused: a partial refusal must not
// take the pool down with it.
func TestGateKeepsHealthyEndpointsWhenOneIsRefused(t *testing.T) {
	gpu := healthServer(t, map[string]any{"batch_pipeline": true, "device": "cuda"})
	cpu := healthServer(t, map[string]any{"batch_pipeline": true, "device": "cpu"})

	gated, refused := gateEndpoints(context.Background(), []Endpoint{
		{URL: gpu.URL, RequireGPU: true, Label: "gpu"},
		{URL: cpu.URL, RequireGPU: true, Label: "cpu"},
	}, nil)
	if len(gated) != 1 || gated[0].Endpoint.URL != gpu.URL {
		t.Fatalf("expected only the cuda endpoint to survive, got %+v", gated)
	}
	if len(refused) != 1 || refused[0].URL != cpu.URL {
		t.Fatalf("expected exactly the cpu endpoint refused, got %+v", refused)
	}
	if !strings.Contains(describeRefusals(refused), "cpu") {
		t.Errorf("describeRefusals lost the reason: %q", describeRefusals(refused))
	}
}

// ---------------------------------------------------------------------------
// Capability label routing
// ---------------------------------------------------------------------------

func capsOf(t *testing.T, device string, batch bool, declared ...string) map[string]bool {
	t.Helper()
	g := gatedEndpoint{
		Endpoint: Endpoint{URL: "http://x", Capabilities: declared},
		Health:   remoteHealth{Device: device},
		Probed:   true,
	}
	if batch {
		b := true
		g.Health.BatchPipeline = &b
	}
	caps, _ := endpointCapabilities(g)
	return caps
}

// TestMeasuredCapabilitiesDeriveBothBackendAndFamily: a requirement can be as
// broad as "gpu" or as narrow as "metal", so the probe must yield both.
func TestMeasuredCapabilitiesDeriveBothBackendAndFamily(t *testing.T) {
	cases := []struct {
		device string
		batch  bool
		want   []string
	}{
		{"cuda", true, []string{"batch", "cuda", "gpu"}},
		{"cuda:0", false, []string{"cuda", "gpu"}},
		{"metal", false, []string{"gpu", "metal"}},
		{"cpu", true, []string{"batch", "cpu"}},
		{"vulkan", false, []string{}}, // not allow-listed: proves nothing
		{"", false, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.device, func(t *testing.T) {
			got := sortedKeys(capsOf(t, tc.device, tc.batch))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("device %q batch=%v -> %v, want %v", tc.device, tc.batch, got, tc.want)
			}
		})
	}
}

// TestDeclaredCapabilitiesCannotForgeAMeasuredOne is the whole reason the two
// sources are kept apart. Kind failed as a control because an operator could
// assert "gpu" on a CPU box; declaring it here must not satisfy a gpu
// requirement either.
func TestDeclaredCapabilitiesCannotForgeAMeasuredOne(t *testing.T) {
	caps := capsOf(t, "cpu", false, "gpu", "cuda", "local", "unmetered")
	if caps["gpu"] || caps["cuda"] {
		t.Errorf("a declared gpu/cuda label was believed on a CPU endpoint: %v", sortedKeys(caps))
	}
	if !caps["local"] || !caps["unmetered"] {
		t.Errorf("unmeasurable declared labels were dropped: %v", sortedKeys(caps))
	}
	if !caps["cpu"] {
		t.Errorf("measured cpu label missing: %v", sortedKeys(caps))
	}

	// The operator's line was deliberate, so it must not vanish silently.
	g := gatedEndpoint{
		Endpoint: Endpoint{URL: "http://x", Capabilities: []string{"GPU", "local"}},
		Health:   remoteHealth{Device: "cpu"},
		Probed:   true,
	}
	if _, ignored := endpointCapabilities(g); len(ignored) != 1 || ignored[0] != "gpu" {
		t.Errorf("ignored labels = %v, want [gpu] so the caller can log it", ignored)
	}
}

func TestRequiredLabelsForUnionsPoolRequiresAndRequireGPUSugar(t *testing.T) {
	got := requiredLabelsFor(Endpoint{RequireGPU: true}, []string{"local", "GPU", " ", "fast"})
	want := "fast,gpu,local"
	if strings.Join(got, ",") != want {
		t.Errorf("got %v, want [%s] — require_gpu must dedupe into the pool set, blanks dropped", got, want)
	}

	if got := requiredLabelsFor(Endpoint{}, nil); len(got) != 0 {
		t.Errorf("no requirement should be an empty set, got %v", got)
	}
}

// TestEmptyRequirementSetAcceptsEveryEndpoint pins the historical behaviour.
// Without it, an off-by-one in the matcher would refuse the whole pool the
// moment nobody configured a label.
func TestEmptyRequirementSetAcceptsEveryEndpoint(t *testing.T) {
	for _, device := range []string{"cpu", "cuda", "", "wat"} {
		g := gatedEndpoint{Endpoint: Endpoint{URL: "http://x"}, Health: remoteHealth{Device: device}, Probed: true}
		if reason := capabilityRefusalReason(g, nil); reason != "" {
			t.Errorf("device %q refused with no requirement configured: %s", device, reason)
		}
	}
	// Even an unprobed endpoint: a failed probe with no requirement is the
	// per-file fallback's business, not the gate's.
	g := gatedEndpoint{Endpoint: Endpoint{URL: "http://dead"}}
	if reason := capabilityRefusalReason(g, nil); reason != "" {
		t.Errorf("unprobed endpoint refused with no requirement configured: %s", reason)
	}
}

// TestTierRoutingSelectsBackendsMeetingAllLabels is the feature: two healthy
// GPU boxes, one of them declared "local", and a requirement of [gpu local]
// must select exactly one. It also proves matching is conjunctive — satisfying
// one of two labels is not enough.
func TestTierRoutingSelectsBackendsMeetingAllLabels(t *testing.T) {
	remote := healthServer(t, map[string]any{"batch_pipeline": true, "device": "cuda"})
	local := healthServer(t, map[string]any{"batch_pipeline": true, "device": "metal"})

	endpoints := []Endpoint{
		{URL: remote.URL, Label: "remote-gpu", Priority: 1},
		{URL: local.URL, Label: "local-gpu", Priority: 50, Capabilities: []string{"local"}},
	}

	gated, refused := gateEndpoints(context.Background(), endpoints, []string{"gpu", "local"})
	if len(gated) != 1 || gated[0].Endpoint.URL != local.URL {
		t.Fatalf("expected only the local gpu endpoint, got %+v", gated)
	}
	if len(refused) != 1 || refused[0].Label != "remote-gpu" {
		t.Fatalf("expected remote-gpu refused, got %+v", refused)
	}
	// The refusal must name the requirement AND what was offered, or an
	// operator cannot tell a typo from a genuinely unqualified box.
	msg := refused[0].Reason
	for _, want := range []string{"missing [local]", "gpu", "cuda"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}

	// Known-good twin: relax the requirement and BOTH must qualify, so the
	// test above is not passing because the matcher refuses indiscriminately.
	gated, refused = gateEndpoints(context.Background(), endpoints, []string{"gpu"})
	if len(gated) != 2 || len(refused) != 0 {
		t.Fatalf("requiring only [gpu] should keep both, got %d kept / %d refused", len(gated), len(refused))
	}
}

// TestUnsatisfiableRequirementFailsClosedNamingTheLabels: a typo'd label must
// not quietly route to nobody.
func TestUnsatisfiableRequirementFailsClosedNamingTheLabels(t *testing.T) {
	srv := healthServer(t, map[string]any{"batch_pipeline": true, "device": "cuda"})
	_, err := transcribePool(
		context.Background(),
		[]Endpoint{{URL: srv.URL, Concurrency: 1, Label: "gpu-box"}},
		[]string{"gpu", "unmeetable"},
		map[string]string{"book-1": "/nonexistent.wav"},
		nil,
	)
	if err == nil {
		t.Fatal("an unsatisfiable requirement was silently accepted")
	}
	for _, want := range []string{"unmeetable", "gpu-box", "capability requirements"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
