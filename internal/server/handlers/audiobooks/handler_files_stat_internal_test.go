// file: internal/server/handlers/audiobooks/handler_files_stat_internal_test.go
// version: 1.1.0
// guid: ab314bb6-1fc3-4f07-bb17-222427e6d5f5
// last-edited: 2026-10-06

package audiobookshandler

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestStatFilePaths_RealDisk proves file_exists comes from the disk, not the
// stored flag: a present file is true, an absent one false, an empty path
// unknown.
func TestStatFilePaths_RealDisk(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.m4b")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := statFilePaths(context.Background(), []string{present, filepath.Join(dir, "gone.m4b"), ""})
	if got[0].Exists == nil || !*got[0].Exists {
		t.Fatalf("present file: want exists=true, got %+v", got[0])
	}
	if got[1].Exists == nil || *got[1].Exists {
		t.Fatalf("absent file: want exists=false, got %+v", got[1])
	}
	if got[2].Exists != nil || got[2].CheckError == "" {
		t.Fatalf("empty path: want unknown with a reason, got %+v", got[2])
	}
}

// TestStatFilePaths_NonNotExistErrorIsUnknown: a permission/I/O error must not
// be reported as "missing" — it says nothing about presence.
func TestStatFilePaths_NonNotExistErrorIsUnknown(t *testing.T) {
	stat := func(string) (os.FileInfo, error) { return nil, fs.ErrPermission }
	got := newStatChecker(fileStatMaxInFlight, time.Minute).statPaths(context.Background(), []string{"/a"}, stat)
	if got[0].Exists != nil {
		t.Fatalf("want unknown, got exists=%v", *got[0].Exists)
	}
	if got[0].CheckError == "" {
		t.Fatal("want a check error naming the cause")
	}
}

// TestStatFilePaths_HungStatTimesOut: a stat that never returns must not stall
// the response; the deadline reports the stuck paths as unknown.
func TestStatFilePaths_HungStatTimesOut(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stat := func(p string) (os.FileInfo, error) {
		if p == "/hung" {
			<-release
		}
		return nil, fs.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := newStatChecker(fileStatMaxInFlight, time.Minute).statPaths(ctx, []string{"/fast", "/hung"}, stat)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stat did not honour the deadline (took %v)", time.Since(start))
	}
	if got[0].Exists == nil || *got[0].Exists {
		t.Fatalf("fast path: want exists=false, got %+v", got[0])
	}
	if got[1].Exists != nil || got[1].CheckError != "disk check timed out" {
		t.Fatalf("hung path: want timed-out unknown, got %+v", got[1])
	}
}

// TestStatFilePaths_BoundedPool: concurrent stats never exceed fileStatWorkers.
func TestStatFilePaths_BoundedPool(t *testing.T) {
	var inFlight, peak atomic.Int32
	stat := func(string) (os.FileInfo, error) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inFlight.Add(-1)
		return nil, errors.New("boom")
	}
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = "/p"
	}
	got := newStatChecker(fileStatMaxInFlight, time.Minute).statPaths(context.Background(), paths, stat)
	if p := peak.Load(); p > fileStatWorkers {
		t.Fatalf("peak concurrency %d exceeds pool size %d", p, fileStatWorkers)
	}
	for i, g := range got {
		if g.Exists != nil || g.CheckError != "boom" {
			t.Fatalf("path %d: want unknown/boom, got %+v", i, g)
		}
	}
}

// TestStatChecker_GlobalCapReturnsUnknownWhenFull: with every process-wide
// slot held by a hung stat, a new request does not stat at all — it reads
// "busy" immediately — so stuck threads can never exceed the cap.
func TestStatChecker_GlobalCapReturnsUnknownWhenFull(t *testing.T) {
	sc := newStatChecker(2, time.Minute)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	hung := func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-release
		return nil, fs.ErrNotExist
	}
	// Two hung stats on different roots, each holding one slot past its deadline.
	for _, p := range []string{"/r1/a/x", "/r2/b/y"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		sc.statPaths(ctx, []string{p}, hung)
		cancel()
	}
	if n := len(sc.slots); n != 2 {
		t.Fatalf("want both slots held by hung stats, got %d", n)
	}
	before := calls.Load()
	start := time.Now()
	got := sc.statPaths(context.Background(), []string{"/r3/c/z"}, hung)
	if time.Since(start) > time.Second {
		t.Fatalf("a full cap must answer immediately, took %v", time.Since(start))
	}
	if calls.Load() != before {
		t.Fatal("stat was attempted although every slot was taken")
	}
	if got[0].Exists != nil || !strings.Contains(got[0].CheckError, "busy") {
		t.Fatalf("want unknown/busy, got %+v", got[0])
	}
}

// TestStatChecker_BreakerSkipsTrippedRootThenRecovers: a stat that hangs past
// the deadline trips its root; later paths under that root are not stat'ed
// until the cooldown passes, and other roots are unaffected.
func TestStatChecker_BreakerSkipsTrippedRootThenRecovers(t *testing.T) {
	sc := newStatChecker(8, time.Minute)
	now := time.Now()
	sc.now = func() time.Time { return now }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var nasCalls atomic.Int32
	stat := func(p string) (os.FileInfo, error) {
		if strings.HasPrefix(p, "/mnt/nas/") {
			if nasCalls.Add(1) == 1 {
				<-release // first stat under the NAS hangs
			}
		}
		return nil, fs.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	got := sc.statPaths(ctx, []string{"/mnt/nas/a.m4b"}, stat)
	cancel()
	if got[0].CheckError != "disk check timed out" {
		t.Fatalf("want timed out, got %+v", got[0])
	}

	got = sc.statPaths(context.Background(), []string{"/mnt/nas/b.m4b", "/mnt/local/c.m4b"}, stat)
	if got[0].Exists != nil || !strings.Contains(got[0].CheckError, "suspended") {
		t.Fatalf("tripped root: want unknown/suspended, got %+v", got[0])
	}
	if nasCalls.Load() != 1 {
		t.Fatalf("tripped root was stat'ed again (%d calls)", nasCalls.Load())
	}
	if got[1].Exists == nil || *got[1].Exists {
		t.Fatalf("other root: want exists=false, got %+v", got[1])
	}

	now = now.Add(61 * time.Second)
	got = sc.statPaths(context.Background(), []string{"/mnt/nas/b.m4b"}, stat)
	if got[0].Exists == nil || *got[0].Exists {
		t.Fatalf("after cooldown: want a real stat (exists=false), got %+v", got[0])
	}
}

// TestStatChecker_QueuedPathsDoNotTripRoot: paths that never started before
// the deadline (queued behind slow stats) must not suspend their root.
func TestStatChecker_QueuedPathsDoNotTripRoot(t *testing.T) {
	sc := newStatChecker(fileStatMaxInFlight, time.Minute)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stat := func(p string) (os.FileInfo, error) {
		if strings.HasPrefix(p, "/slow/") {
			<-release
		}
		return nil, fs.ErrNotExist
	}
	// fileStatWorkers slow paths occupy every worker; the /other path queues.
	paths := make([]string, 0, fileStatWorkers+1)
	for range fileStatWorkers {
		paths = append(paths, "/slow/x/f")
	}
	paths = append(paths, "/other/y/f")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	sc.statPaths(ctx, paths, stat)
	cancel()
	if sc.isTripped("/other/y") {
		t.Fatal("a queued, never-started path tripped its root")
	}
	if !sc.isTripped("/slow/x") {
		t.Fatal("the hung root should be tripped")
	}
}

func TestStatRoot(t *testing.T) {
	for in, want := range map[string]string{
		"/mnt/nas/books/a.m4b": "/mnt/nas",
		"/Volumes/Media/x":     "/Volumes/Media",
		"/a":                   "/a",
		"/mnt/nas/../other/f":  "/mnt/other",
	} {
		if got := statRoot(in); got != want {
			t.Errorf("statRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
