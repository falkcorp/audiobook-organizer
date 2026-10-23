// file: internal/transcribe/dispatcher_alloc_test.go
// version: 1.0.2
// guid: 7d2e9a41-5b3c-4f68-a1d7-3c8e0b6f9254
// last-edited: 2026-10-23

package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func countJobs[J any](a []map[string]J) []int {
	out := make([]int, len(a))
	for i, m := range a {
		out[i] = len(m)
	}
	return out
}

func jobSet(n int) map[string]string {
	m := make(map[string]string, n)
	for i := range n {
		m[fmt.Sprintf("job-%03d", i)] = "p"
	}
	return m
}

// A call that carries one batch must not always land on endpoint 0: equal
// priority endpoints are offered first in rotation.
func TestAllocateJobs_RoundRobinsEqualPriorityEndpoints(t *testing.T) {
	eps := []Endpoint{
		{URL: "http://a", Concurrency: 4, Priority: 1},
		{URL: "http://b", Concurrency: 4, Priority: 1},
		{URL: "http://c", Concurrency: 4, Priority: 1},
	}
	free := func(ep Endpoint) int { return ep.Concurrency }
	hits := make([]int, len(eps))
	for start := range 9 {
		got := countJobs(allocateJobsWith(eps, jobSet(whisperBatchSize()), free, start))
		for i, n := range got {
			hits[i] += n
		}
	}
	for i, n := range hits {
		if n == 0 {
			t.Fatalf("endpoint %d never received a batch across 9 allocations: %v", i, hits)
		}
	}
}

// An endpoint with no free slot is skipped while another has one.
func TestAllocateJobs_SkipsEndpointWithFullSlots(t *testing.T) {
	eps := []Endpoint{
		{URL: "http://full", Concurrency: 2, Priority: 1},
		{URL: "http://open", Concurrency: 2, Priority: 1},
	}
	free := func(ep Endpoint) int {
		if ep.URL == "http://full" {
			return 0
		}
		return 2
	}
	for start := range 4 {
		got := countJobs(allocateJobsWith(eps, jobSet(whisperBatchSize()), free, start))
		if got[0] != 0 || got[1] != whisperBatchSize() {
			t.Fatalf("start=%d: full endpoint got work: %v", start, got)
		}
	}
}

// Priority still orders tiers: a lower number fills first, and the next tier
// only gets what the first tier's free slots cannot take.
func TestAllocateJobs_PriorityTiersSpillOnlyWhenSaturated(t *testing.T) {
	eps := []Endpoint{
		{URL: "http://gpu", Concurrency: 1, Priority: 1},
		{URL: "http://cpu", Concurrency: 1, Priority: 100},
	}
	free := func(ep Endpoint) int { return ep.Concurrency }
	b := whisperBatchSize()
	if got := countJobs(allocateJobsWith(eps, jobSet(b), free, 0)); got[0] != b || got[1] != 0 {
		t.Fatalf("one batch must stay on the preferred tier: %v", got)
	}
	if got := countJobs(allocateJobsWith(eps, jobSet(2*b), free, 0)); got[0] != b || got[1] != b {
		t.Fatalf("a second batch must spill once the preferred slot is taken: %v", got)
	}
}

// Overflow beyond every free slot still gets assigned (queued on slots), and
// every job is assigned exactly once.
func TestAllocateJobs_OverflowAssignsEveryJobOnce(t *testing.T) {
	eps := []Endpoint{
		{URL: "http://a", Concurrency: 1, Priority: 1},
		{URL: "http://b", Concurrency: 1, Priority: 1},
	}
	free := func(Endpoint) int { return 0 }
	n := 5 * whisperBatchSize()
	a := allocateJobsWith(eps, jobSet(n), free, 0)
	seen := map[string]int{}
	for _, m := range a {
		for id := range m {
			seen[id]++
		}
	}
	if len(seen) != n {
		t.Fatalf("assigned %d of %d jobs", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("job %s assigned %d times", id, c)
		}
	}
}

// Under saturation (no free slot anywhere) a small batch still rotates across
// the equal-priority endpoints of the preferred tier instead of always queueing
// on the first one, and never spills to a lower tier.
func TestAllocateJobs_SaturatedPoolRotatesWithinThePreferredTier(t *testing.T) {
	eps := []Endpoint{
		{URL: "http://a", Concurrency: 2, Priority: 1},
		{URL: "http://b", Concurrency: 2, Priority: 1},
		{URL: "http://c", Concurrency: 2, Priority: 1},
		{URL: "http://slow", Concurrency: 2, Priority: 100},
	}
	free := func(Endpoint) int { return 0 }
	hits := make([]int, len(eps))
	for start := range 9 {
		got := countJobs(allocateJobsWith(eps, jobSet(whisperBatchSize()), free, start))
		for i, n := range got {
			hits[i] += n
		}
	}
	for i := range 3 {
		if hits[i] == 0 {
			t.Fatalf("preferred endpoint %d never received a saturated batch: %v", i, hits)
		}
	}
	if hits[3] != 0 {
		t.Fatalf("a saturated single batch spilled to the lower tier: %v", hits)
	}
	// Weighted queueing still holds: a big overflow gives each endpoint its
	// Concurrency x batch share per pass.
	got := countJobs(allocateJobsWith(eps, jobSet(6*whisperBatchSize()), free, 1))
	if got[0] != 2*whisperBatchSize() || got[1] != 2*whisperBatchSize() || got[2] != 2*whisperBatchSize() || got[3] != 0 {
		t.Fatalf("overflow pass shares: %v", got)
	}
}

func countingWhisper(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ok"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// End to end through the pool: many small calls (one batch each, the shape the
// file op produces) reach BOTH endpoints.
func TestPool_SmallBatchesSpreadAcrossEndpoints(t *testing.T) {
	resetInFlightState()
	var aHits, bHits atomic.Int64
	a, b := countingWhisper(t, &aHits), countingWhisper(t, &bHits)
	eps := []Endpoint{
		{URL: a.URL, Concurrency: 2, Label: "a", Priority: 1},
		{URL: b.URL, Concurrency: 2, Label: "b", Priority: 1},
	}
	for range 8 {
		jobs := tempWAVJobs(t, 2)
		res, err := transcribePool(context.Background(), eps, nil, jobs, nil, nil)
		if err != nil || len(res) != len(jobs) {
			t.Fatalf("call failed: %d results, err=%v", len(res), err)
		}
	}
	if aHits.Load() == 0 || bHits.Load() == 0 {
		t.Fatalf("work did not spread: endpoint a=%d b=%d requests", aHits.Load(), bHits.Load())
	}
}

// An endpoint whose slots are all held is skipped by the pool, not queued
// behind, while another endpoint is idle.
func TestPool_FullEndpointIsSkipped(t *testing.T) {
	resetInFlightState()
	var aHits, bHits atomic.Int64
	a, b := countingWhisper(t, &aHits), countingWhisper(t, &bHits)
	eps := []Endpoint{
		{URL: a.URL, Concurrency: 1, Label: "a", Priority: 1},
		{URL: b.URL, Concurrency: 1, Label: "b", Priority: 1},
	}
	release, err := acquireInFlight(context.Background(), a.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for range 4 {
		jobs := tempWAVJobs(t, 2)
		if _, err := transcribePool(context.Background(), eps, nil, jobs, nil, nil); err != nil {
			t.Fatalf("call failed: %v", err)
		}
	}
	if aHits.Load() != 0 || bHits.Load() == 0 {
		t.Fatalf("a's slot was held; expected all work on b, got a=%d b=%d", aHits.Load(), bHits.Load())
	}
}
