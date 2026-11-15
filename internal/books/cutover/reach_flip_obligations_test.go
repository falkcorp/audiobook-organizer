// file: internal/books/cutover/reach_flip_obligations_test.go
// version: 1.0.0
// guid: 0b7d4e92-6a35-4c18-9f40-d82c1a5e7b36
// last-edited: 2026-11-15

package cutover

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// The flip's open obligations (round-3 MINOR-C).
//
// Books may mount with LegacyAuthCore only when this package's oracle has
// nothing left that it knows it does not compare. Five things are still open; each
// is a named obligation of F4-08 (or the handler migration), listed in
// reachFlipObligations. reachFlipProblems fails at the flip while the list is
// non-empty, one problem per entry.
//
// The list is not trusted on its own. Each entry is tied to evidence the oracle
// already holds (reachObligationEvidence reads the recording, the oracle's rows and
// the principal table, never the list), and reachObligationProblems requires the
// listed names to be exactly the ones whose evidence is open: an entry cannot be
// removed to unblock the flip while the thing it names is still true, and an entry
// whose evidence has gone is stale. Discharging an obligation therefore means
// changing the evidence (a replay test that makes the recorded cells compared, a
// registered-path test, a recorded allow cell) and deleting the entry in the
// same change, where a reviewer sees both.

// reachFlipObligation is one open obligation.
type reachFlipObligation struct {
	name string // F4-07-..., the name seams.d/F4-07.md and the todo fragment give it
	// discharge says where the obligation is stated and what discharges it.
	discharge string
}

// reachFlipObligations are the obligations that must be gone before the flip.
var reachFlipObligations = []reachFlipObligation{
	{"F4-07-ABS-REPLAY", "seams.d/F4-07.md, Obligations on F4-08, ABS replay: replay R0's recording of the delegated ABS routes against the registered path after M3 moves keys and roles into core, and state F-1's outcome on that surface"},
	{"F4-07-HANDLER-REFUSALS", "seams.d/F4-07.md, Obligations on F4-08, handler refusals: prove the eight auth-off handler refusals on the registered path (port TestReachHeadHandlers_RefuseTheAuthDisabledPrincipal, or delete the seven credential routes)"},
	{"F4-07-HIDDEN-HANDLER-CHECKS", "seams.d/F4-07.md, Obligations on F4-08, hidden handler checks: prove the in-handler checks the probe's dummy-id 404 hides, on the registered path"},
	{"F4-07-OWNER-SET", "seams.d/F4-07.md, Obligations on F4-08, owner set: with internal/cfaccess configured and an owner set, assert on the registered path that a non-owner admin session is refused on every Guards.Owner route"},
	{"F4-07-OWNER-ALLOW", "seams.d/F4-07.md, Obligations on F4-08, owner allow: the Access-verified owner is admitted on every Guards.Owner route; R0's allow side is unrecorded (the recording boot has no Access verifier and no issuer), so it is asserted on the registered path"},
}

// reachObligationInput is what the evidence is read from.
type reachObligationInput struct {
	vd       *reachR0Doc
	res      reachDiffResult
	refusals int               // the oracle's HEAD handler-refusal rows
	assured  func(string) bool // who the oracle's owner model treats as the verified owner
}

// reachObligationEvidence maps every obligation to why it is open, or "" when the
// evidence for it has gone.
func reachObligationEvidence(in reachObligationInput) map[string]string {
	out := map[string]string{}

	absCells := 0
	for _, r := range in.vd.Routes {
		if r.ABSOnly {
			absCells += len(r.Verdict)
		}
	}
	if n := len(in.res.delegated); n > 0 || absCells > 0 {
		out["F4-07-ABS-REPLAY"] = fmt.Sprintf("%d delegated routes, %d recorded abs_only cells compared by nothing", n, absCells)
	} else {
		out["F4-07-ABS-REPLAY"] = ""
	}

	out["F4-07-HANDLER-REFUSALS"] = ""
	if in.refusals > 0 {
		out["F4-07-HANDLER-REFUSALS"] = fmt.Sprintf("%d HEAD handler-refusal rows hold a refusal only HEAD's handlers make", in.refusals)
	}

	out["F4-07-HIDDEN-HANDLER-CHECKS"] = ""
	if n := len(in.vd.UniformNotFound); n > 0 {
		out["F4-07-HIDDEN-HANDLER-CHECKS"] = fmt.Sprintf("%d routes answer every principal 404, so an in-handler check behind the dummy id is invisible", n)
	}

	out["F4-07-OWNER-SET"] = ""
	assured := 0
	if in.res.r2 != nil {
		for _, name := range in.res.r2.names {
			if in.assured(name) {
				assured++
			}
		}
	}
	if assured == 0 {
		out["F4-07-OWNER-SET"] = "the principal table holds no owner-assured principal, so no registered-path test of the owner set stands"
	}

	out["F4-07-OWNER-ALLOW"] = "verdicts.json owner_set has no allowed cell: R0's owner-allow side is unrecorded"
	if in.vd.OwnerSet != nil {
		for _, r := range in.vd.OwnerSet.Routes {
			if strings.Contains(r.Verdict, "A") {
				out["F4-07-OWNER-ALLOW"] = ""
			}
		}
	}
	return out
}

// reachObligationProblems is what the list fails on, as text: the listed names
// must be exactly the obligations whose evidence is open, each must say where it is
// discharged, and seams.d/F4-07.md (which outlives the todo fragment, which the
// daily assembler folds into TODO.md and deletes) must name it.
func reachObligationProblems(in reachObligationInput, listed []reachFlipObligation, seams []byte) []string {
	var out []string
	evidence := reachObligationEvidence(in)
	names := map[string]bool{}
	for _, o := range listed {
		if names[o.name] {
			out = append(out, o.name+" is listed twice")
		}
		names[o.name] = true
		why, known := evidence[o.name]
		switch {
		case !known:
			out = append(out, o.name+" is listed and the oracle has no evidence for it: name an obligation the oracle can see")
		case why == "":
			out = append(out, o.name+" is listed and its evidence has gone: delete the entry (the flip waits only on obligations that are open)")
		}
		if strings.TrimSpace(o.discharge) == "" {
			out = append(out, o.name+" names no discharge point")
		}
		if !strings.Contains(string(seams), o.name) {
			out = append(out, o.name+" is not named in seams.d/F4-07.md: the obligation must be stated where F4-08 reads it")
		}
	}
	for name, why := range evidence {
		if why != "" && !names[name] {
			out = append(out, fmt.Sprintf("%s is open (%s) and missing from reachFlipObligations: the flip must stay blocked until it is discharged", name, why))
		}
	}
	slices.Sort(out)
	return out
}

const reachSeamsSource = "../../../docs/agent-tasks/platform-core/seams.d/F4-07.md"

func reachObligationInputOf(t testing.TB) reachObligationInput {
	t.Helper()
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _, _ := reachRun(t, pd, vd, atoms, atoms)
	return reachObligationInput{vd: vd, res: res, refusals: len(reachHandlerRefusals()), assured: reachNoOwnerAssured}
}

func reachObligationProblemsNow(t testing.TB) []string {
	t.Helper()
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	return reachObligationProblems(reachObligationInputOf(t), reachFlipObligations, seams)
}

func TestReachFlipObligations_ListedNamesAreExactlyTheOpenOnes(t *testing.T) {
	for _, p := range reachObligationProblemsNow(t) {
		t.Error(p)
	}
	if len(reachFlipObligations) == 0 {
		t.Error("reachFlipObligations is empty while the recording still shows open obligations")
	}
}

// Each obligation must be load-bearing: removing it from the list fails the
// oracle by name, so the list cannot be emptied to unblock the flip.
func TestReachFlipObligations_RemovingAnEntryFailsTheOracle(t *testing.T) {
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	in := reachObligationInputOf(t)
	for i, o := range reachFlipObligations {
		without := slices.Delete(slices.Clone(reachFlipObligations), i, i+1)
		got := reachObligationProblems(in, without, seams)
		if len(got) != 1 || !strings.Contains(got[0], o.name) || !strings.Contains(got[0], "missing from reachFlipObligations") {
			t.Errorf("removing %s: %q", o.name, got)
		}
	}
	if got := reachObligationProblems(in, nil, seams); len(got) != len(reachFlipObligations) {
		t.Errorf("an empty list: %d problems, want %d: %q", len(got), len(reachFlipObligations), got)
	}
}

// And the other way: an entry whose evidence is gone, an unknown name, a name the
// seams do not state and an entry with no discharge point are each refused.
func TestReachFlipObligations_AStaleOrUnstatedEntryFails(t *testing.T) {
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	in := reachObligationInputOf(t)

	closed := in
	closed.refusals = 0
	if got := reachObligationProblems(closed, reachFlipObligations, seams); len(got) != 1 || !strings.Contains(got[0], "F4-07-HANDLER-REFUSALS") || !strings.Contains(got[0], "evidence has gone") {
		t.Errorf("no handler-refusal rows left: %q", got)
	}

	vd := *in.vd
	vd.UniformNotFound = nil
	vd.Routes = slices.Clone(vd.Routes)
	for i := range vd.Routes {
		vd.Routes[i].ABSOnly = false
	}
	closed = in
	closed.vd = &vd
	closed.res.delegated = nil
	got := reachObligationProblems(closed, reachFlipObligations, seams)
	if len(got) != 2 {
		t.Errorf("no abs_only cells, delegated routes or uniform-404 routes left: want the ABS-REPLAY and HIDDEN-HANDLER-CHECKS entries stale, got %q", got)
	}

	// an owner-assured principal in the table closes OWNER-SET; a recorded allow closes OWNER-ALLOW
	closed = in
	closed.assured = func(n string) bool { return n == "session:admin" }
	if got := reachObligationProblems(closed, reachFlipObligations, seams); len(got) != 1 || !strings.Contains(got[0], "F4-07-OWNER-SET") {
		t.Errorf("an owner-assured principal: %q", got)
	}
	owner := *in.vd.OwnerSet
	owner.Routes = slices.Clone(owner.Routes)
	owner.Routes[0].Verdict = "A" + owner.Routes[0].Verdict[1:]
	vd = *in.vd
	vd.OwnerSet = &owner
	closed = in
	closed.vd = &vd
	if got := reachObligationProblems(closed, reachFlipObligations, seams); len(got) != 1 || !strings.Contains(got[0], "F4-07-OWNER-ALLOW") {
		t.Errorf("a recorded owner allow: %q", got)
	}

	if got := reachObligationProblems(in, append(slices.Clone(reachFlipObligations), reachFlipObligation{"F4-07-BATCAVE", "x"}), seams); len(got) < 1 {
		t.Error("an unknown obligation was accepted")
	}
	if got := reachObligationProblems(in, reachFlipObligations, []byte("the seams state nothing")); len(got) != len(reachFlipObligations) {
		t.Errorf("seams that name no obligation: %d problems, want %d", len(got), len(reachFlipObligations))
	}
	bare := slices.Clone(reachFlipObligations)
	bare[0].discharge = " "
	if got := reachObligationProblems(in, bare, seams); len(got) != 1 || !strings.Contains(got[0], "no discharge point") {
		t.Errorf("an entry with no discharge point: %q", got)
	}
}
