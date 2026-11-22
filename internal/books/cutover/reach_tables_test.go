// file: internal/books/cutover/reach_tables_test.go
// version: 0.1.0
// guid: 3b7e2f9a-6d14-4c85-a0e3-91f5c8b2d746
// last-edited: 2026-11-22

package cutover

import (
	"fmt"
	"sort"
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

// reachOwnerTwins declares, for each owner route the recording has no row for, a
// recorded owner route that is its twin: the same permission, the same shape of
// action on the same resource, so R0's owner-set answer for the twin is the
// answer R0 would have given the route. The R2-only reissue route is the twin of
// revoke (both act on one guest's service token, both need users.manage plus the
// owner). The comparison reads the twin's recorded letters and holds the model's
// letters for the route to them, so dropping the Owner guard on reissue moves a
// model letter and fails the way a recorded route's would, instead of only
// failing the mapping. It is a declared claim: a twin that is not one is for
// review to catch, and the check below at least requires the twin to be recorded,
// to be an Owner route in R2 and to carry the same R2 requirement.
var reachOwnerTwins = map[string]string{
	"POST /api/v1/admin/guest-service-tokens/:userId/reissue": "POST /api/v1/admin/guest-service-tokens/:userId/revoke",
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
