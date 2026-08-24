// file: internal/scanner/process_file_timeout_test.go
// version: 1.1.0
// guid: 60f4711a-799e-4583-8b12-21b77ce2bc3d
// last-edited: 2026-08-23

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Prod's library.scan stalled at the SAME item across 9 runs over 3 days: the
// numerator stuck at 14912 while the denominator drifted 40109 -> 40089, each run
// ending "abandoned: op goroutine did not exit within grace after context
// cancellation". ProcessFile's chain (os.Open -> tag.ReadFrom -> SHA-256) is
// blocking syscalls and third-party parsing that ignore ctx, so one malformed
// container stops the whole scan forever.
//
// These tests exercise the arms that matter. The happy path is the easy one and
// proves the least: a wrapper that never returns early would pass it.

// callBounded runs processFileBounded on its own goroutine and fails the test if
// it does not return within hardCap.
//
// Every test below feeds processFileBounded work that never returns on its own;
// the call escapes ONLY because one of the select arms fires. Calling it
// synchronously therefore makes the test's own liveness depend on the thing
// under test. A mutation that deletes an arm does not fail those assertions --
// it never reaches them, and the test binary hangs until Go's -timeout panics.
//
// That distinction matters more than it looks. A hang reads as "still running"
// in CI, not as a red test, so a regression in the timeout guard would present
// as a stuck job -- the exact symptom the guard exists to prevent. It also
// quietly weakens mutation testing: #2830's matrix scored 5/5, but only because
// the mutations picked happened to return the WRONG value rather than not
// return at all. Bounding the call here is what makes a future 5/5 mean
// something.
//
// Credit: the hang-not-fail failure mode was reported by a parallel session that
// hit it in internal/operations/registry on 2026-08-23.
func callBounded(t *testing.T, hardCap time.Duration, ctx context.Context, path string,
	timeout time.Duration, work func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error),
) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
	t.Helper()

	type outcome struct {
		meta *metadata.Metadata
		mi   *mediainfo.MediaInfo
		hash string
		err  error
	}
	// Buffered: if the hard cap fires first, the late send must not block a
	// goroutine forever -- the same reasoning as the channel inside
	// processFileBounded itself.
	done := make(chan outcome, 1)
	go func() {
		meta, mi, hash, err := processFileBounded(ctx, path, timeout, work)
		done <- outcome{meta, mi, hash, err}
	}()

	select {
	case o := <-done:
		return o.meta, o.mi, o.hash, o.err
	case <-time.After(hardCap):
		t.Fatalf("processFileBounded(%q, timeout=%v) did not return within %v: "+
			"the call is not bounded at all, so no assertion below this line can run",
			path, timeout, hardCap)
		return nil, nil, "", nil
	}
}

// The load-bearing case: work that never returns must not block the caller.
func TestProcessFileBounded_TimesOutOnWorkThatNeverReturns(t *testing.T) {
	never := make(chan struct{})
	defer close(never) // let the leaked goroutine exit when the test ends

	work := func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
		<-never // exactly what tag.ReadFrom does on a malformed atom tree
		return nil, nil, "", nil
	}

	start := time.Now()
	_, _, _, err := callBounded(t, 2*time.Second, context.Background(), "/stuck.m4b", 50*time.Millisecond, work)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("work that never returns produced no error: the scan would hang forever, " +
			"which is the prod symptom this guards")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should say it timed out, got %v", err)
	}
	if !strings.Contains(err.Error(), "/stuck.m4b") {
		t.Errorf("error must name the file so the stuck item is identifiable, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("returned after %v: the timeout did not bound the call", elapsed)
	}
}

// A leaked goroutine must still be able to SEND, or every timed-out file leaks a
// goroutine permanently. This is why the result channel is buffered.
//
// An earlier version of this test watched the WORK function instead, and it was
// useless: work() returns before processFileBounded's goroutine performs the
// send, so its deferred close fired whether or not the send then blocked. It
// passed against an unbuffered channel -- verified by mutation. The only way to
// see this defect is to watch the sending goroutine itself, which is not
// reachable directly, so count goroutines.
func TestProcessFileBounded_LateSendDoesNotLeakAGoroutine(t *testing.T) {
	release := make(chan struct{})

	work := func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
		<-release
		return &metadata.Metadata{Title: "late"}, nil, "h", nil
	}

	settle := func() int {
		// Goroutine counts are noisy; take the minimum over a few reads so an
		// unrelated transient does not decide the result.
		best := runtime.NumGoroutine()
		for i := 0; i < 20; i++ {
			time.Sleep(10 * time.Millisecond)
			if n := runtime.NumGoroutine(); n < best {
				best = n
			}
		}
		return best
	}

	before := settle()

	_, _, _, err := callBounded(t, 2*time.Second, context.Background(), "/slow.m4b", 20*time.Millisecond, work)
	if err == nil {
		t.Fatal("expected a timeout")
	}

	close(release) // the abandoned goroutine now finishes work() and sends

	// With a buffered channel the send succeeds and the goroutine exits, so the
	// count returns to baseline. With an unbuffered channel it blocks on the send
	// forever and the count stays elevated.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutine count did not return to baseline (before=%d, now=%d): the "+
		"abandoned goroutine is stuck on its send, so every timed-out file leaks "+
		"one permanently. The result channel must be buffered.",
		before, runtime.NumGoroutine())
}

// Cancellation must be observed promptly even while the work is still running,
// so a cancelled scan stops between files instead of waiting out the timeout.
func TestProcessFileBounded_HonoursContextCancellation(t *testing.T) {
	never := make(chan struct{})
	defer close(never)
	work := func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
		<-never
		return nil, nil, "", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, _, _, err := callBounded(t, 2*time.Second, ctx, "/x.m4b", time.Hour, work)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled context produced no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("context.Canceled must survive wrapping so callers can distinguish "+
			"a cancel from a stuck file, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("waited %v after cancellation: the ctx arm is not being selected", elapsed)
	}
}

// The converse, so the guard cannot pass by failing everything.
func TestProcessFileBounded_PassesThroughRealResultsAndErrors(t *testing.T) {
	want := &metadata.Metadata{Title: "Real Book"}
	got, _, hash, err := processFileBounded(context.Background(), "/ok.m4b", time.Minute,
		func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
			return want, nil, "abc123", nil
		})
	if err != nil {
		t.Fatalf("a fast success became an error: %v", err)
	}
	if got != want || hash != "abc123" {
		t.Errorf("result not passed through: meta=%v hash=%q", got, hash)
	}

	sentinel := fmt.Errorf("corrupt header")
	_, _, _, err = processFileBounded(context.Background(), "/bad.m4b", time.Minute,
		func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
			return nil, nil, "", sentinel
		})
	if !errors.Is(err, sentinel) {
		t.Errorf("a real ProcessFile error must reach the caller unchanged, got %v", err)
	}
}

// End to end through the exported wrapper on a genuine file, so the plumbing
// between ProcessFileWithTimeout and ProcessFile is covered too.
func TestProcessFileWithTimeout_RealFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.m4b")
	if err := os.WriteFile(path, []byte("not really an audio file"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// The tag read fails on this content and ProcessFile falls back, but it must
	// RETURN -- promptly, with a hash of the bytes it did read.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, hash, _ := ProcessFileWithTimeout(context.Background(), path)
		if hash == "" {
			t.Error("expected a content hash even when the tag read fails")
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ProcessFileWithTimeout did not return on an ordinary small file")
	}
}
