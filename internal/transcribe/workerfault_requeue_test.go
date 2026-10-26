// file: internal/transcribe/workerfault_requeue_test.go
// version: 1.3.0
// guid: f07c4f8d-09cb-4988-bf56-25da7f68d1e9
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// B1 (review of #4176): a worker-fault response was re-queued with no memory, so
// a set of genuine loopers that is most of what is left in a dispatch faulted on
// every response that carried it, and two endpoints traded the same jobs until
// the op was cancelled. These tests run the dispatcher with real batch sizes
// (whisper_batch_size 16, so one request carries every job, as in production).

// shortenPacing makes a paced dispatch wait milliseconds, not minutes.
func shortenPacing(t *testing.T, interval, maxWait time.Duration) {
	t.Helper()
	oi, om := poolPaceInterval, poolPaceMax
	poolPaceInterval, poolPaceMax = interval, maxWait
	t.Cleanup(func() { poolPaceInterval, poolPaceMax = oi, om })
}

// clearCooldown models a round that outlasts another endpoint's cooldown (a
// round of three 300 s clips is an hour; the cooldown is at most five minutes).
func clearCooldown(url string) {
	aidispatch.DefaultHealth().MarkSuccess(endpointIDForURL(url))
}

// faultPool is a pool of fake workers whose send behaves like the transports: a
// request is split into chunks of the batch size, each chunk is answered by
// answer(url, id), each response is judged on its own by splitWorkerFault, and
// the send stops at the first faulted chunk. Every send clears the cooldown of
// every OTHER endpoint (see clearCooldown).
type faultPool struct {
	h      *splitHarness
	answer func(url, id string) BatchResult
	batch  int

	mu    sync.Mutex
	sends []sendRecord
}

func newFaultPool(t *testing.T, workers, batch int, answer func(url, id string) BatchResult) *faultPool {
	t.Helper()
	p := &faultPool{h: newSplitHarness(t, workers, aliveHealth), answer: answer, batch: batch}
	useEndpoints(t, batch, endpointURLs(p.h.eps)...)
	return p
}

func (p *faultPool) send(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
	url := g.Endpoint.URL
	ids := sortedIDs(sub)
	p.mu.Lock()
	p.sends = append(p.sends, sendRecord{url, ids})
	p.mu.Unlock()
	for _, ep := range p.h.eps {
		if ep.URL != url {
			clearCooldown(ep.URL)
		}
	}
	out := map[string]BatchResult{}
	for chunk := range slices.Chunk(ids, p.batch) {
		res := make(map[string]BatchResult, len(chunk))
		for _, id := range chunk {
			res[id] = p.answer(url, id)
		}
		kept, fault := splitWorkerFault(url, res)
		for id, r := range kept {
			out[id] = r
		}
		if fault != nil {
			return out, fault
		}
	}
	return out, nil
}

func (p *faultPool) sendLog() []sendRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sends)
}

func (p *faultPool) run(t *testing.T, ids ...string) (map[string]BatchResult, error) {
	t.Helper()
	jobs := make(map[string]string, len(ids))
	for _, id := range ids {
		jobs[id] = "unused"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchPool(ctx, p.h.eps, nil, jobs, poolOptions{}, p.send)
	if ctx.Err() != nil {
		log := p.sendLog()
		t.Fatalf("the dispatch only ended because the test cancelled it, after %d sends (first: %v)", len(log), log[:min(len(log), 4)])
	}
	return res, err
}

// requireNoResendToAFaultedWorker fails if a job was sent again to an endpoint
// whose faulted response had held it, or if any job is still pending forever.
func (p *faultPool) faultedOnBy(t *testing.T, isFaulted func(url string, ids []string) bool) map[string]map[string]bool {
	t.Helper()
	seen := map[string]map[string]bool{}
	for _, s := range p.sendLog() {
		for _, id := range s.ids {
			if seen[id][s.url] {
				t.Errorf("%s was sent again to %s after that worker's faulted response held it", id, s.url)
			}
		}
		if isFaulted(s.url, s.ids) {
			for _, id := range s.ids {
				if seen[id] == nil {
					seen[id] = map[string]bool{}
				}
				seen[id][s.url] = true
			}
		}
	}
	return seen
}

func loopers(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

// isLooper answers a genuine looper: its clip runs out the decoder's deadline on
// every worker. Everything else transcribes.
func isLooper(_ string, id string) BatchResult {
	if strings.HasPrefix(id, "loop") {
		return BatchResult{Error: timeoutText}
	}
	return BatchResult{Text: "T:" + id}
}

// The reviewer's overlay, made real: three loopers alone in a dispatch on two
// endpoints. Before the fix the same three jobs were sent 72 times and the
// dispatch ended only when the test cancelled it.
func TestPool_LoopersAloneInADispatchTerminate(t *testing.T) {
	p := newFaultPool(t, 2, 16, isLooper)
	res, err := p.run(t, loopers(3, "loop")...)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sends := p.sendLog()
	if len(sends) > 3 {
		t.Errorf("sends = %d, want at most 3 (one faulted request, then the jobs in units too small to fault): %v", len(sends), sends)
	}
	faultedOn := map[string]string{}
	for _, s := range sends {
		if len(s.ids) >= timeoutBreakerMin {
			for _, id := range s.ids {
				faultedOn[id] = s.url
			}
		}
	}
	for _, id := range loopers(3, "loop") {
		r, ok := res[id]
		if !ok {
			t.Fatalf("%s has no result", id)
		}
		// Judged in a unit too small to fault, on any worker: a per-clip
		// verdict naming that worker, which the ops count as its strike.
		if PathErrorCode(r.Error) != PathErrTranscribeTimeout || r.Endpoint == "" {
			t.Errorf("%s = %+v, want a per-clip timeout verdict naming its worker", id, r)
		}
		if faultedOn[id] == "" {
			t.Errorf("%s was never in a faulted request: the scenario did not fault", id)
		}
	}
}

// The brief's case: two endpoints where every job keeps faulting (a worker-level
// code faults a response of any size). It must end, with every job deferred.
func TestPool_EveryResponseFaultingOnTwoEndpointsEndsWithDeferrals(t *testing.T) {
	for _, code := range []string{misconfText} {
		t.Run(PathErrorCode(code), func(t *testing.T) {
			p := newFaultPool(t, 2, 16, func(string, string) BatchResult { return BatchResult{Error: code} })
			ids := loopers(5, "job")
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v, want none: every job is accounted for as a deferral", err)
			}
			for _, id := range ids {
				if r := res[id]; PathErrorCode(r.Error) != PathErrWorkerFault || IsStrikeError(r.Error) {
					t.Errorf("%s = %+v, want a worker_fault deferral", id, r)
				}
			}
			p.faultedOnBy(t, func(string, []string) bool { return true })
			if n := len(p.sendLog()); n > 4 {
				t.Errorf("sends = %d, want a handful: %v", n, p.sendLog())
			}
		})
	}
}

// The production triggers, path mode on four workers with batch size 16.
func TestPool_ProductionFaultShapesConvergeOnPerClipVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		// The book op's retry 1: three music-intro books whose 300 s clips loop.
		{"three retry-1 loopers in one round", loopers(3, "loop")},
		// The per-file op's tier-2 batch: 8 chapter files of one book, 5 of them
		// opening on the same long theme.
		{"eight chapter files, five loopers", append(loopers(5, "loop"), loopers(3, "chap")...)},
		// The residue once the library is covered: a batch of nothing but hard files.
		{"leftover hard files", loopers(8, "loop")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFaultPool(t, 4, 16, isLooper)
			res, err := p.run(t, tc.ids...)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			for _, id := range tc.ids {
				r, ok := res[id]
				switch {
				case !ok:
					t.Errorf("%s has no result", id)
				case !strings.HasPrefix(id, "loop"):
					if r.Text != "T:"+id {
						t.Errorf("%s = %+v, want its transcript", id, r)
					}
				case PathErrorCode(r.Error) != PathErrTranscribeTimeout || r.Endpoint == "":
					// R2: every looper converges on a per-clip verdict naming its
					// worker; none is deferred.
					t.Errorf("%s = %+v, want a per-clip timeout verdict", id, r)
				}
			}
			if n := len(p.sendLog()); n > 2+len(tc.ids) {
				t.Errorf("sends = %d for %d jobs: %v", n, len(tc.ids), p.sendLog())
			}
		})
	}
}

// S2's "plainly sick" rule: a worker that could not decode at all earlier in
// this dispatch (a worker-level fault) is not believed about a lone timeout
// either. Worker 0 (priority 1) answers j0 worker_misconfigured and times out
// on everything else; worker 1 transcribes everything. Worker 0 gets j0-j2 plus
// j6 in round 1 and faults on the first chunk; in round 2 it is handed j6 alone.
// (A timeout-majority fault does not make a worker sick: see R2.)
func TestPool_ASickWorkersLoneTimeoutIsWithheldAndTheJobGoesElsewhere(t *testing.T) {
	var p *faultPool
	p = newFaultPool(t, 2, 3, func(url, id string) BatchResult {
		if url == p.h.eps[0].URL {
			if id == "j0" {
				return BatchResult{Error: misconfText}
			}
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	ids := loopers(7, "j")
	res, err := p.run(t, ids...)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sick := p.h.eps[0].URL
	loneOnSick := false
	for _, s := range p.sendLog() {
		if s.url == sick && len(s.ids) < timeoutBreakerMin {
			loneOnSick = true
		}
	}
	if !loneOnSick {
		t.Fatalf("the scenario did not put a small request on the sick worker: %v", p.sendLog())
	}
	for _, id := range ids {
		if r := res[id]; r.Text != "T:"+id || r.Endpoint != p.h.eps[1].URL {
			t.Errorf("%s = %+v, want the sound worker's transcript (the sick worker's timeouts are no verdict)", id, r)
		}
	}
}

// S4: every endpoint benched by an EARLIER dispatch. The dispatch waits for one
// to come back instead of failing at once (which deferred page after page).
func TestPool_EveryEndpointBenchedByAnEarlierDispatchIsWaitedOut(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 2*time.Second)
	h := newSplitHarness(t, 2, aliveHealth)
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	for _, ep := range h.eps {
		markEndpointFailure(ep.URL)
	}
	var sends atomic.Int64
	send := func(_ context.Context, _ gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		sends.Add(1)
		return okResults(sub), nil
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		clearCooldown(h.eps[1].URL) // the bench ends
	}()
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"a": "x", "b": "x"}, poolOptions{}, send)
	if err != nil || len(res) != 2 || sends.Load() == 0 {
		t.Fatalf("res=%d sends=%d err=%v, want the dispatch to wait for the bench to end and then send", len(res), sends.Load(), err)
	}
}

func TestPool_ABenchThatOutlastsThePacingAllowanceIsPoolUnavailable(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 60*time.Millisecond)
	h := newSplitHarness(t, 2, aliveHealth)
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	for _, ep := range h.eps {
		markEndpointFailure(ep.URL)
	}
	send := func(context.Context, gatedEndpoint, map[string]string, ProgressFunc) (map[string]BatchResult, error) {
		t.Fatal("nothing may be sent to a benched pool")
		return nil, nil
	}
	_, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"a": "x"}, poolOptions{}, send)
	if !IsPoolUnavailable(err) || !IsTransport(err) || IsWorkerStuck(err) {
		t.Fatalf("err = %v, want a pool-unavailable transport error", err)
	}
}

// N4: a single worker whose decoder is restarting is asked again shortly, rather
// than every batch being deferred for the worker's whole fault window.
func TestGate_ASingleWorkerWhoseDecoderIsRestartingIsWaitedFor(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 2*time.Second)
	var probes atomic.Int64
	w := newPathWorker(t, nil)
	inner := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			extra := map[string]any{}
			if probes.Add(1) <= 2 {
				extra["child_start_error"] = "inference process not ready after 300s"
			}
			_ = json.NewEncoder(rw).Encode(pathHealth(extra))
			return
		}
		inner.ServeHTTP(rw, r)
	})
	useEndpoints(t, 4, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	if err != nil || len(res) != 2 {
		t.Fatalf("res=%d err=%v, want the dispatch to wait out the decoder restart and send", len(res), err)
	}
	if probes.Load() < 3 {
		t.Errorf("probes = %d, want the gate to have asked again", probes.Load())
	}
}

// N1: the deadline header is rounded UP, so the worker (whose clock starts later)
// never gives up on a request before the client does.
func TestRequestDeadlineHeaderRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		timeout time.Duration
		want    string
	}{
		{840 * time.Second, "840"},
		{840*time.Second + 1, "841"},
		{719*time.Second + 400*time.Millisecond, "720"},
	} {
		req, _ := http.NewRequest(http.MethodPost, "http://192.0.2.1:8000/transcribe-batch", nil)
		setRequestDeadlineHeader(req, &http.Client{Timeout: tc.timeout})
		if got := req.Header.Get(requestDeadlineHeader); got != tc.want {
			t.Errorf("timeout %s: header = %q, want %q", tc.timeout, got, tc.want)
		}
	}
}

// N1: a request_abandoned answer, should a client still read one, is no answer.
func TestIsNoAnswer(t *testing.T) {
	for text, want := range map[string]bool{
		"request_abandoned: the client's request timeout passed before this clip started": true,
		"decoder_killed: the decode process was killed from outside (exit code -9)":       false,
		misconfText:               true,
		"worker_fault: no answer": true,
		timeoutText:               false,
		crashText:                 false,
		"whisper_error: boom":     false,
		"":                        false,
	} {
		if got := IsNoAnswer(text); got != want {
			t.Errorf("IsNoAnswer(%q) = %v, want %v", text, got, want)
		}
	}
}

// S2 rests on this: a strike from an unnamed worker never counts as a second
// strike, so every answer the dispatcher hands an op must name the worker that
// gave it, on the one-worker path (dispatchSingle) and the pool alike. Only a
// no-answer marker the dispatcher made itself may be unnamed.
func TestPool_EveryAnswerNamesTheWorkerThatGaveIt(t *testing.T) {
	for _, workers := range []int{1, 4} {
		t.Run(fmt.Sprintf("%d workers", workers), func(t *testing.T) {
			p := newFaultPool(t, workers, 16, isLooper)
			ids := append(loopers(2, "loop"), loopers(14, "ok")...) // a minority of timeouts: verdicts
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			urls := map[string]bool{}
			for _, ep := range p.h.eps {
				urls[ep.URL] = true
			}
			for _, id := range ids {
				r, ok := res[id]
				if !ok {
					t.Fatalf("%s has no result", id)
				}
				if IsNoAnswer(r.Error) {
					continue
				}
				if !urls[r.Endpoint] {
					t.Errorf("%s = %+v: the answer does not name the worker that gave it", id, r)
				}
			}
			if r := res["loop0"]; !IsStrikeError(r.Error) {
				t.Fatalf("the scenario must produce a strike-type verdict, got %+v", r)
			}
		})
	}
}

// R2 (review of #4182): a page whose chunks are mostly genuine loopers faults on
// EVERY endpoint in the first round. Its loopers must still converge on per-clip
// verdicts (strikes the store can count), not be deferred on every run because
// no endpoint was left that had not faulted.
func TestPool_ALooperPageThatFaultsEveryEndpointConvergesOnPerClipVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workers, loop int
		ok            int
	}{
		{"six workers, 96 loopers", 6, 96, 0},
		{"a 200-job mixed page", 6, 120, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFaultPool(t, tc.workers, 16, isLooper)
			// Two loopers to every chapter, so every chunk is a looper majority.
			var ids []string
			for li, oi := 0, 0; li < tc.loop || oi < tc.ok; {
				for range 2 {
					if li < tc.loop {
						ids = append(ids, fmt.Sprintf("loop%03d", li))
						li++
					}
				}
				if oi < tc.ok {
					ids = append(ids, fmt.Sprintf("chap%03d", oi))
					oi++
				}
			}
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			faultedFirst := 0
			for _, s := range p.sendLog()[:min(tc.workers, len(p.sendLog()))] {
				if len(s.ids) >= timeoutBreakerMin {
					faultedFirst++
				}
			}
			if faultedFirst < tc.workers {
				t.Fatalf("the scenario must fault every endpoint in round 1: %d of %d", faultedFirst, tc.workers)
			}
			strikes := 0
			for _, id := range ids {
				r, ok := res[id]
				switch {
				case !ok:
					t.Fatalf("%s has no result", id)
				case strings.HasPrefix(id, "chap"):
					if r.Text != "T:"+id {
						t.Errorf("%s = %+v, want its transcript", id, r)
					}
				case PathErrorCode(r.Error) == PathErrTranscribeTimeout && r.Endpoint != "":
					strikes++
				default:
					t.Errorf("%s = %+v, want a per-clip timeout verdict naming its worker", id, r)
				}
			}
			if strikes != tc.loop {
				t.Fatalf("strikes = %d, want one per looper (%d)", strikes, tc.loop)
			}
		})
	}
}

// N2 (owner decision, review of #4182): decoder_killed is a strike-type answer.
// A lone kill is a per-clip verdict naming its worker (the store counts a second
// one only from a different worker), and a response of mostly kills is a
// timeout-majority fault re-sent in units too small to fault, on one worker as
// on many: it never ends the dispatch or defers the batch.
func TestPool_DecoderKilledIsAPerClipStrikeThatNeverDrainsTheBatch(t *testing.T) {
	const killed = "decoder_killed: the decode process was killed from outside (exit code -9)"
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d workers", workers), func(t *testing.T) {
			p := newFaultPool(t, workers, 16, func(_ string, id string) BatchResult {
				if strings.HasPrefix(id, "kill") {
					return BatchResult{Error: killed}
				}
				return BatchResult{Text: "T:" + id}
			})
			ids := append(loopers(5, "kill"), loopers(3, "chap")...)
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			for _, id := range ids {
				r := res[id]
				if strings.HasPrefix(id, "chap") {
					if r.Text != "T:"+id {
						t.Errorf("%s = %+v, want its transcript", id, r)
					}
					continue
				}
				if PathErrorCode(r.Error) != PathErrDecoderKilled || !IsStrikeError(r.Error) || IsNoAnswer(r.Error) || r.Endpoint == "" {
					t.Errorf("%s = %+v, want a per-clip decoder_killed strike naming its worker", id, r)
				}
			}
			if n := len(p.sendLog()); n > 1+len(ids) {
				t.Errorf("sends = %d for %d jobs: %v", n, len(ids), p.sendLog())
			}
		})
	}
}
