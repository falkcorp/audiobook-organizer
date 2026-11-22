// file: internal/server/legacy_manifest_gin_allowlist_test.go
// version: 0.1.0
// guid: 30d90274-0c54-4aba-91e3-75e4c339c14a
// last-edited: 2026-11-22

package server_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two differences crossCheck cannot call weaker or stronger are named here,
// one entry per route, and the check fails on every row of either class that has
// no entry and on every entry whose row no longer is of that class (a stale
// entry would otherwise vouch for a route nobody looked at):
//
//   - ROLE-NAME-GATE: the chain's admin gate tests the admin role by name and the
//     manifest row is a scope.
//   - CONDITIONAL-GUARD: the chain's credRouteWhen guard refuses some requests of
//     the route and the row, which cannot say "some", carries no Guards.Interactive.
//
// Each entry names the harness cell that proves the route's behaviour today (a
// test, and a literal in its file that names the route or case: allowListCells
// fails if either has moved). A role-name entry that no cell exercises with a
// role holding the row's scopes says so in its Note ("NO CELL: ..."); since the
// owner's ruling of 2026-11-22 every role-name entry cites one (scopeGateAdmits)
// and carries the ruling in its Note instead.

// Modes an entry applies to: both ABS modes, or one.
const (
	modeBoth = "both"
	modeOff  = "abs_off"
	modeOn   = "abs_on"
)

// cellRef names one harness cell: a test function, in a file relative to
// internal/server, and a literal of that file that names the route or case the
// test runs it on (empty when the test is one case).
type cellRef struct {
	File, Test, Sub string
	// ScopeHolding: the cell sends a non-admin principal that holds the row's
	// scopes and expects the refusal. A cell without it sends a non-admin that
	// holds nothing the row names: it proves gin refuses a non-admin, not that
	// the refusal is the role test's and not the scopes'.
	ScopeHolding bool
}

type allowEntry struct {
	Class  string // "ROLE-NAME-GATE" or "CONDITIONAL-GUARD"
	Method string
	Path   string // the gin path, as the manifest row has it
	Modes  string
	Reason string
	Cells  []cellRef
	Note   string
}

func (e allowEntry) key() string { return e.Method + " " + e.Path }

const (
	fAdminRoutes = "admin_routes_test.go"
	fAdminGates  = "identity_admin_gates_test.go"
	fMasterList  = "master_list_handler_test.go"
	fCredGuard   = "apikey_credential_guard_test.go"
	fRotateGone  = "wire_apikeys_gone_test.go"
	fRefusals    = "../books/cutover/registered_handler_refusals_test.go"
	fScopeGate   = "../books/cutover/registered_role_name_rows_test.go"
	fKeyForOther = "handlers/apikeys_identity_another_user_test.go"
)

// headRefused: HEAD's gin chain refuses a signed-in viewer on this route with the
// admin gate's own 403 (admin_routes_test.go, one assertion per classified route).
func headRefused(route string) cellRef {
	return cellRef{File: fAdminRoutes, Test: "TestAdminRoutes_AuthOnNonAdminGets403", Sub: `"` + route + `"`}
}

// bridge: under core identity a viewer holding exactly the row's scopes is
// refused (the bridge's admin gate keeps HEAD's role test).
func bridge(subtest string) cellRef {
	return cellRef{File: fAdminGates, Test: "TestAdminGates_BridgeRefusals", Sub: subtest, ScopeHolding: true}
}

// customAdmin: in the registered build a role that holds core:admin without being
// the admin role is refused.
func customAdmin(route string) cellRef {
	return cellRef{File: fRefusals, Test: "TestReachRegisteredPath_CustomCoreAdmin", Sub: `route: "` + route + `"`, ScopeHolding: true}
}

// scopeGateAdmits: in the registered build a custom role (and, where a key can
// hold it, an admin's key) holding exactly the row's scopes passes stage 13, the
// row's whole gate once B10g unmounts gin (the owner's ruling below).
func scopeGateAdmits(route string) cellRef {
	return cellRef{File: fScopeGate, Test: "TestReachRegisteredPath_RoleNameRowsScopeGateAdmits", Sub: `"` + route + `"`, ScopeHolding: true}
}

// ownerWidening is the ruling every role-name entry carries (F4-08 B10rn STOP 1,
// option 1: permissions only; 05-rbac §6.2, 09-abo-on-core §9.2.6).
const ownerWidening = "owner-approved widening, 2026-11-22 (F4-08 B10rn STOP 1, option 1): from B10g the row's scope is the whole gate, " +
	"so a custom role or key holding it is admitted where HEAD's name gate refused it; admin-granted only " +
	"(TestReachRegisteredPath_NonAdminCannotGrantAdminScopes). B10g deletes this entry when it unmounts gin"

func roleName(method, path, kind, row string, cells ...cellRef) allowEntry {
	e := allowEntry{
		Class: "ROLE-NAME-GATE", Method: method, Path: path, Modes: modeBoth,
		Reason: fmt.Sprintf("gin: %s admin gate, the admin role by name; row: %s", kind, row),
		Cells:  append(cells, scopeGateAdmits(method+" "+path)),
		Note:   ownerWidening,
	}
	if !slices.ContainsFunc(e.Cells, func(c cellRef) bool { return c.ScopeHolding }) {
		e.Note = "NO CELL: no test sends a non-admin role that holds the row's scopes to this route; must be cleared before B10g"
	}
	return e
}

// roleNameEntries: gin gates on the admin role by name. Once B10g unmounts gin,
// the manifest scope becomes the real gate, and a custom role or key holding that
// scope gets in where today it doesn't. The owner approved that widening on
// 2026-11-22 (ownerWidening), so the rows stay as they are. Every entry cites
// scopeGateAdmits, the cell that proves stage 13 admits such a role, and stays
// until B10g unmounts gin and deletes it.
var roleNameEntries = []allowEntry{
	roleName("GET", "/api/v1/admin/debug/book-files/:id", "read-only", "books:admin.view",
		headRefused("GET /api/v1/admin/debug/book-files/:id")),
	roleName("GET", "/api/v1/admin/debug/books/:id", "read-only", "books:admin.view",
		headRefused("GET /api/v1/admin/debug/books/:id")),
	roleName("GET", "/api/v1/admin/debug/edits", "read-only", "books:admin.view",
		headRefused("GET /api/v1/admin/debug/edits"), customAdmin("GET /api/v1/admin/debug/edits")),
	roleName("GET", "/api/v1/admin/debug/lookup", "read-only", "books:admin.view",
		headRefused("GET /api/v1/admin/debug/lookup"), bridge("admindebug read gate")),
	roleName("PATCH", "/api/v1/admin/debug/book-files/:id", "always-authenticated", "books:admin.debug",
		headRefused("PATCH /api/v1/admin/debug/book-files/:id")),
	roleName("PATCH", "/api/v1/admin/debug/books/:id", "always-authenticated", "books:admin.debug",
		headRefused("PATCH /api/v1/admin/debug/books/:id"), bridge("admindebug write gate")),
	roleName("POST", "/api/v1/admin/debug/edits/:edit_id/undo", "always-authenticated", "books:admin.debug",
		headRefused("POST /api/v1/admin/debug/edits/:edit_id/undo")),

	roleName("GET", "/api/v1/admin/guest-service-tokens", "always-authenticated", "core:admin",
		headRefused("GET /api/v1/admin/guest-service-tokens"),
		bridge("per-route always-authenticated (adminGroup + credRoute)"),
		customAdmin("GET /api/v1/admin/guest-service-tokens")),
	roleName("GET", "/api/v1/admin/guest-service-tokens/plan", "always-authenticated", "core:admin",
		headRefused("GET /api/v1/admin/guest-service-tokens/plan")),
	roleName("POST", "/api/v1/admin/guest-service-tokens/:userId/reissue", "always-authenticated", "core:admin",
		headRefused("POST /api/v1/admin/guest-service-tokens/:userId/reissue")),
	roleName("POST", "/api/v1/admin/guest-service-tokens/:userId/revoke", "always-authenticated", "core:admin",
		headRefused("POST /api/v1/admin/guest-service-tokens/:userId/revoke")),

	roleName("POST", "/api/v1/admin/recompact-digests", "always-authenticated", "core:admin",
		headRefused("POST /api/v1/admin/recompact-digests")),
	roleName("GET", "/api/v1/cache/stats/keys", "read-only", "books:admin.view",
		headRefused("GET /api/v1/cache/stats/keys"), bridge("per-route read-only (adminRoute)"),
		customAdmin("GET /api/v1/cache/stats/keys")),
	roleName("POST", "/api/v1/cache/invalidate", "always-authenticated", "core:admin",
		headRefused("POST /api/v1/cache/invalidate"), bridge("per-route always-authenticated (adminRoute)")),
	roleName("POST", "/api/v1/maintenance/wipe", "always-authenticated", "core:admin",
		headRefused("POST /api/v1/maintenance/wipe")),

	// The master-list group runs adminRole and s.perm(users.manage); the row is
	// AllOf(core:users.manage, core:roles.view, books:admin.view).
	roleName("GET", "/api/v1/master-lists", "read-only", "all(core:users.manage,core:roles.view,books:admin.view)",
		cellRef{File: fMasterList, Test: "TestMasterListUpload_AuthAndRole", Sub: "the listing is admin-only too"},
		bridge("group-level read-only (master-list group)")),
	roleName("POST", "/api/v1/master-lists", "read-only", "all(core:users.manage,core:roles.view,books:admin.view)",
		cellRef{File: fMasterList, Test: "TestMasterListUpload_AuthAndRole", Sub: "w = f.postMasterList(t, sess.ID"}),
	roleName("DELETE", "/api/v1/master-lists/:id", "read-only", "all(core:users.manage,core:roles.view,books:admin.view)",
		cellRef{File: fMasterList, Test: "TestMasterListDeleteAndElsewhere_AdminOnlyAndScoped", Sub: "delete as %s"}),
	roleName("GET", "/api/v1/master-lists/elsewhere", "read-only", "all(core:users.manage,core:roles.view,books:admin.view)",
		cellRef{File: fMasterList, Test: "TestMasterListDeleteAndElsewhere_AdminOnlyAndScoped", Sub: "elsewhere as %s"}),
}

// conditionalGuardEntries: credRouteWhen guards the request only when it targets
// another user's credential (apiKeyCreateForAnotherUser, apiKeyOwnedByAnotherUser).
// The row is AuthRequired with no Guards.Interactive, because Guards cannot say
// "sometimes". Once gin unmounts, nothing in the chain refuses an API key that
// mints for, or rotates, another user's key unless core's own check does; each
// entry must be cleared before B10g by a cell that proves core refuses it.
var conditionalGuardEntries = []allowEntry{
	{
		Class: "CONDITIONAL-GUARD", Method: "POST", Path: "/api/v1/auth/api-keys", Modes: modeBoth,
		Reason: "gin: credRouteWhen(apiKeyCreateForAnotherUser) refuses a key principal minting for another user; row: authenticated, no Guards.Interactive",
		Cells: []cellRef{
			{File: fCredGuard, Test: "TestCredentialGuard_APIKeyRefusedEverywhere_SessionAllowed", Sub: `"api key for another user"`},
			{File: fRefusals, Test: "TestReachRegisteredPath_HiddenHandlerChecks", Sub: `{route: "POST /api/v1/auth/api-keys", method: "POST"`},
			// With the guard gone the handler refuses it itself under identity
			// (F4-08 B10rn STOP 2 ruling, option 2), and core's own key routes
			// refuse a key minting for, or acting on, another user's key.
			{File: fKeyForOther, Test: "TestAPIKeysIdentity_AKeyCannotCreateAKeyForAnotherUser", Sub: `"a key for another user is refused by the handler"`},
			{File: fScopeGate, Test: "TestReachRegisteredPath_CoreRefusesAKeyOnAnotherUsersCredential", Sub: `"/api/core/v1/users/"+dick+"/api-keys"`},
		},
	},
	{
		Class: "CONDITIONAL-GUARD", Method: "POST", Path: "/api/v1/auth/api-keys/:id/rotate", Modes: modeBoth,
		Reason: "gin: credRouteWhen(apiKeyOwnedByAnotherUser) refuses a key principal rotating another user's key; row: authenticated, no Guards.Interactive",
		Cells: []cellRef{
			{File: fRotateGone, Test: "TestRetiredAPIKeyRotate_CredentialGuardRunsBeforeGone", Sub: `+f.targetKeyID+"/rotate"`},
		},
	},
}

func allowEntries() []allowEntry {
	return slices.Concat(roleNameEntries, conditionalGuardEntries)
}

func (e allowEntry) appliesTo(mode string) bool { return e.Modes == modeBoth || e.Modes == mode }

// allowListProblems compares the advisory findings of one ABS mode with the
// entries: a finding of either class without an entry, an entry whose row is not
// of its class in this mode, and a route listed twice.
func allowListProblems(mode string, findings []finding, entries []allowEntry) []string {
	type id struct{ class, key string }
	found := map[id]bool{}
	for _, f := range findings {
		if advisoryKinds[f.kind] {
			found[id{f.kind, f.key}] = true
		}
	}
	listed := map[id]bool{}
	var out []string
	for _, e := range entries {
		if !e.appliesTo(mode) {
			continue
		}
		k := id{e.Class, e.key()}
		if listed[k] {
			out = append(out, fmt.Sprintf("DUPLICATE: %s %s is listed twice", e.Class, e.key()))
		}
		listed[k] = true
		if !found[k] {
			out = append(out, fmt.Sprintf("STALE: %s %s is listed but the route is no longer of that class (or no longer a route) in %s", e.Class, e.key(), mode))
		}
	}
	for k := range found {
		if !listed[k] {
			out = append(out, fmt.Sprintf("UNLISTED: %s %s has no allow-list entry in %s: add one with the cell that proves it, or change the row", k.class, k.key, mode))
		}
	}
	slices.Sort(out)
	return out
}

// TestAllowListProblems_Rule pins allowListProblems on hand-built findings.
func TestAllowListProblems_Rule(t *testing.T) {
	entry := func(class, key, modes string) allowEntry {
		method, path, _ := strings.Cut(key, " ")
		return allowEntry{Class: class, Method: method, Path: path, Modes: modes}
	}
	fnd := func(class, key string) finding { return finding{kind: class, key: key} }
	const rn, cg = "ROLE-NAME-GATE", "CONDITIONAL-GUARD"

	cases := []struct {
		name     string
		findings []finding
		entries  []allowEntry
		mode     string
		want     []string
	}{
		{"listed", []finding{fnd(rn, "GET /a")}, []allowEntry{entry(rn, "GET /a", modeBoth)}, modeOff, nil},
		{"unlisted row", []finding{fnd(rn, "GET /a"), fnd(rn, "GET /b")}, []allowEntry{entry(rn, "GET /a", modeBoth)}, modeOff,
			[]string{"UNLISTED: ROLE-NAME-GATE GET /b has no allow-list entry in abs_off: add one with the cell that proves it, or change the row"}},
		{"stale entry, route gone", nil, []allowEntry{entry(rn, "GET /a", modeBoth)}, modeOff,
			[]string{"STALE: ROLE-NAME-GATE GET /a is listed but the route is no longer of that class (or no longer a route) in abs_off"}},
		{"stale entry, route is the other class now", []finding{fnd(cg, "GET /a")}, []allowEntry{entry(rn, "GET /a", modeBoth)}, modeOn,
			[]string{
				"STALE: ROLE-NAME-GATE GET /a is listed but the route is no longer of that class (or no longer a route) in abs_on",
				"UNLISTED: CONDITIONAL-GUARD GET /a has no allow-list entry in abs_on: add one with the cell that proves it, or change the row",
			}},
		{"entry for the other mode is not stale here", nil, []allowEntry{entry(rn, "GET /a", modeOn)}, modeOff, nil},
		{"entry for this mode only: the other mode's row is unlisted", []finding{fnd(rn, "GET /a")}, []allowEntry{entry(rn, "GET /a", modeOn)}, modeOff,
			[]string{"UNLISTED: ROLE-NAME-GATE GET /a has no allow-list entry in abs_off: add one with the cell that proves it, or change the row"}},
		{"listed twice", []finding{fnd(rn, "GET /a")}, []allowEntry{entry(rn, "GET /a", modeBoth), entry(rn, "GET /a", modeOff)}, modeOff,
			[]string{"DUPLICATE: ROLE-NAME-GATE GET /a is listed twice"}},
		{"a non-advisory finding needs no entry", []finding{fnd("MANIFEST-WEAKER", "GET /a")}, nil, modeOff, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, allowListProblems(tc.mode, tc.findings, tc.entries))
		})
	}
}

// TestAllowListEntries_AreCitedAndWellFormed: every entry has a reason and a
// valid class and mode, cites at least one cell, and each cell is real: its test
// function exists in the file and the literal that names its case appears there.
// A role-name entry has a ScopeHolding cell or a Note that says NO CELL, never
// both. The rows with no such cell are logged so they can be listed before B10g.
func TestAllowListEntries_AreCitedAndWellFormed(t *testing.T) {
	var noCell []string
	for _, e := range allowEntries() {
		t.Run(strings.ReplaceAll(e.Class+"_"+e.key(), " ", "_"), func(t *testing.T) {
			assert.Contains(t, []string{"ROLE-NAME-GATE", "CONDITIONAL-GUARD"}, e.Class)
			assert.Contains(t, []string{modeBoth, modeOff, modeOn}, e.Modes)
			assert.NotEmpty(t, e.Reason)
			require.NotEmpty(t, e.Cells, "an entry names the cell that proves the route's behaviour today")
			scope := false
			for _, c := range e.Cells {
				scope = scope || c.ScopeHolding
				src, err := os.ReadFile(c.File)
				require.NoError(t, err, "cell file %s", c.File)
				assert.Regexp(t, regexp.MustCompile(`(?m)^func `+regexp.QuoteMeta(c.Test)+`\(`), string(src), "cell test %s in %s", c.Test, c.File)
				if c.Sub != "" {
					assert.Contains(t, string(src), c.Sub, "cell %s names a case (%s) that %s no longer has", c.Test, c.Sub, c.File)
				}
			}
			hasNote := strings.HasPrefix(e.Note, "NO CELL")
			if e.Class == "ROLE-NAME-GATE" {
				assert.Equal(t, !scope, hasNote, "a role-name entry has a scope-holding cell or a NO CELL note, not both and not neither")
			} else {
				assert.False(t, hasNote)
			}
			if hasNote {
				noCell = append(noCell, e.key())
			}
		})
	}
	slices.Sort(noCell)
	t.Logf("%d role-name rows have no cell that sends a scope-holding non-admin role (each must be cleared before B10g): %s",
		len(noCell), strings.Join(noCell, "; "))
}
