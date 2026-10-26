// file: internal/transcribe/stuck_dispatch_test.go
// version: 1.0.1
// guid: 7e3a91c5-2b64-4d08-a5f7-1c9d0e8b6a32
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func sortedIDs[J any](sub map[string]J) []string {
	return slices.Sorted(func(yield func(string) bool) {
		for id := range sub {
			if !yield(id) {
				return
			}
		}
	})
}

func okResults[J any](sub map[string]J) map[string]BatchResult {
	out := make(map[string]BatchResult, len(sub))
	for id := range sub {
		out[id] = BatchResult{Text: "T:" + id}
	}
	return out
}

// ── S4: stuck vs outage must not depend on which result arrives last ────────

func TestPoolFailureClassifiesOnTheAggregateNotTheLastResult(t *testing.T) {
	stuckA := fmt.Errorf("http://192.0.2.1: %w", &WorkerStuckError{URL: "http://192.0.2.1", After: time.Minute, Jobs: []string{"a"}})
	stuckB := fmt.Errorf("http://192.0.2.2: %w", &WorkerStuckError{URL: "http://192.0.2.2", After: time.Minute, Jobs: []string{"b"}})
	outage := fmt.Errorf("http://192.0.2.3: %w", errors.New("dial tcp: connection refused"))
	fallback := errors.New("fallback")

	t.Run("every failing endpoint stuck is a stuck pool", func(t *testing.T) {
		err := poolFailure(map[string]error{"http://192.0.2.1": stuckA, "http://192.0.2.2": stuckB}, fallback)
		if !IsWorkerStuck(err) {
			t.Fatalf("want stuck, got %v", err)
		}
		if !strings.Contains(err.Error(), "192.0.2.1") || !strings.Contains(err.Error(), "192.0.2.2") {
			t.Errorf("both endpoints must be named: %v", err)
		}
	})
	t.Run("one outage among stuck endpoints makes the whole failure an outage", func(t *testing.T) {
		err := poolFailure(map[string]error{"http://192.0.2.1": stuckA, "http://192.0.2.3": outage}, fallback)
		if IsWorkerStuck(err) {
			t.Fatalf("a partial outage read as a stuck pool: %v", err)
		}
		if !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "192.0.2.1") {
			t.Errorf("both causes must be listed: %v", err)
		}
	})
	t.Run("no recorded failure is the fallback", func(t *testing.T) {
		if got := poolFailure(nil, fallback); got != fallback {
			t.Fatalf("got %v", got)
		}
	})
}

func TestDispatchClassificationIsIndependentOfResultArrivalOrder(t *testing.T) {
	for _, tc := range []struct {
		name               string
		stuckDelay, outDly time.Duration
	}{
		{"stuck endpoint answers last", 80 * time.Millisecond, 0},
		{"outage endpoint answers last", 0, 80 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSplitHarness(t, 2, aliveHealth)
			send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
				if g.Endpoint.URL == h.eps[0].URL {
					time.Sleep(tc.stuckDelay)
					return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: sortedIDs(sub)}
				}
				time.Sleep(tc.outDly)
				return nil, errors.New("connection reset by peer")
			}
			useEndpoints(t, 1, endpointURLs(h.eps)...)
			_, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
			if err == nil || !IsTransport(err) {
				t.Fatalf("err = %v, want a transport error", err)
			}
			if IsWorkerStuck(err) {
				t.Errorf("one endpoint was merely down, yet the pool is reported stuck (order-dependent): %v", err)
			}
			if !strings.Contains(err.Error(), h.eps[0].URL) || !strings.Contains(err.Error(), h.eps[1].URL) {
				t.Errorf("both endpoints must be named: %v", err)
			}
		})
	}
}

// ── S5: an outage mid-request is not "stuck" ────────────────────────────────

func TestUnansweredRequestFromAWorkerThatVanishedIsAnOutageNotStuck(t *testing.T) {
	t.Run("single endpoint", func(t *testing.T) {
		h := newSplitHarness(t, 1, aliveHealth)
		send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
			h.srvs[0].Close() // the host went away after accepting the request
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		useEndpoints(t, 4, endpointURLs(h.eps)...)
		_, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
		if !IsTransport(err) {
			t.Fatalf("err = %v", err)
		}
		if IsWorkerStuck(err) {
			t.Fatalf("a worker that stopped answering /health was reported stuck: %v", err)
		}
		if !strings.Contains(err.Error(), "no longer answers /health") {
			t.Errorf("the message should say why: %v", err)
		}
	})
	t.Run("pool: no split, the jobs are simply re-queued to the survivor", func(t *testing.T) {
		h := newSplitHarness(t, 2, aliveHealth)
		var mu sync.Mutex
		var sends []sendRecord
		send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
			mu.Lock()
			sends = append(sends, sendRecord{g.Endpoint.URL, sortedIDs(sub)})
			mu.Unlock()
			if g.Endpoint.URL == h.eps[0].URL {
				h.srvs[0].Close()
				return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
			}
			return okResults(sub), nil
		}
		useEndpoints(t, 2, endpointURLs(h.eps)...)
		jobs := map[string]string{"j0": "x", "j1": "x", "j2": "x", "j3": "x"}
		res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, send)
		if err != nil || len(res) != 4 {
			t.Fatalf("res=%d err=%v", len(res), err)
		}
		for id, r := range res {
			if r.Error != "" {
				t.Errorf("%s was condemned by an outage: %+v", id, r)
			}
		}
		// Whole request re-sent, never split into halves.
		for _, s := range sends {
			if s.url == h.eps[1].URL && len(s.ids) == 1 {
				t.Errorf("an outage must not split the request, but %v was sent alone", s.ids)
			}
		}
	})
}

// ── N1: the op's own cancellation is not evidence about a worker ────────────

func TestCancelledOpDoesNotBenchTheEndpoint(t *testing.T) {
	h := newSplitHarness(t, 2, aliveHealth)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		cancel()
		return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
	}
	useEndpoints(t, 1, endpointURLs(h.eps)...)
	_, err := dispatchPool(ctx, h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, ep := range h.eps {
		if endpointInCooldown(ep.URL) {
			t.Errorf("%s was benched because the OP was cancelled; the cooldown outlives the op", ep.URL)
		}
	}
}

// ── S6: partial results survive a dispatch that ends in error ───────────────

func TestDispatchReturnsSettledResultsWithTheError(t *testing.T) {
	t.Run("single endpoint", func(t *testing.T) {
		h := newSplitHarness(t, 1, aliveHealth)
		send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
			return map[string]BatchResult{"j0": {Text: "done"}}, errors.New("connection reset by peer")
		}
		useEndpoints(t, 4, endpointURLs(h.eps)...)
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
		if err == nil || res["j0"].Text != "done" {
			t.Fatalf("res=%v err=%v: the finished job must come back with the error", res, err)
		}
	})
	t.Run("pool exhausted", func(t *testing.T) {
		h := newSplitHarness(t, 2, aliveHealth)
		send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
			first := sortedIDs(sub)[0]
			return map[string]BatchResult{first: {Text: "done-" + first}}, errors.New("connection reset by peer")
		}
		useEndpoints(t, 2, endpointURLs(h.eps)...)
		jobs := map[string]string{"j0": "x", "j1": "x", "j2": "x", "j3": "x"}
		res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, send)
		if err == nil || !IsTransport(err) {
			t.Fatalf("err = %v", err)
		}
		if res["j0"].Text != "done-j0" || res["j2"].Text != "done-j2" {
			t.Errorf("results settled before the pool ran dry were thrown away: %v", res)
		}
	})
	t.Run("a condemned verdict is not lost when the pool then runs out", func(t *testing.T) {
		h := wedgedSplitHarness(t, 2, "j1")
		send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, p ProgressFunc) (map[string]BatchResult, error) {
			if slices.Contains(sortedIDs(sub), "j9") {
				return nil, errors.New("connection reset by peer") // j9's host is down
			}
			return h.send(ctx, g, sub, p)
		}
		useEndpoints(t, 1, endpointURLs(h.eps)...)
		// j0 and j1 run first (one chunk each); j1 is the culprit and gets
		// condemned while j9 can only fail.
		res, _ := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
		if PathErrorCode(res["j1"].Error) != PathErrTranscribeTimeout {
			t.Fatalf("j1 = %+v, want its transcribe_timeout verdict", res["j1"])
		}
	})
}

func TestPathChunksKeepEarlierChunksWhenTheOpIsCancelledBetweenThem(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 2, w.srv.URL)
	config.Mutate(func(c *config.Config) { c.WhisperBatchSleepMS = 10000 })
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	res, err := transcribePathChunks(ctx, w.srv.URL, remoteHealth{}, 1, pathJobs(t, 4), nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 2 {
		t.Errorf("the chunk that finished before the cancel must be returned, got %d result(s)", len(res))
	}
}

func TestPathPoolReturnsChunksDoneBeforeAStuckOne(t *testing.T) {
	shortenRequestTimeout(t, 300*time.Millisecond)
	w := hangingPathWorker(t, pathHealth(nil), "book-c")
	useEndpoints(t, 2, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 4), BatchOptions{})
	if err == nil || !IsWorkerStuck(err) {
		t.Fatalf("err = %v, want stuck", err)
	}
	if res["book-a"].Text == "" || res["book-b"].Text == "" {
		t.Errorf("the first chunk completed and must come back with the error: %v", res)
	}
}

func TestUploadErrorKeepsJournalServedResults(t *testing.T) {
	var fail atomic.Bool
	srv, calls := batchServer(t, map[string]any{"status": "ok", "model": "M", "device": "metal"})
	_ = calls
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transcribe-batch" && fail.Load() {
			http.Error(rw, "down", http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(rw, r)
	})
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	journal := newMapJournal()
	yes := true
	h := remoteHealth{BatchPipeline: &yes, Model: "M"}
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, true, 1,
		map[string]string{"book-a": write("a.wav", "RIFF-a")}, nil, journal); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	res, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, true, 1,
		map[string]string{"book-a": write("a2.wav", "RIFF-a"), "book-b": write("b.wav", "RIFF-b")}, nil, journal)
	if err == nil {
		t.Fatal("want the batch error")
	}
	if res["book-a"].Text == "" {
		t.Errorf("the journal-served result was dropped on the error path: %v", res)
	}
}

// ── N9: followers share their leader's fate ─────────────────────────────────

func TestStuckErrorCarriesJournalFollowers(t *testing.T) {
	shortenRequestTimeout(t, 250*time.Millisecond)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transcribe-batch", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	dir := t.TempDir()
	jobs := map[string]string{}
	for _, id := range []string{"book-a", "book-b"} { // identical bytes
		p := filepath.Join(dir, id+".wav")
		if err := os.WriteFile(p, []byte("RIFF-identical"), 0o644); err != nil {
			t.Fatal(err)
		}
		jobs[id] = p
	}
	yes := true
	_, err := transcribeRemoteWithHealth(context.Background(), srv.URL, remoteHealth{BatchPipeline: &yes, Model: "M"}, true, 1, jobs, nil, newMapJournal())
	se, ok := errors.AsType[*WorkerStuckError](err)
	if !ok {
		t.Fatalf("err = %v, want stuck", err)
	}
	if !slices.Equal(se.Jobs, []string{"book-a"}) || !slices.Equal(se.Followers["book-a"], []string{"book-b"}) {
		t.Fatalf("jobs=%v followers=%v; the follower must be named so the dispatcher keeps it with its leader", se.Jobs, se.Followers)
	}
}

func TestFollowersAreCondemnedWithTheirLeaderAndNeverSentAlone(t *testing.T) {
	h := wedgedSplitHarness(t, 5)
	var mu sync.Mutex
	var sends []sendRecord
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		ids := sortedIDs(sub)
		mu.Lock()
		sends = append(sends, sendRecord{g.Endpoint.URL, ids})
		mu.Unlock()
		// j1b is byte-identical to j1: only j1 would really be sent.
		var leaders []string
		for _, id := range ids {
			if id != "j1b" {
				leaders = append(leaders, id)
			}
		}
		if slices.Contains(ids, "j1") {
			h.wedge(g.Endpoint.URL)
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: leaders,
				Followers: map[string][]string{"j1": {"j1b"}}}
		}
		return okResults(sub), nil
	}
	useEndpoints(t, 4, endpointURLs(h.eps)...)
	jobs := map[string]string{"j0": "x", "j1": "x", "j1b": "x", "j2": "x"}
	res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, send)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	for _, id := range []string{"j1", "j1b"} {
		if PathErrorCode(res[id].Error) != PathErrTranscribeTimeout {
			t.Errorf("%s = %+v, want the culprit's verdict (identical audio)", id, res[id])
		}
	}
	for _, id := range []string{"j0", "j2"} {
		if res[id].Text != "T:"+id {
			t.Errorf("innocent %s = %+v", id, res[id])
		}
	}
	for _, s := range sends {
		if slices.Contains(s.ids, "j1b") && !slices.Contains(s.ids, "j1") {
			t.Errorf("follower j1b was sent without its leader: %v", s)
		}
	}
}

func TestARecoveredLeaderIsRetriedWithItsFollowersNotWithoutThem(t *testing.T) {
	h := newSplitHarness(t, 3, aliveHealth)
	var mu sync.Mutex
	var sends []sendRecord
	stuckOnce := true
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		ids := sortedIDs(sub)
		mu.Lock()
		sends = append(sends, sendRecord{g.Endpoint.URL, ids})
		stuck := stuckOnce && slices.Contains(ids, "j1")
		if stuck {
			stuckOnce = false
		}
		mu.Unlock()
		if stuck {
			var leaders []string
			for _, id := range ids {
				if id != "j1b" {
					leaders = append(leaders, id)
				}
			}
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: leaders,
				Followers: map[string][]string{"j1": {"j1b"}}}
		}
		return okResults(sub), nil // a real sender fans the leader's result out to j1b
	}
	useEndpoints(t, 4, endpointURLs(h.eps)...)
	jobs := map[string]string{"j0": "x", "j1": "x", "j1b": "x", "j2": "x"}
	res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, send)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	for id, r := range res {
		if r.Error != "" {
			t.Errorf("%s = %+v: a transient stall must not condemn anything", id, r)
		}
	}
	for _, s := range sends {
		if slices.Contains(s.ids, "j1b") && !slices.Contains(s.ids, "j1") {
			t.Errorf("follower j1b was sent without its leader: %v (it would be a second, unrelated probe of the same audio)", s)
		}
	}
}

// ── S9: free jobs keep the other endpoints busy while a part runs ───────────

func TestFreeJobsRunOnIdleEndpointsWhileASplitPartRuns(t *testing.T) {
	h := newSplitHarness(t, 4, aliveHealth)
	var mu sync.Mutex
	called := map[string]bool{}
	freeSent := make(chan struct{})
	var freeOnce sync.Once
	var servedWhilePartRan atomic.Bool
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		ids := sortedIDs(sub)
		mu.Lock()
		first := !called[g.Endpoint.URL]
		called[g.Endpoint.URL] = true
		mu.Unlock()
		switch {
		case first && g.Endpoint.URL == h.eps[0].URL: // stuck on [j00 j01]
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: ids}
		case first && g.Endpoint.URL == h.eps[1].URL: // outage on [j02 j03]: they become free jobs
			return nil, errors.New("connection reset by peer")
		case slices.Equal(ids, []string{"j02", "j03"}):
			freeOnce.Do(func() { close(freeSent) })
			return okResults(sub), nil
		case len(ids) == 1 && (ids[0] == "j00" || ids[0] == "j01"):
			// A split part that "runs for a long time": the free jobs must be
			// served by the idle endpoint meanwhile, not after this returns.
			select {
			case <-freeSent:
				servedWhilePartRan.Store(true)
			case <-time.After(3 * time.Second):
			}
		}
		return okResults(sub), nil
	}
	useEndpoints(t, 2, endpointURLs(h.eps)...)
	jobs := map[string]string{}
	for i := range 6 {
		jobs[fmt.Sprintf("j%02d", i)] = "x"
	}
	res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, send)
	if err != nil || len(res) != 6 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	if !servedWhilePartRan.Load() {
		t.Error("the free jobs were not sent while a split part was running; the idle endpoint sat unused")
	}
}

// NN5. A leader that sat in a LATER chunk of a call whose earlier chunk got stuck
// was never in a stuck request, but its followers are known (the error carries
// the whole call's). When it runs as free work it must carry its follower with
// it, or it succeeds alone, nothing fans its answer out, and the follower is
// decoded all over again next round.
func TestAFreeLeaderFromALaterChunkIsSentWithItsFollowers(t *testing.T) {
	h := newSplitHarness(t, 2, aliveHealth)
	var mu sync.Mutex
	var sends []sendRecord
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		ids := sortedIDs(sub)
		mu.Lock()
		sends = append(sends, sendRecord{g.Endpoint.URL, ids})
		mu.Unlock()
		if slices.Contains(ids, "a") && len(ids) > 1 {
			// The first chunk of the call (just "a") hangs; "b" (with its
			// follower "b2") and "c" were in later chunks and were never sent.
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: []string{"a"},
				Followers: map[string][]string{"b": {"b2"}}}
		}
		return okResults(sub), nil
	}
	useEndpoints(t, 4, endpointURLs(h.eps)...)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"a": "x", "b": "x", "b2": "x", "c": "x"}, poolOptions{}, send)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	for _, s := range sends {
		if slices.Contains(s.ids, "b") != slices.Contains(s.ids, "b2") {
			t.Errorf("leader b and follower b2 were split across requests: %v (all sends: %v)", s, sends)
		}
	}
}
