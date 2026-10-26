// file: internal/transcribe/gate_unreachable_test.go
// version: 1.0.0
// guid: 30adab37-a67c-4906-90cb-278f46f4a751
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// R1 (review of #4182): on a pool whose endpoints all set require_gpu (prod),
// an endpoint whose /health cannot be read is refused at the gate. When every
// endpoint is unreachable that refusal must be paced and end as
// ErrPoolUnavailable (an outage the ops wait out and count), not an
// unrecognised error that defers the page in under a millisecond.

// requireGPU turns require_gpu on for every configured endpoint.
func requireGPU(t *testing.T) {
	t.Helper()
	config.Mutate(func(c *config.Config) {
		eps := append([]config.WhisperEndpoint(nil), c.WhisperEndpoints...)
		for i := range eps {
			eps[i].RequireGPU = true
		}
		c.WhisperEndpoints = eps
	})
}

// deadURL is an endpoint that refuses connections at once.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func TestGate_AnUnreachableRequireGPUPoolIsPacedThenUnavailable(t *testing.T) {
	const paceMax = 150 * time.Millisecond
	shortenPacing(t, 10*time.Millisecond, paceMax)
	useEndpoints(t, 4, deadURL(t), deadURL(t))
	requireGPU(t)

	start := time.Now()
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	if !IsPoolUnavailable(err) || !IsTransport(err) {
		t.Fatalf("an unreachable require_gpu pool must end as ErrPoolUnavailable (a transport error), got %v", err)
	}
	if elapsed := time.Since(start); elapsed < paceMax {
		t.Fatalf("the dispatch gave up after %s, before the %s pacing allowance", elapsed, paceMax)
	}
}

func TestGate_ARequireGPUPoolThatComesBackIsWaitedFor(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 2*time.Second)
	var probes atomic.Int64
	w := newPathWorker(t, nil)
	inner := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			if probes.Add(1) <= 2 {
				// The tunnel is down: nothing usable answers.
				rw.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(rw).Encode(pathHealth(nil))
			return
		}
		inner.ServeHTTP(rw, r)
	})
	useEndpoints(t, 4, w.srv.URL)
	requireGPU(t)

	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	if err != nil || len(res) != 2 {
		t.Fatalf("res=%d err=%v, want the dispatch to wait for /health to come back and send", len(res), err)
	}
	if probes.Load() < 3 {
		t.Errorf("probes = %d, want the gate to have asked again", probes.Load())
	}
}

// The control: a pool that answers /health and is shown to lack the required
// label is a configuration problem, refused at once, never waited for.
func TestGate_AReachablePoolWithoutTheRequiredLabelIsRefusedAtOnce(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 5*time.Second)
	w := newPathWorker(t, pathHealth(map[string]any{"device": "cpu"}))
	useEndpoints(t, 4, w.srv.URL)
	requireGPU(t)

	start := time.Now()
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{})
	if err == nil || IsPoolUnavailable(err) {
		t.Fatalf("a CPU-only pool under require_gpu must be refused, not reported unavailable: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a capability refusal waited %s; it must fail at once", elapsed)
	}
}
