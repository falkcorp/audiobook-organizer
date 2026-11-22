// file: internal/books/cutover/reach_owner_test.go
// version: 1.4.1
// guid: 5c0e8b71-3f2a-4d96-8a14-b7e69d1c2f08
// last-edited: 2026-11-18

package cutover

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The owner-set half of the reach oracle (round-2 MINOR-8).
//
// The main recording boots R0 with no owner_email, so R0's owner gate
// (ownerGateWhenOwnerSet, owner_routes.go @f6698f9bf) was never on in it, and the
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
// golden list is untouched. A missing recording fails it (the recording is
// committed; the flip guard in reach_pin_source_test.go holds the same condition
// at the mount switch).

// reachOwnerRecorded are the eight routes the recorder boots again with an owner
// set (OWNER_ROUTES in scripts/r2corpus/record_r0_reach.py), in its order. They
// are exactly R2's manifest Guards.Owner routes: R0 is main at the pinned commit
// (F4-08), which already serves the reissue route and no longer serves the reset
// pair, so every owner route has a recording of its own and no route stands in
// for another. Until 2026-11-22 R0 was the v0.223.0 release, whose nine owner
// routes needed a 9-to-8 mapping (the reset pair R0-only, reissue R2-only and
// judged through revoke as its declared twin); the re-recording discharged it.
var reachOwnerRecorded = []string{
	"GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/v1/admin/guest-service-tokens/:userId/reissue",
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke",
	"POST /api/v1/backup/restore",
	"POST /api/v1/users/:id/merge-into/:target/apply",
	"POST /api/v1/users/:id/merge-into/:target/preview",
	"POST /api/v1/users/merges/:jid/resume",
	"POST /api/v1/users/merges/:jid/undo",
}

// reachOwnerAssured says whether a principal is the Access-verified owner. The
// table of 05-rbac section 12 holds none, so it is false for every name. It is
// the ONE predicate: the owner-set comparison judges the recording with it
// (reachChecks) and the flip obligations read it (reachObligationInputFrom), so
// changing it at one site changes it at both. The functions that take it as a
// parameter do so only so a test can show the comparison tells models apart. The
// round-4 C2 swapped the obligations' copy for always-true while the model kept
// the real one, and the oracle stayed green.
func reachOwnerAssured(string) bool { return false }

// reachOwnerSetVerdict is the model of a route under an owner set with the Access
// verifier internal/cfaccess ships. It READS R2: r2 is the evaluation of the
// manifest the oracle was run on (possibly a mutated copy), so a manifest change
// moves it.
//
//   - a route whose manifest entry carries Guards.Owner: a principal that is not
//     owner-assured is refused (D); an owner-assured one gets R2's own letter for the
//     route (the guard admits it, the requirement still applies);
//   - a route without Guards.Owner: R2's letter for every principal. R0 gates it
//     with ownerGateWhenOwnerSet and the manifest no longer does, which is the
//     difference the comparison with the recording exists to show.
//
// ok is false when R2 judged no such route (it is not in the manifest, or it is
// delegated), which the caller must account for: it is never a silent skip.
func reachOwnerSetVerdict(key string, r2 *reachR2, assured func(string) bool) (string, bool) {
	base, ok := r2.verdict[key]
	if !ok {
		return "", false
	}
	if !r2.ownerGuard[key] {
		return base, true
	}
	b := []byte(base)
	for i := range b {
		if !assured(r2.names[i]) {
			b[i] = 'D'
		}
	}
	return string(b), true
}

// reachOwnerGateText is what R0's owner gate says to a caller that is not the
// verified owner (auth.OwnerProofWhyNot @f6698f9bf; record_r0_reach.py OWNER_REFUSAL_TEXT).
const reachOwnerGateText = "sign in through Cloudflare Access"

// reachOwnerSetProblems compares the owner-set recording with R2's model of it,
// route by route and principal by principal: the manifest's Guards.Owner routes,
// read from the evaluation so a mutated manifest is seen, must be the recorded
// ones, and each recorded route goes through reachOwnerSetVerdict. An owner route
// the recording lacks, or a recorded one the manifest no longer guards, fails the
// set comparison; a guard dropped on a recorded route also fails through the model.
//
// Beside that, a recorded-refusal check: on every recorded owner route at least
// one principal is allowed in the main recording and refused here. It proves one
// thing, that the gate refused in the recording (the boot had an owner), not that
// the model is right.
func reachOwnerSetProblems(vd *reachR0Doc, r2 *reachR2, assured func(string) bool) []string {
	var p []string
	if r2 == nil {
		return []string{"the oracle run carries no R2 evaluation, so the owner-set model has nothing to read"}
	}
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

	// The recorded routes are the manifest's Guards.Owner set, read from the
	// evaluation so a mutated manifest is seen.
	var have []string
	for k, owner := range r2.ownerGuard {
		if owner {
			have = append(have, k)
		}
	}
	want := slices.Sorted(slices.Values(reachOwnerRecorded))
	slices.Sort(have)
	if !slices.Equal(have, want) {
		p = append(p, "the manifest's Guards.Owner routes are "+strings.Join(have, ", ")+", the recording's are "+strings.Join(want, ", "))
	}
	if len(have) != len(reachOwnerRecorded) {
		p = append(p, "the manifest has "+strconv.Itoa(len(have))+" Guards.Owner routes, the recording "+strconv.Itoa(len(reachOwnerRecorded)))
	}

	main := map[string]string{}
	for _, r := range vd.Routes {
		main[r.key()] = r.Verdict
	}
	for _, r := range vd.OwnerSet.Routes {
		k := r.key()
		if r.AdminRefusal == "" || !strings.Contains(r.AdminRefusal, reachOwnerGateText) {
			p = append(p, k+": the admin session's recorded refusal is "+strconv.Quote(r.AdminRefusal)+", not the owner gate's ("+reachOwnerGateText+")")
		}
		if len(r.Verdict) != len(r2.names) {
			p = append(p, k+": verdict has "+strconv.Itoa(len(r.Verdict))+" letters for "+strconv.Itoa(len(r2.names))+" principals")
			continue
		}
		moved := false
		if m, ok := main[k]; ok && len(m) == len(r.Verdict) {
			for i := range m {
				moved = moved || (m[i] == 'A' && r.Verdict[i] == 'D')
			}
		}
		if !moved {
			p = append(p, k+": no principal is allowed in the main recording and refused in the owner set: the owner gate shows no effect")
		}
		m, ok := reachOwnerSetVerdict(k, r2, assured)
		if !ok {
			p = append(p, k+": recorded owner route has no R2 verdict (dropped from the manifest or delegated)")
			continue
		}
		for i := range m {
			if r.Verdict[i] != m[i] {
				p = append(p, k+" "+r2.names[i]+": R0 "+string(r.Verdict[i])+", owner-set model "+string(m[i]))
			}
		}
	}
	if o := vd.OwnerSet.Canary; o == nil || !maps.Equal(o, vd.Canary) {
		p = append(p, "owner_set canary is not the main recording's canary: the owner boots' principals did not behave as the main boots' did")
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
		t.Fatal("verdicts.json has no owner_set recording: the owner-gated routes were recorded without an owner (record_r0_reach.py)")
	}
	atoms := idAtoms()
	_, _, r2 := reachRun(t, pd, vd, atoms, atoms)
	if p := reachOwnerSetProblems(vd, r2, reachOwnerAssured); len(p) > 0 {
		t.Fatalf("the owner-set recording and the model disagree:\n%s", strings.Join(p, "\n"))
	}
}

// reachOwnerFixture is an in-memory recording and evaluation for three
// principals: main allows the admin session on every owner route, the owner set
// refuses all three, and R2 judges each manifest owner route Guards.Owner with
// the admin session allowed by its requirement.
func reachOwnerFixture() (*reachR0Doc, *reachR2) {
	names := []string{"session:admin", "key:admin:every", "anonymous"}
	o := &reachR0OwnerSet{What: "w", OwnerEmail: "bruce@example.invalid", Canary: map[string]int{"session:admin": 200}}
	vd := &reachR0Doc{OwnerSet: o, Canary: map[string]int{"session:admin": 200}}
	r2 := &reachR2{verdict: map[string]string{}, ownerGuard: map[string]bool{}, names: names}
	for _, k := range reachOwnerRecorded {
		m, path, _ := strings.Cut(k, " ")
		vd.Routes = append(vd.Routes, reachR0Route{Method: m, Path: path, Verdict: "ADD"})
		o.Routes = append(o.Routes, reachR0Route{Method: m, Path: path, Verdict: "DDD", AdminRefusal: "Owner actions need you to " + reachOwnerGateText})
		r2.verdict[k] = "ADD"
		r2.ownerGuard[k] = true
	}
	return vd, r2
}

// The model reads R2: an Owner guard turns a non-owner into D, an owner-assured
// principal gets R2's own letter, and a route with no Owner guard is R2's letter
// for everyone.
func TestReachOwnerSet_ModelReadsR2(t *testing.T) {
	_, r2 := reachOwnerFixture()
	const k = "POST /api/v1/backup/restore"
	if got, ok := reachOwnerSetVerdict(k, r2, reachOwnerAssured); !ok || got != "DDD" {
		t.Errorf("an Owner guard and no owner-assured principal: %q %v, want DDD", got, ok)
	}
	if got, _ := reachOwnerSetVerdict(k, r2, func(n string) bool { return n == "session:admin" }); got != "ADD" {
		t.Errorf("an owner-assured admin keeps R2's letter: %q, want ADD", got)
	}
	r2.ownerGuard[k] = false
	if got, _ := reachOwnerSetVerdict(k, r2, reachOwnerAssured); got != "ADD" {
		t.Errorf("a route without the Owner guard is R2's letters: %q, want ADD", got)
	}
	if _, ok := reachOwnerSetVerdict("GET /api/v1/batcave/gadgets", r2, reachOwnerAssured); ok {
		t.Error("a route R2 did not judge has a model letter")
	}
}

// The comparison must tell models and recordings apart, each way. Run on an
// in-memory recording, so it holds whatever is committed.
func TestReachOwnerSet_ComparisonFailsOnADifferentModelOrRecording(t *testing.T) {
	vd, r2 := reachOwnerFixture()
	if p := reachOwnerSetProblems(vd, r2, reachOwnerAssured); len(p) != 0 {
		t.Fatalf("a recording that matches the model has problems: %v", p)
	}
	const restore = "POST /api/v1/backup/restore"
	// the cell index of the route in the owner set
	idx := func(d *reachR0Doc, key string) int {
		for i, r := range d.OwnerSet.Routes {
			if r.key() == key {
				return i
			}
		}
		t.Fatalf("no %s", key)
		return -1
	}
	cases := []struct {
		name string
		mut  func(*reachR0Doc, *reachR2)
		min  int // at least this many problems
		eq   int // exactly this many, when > 0
		any  string
	}{
		{"a model that admits the admin session differs on every recorded route", nil, 0, 0, ""},
		{"no owner_set", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet = nil }, 1, 1, "no owner_set"},
		{"no owner_email", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.OwnerEmail = "" }, 1, 0, "owner_email"},
		{"a route lost", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Routes = d.OwnerSet.Routes[1:] }, 1, 0, "owner_set routes are"},
		{"a verdict with the wrong number of letters", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Routes[idx(d, restore)].Verdict = "DD" }, 1, 0, "letters"},
		{"one admitted cell", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Routes[idx(d, restore)].Verdict = "DAD" }, 1, 1, "key:admin:every"},
		{"the reviewer's mutant: the admin session's restore cell set to A", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Routes[idx(d, restore)].Verdict = "ADD" }, 2, 0, "session:admin"},
		{"the Owner guard dropped on a recorded route fails the set and the model", func(_ *reachR0Doc, r *reachR2) { r.ownerGuard[restore] = false }, 3, 0, "owner-set model A"},
		{"a new owner route the recording lacks", func(_ *reachR0Doc, r *reachR2) {
			r.verdict["POST /api/v1/batcave/alarm"] = "ADD"
			r.ownerGuard["POST /api/v1/batcave/alarm"] = true
		}, 2, 0, "POST /api/v1/batcave/alarm"},
		{"a recorded route dropped from the manifest", func(_ *reachR0Doc, r *reachR2) { delete(r.verdict, restore); delete(r.ownerGuard, restore) }, 2, 0, "no R2 verdict"},
		{"the admin refusal is not the gate's text", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Routes[0].AdminRefusal = "missing permission" }, 1, 1, "not the owner gate's"},
		{"no principal moves from the main recording to the owner set", func(d *reachR0Doc, _ *reachR2) { d.Routes[0].Verdict = "DDD" }, 1, 1, "shows no effect"},
		{"the owner boots' canary differs", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Canary["session:admin"] = 403 }, 1, 1, "canary"},
		{"the owner boots recorded no canary", func(d *reachR0Doc, _ *reachR2) { d.OwnerSet.Canary = nil }, 1, 1, "canary"},
	}
	for _, tc := range cases {
		if tc.mut == nil {
			p := reachOwnerSetProblems(vd, r2, func(n string) bool { return n == "session:admin" })
			// one line per recorded route
			if want := len(reachOwnerRecorded); len(p) != want {
				t.Errorf("%s: %d problems, want %d: %v", tc.name, len(p), want, p)
			}
			continue
		}
		d, r := reachOwnerFixture()
		tc.mut(d, r)
		p := reachOwnerSetProblems(d, r, reachOwnerAssured)
		joined := strings.Join(p, "\n")
		if len(p) < tc.min || (tc.eq > 0 && len(p) != tc.eq) || !strings.Contains(joined, tc.any) {
			t.Errorf("%s: %d problem(s) %v, want >= %d (exactly %d when set), naming %q", tc.name, len(p), p, tc.min, tc.eq, tc.any)
		}
	}
}

// The reviewer's mutant on the real data: setting one owner-set cell to A fails
// the ORACLE ITSELF, not only the owner-set tests (round-3 MINOR-7 residue).
func TestReachOwnerSet_OneAllowedCellFailsTheOracleItself(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, whos, _ := reachRun(t, pd, vd, atoms, atoms)
	rows := reachOracleRows()
	if p := reachChecks(vd, res, rows); len(p) != 0 {
		t.Fatalf("the unchanged recording already fails the oracle: %v", p)
	}
	admin := -1
	for i, w := range whos {
		if w.name == "session:admin" {
			admin = i
		}
	}
	mutated := *vd
	o := *vd.OwnerSet
	o.Routes = slices.Clone(o.Routes)
	for i, r := range o.Routes {
		if r.key() != "POST /api/v1/backup/restore" {
			continue
		}
		v := []byte(r.Verdict)
		v[admin] = 'A'
		o.Routes[i].Verdict = string(v)
	}
	mutated.OwnerSet = &o
	if p := reachChecks(&mutated, res, rows); len(p) == 0 {
		t.Fatal("the restore / session:admin owner-set cell was set to A and reachChecks passed")
	}
}

// The manifest side needs no recording: the manifest's Guards.Owner routes are
// the recorded ones.
func TestReachOwnerSet_TheManifestsOwnerRoutesAreTheRecordedOnes(t *testing.T) {
	want := slices.Clone(reachOwnerRecorded)
	have := reachManifestOwnerRoutes()
	slices.Sort(want)
	slices.Sort(have)
	if !slices.Equal(have, want) {
		t.Fatalf("manifest Guards.Owner routes:\n got %v\nwant %v", have, want)
	}
}
