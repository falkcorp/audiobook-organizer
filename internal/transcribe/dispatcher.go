// file: internal/transcribe/dispatcher.go
// version: 1.7.2
// guid: ea9de4e6-980d-411f-a92c-878af1df490a
// last-edited: 2026-10-23

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// Endpoint describes one Whisper server in the dispatch pool.
//
// Dispatch is a filter followed by priority-ordered spill, in that order:
//
//  1. Capability labels FILTER the candidate set. An endpoint qualifies only
//     if its capabilities contain every label the pool requires (see
//     capabilities.go). This is what "tier routing" means here — a tier is a
//     required-label set, not an entry in a routing table.
//  2. Priority ORDERS the survivors. Lower numbers are filled first (a GPU box
//     gets 1, a CPU box gets 100), one batch-sized chunk per FREE request
//     slot, with the remainder spilling to lower-priority endpoints only when
//     the higher tier's slots are all taken. Equal-priority endpoints share
//     work round-robin.
//
// So deadline-less bulk work lands on low-priority endpoints through spill,
// while work that genuinely CANNOT run somewhere (it needs a GPU, or must stay
// local) is excluded by step 1 rather than merely deprioritised in step 2.
type Endpoint struct {
	// URL is the base URL of the faster-whisper server,
	// e.g. "http://whisper-1.local:8000".
	URL string
	// Concurrency is the endpoint's request-slot count: how many requests it
	// may serve at once (enforced at the request), and so how many
	// whisperBatchSize-sized chunks allocation offers it while its slots are
	// free. Values < 1 are treated as 1.
	Concurrency int
	// Label is a human-readable name for logs ("gpu-box", "cpu-node").
	Label string
	// Priority orders allocation: lower = preferred. Healthy endpoints are
	// filled in ascending Priority order; jobs spill to higher numbers only
	// when lower-numbered endpoints are saturated or unhealthy.
	Priority int
	// RequireGPU refuses this endpoint unless it proves the "gpu" capability.
	// Sugar for adding "gpu" to this endpoint's required set; see
	// capabilities.go for why this is fail-closed.
	RequireGPU bool
	// Capabilities are labels an operator DECLARES about this endpoint, for
	// properties no probe can see ("local", "unmetered", "fast"). Labels that
	// are measured from /health instead (gpu, cpu, cuda, metal, batch, ...)
	// are ignored here and derived from the probe -- declaring them would
	// recreate the Kind illusion this replaces.
	Capabilities []string
}

// Cooldown policy moved to internal/aidispatch (Health), keyed by endpoint ID:
// an endpoint that fails a batch is benched for aidispatch.CooldownBase ×
// consecutive-failures, capped at aidispatch.CooldownMax, so a dead box is
// retried occasionally without being hammered on every page. The process-wide
// tracker persists the bench across TranscribeBatch calls. These wrappers keep
// the URL-keyed call sites below unchanged, via the same URL→ID shim as the
// in-flight slots.

func markEndpointFailure(url string) {
	aidispatch.DefaultHealth().MarkFailure(endpointIDForURL(url))
}

func markEndpointSuccess(url string) {
	aidispatch.DefaultHealth().MarkSuccess(endpointIDForURL(url))
}

func endpointInCooldown(url string) bool {
	return aidispatch.DefaultHealth().InCooldown(endpointIDForURL(url))
}

// transcribePool distributes jobs across the endpoint pool.
//
// Round loop: healthy endpoints (not in cooldown) are filled in priority
// order via allocateJobs, each endpoint's share is sent through
// transcribeRemoteWithHealth concurrently, failures bench the endpoint and leave its
// jobs in the remaining set to be re-queued to the survivors on the next
// round. A *TransportError (naming every endpoint in the pool) is returned
// ONLY when jobs remain and no healthy endpoint is left — per the locked
// decision in PLAN.md, that error carries no per-file meaning and callers
// must write nothing.
func transcribePool(ctx context.Context, endpoints []Endpoint, requires []string, jobs map[string]string, onProgress ProgressFunc, journal ResultJournal) (map[string]BatchResult, error) {
	return dispatchPool(ctx, endpoints, requires, jobs, onProgress, nil,
		func(ctx context.Context, g gatedEndpoint, sub map[string]string, progress ProgressFunc) (map[string]BatchResult, error) {
			return transcribeRemoteWithHealth(ctx, g.Endpoint.URL, g.Health, g.Probed, g.Endpoint.Concurrency, sub, progress, journal)
		})
}

// endpointSender transcribes one endpoint's share of a dispatch. It is the only
// part of the round loop that depends on what a job IS (a WAV path to upload,
// or a server path the worker extracts itself), which is why dispatchPool is
// generic over the job type instead of being copied per kind of job.
type endpointSender[J any] func(ctx context.Context, g gatedEndpoint, jobs map[string]J, onProgress ProgressFunc) (map[string]BatchResult, error)

// dispatchPool is the pool round loop shared by every kind of job: gate,
// priority-ordered allocation, concurrent per-endpoint sends, cooldown on
// failure, re-queue to survivors. extraRefusal (may be nil) lets a caller
// refuse endpoints the label gate cannot express -- the path pipeline refuses
// a worker whose mapped NFS root is unreadable right now. A non-empty return is
// the operator-facing reason and is reported like any capability refusal.
func dispatchPool[J any](ctx context.Context, endpoints []Endpoint, requires []string, jobs map[string]J, onProgress ProgressFunc, extraRefusal func(gatedEndpoint) string, send endpointSender[J]) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	if len(endpoints) == 0 {
		return nil, classifyTransport(nil, fmt.Errorf("no whisper endpoints configured"))
	}

	// The require_gpu gate runs BEFORE the single-endpoint fast path below.
	// Putting it inside the pool loop instead would miss the one shape that
	// actually occurs in production today -- a single configured endpoint
	// quietly serving from a CPU backend -- because that shape never reaches
	// the loop. gateEndpoints also performs the /health probe that the batch
	// decision needs, so this adds no requests.
	gated, refused := gateEndpoints(ctx, endpoints, requires)
	if extraRefusal != nil {
		kept := gated[:0:0]
		for _, g := range gated {
			if reason := extraRefusal(g); reason != "" {
				refused = append(refused, gpuRefusal{URL: g.Endpoint.URL, Label: g.Endpoint.Label, Reason: reason})
				continue
			}
			kept = append(kept, g)
		}
		gated = kept
	}
	for _, r := range refused {
		slog.Warn("transcribe: endpoint refused by capability requirements", "url", r.URL, "label", r.Label, "reason", r.Reason)
	}
	if len(gated) == 0 {
		// Distinct from "no whisper endpoints configured": that sends an
		// operator hunting a config typo, when the real answer is that the
		// box they configured is not on a GPU.
		return nil, classifyTransport(endpointURLs(endpoints), fmt.Errorf(
			"all %d whisper endpoint(s) refused by capability requirements %v: %s",
			len(endpoints), requires, describeRefusals(refused)))
	}

	// Health is keyed by URL so allocateJobs keeps operating on []Endpoint
	// and its index-based contract with the assignment slices is untouched.
	probeByURL := make(map[string]gatedEndpoint, len(gated))
	eligible := make([]Endpoint, 0, len(gated))
	for _, g := range gated {
		probeByURL[g.Endpoint.URL] = g
		eligible = append(eligible, g.Endpoint)
	}

	// One endpoint: byte-for-byte the historical single-URL behaviour.
	if len(eligible) == 1 {
		g := probeByURL[eligible[0].URL]
		results, err := send(ctx, g, jobs, onProgress)
		if err != nil {
			return nil, classifyTransport([]string{g.Endpoint.URL}, err)
		}
		return results, nil
	}

	ordered := make([]Endpoint, len(eligible))
	copy(ordered, eligible)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	remaining := make(map[string]J, len(jobs))
	maps.Copy(remaining, jobs)
	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)

	// Pool-wide progress: each endpoint call reports its own cumulative
	// (done, total); the adapter converts that to a shared pool-wide count.
	var progressMu sync.Mutex
	poolDone := 0
	progressFor := func() ProgressFunc {
		if onProgress == nil {
			return nil
		}
		last := 0
		return func(d, _ int) {
			progressMu.Lock()
			poolDone += d - last
			last = d
			cur := poolDone
			progressMu.Unlock()
			onProgress(cur, total)
		}
	}

	var lastErr error
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, classifyTransport(endpointURLs(ordered), err)
		}
		var healthy []Endpoint
		for _, ep := range ordered {
			if !endpointInCooldown(ep.URL) {
				healthy = append(healthy, ep)
			}
		}
		if len(healthy) == 0 {
			if lastErr == nil {
				lastErr = fmt.Errorf("all %d whisper endpoints in cooldown", len(ordered))
			}
			return nil, classifyTransport(endpointURLs(ordered), lastErr)
		}

		assignments := allocateJobs(healthy, remaining)

		type epResult struct {
			idx int
			res map[string]BatchResult
			err error
		}
		resCh := make(chan epResult, len(assignments))
		var wg sync.WaitGroup
		for idx, sub := range assignments {
			if len(sub) == 0 {
				continue
			}
			ep := healthy[idx]
			wg.Go(func() {
				g := probeByURL[ep.URL]
				r, err := send(ctx, g, sub, progressFor())
				resCh <- epResult{idx: idx, res: r, err: err}
			})
		}
		wg.Wait()
		close(resCh)

		for r := range resCh {
			ep := healthy[r.idx]
			if r.err != nil {
				// Bench the endpoint; its jobs stay in remaining and are
				// re-queued to a surviving endpoint on the next round.
				//
				// ErrSlotWait is NOT endpoint evidence: the request was never
				// sent, we only failed to get a slot (usually because the op
				// was cancelled). poolHealth is process-global and its cooldown
				// OUTLIVES the op, so benching here would make the next run
				// report "all N endpoints in cooldown" about servers that were
				// never contacted.
				if !errors.Is(r.err, ErrSlotWait) {
					markEndpointFailure(ep.URL)
				}
				lastErr = fmt.Errorf("%s: %w", ep.URL, r.err)
				continue
			}
			markEndpointSuccess(ep.URL)
			maps.Copy(results, r.res)
			// Clear the whole assigned set, not just returned IDs: the
			// transport skips unreadable WAVs without error (same as the
			// single-URL path), so a missing result is a skip, not a retry.
			for id := range assignments[r.idx] {
				delete(remaining, id)
			}
		}
	}
	return results, nil
}

// allocCursor rotates which equal-priority endpoint an allocation offers
// first. Without it every call starts at the same endpoint, and a caller that
// sends one small batch per call (the per-file intro op sends 8 jobs per call)
// would put every batch on endpoint 0 while the others idle.
var allocCursor atomic.Uint64

// endpointFreeSlots is how many request slots url has free right now: its
// Concurrency (values < 1 mean 1) minus the slots currently held.
func endpointFreeSlots(ep Endpoint) int {
	return max(ep.Concurrency, 1) - inFlightDepth(ep.URL)
}

// allocateJobs assigns remaining jobs to healthy endpoints (already sorted by
// ascending Priority). See allocateJobsWith for the policy.
func allocateJobs[J any](healthy []Endpoint, remaining map[string]J) []map[string]J {
	return allocateJobsWith(healthy, remaining, endpointFreeSlots, int(allocCursor.Add(1)-1))
}

// allocateJobsWith assigns jobs capacity-first, then by overflow.
//
// Phase 1 (capacity-aware): one chunk of whisperBatchSize jobs is offered per
// FREE request slot. Endpoints are visited tier by tier in ascending Priority
// (a lower number fills first; jobs spill to the next tier only once every
// slot in the tier is taken), and within a tier of equal Priority they are
// visited round-robin starting at `start`, so equal endpoints share the work
// instead of the first one taking it all. An endpoint whose slots are full is
// skipped rather than queued behind.
//
// Phase 2 (overflow): when every slot in the pool is taken and jobs remain,
// each pass offers every endpoint up to Concurrency x whisperBatchSize jobs,
// tier by tier in priority order, round-robin from `start` inside a tier, so
// the excess queues on slots proportionally to Concurrency and a small batch
// still rotates across equal endpoints.
// Job IDs are assigned in sorted order for determinism.
func allocateJobsWith[J any](healthy []Endpoint, remaining map[string]J, free func(Endpoint) int, start int) []map[string]J {
	assignments := make([]map[string]J, len(healthy))
	ids := make([]string, 0, len(remaining))
	for id := range remaining {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	chunk := max(whisperBatchSize(), 1)

	pos := 0
	give := func(i, n int) {
		for k := 0; k < n && pos < len(ids); k++ {
			if assignments[i] == nil {
				assignments[i] = make(map[string]J)
			}
			assignments[i][ids[pos]] = remaining[ids[pos]]
			pos++
		}
	}

	// Phase 1: tiers of equal priority, round-robin inside a tier.
	for lo := 0; lo < len(healthy) && pos < len(ids); {
		hi := lo + 1
		for hi < len(healthy) && healthy[hi].Priority == healthy[lo].Priority {
			hi++
		}
		tier := hi - lo
		slots := make([]int, tier)
		for k := range tier {
			slots[k] = max(free(healthy[lo+k]), 0)
		}
		for pos < len(ids) {
			progressed := false
			for k := range tier {
				idx := (start + k) % tier
				if slots[idx] == 0 || pos >= len(ids) {
					continue
				}
				give(lo+idx, chunk)
				slots[idx]--
				progressed = true
			}
			if !progressed {
				break
			}
		}
		lo = hi
	}

	// Phase 2: every slot is taken; queue the rest. Each pass walks the tiers
	// in priority order and, inside a tier, visits endpoints round-robin from
	// `start`, one chunk per visit, up to each endpoint's Concurrency chunks. A
	// pass therefore still offers every endpoint Concurrency x whisperBatchSize
	// jobs, but a small batch no longer always lands on healthy[0].
	for pos < len(ids) {
		for lo := 0; lo < len(healthy) && pos < len(ids); {
			hi := lo + 1
			for hi < len(healthy) && healthy[hi].Priority == healthy[lo].Priority {
				hi++
			}
			tier := hi - lo
			maxConc := 0
			for k := range tier {
				maxConc = max(maxConc, max(healthy[lo+k].Concurrency, 1))
			}
			for r := 0; r < maxConc && pos < len(ids); r++ {
				for k := range tier {
					idx := (start + k) % tier
					if r < max(healthy[lo+idx].Concurrency, 1) && pos < len(ids) {
						give(lo+idx, chunk)
					}
				}
			}
			lo = hi
		}
	}
	return assignments
}

func endpointURLs(eps []Endpoint) []string {
	urls := make([]string, 0, len(eps))
	for _, ep := range eps {
		urls = append(urls, ep.URL)
	}
	return urls
}
