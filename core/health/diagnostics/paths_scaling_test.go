// file: core/health/diagnostics/paths_scaling_test.go
// version: 1.4.0
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

// TestRedactPaths_RouteQueryShapesAreLinear (round 5): the route scanner takes a
// whole token to whitespace and then reads its path part and query a second
// time, decoding percent escapes up to maxDecode rounds. Each shape below makes
// a naive version re-scan its input from every delimiter, every "&", every "%"
// or every failed chunk, and so is quadratic; none may be, and none may leak.
func TestRedactPaths_RouteQueryShapesAreLinear(t *testing.T) {
	roots := scalingRoots()
	for name, line := range map[string]string{
		"nested escapes":        "/api/x?a=" + strings.Repeat("%25", 130_000) + "2F",
		"many query parts":      "/api/x?" + strings.Repeat("a=1&", 90_000) + "p=%2Fhome%2Fbruce",
		"commas":                strings.Repeat("/api/x,", 50_000) + "/home/bruce",
		"question marks":        strings.Repeat("/api/x?", 50_000) + "/home/bruce",
		"trailing punctuation":  "/api/x" + strings.Repeat(".'", 100_000) + " /home/bruce",
		"failed chunks":         strings.Repeat("/api/%zz,", 40_000) + "/home/bruce",
		"slash runs":            "/api/x" + strings.Repeat("/", 200_000) + "home/bruce",
		"decoded slash runs":    "/api/x" + strings.Repeat("%2F", 100_000) + "home%2Fbruce",
		"bare percents":         "/api/x?a=" + strings.Repeat("%", 200_000) + "&p=%2Fhome%2Fbruce",
		"fragments":             "/api/x" + strings.Repeat("#a", 100_000) + "#%2Fhome%2Fbruce",
		"whitespace separated":  strings.Repeat("/api/x?a=1 ", 30_000) + "/home/bruce",
		"alternating encodings": strings.Repeat("/api/x?a=%2Fapi%2Fx%3Fa%3D1&", 12_000) + "p=/home/bruce",
	} {
		out := timeRedact(t, roots, line)
		if strings.Contains(out, "bruce") {
			t.Errorf("%s: a path survived: %q", name, head(out))
		}
	}
}

// scalingAttempts is how many times bestOf runs a slow input before it counts.
const scalingAttempts = 3

// bestOf runs run(line) until one run is under scalingBound (at most
// scalingAttempts runs) and returns the output and the fastest time. A runner
// that stalls for seconds (core-ci runs every package at once under -race and
// coverage; one 360 KB line took 3.6 s there and 60 ms here) slows one run, not
// three in a row, while a quadratic scan takes 6 to 26 s on every run.
func bestOf(run func(string) string, line string) (out string, best time.Duration) {
	for i := range scalingAttempts {
		start := time.Now()
		out = run(line)
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
		if best <= scalingBound {
			break
		}
	}
	return out, best
}

// TestRedactPaths_UrlAndEncodedTokenShapesAreLinear (round 6): a URL is
// recognised at any scheme start and read to the end of its token, an opaque
// query is not scanned at all, and a token that carries an encoded separator is
// decoded up to maxDecode+1 layers before it is judged. Each shape below makes a
// naive version re-read its token from every scheme start, every "%" or every
// token start, and so is quadratic. None may be, on the text road or on the
// whole-value road, and none may leak. The lines are sized to take well under a
// second each with -race and -coverprofile on a workstation (core-ci runs them
// that way on a shared runner several times slower), and a quadratic scan of
// them still takes many seconds.
func TestRedactPaths_UrlAndEncodedTokenShapesAreLinear(t *testing.T) {
	roots := scalingRoots()
	for name, line := range map[string]string{
		"https runs":            strings.Repeat("https://h/", 15_000) + "?p=/home/bruce",
		"url queries":           strings.Repeat("https://h/x?a=1 ", 8_000) + "/home/bruce",
		"url long query":        "https://h/x?" + strings.Repeat("a=1&", 30_000) + "p=/home/bruce",
		"url fragments":         "https://h/x" + strings.Repeat("#a", 30_000) + "#/home/bruce",
		"scheme-start letters":  strings.Repeat("a!", 50_000) + "/home/bruce",
		"scheme-like runs":      strings.Repeat("ab+c-d.e", 12_000) + " /home/bruce",
		"encoded layers":        "path=" + strings.Repeat("%25", 40_000) + "2Fhome%2Fbruce",
		"encoded separators":    "path=" + strings.Repeat("%2F", 40_000) + "home%2Fbruce",
		"many encoded tokens":   strings.Repeat("a%2Fb ", 15_000) + "/home/bruce",
		"many token starts":     strings.Repeat(`"%2F`, 20_000) + "/home/bruce",
		"opening brackets":      strings.Repeat("(", 100_000) + "%2Fhome%2Fbruce",
		"bracketed tokens":      strings.Repeat("(a%2Fb) ", 10_000) + "/home/bruce",
		"bad escapes":           "path=" + strings.Repeat("%zz&%2", 20_000) + "&%2Fhome%2Fbruce",
		"encoded tokens, paths": strings.Repeat("x=%2Fhome%2Fbruce ", 6_000),
	} {
		if len(line) >= maxLineBytes {
			t.Fatalf("%s: %d bytes would not reach the redactor", name, len(line))
		}
		for road, run := range map[string]func(string) string{"text": roots.redactPaths, "value": roots.redactValue} {
			out, d := bestOf(run, line)
			if d > scalingBound {
				t.Fatalf("%s/%s: %d bytes took %s in each of %d runs, want under %s: the scan is not linear", name, road, len(line), d, scalingAttempts, scalingBound)
			}
			if strings.Contains(out, "bruce") {
				t.Errorf("%s/%s: a path survived: %q", name, road, head(out))
			}
		}
	}
}

// TestRedactPaths_QuotedTokenShapesAreLinear (round 7, G9): a route, a URL and an
// encoded token run to whitespace and no quote, backtick or bracket ends them, so
// a line with many quotes and no whitespace is one long token. A scan that judged
// that token again from each quote (every quote is a place a token may start) is
// quadratic; one judgement covers every start inside it. None of these may be,
// and none may leak.
func TestRedactPaths_QuotedTokenShapesAreLinear(t *testing.T) {
	roots := scalingRoots()
	for name, line := range map[string]string{
		"quoted encoded no path":   strings.Repeat(`"a%41`, 20_000) + " /home/bruce",
		"quotes then a percent":    strings.Repeat(`"a`, 60_000) + " %zz /home/bruce",
		"backtick tokens":          strings.Repeat("`a%41", 20_000) + " /home/bruce",
		"escaped quote tokens":     strings.Repeat(`\"a%41`, 15_000) + " /home/bruce",
		"quoted route paths":       strings.Repeat(`"/api/x`, 20_000) + " /home/bruce",
		"quoted route queries":     strings.Repeat(`"/api/x?a=1`, 15_000) + " /home/bruce",
		"quoted url paths":         strings.Repeat(`"https://h/x`, 12_000) + " /home/bruce",
		"quoted url queries":       strings.Repeat(`"https://h/x?a=%41`, 8_000) + " /home/bruce",
		"quoted tokens, spaced":    strings.Repeat(`"a%41" `, 15_000) + "/home/bruce",
		"quote runs, then percent": strings.Repeat(`"`, 100_000) + "%41 /home/bruce",
	} {
		if len(line) >= maxLineBytes {
			t.Fatalf("%s: %d bytes would not reach the redactor", name, len(line))
		}
		for road, run := range map[string]func(string) string{"text": roots.redactPaths, "value": roots.redactValue} {
			out, d := bestOf(run, line)
			if d > scalingBound {
				t.Fatalf("%s/%s: %d bytes took %s in each of %d runs, want under %s: the scan is not linear", name, road, len(line), d, scalingAttempts, scalingBound)
			}
			if strings.Contains(out, "bruce") {
				t.Errorf("%s/%s: a path survived: %q", name, road, head(out))
			}
		}
	}
}

func head(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
