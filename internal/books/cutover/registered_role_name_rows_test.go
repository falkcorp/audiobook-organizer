// file: internal/books/cutover/registered_role_name_rows_test.go
// version: 0.1.0
// guid: 77e6bf47-eea7-43f5-a067-8ab9c1fef0b0
// last-edited: 2026-11-22

package cutover_test

// F4-08 B10rn: the two kinds of route internal/server's gin cross-check cannot call
// weaker or stronger (legacy_manifest_gin_allowlist_test.go), run on the flipped
// build.
//
//	TestReachRegisteredPath_RoleNameRowsScopeGateAdmits
//	    the 19 ROLE-NAME-GATE rows, under the owner's ruling of 2026-11-22 (B10rn
//	    STOP 1, option 1: permissions only; 05-rbac §6.2, 09 §9.2.6): from B10g,
//	    which unmounts gin, a row's manifest scope is its whole gate, and a custom
//	    role or key holding exactly that scope is admitted. This is an owner-approved
//	    widening over HEAD's RequireAdmin, admin-granted only
//	    (TestReachRegisteredPath_NonAdminCannotGrantAdminScopes). The test asserts
//	    that post-B10g expectation through stage 13's own decision. A core:admin key
//	    is refused at stage 13 on the four guest-service-token rows, which carry
//	    Guards.Interactive. Gin's answer behind stage 13 is reported, not asserted:
//	    until B10g the bridge still runs HEAD's role-name test (bridgedAdminGate)
//	    and refuses the role with "admin role required", and from B10g it admits
//	    it. B10g checklist: once gin is unmounted, this test still passes unchanged,
//	    and the gin-refusal log lines disappear.
//
//	TestReachRegisteredPath_NonAdminCannotGrantAdminScopes
//	    the other half of the ruling: a user who manages roles but holds none of
//	    core:admin, books:admin.view or books:admin.debug can neither put one in a
//	    role (create or update) nor mint a key holding one. Core refuses each with
//	    its escalation refusal.
//
//	TestReachRegisteredPath_CoreRefusesAKeyOnAnotherUsersCredential
//	    the CONDITIONAL-GUARD rows' question asked of core's own key routes, which
//	    replace books' /api/v1/auth/api-keys under identity. An admin's key holding
//	    core:users.manage is refused when it mints a key for another user, disables
//	    another user's key or revokes another user's key, and each refusal is pinned
//	    to the core check that writes it. Core has no rotate.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The scope sets of the 19 rows, as rbac.Requirement.String spells them.
const (
	rnAdminView  = "books:admin.view"
	rnAdminDebug = "books:admin.debug"
	rnCoreAdmin  = "core:admin"
	rnMasterList = "all(core:users.manage,core:roles.view,books:admin.view)"
)

// rnHolder is the custom role that holds exactly one row class's scopes, and the user
// who signs in with it.
type rnHolder struct {
	perm, role, title, user string
	scopes                  []string
}

var rnHolders = []rnHolder{
	{rnAdminView, "custom.archivists", "Archivists", "tim.drake.archivist", []string{"books:admin.view"}},
	{rnAdminDebug, "custom.debuggers", "Debuggers", "cassandra.cain.debugger", []string{"books:admin.debug"}},
	{rnCoreAdmin, "custom.wardens", "Wardens", "alfred.pennyworth.warden", []string{"core:admin"}},
	{rnMasterList, "custom.listkeepers", "List keepers", "stephanie.brown.listkeeper",
		[]string{"core:users.manage", "core:roles.view", "books:admin.view"}},
}

// rnRow is one ROLE-NAME-GATE row. Every path id is a placeholder: gin's admin gate
// refuses before any handler reads it, and stage 13 does not read it.
type rnRow struct {
	route, method, path, perm string
}

var rnRows = []rnRow{
	{"GET /api/v1/admin/debug/book-files/:id", "GET", "/api/v1/admin/debug/book-files/r2reach-missing", rnAdminView},
	{"GET /api/v1/admin/debug/books/:id", "GET", "/api/v1/admin/debug/books/r2reach-missing", rnAdminView},
	{"GET /api/v1/admin/debug/edits", "GET", "/api/v1/admin/debug/edits", rnAdminView},
	{"GET /api/v1/admin/debug/lookup", "GET", "/api/v1/admin/debug/lookup", rnAdminView},
	{"GET /api/v1/cache/stats/keys", "GET", "/api/v1/cache/stats/keys", rnAdminView},

	{"PATCH /api/v1/admin/debug/book-files/:id", "PATCH", "/api/v1/admin/debug/book-files/r2reach-missing", rnAdminDebug},
	{"PATCH /api/v1/admin/debug/books/:id", "PATCH", "/api/v1/admin/debug/books/r2reach-missing", rnAdminDebug},
	{"POST /api/v1/admin/debug/edits/:edit_id/undo", "POST", "/api/v1/admin/debug/edits/r2reach-missing/undo", rnAdminDebug},

	{"GET /api/v1/admin/guest-service-tokens", "GET", "/api/v1/admin/guest-service-tokens", rnCoreAdmin},
	{"GET /api/v1/admin/guest-service-tokens/plan", "GET", "/api/v1/admin/guest-service-tokens/plan", rnCoreAdmin},
	{"POST /api/v1/admin/guest-service-tokens/:userId/reissue", "POST", "/api/v1/admin/guest-service-tokens/r2reach-missing/reissue", rnCoreAdmin},
	{"POST /api/v1/admin/guest-service-tokens/:userId/revoke", "POST", "/api/v1/admin/guest-service-tokens/r2reach-missing/revoke", rnCoreAdmin},
	{"POST /api/v1/admin/recompact-digests", "POST", "/api/v1/admin/recompact-digests", rnCoreAdmin},
	{"POST /api/v1/cache/invalidate", "POST", "/api/v1/cache/invalidate", rnCoreAdmin},
	{"POST /api/v1/maintenance/wipe", "POST", "/api/v1/maintenance/wipe", rnCoreAdmin},

	{"GET /api/v1/master-lists", "GET", "/api/v1/master-lists", rnMasterList},
	{"POST /api/v1/master-lists", "POST", "/api/v1/master-lists", rnMasterList},
	{"DELETE /api/v1/master-lists/:id", "DELETE", "/api/v1/master-lists/r2reach-missing", rnMasterList},
	{"GET /api/v1/master-lists/elsewhere", "GET", "/api/v1/master-lists/elsewhere", rnMasterList},
}

// rnKeyScopes are the admin's core keys, one per row class, each holding exactly
// that class's scopes. books:admin.debug is Sensitive, which no key satisfies
// (05-rbac §4.4), so the debug write rows have no key: stage 13 refuses every key
// there.
var rnKeyScopes = map[string][]string{
	rnAdminView:  {"books:admin.view"},
	rnMasterList: {"core:users.manage", "core:roles.view", "books:admin.view"},
	rnCoreAdmin:  {"core:admin"},
}

// The two refusals gin's RequireAdmin writes behind stage 13 until B10g
// (internal/server/middleware/auth.go): the role-name test, and the test that an
// admin's key holds every atom.
const (
	rnNameRefusal = "admin role required"
	rnKeyRefusal  = "admin role required: this API key's scopes do not include every permission"
)

// rnBehindStage13 checks what answered behind stage 13 for a principal stage 13
// admitted: until B10g, gin's role-name refusal (want); from B10g, the handler
// (anything but 401/403). Any other refusal fails. It returns which it saw.
func rnBehindStage13(t *testing.T, p *Principal, res Result, want string) string {
	t.Helper()
	switch {
	case res.Verdict() == 'A':
		return "admitted by the handler (gin unmounted)"
	case res.Status == http.StatusForbidden && rnMessage(res) == want:
		return "refused by gin's role-name gate (until B10g)"
	}
	t.Errorf("%s: stage 13 admitted it and something other than gin's role-name gate refused it: %d %s", p.Name, res.Status, res.excerpt())
	return ""
}

// rnCustomSession creates holder's role through core's role CRUD as the admin
// session, invites holder's user into it, accepts the invite and signs the user in.
func rnCustomSession(t *testing.T, h *Registered, holder rnHolder) *Principal {
	t.Helper()
	adm := h.Principal("session:admin")
	if res := h.MustDo(t, adm, "POST", "/api/core/v1/roles", map[string]any{
		"name": holder.role, "title": holder.title, "description": "exactly the scopes of " + holder.perm + ", not named admin",
		"permissions": holder.scopes,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create %s: %d %s", holder.role, res.Status, res.excerpt())
	}
	res := h.MustDo(t, adm, "POST", "/api/core/v1/users/invites", map[string]any{"username": holder.user, "role": holder.role})
	if res.Status != http.StatusCreated {
		t.Fatalf("invite %s: %d %s", holder.user, res.Status, res.excerpt())
	}
	var inv struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(res.Body, &inv); err != nil || inv.Link == "" {
		t.Fatalf("the invite answered no link: %s", res.excerpt())
	}
	token := inv.Link[strings.LastIndexAny(inv.Link, "=/#")+1:]
	const password = "fixture-password-batcave-1939"
	req := httptest.NewRequest(http.MethodPost, "/api/core/v1/auth/invites/accept",
		strings.NewReader(fmt.Sprintf(`{"token":%q,"password":%q}`, token, password)))
	req.Header.Set("Content-Type", "application/json")
	if acc := h.Raw(req); acc.Status != http.StatusOK {
		t.Fatalf("accept %s's invite: %d %s", holder.user, acc.Status, acc.excerpt())
	}
	p := &Principal{Name: "session:" + holder.role, Kind: "session", User: holder.user, Role: holder.role, peer: h.nextPeer()}
	if p.cookie = h.login(p, password); p.cookie == nil {
		t.Fatalf("%s has no session", holder.user)
	}
	// The role holds exactly its scopes, so it may lack core:self and GET /me; read
	// the stored role back as the admin instead.
	var role struct {
		Permissions []string `json:"permissions"`
	}
	h.coreJSON(adm, http.MethodGet, "/api/core/v1/roles/"+holder.role, nil, &role)
	if got, want := fmt.Sprint(sorted(role.Permissions)), fmt.Sprint(sorted(holder.scopes)); got != want {
		t.Fatalf("%s holds %s, want exactly %s", holder.role, got, want)
	}
	return p
}

// rnAdminKey mints, as the admin session on its own route, a core key holding exactly
// scopes, and returns it as a key principal of the admin user.
func rnAdminKey(t *testing.T, h *Registered, name string, scopes []string) *Principal {
	t.Helper()
	adm := h.Principal("session:admin")
	var made struct {
		Key string `json:"key"`
	}
	h.coreJSON(adm, http.MethodPost, "/api/core/v1/me/api-keys", map[string]any{"name": name, "scopes": scopes, "expires_in": "720h"}, &made)
	if !strings.HasPrefix(made.Key, "mck_") {
		t.Fatalf("core minted no mck_ key for %s", name)
	}
	return &Principal{Name: "corekey:" + name, Kind: "key", User: adm.User, Role: adm.Role, Scopes: scopes, Core: true, bearer: made.Key, peer: h.nextPeer()}
}

func sorted(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

func rnMessage(res Result) string {
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(res.Body, &b)
	return b.Error
}

func TestReachRegisteredPath_RoleNameRowsScopeGateAdmits(t *testing.T) {
	if got := len(rnRows); got != 19 {
		t.Fatalf("the table has %d rows, the allow-list names 19 ROLE-NAME-GATE rows", got)
	}
	h := bootRegistered(t, registeredConfig{})
	viewer := h.Principal("session:viewer")
	holders := map[string]*Principal{}
	for _, holder := range rnHolders {
		holders[holder.perm] = rnCustomSession(t, h, holder)
	}
	keys := map[string]*Principal{}
	for perm, scopes := range rnKeyScopes {
		keys[perm] = rnAdminKey(t, h, "oracle-"+strings.NewReplacer(":", "-", ".", "-", "(", "-", ")", "", ",", "-").Replace(perm), scopes)
	}

	for _, r := range rnRows {
		t.Run(r.route, func(t *testing.T) {
			// Control: stage 13 judges the row's scopes on this route. A viewer holds
			// none of them and stage 13 itself refuses it.
			ctl, err := h.Do(viewer, r.method, r.path, nil)
			if err != nil {
				t.Fatalf("viewer: %v", err)
			}
			if ctl.Route == nil || ctl.Route.Method+" "+ctl.Route.Name != r.route {
				t.Fatalf("the request matched %v, not %s", ctl.Route, r.route)
			}
			if got := ctl.Route.Perm.String(); got != r.perm {
				t.Fatalf("the row's requirement is %s, the table says %s: the row changed, redo the B10rn table", got, r.perm)
			}
			if ctl.Stage13 != Stage13Refused {
				t.Fatalf("control: stage 13 did not refuse a viewer (%d %s): the cell proves nothing", ctl.Status, ctl.excerpt())
			}

			// A custom role holding exactly the row's scopes: stage 13, the whole gate
			// from B10g, admits it (the owner-approved widening).
			p := holders[r.perm]
			res, err := h.Do(p, r.method, r.path, nil)
			if err != nil {
				t.Fatalf("%s: %v", p.Name, err)
			}
			if res.Stage13 == Stage13Refused {
				t.Errorf("stage 13 refuses %s (%s %s): a custom role holding exactly %s is not admitted, against the owner's ruling (05-rbac §6.2)",
					p.Name, res.Code, res.excerpt(), r.perm)
			} else {
				t.Logf("%s: stage 13 admits; behind it: %s", p.Name, rnBehindStage13(t, p, res, rnNameRefusal))
			}

			// An admin's key holding exactly the row's scopes: admitted by stage 13,
			// except where the row carries Guards.Interactive (the guest-service-token
			// rows), which refuses every key.
			k := keys[r.perm]
			if k == nil {
				return
			}
			res, err = h.Do(k, r.method, r.path, nil)
			if err != nil {
				t.Fatalf("%s: %v", k.Name, err)
			}
			if ctl.Route.Guards.Interactive {
				h.Refused(res, http.StatusForbidden, "interactive_required")
				return
			}
			if res.Stage13 == Stage13Refused {
				t.Errorf("stage 13 refuses %s (%s %s): an admin's key holding exactly %s is not admitted", k.Name, res.Code, res.excerpt(), r.perm)
			} else {
				t.Logf("%s: stage 13 admits; behind it: %s", k.Name, rnBehindStage13(t, k, res, rnKeyRefusal))
			}
		})
	}
}

// rnUserID is the core user id of username, read as the admin session.
func rnUserID(t *testing.T, h *Registered, username string) string {
	t.Helper()
	for _, u := range rhrItems(rhrBody(t, h.MustDo(t, h.Principal("session:admin"), "GET", "/api/core/v1/users?limit=100", nil)), "items") {
		if rhrStr(u, "username") == username {
			return rhrStr(u, "id")
		}
	}
	t.Fatalf("core lists no user %q", username)
	return ""
}

// rnKeys is the id->status of every key of the user, read as the admin session.
func rnKeys(t *testing.T, h *Registered, uid string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, k := range rhrItems(rhrBody(t, h.MustDo(t, h.Principal("session:admin"), "GET", "/api/core/v1/users/"+uid+"/api-keys?limit=100", nil)), "items") {
		out[rhrStr(k, "id")] = rhrStr(k, "status")
	}
	return out
}

func TestReachRegisteredPath_CoreRefusesAKeyOnAnotherUsersCredential(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	adm := h.Principal("session:admin")
	dick := rnUserID(t, h, "dick.grayson")
	before := rnKeys(t, h, dick)
	var dicksKey string
	for id, st := range before {
		if st == "active" {
			dicksKey = id
			break
		}
	}
	if dicksKey == "" {
		t.Fatalf("dick.grayson has no active key: %v", before)
	}
	mint := map[string]any{"name": "batarang", "scopes": []string{"books:view"}, "expires_in": "720h"}

	// The imported abk_ key and its core-native twin: lucius.fox's (an admin's) key
	// holding users.manage, core:users.manage and core:roles.view after M3.
	for _, name := range []string{"key:admin:users", coreKeyName("key:admin:users")} {
		k := h.Principal(name)
		t.Run(name, func(t *testing.T) {
			// Mint a key for another user: stage 13's Guards.Interactive on core's
			// createUserAPIKey.
			res := h.MustDo(t, k, "POST", "/api/core/v1/users/"+dick+"/api-keys", mint)
			h.Refused(res, http.StatusForbidden, "interactive_required")

			// Disable, then revoke, another user's key: auth.Service.mayManage, which
			// requires an interactive method to act on another user. Stage 13 and
			// the service both answer permission_denied, so the control comes first:
			// the same key disables and revokes a child key of its own on the same
			// two routes, which proves stage 13 admits it there and the refusal
			// below is the service's.
			//
			// The core-native twin holds no core:self (mintCoreKeys leaves the
			// ambient scopes off), so stage 13 refuses it on every core:self route
			// and only the imported key reaches the service there.
			var child struct {
				APIKey struct {
					ID string `json:"id"`
				} `json:"api_key"`
			}
			reaches := !k.Core
			if reaches {
				h.coreJSON(k, http.MethodPost, "/api/core/v1/me/api-keys", map[string]any{"name": "grapnel", "scopes": []string{"core:users.manage"}, "expires_in": "1h"}, &child)
				if child.APIKey.ID == "" {
					t.Fatal("the key's own child key has no id")
				}
			}
			for _, m := range []string{"PATCH", "DELETE"} {
				var body any
				if m == "PATCH" {
					body = map[string]any{"status": "disabled"}
				}
				if reaches {
					if res := h.MustDo(t, k, m, "/api/core/v1/api-keys/"+child.APIKey.ID, body); res.Status/100 != 2 {
						t.Fatalf("control: %s of the key's own child answered %d %s: the cell proves nothing", m, res.Status, res.excerpt())
					}
				}
				res = h.MustDo(t, k, m, "/api/core/v1/api-keys/"+dicksKey, body)
				h.Refused(res, http.StatusForbidden, "permission_denied")
			}
		})
	}
	if after := rnKeys(t, h, dick); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("dick.grayson's keys changed under a key's requests: %v, was %v", after, before)
	}

	// Control: the same three acts through the admin's interactive session succeed,
	// so each refusal above is about the credential, not the route.
	if res := h.MustDo(t, adm, "POST", "/api/core/v1/users/"+dick+"/api-keys", mint); res.Status != http.StatusCreated {
		t.Errorf("control: the admin session could not mint a key for dick.grayson: %d %s", res.Status, res.excerpt())
	}
	if res := h.MustDo(t, adm, "PATCH", "/api/core/v1/api-keys/"+dicksKey, map[string]any{"status": "disabled"}); res.Status/100 != 2 {
		t.Errorf("control: the admin session could not disable dick.grayson's key: %d %s", res.Status, res.excerpt())
	}
	if res := h.MustDo(t, adm, "DELETE", "/api/core/v1/api-keys/"+dicksKey, nil); res.Status/100 != 2 {
		t.Errorf("control: the admin session could not revoke dick.grayson's key: %d %s", res.Status, res.excerpt())
	}
}

// rnDetail is the detail of a problem document.
func rnDetail(res Result) string {
	var b struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(res.Body, &b)
	return b.Detail
}

// The escalation refusals core writes (core/internal/rbachttp/errors.go,
// core/internal/authhttp/errors.go): a role write naming a permission the actor
// does not hold (rbac.ErrEscalation), and a key whose scopes the actor does not
// hold (auth.ErrScopeEscalation).
const (
	rnRoleEscalation = "a role may be written only by an actor who holds every permission the change touches"
	rnKeyEscalation  = "the key would hold a scope its creator does not"
)

func TestReachRegisteredPath_NonAdminCannotGrantAdminScopes(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	// A role manager and user manager with none of the admin scopes, and a user it
	// dominates (a role holding core:roles.view only) to assign roles to.
	keeper := rnCustomSession(t, h, rnHolder{"", "custom.rolekeepers", "Role keepers", "leslie.thompkins.rolekeeper",
		[]string{"core:self", "core:roles.manage", "core:roles.view", "core:users.manage"}})
	rnCustomSession(t, h, rnHolder{"", "custom.clerks", "Clerks", "jim.corrigan.clerk", []string{"core:roles.view"}})
	clerk := rnUserID(t, h, "jim.corrigan.clerk")
	// A role holding books:admin.view, made by the admin, for the assignment case.
	if res := h.MustDo(t, h.Principal("session:admin"), "POST", "/api/core/v1/roles", map[string]any{
		"name": "custom.archivists", "title": "Archivists", "permissions": []string{"books:admin.view"},
	}); res.Status != http.StatusCreated {
		t.Fatalf("create custom.archivists: %d %s", res.Status, res.excerpt())
	}

	// Controls: the keeper may create a role, mint a key and assign a role, each
	// with a permission it holds, so every refusal below is about the scope.
	if res := h.MustDo(t, keeper, "POST", "/api/core/v1/roles", map[string]any{
		"name": "custom.readers", "title": "Readers", "permissions": []string{"core:roles.view"},
	}); res.Status != http.StatusCreated {
		t.Fatalf("control: the keeper could not create a role it holds every permission of: %d %s", res.Status, res.excerpt())
	}
	if res := h.MustDo(t, keeper, "POST", "/api/core/v1/me/api-keys", map[string]any{
		"name": "control", "scopes": []string{"core:roles.view"}, "expires_in": "1h",
	}); res.Status != http.StatusCreated {
		t.Fatalf("control: the keeper could not mint a key it holds the scope of: %d %s", res.Status, res.excerpt())
	}
	if res := h.MustDo(t, keeper, "PUT", "/api/core/v1/users/"+clerk+"/roles", map[string]any{
		"roles": []string{"custom.clerks", "custom.readers"},
	}); res.Status/100 != 2 {
		t.Fatalf("control: the keeper could not assign a role it holds: %d %s", res.Status, res.excerpt())
	}

	for i, scope := range []string{"core:admin", "books:admin.view", "books:admin.debug"} {
		t.Run(scope, func(t *testing.T) {
			res := h.MustDo(t, keeper, "POST", "/api/core/v1/roles", map[string]any{
				"name": fmt.Sprintf("custom.usurpers%d", i), "title": "Usurpers", "permissions": []string{"core:roles.view", scope},
			})
			if h.Refused(res, http.StatusForbidden, "scope_escalation") && rnDetail(res) != rnRoleEscalation {
				t.Errorf("create a role holding %s: detail %q, want %q", scope, rnDetail(res), rnRoleEscalation)
			}
			res = h.MustDo(t, keeper, "POST", "/api/core/v1/me/api-keys", map[string]any{
				"name": "usurper", "scopes": []string{scope}, "expires_in": "1h",
			})
			if h.Refused(res, http.StatusForbidden, "scope_escalation") && rnDetail(res) != rnKeyEscalation {
				t.Errorf("mint a key holding %s: detail %q, want %q", scope, rnDetail(res), rnKeyEscalation)
			}
		})
	}
	// Granting through assignment: the admin role (core:admin) and a role holding
	// books:admin.view. Core refuses it in auth.Service.SetUserRoles with
	// RBAC.CanAssign (core/auth/users.go), wrapped in auth.ErrScopeEscalation, and
	// authhttp writes the one detail it has for that error (rnKeyEscalation, which
	// names a key even here: core/internal/authhttp/errors.go).
	for _, role := range []string{"admin", "custom.archivists"} {
		t.Run("assign "+role, func(t *testing.T) {
			res := h.MustDo(t, keeper, "PUT", "/api/core/v1/users/"+clerk+"/roles", map[string]any{"roles": []string{"custom.clerks", role}})
			if h.Refused(res, http.StatusForbidden, "scope_escalation") && rnDetail(res) != rnKeyEscalation {
				t.Errorf("assign %s: detail %q, want %q", role, rnDetail(res), rnKeyEscalation)
			}
		})
	}
	// Nothing changed: the clerk holds what the control gave it, and no usurper role exists.
	var roles struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	h.coreJSON(h.Principal("session:admin"), http.MethodGet, "/api/core/v1/roles", nil, &roles)
	for _, r := range roles.Items {
		if strings.HasPrefix(r.Name, "custom.usurpers") {
			t.Errorf("role %s was created", r.Name)
		}
	}
}
