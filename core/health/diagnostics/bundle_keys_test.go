// file: core/health/diagnostics/bundle_keys_test.go
// version: 1.1.0
// guid: 8d2f4a61-7c03-4e95-b1a8-3f6e0c9d5b72
// last-edited: 2026-11-15

package diagnostics

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

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
// the linear search names every member exactly as the quadratic reference does,
// and makes at most three lookups per key.
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
		st.keyProbes = 0
		got, want := st.walkObject(x, 0), referenceKeys(st, x)
		if st.keyProbes > 3*len(x) {
			t.Fatalf("%d lookups to name %d keys, want at most 3 per key: %v", st.keyProbes, len(x), x)
		}
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
// redact to "<path>".
func collidingKeys(n int) map[string]any {
	x := make(map[string]any, n)
	for i := range n {
		x[fmt.Sprintf("/a/%d", i)] = i
	}
	return x
}

// TestKeyNamer_ProbesAreLinear (round 8, M5; round 9, M-2): the suffix of the
// n-th key that collapses to the same text used to be found by probing from "#2",
// so n keys cost n*n/2 lookups: 16,000 path keys took 12.8 s, a 100,000-file
// section would outlast the 30-minute task. The test counts lookups in the object
// being built, which is the same on every machine, and asserts at most three per
// key, so it has no clock to be moved by a loaded runner (a wall-clock bound
// failed core-ci twice, at 3.05 s and at 3.29 s, for a scan that was linear).
// Under the quadratic search 1,000 keys already cost 500,000 lookups against a
// bound of 3,000.
//
// Four shapes, each at four sizes, with no clock: every key collapses to one text;
// the suffix names are already taken by real keys, which the counter has to pass
// once; the keys collapse to many texts at once; and no key collapses at all.
func TestKeyNamer_ProbesAreLinear(t *testing.T) {
	shapes := map[string]func(i int) string{
		"one text":   func(int) string { return "<path>" },
		"many texts": func(i int) string { return fmt.Sprintf("<path%d>", i%7) },
		"distinct":   func(i int) string { return fmt.Sprintf("key%d", i) },
	}
	for name, base := range shapes {
		for _, n := range []int{1_000, 4_000, 16_000, 64_000} {
			kn := newKeyNamer(n)
			for i := range n {
				kn.out[kn.name(base(i))] = i
				// Fatal as soon as the bound is passed, so a quadratic search
				// fails in microseconds instead of running to the end.
				if kn.probes > 3*n {
					t.Fatalf("%s, %d keys: %d lookups after %d keys, want at most %d in all: the suffix search is not linear", name, n, kn.probes, i+1, 3*n)
				}
			}
			if len(kn.out) != n {
				t.Fatalf("%s, %d keys: %d members, a member was lost or overwritten", name, n, len(kn.out))
			}
		}
	}

	// Names taken in advance: <path>#2 .. <path>#(n/2) are real keys, so the
	// colliding keys pass over n/2 occupied names, once.
	for _, n := range []int{1_000, 4_000, 16_000, 64_000} {
		kn := newKeyNamer(n)
		for j := 2; j <= n/2+1; j++ {
			kn.out[kn.name(fmt.Sprintf("<path>#%d", j))] = -j
		}
		for i := range n - n/2 {
			kn.out[kn.name("<path>")] = i
			if kn.probes > 3*n {
				t.Fatalf("taken suffixes, %d keys: %d lookups after %d colliding keys, want at most %d in all", n, kn.probes, i+1, 3*n)
			}
		}
		if len(kn.out) != n {
			t.Errorf("taken suffixes, %d keys: %d members, %d lookups, want %d members and at most %d lookups", n, len(kn.out), kn.probes, n, 3*n)
		}
	}

	// The names come out in order, and a value is still under the key it was
	// stored with.
	kn := newKeyNamer(8)
	var got []string
	for range 4 {
		k := kn.name("<path>")
		kn.out[k] = nil
		got = append(got, k)
	}
	if want := []string{"<path>", "<path>#2", "<path>#3", "<path>#4"}; !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	// The count is exact for this shape (one lookup for the first key, two for
	// each of the next three), so a count that stopped counting would not pass
	// for a linear search.
	if kn.probes != 7 {
		t.Errorf("4 colliding keys took %d lookups, want 7", kn.probes)
	}
}

// TestWalkObject_CollidingKeysKeepEveryMember: the whole walk, with the redaction of
// every key, over 16,000 keys that all redact to "<path>" (17 s with the quadratic
// search) keeps every member under its own name in sorted key order, and names
// them with two lookups per key. There is no time bound here: a clock is
// what the counter replaced.
func TestWalkObject_CollidingKeysKeepEveryMember(t *testing.T) {
	const n = 16_000
	x := collidingKeys(n)
	st := keysBuild()
	out := st.walkObject(x, 0)
	// One lookup for the first key and two for each other: exactly 2n-1.
	if st.keyProbes != 2*n-1 {
		t.Errorf("%d lookups to name %d colliding keys, want %d: the suffix search is not linear, or the walk does not count it", st.keyProbes, n, 2*n-1)
	}
	sorted := slices.Sorted(maps.Keys(x))
	if len(out) != n || out["<path>"] != x[sorted[0]] || out["<path>#2"] != x[sorted[1]] || out[fmt.Sprintf("<path>#%d", n)] != x[sorted[n-1]] {
		t.Errorf("%d members, want %d in key order: <path>=%v <path>#2=%v", len(out), n, out["<path>"], out["<path>#2"])
	}
}
