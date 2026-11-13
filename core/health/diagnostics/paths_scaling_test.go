// file: core/health/diagnostics/paths_scaling_test.go
// version: 1.0.0
// guid: 3e8a5d71-c204-4b96-8f1d-6a0b9e2c4d58
// last-edited: 2026-11-13

package diagnostics

import (
	"strings"
	"testing"
	"time"
)

// bestOf runs f three times and returns the fastest, so one descheduled run
// cannot decide a scaling ratio.
func bestOf(f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for range 3 {
		start := time.Now()
		f()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// TestRedactPaths_LineOfAngleBracketsIsLinear: every ">" asks whether it ends a
// root placeholder, and that question used to search back through the whole
// prefix, which is quadratic on a long line of them. Quadrupling the line must
// cost about four times as much (linear), never sixteen (quadratic): the ratio
// has to stay under 8. The lines stay under maxLineBytes, so they are what a
// log line can really be.
func TestRedactPaths_LineOfAngleBracketsIsLinear(t *testing.T) {
	roots := newPathRoots([]PathRoot{{Name: "lib", Path: "/srv/lib"}, {Name: "vault", Path: "/srv/vault"}})
	// A slash anywhere keeps redactPaths from returning early on a line that
	// cannot hold a path, so every ">" is really judged.
	small, large := strings.Repeat(">", 100_000)+" /x", strings.Repeat(">", 400_000)+" /x"
	if len(large) >= maxLineBytes {
		t.Fatalf("test line of %d bytes would not reach the redactor", len(large))
	}
	var sink string
	ts := bestOf(func() { sink = roots.redactPaths(small) })
	tl := bestOf(func() { sink = roots.redactPaths(large) })
	if sink != large[:len(large)-2]+"<path>" {
		t.Fatal("the line of '>' was not rewritten as expected")
	}
	if ts <= 0 {
		ts = time.Nanosecond
	}
	if ratio := float64(tl) / float64(ts); ratio >= 8 {
		t.Errorf("4x the line took %.1fx as long (%s -> %s), want under 8x: the scan is not linear", ratio, ts, tl)
	}
	// And the placeholder rule itself is still right at scale.
	if got := roots.redactPaths(strings.Repeat(">", 1000) + "<root:lib>/x <root:lib>/home/bruce/y"); !strings.HasSuffix(got, "<root:lib>/x <root:lib>/home/bruce/y") {
		t.Errorf("a configured root's placeholder was not skipped: %q", got[len(got)-40:])
	}
}
