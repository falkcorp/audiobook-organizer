// file: internal/transcribe/workerfault_test.go
// version: 1.4.0
// guid: d4503544-1899-4765-aa15-eda9489b6832
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	timeoutText = "transcribe_timeout: no result within 90s; the decode process was killed and restarted"
	crashText   = "decoder_crashed: the decode process died while it held this clip"
	misconfText = "worker_misconfigured: inference process not ready after 300s"
)

func resultsOf(errs ...string) map[string]BatchResult {
	out := make(map[string]BatchResult, len(errs))
	for i, e := range errs {
		id := "b" + strconv.Itoa(i)
		if e == "" {
			out[id] = BatchResult{Text: "T:" + id}
		} else {
			out[id] = BatchResult{Error: e}
		}
	}
	return out
}

// NS1. The breaker reads ONE response: the denominator is the worker's own batch.
func TestWorkerFaultIn_JudgesOneResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		errs  []string
		fault int
	}{
		{"two genuine loopers among two clips are two clips, not a sick worker", []string{timeoutText, timeoutText}, 0},
		{"two timeouts among three clips are below the minimum", []string{timeoutText, timeoutText, ""}, 0},
		{"three timeouts among four clips are a worker fault", []string{timeoutText, timeoutText, timeoutText, ""}, 3},
		{"three timeouts among eight clips are a minority", []string{timeoutText, timeoutText, timeoutText, "", "", "", "", ""}, 0},
		{"timeouts and crashes count together", []string{timeoutText, crashText, timeoutText, ""}, 3},
		{"exactly half is not a majority", []string{timeoutText, timeoutText, timeoutText, "", "", ""}, 0},
		{"a lone worker_misconfigured is the worker, whatever else is in the response", []string{misconfText, "", "", "", "", ""}, 1},
		{"an ordinary decode error is never a fault", []string{"whisper_error: boom", "whisper_error: boom", "whisper_error: boom"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resultsOf(tc.errs...)
			kept, fault := splitWorkerFault("http://192.0.2.1:8000", res)
			if tc.fault == 0 {
				if fault != nil || len(kept) != len(res) {
					t.Fatalf("fault=%v kept=%d, want the response untouched", fault, len(kept))
				}
				return
			}
			if fault == nil || len(fault.Faulted) != tc.fault || len(kept) != len(res)-tc.fault {
				t.Fatalf("fault=%+v kept=%d, want %d faulted", fault, len(kept), tc.fault)
			}
			for id := range fault.Faulted {
				if _, both := kept[id]; both {
					t.Errorf("%s is both kept and faulted", id)
				}
			}
			if !IsWorkerFault(fault) || !errors.Is(fault, ErrWorkerFault) {
				t.Errorf("not recognised as a worker fault: %v", fault)
			}
		})
	}
}

// The error text must not read as a network failure, and a worker fault is NOT
// a recognised outage (B1, review of #4176): the per-file op waits out and
// re-sends a recognised failure five times and then stops the run, which for a
// fault caused by the batch's own clips re-sent the same clips into the same
// fault and stopped every later run at the same batch.
func TestWorkerFaultIsNotARecognisedOutageNorAStuckWorker(t *testing.T) {
	_, fault := splitWorkerFault("http://192.0.2.1:8000", resultsOf(timeoutText, timeoutText, timeoutText))
	err := classifyTransport([]string{"http://192.0.2.1:8000"}, fault)
	var te *TransportError
	if !errors.As(err, &te) || te.Recognized {
		t.Fatalf("err = %v recognized=%v, want an UNRECOGNISED transport-class error", err, te != nil && te.Recognized)
	}
	if IsWorkerStuck(err) {
		t.Fatalf("a worker fault is not a stuck worker: %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
		t.Fatalf("worker fault text reads as a network failure: %v", err)
	}
}

func TestIsStrikeError(t *testing.T) {
	for text, want := range map[string]bool{
		timeoutText: true, crashText: true, misconfText: false,
		"whisper_error: x": false, "ffmpeg_timeout: x": false, "": false,
	} {
		if got := IsStrikeError(text); got != want {
			t.Errorf("IsStrikeError(%q) = %v, want %v", text, got, want)
		}
	}
}

// pathBatchWorker answers /transcribe-path-batch with respond's error text per
// job path ("" is a transcript) and records the request headers.
func pathBatchWorker(t *testing.T, health map[string]any, respond func(path string) string) (*pathWorker, func() []http.Header) {
	t.Helper()
	w := newPathWorker(t, health)
	w.respond = func(_ string, job map[string]any) map[string]any {
		if e := respond(job["path"].(string)); e != "" {
			return map[string]any{"text": "", "error": e}
		}
		return map[string]any{"text": "T", "error": nil}
	}
	var mu sync.Mutex
	var headers []http.Header
	inner := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			headers = append(headers, r.Header.Clone())
			mu.Unlock()
		}
		inner.ServeHTTP(rw, r)
	})
	return w, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Header(nil), headers...)
	}
}

// NS1: path pipeline. Most of one response timing out is the worker; the sound
// results of the response are kept and the rest are NOT returned as verdicts.
// With a single worker there is nobody else to send the faulted jobs to, so they
// are deferred (worker_fault), and the chunk the transport never sent is sent
// again rather than deferred on every run (B1).
func TestPathChunks_AResponseOfMostlyTimeoutsIsAWorkerFaultThatKeepsTheSoundResults(t *testing.T) {
	w, _ := pathBatchWorker(t, pathHealth(nil), func(p string) string {
		if strings.Contains(p, "book-d") {
			return ""
		}
		return timeoutText
	})
	useEndpoints(t, 4, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 8), BatchOptions{})
	if err != nil {
		t.Fatalf("err = %v, want none: a timeout-majority fault on the only worker re-sends its jobs and the dispatch goes on", err)
	}
	if res["book-d"].Text != "T" {
		t.Errorf("the sound result in the faulted response was thrown away: %+v", res)
	}
	if len(res) != 8 {
		t.Fatalf("results = %d, want 8 (7 per-clip verdicts + 1 transcript): %+v", len(res), res)
	}
	// R2 (review of #4182): the faulted jobs are judged per clip, in requests
	// too small to fault, by the same worker; the store's different-worker rule
	// keeps such a strike from ever making a file permanent on its own.
	for id, r := range res {
		if id != "book-d" && (PathErrorCode(r.Error) != PathErrTranscribeTimeout || r.Endpoint != w.srv.URL) {
			t.Errorf("%s = %+v, want a per-clip timeout verdict naming the worker", id, r)
		}
	}
	// Two chunks of 4 that each fault, then the 7 faulted jobs in requests of
	// at most 2.
	if w.postCount() != 6 {
		t.Errorf("POSTs = %d, want 6", w.postCount())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, req := range w.requests[2:] {
		if len(req) >= timeoutBreakerMin {
			t.Errorf("re-send %d carried %d clips, want fewer than %d", i, len(req), timeoutBreakerMin)
		}
	}
}

// A minority of timeouts in a response is still a verdict about those files.
func TestPathChunks_AMinorityOfTimeoutsIsStillAVerdictPerFile(t *testing.T) {
	w, _ := pathBatchWorker(t, pathHealth(nil), func(p string) string {
		if strings.Contains(p, "book-b") || strings.Contains(p, "book-c") {
			return timeoutText
		}
		return ""
	})
	useEndpoints(t, 8, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 8), BatchOptions{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, id := range []string{"book-b", "book-c"} {
		if PathErrorCode(res[id].Error) != PathErrTranscribeTimeout {
			t.Errorf("%s = %+v, want its timeout verdict", id, res[id])
		}
	}
}

// NS5/NS6: the worker's own refusal is never a per-book result. On the only
// worker it is a deferral, and a worker-level fault stops the dispatch there (the
// rest are unanswered, with an error that is not a recognised outage) instead of
// sending more to a worker that cannot decode.
func TestPathChunks_WorkerMisconfiguredIsAFaultEvenForOneClip(t *testing.T) {
	w, _ := pathBatchWorker(t, pathHealth(nil), func(string) string { return misconfText })
	useEndpoints(t, 4, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v, want one deferral", res)
	}
	for id, r := range res {
		if !IsNoAnswer(r.Error) || IsStrikeError(r.Error) {
			t.Errorf("%s = %+v, want a no-answer deferral", id, r)
		}
	}

	useEndpoints(t, 1, w.srv.URL)
	res, err = TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	var te *TransportError
	if !errors.As(err, &te) || te.Recognized || !IsWorkerFault(err) {
		t.Fatalf("err = %v, want an unrecognised transport error carrying the worker fault", err)
	}
	if len(res) != 1 {
		t.Errorf("results = %+v: only the faulted job is deferred; the two never sent stay unanswered", res)
	}
}

// NS1: upload pipeline, same rule.
func TestUploadBatch_AResponseOfMostlyTimeoutsIsAWorkerFault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"status": "ok", "batch_pipeline": true, "model": "M"})
	})
	posts := 0
	var mu sync.Mutex
	mux.HandleFunc("POST /transcribe-batch", func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		results := map[string]any{}
		for _, fh := range r.MultipartForm.File["files"] {
			results[fh.Filename] = map[string]any{"text": "", "error": crashText}
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"results": results})
	})
	// The worker decodes: it answers the canary clip, so its crashes are
	// about the clips (see canaryOK).
	mux.HandleFunc("POST /transcribe", func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"text": "Welcome to Gotham City.", "error": nil})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	useEndpoints(t, 4, srv.URL)
	res, err := transcribePool(context.Background(), []Endpoint{{URL: srv.URL, Concurrency: 1}}, nil, tempWAVJobs(t, 8), nil, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want none: the faulted jobs are re-sent in requests too small to fault", err)
	}
	if len(res) != 8 {
		t.Fatalf("results = %d, want 8 per-clip verdicts", len(res))
	}
	for id, r := range res {
		if PathErrorCode(r.Error) != PathErrDecoderCrashed || r.Endpoint != srv.URL {
			t.Errorf("%s = %+v: want a per-clip crash verdict naming the worker", id, r)
		}
	}
	if posts != 6 {
		t.Errorf("POSTs = %d, want 6 (two faulted chunks of 4, then four pairs)", posts)
	}
}

// NS1/NS6: the dispatcher benches a faulted endpoint and hands its jobs to the
// others, instead of returning them as verdicts.
func TestPool_AFaultedWorkersJobsGoToAnotherWorkerAndItIsBenched(t *testing.T) {
	h := newSplitHarness(t, 2, aliveHealth)
	var mu sync.Mutex
	sent := map[string][]string{}
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		mu.Lock()
		sent[g.Endpoint.URL] = append(sent[g.Endpoint.URL], sortedIDs(sub)...)
		mu.Unlock()
		if g.Endpoint.URL == h.eps[0].URL {
			return nil, &WorkerFaultError{URL: g.Endpoint.URL, Reason: "test", Faulted: map[string]BatchResult{}}
		}
		return okResults(sub), nil
	}
	useEndpoints(t, 2, endpointURLs(h.eps)...)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x", "j2": "x", "j3": "x"}, poolOptions{}, send)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v: the healthy worker must finish the faulted worker's jobs", len(res), err)
	}
	if !endpointInCooldown(h.eps[0].URL) {
		t.Errorf("the faulted worker must be benched")
	}
	if endpointInCooldown(h.eps[1].URL) {
		t.Errorf("the sound worker must not be")
	}
	for id, r := range res {
		if r.Error != "" || r.Condemned {
			t.Errorf("%s = %+v", id, r)
		}
	}
}

func TestPool_EveryWorkerFaultedIsAnErrorNotVerdicts(t *testing.T) {
	h := newSplitHarness(t, 2, aliveHealth)
	send := func(_ context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		return map[string]BatchResult{"j0": {Text: "kept"}}, &WorkerFaultError{URL: g.Endpoint.URL, Reason: "test"}
	}
	useEndpoints(t, 1, endpointURLs(h.eps)...)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j0": "x", "j1": "x"}, poolOptions{}, send)
	if err == nil || !IsTransport(err) || IsWorkerStuck(err) {
		t.Fatalf("err = %v, want a transport error that is not stuck", err)
	}
	if res["j0"].Text != "kept" {
		t.Errorf("a sound result settled before the fault was lost: %+v", res)
	}
	if _, ok := res["j1"]; ok {
		t.Errorf("j1 was never answered and must not have a result: %+v", res["j1"])
	}
}

// NS4. A 503 from /health is valid JSON that decodes into an all-zero health,
// which would read as "a worker with no busy fields".
func TestProbeRemoteHealth_ANon200IsNotAProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rw.Write([]byte(`{"detail": "path check timed out"}`))
	}))
	t.Cleanup(srv.Close)
	if h, ok := probeRemoteHealth(context.Background(), srv.URL); ok {
		t.Fatalf("a 503 decoded as a successful probe: %+v", h)
	}
}

// NS4: a worker that cannot start its decoder is refused before a request pays
// the child-ready timeout to be told so.
func TestGate_RefusesAWorkerThatReportsItsDecoderCannotStart(t *testing.T) {
	shortenPacing(t, 5*time.Millisecond, 40*time.Millisecond)
	w := newPathWorker(t, pathHealth(map[string]any{"child_start_error": "model file missing"}))
	useEndpoints(t, 4, w.srv.URL)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	requireTransport(t, err, "decoder process cannot start", "model file missing")
	if !IsPoolUnavailable(err) {
		t.Errorf("err = %v, want pool-unavailable once the pacing allowance is spent", err)
	}
	if w.postCount() != 0 {
		t.Errorf("POSTs = %d, want 0: the worker said it cannot decode", w.postCount())
	}
}

// NS3 (worker side of the fix): the client tells the worker how long it will
// wait, so an abandoned request does not keep the inference lock.
func TestBatchRequestsCarryTheClientsDeadlineToTheWorker(t *testing.T) {
	w, headers := pathBatchWorker(t, pathHealth(map[string]any{
		"clip_timeout_floor_s": 60.0, "clip_timeout_factor": 4.0, "child_ready_timeout_s": 300.0, "extract_timeout_s": 120.0,
	}), func(string) string { return "" })
	useEndpoints(t, 4, w.srv.URL)
	if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{}); err != nil {
		t.Fatal(err)
	}
	hs := headers()
	if len(hs) != 1 {
		t.Fatalf("POSTs = %d", len(hs))
	}
	// one 90 s clip: max(60, 4x90)=360 + 60 slack + 300 child ready + 120 ffmpeg.
	want := "840"
	if got := hs[0].Get("X-Whisper-Request-Timeout-S"); got != want {
		t.Errorf("X-Whisper-Request-Timeout-S = %q, want %q", got, want)
	}
}
