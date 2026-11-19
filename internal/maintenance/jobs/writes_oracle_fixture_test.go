// file: internal/maintenance/jobs/writes_oracle_fixture_test.go
// version: 1.2.0
// guid: 8fbddfb2-91e8-42bd-ab62-8e8e7e8cd794
// last-edited: 2026-11-19

package jobs_test

import (
	"slices"
	"sync"
	"testing"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa/ssautil"
)

// The oracle walk is tested on its own fixture (testdata/oraclefix), not only on
// the jobs it is pointed at: the jobs happen to hold no write that is reached
// through a name the oracle cannot classify, so a walk that stopped following
// interface calls or function values would still pass every job-level test. The
// fixture holds one root per shape.

const oracleFixturePkg = oracleModule + "/internal/maintenance/jobs/testdata/oraclefix/fix"

var (
	oracleFixtureOnce sync.Once
	oracleFixtureProg *oracleProgram
	oracleFixtureErr  error
)

func oracleFixture(t *testing.T) *oracleProgram {
	t.Helper()
	oracleFixtureOnce.Do(func() { oracleFixtureProg, oracleFixtureErr = loadOracleProgram(false, oracleFixturePkg) })
	if oracleFixtureErr != nil {
		t.Fatal(oracleFixtureErr)
	}
	return oracleFixtureProg
}

// fixtureWrites is the resource kinds the walk from fix.<root> finds.
func fixtureWrites(t *testing.T, op *oracleProgram, root string) []string {
	t.Helper()
	fn := op.roots[0].Func(root)
	if fn == nil {
		t.Fatalf("the fixture has no function %s", root)
	}
	kinds, _, _, _, _ := derivedWrites(op, fn)
	return kinds
}

// TestOracleWalk_FindsWritesReachedByOtherNames: a store write reached through an
// interface method, a function value or a method whose names say nothing about
// writing is classified, and a callback is attributed to the caller that passed
// it and to no other.
func TestOracleWalk_FindsWritesReachedByOtherNames(t *testing.T) {
	op := oracleFixture(t)
	for _, tc := range []struct {
		root string
		want []string
		why  string
	}{
		{"ViaInterface", []string{"books"}, "an interface method named Undo, implemented by a wrapper that calls ModifyBook"},
		{"ViaFuncValue", []string{"authors"}, "a function value read from a package variable, registered in init under another name"},
		{"ViaStructField", []string{"books"}, "a function value stored in a struct field by a constructor the root never calls"},
		{"ViaNonVerbStoreMethod", []string{"books"}, "a store method whose name starts with Revert"},
		{"SharedWriter", []string{"authors"}, "a callback passed to a shared helper"},
		{"SharedReadOnly", nil, "a reading callback passed to the same shared helper writes nothing (the helper's callback is the caller's, not every caller's)"},
		{"MixedReadOnly", nil, "a callback that may also be a package variable's step is the caller's, not the graph's"},
		{"ViaOrphanInterface", []string{"books"}, "an interface nothing implements is judged by its method name"},
		{"ViaGeneric", []string{"books"}, "a closure handed to an instance of this module's generic"},
		{"UsesStdGeneric", nil, "a standard library generic instance is not followed"},
		{"ReportsProgress", nil, "a progress reporter is not a books write, nor is what implements it"},
		{"ConvertedReadOnly", nil, "a callback converted inside the shared helper is still the caller's"},
		{"ConvertedWriter", []string{"authors"}, "the converting helper with a writing callback"},
		{"ViaEitherField", []string{"authors", "books"}, "a function value chosen between two package-level values"},
		{"Read", nil, "a read"},
	} {
		got := fixtureWrites(t, op, tc.root)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: the walk finds %v, want %v (%s)", tc.root, got, tc.want, tc.why)
		}
	}
}

// TestOracleWalk_FollowsGenericInstancesByTheirOrigin: an instance of a generic
// function has no package of its own (Pkg is nil), so what the walk follows is
// decided by the package of the generic it was made from: this module's, yes;
// the standard library's, no.
func TestOracleWalk_FollowsGenericInstancesByTheirOrigin(t *testing.T) {
	op := oracleFixture(t)
	var module, std int
	for f := range ssautil.AllFunctions(op.prog) {
		if f.Pkg != nil || f.Origin() == nil || f.Parent() != nil {
			continue
		}
		path := f.Origin().Pkg.Pkg.Path()
		switch {
		case path == oracleFixturePkg:
			module++
			if !oracleFollows(f) {
				t.Errorf("%s, an instance of this module's generic, is not followed", f)
			}
		case path == "slices":
			std++
			if oracleFollows(f) {
				t.Errorf("%s, an instance of a standard library generic, is followed", f)
			}
		}
	}
	if module == 0 || std == 0 {
		t.Fatalf("the fixture has %d module and %d standard library generic instances; want both", module, std)
	}
}

// TestOracleWalk_NeedsTheCallGraph pins what the call graph adds: with an empty
// graph (the walk before F5-18m resolved no interface call and no function value)
// the three graph shapes find nothing, while the shapes that need no graph still
// do. It is the sensitivity check of the test above: if the graph shapes found
// their writes without the graph, that test would prove nothing about it.
func TestOracleWalk_NeedsTheCallGraph(t *testing.T) {
	op := *oracleFixture(t)
	empty := callgraph.New(nil)
	op.graph, op.cha = empty, empty
	for _, root := range []string{"ViaInterface", "ViaFuncValue", "ViaStructField"} {
		if got := fixtureWrites(t, &op, root); len(got) != 0 {
			t.Errorf("%s: finds %v without a call graph; the fixture does not exercise the graph", root, got)
		}
	}
	for root, want := range map[string][]string{"ViaNonVerbStoreMethod": {"books"}, "SharedWriter": {"authors"}} {
		if got := fixtureWrites(t, &op, root); !slices.Equal(got, want) {
			t.Errorf("%s: finds %v without a call graph, want %v (it needs no graph)", root, got, want)
		}
	}
}
