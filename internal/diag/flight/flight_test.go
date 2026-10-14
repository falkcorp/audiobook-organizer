// file: internal/diag/flight/flight_test.go
// version: 1.1.0
// guid: f26518cf-c820-4b1a-a14b-c616781751e9
// last-edited: 2026-10-14

package flight

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/trace"
	"strings"
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
	// New reads the off switch, so a developer or CI environment that exports
	// ABK_FLIGHT_RECORDER=off must not turn every test here into a no-op.
	t.Setenv(EnvSwitch, "")
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
	return filesWithSuffix(t, dir, traceSuffix)
}

func dumpFiles(t *testing.T, dir string) []string {
	t.Helper()
	return filesWithSuffix(t, dir, gorSuffix)
}

func filesWithSuffix(t *testing.T, dir, suffix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// snap requests a snapshot and waits for its write to finish.
func snap(r *Recorder, reason string) {
	r.Snapshot(reason)
	r.inflight.Wait()
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
	// Pinning is off here so rotation alone is measured.
	r, clk := startRecorder(t, Config{MaxPinned: -1})
	// A file that is not a snapshot must survive rotation.
	other := filepath.Join(r.dir, "keep-me.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var first string
	for i := 0; i < 12; i++ {
		snap(r, "rotate")
		if i == 0 {
			first = traceFiles(t, r.dir)[0]
		}
		clk.Advance(3 * time.Hour) // past the 2h backoff ceiling
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
	// MaxFiles counts snapshots: every trace keeps its goroutine dump, and a
	// rotated trace takes its dump with it.
	dumps := dumpFiles(t, r.dir)
	if len(dumps) != 10 {
		t.Fatalf("want 10 goroutine dumps, got %d: %v", len(dumps), dumps)
	}
	for i, f := range files {
		if want := strings.TrimSuffix(f, traceSuffix) + gorSuffix; dumps[i] != want {
			t.Fatalf("dump %q does not pair with trace %q", dumps[i], f)
		}
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 20 {
		t.Fatalf("List: want 20 (10 traces + 10 dumps), got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name <= list[i].Name {
			t.Fatalf("List not newest first: %s before %s", list[i-1].Name, list[i].Name)
		}
	}
}

func TestSnapshotWritesGoroutineDump(t *testing.T) {
	r, _ := startRecorder(t, Config{})
	snap(r, "dump-check")

	traces, dumps := traceFiles(t, r.dir), dumpFiles(t, r.dir)
	if len(traces) != 1 || len(dumps) != 1 {
		t.Fatalf("want one trace and one dump, got %v and %v", traces, dumps)
	}
	if got, want := dumps[0], strings.TrimSuffix(traces[0], traceSuffix)+gorSuffix; got != want {
		t.Fatalf("dump %q is not the trace's sibling %q", got, want)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, dumps[0]))
	if err != nil {
		t.Fatal(err)
	}
	// debug=2 prints "goroutine N [state]:" followed by the stack, and this
	// test's own goroutine is in it.
	if !bytes.Contains(data, []byte("goroutine ")) || !bytes.Contains(data, []byte("TestSnapshotWritesGoroutineDump")) {
		t.Fatalf("not a full goroutine dump (%d bytes)", len(data))
	}

	// Both files are served by Open and listed.
	for _, name := range []string{traces[0], dumps[0]} {
		f, err := r.Open(name)
		if err != nil {
			t.Fatalf("Open(%q): %v", name, err)
		}
		_ = f.Close()
	}
	list, _ := r.List()
	if len(list) != 2 {
		t.Fatalf("List: want the trace and the dump, got %v", list)
	}
}

// A repeat of one reason waits twice as long each time: 5m, 10m, 20m, 40m,
// 80m, then 2h and no more. Each step is probed one second early (dropped)
// and then on the dot (allowed).
func TestSnapshotBackoffDoublesToCeiling(t *testing.T) {
	r, clk := startRecorder(t, Config{MaxFiles: 50})
	snap(r, "flap")
	want := 1
	for _, gap := range []time.Duration{
		5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute,
		80 * time.Minute, 2 * time.Hour, 2 * time.Hour,
	} {
		clk.Advance(gap - time.Second)
		snap(r, "flap")
		if got := len(traceFiles(t, r.dir)); got != want {
			t.Fatalf("a second before the %s gap: want %d files, got %d", gap, want, got)
		}
		clk.Advance(time.Second)
		snap(r, "flap")
		want++
		if got := len(traceFiles(t, r.dir)); got != want {
			t.Fatalf("at the %s gap: want %d files, got %d", gap, want, got)
		}
	}
}

// Once a reason has been quiet for as long as its current gap, the next
// request starts over at MinGap instead of continuing the escalation.
func TestSnapshotBackoffResetsAfterQuiet(t *testing.T) {
	r, clk := startRecorder(t, Config{MaxFiles: 50})
	snap(r, "flap")
	for _, gap := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute} {
		// The early probe keeps the reason "not quiet", as a stall that
		// keeps re-firing does; without it the silence would reset the gap.
		clk.Advance(gap - time.Second)
		snap(r, "flap")
		clk.Advance(time.Second)
		snap(r, "flap")
	}
	if got := len(traceFiles(t, r.dir)); got != 4 {
		t.Fatalf("setup: want 4 files, got %d", got)
	}
	// The gap is now 40m. 45m of silence is a quiet period at least that long.
	clk.Advance(45 * time.Minute)
	snap(r, "flap")
	if got := len(traceFiles(t, r.dir)); got != 5 {
		t.Fatalf("after a quiet period: want 5 files, got %d", got)
	}
	// Reset: the next gap is 5m, not the 80m the escalation would have reached.
	clk.Advance(5*time.Minute - time.Second)
	snap(r, "flap")
	if got := len(traceFiles(t, r.dir)); got != 5 {
		t.Fatalf("just inside the reset gap: want 5 files, got %d", got)
	}
	clk.Advance(time.Second)
	snap(r, "flap")
	if got := len(traceFiles(t, r.dir)); got != 6 {
		t.Fatalf("at the reset gap: want 6 files, got %d (the backoff did not reset)", got)
	}
}

// A stall that keeps re-firing is not quiet even while every request is
// dropped, so the gap keeps growing instead of resetting.
func TestSnapshotBackoffNotQuietWhileRequestsContinue(t *testing.T) {
	r, clk := startRecorder(t, Config{MaxFiles: 50})
	snap(r, "flap")
	clk.Advance(4 * time.Minute)
	snap(r, "flap") // dropped, but the reason is still being requested
	clk.Advance(time.Minute)
	snap(r, "flap") // second snapshot; the gap is now 10m
	for i := 0; i < 19; i++ {
		clk.Advance(30 * time.Second)
		snap(r, "flap") // a tick every 30s for 9m30s: all inside the gap
	}
	if got := len(traceFiles(t, r.dir)); got != 2 {
		t.Fatalf("want 2 files while requests keep arriving inside the gap, got %d", got)
	}
	clk.Advance(30 * time.Second)
	snap(r, "flap") // 10m after the second: allowed, and the gap becomes 20m
	clk.Advance(10*time.Minute + 30*time.Second)
	snap(r, "flap")
	if got := len(traceFiles(t, r.dir)); got != 3 {
		t.Fatalf("a 20m gap must still be in force 10m30s after the third snapshot: want 3 files, got %d", got)
	}
}

// The first snapshot of each reason is pinned: rotation keeps it however many
// later snapshots push through, and pinned pairs are capped.
func TestFirstSnapshotPerReasonIsPinned(t *testing.T) {
	r, clk := startRecorder(t, Config{MaxFiles: 3, MaxPinned: 2})
	snap(r, "alpha")
	alpha := traceFiles(t, r.dir)[0]
	clk.Advance(3 * time.Hour)
	snap(r, "beta")
	beta := traceFiles(t, r.dir)
	clk.Advance(3 * time.Hour)
	snap(r, "gamma") // over the pin cap of 2: an ordinary snapshot
	gamma := strings.TrimSuffix(traceFiles(t, r.dir)[2], traceSuffix)
	for i := 0; i < 8; i++ {
		clk.Advance(3 * time.Hour)
		snap(r, "alpha")
	}

	files := traceFiles(t, r.dir)
	has := func(name string) bool {
		for _, f := range files {
			if f == name {
				return true
			}
		}
		return false
	}
	if !has(alpha) {
		t.Fatalf("pinned first alpha snapshot %s was rotated away: %v", alpha, files)
	}
	if len(beta) != 2 || !has(beta[1]) {
		t.Fatalf("pinned first beta snapshot was rotated away: %v", files)
	}
	if has(gamma + traceSuffix) {
		t.Fatalf("gamma was past the pin cap and old enough to rotate, but survived: %v", files)
	}
	// 2 pinned + MaxFiles unpinned; each with its dump.
	if len(files) != 5 || len(dumpFiles(t, r.dir)) != 5 {
		t.Fatalf("want 2 pinned + 3 unpinned pairs, got %d traces and %d dumps", len(files), len(dumpFiles(t, r.dir)))
	}
}

// A snapshot that fails to write does not rate limit its reason: the next
// strike tries again at once.
func TestFailedWriteDoesNotStampReason(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root process can write into a read-only directory")
	}
	r, _ := startRecorder(t, Config{})
	if err := os.Chmod(r.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.dir, 0o750) })
	snap(r, "flaky")
	if got := len(traceFiles(t, r.dir)); got != 0 {
		t.Fatalf("write into a read-only dir produced %d files", got)
	}
	if err := os.Chmod(r.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	snap(r, "flaky") // same instant: a failed write must not have used up the gap
	if got := len(traceFiles(t, r.dir)); got != 1 {
		t.Fatalf("retry after a failed write: want 1 file, got %d", got)
	}
}

// A request that arrives while another reason's write is in flight is not
// lost: the file it produces carries both reasons in its name, both reasons
// are rate limited afterwards, and the pair keeps its dump.
func TestInFlightWriteRecordsOtherReason(t *testing.T) {
	r, clk := startRecorder(t, Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	r.afterTrace = func() { close(entered); <-release }
	var drops []string
	var dmu sync.Mutex
	r.dropf = func(format string, args ...any) {
		dmu.Lock()
		drops = append(drops, format)
		dmu.Unlock()
	}

	r.Snapshot("watchdog-stuck")
	<-entered
	r.Snapshot("search-index-stall")
	r.Snapshot("search-index-stall") // a duplicate is not recorded twice
	close(release)
	r.inflight.Wait()

	files := traceFiles(t, r.dir)
	if len(files) != 1 || !strings.HasSuffix(files[0], "-watchdog-stuck-and-search-index-stall.trace") {
		t.Fatalf("want one file covering both reasons, got %v", files)
	}
	if dumps := dumpFiles(t, r.dir); len(dumps) != 1 || !strings.HasPrefix(dumps[0], strings.TrimSuffix(files[0], traceSuffix)) {
		t.Fatalf("the dump must carry the same name: %v", dumps)
	}
	dmu.Lock()
	n := len(drops)
	dmu.Unlock()
	if n != 1 {
		t.Fatalf("the repeat inside the drop-log interval must not log again: %d lines", n)
	}

	// The covered reason counts as snapshotted: it is rate limited now.
	clk.Advance(time.Minute)
	snap(r, "search-index-stall")
	if got := len(traceFiles(t, r.dir)); got != 1 {
		t.Fatalf("covered reason was not rate limited: %d files", got)
	}
}

// Dropped requests log one Info line per kind and reason per interval, not
// one per request.
func TestDroppedRequestsLogRateLimited(t *testing.T) {
	r, clk := startRecorder(t, Config{})
	var lines []string
	var mu sync.Mutex
	r.dropf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(lines) }

	snap(r, "noisy")
	for i := 0; i < 10; i++ {
		r.Snapshot("noisy") // rate limited, 10 requests in a burst
	}
	if got := count(); got != 1 {
		t.Fatalf("10 rate-limited requests: want 1 log line, got %d: %v", got, lines)
	}
	clk.Advance(dropLogEvery)
	r.Snapshot("noisy")
	if got := count(); got != 2 {
		t.Fatalf("after the interval: want a second line, got %d", got)
	}
	r.Snapshot("other-reason") // different key, in-flight is not involved: written, not dropped
	r.inflight.Wait()
	if got := count(); got != 2 {
		t.Fatalf("a written snapshot must not log a drop: %d", got)
	}

	r.Stop()
	r.Snapshot("after-stop")
	r.Snapshot("after-stop")
	if got := count(); got != 3 {
		t.Fatalf("a request after Stop: want one more line, got %d: %v", got, lines)
	}
	if !strings.Contains(lines[2], "not recording") {
		t.Fatalf("the stop drop says %q", lines[2])
	}
}

// Past the wait cap Stop returns without stopping the runtime recorder under a
// WriteTo that has not finished (a data race in go1.27.2).
func TestStopTimeoutLeavesRecorderRunning(t *testing.T) {
	r, _ := startRecorder(t, Config{})
	r.stopWait = 50 * time.Millisecond
	entered, release := make(chan struct{}), make(chan struct{})
	r.afterTrace = func() { close(entered); <-release }
	r.Snapshot("slow")
	<-entered

	start := time.Now()
	r.Stop()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Stop held shutdown for %s", elapsed)
	}
	if !r.fr.Enabled() {
		t.Fatal("Stop timed out but stopped the runtime recorder under a live write")
	}
	close(release)
	r.inflight.Wait()
	r.fr.Stop() // free the process-wide slot for the next test
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
	t.Setenv(EnvSwitch, "")
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

// A file named like a snapshot that is a link out of Dir is not served: the
// recorder never writes links, and Open must not follow one.
func TestOpenRefusesSymlinkOutOfDir(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	const name = "20261014T010203Z-evil.trace"
	if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	r := New(Config{Dir: dir, Disabled: true})
	f, err := r.Open(name)
	if err == nil {
		_ = f.Close()
		t.Fatal("Open followed a symlink out of Dir")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(symlink) = %v; want fs.ErrNotExist", err)
	}
	if list, _ := r.List(); len(list) != 0 {
		t.Fatalf("List shows a symlink as a snapshot: %v", list)
	}
}
