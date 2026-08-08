// file: internal/transcribe/dispatcher.go
// version: 1.0.0
// guid: ea9de4e6-980d-411f-a92c-878af1df490a
// last-edited: 2026-08-07

package transcribe

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Endpoint describes one Whisper server in the dispatch pool.
//
// Allocation is priority-ordered, not tier-routed: lower Priority numbers are
// filled first (a GPU box gets 1, a CPU box gets 100), each up to its
// Concurrency worth of batch-sized chunks, with the remainder spilling to
// lower-priority endpoints. Deadline-less bulk work (tier 3) is expected to
// land on low-priority CPU endpoints through this spill mechanism rather than
// through any explicit tier routing table.
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
	// Kind is informational only ("gpu", "cpu", or ""). A future /health
	// auto-probe may fill it; nothing routes on it today.
	Kind string
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
func transcribePool(ctx context.Context, endpoints []Endpoint, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	if len(endpoints) == 0 {
		return nil, classifyTransport(nil, fmt.Errorf("no whisper endpoints configured"))
	}
	// One endpoint: byte-for-byte the historical single-URL behaviour.
	if len(endpoints) == 1 {
		results, err := transcribeRemote(ctx, endpoints[0].URL, jobs, onProgress)
		if err != nil {
			return nil, classifyTransport([]string{endpoints[0].URL}, err)
		}
		return results, nil
	}

	ordered := make([]Endpoint, len(endpoints))
	copy(ordered, endpoints)
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
			wg.Add(1)
			go func(idx int, ep Endpoint, sub map[string]string) {
				defer wg.Done()
				r, err := transcribeRemote(ctx, ep.URL, sub, progressFor())
				resCh <- epResult{idx: idx, res: r, err: err}
			}(idx, healthy[idx], sub)
		}
		wg.Wait()
		close(resCh)

		for r := range resCh {
			ep := healthy[r.idx]
			if r.err != nil {
				// Bench the endpoint; its jobs stay in remaining and are
				// re-queued to a surviving endpoint on the next round.
				markEndpointFailure(ep.URL)
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
			take := c * whisperBatchSize
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
