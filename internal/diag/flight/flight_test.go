// file: internal/diag/flight/flight_test.go
// version: 1.0.0
// guid: f26518cf-c820-4b1a-a14b-c616781751e9
// last-edited: 2026-10-14

package flight

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/trace"
	"sync"
	"testing"
	"time"
)

// Only one flight recorder may be active per process, so no test here runs in
// parallel and every test that starts one stops it in Cleanup.

// stepClock is an injectable clock the test advances by hand.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStepClock() *stepClock {
	return &stepClock{now: time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC)}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// startRecorder starts a recorder on a temp dir with a stepped clock.
func startRecorder(t *testing.T, cfg Config) (*Recorder, *stepClock) {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(t.TempDir(), "traces")
	}
	r := New(cfg)
	clk := newStepClock()
	r.clock = clk.Now
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	return r, clk
}

func traceFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".trace" {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestSnapshotWritesOpenableTrace(t *testing.T) {
	r, _ := startRecorder(t, Config{})
	// Give the recorder something to hold.
	for i := 0; i < 1000; i++ {
		_ = make([]byte, 1024)
	}
	r.Snapshot("test-openable")
	r.inflight.Wait()

	files := traceFiles(t, r.dir)
	if len(files) != 1 {
		t.Fatalf("want 1 trace file, got %v", files)
	}
	if !filePattern.MatchString(files[0]) {
		t.Fatalf("file name %q does not match the snapshot pattern", files[0])
	}
	path := filepath.Join(r.dir, files[0])
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 || !bytes.HasPrefix(data, []byte("go 1.")) {
		t.Fatalf("not a Go execution trace: %d bytes, prefix %q", len(data), data[:min(len(data), 16)])
	}

	if testing.Short() {
		return
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Log("go not on PATH; skipping go tool trace check")
		return
	}
	out, err := exec.Command(goBin, "tool", "trace", "-pprof=sched", path).CombinedOutput()
	if err != nil {
		t.Fatalf("go tool trace -pprof=sched: %v\n%s", err, out)
	}
	t.Logf("go tool trace -pprof=sched: exit 0, %d bytes of profile", len(out))
}

func TestSnapshotRateLimitedPerReason(t *testing.T) {
	r, clk := startRecorder(t, Config{})

	r.Snapshot("alpha")
	r.inflight.Wait()
	clk.Advance(time.Minute) // well inside the 5m gap
	r.Snapshot("alpha")
	r.inflight.Wait()
	if got := len(traceFiles(t, r.dir)); got != 1 {
		t.Fatalf("second alpha inside MinGap: want 1 file, got %d", got)
	}

	// A different reason is not limited by alpha's gap.
	r.Snapshot("beta")
	r.inflight.Wait()
	if got := len(traceFiles(t, r.dir)); got != 2 {
		t.Fatalf("beta: want 2 files, got %d", got)
	}

	// Past the gap, alpha is allowed again.
	clk.Advance(5 * time.Minute)
	r.Snapshot("alpha")
	r.inflight.Wait()
	if got := len(traceFiles(t, r.dir)); got != 3 {
		t.Fatalf("alpha after MinGap: want 3 files, got %d", got)
	}
}

func TestSnapshotRotatesToMaxFiles(t *testing.T) {
	r, clk := startRecorder(t, Config{})
	// A file that is not a snapshot must survive rotation.
	other := filepath.Join(r.dir, "keep-me.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var first string
	for i := 0; i < 12; i++ {
		r.Snapshot("rotate")
		r.inflight.Wait()
		if i == 0 {
			first = traceFiles(t, r.dir)[0]
		}
		clk.Advance(6 * time.Minute)
	}
	files := traceFiles(t, r.dir)
	if len(files) != 10 {
		t.Fatalf("want 10 files after 12 snapshots, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if f == first {
			t.Fatalf("oldest snapshot %s survived rotation", first)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("rotation touched a non-snapshot file: %v", err)
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 10 {
		t.Fatalf("List: want 10, got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name <= list[i].Name {
			t.Fatalf("List not newest first: %s before %s", list[i-1].Name, list[i].Name)
		}
	}
}

func TestConcurrentSnapshotsDoNotBlock(t *testing.T) {
	r, _ := startRecorder(t, Config{})

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { r.Snapshot("burst") })
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("20 Snapshot calls took %s; they must return without waiting on disk", elapsed)
	}
	r.inflight.Wait()
	if got := len(traceFiles(t, r.dir)); got > 1 {
		t.Fatalf("want at most 1 file from a burst, got %d", got)
	}
}

func TestDisabledRecorderIsNoop(t *testing.T) {
	t.Setenv(EnvSwitch, "off")
	dir := filepath.Join(t.TempDir(), "traces")
	r := New(Config{Dir: dir})
	if err := r.Start(); err != nil {
		t.Fatalf("Start on a disabled recorder: %v", err)
	}
	r.Snapshot("anything")
	r.Stop()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disabled recorder created %s (stat err %v)", dir, err)
	}
	list, err := r.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("List on a disabled recorder: %v, %v", list, err)
	}

	// A disabled recorder never takes the process-wide recorder slot, so
	// another one can still start.
	t.Setenv(EnvSwitch, "")
	live, _ := startRecorder(t, Config{})
	if !live.fr.Enabled() {
		t.Fatal("a recorder after a disabled one did not start")
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	good := "20261014T010203Z-test.trace"
	if err := os.WriteFile(filepath.Join(dir, good), []byte("go 1.27 trace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "x"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Dir: dir, Disabled: true})

	for _, name := range []string{
		"../x", "a/b", "", "20261014T010203Z-test.txt", "/etc/passwd",
		"../" + good, "20261014T010203Z-TEST.trace", "20261014T010203Z-test.trace/",
	} {
		f, err := r.Open(name)
		if err == nil {
			_ = f.Close()
			t.Errorf("Open(%q) succeeded; want rejection", name)
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) = %v; want fs.ErrNotExist", name, err)
		}
	}

	f, err := r.Open(good)
	if err != nil {
		t.Fatalf("Open(%q): %v", good, err)
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(f)
	if string(b) != "go 1.27 trace" {
		t.Fatalf("read %q", b)
	}
}

func TestNilRecorderSafe(t *testing.T) {
	var r *Recorder
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	r.Snapshot("x")
	if list, err := r.List(); err != nil || list != nil {
		t.Fatalf("List: %v %v", list, err)
	}
	if _, err := r.Open("20261014T010203Z-x.trace"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open: %v", err)
	}
	r.Stop()
}

func TestStopWaitsForSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "traces")
	r := New(Config{Dir: dir})
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	r.Snapshot("at-stop")
	r.Stop()
	if r.snapshotting.Load() {
		t.Fatal("Stop returned with a snapshot still in flight")
	}
	files := traceFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("want the in-flight snapshot on disk after Stop, got %v", files)
	}
	// After Stop, Snapshot is a no-op and Stop is idempotent.
	r.Snapshot("after-stop")
	r.Stop()
	if got := len(traceFiles(t, dir)); got != 1 {
		t.Fatalf("Snapshot after Stop wrote a file: %d", got)
	}
	if r.fr.Enabled() {
		t.Fatal("recorder still enabled after Stop")
	}
}

func TestInvalidReasonBecomesUnknown(t *testing.T) {
	r, _ := startRecorder(t, Config{})
	r.Snapshot("../Bad Reason")
	r.inflight.Wait()
	files := traceFiles(t, r.dir)
	if len(files) != 1 || !bytes.HasSuffix([]byte(files[0]), []byte("-unknown.trace")) {
		t.Fatalf("want one -unknown.trace file, got %v", files)
	}
}

// The package doc of runtime/trace allows one flight recorder alongside one
// trace.Start consumer. The pprof /debug/pprof/trace endpoint is a
// trace.Start consumer, so this is the coexistence 06-P3 depends on, checked
// directly (the tagged flight_pprof_test.go drives the real handler).
func TestRecorderCoexistsWithTraceStart(t *testing.T) {
	r, _ := startRecorder(t, Config{})
	if err := trace.Start(io.Discard); err != nil {
		t.Fatalf("trace.Start with an active flight recorder: %v", err)
	}
	r.Snapshot("coexist")
	r.inflight.Wait()
	trace.Stop()
	if got := len(traceFiles(t, r.dir)); got != 1 {
		t.Fatalf("snapshot during trace.Start: want 1 file, got %d", got)
	}
}
