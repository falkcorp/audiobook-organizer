// file: internal/books/cutover/reach_tables_test.go
// version: 0.1.0
// guid: 3b7e2f9a-6d14-4c85-a0e3-91f5c8b2d746
// last-edited: 2026-11-22

package cutover

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The data tables of the reach oracle that outlive the hand evaluator. The
// registered-path oracle (registered_reach_eval_test.go) replaced the evaluator;
// these tables are what the flip obligations (reach_flip_obligations_test.go),
// the handler-admin pin source check and the B10 cells still read. Each is moved
// here UNCHANGED from the file the evaluator's removal deletes (F4-08 B10f):
// reach_owner_test.go, reach_pin_test.go, reach_oracle_test.go, reach_gate_test.go
// and reach_mutation_test.go.

// reachOwnerRecorded are the eight routes the recorder boots again with an owner
// set (OWNER_ROUTES in scripts/r2corpus/record_r0_reach.py), in its order. They
// are exactly the books manifest's Guards.Owner routes: R0 is main at the pinned
// commit (F4-08), which already serves the reissue route and no longer serves the
// reset pair, so every books owner route has a recording of its own and no route
// stands in for another. Until 2026-11-22 R0 was the v0.223.0 release, whose nine
// owner routes needed a 9-to-8 mapping (the reset pair R0-only, reissue R2-only
// and judged through revoke as its declared twin); the re-recording discharged it.
// Core's own owner route (reachCoreOwnerRoutes) is not a books route and has no
// recording: registered_owner_test.go pins its letters as a deliberate addition.
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

// reachOwnerGateText is what R0's owner gate says to a caller that is not the
// verified owner (auth.OwnerProofWhyNot @v0.223.0; record_r0_reach.py OWNER_REFUSAL_TEXT).
const reachOwnerGateText = "sign in through Cloudflare Access"

// reachPinnedCells are the cells the reachHandlerAdmin pin removes from the
// diff: each is a principal the route-level gate (users.manage) lets in and R0's
// handler refuses, because the handler tests the admin role by name and that a
// key may act as admin. They are the F4-08 obligation spelled out in
// seams.d/F4-07.md: until the registered path proves the migrated handlers
// (the merge actor check, SetUserEmail), these 11 cells are asserted by the
// pin and by nothing that runs the handler.
var reachPinnedCells = []string{
	"GET /api/v1/users/:id/footprint / authoff: R0 D, R2 A",
	"GET /api/v1/users/:id/footprint / key:admin:users: R0 D, R2 A",
	"GET /api/v1/users/:id/footprint / key:custom-all:empty: R0 D, R2 A",
	"GET /api/v1/users/:id/footprint / key:custom-all:every: R0 D, R2 A",
	"GET /api/v1/users/:id/footprint / session:custom-all: R0 D, R2 A",
	"GET /api/v1/users/merges / authoff: R0 D, R2 A",
	"GET /api/v1/users/merges / key:admin:users: R0 D, R2 A",
	"GET /api/v1/users/merges / key:custom-all:empty: R0 D, R2 A",
	"GET /api/v1/users/merges / key:custom-all:every: R0 D, R2 A",
	"GET /api/v1/users/merges / session:custom-all: R0 D, R2 A",
	"PUT /api/v1/users/:id/email / session:custom-all: R0 D, R2 A",
}

// reachPinSitProblems is what the pin-sits-on-users.manage check fails on: the
// pin refuses a caller without core:admin on top of the route's own requirement;
// it does not replace it. A pinned route that stops asking for users.manage at
// the router would still pass the gate, because every cell the pin leaves open is
// one R0 also leaves open, so the requirement is asserted here.
func reachPinSitProblems() []string {
	var out []string
	seen := 0
	for _, lr := range reachManifest() {
		key := lr.Method + " " + lr.GinPath
		if _, pinned := reachHandlerAdmin[key]; !pinned {
			continue
		}
		seen++
		if lr.Perm.Kind() != rbac.KindLegacyAtom || lr.Perm.Atom() != "users.manage" {
			out = append(out, fmt.Sprintf("%s is pinned as a handler-level core:admin check, but its route requirement is kind %v, not the users.manage atom", key, lr.Perm.Kind()))
		}
	}
	if seen != len(reachHandlerAdmin) {
		out = append(out, fmt.Sprintf("%d of %d pinned routes are in the manifest", seen, len(reachHandlerAdmin)))
	}
	return out
}

func TestReachHandlerAdminPin_SitsOnRoutesThatAskForUsersManage(t *testing.T) {
	for _, p := range reachPinSitProblems() {
		t.Error(p)
	}
}

// reachHandlerAdmin are the routes whose handler, not the router, refuses a
// caller that does not hold core:admin. At HEAD each tests the admin role by
// name and that a key may act as admin; 05-rbac 4.6 moves each to
// rbac.Can(ctx, core:admin) with key scoping, "never adminRole", and says so
// for these: the merge handlers' mergeActor, including the two reads that are not
// credential routes (GET /users/:id/footprint, GET /users/merges), and the
// role-name checks of handlers/user.go (SetUserEmail, whose route's users.manage
// "is not enough"). The oracle's route-level gate cannot see a handler, so the
// check is pinned here from the spec; it is a claim about code that F4-08 or
// the handler migration (04) writes, and unproven until then (seams.d/F4-07.md).
var reachHandlerAdmin = map[string]string{
	"GET /api/v1/users/:id/footprint": "05-rbac 4.6: mergeActor checks core:admin (user_merge.go:68-70, called at :101) @v0.223.0",
	"GET /api/v1/users/merges":        "05-rbac 4.6: mergeActor checks core:admin (user_merge.go:68-70, called at :194) @v0.223.0",
	"PUT /api/v1/users/:id/email":     "05-rbac 4.6: SetUserEmail checks core:admin in place of the admin role name (handlers/user.go:456-458) @v0.223.0",
}

// reachIdentityRoutes are the credential-metadata routes whose successors are
// core's native ones (05-authn 4.11, 4.13): the manifest keeps them as
// Authenticated until F4-08 deletes them, and core's replacement answers the
// auth-disabled principal 401 itself, which is the core-route half's assertion.
var reachIdentityRoutes = []string{
	"GET /api/v1/auth/me",
	"GET /api/v1/auth/sessions",
	"DELETE /api/v1/auth/sessions/:id",
	"GET /api/v1/auth/api-keys",
	"GET /api/v1/auth/api-keys/:id",
	"POST /api/v1/auth/api-keys",
	"DELETE /api/v1/auth/api-keys/:id",
}

// reachMaskedRoutes are the routes whose own requirement the oracle cannot
// see: an Owner guard (its fallback is Interactive plus core:admin, 05-authn 4.8)
// or the handler-admin pin refuses everyone but an admin session first, so
// widening or narrowing the requirement moves no cell (33 of the manifest
// mutants above). Each requirement is pinned here, from the spec, and the set
// is held to the manifest's Owner-guarded routes plus the pinned ones.
var reachMaskedRoutes = map[string]string{
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke":  "core:admin",
	"POST /api/v1/admin/guest-service-tokens/:userId/reissue": "core:admin",
	"GET /api/v1/admin/guest-service-tokens/plan":             "core:admin",
	"POST /api/v1/backup/restore":                             "legacy(settings.manage)",
	"PUT /api/v1/users/:id/email":                             "legacy(users.manage)",
	"GET /api/v1/users/:id/footprint":                         "legacy(users.manage)",
	"POST /api/v1/users/:id/merge-into/:target/apply":         "legacy(users.manage)",
	"POST /api/v1/users/:id/merge-into/:target/preview":       "legacy(users.manage)",
	"GET /api/v1/users/merges":                                "legacy(users.manage)",
	"POST /api/v1/users/merges/:jid/resume":                   "legacy(users.manage)",
	"POST /api/v1/users/merges/:jid/undo":                     "legacy(users.manage)",
}

// reachMaskedProblems is what the masked-route pin fails on, as text.
func reachMaskedProblems() []string { return reachMaskedProblemsOf(reachManifest()) }

// reachMaskedProblemsOf is reachMaskedProblems over the given manifest.
func reachMaskedProblemsOf(routes []httpapi.LegacyRoute) []string {
	var out []string
	got := map[string]string{}
	for _, lr := range routes {
		k := lr.Method + " " + lr.GinPath
		if _, pinned := reachHandlerAdmin[k]; pinned || lr.Guards.Owner {
			got[k] = lr.Perm.String()
		}
	}
	for k, want := range reachMaskedRoutes {
		if got[k] != want {
			out = append(out, fmt.Sprintf("%s: requirement %q, pinned %q (the oracle cannot see this change: an Owner guard or the handler pin dominates it)", k, got[k], want))
		}
	}
	for k := range got {
		if _, ok := reachMaskedRoutes[k]; !ok {
			out = append(out, k+" is Owner-guarded or pinned and its requirement is not pinned in reachMaskedRoutes")
		}
	}
	sort.Strings(out)
	return out
}

func TestReachOracle_RoutesAGuardDominatesKeepTheirRequirement(t *testing.T) {
	for _, p := range reachMaskedProblems() {
		t.Error(p)
	}
}

// reachHandlerRefusalCells are the eight auth-off cells the router admits and R0's
// handler refused, on routes that are not Public: the seven credential routes
// (reachIdentityRoutes, 401: the handler needs a user and auth off has none) and
// the approval of a run (403: ApproveRun needs a user to record). They were read
// from the removed evaluator's own diff, its auth-off cells that went R0 D to R2 A
// on a route that is not Public, and the registered path proves each again
// (registered_reach_eval_test.go's auth-off boot, B10e2's HandlerRefusals cells).
// Each is an F4-08 obligation (seams.d/F4-07.md, round-2 MINOR-3), the bar of
// F4-07-HANDLER-REFUSALS.
var reachHandlerRefusalCells = append(slices.Clone(reachIdentityRoutes), "POST /api/v3/ops/runs/:id/approve")

// reachWantR0Only and reachWantR2Only are the routes only one side has, each with
// the reason. A route that appears or disappears fails, never silently.
var (
	reachWantR0Only = []string{
		"GET /", // the SPA handler, outside the manifest (served by the embedded frontend)
	}
	// R0 is main at f6698f9bf (F4-08), which serves the reissue route (1619250eb)
	// and has dropped reconcile/preview (64fdc707e) and the reset pair (623da76d3),
	// so the flip's only route R0 lacks is its own.
	reachWantR2Only = []string{
		"POST /api/v1/auth/phone-key/claim", // F4-08 B10s: the guest's one-time phone-app key under core identity; newer than R0's commit
	}
)

// reachWantR2OnlyVerdicts pins what each R2-only route admits at stage 13, principal
// by principal (the letters in the order of the principal table: verdicts.json's
// 36 principals before the auth-disabled one). An R2-only route has no R0 recording,
// so no cell is compared and a change of its requirement or of its Interactive guard
// would move reach and fail nothing; the letters are the decision, and a mutant that
// moves one fails on the registered path (registered_reach_eval_test.go). The
// claim's letters are the credential routes' shape (the same as the ones before
// PUT /auth/me/password in verdicts.json): the seven human sessions but the
// fp-worker role admit (any signed-in user claims their own key), every API key,
// the anonymous caller and the auth-off principal are refused (a credRoute needs
// an interactive session that has a user). The reissue route is not listed: R0
// records it since F4-08, and its Owner guard masks its requirement, which
// reachMaskedRoutes pins.
var reachWantR2OnlyVerdicts = map[string]string{
	"POST /api/v1/auth/phone-key/claim": "AAADAAADDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
}

// reachOwnerTableProblems holds the owner-set tables to the manifest and to R0's
// owner_set recording, with no evaluator: what remains of the removed owner-set
// model once the registered path (B10e1r's cells) judges the owner routes itself.
//
//   - the recording boots R0 with owner_email set and no Access verifier, so its
//     owner_set names an owner and covers exactly reachOwnerRecorded;
//   - the manifest's Guards.Owner routes, the delegated ones left out, are the
//     recorded ones;
//   - no principal passes an owner route in the recording (the table holds no
//     owner: reachOwnerAssured is false for every name), the admin session's
//     refusal is the owner gate's, some principal the main recording allowed is
//     refused there (the gate was on);
//   - the owner boots' canary is the main recording's.
func reachOwnerTableProblems(vd *reachR0Doc, manifest []httpapi.LegacyRoute) []string {
	var p []string
	if vd.OwnerSet == nil {
		return []string{"verdicts.json has no owner_set recording: the owner-gated routes were recorded without an owner (record_r0_reach.py)"}
	}
	if vd.OwnerSet.OwnerEmail == "" {
		p = append(p, "owner_set names no owner_email: R0's owner gate was not on in it")
	}
	var got []string
	recorded := map[string]reachR0Route{}
	for _, r := range vd.OwnerSet.Routes {
		got = append(got, r.key())
		recorded[r.key()] = r
	}
	if !slices.Equal(got, reachOwnerRecorded) {
		p = append(p, "owner_set routes are "+strings.Join(got, ", ")+", want "+strings.Join(reachOwnerRecorded, ", "))
	}

	var have []string
	for _, lr := range manifest {
		if lr.Guards.Owner && !reachDelegated(lr) {
			have = append(have, lr.Method+" "+lr.GinPath)
		}
	}
	want := slices.Sorted(slices.Values(reachOwnerRecorded))
	slices.Sort(have)
	if !slices.Equal(have, want) {
		p = append(p, "the manifest's Guards.Owner routes are "+strings.Join(have, ", ")+", the recording's are "+strings.Join(want, ", "))
	}
	if len(have) != len(reachOwnerRecorded) {
		p = append(p, fmt.Sprintf("the manifest has %d Guards.Owner routes, the recording %d", len(have), len(reachOwnerRecorded)))
	}

	main := map[string]string{}
	for _, r := range vd.Routes {
		main[r.key()] = r.Verdict
	}
	for _, r := range vd.OwnerSet.Routes {
		k := r.key()
		if !strings.Contains(r.AdminRefusal, reachOwnerGateText) {
			p = append(p, k+": the admin session's recorded refusal is "+fmt.Sprintf("%q", r.AdminRefusal)+", not the owner gate's ("+reachOwnerGateText+")")
		}
		if len(r.Verdict) != len(vd.Principals) {
			p = append(p, fmt.Sprintf("%s: verdict has %d letters for %d principals", k, len(r.Verdict), len(vd.Principals)))
			continue
		}
		moved := false
		for i, name := range vd.Principals {
			if !reachOwnerAssured(name) && r.Verdict[i] != 'D' {
				p = append(p, k+" "+name+": the owner set admits a principal that is not the verified owner")
			}
			if m := main[k]; len(m) == len(r.Verdict) && m[i] == 'A' && r.Verdict[i] == 'D' {
				moved = true
			}
		}
		if !moved {
			p = append(p, k+": no principal is allowed in the main recording and refused in the owner set: the owner gate shows no effect")
		}
	}
	if o := vd.OwnerSet.Canary; o == nil || !maps.Equal(o, vd.Canary) {
		p = append(p, "owner_set canary is not the main recording's canary: the owner boots' principals did not behave as the main boots' did")
	}
	return p
}

func TestReachOwnerTables_MatchTheManifestAndTheOwnerSetRecording(t *testing.T) {
	_, vd := reachLoad(t)
	if p := reachOwnerTableProblems(vd, reachManifest()); len(p) > 0 {
		t.Fatalf("the owner tables, the manifest and the owner_set recording disagree:\n%s", strings.Join(p, "\n"))
	}
}

// Each doctoring of the manifest or the recording is reported, never absorbed.
func TestReachOwnerTables_FailOnADoctoredManifestOrRecording(t *testing.T) {
	_, vd := reachLoad(t)
	const reissue = "POST /api/v1/admin/guest-service-tokens/:userId/reissue"
	mutateManifest := func(f func(*httpapi.LegacyRoute)) []httpapi.LegacyRoute {
		m := slices.Clone(reachManifest())
		for i := range m {
			f(&m[i])
		}
		return m
	}
	lessRoute := *vd
	lessOwner := *vd.OwnerSet
	lessOwner.Routes = slices.DeleteFunc(slices.Clone(lessOwner.Routes), func(r reachR0Route) bool { return r.key() == reissue })
	lessRoute.OwnerSet = &lessOwner
	cases := []struct {
		name     string
		vd       *reachR0Doc
		manifest []httpapi.LegacyRoute
		want     string
	}{
		{"the Owner guard dropped on a recorded route", vd, mutateManifest(func(lr *httpapi.LegacyRoute) {
			if lr.Method+" "+lr.GinPath == reissue {
				lr.Guards.Owner = false
			}
		}), "Guards.Owner routes"},
		{"a manifest route gains the Owner guard the recording does not show", vd, mutateManifest(func(lr *httpapi.LegacyRoute) {
			if lr.Method+" "+lr.GinPath == "PUT /api/v1/config" {
				lr.Guards.Owner = true
			}
		}), "PUT /api/v1/config"},
		{"the recording lacks an owner route the manifest guards", &lessRoute, reachManifest(), "owner_set routes are"},
		{"no owner_set recording", &reachR0Doc{}, reachManifest(), "no owner_set recording"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(reachOwnerTableProblems(c.vd, c.manifest), "\n"); !strings.Contains(got, c.want) {
				t.Errorf("want a problem containing %q, got:\n%s", c.want, got)
			}
		})
	}
	// An owner set that admits a principal fails.
	doctored := *vd
	o := *vd.OwnerSet
	o.Routes = slices.Clone(o.Routes)
	v := []byte(o.Routes[0].Verdict)
	v[0] = 'A'
	o.Routes[0].Verdict = string(v)
	doctored.OwnerSet = &o
	if got := strings.Join(reachOwnerTableProblems(&doctored, reachManifest()), "\n"); !strings.Contains(got, "not the verified owner") {
		t.Errorf("an owner-set verdict admitting a principal was not reported:\n%s", got)
	}
}
