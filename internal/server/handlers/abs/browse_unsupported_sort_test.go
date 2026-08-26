// file: internal/server/handlers/abs/browse_unsupported_sort_test.go
// version: 2.2.0
// guid: 2a9f4d13-8b07-4e56-91c2-5d3e08a7f6b4
// last-edited: 2026-08-25

package abs

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// A sort this server cannot perform must not be answered in silence.
//
// absSortField returns "" for anything absSortFields does not know, and ""
// means "no ordering requested" everywhere downstream — so the client gets a
// 200 and the store's default order. warnUnindexedSort cannot report it: its
// first line returns early on field == "", so the one case that most needed
// reporting was the one case it skipped.
//
// bus* names are task-unique per repo convention for package-shared helpers.

// busWriter serialises writes so the concurrency test below can share one
// buffer. slog's own handlers lock around each write, but the buffer is read
// from the test goroutine while workers are still finishing, so it needs its
// own mutex regardless.
type busWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *busWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *busWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func busCapture(t *testing.T) (*busWriter, func()) {
	t.Helper()
	w := &busWriter{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return w, func() { slog.SetDefault(prev) }
}

func busCtx(q string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+q, nil)
	return c
}

// busReset reopens both rate limiters so each subtest starts from a state where
// a warning is still possible. Without it the first subtest to run consumes the
// only warning and the rest pass for the wrong reason.
//
// Storing 0 is what "never warned" means: now-0 is ~1.7e9 seconds, far past
// the window, so the next call always fires.
func busReset() {
	absUnsupportedSortLastWarn.Store(0)
	absUnindexedSortWarned = sync.Map{}
}

func TestUnsupportedSortIsReported(t *testing.T) {
	t.Run("unsupported sort warns and names the alternatives", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		f := absItemFilter(busCtx("sort=media.metadata.fileModified"))
		if f.SortBy != "" {
			t.Fatalf("fixture assumes this sort is unmapped; absSortField returned %q — "+
				"if it was just mapped, move it to the supported case below", f.SortBy)
		}
		got := buf.String()
		if !strings.Contains(got, "no field for") {
			t.Errorf("an unsupported sort was answered in silence; log was:\n%s", got)
		}
		// The warning must name what IS supported, or it tells the reader only
		// that something failed.
		if !strings.Contains(got, "publishedyear") {
			t.Errorf("warning does not list the supported sorts; log was:\n%s", got)
		}
	})

	t.Run("supported sort does not warn", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		f := absItemFilter(busCtx("sort=media.metadata.publishedYear"))
		if f.SortBy != "year" {
			t.Fatalf("absSortField = %q, want year", f.SortBy)
		}
		if strings.Contains(buf.String(), "no field for") {
			t.Errorf("a supported sort produced the unsupported-sort warning:\n%s", buf.String())
		}
	})

	t.Run("no sort at all does not warn", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		absItemFilter(busCtx(""))
		if strings.Contains(buf.String(), "no field for") {
			t.Errorf("absent sort must be silent, it is not an error:\n%s", buf.String())
		}
	})

	t.Run("at most one warning per window, even for distinct values", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		for range 5 {
			absItemFilter(busCtx("sort=media.metadata.fileBirthtime"))
		}
		// A DIFFERENT unsupported sort inside the same window is suppressed
		// too. This is a deliberate trade and the reason it is acceptable is
		// that it is TEMPORARY: the previous design deduplicated per distinct
		// value, which is better diagnostics right up until 64 junk values
		// exhausted the cap and silenced every future gap permanently.
		absItemFilter(busCtx("sort=progress"))

		if n := strings.Count(buf.String(), "no field for"); n != 1 {
			t.Errorf("rate limit failed: %d warnings inside one window, want exactly 1", n)
		}
	})

	t.Run("the next window reopens", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		absItemFilter(busCtx("sort=media.metadata.fileBirthtime"))
		// Age the limiter past the window rather than sleeping a minute.
		absUnsupportedSortLastWarn.Store(absUnsupportedSortLastWarn.Load() - absUnsupportedSortWarnEvery - 1)
		absItemFilter(busCtx("sort=media.metadata.fileBirthtime"))

		if n := strings.Count(buf.String(), "no field for"); n != 2 {
			t.Errorf("got %d warnings, want 2 — the limiter did not reopen, which is "+
				"the permanent-silence failure this design exists to prevent", n)
		}
	})
}

// TestUnsupportedSortLimiterIsConcurrencySafe is the security regression.
//
// The first version of this limiter keyed a sync.Map on the RAW query
// parameter and capped the distinct-key count at 64. That cap bounded key
// COUNT, not bytes: the key was untruncated and MaxHeaderBytes is 1 MB, so 64
// keys could retain ~64 MB. It was also check-then-act — Load, LoadOrStore,
// Add — so a concurrent burst blew past the cap; 5,000 callers measured 961
// keys held.
//
// Both defects are gone by construction: the limiter is a single atomic.Int64
// holding a timestamp and retains nothing derived from client input. This test
// pins the property that made the old design unsafe — that concurrent callers
// could each pass the gate.
//
// NOTE: -race cannot catch the old bug. Load/LoadOrStore/Add on sync.Map and
// atomic.Int64 are race-detector-clean while overshooting 15×; it was a
// check-then-act LOGIC race, not a data race. Only counting the output finds it.
//
// Mutation results, measured, including the one that survives:
//
//   - restore the #2913 keyed-map-plus-cap design → KILLED, 3/3 runs. This is
//     the regression that matters and the reason this test exists.
//   - delete the window gate → KILLED.
//   - delete the CompareAndSwap, leaving a bare Store → **SURVIVES**.
//
// The survivor is honest and is not worth chasing. With a single shared
// timestamp the overshoot window is a few nanoseconds wide, so a bare Store is
// *almost* always correct and no scheduler this test can command will reliably
// prove otherwise. The CAS is kept because it makes "one winner per window" a
// guarantee rather than a probability — correctness by construction, not by
// test. Do not delete it on the grounds that nothing fails: that is exactly
// what the measurement above says, and it is not evidence the CAS is unneeded.
//
// The old design overshot 15× not through a narrow race but structurally:
// every caller had a UNIQUE key, so LoadOrStore never reported "seen" and the
// lagging counter was the only gate.
func TestUnsupportedSortLimiterIsConcurrencySafe(t *testing.T) {
	busReset()
	buf, restore := busCapture(t)
	defer restore()

	const workers = 500

	// Build every context BEFORE the barrier. Constructing a gin context is
	// slower than the limiter itself, so doing it after the release lets the
	// first caller finish the whole gate before the rest arrive — the test
	// then passes even with the CAS deleted, which is exactly the false
	// confidence this test exists to avoid.
	ctxs := make([]*gin.Context, workers)
	for i := range ctxs {
		ctxs[i] = busCtx(fmt.Sprintf("sort=media.metadata.junk%d", i))
	}

	var ready, wg sync.WaitGroup
	ready.Add(workers)
	wg.Add(workers)
	start := make(chan struct{})
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			ready.Done()
			<-start // every goroutine is parked here before any of them runs
			absItemFilter(ctxs[i])
		}(i)
	}
	ready.Wait()
	close(start)
	wg.Wait()

	// Exactly one, not "at most a few": the CAS admits a single winner per
	// window. Any number above 1 means callers are passing the gate and then
	// all logging, which is precisely the old overshoot.
	if n := strings.Count(buf.String(), "no field for"); n != 1 {
		t.Errorf("%d warnings from %d concurrent distinct sorts, want exactly 1 — "+
			"concurrent callers are passing the rate-limit gate together", n, workers)
	}
}

// TestUnsupportedSortLogValueIsTruncated keeps a client from bloating the log
// line it triggers: the raw value is echoed back into it.
func TestUnsupportedSortLogValueIsTruncated(t *testing.T) {
	busReset()
	buf, restore := busCapture(t)
	defer restore()

	long := strings.Repeat("z", absSortRawLogMax*4)
	absItemFilter(busCtx("sort=" + long))

	got := buf.String()
	if !strings.Contains(got, "truncated") {
		t.Errorf("an oversized sort_param reached the log untruncated:\n%.200s", got)
	}
	if strings.Contains(got, long) {
		t.Error("the full client-supplied string was logged verbatim")
	}
}

// TestUnindexedSortLogValueIsTruncated covers the SIBLING warning.
//
// warnUnindexedSort is rate-limited per mapped field, so the NUMBER of lines it
// can emit is bounded by absSortFields. That bounded the count and hid the
// hazard: each of those lines echoed the client's raw parameter back
// untruncated, and a sort parameter can be a megabyte. The original fix
// hardened warnUnsupportedSort and left its twin exposed to the defect it had
// just named.
func TestUnindexedSortLogValueIsTruncated(t *testing.T) {
	busReset()
	buf, restore := busCapture(t)
	defer restore()

	// absSortField takes the LAST dotted segment, so a long prefix maps to a
	// real field while leaving raw enormous.
	long := strings.Repeat("z", absSortRawLogMax*4)
	f := absItemFilter(busCtx("sort=" + long + ".duration"))
	if f.SortBy != "duration" {
		t.Fatalf("fixture needs a MAPPED field to reach warnUnindexedSort; got SortBy=%q", f.SortBy)
	}

	got := buf.String()
	if !strings.Contains(got, "no memdb index") {
		t.Fatalf("fixture did not reach warnUnindexedSort — if duration became an "+
			"enabled index, pick another unindexed field; log was:\n%.300s", got)
	}
	if strings.Contains(got, long) {
		t.Error("warnUnindexedSort logged the full client-supplied string verbatim")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("an oversized sort_param reached the log untruncated:\n%.300s", got)
	}
}

// TestAbsTruncateForLogKeepsValidUTF8 pins the rune boundary.
//
// Cutting at a byte offset lands inside a multi-byte rune roughly two times in
// three for 3-byte characters. The result is invalid UTF-8 in the log stream,
// and a slog JSON handler rewrites it to U+FFFD silently — so the corruption
// never surfaces as an error, it just quietly mangles the value a reader is
// trying to diagnose from.
func TestAbsTruncateForLogKeepsValidUTF8(t *testing.T) {
	// Sweep every offset so the test does not depend on one lucky alignment.
	for _, r := range []string{"日", "é", "😀"} {
		s := strings.Repeat(r, 64)
		for max := 1; max <= 96; max++ {
			got := absTruncateForLog(s, max)
			if !utf8.ValidString(got) {
				t.Fatalf("absTruncateForLog(%q…, %d) produced invalid UTF-8: %q", r, max, got)
			}
		}
	}

	// Short input is returned untouched, and must not panic on the empty string.
	for _, s := range []string{"", "abc"} {
		if got := absTruncateForLog(s, absSortRawLogMax); got != s {
			t.Errorf("absTruncateForLog(%q) = %q, want it unchanged", s, got)
		}
	}
}
