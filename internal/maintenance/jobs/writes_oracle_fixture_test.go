// file: internal/maintenance/jobs/writes_oracle_fixture_test.go
// version: 1.5.0
// guid: 8fbddfb2-91e8-42bd-ab62-8e8e7e8cd794
// last-edited: 2026-11-19

package jobs_test

import (
	"slices"
	"strings"
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
		{"MixedReadOnly", []string{"authors"}, "the helper may fall back to a package variable's step, so that step's writes are reported (the safe side) though this caller passes a reader"},
		{"ViaPartialImpls", []string{"books"}, "the program knows one implementation of the interface (it does not write), the production wiring passes the store: the method name still says write"},
		{"ViaOutsideBody", []string{"books"}, "the one known implementation writes through code outside the module: only the method name is evidence"},
		{"ViaFieldCallback", []string{"books"}, "a function value loaded from a struct field and passed to a shared helper"},
		{"ViaGlobalCallback", []string{"authors"}, "a function value loaded from a package-level slice and passed to a shared helper"},
		{"ViaNilDefault", []string{"authors"}, "the helper's nil-means-default idiom"},
		{"ViaUnwiredInterface", []string{"books"}, "a non-verb method on a value the program never passes: the class hierarchy's implementation"},
		{"ViaMapCallback", []string{"authors"}, "a function value loaded from a package-level map"},
		{"ViaContextFunc", []string{"authors", "books"}, "a function value no flow reaches: the signature-based callees (every function of that signature)"},
		{"ViaOrphanInterface", []string{"books"}, "an interface nothing implements is judged by its method name"},
		{"ViaGeneric", []string{"books"}, "a closure handed to an instance of this module's generic"},
		{"UsesStdGeneric", nil, "a standard library generic instance is not followed"},
		{"ReportsProgress", []string{"books"}, "a progress reporter's own name is not judged, but its implementation's body is walked: it writes through the store"},
		{"ReportsQuietly", nil, "a progress reporter that does nothing: neither its name nor its body is a write"},
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

// TestOracleWalk_NotWalkedIsOnlyTheHeartbeat: the walk skips the scan stand-down
// hold's heartbeat (Checkpoint, Beat, beat) and no other method of it: take,
// Finish and Release still are followed.
func TestOracleWalk_NotWalkedIsOnlyTheHeartbeat(t *testing.T) {
	const recv = "(*github.com/falkcorp/audiobook-organizer/internal/operations/registry.ScanStandDownHold)."
	for _, name := range []string{"Checkpoint", "Beat", "beat"} {
		if !oracleNotWalked.MatchString(recv + name) {
			t.Errorf("%s is the heartbeat and must not be walked", name)
		}
	}
	for _, name := range []string{"take", "Finish", "Release", "Retain", "CheckpointAndFinish"} {
		if oracleNotWalked.MatchString(recv + name) {
			t.Errorf("%s is not the heartbeat and must be walked", name)
		}
	}
}

// TestOracleWalk_RecordsWhatItCannotResolve: a call through a function value that
// neither the flow graph nor the signature graph can name is recorded, not
// skipped, so a job that makes one fails TestJobWrites_NoUnresolvedCalls unless
// the call is allow-listed with a reason.
func TestOracleWalk_RecordsWhatItCannotResolve(t *testing.T) {
	op := oracleFixture(t)
	// Every shape the signature-based fallback is not applied to (a declared
	// variable, a call result, an outside call's closure) must still be
	// recorded, never skipped. The key names where the value was made: the
	// outside function a call result came from, the variable nothing stores to
	// (read where the caller passes it, for ViaUnsetStepParam), a parameter no
	// walked caller supplies.
	for root, what := range map[string]string{
		"ViaUnresolved":     "#outside:sync.Map.Load",
		"ViaUnsetHook":      "#unsetHook",
		"ViaCallResult":     "#outside:sync.Map.Load",
		"ViaOutsideClosure": "#outside:context.WithCancel",
		"ViaUnsetStepParam": "#unsetStep",
		"ViaPhiOfHooks":     "#unsetHook",
		"ViaOnlyAsValue":    "#parameter:fn",
	} {
		_, _, _, calls, _ := derivedWrites(op, op.roots[0].Func(root))
		var found []string
		for k := range calls {
			if strings.HasPrefix(k, "UNRESOLVED.") && strings.HasSuffix(k, what) {
				found = append(found, k)
			}
		}
		if len(found) != 1 {
			t.Errorf("%s records %v, want exactly one UNRESOLVED call ending %s", root, calls, what)
		}
	}
	_, _, _, calls, _ := derivedWrites(op, op.roots[0].Func("ViaFieldCallback"))
	for k := range calls {
		if strings.HasPrefix(k, "UNRESOLVED.") {
			t.Errorf("ViaFieldCallback records %s, but its callback is resolvable", k)
		}
	}
}

// TestOracleWalk_KeysAGenericStoreFunction: a call to an instance of a generic
// function of the store package is keyed by the generic, like any store function
// (an instance has no package of its own, and keying by it dropped the call).
func TestOracleWalk_KeysAGenericStoreFunction(t *testing.T) {
	op := oracleFixture(t)
	_, _, _, calls, _ := derivedWrites(op, op.roots[0].Func("ViaGenericStoreFn"))
	for k := range calls {
		if strings.HasPrefix(k, "STOREFN.") && strings.HasSuffix(k, ".UpdateWith") {
			return
		}
	}
	t.Errorf("ViaGenericStoreFn records %v, want a STOREFN key for UpdateWith", calls)
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
// graph the interface call and the function value no flow reaches find nothing,
// while the shapes that need no graph (a callback the caller names, a function
// value loaded from a field, a variable or a slice that the module stores into)
// still do. It is the sensitivity check of the test above: if the graph shapes found
// their writes without the graph, that test would prove nothing about it.
func TestOracleWalk_NeedsTheCallGraph(t *testing.T) {
	op := *oracleFixture(t)
	empty := callgraph.New(nil)
	op.graph, op.cha = empty, empty
	for _, root := range []string{"ViaInterface", "ViaContextFunc"} {
		if got := fixtureWrites(t, &op, root); len(got) != 0 {
			t.Errorf("%s: finds %v without a call graph; the fixture does not exercise the graph", root, got)
		}
	}
	// What the module's own stores into a field, a package variable or a slice
	// say (ViaFuncValue, ViaStructField, ViaFieldCallback, ViaGlobalCallback) is
	// resolved without any call graph, like the caller's own callback.
	for root, want := range map[string][]string{
		"ViaNonVerbStoreMethod": {"books"}, "SharedWriter": {"authors"},
		"ViaFuncValue": {"authors"}, "ViaStructField": {"books"},
		"ViaFieldCallback": {"books"}, "ViaGlobalCallback": {"authors"},
	} {
		if got := fixtureWrites(t, &op, root); !slices.Equal(got, want) {
			t.Errorf("%s: finds %v without a call graph, want %v (it needs no graph)", root, got, want)
		}
	}
}

// TestOracleWalk_DefaultDeny: every shape the round-2 dispatch walked to no
// target and no key (round-2 review B1 (a)-(f), M1) either finds the write or
// records an UNRESOLVED key, and where the program holds the function the write
// is found as well. want is the write set; key is a fragment one UNRESOLVED key
// must contain ("" when the shape is fully resolved and must record none).
func TestOracleWalk_DefaultDeny(t *testing.T) {
	op := oracleFixture(t)
	for _, tc := range []struct {
		root string
		want []string
		key  string
		why  string
	}{
		{"ViaInjectedField", []string{"books"}, "#apply", "(a) a field of a parameter filled by an exported constructor's parameter: the in-program caller's function, and a key for the wiring's"},
		{"ViaInjectedFieldCallback", []string{"books"}, "#apply", "(a) the same field handed to a shared helper"},
		{"ViaDefaultPlusSetter", []string{"authors"}, "#settable", "(b) a default plus an exported setter: the default, and a key for the setter"},
		{"ViaParamOrUnset", nil, "#unsetStepB", "(c) a parameter mixed with an unset default, every caller passing nil"},
		{"ViaTwoLevelForward", nil, "#unsetStepC", "(d) an unset variable forwarded through two helpers"},
		{"ViaHelperOfCallResult", nil, "#outside:sync.", "(d) a helper handed a call result that names no function"},
		{"ViaHelperOfOutsideClosure", nil, "#outside:context.WithCancel", "(d) a helper handed an outside closure"},
		{"ViaCopiedSteps", []string{"authors"}, "", "(e) a local slice filled by copy from a package-level slice"},
		{"ViaRangeCopied", nil, "#src", "(e) a local slice filled from a parameter's elements"},
		{"ViaHandedOutside", nil, "#fire", "(f) a field nothing stores to handed to code outside the module"},
		{"ViaPartialNonVerb", []string{"books"}, "", "M1: a non-verb method whose implementations the program knows in part: every implementation the program holds is walked"},
		{"ViaOrphanNonVerb", nil, "#iface:", "M1: a non-verb method of a module interface the program holds no implementation of"},
		{"ViaOutsideIface", nil, "#iface:fmt.Stringer.String", "M1: a non-verb method of an interface declared outside the module"},
	} {
		kinds, _, _, calls, _ := derivedWrites(op, op.roots[0].Func(tc.root))
		if !slices.Equal(kinds, tc.want) {
			t.Errorf("%s: the walk finds %v, want %v (%s)", tc.root, kinds, tc.want, tc.why)
		}
		var keys []string
		for k := range calls {
			if strings.HasPrefix(k, "UNRESOLVED.") {
				keys = append(keys, k)
			}
		}
		switch {
		case tc.key == "" && len(keys) != 0:
			t.Errorf("%s: records %v, want no UNRESOLVED key (%s)", tc.root, keys, tc.why)
		case tc.key != "" && !slices.ContainsFunc(keys, func(k string) bool { return strings.Contains(k, tc.key) }):
			t.Errorf("%s: records %v, want an UNRESOLVED key containing %q (%s)", tc.root, keys, tc.key, tc.why)
		}
	}
}
