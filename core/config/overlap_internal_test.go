// file: core/config/overlap_internal_test.go
// version: 1.0.1
// guid: 8b1d6e43-2a95-4f7c-9e60-d4c3a7b15f28
// last-edited: 2026-11-12

package config

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// pairAB claims p.a and p.ab: two keys whose names share a string prefix.
type pairAB struct {
	A  int `json:"a"`
	AB int `json:"ab"`
}

// nestedAB claims p.n.a and p.n.ab: keys two levels below the section path, so a
// section can claim a path that another section's path is above and its keys
// below.
type nestedAB struct {
	N pairAB `json:"n"`
}

// TestOverlap_Cases: what Register accepts and refuses at the section level, in
// either registration order, with the error text that names both sections.
func TestOverlap_Cases(t *testing.T) {
	leaf := func(p Path) any { return Define(p, 0) }
	pair := func(p Path) any { return Define(p, pairAB{}) }
	mapsec := func(p Path) any { return Define(p, map[string]int{}) }
	nested := func(p Path) any { return Define(p, nestedAB{}) }
	beacons := func(p Path) any { return Define(p, cacheBeacons{}) }
	type step struct {
		sec  any
		want string // "" registers; otherwise a substring of the ErrDuplicate text
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"exact path twice", []step{
			{leaf("core.t.a"), ""},
			{leaf("core.t.a"), "section core.t.a is registered twice"},
		}},
		{"a claim under a registered claim", []step{
			{leaf("core.t.a"), ""},
			{leaf("core.t.a.b"), "section core.t.a.b's key core.t.a.b overlaps section core.t.a's key core.t.a"},
		}},
		{"a claim that sits between a registered section's path and its keys", []step{
			{nested("core.t"), ""},
			{leaf("core.t.n"), "section core.t.n's key core.t.n overlaps section core.t's key core.t.n.a"},
		}},
		{"a section whose keys run through a registered claim", []step{
			{leaf("core.t.n"), ""},
			{nested("core.t"), "section core.t's key core.t.n.a overlaps section core.t.n's key core.t.n"},
		}},
		{"a claim over a registered claim", []step{
			{leaf("core.t.a.b"), ""},
			{leaf("core.t.a"), "section core.t.a's key core.t.a overlaps section core.t.a.b's key core.t.a.b"},
		}},
		{"a key equal to a registered key", []step{
			{pair("core.t"), ""},
			{leaf("core.t.a"), "section core.t.a's key core.t.a overlaps section core.t's key core.t.a"},
		}},
		{"a registered key equal to the new key, other order", []step{
			{leaf("core.t.ab"), ""},
			{pair("core.t"), "section core.t's key core.t.ab overlaps section core.t.ab's key core.t.ab"},
		}},
		{"a map section over a registered key", []step{
			{leaf("core.t.m.x"), ""},
			{mapsec("core.t.m"), "section core.t.m's key core.t.m overlaps section core.t.m.x's key core.t.m.x"},
		}},
		{"a key under a map section", []step{
			{mapsec("core.t.m"), ""},
			{leaf("core.t.m.x"), "overlaps section core.t.m's key core.t.m"},
		}},
		{"siblings that share a string prefix, not a segment", []step{
			{leaf("core.t.a.b"), ""},
			{leaf("core.t.a.bc"), ""},
			{leaf("core.t.a.b.c"), "overlaps section core.t.a.b's key core.t.a.b"},
		}},
		{"a.b and a.bc, the longer first", []step{
			{leaf("core.t.a.bc"), ""},
			{leaf("core.t.a.b"), ""},
			{leaf("core.t.a"), "overlaps"},
		}},
		{"keys that share a string prefix", []step{
			{leaf("core.t.ab"), ""},
			{pair("core.t.a"), ""},
			{leaf("core.t.abc"), ""},
		}},
		{"a section path that is a string prefix of another's", []step{
			{pair("core.t.a"), ""},
			{pair("core.t.ab"), ""},
			{leaf("core.t.abc"), ""},
			{leaf("core.t.a.a"), "overlaps section core.t.a's key core.t.a.a"},
		}},
		{"a refused section is not registered", []step{
			{leaf("core.t.a"), ""},
			{leaf("core.t.a.b"), "overlaps"},
			{leaf("core.t.a.b.c"), "overlaps section core.t.a's key core.t.a"},
			{leaf("core.t.c"), ""},
		}},
		{"a section under an entry list's parent", []step{
			{beacons("core.t.b"), ""},
			{leaf("core.t.b.zed"), "section core.t.b.zed lies under core.t.b, where section core.t.b's list core.t.b.beacons names its entries' secrets"},
		}},
		{"an entry list over a registered section, other order", []step{
			{leaf("core.t.b.zed"), ""},
			{beacons("core.t.b"), "section core.t.b.zed lies under core.t.b, where section core.t.b's list core.t.b.beacons names its entries' secrets"},
		}},
		{"a sibling of an entry list's parent", []step{
			{beacons("core.t.b"), ""},
			{leaf("core.t.bz"), ""},
			{leaf("core.t.c"), ""},
		}},
	}
	c := newTemplateCache(maxSectionTemplates)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCacheStore(t, "gotham", c)
			for i, st := range tc.steps {
				_, err := RegisterSections(s.Registry(), st.sec)
				switch {
				case st.want == "" && err != nil:
					t.Fatalf("step %d: %v", i, err)
				case st.want != "" && err == nil:
					t.Fatalf("step %d registered; want a refusal naming %q", i, st.want)
				case st.want != "":
					if !errors.Is(err, ErrDuplicate) || !strings.Contains(err.Error(), st.want) {
						t.Fatalf("step %d: %v\nwant ErrDuplicate containing %q", i, err, st.want)
					}
				}
			}
		})
	}
}

// specSink is a Registry that keeps the spec Register builds, so a test can
// build a section without a store.
type specSink struct{ sp *sectionSpec }

func (r *specSink) register(sp *sectionSpec) (*binding, error) {
	r.sp = sp
	return &binding{}, nil
}

func builtOnly(t testing.TB, sec any) *section {
	t.Helper()
	sink := &specSink{}
	if _, err := sec.(registrar).registerWith(sink); err != nil {
		t.Fatal(err)
	}
	s, err := buildSection(sink.sp)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestOverlap_IndexMatchesFullScan: for random sequences of sections the index
// refuses exactly what the scan of every registered section refused, with the
// same error text, and accepts exactly the same ones. Segment names that share
// string prefixes, map and list sections and every depth are in the mix.
func TestOverlap_IndexMatchesFullScan(t *testing.T) {
	segs := []string{"a", "ab", "abc", "b"}
	var paths []Path
	var walk func(p Path, depth int)
	walk = func(p Path, depth int) {
		if depth > 0 {
			paths = append(paths, p)
		}
		if depth == 3 {
			return
		}
		for _, s := range segs {
			walk(p.Join(s), depth+1)
		}
	}
	walk("core.t", 0)
	kinds := []func(Path) any{
		func(p Path) any { return Define(p, 0) },
		func(p Path) any { return Define(p, pairAB{}) },
		func(p Path) any { return Define(p, map[string]int{}) },
		func(p Path) any { return Define(p, cacheBeacons{}) },
		func(p Path) any { return Define(p, nestedAB{}) },
	}
	// Build once per (path, kind): the pool the trials draw from.
	type cand struct {
		sec  *section
		name string
	}
	var pool []cand
	for _, p := range paths {
		for ki, mk := range kinds {
			pool = append(pool, cand{builtOnly(t, mk(p)), fmt.Sprintf("%s/%d", p, ki)})
		}
	}
	rng := rand.New(rand.NewPCG(7, 11))
	refused, accepted := 0, 0
	for trial := range 60 {
		ix := newOverlapIndex()
		var added []*section
		for range 40 {
			c := pool[rng.IntN(len(pool))]
			got := ix.check(c.sec)
			sorted := slices.Clone(added)
			slices.SortFunc(sorted, func(a, b *section) int { return strings.Compare(string(a.path), string(b.path)) })
			var want error
			for _, o := range sorted {
				if want = conflict(c.sec, o); want != nil {
					break
				}
			}
			if (got == nil) != (want == nil) || (got != nil && got.Error() != want.Error()) {
				t.Fatalf("trial %d, registering %s after %d sections:\nindex: %v\nscan:  %v", trial, c.name, len(added), got, want)
			}
			if want != nil {
				refused++
				continue
			}
			accepted++
			ix.add(c.sec)
			added = append(added, c.sec)
		}
	}
	if refused < 200 || accepted < 200 {
		t.Errorf("only %d refusals and %d acceptances: the trials do not exercise both sides", refused, accepted)
	}
}
