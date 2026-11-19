// file: core/updater/download_bounds_test.go
// version: 1.0.0
// guid: 3c8e5a17-9d24-4b60-a1f7-6e0d2b94c385
// last-edited: 2026-11-18

//go:build unix

package updater

// The time bounds on a download (F8-02 review m1): a body that stalls is cut
// off after the idle timeout, one that keeps arriving is not, and the whole
// transfer has an overall limit. They hold for Install, Apply and
// FetchRelease alike because all three download through one function.

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A source that never sends a first byte is cut off by the same timeout.
func TestDownload_SilentSourceIsCutOff(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	src.hang[src.rels["v0.224.0"].Assets[0].URL] = true
	r := newRig(t, "v0.223.0", src)
	r.u.idleTimeout = 100 * time.Millisecond

	dest := filepath.Join(t.TempDir(), "fetched")
	err := r.u.FetchRelease(t.Context(), "v0.224.0", dest)
	if !errors.Is(err, ErrDownloadFailed) || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("a silent fetch = %v, want ErrDownloadFailed naming the idle timeout", err)
	}
	assertAbsent(t, dest)
	assertAbsent(t, dest+".part")
}

func TestDownload_StalledBodyIsCutOffAndLeavesNothing(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	src.stall[src.rels["v0.224.0"].Assets[0].URL] = true
	r := newRig(t, "v0.223.0", src)
	r.u.idleTimeout = 100 * time.Millisecond

	dest := filepath.Join(t.TempDir(), "fetched")
	start := time.Now()
	err := r.u.FetchRelease(t.Context(), "v0.224.0", dest)
	if !errors.Is(err, ErrDownloadFailed) || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("a stalled fetch = %v, want ErrDownloadFailed naming the idle timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the stalled fetch took %v, it should stop at the idle timeout", d)
	}
	assertAbsent(t, dest)
	assertAbsent(t, dest+".part")
	r.assertUntouched()

	// The legacy Install entry (the gin apply) is bounded the same way and
	// removes <exe>.new.
	if err := r.u.Install(t.Context(), "v0.224.0"); !errors.Is(err, ErrDownloadFailed) {
		t.Errorf("a stalled Install = %v, want ErrDownloadFailed", err)
	}
	assertAbsent(t, r.exe+".new")
	r.assertUntouched()
}

// trickleSource serves the fake source's bodies a few bytes at a time.
type trickleSource struct {
	tagSource
	per   int
	delay time.Duration
}

func (t trickleSource) Open(ctx context.Context, a Asset) (io.ReadCloser, int64, error) {
	rc, size, err := t.tagSource.Open(ctx, a)
	if err != nil {
		return nil, size, err
	}
	return &trickle{ctx: ctx, rc: rc, per: t.per, delay: t.delay}, size, nil
}

type trickle struct {
	ctx   context.Context
	rc    io.ReadCloser
	per   int
	delay time.Duration
}

func (t *trickle) Read(p []byte) (int, error) {
	select {
	case <-time.After(t.delay):
	case <-t.ctx.Done():
		return 0, context.Cause(t.ctx) // as net/http's body does
	}
	if len(p) > t.per {
		p = p[:t.per]
	}
	return t.rc.Read(p)
}
func (t *trickle) Close() error { return t.rc.Close() }

func TestDownload_ProgressingBodyIsOnlyBoundedByTheOverallLimit(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	// About 12 reads, 40 ms apart: nearly half a second in all, never idle for
	// anything like the 250 ms idle timeout.
	slow := trickleSource{tagSource: tagSource{src}, per: 16, delay: 40 * time.Millisecond}

	r := newRig(t, "v0.223.0", src, withSource(slow))
	r.u.idleTimeout = 250 * time.Millisecond
	dest := filepath.Join(t.TempDir(), "fetched")
	if err := r.u.FetchRelease(t.Context(), "v0.224.0", dest); err != nil {
		t.Fatalf("a slow body that kept arriving was cut off: %v", err)
	}
	if got := readFile(t, dest); got != string(fakeBinary(testApp, "v0.224.0")) {
		t.Errorf("fetched body = %q", got)
	}

	// The same body under an overall limit shorter than the transfer.
	r2 := newRig(t, "v0.223.0", src, withSource(slow))
	r2.u.idleTimeout = time.Minute
	r2.u.maxDuration = 100 * time.Millisecond
	dest2 := filepath.Join(t.TempDir(), "fetched")
	err := r2.u.FetchRelease(t.Context(), "v0.224.0", dest2)
	if !errors.Is(err, ErrDownloadFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a transfer past the overall limit = %v, want ErrDownloadFailed wrapping the deadline", err)
	}
	assertAbsent(t, dest2)
	assertAbsent(t, dest2+".part")
}
