// file: internal/server/handlers/audiobooks/handler_files_stat_internal_test.go
// version: 1.0.0
// guid: ab314bb6-1fc3-4f07-bb17-222427e6d5f5
// last-edited: 2026-10-05

package audiobookshandler

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	got := statFilePathsWith(context.Background(), []string{"/a"}, stat)
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
	got := statFilePathsWith(ctx, []string{"/fast", "/hung"}, stat)
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
	got := statFilePathsWith(context.Background(), paths, stat)
	if p := peak.Load(); p > fileStatWorkers {
		t.Fatalf("peak concurrency %d exceeds pool size %d", p, fileStatWorkers)
	}
	for i, g := range got {
		if g.Exists != nil || g.CheckError != "boom" {
			t.Fatalf("path %d: want unknown/boom, got %+v", i, g)
		}
	}
}
