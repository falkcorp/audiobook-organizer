// file: internal/transcribe/stuck_test.go
// version: 1.4.1
// guid: 0b6f3d94-5a1e-4c78-9d02-7e3a8c5b1f46
// last-edited: 2026-10-27

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	// A current worker reports the two waits that precede a clip as well.
	worker := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4, ChildReadyTimeoutS: 300, ExtractTimeoutS: 120}
	// A 1.8.0 worker reports the deadline formula but not the two waits.
	worker18 := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4}
	cases := []struct {
		name    string
		h       remoteHealth
		c       []float64
		extract bool
		want    time.Duration
	}{
		{"worker reports nothing: historical ceiling", remoteHealth{}, clips(16, 90), false, 600 * time.Second},
		{"worker reports nothing, path request: historical ceiling", remoteHealth{}, clips(16, 90), true, 600 * time.Second},
		{"full upload chunk: clip limits + slack + one model load", worker, clips(16, 90), false, (16*360 + 60 + 300) * time.Second},
		{"full path chunk adds one ffmpeg allowance", worker, clips(16, 90), true, (16*360 + 60 + 300 + 120) * time.Second},
		{"short clips use the 60s floor", worker, clips(16, 5), false, (16*60 + 60 + 300) * time.Second},
		// No 600 s floor: a one-clip request is bounded by its own limit.
		{"one short clip is NOT floored at the historical ceiling", worker, clips(1, 5), false, (60 + 60 + 300) * time.Second},
		{"floor only", remoteHealth{ClipTimeoutFloorS: 100, ChildReadyTimeoutS: 10}, clips(10, 90), false, (10*100 + 60 + 10) * time.Second},
		{"1.8.0 worker: the allowances default to its own defaults", worker18, clips(2, 5), true, (2*60 + 60 + 300 + 120) * time.Second},
	}
	for _, tc := range cases {
		if got := requestTimeout(tc.h, tc.c, tc.extract); got != tc.want {
			t.Errorf("%s: requestTimeout = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestRequestTimeoutOutlastsTheWorkersOwnVerdict(t *testing.T) {
	// The invariant the formula exists for: whatever the worker spends on a
	// request when every clip hits its deadline, the client is still waiting.
	h := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4, ChildReadyTimeoutS: 300, ExtractTimeoutS: 120}
	secs := []float64{90, 10, 300, 5, 90}
	var workerWorst time.Duration
	for _, s := range secs {
		workerWorst += time.Duration(max(h.ClipTimeoutFloorS, h.ClipTimeoutFactor*s) * float64(time.Second))
	}
	for _, extract := range []bool{false, true} {
		if got := requestTimeout(h, secs, extract); got <= workerWorst {
			t.Fatalf("extract=%v: ceiling %s does not outlast the worker's worst case %s", extract, got, workerWorst)
		}
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
		got := stuckFromHTTP(context.Background(), timeoutErr, "http://192.0.2.1", time.Second, ids)
		if !IsWorkerStuck(got) {
			t.Fatalf("got %v, want a stuck error", got)
		}
		se, ok := errors.AsType[*WorkerStuckError](got)
		if !ok || !slices.Equal(se.Jobs, ids) || se.URL != "http://192.0.2.1" {
			t.Fatalf("stuck error = %+v", se)
		}
	})
	t.Run("a cancelled caller is not stuck", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := stuckFromHTTP(ctx, timeoutErr, "http://192.0.2.1", time.Second, ids); IsWorkerStuck(got) {
			t.Fatalf("operator cancellation read as a stuck worker: %v", got)
		}
	})
	t.Run("a dial timeout is an outage, not stuck", func(t *testing.T) {
		dialErr := &url.Error{Op: "Post", URL: "http://192.0.2.1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutNetErr{}}}
		if got := stuckFromHTTP(context.Background(), dialErr, "http://192.0.2.1", time.Second, ids); IsWorkerStuck(got) {
			t.Fatalf("dial timeout read as a stuck worker: %v", got)
		}
	})
	t.Run("a refused connection is not stuck", func(t *testing.T) {
		_, err := http.Get(deadServerURL(t))
		if got := stuckFromHTTP(context.Background(), err, "http://192.0.2.1", time.Second, ids); IsWorkerStuck(got) {
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
	err := classifyTransport([]string{"http://192.0.2.1"}, &WorkerStuckError{URL: "http://192.0.2.1", After: 10 * time.Minute, Jobs: []string{"a"}})
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
		// stuckGrace is 45 s: the parent reaps a killed decoder for up to 30 s
		// (InferenceChild._kill) and busy_seconds keeps climbing meanwhile.
		{"inside the grace that covers a 30 s reap", g(f64(100), f64(60), true), false},
		{"just past the grace", g(f64(106), f64(60), true), true},
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

	_, err := transcribePool(context.Background(), []Endpoint{{URL: srv.URL, Concurrency: 1}}, nil, tempWAVJobs(t, 2), nil, nil, nil)
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
	srvs   []*httptest.Server // the fake workers' /health servers, index-aligned with eps
	// wedgeOnPoison makes a worker that returns a stuck error report a decode
	// that is past its deadline from then on, like a decoder that will not die.
	// Off, a stuck request leaves the worker answering /health as idle, which is
	// a request that waited (queued behind another one) rather than one that
	// hung the decoder.
	wedgeOnPoison bool
	wedged        sync.Map // "host:port" -> true once wedged
	// childFault holds the workers ("host:port") whose /health reports that the
	// decoder cannot start, as a worker does for its fault window.
	childFault sync.Map
	// machineID holds the machine_id ("host:port" -> id) a worker reports from
	// now on, overriding what machineHarness started it with: a worker whose
	// machine-id lookup missed its first wait reports one later in the same
	// process (see setMachineID).
	machineID sync.Map
	// backend holds the /health backend ("host:port" -> backend) a worker reports
	// from now on, overriding the "mlx" machineHarness starts it with: a URL taken
	// over by a worker of another server (see setBackend).
	backend sync.Map
	// healthDelay holds how long a worker ("host:port" -> time.Duration) holds
	// every /health request before it answers, or until the request is cancelled:
	// a probe that is in flight when the op is cancelled (see delayHealth).
	healthDelay sync.Map
	// healthField holds extra /health fields a worker reports from now on
	// ("host:port" -> map of field -> value, never modified once stored), merged
	// over what machineHarness started it with (see setHealthField): a worker
	// whose health changed with its identity intact.
	healthField sync.Map
}

// wedge marks the worker at url as wedged from now on.
func (h *splitHarness) wedge(url string) {
	h.wedged.Store(strings.TrimPrefix(url, "http://"), true)
}

// setChildFault starts (on) or ends the child-fault window of the worker at url.
func (h *splitHarness) setChildFault(url string, on bool) {
	if on {
		h.childFault.Store(strings.TrimPrefix(url, "http://"), true)
		return
	}
	h.childFault.Delete(strings.TrimPrefix(url, "http://"))
}

// setMachineID makes the worker at url report machine_id id from now on, as
// scripts/whisper_mlx_server.py does once its retried lookup succeeds.
func (h *splitHarness) setMachineID(url, id string) {
	h.machineID.Store(strings.TrimPrefix(url, "http://"), id)
}

// delayHealth makes the worker at url hold its /health answers for d from now
// on (a probe cut short by a cancellation reads nothing).
func (h *splitHarness) delayHealth(url string, d time.Duration) {
	h.healthDelay.Store(strings.TrimPrefix(url, "http://"), d)
}

// setHealthField makes the worker at url report fields in /health from now on
// (replacing any set before), leaving its identity as it was.
func (h *splitHarness) setHealthField(url string, fields map[string]any) {
	h.healthField.Store(strings.TrimPrefix(url, "http://"), maps.Clone(fields))
}

// setBackend makes the worker at url report backend b from now on: a URL whose
// worker was replaced by another server ("faster-whisper" is
// scripts/whisper_server.py, which never reports a machine_id).
func (h *splitHarness) setBackend(url, b string) {
	h.backend.Store(strings.TrimPrefix(url, "http://"), b)
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
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if _, wedged := h.wedged.Load(r.Host); wedged {
				_ = json.NewEncoder(rw).Encode(map[string]any{"status": "ok", "device": "metal", "batch_pipeline": true,
					"busy_seconds": 900.0, "busy_limit_seconds": 60.0})
				return
			}
			_ = json.NewEncoder(rw).Encode(hh)
		}))
		t.Cleanup(srv.Close)
		h.srvs = append(h.srvs, srv)
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
			if h.wedgeOnPoison {
				h.wedge(g.Endpoint.URL)
			}
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
	return dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{}, h.send)
}

var aliveHealth = map[string]any{"status": "ok", "device": "metal", "batch_pipeline": true, "busy_seconds": 0.0, "busy_limit_seconds": nil}

// wedgedSplitHarness is a pool whose workers wedge when they take the poison
// job, which is what a condemnation needs: a lone job is only blamed on a worker
// that, when asked after the request ran out, could not say it was healthy. A
// wedged worker is benched, so narrowing a batch down to one file takes about
// one spare worker per level of the split.
func wedgedSplitHarness(t *testing.T, workers int, poison ...string) *splitHarness {
	t.Helper()
	h := newSplitHarness(t, workers, aliveHealth, poison...)
	h.wedgeOnPoison = true
	return h
}

func TestSplitIsolatesTheStuckFileAndInnocentsComplete(t *testing.T) {
	h := wedgedSplitHarness(t, 7, "j02")
	res, err := h.run(t, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 4 {
		t.Fatalf("results = %d, want 4", len(res))
	}
	for id, r := range res {
		if id == "j02" {
			if PathErrorCode(r.Error) != PathErrTranscribeTimeout || r.Text != "" {
				t.Errorf("culprit must be a transcribe_timeout verdict, got %+v", r)
			}
			if !r.Condemned {
				t.Errorf("the dispatcher's verdict must say it is one (callers strike on it even when the pool failed): %+v", r)
			}
			continue
		}
		if r.Error != "" || r.Text != "T:"+id || r.Condemned {
			t.Errorf("innocent %s was condemned or lost: %+v", id, r)
		}
	}
	// The culprit was retried ALONE on two different workers before the verdict.
	solo := map[string]bool{}
	for _, s := range h.sends {
		if slices.Equal(s.ids, []string{"j02"}) {
			solo[s.url] = true
		}
	}
	if len(solo) != 2 {
		t.Errorf("culprit sent alone to %d distinct workers, want 2; sends=%v", len(solo), h.sends)
	}
}

func TestSplitCulpritIsNeverRetriedAloneOnAWorkerItWasAlreadyStuckOnAlone(t *testing.T) {
	h := wedgedSplitHarness(t, 7, "j03")
	if _, err := h.run(t, 4); err != nil {
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
	// 8 jobs, one poison: 7 innocents must come back as real transcripts.
	h := wedgedSplitHarness(t, 9, "j05")
	res, err := h.run(t, 8)
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, r := range res {
		if r.Error != "" {
			bad++
		}
	}
	if len(res) != 8 || bad != 1 {
		t.Errorf("results=%d errors=%d, want 8 results and exactly 1 error", len(res), bad)
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

// NS3. A request that ran out its ceiling on a worker that then answers /health
// as idle and inside its own deadline WAITED (it queued behind another request);
// it did not hang the decoder. Such a worker stays usable, and a lone job that
// overran on it is not blamed: no verdict is written for the file, however many
// workers it overran on.
func TestSplitAliveWorkerThatMissedTheCeilingIsNotBenchedAndBlamesNoFile(t *testing.T) {
	h := newSplitHarness(t, 2, aliveHealth, "j01")
	res, err := h.run(t, 4)
	if err == nil || !IsWorkerStuck(err) {
		t.Fatalf("err = %v, want the stuck-pool error that makes the callers defer", err)
	}
	if r, ok := res["j01"]; ok {
		t.Errorf("j01 = %+v: a file whose request merely waited must get no verdict", r)
	}
	for _, id := range []string{"j00", "j02", "j03"} {
		if res[id].Text != "T:"+id {
			t.Errorf("%s = %+v", id, res[id])
		}
	}
	for _, ep := range h.eps {
		if endpointInCooldown(ep.URL) {
			t.Errorf("%s is idle and inside its deadline; it must not be benched", ep.URL)
		}
	}
	// And the lone job was not re-placed on a worker it had already overrun on.
	alone := map[string]int{}
	for _, s := range h.sends {
		if slices.Equal(s.ids, []string{"j01"}) {
			alone[s.url]++
		}
	}
	for url, n := range alone {
		if n > 1 {
			t.Errorf("j01 was sent alone to %s %d times", url, n)
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
	res, err := dispatchPool(context.Background(), good.eps, nil, jobs, poolOptions{}, send)
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
