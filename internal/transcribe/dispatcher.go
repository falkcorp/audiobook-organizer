// file: internal/transcribe/dispatcher.go
// version: 1.4.0
// guid: ea9de4e6-980d-411f-a92c-878af1df490a
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
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
//     gets 1, a CPU box gets 100), each up to its Concurrency worth of
//     batch-sized chunks, with the remainder spilling to lower-priority
//     endpoints.
//
// So deadline-less bulk work lands on low-priority endpoints through spill,
// while work that genuinely CANNOT run somewhere (it needs a GPU, or must stay
// local) is excluded by step 1 rather than merely deprioritised in step 2.
type Endpoint struct {
	// URL is the base URL of the faster-whisper server,
	// e.g. "http://whisper-1.local:8000".
	URL string
	// Concurrency is the allocation weight: how many whisperBatchSize-sized
	// chunks this endpoint is offered per allocation pass. Values < 1 are
	// treated as 1.
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

// Cooldown policy: an endpoint that fails a batch is benched for
// cooldownBase × consecutive-failures, capped at cooldownMax, so a dead box
// is retried occasionally without being hammered on every page.
const (
	cooldownBase = 30 * time.Second
	cooldownMax  = 5 * time.Minute
)

// endpointHealth tracks per-URL consecutive failures and the cooldown window.
// It is package-level (keyed by URL) so the bench persists across
// TranscribeBatch calls within one process.
type endpointHealth struct {
	consecFails   int
	cooldownUntil time.Time
}

var (
	poolHealthMu sync.Mutex
	poolHealth   = map[string]*endpointHealth{}
)

func markEndpointFailure(url string) {
	poolHealthMu.Lock()
	defer poolHealthMu.Unlock()
	h := poolHealth[url]
	if h == nil {
		h = &endpointHealth{}
		poolHealth[url] = h
	}
	h.consecFails++
	d := time.Duration(h.consecFails) * cooldownBase
	if d > cooldownMax {
		d = cooldownMax
	}
	h.cooldownUntil = time.Now().Add(d)
}

func markEndpointSuccess(url string) {
	poolHealthMu.Lock()
	defer poolHealthMu.Unlock()
	if h := poolHealth[url]; h != nil {
		h.consecFails = 0
		h.cooldownUntil = time.Time{}
	}
}

func endpointInCooldown(url string) bool {
	poolHealthMu.Lock()
	defer poolHealthMu.Unlock()
	h := poolHealth[url]
	return h != nil && time.Now().Before(h.cooldownUntil)
}

// transcribePool distributes jobs across the endpoint pool.
//
// Round loop: healthy endpoints (not in cooldown) are filled in priority
// order via allocateJobs, each endpoint's share is sent through
// transcribeRemote concurrently, failures bench the endpoint and leave its
// jobs in the remaining set to be re-queued to the survivors on the next
// round. A *TransportError (naming every endpoint in the pool) is returned
// ONLY when jobs remain and no healthy endpoint is left — per the locked
// decision in PLAN.md, that error carries no per-file meaning and callers
// must write nothing.
func transcribePool(ctx context.Context, endpoints []Endpoint, requires []string, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
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
		results, err := transcribeRemoteWithHealth(ctx, g.Endpoint.URL, g.Health, g.Probed, g.Endpoint.Concurrency, jobs, onProgress)
		if err != nil {
			return nil, classifyTransport([]string{g.Endpoint.URL}, err)
		}
		return results, nil
	}

	ordered := make([]Endpoint, len(eligible))
	copy(ordered, eligible)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	remaining := make(map[string]string, len(jobs))
	for id, path := range jobs {
		remaining[id] = path
	}
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
				r, err := transcribeRemoteWithHealth(ctx, ep.URL, g.Health, g.Probed, ep.Concurrency, sub, progressFor())
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
			for id, br := range r.res {
				results[id] = br
			}
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

// allocateJobs assigns remaining jobs to healthy endpoints (already sorted by
// ascending Priority). Each pass offers every endpoint up to
// Concurrency×whisperBatchSize jobs in priority order; passes repeat until
// all jobs are assigned, so overflow beyond total capacity distributes
// proportionally to Concurrency while lower-Priority numbers fill first.
// Job IDs are assigned in sorted order for determinism.
func allocateJobs(healthy []Endpoint, remaining map[string]string) []map[string]string {
	assignments := make([]map[string]string, len(healthy))
	ids := make([]string, 0, len(remaining))
	for id := range remaining {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	pos := 0
	for pos < len(ids) {
		for i, ep := range healthy {
			c := ep.Concurrency
			if c < 1 {
				c = 1
			}
			take := c * whisperBatchSize()
			for n := 0; n < take && pos < len(ids); n++ {
				if assignments[i] == nil {
					assignments[i] = make(map[string]string)
				}
				assignments[i][ids[pos]] = remaining[ids[pos]]
				pos++
			}
			if pos >= len(ids) {
				break
			}
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
