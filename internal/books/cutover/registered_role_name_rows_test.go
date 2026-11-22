// file: internal/books/cutover/registered_role_name_rows_test.go
// version: 0.1.0
// guid: 77e6bf47-eea7-43f5-a067-8ab9c1fef0b0
// last-edited: 2026-11-22

package cutover_test

// F4-08 B10rn: the two kinds of route internal/server's gin cross-check cannot call
// weaker or stronger (legacy_manifest_gin_allowlist_test.go), run on the flipped
// build. Neither test clears an allow-list entry. Both record what B10g, which
// unmounts gin, has to decide:
//
//	TestReachRegisteredPath_RoleNameRowsScopeAdmitsNameRefuses
//	    the 19 ROLE-NAME-GATE rows. For each, a custom role holding exactly the
//	    row's scopes passes stage 13, which is the manifest scope gate that B10g
//	    leaves as the only gate. It is refused only by gin's role-name test
//	    (RequireAdmin: "admin role required"). On the books:admin.view and
//	    master-list rows an admin's key holding exactly the row's scopes is the
//	    same: stage 13 admits it and gin refuses it because it lacks an atom (a
//	    core:admin key gets one answer from both gates). The name gate and the scope gate therefore admit
//	    different sets, and no rbac.Requirement can close the gap, because core lets
//	    an admin create such a role (§4.7 role CRUD, custom.<slug>). The test fails
//	    as soon as stage 13 refuses the custom role (the row was narrowed) or gin
//	    admits it (gin relaxed): either way the B10rn table changed and must be
//	    redone.
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
// (05-rbac §4.4), so the debug write rows have no key: there stage 13 and gin both
// refuse every key.
var rnKeyScopes = map[string][]string{
	rnAdminView:  {"books:admin.view"},
	rnMasterList: {"core:users.manage", "core:roles.view", "books:admin.view"},
	rnCoreAdmin:  {"core:admin"},
}

// rnKeyAgrees: the classes on which the key gets the same answer from both gates.
// A core:admin key satisfies every atom's route requirement (none of them is
// Sensitive), so gin's every-atom test passes it too, and the guest-service-token
// rows' Guards.Interactive refuses it at stage 13. On the other classes the key
// splits the gates the way the custom role does.
var rnKeyAgrees = map[string]bool{rnCoreAdmin: true}

// The two refusals gin's RequireAdmin writes (internal/server/middleware/auth.go):
// the role-name test, and the test that an admin's key holds every atom.
const (
	rnNameRefusal = "admin role required"
	rnKeyRefusal  = "admin role required: this API key's scopes do not include every permission"
)

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

func TestReachRegisteredPath_RoleNameRowsScopeAdmitsNameRefuses(t *testing.T) {
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

			// A custom role holding exactly the row's scopes: the scope gate admits it,
			// the name gate refuses it.
			p := holders[r.perm]
			res, err := h.Do(p, r.method, r.path, nil)
			if err != nil {
				t.Fatalf("%s: %v", p.Name, err)
			}
			if res.Stage13 == Stage13Refused {
				t.Errorf("stage 13 refuses %s (%s %s): the scope gate no longer admits a custom role holding %s, so the row is not the B10rn row; redo the table",
					p.Name, res.Code, res.excerpt(), r.perm)
			}
			if res.Status != http.StatusForbidden || rnMessage(res) != rnNameRefusal {
				t.Errorf("gin no longer refuses %s by role name (%d %s): with gin relaxed the scope alone admits a role that is not the admin role, a WIDENING over HEAD that B10g must rule on",
					p.Name, res.Status, res.excerpt())
			}

			// An admin's key holding exactly the row's scope: the same split, with
			// the refusal of the key that lacks an atom.
			k := keys[r.perm]
			if k == nil {
				return
			}
			res, err = h.Do(k, r.method, r.path, nil)
			if err != nil {
				t.Fatalf("%s: %v", k.Name, err)
			}
			if rnKeyAgrees[r.perm] {
				// Both gates refuse (stage 13 does), or both admit (gin then lets
				// the request through to the handler).
				if res.Stage13 != Stage13Refused && res.Verdict() == 'D' {
					t.Errorf("stage 13 admits %s and gin refuses it (%d %s): the core:admin key now splits the gates too; redo the table", k.Name, res.Status, res.excerpt())
				}
				t.Logf("%s: %d stage13=%v", k.Name, res.Status, res.Stage13)
				return
			}
			if res.Stage13 == Stage13Refused {
				t.Errorf("stage 13 refuses %s (%s %s): an admin's key holding %s no longer passes the scope gate; redo the table", k.Name, res.Code, res.excerpt(), r.perm)
			}
			if res.Status != http.StatusForbidden || rnMessage(res) != rnKeyRefusal {
				t.Errorf("gin no longer refuses %s for lacking an atom (%d %s): with gin relaxed the key's scope alone admits it", k.Name, res.Status, res.excerpt())
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
