// file: internal/transcribe/stuck_test.go
// version: 1.0.0
// guid: 0b6f3d94-5a1e-4c78-9d02-7e3a8c5b1f46
// last-edited: 2026-10-25

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── request ceiling ──────────────────────────────────────────────────────────

func TestRequestTimeout(t *testing.T) {
	clips := func(n int, secs float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = secs
		}
		return out
	}
	worker := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4}
	cases := []struct {
		name string
		h    remoteHealth
		c    []float64
		want time.Duration
	}{
		{"worker reports nothing: historical ceiling", remoteHealth{}, clips(16, 90), 600 * time.Second},
		{"full chunk of 90s clips: sum of 360s limits + slack", worker, clips(16, 90), (16*360 + 60) * time.Second},
		{"short clips use the 60s floor", worker, clips(16, 5), (16*60 + 60) * time.Second},
		{"small request never drops below the historical ceiling", worker, clips(2, 5), 600 * time.Second},
		{"floor only", remoteHealth{ClipTimeoutFloorS: 100}, clips(10, 90), (10*100 + 60) * time.Second},
	}
	for _, tc := range cases {
		if got := requestTimeout(tc.h, tc.c); got != tc.want {
			t.Errorf("%s: requestTimeout = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestRequestTimeoutOutlastsTheWorkersOwnVerdict(t *testing.T) {
	// The invariant the formula exists for: whatever the worker spends on a
	// request (every clip hitting its deadline), the client is still waiting.
	h := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4}
	secs := []float64{90, 10, 300, 5, 90}
	var workerWorst time.Duration
	for _, s := range secs {
		workerWorst += time.Duration(max(h.ClipTimeoutFloorS, h.ClipTimeoutFactor*s) * float64(time.Second))
	}
	if got := requestTimeout(h, secs); got <= workerWorst {
		t.Fatalf("ceiling %s does not outlast the worker's worst case %s", got, workerWorst)
	}
}

// ── classification of a failed client call ──────────────────────────────────

func TestStuckFromHTTP(t *testing.T) {
	ids := []string{"a", "b"}
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, timeoutErr := client.Get(hang.URL)
	if timeoutErr == nil {
		t.Fatal("expected a client timeout")
	}

	t.Run("client timeout on an accepted request is stuck", func(t *testing.T) {
		got := stuckFromHTTP(context.Background(), timeoutErr, "http://w", time.Second, ids)
		if !IsWorkerStuck(got) {
			t.Fatalf("got %v, want a stuck error", got)
		}
		se, ok := errors.AsType[*WorkerStuckError](got)
		if !ok || !slices.Equal(se.Jobs, ids) || se.URL != "http://w" {
			t.Fatalf("stuck error = %+v", se)
		}
	})
	t.Run("a cancelled caller is not stuck", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := stuckFromHTTP(ctx, timeoutErr, "http://w", time.Second, ids); IsWorkerStuck(got) {
			t.Fatalf("operator cancellation read as a stuck worker: %v", got)
		}
	})
	t.Run("a dial timeout is an outage, not stuck", func(t *testing.T) {
		dialErr := &url.Error{Op: "Post", URL: "http://w", Err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutNetErr{}}}
		if got := stuckFromHTTP(context.Background(), dialErr, "http://w", time.Second, ids); IsWorkerStuck(got) {
			t.Fatalf("dial timeout read as a stuck worker: %v", got)
		}
	})
	t.Run("a refused connection is not stuck", func(t *testing.T) {
		_, err := http.Get(deadServerURL(t))
		if got := stuckFromHTTP(context.Background(), err, "http://w", time.Second, ids); IsWorkerStuck(got) {
			t.Fatalf("connection refused read as stuck: %v", got)
		}
	})
	t.Run("nil stays nil", func(t *testing.T) {
		if got := stuckFromHTTP(context.Background(), nil, "u", 0, nil); got != nil {
			t.Fatalf("got %v", got)
		}
	})
}

type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

func TestStuckTransportErrorSaysWorkerStuckNotUnreachable(t *testing.T) {
	err := classifyTransport([]string{"http://w"}, &WorkerStuckError{URL: "http://w", After: 10 * time.Minute, Jobs: []string{"a"}})
	if !IsTransport(err) || !IsWorkerStuck(err) {
		t.Fatalf("want a transport error that is stuck, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "worker stuck") || strings.Contains(msg, "unreachable") {
		t.Errorf("message should say stuck, not unreachable: %q", msg)
	}
}

// ── the gate ─────────────────────────────────────────────────────────────────

func f64(v float64) *float64 { return &v }

func TestStuckRefusal(t *testing.T) {
	g := func(busy, limit *float64, probed bool) gatedEndpoint {
		return gatedEndpoint{Probed: probed, Health: remoteHealth{BusySeconds: busy, BusyLimitSeconds: limit}}
	}
	cases := []struct {
		name    string
		g       gatedEndpoint
		refused bool
	}{
		{"idle current worker", g(f64(0), nil, true), false},
		{"inside its deadline", g(f64(50), f64(60), true), false},
		{"past the deadline but inside the grace", g(f64(80), f64(60), true), false},
		{"past deadline and grace", g(f64(500), f64(60), true), true},
		{"old worker reports nothing", g(nil, nil, true), false},
		{"unprobed endpoint is not judged", g(f64(500), f64(60), false), false},
	}
	for _, tc := range cases {
		if got := stuckRefusal(tc.g) != ""; got != tc.refused {
			t.Errorf("%s: refused=%v, want %v", tc.name, got, tc.refused)
		}
	}
}

func TestPoolRefusesAStuckWorkerWithoutSendingAndSaysStuck(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{"busy_seconds": 900.0, "busy_limit_seconds": 60.0}))
	useEndpoints(t, 0, w.srv.URL)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	requireTransport(t, err, "stuck")
	if !IsWorkerStuck(err) {
		t.Errorf("an all-stuck pool must be a stuck error, not an outage: %v", err)
	}
	if w.postCount() != 0 {
		t.Errorf("a stuck worker received %d request(s); it must receive none", w.postCount())
	}
}

func TestPoolRoutesAroundAStuckWorker(t *testing.T) {
	stuck := newPathWorker(t, pathHealth(map[string]any{"busy_seconds": 900.0, "busy_limit_seconds": 60.0}))
	ok := newPathWorker(t, pathHealth(map[string]any{"busy_seconds": 0.0, "busy_limit_seconds": nil}))
	useEndpoints(t, 0, stuck.srv.URL, ok.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	if err != nil || len(res) != 3 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	if stuck.postCount() != 0 || ok.postCount() == 0 {
		t.Errorf("stuck got %d requests, healthy got %d", stuck.postCount(), ok.postCount())
	}
}

func TestPoolDoesNotRefuseAnOldWorkerOrAnIdleOne(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"old worker, no busy fields": nil,
		"idle current worker":        {"busy_seconds": 0.0, "busy_limit_seconds": nil},
		"busy inside its deadline":   {"busy_seconds": 30.0, "busy_limit_seconds": 60.0},
	} {
		w := newPathWorker(t, pathHealth(extra))
		useEndpoints(t, 0, w.srv.URL)
		if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ── worker's per-file verdict passes through untouched ──────────────────────

func TestTranscribeByPathBatch_WorkerTimeoutVerdictIsAPerFileResultNotAnError(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	w.respond = func(key string, job map[string]any) map[string]any {
		if strings.Contains(job["path"].(string), "book-b") {
			return map[string]any{"text": "", "error": "transcribe_timeout: no result within 90s; the decode process was killed and restarted"}
		}
		return map[string]any{"text": "fine", "error": nil}
	}
	useEndpoints(t, 0, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	if err != nil {
		t.Fatalf("a per-file timeout verdict must not fail the batch: %v", err)
	}
	if got := PathErrorCode(res["book-b"].Error); got != PathErrTranscribeTimeout {
		t.Errorf("code = %q, want %q (error %q)", got, PathErrTranscribeTimeout, res["book-b"].Error)
	}
	if res["book-a"].Text != "fine" || res["book-c"].Text != "fine" || res["book-a"].Error != "" {
		t.Errorf("neighbours must be unaffected: %+v %+v", res["book-a"], res["book-c"])
	}
	if w.postCount() != 1 {
		t.Errorf("POSTs = %d; a per-file verdict must not trigger any re-send", w.postCount())
	}
}

// ── a request the worker never answers, over real HTTP ──────────────────────

// hangingPathWorker blocks every /transcribe-path-batch request whose jobs
// include a path containing hangOn until the test ends, answering the rest.
func hangingPathWorker(t *testing.T, health map[string]any, hangOn string) *pathWorker {
	t.Helper()
	w := newPathWorker(t, health)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	inner := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transcribe-path-batch" {
			body := new(strings.Builder)
			buf := make([]byte, 4096)
			for {
				n, err := r.Body.Read(buf)
				body.Write(buf[:n])
				if err != nil {
					break
				}
			}
			if strings.Contains(body.String(), hangOn) {
				select {
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			r.Body = nopCloser{strings.NewReader(body.String())}
		}
		inner.ServeHTTP(rw, r)
	})
	return w
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func shortenRequestTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := defaultRequestTimeout
	defaultRequestTimeout = d
	t.Cleanup(func() { defaultRequestTimeout = old })
}

func TestPathChunk_UnansweredRequestIsStuckAndKeepsEarlierChunks(t *testing.T) {
	shortenRequestTimeout(t, 300*time.Millisecond)
	w := hangingPathWorker(t, pathHealth(nil), "book-c")
	jobs := pathJobs(t, 4) // sorted: a, b | c, d at batch size 2
	useEndpoints(t, 2, w.srv.URL)

	res, err := transcribePathChunks(context.Background(), w.srv.URL, remoteHealth{}, 1, jobs, nil, nil, nil)
	if !IsWorkerStuck(err) {
		t.Fatalf("err = %v, want a stuck error", err)
	}
	se, _ := errors.AsType[*WorkerStuckError](err)
	if se == nil || !slices.Equal(slices.Sorted(slices.Values(se.Jobs)), []string{"book-c", "book-d"}) {
		t.Fatalf("stuck error must name exactly the unanswered chunk: %+v", se)
	}
	if len(res) != 2 || res["book-a"].Text == "" || res["book-b"].Text == "" {
		t.Errorf("the chunk that completed before the stuck one must be returned: %v", res)
	}
}

func TestSingleEndpointStuckIsATransportErrorNotAnOutageAndIsNotRetried(t *testing.T) {
	shortenRequestTimeout(t, 250*time.Millisecond)
	w := hangingPathWorker(t, pathHealth(nil), "book-")
	useEndpoints(t, 0, w.srv.URL)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	requireTransport(t, err, "worker stuck")
	if !IsWorkerStuck(err) {
		t.Fatalf("not classified stuck: %v", err)
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Errorf("a wedged-but-reachable worker was reported unreachable: %v", err)
	}
	if w.postCount() != 0 { // the hanging handler never records the request
		t.Errorf("unexpected recorded requests: %d", w.postCount())
	}
}

func TestSingleEndpointCancelledOpIsNotStuck(t *testing.T) {
	w := hangingPathWorker(t, pathHealth(nil), "book-")
	useEndpoints(t, 0, w.srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := TranscribeByPathBatch(ctx, pathJobs(t, 1), BatchOptions{})
	if err == nil || IsWorkerStuck(err) {
		t.Fatalf("an operator cancel must not read as a stuck worker: %v", err)
	}
}

func TestUploadPath_UnansweredRequestIsStuck(t *testing.T) {
	shortenRequestTimeout(t, 250*time.Millisecond)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"status": "ok", "batch_pipeline": true, "device": "metal", "busy_seconds": 0.0, "busy_limit_seconds": nil})
	})
	mux.HandleFunc("POST /transcribe-batch", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	// Cleanups run last-in first-out: release the hung handler BEFORE Close,
	// which otherwise waits on it forever.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	_, err := transcribePool(context.Background(), []Endpoint{{URL: srv.URL, Concurrency: 1}}, nil, tempWAVJobs(t, 2), nil, nil)
	requireTransport(t, err, "worker stuck")
	if !IsWorkerStuck(err) {
		t.Fatalf("upload path: not classified stuck: %v", err)
	}
}

// ── split-and-retry in the pool ─────────────────────────────────────────────

// splitHarness is N healthy fake workers (health only; sends are faked) plus a
// recording sender. A request containing the poison job is "stuck".
type splitHarness struct {
	mu     sync.Mutex
	sends  []sendRecord
	poison map[string]bool
	eps    []Endpoint
}

type sendRecord struct {
	url string
	ids []string
}

func newSplitHarness(t *testing.T, workers int, health map[string]any, poison ...string) *splitHarness {
	t.Helper()
	h := &splitHarness{poison: map[string]bool{}}
	for _, p := range poison {
		h.poison[p] = true
	}
	for i := range workers {
		hh := health
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(rw).Encode(hh)
		}))
		t.Cleanup(srv.Close)
		h.eps = append(h.eps, Endpoint{URL: srv.URL, Concurrency: 1, Priority: i + 1, Label: fmt.Sprintf("w%d", i)})
	}
	return h
}

func (h *splitHarness) send(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range sub {
			if !yield(id) {
				return
			}
		}
	})
	h.mu.Lock()
	h.sends = append(h.sends, sendRecord{g.Endpoint.URL, ids})
	h.mu.Unlock()
	for _, id := range ids {
		if h.poison[id] {
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: ids}
		}
	}
	out := make(map[string]BatchResult, len(ids))
	for _, id := range ids {
		out[id] = BatchResult{Text: "T:" + id}
	}
	return out, nil
}

func (h *splitHarness) run(t *testing.T, n int) (map[string]BatchResult, error) {
	t.Helper()
	useEndpoints(t, n, endpointURLs(h.eps)...) // one chunk holds every job
	jobs := map[string]string{}
	for i := range n {
		jobs[fmt.Sprintf("j%02d", i)] = "unused"
	}
	return dispatchPool(context.Background(), h.eps, nil, jobs, nil, nil, h.send)
}

var aliveHealth = map[string]any{"status": "ok", "device": "metal", "batch_pipeline": true, "busy_seconds": 0.0, "busy_limit_seconds": nil}

func TestSplitIsolatesTheStuckFileAndInnocentsComplete(t *testing.T) {
	h := newSplitHarness(t, 3, aliveHealth, "j05")
	res, err := h.run(t, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 8 {
		t.Fatalf("results = %d, want 8", len(res))
	}
	for id, r := range res {
		if id == "j05" {
			if PathErrorCode(r.Error) != PathErrTranscribeTimeout || r.Text != "" {
				t.Errorf("culprit must be a transcribe_timeout verdict, got %+v", r)
			}
			continue
		}
		if r.Error != "" || r.Text != "T:"+id {
			t.Errorf("innocent %s was condemned or lost: %+v", id, r)
		}
	}
	// The culprit was retried ALONE on two different workers before the verdict.
	solo := map[string]bool{}
	for _, s := range h.sends {
		if slices.Equal(s.ids, []string{"j05"}) {
			solo[s.url] = true
		}
	}
	if len(solo) != 2 {
		t.Errorf("culprit sent alone to %d distinct workers, want 2; sends=%v", len(solo), h.sends)
	}
}

func TestSplitCulpritIsNeverRetriedAloneOnAWorkerItWasAlreadyStuckOnAlone(t *testing.T) {
	h := newSplitHarness(t, 4, aliveHealth, "j03")
	if _, err := h.run(t, 8); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, s := range h.sends {
		if slices.Equal(s.ids, []string{"j03"}) {
			seen[s.url]++
		}
	}
	for url, n := range seen {
		if n > 1 {
			t.Errorf("j03 was sent alone to %s %d times", url, n)
		}
	}
}

func TestSplitBatchOfOneFileAmongManyDoesNotCondemnTheBatch(t *testing.T) {
	// 16 jobs, one poison: 15 innocents must come back as real transcripts.
	h := newSplitHarness(t, 4, aliveHealth, "j11")
	res, err := h.run(t, 16)
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, r := range res {
		if r.Error != "" {
			bad++
		}
	}
	if len(res) != 16 || bad != 1 {
		t.Errorf("results=%d errors=%d, want 16 results and exactly 1 error", len(res), bad)
	}
}

func TestSplitWedgedOldWorkersAreBenchedAndPoolExhaustionIsStuckNotOutage(t *testing.T) {
	// Old workers (no busy fields) hold their lock forever once wedged, so each
	// stuck worker is benched; with the poison in every part the pool runs out.
	h := newSplitHarness(t, 2, map[string]any{"status": "ok", "device": "metal", "batch_pipeline": true}, "j01")
	_, err := h.run(t, 4)
	if err == nil || !IsTransport(err) || !IsWorkerStuck(err) {
		t.Fatalf("want a transport error that is stuck, got %v", err)
	}
	perURL := map[string]int{}
	for _, s := range h.sends {
		perURL[s.url]++
	}
	if perURL[h.eps[0].URL] != 1 {
		t.Errorf("the wedged first worker was sent %d requests, want exactly 1 (it must be benched)", perURL[h.eps[0].URL])
	}
}

func TestSplitAliveWorkerThatMissedTheCeilingIsNotBenched(t *testing.T) {
	// Same stuck request, but the worker answers /health and is within its own
	// deadline: it must stay usable, or two workers could never narrow a batch
	// down to the file responsible.
	h := newSplitHarness(t, 2, aliveHealth, "j01")
	res, err := h.run(t, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if PathErrorCode(res["j01"].Error) != PathErrTranscribeTimeout {
		t.Errorf("j01 = %+v, want a transcribe_timeout verdict", res["j01"])
	}
	for _, id := range []string{"j00", "j02", "j03"} {
		if res[id].Text != "T:"+id {
			t.Errorf("%s = %+v", id, res[id])
		}
	}
}

func TestSingleEndpointStuckIsNotSplitOrRetried(t *testing.T) {
	h := newSplitHarness(t, 1, aliveHealth, "j01")
	_, err := h.run(t, 4)
	if !IsTransport(err) || !IsWorkerStuck(err) {
		t.Fatalf("err = %v", err)
	}
	if len(h.sends) != 1 {
		t.Errorf("single endpoint sent %d times; the historical behaviour is exactly one", len(h.sends))
	}
}

func TestPoolKeepsChunksThatCompletedBeforeANonStuckFailure(t *testing.T) {
	good := newSplitHarness(t, 2, aliveHealth)
	var mu sync.Mutex
	sent := map[string]int{}
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		mu.Lock()
		for id := range sub {
			sent[id]++
		}
		mu.Unlock()
		if g.Endpoint.URL != good.eps[0].URL {
			out := map[string]BatchResult{}
			for id := range sub {
				out[id] = BatchResult{Text: "ok"}
			}
			return out, nil
		}
		// First endpoint: completes the lowest id, then fails.
		ids := slices.Sorted(func(y func(string) bool) {
			for id := range sub {
				if !y(id) {
					return
				}
			}
		})
		return map[string]BatchResult{ids[0]: {Text: "done-before-failure"}}, errors.New("connection reset by peer")
	}
	useEndpoints(t, 3, endpointURLs(good.eps)...)
	jobs := map[string]string{"j0": "x", "j1": "x", "j2": "x", "j3": "x", "j4": "x", "j5": "x"}
	res, err := dispatchPool(context.Background(), good.eps, nil, jobs, nil, nil, send)
	if err != nil || len(res) != 6 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	if res["j0"].Text != "done-before-failure" {
		t.Errorf("j0 = %+v, want the result the failing endpoint completed", res["j0"])
	}
	if sent["j0"] != 1 {
		t.Errorf("j0 sent %d times; a completed result must not be re-sent", sent["j0"])
	}
}
