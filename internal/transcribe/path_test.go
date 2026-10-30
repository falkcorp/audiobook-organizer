// file: internal/transcribe/path_test.go
// version: 1.2.2
// guid: 4d8b0e62-91c7-4a35-b6f0-3e5a7c21d984
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// pathWorker is a fake worker speaking the path protocol. It records every
// /transcribe-path-batch request so tests can prove WHAT reached the worker and
// how often -- including that a refused endpoint received nothing.
type pathWorker struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []map[string]map[string]any // one decoded "jobs" object per POST
	respond  func(key string, job map[string]any) map[string]any
	canaries int // canary clips received on /transcribe, answered as a sound worker would

	healthHits atomic.Int64 // GET /health requests received
}

func newPathWorker(t *testing.T, health map[string]any) *pathWorker {
	t.Helper()
	w := &pathWorker{}
	w.respond = func(key string, job map[string]any) map[string]any {
		return map[string]any{"text": "T:" + filepath.Base(job["path"].(string)), "error": nil}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(rw http.ResponseWriter, _ *http.Request) {
		w.healthHits.Add(1)
		_ = json.NewEncoder(rw).Encode(health)
	})
	mux.HandleFunc("POST /transcribe-path-batch", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Jobs map[string]map[string]any `json:"jobs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.requests = append(w.requests, body.Jobs)
		respond := w.respond
		w.mu.Unlock()
		results := map[string]any{}
		for k, j := range body.Jobs {
			results[k] = respond(k, j)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("POST /transcribe", func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		w.canaries++
		w.mu.Unlock()
		_ = json.NewEncoder(rw).Encode(map[string]any{"text": "Welcome to Gotham City.", "error": nil})
	})
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *pathWorker) postCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.requests)
}

func (w *pathWorker) jobsSeen() map[string]map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	all := map[string]map[string]any{}
	for _, req := range w.requests {
		for k, v := range req {
			all[k] = v
		}
	}
	return all
}

func pathHealth(extra map[string]any) map[string]any {
	h := map[string]any{
		"status": "ok", "model": "M", "backend": "mlx", "device": "metal",
		"batch_pipeline": true, "decode_fingerprint": "fp1", "extract_fingerprint": "ex1",
		"path_pipeline": true,
		"path_roots":    []map[string]any{{"prefix": "/mnt/lib", "readable": true}},
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func useEndpoints(t *testing.T, batchSize int, urls ...string) {
	t.Helper()
	// A machine that failed the canary clip is remembered for the life of the
	// process, and tests reuse machine ids: start and end each test with none.
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	// An endpoint's cooldown and failure count live in the process-wide health
	// registry, keyed by URL, and outlive the httptest server that earned them
	// (a cooldown is 30s per consecutive failure, longer than this package's
	// whole run). The OS hands a closed server's port to a later server, so a
	// test could start with a port already benched by an earlier test: the
	// dispatcher then never sends that port work in the first round, it never
	// faults, and the canary round asks one port fewer than the test expects
	// (TestPool_TheCanaryDecidesWhichFaultedMachinesAreBelieved: "canaries = 5,
	// want 6"). Start each test's endpoints healthy, and leave them healthy.
	for _, u := range urls {
		clearCooldown(u)
		t.Cleanup(func() { clearCooldown(u) })
	}
	orig := config.Snapshot()
	eps := make([]config.WhisperEndpoint, 0, len(urls))
	for i, u := range urls {
		eps = append(eps, config.WhisperEndpoint{URL: u, Concurrency: 1, Priority: i + 1})
	}
	config.Mutate(func(c *config.Config) {
		c.WhisperEndpoints = eps
		c.WhisperRemoteURL = ""
		c.WhisperRequires = nil
		c.WhisperBatchSize = batchSize
		c.WhisperBatchSleepMS = 0
	})
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) {
			c.WhisperEndpoints = orig.WhisperEndpoints
			c.WhisperRemoteURL = orig.WhisperRemoteURL
			c.WhisperRequires = orig.WhisperRequires
			c.WhisperBatchSize = orig.WhisperBatchSize
			c.WhisperBatchSleepMS = orig.WhisperBatchSleepMS
		})
	})
}

// useEndpoints starts a test's endpoints healthy and leaves them healthy: a
// port's cooldown is keyed by URL in a process-wide registry, and the OS hands a
// closed httptest server's port to a later one. Standing in for that reuse: the
// URL below is benched as an earlier test's dispatch would have left it.
func TestUseEndpoints_ClearsACooldownLeftOnAReusedPort(t *testing.T) {
	const reused = "http://127.0.0.1:1" // never dialled: only its health entry is read
	t.Cleanup(func() { clearCooldown(reused) })

	markEndpointFailure(reused)
	markEndpointFailure(reused) // two consecutive failures: a 60s cooldown
	if !endpointInCooldown(reused) {
		t.Fatalf("setup: %s is not in cooldown after two failures", reused)
	}

	t.Run("a test using it", func(t *testing.T) {
		useEndpoints(t, 16, reused)
		if endpointInCooldown(reused) {
			t.Errorf("%s is still benched at the start of a test that uses it: a dispatch would send it no work", reused)
		}
		markEndpointFailure(reused) // what this test's own dispatch does
	})

	if endpointInCooldown(reused) {
		t.Errorf("%s is still benched after the test that used it ended, for the next test that is handed its port", reused)
	}
}

// pathJobs writes n real files (so the journal's stat signature works) and
// returns jobs keyed by book id. The files hold no audio: nothing here decodes.
func pathJobs(t *testing.T, n int) map[string]PathJob {
	t.Helper()
	dir := t.TempDir()
	jobs := map[string]PathJob{}
	for i := range n {
		id := "book-" + string(rune('a'+i))
		p := filepath.Join(dir, id+".m4b")
		if err := os.WriteFile(p, []byte("not-audio-"+id), 0o644); err != nil {
			t.Fatal(err)
		}
		jobs[id] = PathJob{Path: p, Seconds: 90}
	}
	return jobs
}

func TestPathErrorCode(t *testing.T) {
	cases := map[string]string{
		"path_unmapped: path is not under a mapped prefix": PathErrUnmapped,
		"not_found: file not found under the mapped root":  PathErrNotFound,
		"ffmpeg_error: Invalid data found":                 PathErrFFmpeg,
		"ffmpeg_timeout: ffmpeg exceeded 120s":             PathErrFFmpegTimeout,
		"bad_request: seconds must be an integer":          PathErrBadRequest,
		"whisper_error: model fell over":                   PathErrWhisper,
		"worker_misconfigured: ffmpeg is not on PATH":      PathErrWorkerMisconfigured,
		// No code: an old/foreign worker's free text must not be guessed at.
		"Failed to load audio: ffmpeg_error: x": "",
		"ffmpeg_error":                          "",
		"something else: entirely":              "",
		"":                                      "",
	}
	for in, want := range cases {
		if got := PathErrorCode(in); got != want {
			t.Errorf("PathErrorCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranscribeByPathBatch_SendsServerPathsAndKeysVerbatim(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)
	jobs := pathJobs(t, 3)
	jobs["Jules Verne — 20,000 Leagues"] = PathJob{Path: "/mnt/lib/x/y.m4b", Seconds: 300}

	var last, total int
	res, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{
		OnProgress: func(d, tot int) { last, total = d, tot },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(jobs) {
		t.Fatalf("got %d results, want %d", len(res), len(jobs))
	}
	if last != len(jobs) || total != len(jobs) {
		t.Errorf("final progress = %d/%d, want %d/%d", last, total, len(jobs), len(jobs))
	}
	seen := w.jobsSeen()
	for id, j := range jobs {
		got, ok := seen[id]
		if !ok {
			t.Fatalf("worker never saw key %q", id)
		}
		if got["path"] != j.Path || int(got["seconds"].(float64)) != j.Seconds {
			t.Errorf("%q: worker saw %v, want path=%q seconds=%d", id, got, j.Path, j.Seconds)
		}
		if res[id].Error != "" || !strings.HasPrefix(res[id].Text, "T:") {
			t.Errorf("%q: result %+v", id, res[id])
		}
	}
}

func TestTranscribeByPathBatch_PerJobErrorsPassThroughAndIsolate(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	w.respond = func(key string, job map[string]any) map[string]any {
		if key == "book-b" {
			return map[string]any{"text": "", "error": "ffmpeg_error: Invalid data"}
		}
		return map[string]any{"text": "ok", "error": nil}
	}
	useEndpoints(t, 0, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	if err != nil {
		t.Fatalf("a per-job error must not be a batch error: %v", err)
	}
	if res["book-b"].Error != "ffmpeg_error: Invalid data" || res["book-a"].Text != "ok" || res["book-c"].Text != "ok" {
		t.Errorf("results = %+v", res)
	}
}

func requireTransport(t *testing.T, err error, wantInMsg ...string) {
	t.Helper()
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T: %v", err, err)
	}
	for _, s := range wantInMsg {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
}

func TestTranscribeByPathBatch_FailsClosedWithoutPathCapability(t *testing.T) {
	cases := map[string]map[string]any{
		"old worker":        {"status": "ok", "model": "M", "batch_pipeline": true},
		"map unset":         pathHealth(map[string]any{"path_pipeline": false, "path_roots": []any{}}),
		"flag but no roots": pathHealth(map[string]any{"path_roots": []any{}}),
	}
	for name, health := range cases {
		t.Run(name, func(t *testing.T) {
			w := newPathWorker(t, health)
			useEndpoints(t, 0, w.srv.URL)
			_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
			requireTransport(t, err, "path_pipeline")
			if w.postCount() != 0 {
				t.Errorf("a refused endpoint received %d POSTs, want 0", w.postCount())
			}
		})
	}
}

func TestTranscribeByPathBatch_LostMountDefersWithoutSending(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{
		"path_roots": []map[string]any{{"prefix": "/mnt/lib", "readable": false}},
	}))
	useEndpoints(t, 0, w.srv.URL)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	requireTransport(t, err, "/mnt/lib", "mount lost")
	if w.postCount() != 0 {
		t.Errorf("worker with a lost mount received %d POSTs, want 0", w.postCount())
	}
}

func TestTranscribeByPathBatch_NoEndpointsConfigured(t *testing.T) {
	useEndpoints(t, 0)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{})
	requireTransport(t, err, "never on this server")
}

// A pool that also holds a non-path worker (say a CUDA box) must use only the
// path-capable one and send the other nothing.
func TestTranscribeByPathBatch_MixedPoolUsesOnlyPathWorkers(t *testing.T) {
	good := newPathWorker(t, pathHealth(nil))
	plain := newPathWorker(t, map[string]any{"status": "ok", "model": "M", "batch_pipeline": true, "device": "cuda"})
	useEndpoints(t, 0, plain.srv.URL, good.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 4), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 || good.postCount() == 0 || plain.postCount() != 0 {
		t.Errorf("results=%d good POSTs=%d plain POSTs=%d", len(res), good.postCount(), plain.postCount())
	}
}

func TestTranscribeByPathBatch_ChunksAndReportsProgressPerChunk(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 2, w.srv.URL)
	var ticks []int
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 5), BatchOptions{
		OnProgress: func(d, _ int) { ticks = append(ticks, d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.postCount() != 3 {
		t.Errorf("POSTs = %d, want 3 (5 jobs at batch size 2)", w.postCount())
	}
	if len(ticks) != 5 || ticks[len(ticks)-1] != 5 {
		t.Errorf("progress ticks = %v, want 5 ending at 5", ticks)
	}
}

func TestTranscribeByPathBatch_ChunkFailureIsATransportError(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(rw).Encode(pathHealth(nil))
			return
		}
		http.Error(rw, "boom", http.StatusInternalServerError)
	})
	useEndpoints(t, 0, w.srv.URL)
	_, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	requireTransport(t, err, "HTTP 500")
}

// ── journal ──────────────────────────────────────────────────────────────────

func TestTranscribeByPathBatch_JournalServesRerunWithoutSending(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)
	jobs := pathJobs(t, 3)
	j := newMapJournal()
	first, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	if j.len() != 3 || w.postCount() != 1 {
		t.Fatalf("after run 1: journal=%d POSTs=%d, want 3 and 1", j.len(), w.postCount())
	}
	var ticks []int
	second, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{
		Journal: j, OnProgress: func(d, _ int) { ticks = append(ticks, d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.postCount() != 1 {
		t.Errorf("rerun reached the worker (%d POSTs); the journal should have served it", w.postCount())
	}
	for id, r := range first {
		if second[id] != r {
			t.Errorf("%s: rerun %+v != first %+v", id, second[id], r)
		}
	}
	if len(ticks) == 0 || ticks[len(ticks)-1] != 3 {
		t.Errorf("served jobs must still report progress to 3, got %v", ticks)
	}
}

// A file replaced in place (new size/mtime) must not be served the old
// transcript, and a different clip length is a different input.
func TestTranscribeByPathBatch_JournalKeyTracksTheFileAndClipLength(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)
	jobs := pathJobs(t, 1)
	j := newMapJournal()
	run := func() {
		t.Helper()
		if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	if w.postCount() != 1 {
		t.Fatalf("unchanged input re-sent: POSTs=%d", w.postCount())
	}
	job := jobs["book-a"]
	if err := os.WriteFile(job.Path, []byte("replaced with different bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	run()
	if w.postCount() != 2 {
		t.Errorf("replaced file was served from the journal (POSTs=%d)", w.postCount())
	}
	job.Seconds = 300
	jobs["book-a"] = job
	run()
	if w.postCount() != 3 {
		t.Errorf("a different clip length was served from the journal (POSTs=%d)", w.postCount())
	}
}

func TestTranscribeByPathBatch_JournalKeyTracksExtractAndDecodeFingerprints(t *testing.T) {
	jobs := pathJobs(t, 1)
	j := newMapJournal()
	for i, extra := range []map[string]any{
		nil,
		{"extract_fingerprint": "ex2"},
		{"decode_fingerprint": "fp2"},
	} {
		w := newPathWorker(t, pathHealth(extra))
		useEndpoints(t, 0, w.srv.URL)
		if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
			t.Fatal(err)
		}
		if w.postCount() != 1 || j.len() != i+1 {
			t.Fatalf("variant %d: POSTs=%d journal=%d, want 1 and %d", i, w.postCount(), j.len(), i+1)
		}
	}
}

func TestTranscribeByPathBatch_ErrorsAndEmptyTextAreNotJournalled(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	w.respond = func(key string, _ map[string]any) map[string]any {
		if key == "book-a" {
			return map[string]any{"text": "", "error": "ffmpeg_timeout: x"}
		}
		return map[string]any{"text": "", "error": nil}
	}
	useEndpoints(t, 0, w.srv.URL)
	j := newMapJournal()
	if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{Journal: j}); err != nil {
		t.Fatal(err)
	}
	if j.len() != 0 {
		t.Errorf("journalled %d entries for error/empty results, want 0", j.len())
	}
}

func TestTranscribeByPathBatch_RefreshBypassesLookupButStillJournals(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)
	jobs := pathJobs(t, 2)
	j := newMapJournal()
	for _, refresh := range []bool{false, true} {
		if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j, RefreshJournal: refresh}); err != nil {
			t.Fatal(err)
		}
	}
	if w.postCount() != 2 || j.len() != 2 {
		t.Errorf("POSTs=%d journal=%d, want 2 and 2", w.postCount(), j.len())
	}
}

// A path job and a WAV clip are different keyspaces even for an identical
// signature string.
func TestPathKeyIsDisjointFromClipKey(t *testing.T) {
	ej := &endpointJournal{model: "mlx/M", fingerprint: "fp", extract: "ex"}
	if ej.pathKey("abc") == ej.key("abc") {
		t.Fatal("path and clip keys collide")
	}
}

// A file that cannot be stat'ed is still sent (the worker reports the real
// reason) but is never journalled.
func TestTranscribeByPathBatch_UnstatableFileIsSentNotJournalled(t *testing.T) {
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)
	j := newMapJournal()
	jobs := map[string]PathJob{"k": {Path: "/mnt/lib/definitely/not/here.m4b", Seconds: 90}}
	if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
		t.Fatal(err)
	}
	if w.postCount() != 1 || j.len() != 0 {
		t.Errorf("POSTs=%d journal=%d, want 1 and 0", w.postCount(), j.len())
	}
}

func TestPathPipelineAvailable(t *testing.T) {
	t.Run("advertised", func(t *testing.T) {
		w := newPathWorker(t, pathHealth(nil))
		useEndpoints(t, 0, w.srv.URL)
		if ok, reason := PathPipelineAvailable(context.Background()); !ok {
			t.Fatalf("not available: %s", reason)
		}
	})
	t.Run("advertised but mount lost still counts as the capability", func(t *testing.T) {
		w := newPathWorker(t, pathHealth(map[string]any{
			"path_roots": []map[string]any{{"prefix": "/mnt/lib", "readable": false}},
		}))
		useEndpoints(t, 0, w.srv.URL)
		if ok, _ := PathPipelineAvailable(context.Background()); !ok {
			t.Fatal("a lost mount must defer pages, not flip the op back to server decoding")
		}
	})
	t.Run("old worker", func(t *testing.T) {
		w := newPathWorker(t, map[string]any{"status": "ok", "model": "M", "batch_pipeline": true})
		useEndpoints(t, 0, w.srv.URL)
		ok, reason := PathPipelineAvailable(context.Background())
		if ok || !strings.Contains(reason, "path_pipeline") {
			t.Fatalf("ok=%v reason=%q", ok, reason)
		}
	})
	t.Run("none configured", func(t *testing.T) {
		useEndpoints(t, 0)
		if ok, reason := PathPipelineAvailable(context.Background()); ok || reason == "" {
			t.Fatalf("ok=%v reason=%q", ok, reason)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		useEndpoints(t, 0, deadServerURL(t))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if ok, _ := PathPipelineAvailable(ctx); ok {
			t.Fatal("an unreachable endpoint is not available")
		}
	})
}
