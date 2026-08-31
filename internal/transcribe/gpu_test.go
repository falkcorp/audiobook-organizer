// file: internal/transcribe/gpu_test.go
// version: 1.0.0
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
func TestGPURefusalReasonIsFailClosed(t *testing.T) {
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
		wantIn:  `device "cpu"`,
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
		wantIn:  `device "vulkan"`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gpuRefusalReason(tc.g)
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
		map[string]string{"book-1": "/nonexistent.wav"},
		nil,
	)
	if err == nil {
		t.Fatal("expected a refusal, got nil error — a CPU worker was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"require_gpu", `"cpu"`, "windows-box"} {
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
	})
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
