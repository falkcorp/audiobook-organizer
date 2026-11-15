// file: internal/books/cutover/reach_owner_test.go
// version: 1.0.0
// guid: 5c0e8b71-3f2a-4d96-8a14-b7e69d1c2f08
// last-edited: 2026-11-15

package cutover

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The owner-set half of the reach oracle (round-2 MINOR-8).
//
// The main recording boots R0 with no owner_email, so R0's owner gate
// (ownerGateWhenOwnerSet, owner_routes.go @v0.223.0) was never on in it, and the
// oracle's model of Guards.Owner is 05-authn 4.8's fallback (Interactive plus
// core:admin). Production sets owner_email. verdicts.json's owner_set is R0
// recorded again on the owner routes with owner_email set and no Cloudflare
// Access verifier: no principal of the table is the owner, so R0's answer is the
// gate's refusal for every principal.
//
// The model of an owner set with the Access verifier internal/cfaccess ships: a
// principal passes a Guards.Owner route only if it is the verified owner, and the
// table holds none, so every principal is refused. reachOwnerSetVerdict is that
// model; the test compares it with the recording, route by route and principal
// by principal. It is a separate comparison with no expected differences, so the
// 217-cell golden list is untouched.

// reachOwnerRecorded are the nine routes the recorder boots again with an owner
// set (OWNER_ROUTES in scripts/r2corpus/record_r0_reach.py).
var reachOwnerRecorded = []string{
	"GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke",
	"POST /api/v1/backup/restore",
	"POST /api/v1/system/factory-reset",
	"POST /api/v1/system/reset",
	"POST /api/v1/users/:id/merge-into/:target/apply",
	"POST /api/v1/users/:id/merge-into/:target/preview",
	"POST /api/v1/users/merges/:jid/resume",
	"POST /api/v1/users/merges/:jid/undo",
}

// The 9 to 8 mapping. R2's manifest has eight Guards.Owner routes: seven of the
// nine recorded ones, plus the reissue route, which is newer than R0 (an R2-only
// route, so it has no recording). The two recorded routes R2's manifest does not
// carry are R0-only routes (the reset pair, which core replaces).
var (
	reachOwnerR0Only = []string{
		"POST /api/v1/system/factory-reset",
		"POST /api/v1/system/reset",
	}
	reachOwnerR2Only = []string{
		"POST /api/v1/admin/guest-service-tokens/:userId/reissue",
	}
)

// reachOwnerSetVerdict is the model: one letter per principal for a route that
// carries Guards.Owner, under an owner set with the Access verifier and a table
// that holds no owner-assured principal. The owner-assured predicate is a
// parameter so a test can show the comparison tells models apart.
func reachOwnerSetVerdict(whos []reachWho, ownerAssured func(reachWho) bool) string {
	var b strings.Builder
	for _, w := range whos {
		if ownerAssured(w) {
			b.WriteByte('A')
		} else {
			b.WriteByte('D')
		}
	}
	return b.String()
}

// reachNoOwnerAssured is the table: none of 05-rbac section 12's principals is
// the Access-verified owner.
func reachNoOwnerAssured(reachWho) bool { return false }

// reachOwnerSetProblems compares the owner-set recording with the model and with
// the manifest. whos is the table's principals in the recording's order; owners
// are the manifest routes that carry Guards.Owner and are not delegated.
func reachOwnerSetProblems(vd *reachR0Doc, whos []reachWho, owners []string, assured func(reachWho) bool) []string {
	var p []string
	if vd.OwnerSet == nil {
		return []string{"verdicts.json has no owner_set recording"}
	}
	if vd.OwnerSet.OwnerEmail == "" {
		p = append(p, "owner_set names no owner_email: R0's owner gate was not on in it")
	}
	var got []string
	for _, r := range vd.OwnerSet.Routes {
		got = append(got, r.key())
	}
	if !slices.Equal(got, reachOwnerRecorded) {
		p = append(p, "owner_set routes are "+strings.Join(got, ", ")+", want "+strings.Join(reachOwnerRecorded, ", "))
	}

	// The 9 to 8 mapping: recorded less R0-only plus R2-only is the manifest's
	// Guards.Owner set.
	want := slices.Clone(reachOwnerRecorded)
	want = slices.DeleteFunc(want, func(k string) bool { return slices.Contains(reachOwnerR0Only, k) })
	want = append(want, reachOwnerR2Only...)
	slices.Sort(want)
	have := slices.Clone(owners)
	slices.Sort(have)
	if !slices.Equal(have, want) {
		p = append(p, "the manifest's Guards.Owner routes are "+strings.Join(have, ", ")+", the recording (less the R0-only, plus the R2-only) gives "+strings.Join(want, ", "))
	}
	if len(owners) != 8 {
		p = append(p, "the manifest has a different number of Guards.Owner routes than the eight the mapping names")
	}

	for _, r := range vd.OwnerSet.Routes {
		if len(r.Verdict) != len(whos) {
			p = append(p, r.key()+": "+"verdict has "+strconv.Itoa(len(r.Verdict))+" letters for "+strconv.Itoa(len(whos))+" principals")
			continue
		}
		if m := reachOwnerSetVerdict(whos, assured); r.Verdict != m {
			for i := range m {
				if r.Verdict[i] != m[i] {
					p = append(p, r.key()+" "+whos[i].name+": R0 "+string(r.Verdict[i])+", owner-set model "+string(m[i]))
				}
			}
		}
	}
	return p
}

func reachManifestOwnerRoutes() []string {
	var out []string
	for _, lr := range reachManifest() {
		if lr.Guards.Owner && !lr.OwnAuth {
			out = append(out, lr.Method+" "+lr.GinPath)
		}
	}
	return out
}

func TestReachOwnerSet_R0RefusesEveryTablePrincipalAsTheOwnerSetModelDoes(t *testing.T) {
	pd, vd := reachLoad(t)
	if vd.OwnerSet == nil {
		t.Skip("verdicts.json has no owner_set recording; TestReachFlip_FailsAtTheFlipWhileThePinOrTheOwnerlessModelStands holds the flip until it does")
	}
	whos := make([]reachWho, len(pd.Principals))
	for i, pr := range pd.Principals {
		whos[i].name = pr.Name
	}
	if p := reachOwnerSetProblems(vd, whos, reachManifestOwnerRoutes(), reachNoOwnerAssured); len(p) > 0 {
		t.Fatalf("the owner-set recording and the model disagree:\n%s", strings.Join(p, "\n"))
	}
}

// The comparison must tell models apart: a model that lets any principal pass an
// Owner route (here, the table's every principal) differs from the recording at
// every cell, and a recording with a different route list, owner or letter count
// is refused. Run on an in-memory recording, so it holds before and after a
// recording is committed.
func TestReachOwnerSet_ComparisonFailsOnADifferentModelOrRecording(t *testing.T) {
	whos := []reachWho{{name: "session:admin"}, {name: "key:admin:every"}, {name: "anonymous"}}
	mk := func() *reachR0Doc {
		o := &reachR0OwnerSet{What: "w", OwnerEmail: "bruce@example.invalid"}
		for _, k := range reachOwnerRecorded {
			m, path, _ := strings.Cut(k, " ")
			o.Routes = append(o.Routes, reachR0Route{Method: m, Path: path, Verdict: "DDD"})
		}
		return &reachR0Doc{OwnerSet: o}
	}
	owners := func() []string {
		var o []string
		for _, k := range reachOwnerRecorded {
			if !slices.Contains(reachOwnerR0Only, k) {
				o = append(o, k)
			}
		}
		return append(o, reachOwnerR2Only...)
	}

	if p := reachOwnerSetProblems(mk(), whos, owners(), reachNoOwnerAssured); len(p) != 0 {
		t.Fatalf("a recording that matches the model has problems: %v", p)
	}
	if p := reachOwnerSetProblems(mk(), whos, owners(), func(w reachWho) bool { return w.name == "session:admin" }); len(p) != len(reachOwnerRecorded) {
		t.Errorf("a model that admits the admin session differs from R0 on %d cells, want %d: %v", len(p), len(reachOwnerRecorded), p)
	}
	if p := reachOwnerSetProblems(&reachR0Doc{}, whos, owners(), reachNoOwnerAssured); len(p) == 0 {
		t.Error("no owner_set recording must be a problem")
	}
	d := mk()
	d.OwnerSet.OwnerEmail = ""
	if p := reachOwnerSetProblems(d, whos, owners(), reachNoOwnerAssured); len(p) == 0 {
		t.Error("a recording with no owner_email must be a problem")
	}
	d = mk()
	d.OwnerSet.Routes = d.OwnerSet.Routes[1:]
	if p := reachOwnerSetProblems(d, whos, owners(), reachNoOwnerAssured); len(p) == 0 {
		t.Error("a recording that lost a route must be a problem")
	}
	d = mk()
	d.OwnerSet.Routes[0].Verdict = "DD"
	if p := reachOwnerSetProblems(d, whos, owners(), reachNoOwnerAssured); len(p) == 0 {
		t.Error("a verdict with the wrong number of letters must be a problem")
	}
	if p := reachOwnerSetProblems(mk(), whos, owners()[1:], reachNoOwnerAssured); len(p) == 0 {
		t.Error("a manifest that dropped an Owner route must break the 9 to 8 mapping")
	}
	d = mk()
	d.OwnerSet.Routes[2].Verdict = "DAD"
	if p := reachOwnerSetProblems(d, whos, owners(), reachNoOwnerAssured); len(p) != 1 {
		t.Errorf("one admitted principal on one route must be exactly one problem, got %v", p)
	}
}

// The manifest side of the 9 to 8 mapping needs no recording: the manifest's
// Guards.Owner routes are the recorded ones less the R0-only pair, plus the R2-only
// reissue route.
func TestReachOwnerSet_TheManifestsOwnerRoutesAreTheRecordedOnesLessR0OnlyPlusR2Only(t *testing.T) {
	want := slices.DeleteFunc(slices.Clone(reachOwnerRecorded), func(k string) bool { return slices.Contains(reachOwnerR0Only, k) })
	want = append(want, reachOwnerR2Only...)
	have := reachManifestOwnerRoutes()
	slices.Sort(want)
	slices.Sort(have)
	if !slices.Equal(have, want) {
		t.Fatalf("manifest Guards.Owner routes:\n got %v\nwant %v", have, want)
	}
}
