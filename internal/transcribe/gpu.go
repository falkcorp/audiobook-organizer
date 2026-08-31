// file: internal/transcribe/gpu.go
// version: 1.0.0
// guid: 3f1c8a52-6d94-4b17-9e02-c8a7d5b41f60
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// gpuDevices is an ALLOW-LIST of ctranslate2/mlx device strings that count as
// GPU-accelerated decoding.
//
// It is an allow-list rather than a deny-list of {"cpu"} on purpose. A
// deny-list accepts every device string it has never heard of, so a new
// backend, a typo, or a truncated field all read as "GPU" by omission -- the
// failure mode is silent and points the wrong way. An allow-list refuses what
// it cannot vouch for, and the refusal names the device, so the fix is
// obvious. This is the same fail-closed shape as the activity-index pushdown
// gate in internal/database.
//
// Membership is a claim that the backend actually offloads decoding:
//   - cuda  -- ctranslate2's NVIDIA backend.
//   - metal -- scripts/whisper_mlx_server.py (MLX on Apple Silicon).
//   - mps   -- the same silicon under PyTorch's name for it, accepted because
//     a non-MLX Apple worker would report it.
//   - rocm/hip -- AMD. Accepted so a future AMD worker is not refused by an
//     allow-list nobody remembered to update. NOTE: ctranslate2 has no ROCm
//     backend, so faster-whisper CANNOT report these; only a different engine
//     (e.g. whisper.cpp + Vulkan) could. Listing them is forward-looking, not
//     a claim that faster-whisper works on AMD.
//
// Deliberately absent: "cpu", "auto", "" -- and "vulkan", which is left out
// because no worker in this repo reports it yet and an allow-list entry
// should follow a real measured value, not an anticipated one.
var gpuDevices = map[string]bool{
	"cuda":  true,
	"metal": true,
	"mps":   true,
	"rocm":  true,
	"hip":   true,
}

// deviceIsGPU reports whether a /health device string names GPU decoding.
//
// It normalises case, surrounding space, and a device-index suffix
// ("cuda:0"), because those are formatting of the same answer. It does NOT
// do prefix or substring matching: "cpu-fallback-from-cuda" must not pass
// because it contains "cuda".
func deviceIsGPU(device string) bool {
	d := strings.ToLower(strings.TrimSpace(device))
	if i := strings.IndexByte(d, ':'); i >= 0 {
		d = d[:i]
	}
	return gpuDevices[d]
}

// gatedEndpoint is an endpoint that passed the gate, carrying the /health
// response that cleared it so the batch decision downstream reuses that same
// probe instead of issuing a second one.
type gatedEndpoint struct {
	Endpoint Endpoint
	Health   remoteHealth
	// Probed is false when the /health request did not complete. Only
	// reachable for an endpoint with RequireGPU off -- the gate refuses a
	// RequireGPU endpoint it could not probe.
	Probed bool
}

// gpuRefusal records one endpoint the gate turned away, and why. The reason
// is operator-facing text: it ends up in the TransportError, which is the
// only place this decision becomes visible.
type gpuRefusal struct {
	URL    string
	Label  string
	Reason string
}

func (r gpuRefusal) String() string {
	name := r.URL
	if r.Label != "" {
		name = fmt.Sprintf("%s (%s)", r.URL, r.Label)
	}
	return name + ": " + r.Reason
}

// gateEndpoints probes every endpoint's /health once and splits the pool into
// those cleared to receive work and those refused by RequireGPU.
//
// Every endpoint is probed, not just the RequireGPU ones, because the probe is
// needed anyway to choose the batch vs per-file path -- so this costs the same
// one request per endpoint the pool already made, just earlier and in one
// place. Probes run concurrently: each carries a 3s timeout, and serialising
// them would make a pool of five dead endpoints take 15s to reject.
//
// The results are NOT cached across calls. A box that gets fixed must become
// eligible again without restarting the service, and this process restarts
// rarely enough that a stale refusal would outlive the problem it describes.
func gateEndpoints(ctx context.Context, endpoints []Endpoint) ([]gatedEndpoint, []gpuRefusal) {
	probes := make([]gatedEndpoint, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Go(func() {
			h, ok := probeRemoteHealth(ctx, ep.URL)
			probes[i] = gatedEndpoint{Endpoint: ep, Health: h, Probed: ok}
		})
	}
	wg.Wait()

	var kept []gatedEndpoint
	var refused []gpuRefusal
	for _, g := range probes {
		if reason := gpuRefusalReason(g); reason != "" {
			refused = append(refused, gpuRefusal{
				URL:    g.Endpoint.URL,
				Label:  g.Endpoint.Label,
				Reason: reason,
			})
			continue
		}
		kept = append(kept, g)
	}
	return kept, refused
}

// gpuRefusalReason returns why the gate refuses this endpoint, or "" to keep
// it. Split out from gateEndpoints so the whole decision is one testable
// function with no network in it.
func gpuRefusalReason(g gatedEndpoint) string {
	if !g.Endpoint.RequireGPU {
		return ""
	}
	if !g.Probed {
		// Fail-closed. An unreachable endpoint is refused by the gate rather
		// than left to the cooldown path, because the two are not the same
		// claim: cooldown says "this failed, try later", the gate says "this
		// was never shown to be a GPU".
		return "require_gpu is set but /health could not be read"
	}
	if strings.TrimSpace(g.Health.Device) == "" {
		return "require_gpu is set but /health reports no device " +
			"(whisper_server.py older than 2.9.0 — upgrade the worker or unset require_gpu)"
	}
	if !deviceIsGPU(g.Health.Device) {
		detail := fmt.Sprintf("require_gpu is set but /health reports device %q", g.Health.Device)
		if ct := strings.TrimSpace(g.Health.ComputeType); ct != "" {
			detail += fmt.Sprintf(" (compute_type %q)", ct)
		}
		return detail
	}
	return ""
}

// describeRefusals renders refusals deterministically for an error message.
func describeRefusals(refused []gpuRefusal) string {
	parts := make([]string, 0, len(refused))
	for _, r := range refused {
		parts = append(parts, r.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
