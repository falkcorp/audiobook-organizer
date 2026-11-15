// file: core/health/diagnostics/bundle_keys_test.go
// version: 1.0.1
// guid: 8d2f4a61-7c03-4e95-b1a8-3f6e0c9d5b72
// last-edited: 2026-11-15

package diagnostics

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// keysBuild is a build that walks objects: the redactor and the path roots of
// the scaling tests, and nothing it writes.
func keysBuild() *build {
	return &build{red: log.NewRedactor(nil), roots: scalingRoots(), written: map[string]bool{}}
}

// referenceKeys is walkObject's suffix rule as it was written before it was made
// linear: every key that collapses probes "#2", "#3", ... from 2 until a name is
// free, in the sorted order of the original keys. It is the specification the
// linear version is held to, and it is quadratic on purpose.
func referenceKeys(st *build, x map[string]any) map[string]any {
	keys := slices.Sorted(maps.Keys(x))
	out := make(map[string]any, len(x))
	for _, k := range keys {
		base := st.redactKey(k)
		nk := base
		for n := 2; ; n++ {
			if _, taken := out[nk]; !taken {
				break
			}
			nk = fmt.Sprintf("%s#%d", base, n)
		}
		out[nk] = x[k]
	}
	return out
}

// TestWalkObject_CollisionSuffixFormatIsPinned (round 8, M5): the bundle's key
// text for a collision is written down, so making the search linear cannot change
// a bundle. A key that redacts to text no other key redacts to carries no suffix;
// the keys that collapse to "<path>" are numbered from 2 in the sorted order of
// the original keys; and a real key that already reads like a suffix is moved
// aside, never overwritten, never merged.
func TestWalkObject_CollisionSuffixFormatIsPinned(t *testing.T) {
	st := keysBuild()
	got := st.walkObject(map[string]any{
		"/Users/bruce/a": 1, // sorted first: keeps "<path>"
		"/Users/bruce/b": 2, // "<path>#2"
		"/Users/bruce/c": 3, // "<path>#3"
		"/srv/lib/x":     4, // under a root: "<root:lib>/x", no collision
		"<path>#2":       5, // a real key reading like a suffix: moved aside
		"Robin":          6, // untouched
	}, 0)
	want := map[string]any{
		"<path>":       1,
		"<path>#2":     2,
		"<path>#3":     3,
		"<root:lib>/x": 4,
		"<path>#2#2":   5,
		"Robin":        6,
	}
	if !maps.Equal(got, want) {
		t.Errorf("collision keys changed:\n got  %v\n want %v", got, want)
	}

	// No collision, no suffix: a bundle without colliding keys is byte for byte
	// what it was.
	plain := map[string]any{"Robin": 1, "Alfred": 2, "/srv/lib/y": 3, "Bat": 4}
	if got := st.walkObject(plain, 0); !maps.Equal(got, map[string]any{"Robin": 1, "Alfred": 2, "<root:lib>/y": 3, "Bat": 4}) {
		t.Errorf("keys with no collision were suffixed or altered: %v", got)
	}
}

// TestWalkObject_LinearSuffixMatchesTheReference: over 1,000 random objects whose keys
// collapse, repeat and spell suffixes of their own ("<path>#2", "x#3", "x#3#2"),
// the linear search names every member exactly as the quadratic reference does.
func TestWalkObject_LinearSuffixMatchesTheReference(t *testing.T) {
	st := keysBuild()
	pool := []string{
		"/Users/bruce/a", "/Users/bruce/b", "/home/bat/x", "/srv/lib/same", "/srv/lib/other",
		"<path>", "<path>#2", "<path>#3", "<path>#2#2", "<path>#10", "<root:lib>/same", "<root:lib>/same#2",
		"x", "x#2", "x#3", "x#3#2", "Robin", "Robin#2", "#2", "", "#",
	}
	rng := rand.New(rand.NewPCG(2026, 8))
	for range 1000 {
		x := map[string]any{}
		for range 1 + rng.IntN(len(pool)) {
			x[pool[rng.IntN(len(pool))]] = len(x)
		}
		// Keys that collapse in bulk, so the counter has real work to do.
		for i := range rng.IntN(40) {
			x[fmt.Sprintf("/Users/bruce/Books/%03d.m4b", i)] = 100 + i
		}
		got, want := st.walkObject(x, 0), referenceKeys(st, x)
		if len(got) != len(x) {
			t.Fatalf("%d members, want %d (a member was lost): %v", len(got), len(x), x)
		}
		// walkMember walks the values; these are numbers, so they come back as they went in.
		if !maps.Equal(got, want) {
			t.Fatalf("linear and reference disagree on %v:\n got  %v\n want %v", x, got, want)
		}
	}
}

// collidingKeys is n members whose keys are all different absolute paths and all
// redact to "<path>". The keys are short on purpose: the redaction of a key costs
// far more than the naming of it (54 us for a 30-byte path against under 1 us for
// the suffix, under -race and coverage), and this test is about the naming.
func collidingKeys(n int) map[string]any {
	x := make(map[string]any, n)
	for i := range n {
		x[fmt.Sprintf("/a/%d", i)] = i
	}
	return x
}

// timeKeys is the best of scalingAttempts walks of x. A run over the bound is
// repeated only when it is under 4 times the bound: a stalled runner slows one
// run, and a quadratic walk (12.8 s for 16,000 keys) must not be run three times.
func timeKeys(x map[string]any) (out map[string]any, best time.Duration) {
	st := keysBuild()
	for i := range scalingAttempts {
		start := time.Now()
		out = st.walkObject(x, 0)
		d := time.Since(start)
		if i == 0 || d < best {
			best = d
		}
		if best <= scalingBound || best > 4*scalingBound {
			break
		}
	}
	return out, best
}

// TestWalkObject_CollidingKeysAreLinear (round 8, M5): the suffix of the n-th key
// that collapses to the same text used to be found by probing from "#2", so n keys
// cost n*n/2 probes: 16,000 path keys took 12.8 s, a 100,000-file section would
// outlast the 30-minute task. 16,000 and 64,000 colliding keys are walked under
// the bound, every member is kept under its own name, and quadrupling the keys
// costs about four times as much, not sixteen.
func TestWalkObject_CollidingKeysAreLinear(t *testing.T) {
	var small time.Duration
	for _, n := range []int{16_000, 64_000} {
		x := collidingKeys(n)
		out, d := timeKeys(x)
		t.Logf("%d colliding keys: %s", n, d)
		if d > scalingBound {
			t.Fatalf("%d colliding keys took %s in each of up to %d runs, want under %s: the suffix search is not linear", n, d, scalingAttempts, scalingBound)
		}
		if len(out) != n {
			t.Fatalf("%d members, want %d", len(out), n)
		}
		// The first key in sorted order keeps the bare name, the k-th (k >= 2) is
		// "<path>#k".
		sorted := slices.Sorted(maps.Keys(x))
		if out["<path>"] != x[sorted[0]] || out["<path>#2"] != x[sorted[1]] || out[fmt.Sprintf("<path>#%d", n)] != x[sorted[n-1]] {
			t.Errorf("%d keys: suffixes are not in key order: <path>=%v <path>#2=%v <path>#%d=%v", n, out["<path>"], out["<path>#2"], n, out[fmt.Sprintf("<path>#%d", n)])
		}
		if n == 16_000 {
			small = d
		} else if d > 10*max(small, 25*time.Millisecond) {
			t.Errorf("4x the keys took %s against %s: more than ten times, the cost is not linear", d, small)
		}
	}
}
