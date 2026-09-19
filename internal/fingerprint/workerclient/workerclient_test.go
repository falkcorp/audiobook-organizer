// file: internal/fingerprint/workerclient/workerclient_test.go
// version: 1.1.0
// guid: 0f3c9a52-6f0e-4d7b-9a55-3d1e2b8c7a41
// last-edited: 2026-09-19

package workerclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

const testKey = "test-key-not-a-secret"

var testVersions = fingerprint.ToolVersionInfo{Fpcalc: "1.6.1", FFmpeg: "8.0.1"}

// fakeRaw is the fake pipeline's print: 100 frames derived from the file's
// bytes and the window offset, so a different file or window hashes
// differently.
func fakeRaw(content []byte, offset float64) []byte {
	seed := sha256.Sum256(append(append([]byte{}, content...), []byte(strconv.FormatFloat(offset, 'f', 3, 64))...))
	raw := make([]byte, 100*4)
	for i := 0; i < 100; i++ {
		binary.LittleEndian.PutUint32(raw[i*4:], binary.LittleEndian.Uint32(seed[(i%8)*4:])+uint32(i))
	}
	return raw
}

func fakeCut(_ context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", fingerprint.ErrWindowFFmpeg, err)
	}
	raw := fakeRaw(data, spec.OffsetSec)
	return &fingerprint.WindowPrint{Kind: spec.Kind, SlotBP: spec.SlotBP, OffsetSec: spec.OffsetSec,
		LengthSec: spec.LengthSec, DecodedSec: spec.LengthSec, Frames: len(raw) / 4, Raw: raw,
		Algorithm: fingerprint.WindowAlgorithm, Pipeline: fingerprint.WindowPipelineID}, nil
}

// fakeServer implements the worker API in memory.
type fakeServer struct {
	t  *testing.T
	mu sync.Mutex

	hello       workerapi.HelloResponse
	pending     []workerapi.Job
	leaseScript []int // statuses answered to lease before any job is served
	nLease      int

	leaseCalls  int
	renewCalls  int
	resultPosts int
	leaseReqs   []workerapi.LeaseRequest
	results     []workerapi.JobResult
	releases    []workerapi.ReleaseRequest
	badAuth     int
	resultsCh   chan struct{}
}

func (s *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				s.mu.Lock()
				s.badAuth++
				s.mu.Unlock()
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	p := apiPrefix
	mux.HandleFunc("GET "+p+"/hello", auth(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, s.hello)
	}))
	mux.HandleFunc("POST "+p+"/lease", auth(func(w http.ResponseWriter, r *http.Request) {
		var req workerapi.LeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.leaseCalls++
		s.leaseReqs = append(s.leaseReqs, req)
		if len(s.leaseScript) > 0 {
			st := s.leaseScript[0]
			s.leaseScript = s.leaseScript[1:]
			if st == http.StatusNoContent {
				w.WriteHeader(st)
				return
			}
			http.Error(w, `{"error":"scripted"}`, st)
			return
		}
		if len(s.pending) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n := req.MaxJobs
		if n <= 0 || n > workerapi.MaxJobsPerLease {
			n = workerapi.MaxJobsPerLease
		}
		n = min(n, len(s.pending))
		s.nLease++
		resp := workerapi.LeaseResponse{LeaseID: fmt.Sprintf("L%d", s.nLease), ExpiresAt: time.Now().Add(time.Minute),
			Jobs: append([]workerapi.Job(nil), s.pending[:n]...)}
		s.pending = s.pending[n:]
		writeJSON(w, resp)
	}))
	mux.HandleFunc("POST "+p+"/lease/{id}/renew", auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.renewCalls++
		s.mu.Unlock()
		writeJSON(w, workerapi.RenewResponse{LeaseID: r.PathValue("id"), ExpiresAt: time.Now().Add(time.Minute)})
	}))
	mux.HandleFunc("POST "+p+"/lease/{id}/release", auth(func(w http.ResponseWriter, r *http.Request) {
		var req workerapi.ReleaseRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.releases = append(s.releases, req)
		s.mu.Unlock()
		writeJSON(w, workerapi.ReleaseResponse{Requeued: len(req.JobIDs)})
	}))
	mux.HandleFunc("POST "+p+"/results", auth(func(w http.ResponseWriter, r *http.Request) {
		var req workerapi.ResultsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.resultPosts++
		s.results = append(s.results, req.Results...)
		resp := workerapi.ResultsResponse{}
		for _, res := range req.Results {
			resp.Statuses = append(resp.Statuses, workerapi.JobStatus{JobID: res.JobID, Status: workerapi.StatusAccepted})
		}
		s.mu.Unlock()
		writeJSON(w, resp)
		if s.resultsCh != nil {
			s.resultsCh <- struct{}{}
		}
	}))
	return mux
}

func (s *fakeServer) nResults() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

// testEnv is a temp-dir "mount", a fake server and a Config wired to both.
type testEnv struct {
	t     *testing.T
	mount string
	srv   *fakeServer
	cfg   Config
	nJob  int
}

var testWindows = []workerapi.Window{
	{Kind: "window", SlotBP: 0, OffsetSec: 0, LengthSec: 120},
	{Kind: "window", SlotBP: 5000, OffsetSec: 300, LengthSec: 120},
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	mount, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, mount: mount, srv: &fakeServer{t: t, resultsCh: make(chan struct{}, 100)}}
	calContent := []byte(strings.Repeat("calibration audio ", 5000)) // > 64 KiB
	calRel := "Author/Calibration Book/part1.m4b"
	fi := e.writeFile(calRel, calContent)
	head := sha256.Sum256(calContent[:calibrationHeadBytes])
	cf := workerapi.CalibrationFile{Root: "libroot", RelB64: base64.StdEncoding.EncodeToString([]byte(calRel)), Rel: calRel,
		Size: fi.Size(), MtimeUnix: fi.ModTime().Unix(), Head64K: hex.EncodeToString(head[:])}
	for _, w := range testWindows {
		sum := sha256.Sum256(fakeRaw(calContent, w.OffsetSec))
		cf.Windows = append(cf.Windows, workerapi.CalibrationWindow{Window: w, RawSHA256: hex.EncodeToString(sum[:]),
			Frames: 100, ToolVersions: workerapi.ToolVersions{Fpcalc: testVersions.Fpcalc, FFmpeg: testVersions.FFmpeg}})
	}
	e.srv.hello = workerapi.HelloResponse{
		Roots:        []workerapi.Root{{ID: "libroot", Remote: true}, {ID: "books", Remote: false}},
		Pipeline:     fingerprint.WindowPipelineID,
		WindowSet:    fingerprint.WindowSetWS1,
		ToolVersions: []workerapi.ToolVersions{{Fpcalc: testVersions.Fpcalc, FFmpeg: testVersions.FFmpeg}},
		Calibration:  []workerapi.CalibrationFile{cf},
		Limits: workerapi.Limits{MaxJobsPerLease: workerapi.MaxJobsPerLease, LeaseTTLSec: 600, RenewEverySec: 120,
			MaxLeasesPerWorker: workerapi.MaxLeasesPerWorker, MaxBodyBytes: workerapi.MaxBodyBytes},
	}
	ts := httptest.NewServer(e.srv.handler())
	t.Cleanup(ts.Close)
	e.cfg = Config{
		ServerURL: ts.URL, APIKey: testKey, WorkerID: "test-worker",
		Roots: map[string]string{"libroot": mount}, Concurrency: 4, Versions: testVersions, Cut: fakeCut,
		statMount: func(string) (MountInfo, error) {
			return MountInfo{FSType: "nfs", MountPoint: mount, ReadOnly: true}, nil
		},
		writeProbe: func(string) error { return nil },
		backoffMin: 2 * time.Millisecond, backoffMax: 10 * time.Millisecond,
		flushEvery: 10 * time.Millisecond, retryDelay: time.Millisecond,
	}
	return e
}

func (e *testEnv) writeFile(rel string, content []byte) os.FileInfo {
	e.t.Helper()
	p := filepath.Join(e.mount, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		e.t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		e.t.Fatal(err)
	}
	return fi
}

// job builds a job for rel as the server would see it: size and mtime from
// fi (the server's stat).
func (e *testEnv) job(rel string, fi os.FileInfo) workerapi.Job {
	e.nJob++
	return workerapi.Job{JobID: fmt.Sprintf("J%d", e.nJob), Ref: fmt.Sprintf("f:file-%d", e.nJob), Root: "libroot",
		RelB64: base64.StdEncoding.EncodeToString([]byte(rel)), Rel: rel, Size: fi.Size(), MtimeUnix: fi.ModTime().Unix(),
		DurationSec: 3600, DurationSource: "fingerprint", WindowSet: fingerprint.WindowSetWS1, Windows: testWindows}
}

func (e *testEnv) addJob(rel string, content []byte) workerapi.Job {
	j := e.job(rel, e.writeFile(rel, content))
	e.srv.mu.Lock()
	e.srv.pending = append(e.srv.pending, j)
	e.srv.mu.Unlock()
	return j
}

type runResult struct{ err error }

func (e *testEnv) start() (drain chan struct{}, done chan runResult) {
	drain = make(chan struct{})
	done = make(chan runResult, 1)
	go func() { done <- runResult{Run(context.Background(), drain, e.cfg)} }()
	return drain, done
}

func (e *testEnv) waitResults(n int, done chan runResult) {
	e.t.Helper()
	deadline := time.After(10 * time.Second)
	for e.srv.nResults() < n {
		select {
		case <-e.srv.resultsCh:
		case r := <-done:
			e.t.Fatalf("Run returned early: %v (have %d/%d results)", r.err, e.srv.nResults(), n)
		case <-deadline:
			e.t.Fatalf("timed out: %d/%d results", e.srv.nResults(), n)
		}
	}
}

func waitDone(t *testing.T, done chan runResult) error {
	t.Helper()
	select {
	case r := <-done:
		return r.err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestRun_HappyPath(t *testing.T) {
	e := newTestEnv(t)
	contents := map[string][]byte{}
	for i := range 6 {
		rel := fmt.Sprintf("Author %d/Book/part %d.mp3", i, i)
		c := []byte(strings.Repeat(fmt.Sprintf("audio %d ", i), 100))
		j := e.addJob(rel, c)
		contents[j.JobID] = c
	}
	drain, done := e.start()
	e.waitResults(6, done)
	close(drain)
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	if e.srv.badAuth != 0 {
		t.Errorf("%d requests without the bearer key", e.srv.badAuth)
	}
	for _, req := range e.srv.leaseReqs {
		if req.WorkerID != "test-worker" || req.FpcalcVersion != "1.6.1" || req.FFmpegVersion != "8.0.1" ||
			req.Pipeline != fingerprint.WindowPipelineID {
			t.Errorf("lease request = %+v", req)
		}
	}
	for _, res := range e.srv.results {
		if res.Outcome != workerapi.OutcomeOK {
			t.Fatalf("job %s: outcome %s (%s)", res.JobID, res.Outcome, res.Error)
		}
		if res.Pipeline != fingerprint.WindowPipelineID || res.FpcalcVersion != "1.6.1" || res.FFmpegVersion != "8.0.1" {
			t.Errorf("job %s: result stamps %+v", res.JobID, res)
		}
		if len(res.Windows) != len(testWindows) {
			t.Fatalf("job %s: %d windows", res.JobID, len(res.Windows))
		}
		for i, w := range res.Windows {
			if w.Window != testWindows[i] {
				t.Errorf("job %s: window %d = %+v, want the job's %+v echoed", res.JobID, i, w.Window, testWindows[i])
			}
			raw, _ := base64.StdEncoding.DecodeString(w.RawB64)
			if want := fakeRaw(contents[res.JobID], w.OffsetSec); string(raw) != string(want) || w.Frames != 100 {
				t.Errorf("job %s slot %d: wrong print", res.JobID, w.SlotBP)
			}
		}
	}
	if len(e.srv.releases) != 0 {
		t.Errorf("releases = %+v, want none (every job finished)", e.srv.releases)
	}
}

func TestRun_RefusesWritableMount(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.statMount = func(string) (MountInfo, error) {
		return MountInfo{FSType: "nfs", MountPoint: e.mount, ReadOnly: false}, nil
	}
	err := Run(context.Background(), make(chan struct{}), e.cfg)
	if !errors.Is(err, ErrWritableMount) {
		t.Fatalf("Run = %v, want ErrWritableMount", err)
	}
	if n := e.srv.leaseCallsSafe(); n != 0 {
		t.Errorf("leased %d times from a writable mount", n)
	}
}

// statfs claims read-only, but the directory accepts a write: the probe
// (check 3) must catch it and leave nothing behind.
func TestRun_WriteProbeCatchesWritableDir(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.writeProbe = nil // the real probe
	err := Run(context.Background(), make(chan struct{}), e.cfg)
	if !errors.Is(err, ErrWritableMount) {
		t.Fatalf("Run = %v, want ErrWritableMount", err)
	}
	ents, _ := os.ReadDir(e.mount)
	for _, de := range ents {
		if strings.HasPrefix(de.Name(), ".fp-worker-probe-") {
			t.Errorf("probe file %s left behind", de.Name())
		}
	}
}

func TestRun_RefusesNonNetworkFS(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.statMount = func(string) (MountInfo, error) {
		return MountInfo{FSType: "apfs", MountPoint: "/System/Volumes/Data", ReadOnly: true}, nil
	}
	if err := Run(context.Background(), make(chan struct{}), e.cfg); !errors.Is(err, ErrMountCheck) {
		t.Fatalf("Run = %v, want ErrMountCheck", err)
	}
}

func TestRun_ParityMismatchExits(t *testing.T) {
	e := newTestEnv(t)
	e.srv.hello.Calibration[0].Windows[1].RawSHA256 = strings.Repeat("0", 64)
	e.addJob("A/B/c.mp3", []byte("x"))
	err := Run(context.Background(), make(chan struct{}), e.cfg)
	if !errors.Is(err, ErrParity) {
		t.Fatalf("Run = %v, want ErrParity", err)
	}
	if !strings.Contains(err.Error(), "slot 5000") {
		t.Errorf("error does not name the mismatched window: %v", err)
	}
	if strings.Contains(err.Error(), e.mount) {
		t.Errorf("error leaks the local mount path: %v", err)
	}
	if n := e.srv.leaseCallsSafe(); n != 0 {
		t.Errorf("leased %d times after a parity mismatch", n)
	}
}

func TestRun_NoCalibrationRefuses(t *testing.T) {
	e := newTestEnv(t)
	e.srv.hello.Calibration = []workerapi.CalibrationFile{}
	if err := Run(context.Background(), make(chan struct{}), e.cfg); !errors.Is(err, ErrParity) {
		t.Fatalf("Run = %v, want ErrParity", err)
	}
}

func TestRun_CalibrationHeadMismatch(t *testing.T) {
	e := newTestEnv(t)
	e.srv.hello.Calibration[0].Head64K = strings.Repeat("a", 64)
	err := Run(context.Background(), make(chan struct{}), e.cfg)
	if !errors.Is(err, ErrMountCheck) || !strings.Contains(err.Error(), "first 64 KiB") {
		t.Fatalf("Run = %v, want a 64 KiB mismatch", err)
	}
}

func TestRun_ToolsNotAllowlisted(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.Versions = fingerprint.ToolVersionInfo{Fpcalc: "1.5.1", FFmpeg: "8.0.1"}
	if err := Run(context.Background(), make(chan struct{}), e.cfg); !errors.Is(err, ErrToolsNotAllowed) {
		t.Fatalf("Run = %v, want ErrToolsNotAllowed", err)
	}
}

func TestRun_UnknownRootRefused(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.Roots = map[string]string{"libroot": e.mount, "books": e.mount}
	if err := Run(context.Background(), make(chan struct{}), e.cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("Run = %v, want ErrConfig for a root the server does not offer", err)
	}
}

func TestRun_Lease409Exits(t *testing.T) {
	e := newTestEnv(t)
	e.srv.leaseScript = []int{http.StatusConflict}
	_, done := e.start()
	if err := waitDone(t, done); !errors.Is(err, ErrToolsNotAllowed) {
		t.Fatalf("Run = %v, want ErrToolsNotAllowed", err)
	}
}

func TestRun_BadKeyExits(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.APIKey = "bad-key-7f3a9c"
	err := Run(context.Background(), make(chan struct{}), e.cfg)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Run = %v, want ErrAuth", err)
	}
	if strings.Contains(err.Error(), e.cfg.APIKey) {
		t.Errorf("error contains the key: %v", err)
	}
}

func TestRun_BacksOffOn204And503And429(t *testing.T) {
	e := newTestEnv(t)
	e.srv.leaseScript = []int{http.StatusServiceUnavailable, http.StatusNoContent, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	e.addJob("A/B/c.mp3", []byte("content"))
	drain, done := e.start()
	e.waitResults(1, done)
	close(drain)
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := e.srv.leaseCallsSafe(); n < 5 {
		t.Errorf("lease called %d times, want >= 5 (4 scripted refusals then the job)", n)
	}
}

// SIGTERM drain: jobs in flight finish and post, jobs not started are
// released by ID, and Run returns nil.
func TestRun_DrainPostsInFlightAndReleasesRest(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.Concurrency = 2
	var jobs []workerapi.Job
	for i := range 5 {
		jobs = append(jobs, e.addJob(fmt.Sprintf("A/B/%d.mp3", i), []byte(strconv.Itoa(i))))
	}
	release := make(chan struct{})
	started := make(chan string, 100)
	var parityDone atomic.Bool
	e.cfg.Cut = func(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
		if parityDone.Load() {
			started <- path
			<-release
		}
		return fakeCut(ctx, path, spec)
	}
	e.cfg.statMount = func(string) (MountInfo, error) {
		// The first statMount calls are the startup gate; by the time a
		// lease is cut, parity is over.
		return MountInfo{FSType: "nfs", MountPoint: e.mount, ReadOnly: true}, nil
	}
	e.srv.mu.Lock()
	pending := e.srv.pending
	e.srv.pending = nil
	e.srv.mu.Unlock()

	drain, done := e.start()
	// Let the gate finish before serving jobs, so only job cuts block.
	for e.srv.leaseCallsSafe() == 0 {
		time.Sleep(time.Millisecond)
	}
	parityDone.Store(true)
	e.srv.mu.Lock()
	e.srv.pending = pending
	e.srv.mu.Unlock()

	for range 2 { // both slots busy on the first window of two jobs
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("jobs never started")
		}
	}
	close(drain)
	time.Sleep(20 * time.Millisecond) // the drain reaches the dispatch loop
	close(release)
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	if len(e.srv.results) != 2 {
		t.Fatalf("posted %d results, want the 2 in flight", len(e.srv.results))
	}
	for _, r := range e.srv.results {
		if r.Outcome != workerapi.OutcomeOK {
			t.Errorf("in-flight job %s: %s (%s)", r.JobID, r.Outcome, r.Error)
		}
	}
	if len(e.srv.releases) != 1 || len(e.srv.releases[0].JobIDs) != 3 {
		t.Fatalf("releases = %+v, want one release of the 3 unstarted jobs", e.srv.releases)
	}
	posted := map[string]bool{}
	for _, r := range e.srv.results {
		posted[r.JobID] = true
	}
	for _, id := range e.srv.releases[0].JobIDs {
		if posted[id] {
			t.Errorf("job %s both posted and released", id)
		}
	}
	if len(posted)+len(e.srv.releases[0].JobIDs) != len(jobs) {
		t.Errorf("posted %d + released %d != %d leased", len(posted), len(e.srv.releases[0].JobIDs), len(jobs))
	}
}

func (s *fakeServer) leaseCallsSafe() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseCalls
}

func TestRun_RenewsLongLease(t *testing.T) {
	e := newTestEnv(t)
	e.cfg.renewEvery = 5 * time.Millisecond
	var slow atomic.Bool
	e.cfg.Cut = func(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
		if slow.Load() {
			time.Sleep(30 * time.Millisecond)
		}
		return fakeCut(ctx, path, spec)
	}
	e.addJob("A/B/c.mp3", []byte("content"))
	slow.Store(true)
	drain, done := e.start()
	e.waitResults(1, done)
	close(drain)
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e.srv.mu.Lock()
	renews := e.srv.renewCalls
	e.srv.mu.Unlock()
	if renews == 0 {
		t.Error("lease never renewed")
	}
}

// A decomposed (NFD) name on disk is found for an NFC job path, and the
// reverse; the fallback runs only when the exact bytes do not exist.
func TestProcess_DecomposedNameResolves(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	nfc := norm.NFC.String("Beyoncé/Café Stories/01 Résumé.mp3")
	nfd := norm.NFD.String(nfc)
	if nfc == nfd {
		t.Fatal("test names are not normalization-sensitive")
	}
	content := []byte("decomposed file")
	fi := e.writeFile(nfd, content)

	res := w.process(context.Background(), e.job(nfc, fi))
	if res.Outcome != workerapi.OutcomeOK {
		t.Fatalf("NFC job for an NFD file: %s (%s)", res.Outcome, res.Error)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.Windows[0].RawB64)
	if string(raw) != string(fakeRaw(content, 0)) {
		t.Error("NFC job cut the wrong file")
	}

	missing := w.process(context.Background(), e.job(norm.NFC.String("Nobody/Épée.mp3"), fi))
	if missing.Outcome != workerapi.OutcomeNotFound {
		t.Errorf("no spelling exists: %s, want not_found", missing.Outcome)
	}
}

// Both spellings exist as different files: each job resolves to its own; an
// alternate-spelling hit with a different size answers stale, never ok.
func TestProcess_NFCAndNFDStayDistinct(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	nfc := norm.NFC.String("Zoë/Noël.mp3")
	nfd := norm.NFD.String(nfc)
	fiC := e.writeFile(nfc, []byte("composed file, longer"))
	if _, err := os.Lstat(filepath.Join(e.mount, nfd)); err == nil {
		t.Skip("this filesystem is normalization-insensitive (APFS/HFS+): NFC and NFD name one file")
	}
	fiD := e.writeFile(nfd, []byte("decomposed"))

	for _, c := range []struct {
		rel     string
		fi      os.FileInfo
		content string
	}{{nfc, fiC, "composed file, longer"}, {nfd, fiD, "decomposed"}} {
		res := w.process(context.Background(), e.job(c.rel, c.fi))
		if res.Outcome != workerapi.OutcomeOK {
			t.Fatalf("%q: %s (%s)", c.rel, res.Outcome, res.Error)
		}
		raw, _ := base64.StdEncoding.DecodeString(res.Windows[0].RawB64)
		if string(raw) != string(fakeRaw([]byte(c.content), 0)) {
			t.Errorf("%q cut the other spelling's file", c.rel)
		}
	}

	// Only the NFD file remains; an NFC job whose server-side file had a
	// different size must come back stale.
	if err := os.Remove(filepath.Join(e.mount, nfc)); err != nil {
		t.Fatal(err)
	}
	res := w.process(context.Background(), e.job(nfc, fiC))
	if res.Outcome != workerapi.OutcomeStale {
		t.Errorf("alternate spelling with another size: %s, want stale", res.Outcome)
	}
}

func TestProcess_RejectsTraversalAndSymlinkEscape(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.mp3")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(secret)
	if err := os.Symlink(outside, filepath.Join(e.mount, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(e.mount, "link.mp3")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../secret.mp3", "a/../../secret.mp3", "/etc/passwd", "a\x00b", "escape/secret.mp3", "link.mp3", ""} {
		res := w.process(context.Background(), e.job(rel, fi))
		if res.Outcome != workerapi.OutcomeRejected {
			t.Errorf("%q: %s (%s), want rejected", rel, res.Outcome, res.Error)
		}
		if strings.Contains(res.Error, e.mount) || strings.Contains(res.Error, outside) {
			t.Errorf("%q: error leaks a local path: %s", rel, res.Error)
		}
	}
	j := e.job("x.mp3", fi)
	j.Root = "books"
	if res := w.process(context.Background(), j); res.Outcome != workerapi.OutcomeRejected {
		t.Errorf("unconfigured root: %s, want rejected", res.Outcome)
	}
}

func TestProcess_StaleAndErrorClasses(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	fi := e.writeFile("a.mp3", []byte("abc"))
	j := e.job("a.mp3", fi)
	j.Size++
	if res := w.process(context.Background(), j); res.Outcome != workerapi.OutcomeStale {
		t.Errorf("size mismatch: %s, want stale", res.Outcome)
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", context.DeadlineExceeded), workerapi.OutcomeTimeout},
		{fmt.Errorf("%w: %w: EIO", fingerprint.ErrWindowFFmpeg, fingerprint.ErrWindowTransient), workerapi.OutcomeTimeout},
		{fmt.Errorf("%w: bad data", fingerprint.ErrWindowFFmpeg), workerapi.OutcomeDecodeError},
		{fmt.Errorf("x: %w", fingerprint.ErrFingerprintTooShort), workerapi.OutcomeDecodeError},
		{fingerprint.ErrWindowShortDecode, workerapi.OutcomeDecodeError},
	} {
		calls := 0
		w.cfg.Cut = func(context.Context, string, fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
			calls++
			return nil, c.err
		}
		res := w.process(context.Background(), e.job("a.mp3", fi))
		if res.Outcome != c.want {
			t.Errorf("%v: %s, want %s", c.err, res.Outcome, c.want)
		}
		if len(res.Windows) != 0 {
			t.Errorf("%v: failed result carries windows", c.err)
		}
		if errors.Is(c.err, fingerprint.ErrWindowTransient) && calls != 2 {
			t.Errorf("transient failure tried %d times, want 2", calls)
		}
	}
}

func TestBatchAndPost_SplitsBySize(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	w.maxBatchBytes = 3000
	results := make(chan workerapi.JobResult, 10)
	for i := range 10 {
		results <- workerapi.JobResult{JobID: fmt.Sprintf("J%d", i), Ref: "f:x", Outcome: workerapi.OutcomeOK,
			Windows: []workerapi.WindowResult{{RawB64: strings.Repeat("A", 800)}}}
	}
	close(results)
	w.batchAndPost(context.Background(), &leaseState{id: "L1"}, results)
	if e.srv.nResults() != 10 {
		t.Fatalf("posted %d results, want 10", e.srv.nResults())
	}
	e.srv.mu.Lock()
	posts := e.srv.resultPosts
	e.srv.mu.Unlock()
	if posts < 4 {
		t.Errorf("%d posts for 10 x ~900 B under a 3000 B cap, want >= 4", posts)
	}
}

// newTestWorker builds a worker for process/batch tests without the gate.
func newTestWorker(t *testing.T, e *testEnv) *worker {
	t.Helper()
	cfg := e.cfg
	cfg.applyDefaults()
	base, err := checkServerURL(cfg.ServerURL)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := resolveRoots(cfg.Roots)
	if err != nil {
		t.Fatal(err)
	}
	w := &worker{cfg: cfg, api: &apiClient{base: base, key: cfg.APIKey, hc: cfg.HTTPClient}, roots: roots,
		sem: make(chan struct{}, 1), wake: make(chan struct{}, 1), recheckC: make(chan struct{}, 1),
		stop: make(chan struct{}), maxBatchBytes: workerapi.MaxBodyBytes - bodyMargin}
	w.tally.statuses = map[string]int{}
	w.tally.outcomes = map[string]int{}
	return w
}

func TestLoadAPIKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	noEnv := func(string) string { return "" }

	if k, err := LoadAPIKey(write("ok", "secret-value\n", 0o600), noEnv); err != nil || k != "secret-value" {
		t.Errorf("0600 file: %q, %v", k, err)
	}
	if _, err := LoadAPIKey(write("ro", "secret-value", 0o400), noEnv); err != nil {
		t.Errorf("0400 file refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660} {
		_, err := LoadAPIKey(write(fmt.Sprintf("m%o", mode), "secret-value", mode), noEnv)
		if !errors.Is(err, ErrConfig) {
			t.Errorf("mode %#o accepted: %v", mode, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("mode %#o: error contains the key", mode)
		}
	}
	if _, err := LoadAPIKey(write("multi", "a=1\nb=2\n", 0o600), noEnv); !errors.Is(err, ErrConfig) {
		t.Errorf("multi-line file accepted: %v", err)
	}
	if _, err := LoadAPIKey(write("empty", "\n", 0o600), noEnv); !errors.Is(err, ErrConfig) {
		t.Errorf("empty file accepted: %v", err)
	}
	if _, err := LoadAPIKey(dir, noEnv); !errors.Is(err, ErrConfig) {
		t.Errorf("directory accepted: %v", err)
	}
	env := func(k string) string {
		if k == KeyEnvVar {
			return " env-key \n"
		}
		return ""
	}
	if k, err := LoadAPIKey("", env); err != nil || k != "env-key" {
		t.Errorf("env: %q, %v", k, err)
	}
	if _, err := LoadAPIKey("", noEnv); !errors.Is(err, ErrConfig) {
		t.Errorf("no key at all: %v", err)
	}
}

func TestParseRootsAndServerURL(t *testing.T) {
	m, err := ParseRoots([]string{"libroot=/Volumes/lib/audiobooks", " books = /Volumes/lib"})
	if err != nil || m["libroot"] != "/Volumes/lib/audiobooks" || m["books"] != "/Volumes/lib" {
		t.Errorf("ParseRoots = %v, %v", m, err)
	}
	for _, bad := range [][]string{{"libroot"}, {"=/x"}, {"libroot=rel/path"}, {"a=/x", "a=/y"}} {
		if _, err := ParseRoots(bad); !errors.Is(err, ErrConfig) {
			t.Errorf("ParseRoots(%q) = %v, want ErrConfig", bad, err)
		}
	}
	for _, ok := range []string{"https://192.0.2.10:8484", "http://127.0.0.1:9", "http://localhost:1", "https://library.example/"} {
		if _, err := checkServerURL(ok); err != nil {
			t.Errorf("checkServerURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"http://192.0.2.10:8484", "ftp://x", "not a url", "/relative"} {
		if _, err := checkServerURL(bad); err == nil {
			t.Errorf("checkServerURL(%q) accepted", bad)
		}
	}
	if !ValidWorkerID("mac-studio.local") || ValidWorkerID("has space") || ValidWorkerID("") {
		t.Error("ValidWorkerID")
	}
}

func TestComponentSpellings(t *testing.T) {
	nfc := norm.NFC.String("Café")
	if got := componentSpellings(nfc); len(got) != 2 || got[0] != nfc || got[1] != norm.NFD.String(nfc) {
		t.Errorf("spellings(NFC) = %q", got)
	}
	if got := componentSpellings("plain ascii"); len(got) != 1 {
		t.Errorf("spellings(ascii) = %q", got)
	}
	if _, ok, err := unicodeAlternatePath(t.TempDir(), "bad\xffbyte"); ok || err != nil {
		t.Errorf("invalid UTF-8 got an alternate: %v %v", ok, err)
	}
}

// I/O failures on the mount (EIO, ESTALE, ETIMEDOUT) are not a verdict on
// the file: they come back as timeout (re-queued, not sent to the server
// lane) and count toward the mount re-check streak.
func TestProcess_IOErrorsAreTimeoutAndTriggerRecheck(t *testing.T) {
	e := newTestEnv(t)
	fi := e.writeFile("a.mp3", []byte("abc"))
	for _, c := range []struct {
		name string
		set  func(*Config)
	}{
		{"resolve", func(c *Config) {
			c.joinRoot = func(string, string) (string, error) {
				return "", &fs.PathError{Op: "lstat", Path: e.mount + "/a.mp3", Err: syscall.EIO}
			}
		}},
		{"open", func(c *Config) {
			c.openFile = func(p string) (*os.File, error) {
				return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.ESTALE}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.set(&e.cfg)
			defer func() { e.cfg.joinRoot, e.cfg.openFile = nil, nil }()
			w := newTestWorker(t, e)
			for i := 0; i <= notFoundRecheckAfter; i++ {
				res := w.process(context.Background(), e.job("a.mp3", fi))
				if res.Outcome != workerapi.OutcomeTimeout {
					t.Fatalf("I/O error: %s (%s), want timeout", res.Outcome, res.Error)
				}
				if strings.Contains(res.Error, e.mount) {
					t.Errorf("error leaks the mount path: %s", res.Error)
				}
			}
			select {
			case <-w.recheckC:
			default:
				t.Error("a run of I/O errors did not request a mount re-check")
			}
		})
	}
}

// The file is re-statted after the cut: a change while cutting is stale.
func TestProcess_FileChangedDuringCutIsStale(t *testing.T) {
	e := newTestEnv(t)
	p := filepath.Join(e.mount, "grow.mp3")
	fi := e.writeFile("grow.mp3", []byte("before"))
	e.cfg.Cut = func(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
		wp, err := fakeCut(ctx, path, spec)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		_, _ = f.WriteString(" and after")
		_ = f.Close()
		return wp, err
	}
	w := newTestWorker(t, e)
	if res := w.process(context.Background(), e.job("grow.mp3", fi)); res.Outcome != workerapi.OutcomeStale {
		t.Errorf("file grew during the cut: %s (%s), want stale", res.Outcome, res.Error)
	}
}

// After the last failed attempt post returns at once rather than sleeping.
func TestPost_NoSleepAfterFinalAttempt(t *testing.T) {
	var mu sync.Mutex
	var last time.Time
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		last = time.Now()
		mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()
	e := newTestEnv(t)
	e.cfg.ServerURL = ts.URL
	// Four waits of 200-400 ms sit between the five attempts; a wait after
	// the last attempt would add at least 200 ms more.
	e.cfg.backoffMin, e.cfg.backoffMax = 400*time.Millisecond, 400*time.Millisecond
	w := newTestWorker(t, e)
	w.post(context.Background(), &leaseState{id: "L1"}, []workerapi.JobResult{{JobID: "J1", Outcome: workerapi.OutcomeOK}})
	mu.Lock()
	defer mu.Unlock()
	if attempts != 5 {
		t.Fatalf("%d attempts, want 5", attempts)
	}
	if d := time.Since(last); d > 150*time.Millisecond {
		t.Errorf("post returned %s after its last attempt: it slept after the final failure", d)
	}
}

// A drain during the parity gate stops it and Run returns nil.
func TestRun_DrainDuringParity(t *testing.T) {
	e := newTestEnv(t)
	inCut := make(chan struct{}, 10)
	e.cfg.Cut = func(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
		inCut <- struct{}{}
		<-ctx.Done()
		return nil, fmt.Errorf("fingerprint window: %w", ctx.Err())
	}
	drain, done := e.start()
	select {
	case <-inCut:
	case <-time.After(10 * time.Second):
		t.Fatal("parity never started")
	}
	close(drain)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Run = %v, want nil after a drain", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parity gate ignored the drain")
	}
	if n := e.srv.leaseCallsSafe(); n != 0 {
		t.Errorf("leased %d times after draining during parity", n)
	}
}

// A directory and the file in it can be in different normalization forms;
// resolution works component by component.
func TestProcess_MixedFormComponents(t *testing.T) {
	e := newTestEnv(t)
	w := newTestWorker(t, e)
	dirNFD := norm.NFD.String("Émile Zola")
	fileNFC := norm.NFC.String("Thérèse Raquin.mp3")
	content := []byte("mixed forms")
	fi := e.writeFile(dirNFD+"/"+fileNFC, content)
	for _, rel := range []string{
		norm.NFC.String(dirNFD) + "/" + fileNFC,                  // all NFC
		dirNFD + "/" + norm.NFD.String(fileNFC),                  // all NFD
		norm.NFC.String(dirNFD) + "/" + norm.NFD.String(fileNFC), // both flipped
	} {
		res := w.process(context.Background(), e.job(rel, fi))
		if res.Outcome != workerapi.OutcomeOK {
			t.Errorf("%q: %s (%s)", rel, res.Outcome, res.Error)
		}
	}
}
