// file: internal/transcribe/capabilities.go
// version: 2.0.0
// guid: 3f1c8a52-6d94-4b17-9e02-c8a7d5b41f60
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"fmt"
	"log/slog"
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
// only place this decision becomes visible, so it names the required set AND
// what the endpoint actually offered.
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
// those cleared to receive work and those that do not satisfy the required
// capability labels.
//
// This FILTERS the candidate set; it does not order it. Priority-ordered
// spill still decides who gets the work among the survivors, so a "tier" here
// is a required-label set plus a priority preference -- there is deliberately
// no routing table.
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
func gateEndpoints(ctx context.Context, endpoints []Endpoint, poolRequires []string) ([]gatedEndpoint, []gpuRefusal) {
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
		if _, ignored := endpointCapabilities(g); len(ignored) > 0 {
			slog.Warn("transcribe: ignoring declared capabilities that are measured from /health, not declarable",
				"url", g.Endpoint.URL, "ignored", ignored)
		}
		if reason := capabilityRefusalReason(g, poolRequires); reason != "" {
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

// measuredLabels is the namespace of capability labels DERIVED from /health.
// An operator cannot declare one of these: that is the entire point. `Kind`
// failed as a control because it let someone type "gpu" on a CPU box and be
// believed; a declared label that trespasses here is dropped for the same
// reason. Keep this in sync with measuredCapabilities below.
var measuredLabels = map[string]bool{
	"gpu": true, "cpu": true, "batch": true,
	"cuda": true, "metal": true, "mps": true, "rocm": true, "hip": true,
}

// normalizeLabel lowercases and trims one label. Applied to both declared and
// required labels so that "GPU", " gpu " and "gpu" are the same requirement --
// a required label that silently matched nothing because of case would take
// the whole pool down with a confusing error.
func normalizeLabel(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// measuredCapabilities derives the labels an endpoint has PROVEN it has, from
// the /health response. An unprobed endpoint has proven nothing and gets an
// empty set -- which is what makes an unsatisfiable requirement fail closed
// rather than fall through.
func measuredCapabilities(g gatedEndpoint) map[string]bool {
	caps := map[string]bool{}
	if !g.Probed {
		return caps
	}
	device := normalizeLabel(g.Health.Device)
	if i := strings.IndexByte(device, ':'); i >= 0 {
		device = device[:i]
	}
	if device != "" && measuredLabels[device] {
		// The specific backend ("cuda", "metal", ...) so a requirement can be
		// as narrow as the operator needs.
		caps[device] = true
	}
	// ...and the family, so the common case is just "gpu".
	if deviceIsGPU(g.Health.Device) {
		caps["gpu"] = true
	} else if device == "cpu" {
		caps["cpu"] = true
	}
	if g.Health.supportsBatch() {
		caps["batch"] = true
	}
	return caps
}

// endpointCapabilities merges measured and declared labels. Measured wins: a
// declared label inside measuredLabels is DROPPED, not merged, so declaring
// "gpu" on a CPU box cannot satisfy a "gpu" requirement. The dropped label is
// returned so the caller can log it -- silently ignoring an operator's
// deliberate config line is its own silent failure.
func endpointCapabilities(g gatedEndpoint) (caps map[string]bool, ignored []string) {
	caps = measuredCapabilities(g)
	for _, raw := range g.Endpoint.Capabilities {
		l := normalizeLabel(raw)
		if l == "" {
			continue
		}
		if measuredLabels[l] {
			ignored = append(ignored, l)
			continue
		}
		caps[l] = true
	}
	sort.Strings(ignored)
	return caps, ignored
}

// requiredLabelsFor is the full requirement set an endpoint must satisfy: the
// pool-wide requirement plus the endpoint's own require_gpu, which is sugar
// for requiring "gpu" of itself. Expressing require_gpu through the same
// matcher keeps ONE gate: two parallel mechanisms could disagree about the
// same endpoint and produce two different refusal messages.
func requiredLabelsFor(ep Endpoint, poolRequires []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		l := normalizeLabel(raw)
		if l == "" || seen[l] {
			return
		}
		seen[l] = true
		out = append(out, l)
	}
	for _, r := range poolRequires {
		add(r)
	}
	if ep.RequireGPU {
		add("gpu")
	}
	sort.Strings(out)
	return out
}

// capabilityRefusalReason returns why the gate refuses this endpoint, or "" to
// keep it. It has no network in it so the whole decision is testable as a
// table.
//
// An EMPTY requirement set means "any endpoint" -- stated explicitly because
// it is the historical behaviour and the pool must not start refusing
// everything the moment nobody has configured a label.
func capabilityRefusalReason(g gatedEndpoint, poolRequires []string) string {
	required := requiredLabelsFor(g.Endpoint, poolRequires)
	if len(required) == 0 {
		return ""
	}
	if !g.Probed {
		// Fail-closed. An unreachable endpoint is refused by the gate rather
		// than left to the cooldown path, because the two are not the same
		// claim: cooldown says "this failed, try later", the gate says "this
		// was never shown to satisfy [labels]".
		return fmt.Sprintf("requires %v but /health could not be read", required)
	}
	caps, _ := endpointCapabilities(g)
	var missing []string
	for _, r := range required {
		if !caps[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	detail := fmt.Sprintf("does not satisfy %v (missing %v; offers %v)", required, missing, sortedKeys(caps))
	// The device and compute_type make "missing [gpu]" a coherent story
	// rather than a bare rejection.
	if d := strings.TrimSpace(g.Health.Device); d != "" {
		detail += fmt.Sprintf("; /health device %q", d)
		if ct := strings.TrimSpace(g.Health.ComputeType); ct != "" {
			detail += fmt.Sprintf(", compute_type %q", ct)
		}
	} else {
		detail += "; /health reports no device " +
			"(whisper_server.py older than 2.9.0 — upgrade the worker or drop the requirement)"
	}
	return detail
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
