// file: core/health/diagnostics/paths_scaling_test.go
// version: 1.1.0
// guid: 3e8a5d71-c204-4b96-8f1d-6a0b9e2c4d58
// last-edited: 2026-11-14

package diagnostics

import (
	"strings"
	"testing"
	"time"
)

// scalingBound is how long one redaction of a line of up to 400k bytes may take.
// The rule is linear, so the real cost is milliseconds even under -race; a
// quadratic scan over these sizes takes 6 s (200k) to 26 s (400k) unloaded, and
// the bound sits far from both. It is an absolute bound on purpose: a ratio of
// two wall-clock timings flakes on a loaded runner, and this does not, since
// only a quadratic cost can come near it.
const scalingBound = 3 * time.Second

func scalingRoots() pathRoots {
	return newPathRoots([]PathRoot{{Name: "lib", Path: "/srv/lib"}, {Name: "vault", Path: "/srv/vault"}})
}

// timeRedact runs redactPaths on line and fails if it takes longer than
// scalingBound. The lines stay under maxLineBytes, so they are what a log line
// can really be.
func timeRedact(t *testing.T, roots pathRoots, line string) string {
	t.Helper()
	if len(line) >= maxLineBytes {
		t.Fatalf("test line of %d bytes would not reach the redactor", len(line))
	}
	start := time.Now()
	out := roots.redactPaths(line)
	if d := time.Since(start); d > scalingBound {
		t.Fatalf("redactPaths of %d bytes took %s, want under %s: the scan is not linear", len(line), d, scalingBound)
	}
	return out
}

// TestRedactPaths_LineOfAngleBracketsIsLinear: every ">" asks whether it ends a
// root placeholder, and that question used to search back through the whole
// prefix, which is quadratic on a long line of them.
func TestRedactPaths_LineOfAngleBracketsIsLinear(t *testing.T) {
	roots := scalingRoots()
	// A slash anywhere keeps redactPaths from returning early on a line that
	// cannot hold a path, so every ">" is really judged.
	large := strings.Repeat(">", 400_000) + " /x"
	if got := timeRedact(t, roots, large); got != large[:len(large)-2]+"<path>" {
		t.Fatal("the line of '>' was not rewritten as expected")
	}
	// And the placeholder rule itself is still right at scale.
	if got := roots.redactPaths(strings.Repeat(">", 1000) + "<root:lib>/x <root:lib>/home/bruce/y"); !strings.HasSuffix(got, "<root:lib>/x <root:lib>/home/bruce/y") {
		t.Errorf("a configured root's placeholder was not skipped: %q", got[len(got)-40:])
	}
}

// TestRedactPaths_TrailingDotRunIsLinear: sentence punctuation after a path used
// to be stripped one character at a time, each step searching back for the last
// separator, so a long run of dots (or of dots and colons) was quadratic: 25k
// dots took 110 ms, 100k 1.6 s, 400k 26 s.
func TestRedactPaths_TrailingDotRunIsLinear(t *testing.T) {
	roots := scalingRoots()
	for name, tc := range map[string]struct{ line, want string }{
		"dots after a name":      {"/x/a" + strings.Repeat(".", 200_000), "<path>" + strings.Repeat(".", 200_000)},
		"dots and colons":        {"/x/a" + strings.Repeat(".:", 100_000), "<path>" + strings.Repeat(".:", 100_000)},
		"dots under a root":      {"/srv/lib/a" + strings.Repeat(".", 200_000), "<root:lib>/a" + strings.Repeat(".", 200_000)},
		"a name made of dots":    {"/x/" + strings.Repeat(".", 200_000), "<path>"},
		"long segment then dots": {"/x/" + strings.Repeat("ab", 100_000) + strings.Repeat(".", 100_000), "<path>" + strings.Repeat(".", 100_000)},
	} {
		if got := timeRedact(t, roots, tc.line); got != tc.want {
			t.Errorf("%s: got %d bytes starting %q, want %d bytes starting %q", name, len(got), head(got), len(tc.want), head(tc.want))
		}
	}
}

// TestRedactPaths_NestedTokensAreLinear: a token that holds a path (the tail of
// a path under a root, the query of a route) is read again from inside, and a
// line built of such tokens nested in each other must not make that quadratic.
func TestRedactPaths_NestedTokensAreLinear(t *testing.T) {
	roots := scalingRoots()
	for name, line := range map[string]string{
		"roots in tails":   strings.Repeat("/srv/lib/a@", 30_000),
		"routes in query":  strings.Repeat("/api/x?a=", 40_000),
		"routes in a path": strings.Repeat("/api/x@", 50_000),
		"escaped queries":  strings.Repeat("/api/x?a=%2Fapi%2Fx%3Fa%3D", 15_000),
	} {
		out := timeRedact(t, roots, line)
		if strings.Contains(out, "/home") {
			t.Errorf("%s: %q", name, head(out))
		}
	}
}

func head(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
