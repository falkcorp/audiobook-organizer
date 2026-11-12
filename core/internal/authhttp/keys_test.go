// file: core/internal/authhttp/keys_test.go
// version: 1.0.2
// guid: e1a47c60-3b92-4d58-8c0f-52f9b6d1a734
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// keysFx is bruce (admin), gordon (a commissioner: users.manage, below admin),
// dick (a detective gordon dominates, with a key) and selina (a viewer).
type keysFx struct {
	e                     *env
	a                     *api
	bruce, gordon, dick   *auth.User
	selina                *auth.User
	dicksKey, brucesKey   *auth.APIKey
	dickRaw, bruceRawText string
}

func newKeysFx(t *testing.T) *keysFx {
	t.Helper()
	e := newEnv(t)
	f := &keysFx{e: e}
	f.bruce = e.user("bruce", "brucebruce", rbac.RoleAdmin)
	f.gordon = e.user("gordon", "gordongordon", roleCommissioner)
	f.dick = e.user("dick", "dickdick", rbac.RoleViewer)
	f.selina = e.user("selina", "selinaselina", rbac.RoleViewer)
	f.dickRaw, f.dicksKey = e.mint(e.session(f.dick), f.dick.ID, "core:self")
	f.bruceRawText, f.brucesKey = e.mint(e.session(f.bruce), f.bruce.ID, "core:admin")
	f.a = e.api()
	return f
}

// A key is read by its owner, or by a manager who dominates the owner. Any
// other caller gets exactly the answer a key that does not exist gets.
func TestKeyByID_ReachIsOwnerOrDominatingManager(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	got, err := call(specKeyGet(), a.getKey, e.session(f.dick), keyReq{ID: f.dicksKey.ID})
	if err != nil || got.ID != f.dicksKey.ID || got.OwnerUsername != "" {
		t.Fatalf("owner: %+v %v", got, err)
	}
	got, err = call(specKeyGet(), a.getKey, e.session(f.gordon), keyReq{ID: f.dicksKey.ID})
	if err != nil || got.OwnerUsername != "dick" {
		t.Fatalf("a dominating manager: %+v %v", got, err)
	}
	if _, err := call(specKeyGet(), a.getKey, e.session(f.bruce), keyReq{ID: f.dicksKey.ID}); err != nil {
		t.Fatalf("admin: %v", err)
	}
	_, missing := call(specKeyGet(), a.getKey, e.session(f.selina), keyReq{ID: "no-such-key"})
	for name, p := range map[string]*auth.Principal{
		"a viewer reading dick's key":            e.session(f.selina),
		"gordon reading the admin's key":         e.session(f.gordon),
		"dick reading the admin's key":           e.session(f.dick),
		"gordon with an API key (not a manager)": e.principal(f.gordon, auth.MethodAPIKey, "core:self", "gotham:patrol"),
	} {
		id := f.dicksKey.ID
		if strings.Contains(name, "admin's") {
			id = f.brucesKey.ID
		}
		_, err := call(specKeyGet(), a.getKey, p, keyReq{ID: id})
		wantErr(t, err, http.StatusNotFound, "not_found")
		if err.Error() != missing.Error() {
			t.Errorf("%s: %q differs from a missing key's %q", name, err, missing)
		}
	}
}

// A write on someone else's key goes to the Service, which refuses a manager
// who does not dominate the owner with dominance_required, and the refusal is
// audited where it is written.
func TestKeyByID_WritesNeedDominanceAndAreAudited(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	gordon := e.session(f.gordon)
	_, err := call(specKeyStatus(), a.setKeyStatus, gordon, keyStatusReq{ID: f.brucesKey.ID, Body: KeyStatus{Status: "disabled"}})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specKeyRevoke(), a.revokeKey, gordon, keyReq{ID: f.brucesKey.ID})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	if e.key(f.brucesKey.ID).Status != auth.KeyActive {
		t.Fatal("the admin's key changed")
	}
	emitted := e.audit.emittedActions()
	if len(emitted) != 2 || emitted[0] != "core.permission.deny" {
		t.Fatalf("emitted %v, want two core.permission.deny", emitted)
	}
	ev := e.audit.emitted[0]
	if ev.Reason != "dominance_required" || ev.Outcome != "denied" || ev.Actor.UserID != string(f.gordon.ID) || ev.Target.ID != string(f.bruce.ID) ||
		ev.Route != "PATCH /api/core/v1/api-keys/{id}" || ev.Permission != "core:self" {
		t.Fatalf("%+v", ev)
	}
	// A dominated owner's key: the manager may disable and revoke it.
	got, err := call(specKeyStatus(), a.setKeyStatus, gordon, keyStatusReq{ID: f.dicksKey.ID, Body: KeyStatus{Status: "disabled"}})
	if err != nil || got.Status != "disabled" {
		t.Fatalf("%+v %v", got, err)
	}
	// ... but not through an API key: acting on another user is interactive.
	gk := e.principal(f.gordon, auth.MethodAPIKey, "core:self", "core:users.manage", "gotham:patrol")
	_, err = call(specKeyRevoke(), a.revokeKey, gk, keyReq{ID: f.dicksKey.ID})
	if err == nil {
		t.Fatal("a manager's API key revoked another user's key")
	}
	if _, err := call(specKeyRevoke(), a.revokeKey, gordon, keyReq{ID: f.dicksKey.ID}); err != nil {
		t.Fatal(err)
	}
	// Another viewer: the same 404 as a missing key, and the key is untouched.
	_, err = call(specKeyRevoke(), a.revokeKey, e.session(f.selina), keyReq{ID: f.brucesKey.ID})
	wantErr(t, err, http.StatusNotFound, "not_found")
	if e.key(f.brucesKey.ID).Status != auth.KeyActive {
		t.Fatal("the admin's key changed")
	}
}

func TestAllKeys_EveryOwnersKeysWithTheirUsernamesAndNoSecret(t *testing.T) {
	f := newKeysFx(t)
	page, err := call(specKeysAll(), f.a.allKeys, f.e.session(f.bruce), listReq{Limit: 50})
	if err != nil || page.Total != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	owners := map[string]string{}
	for _, k := range page.Items {
		owners[k.ID] = k.OwnerUsername
	}
	if owners[f.dicksKey.ID] != "dick" || owners[f.brucesKey.ID] != "bruce" {
		t.Fatalf("%v", owners)
	}
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), f.dickRaw) || strings.Contains(string(b), f.bruceRawText) {
		t.Fatalf("the list carries a key: %s", b)
	}
}

// TestAllKeys_HoldsEachOwnersKeysToDominance: a manager below admin lists the
// keys of the users it dominates, and its own, and nobody else's: the admin's
// key, whose scopes and last-used address /users/{id}/api-keys and GET
// /api-keys/{id} refuse the same caller, is not in the list. The filter runs
// before the page is cut, so Total and the page boundaries do not count what
// is withheld.
func TestAllKeys_HoldsEachOwnersKeysToDominance(t *testing.T) {
	f := newKeysFx(t)
	gordon := f.e.session(f.gordon)
	_, gordonsKey := f.e.mint(gordon, f.gordon.ID, "core:self")

	page, err := call(specKeysAll(), f.a.allKeys, gordon, listReq{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, k := range page.Items {
		ids[k.ID] = true
	}
	if page.Total != 2 || len(page.Items) != 2 || !ids[f.dicksKey.ID] || !ids[gordonsKey.ID] || ids[f.brucesKey.ID] {
		t.Fatalf("gordon sees %v (total %d), want dick's and his own and not the admin's", ids, page.Total)
	}
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), f.brucesKey.ID) || strings.Contains(string(b), "bruce") {
		t.Fatalf("the admin's key leaked into gordon's list: %s", b)
	}
	small, err := call(specKeysAll(), f.a.allKeys, gordon, listReq{Limit: 1})
	if err != nil || small.Total != 2 || len(small.Items) != 1 {
		t.Fatalf("a page of one: total %d, %d items, %v", small.Total, len(small.Items), err)
	}

	admin, err := call(specKeysAll(), f.a.allKeys, f.e.session(f.bruce), listReq{Limit: 50})
	if err != nil || admin.Total != 3 {
		t.Fatalf("the admin sees %d keys, want all 3: %v", admin.Total, err)
	}
}

// TestAllKeys_AKeyOfADeletedOwnerIsAnAdminsToSee: a key whose owner no longer
// exists has no roles to dominate, so only core:admin lists it.
func TestAllKeys_AKeyOfADeletedOwnerIsAnAdminsToSee(t *testing.T) {
	f := newKeysFx(t)
	harvey := f.e.user("harvey", "", rbac.RoleViewer)
	_, orphan := f.e.mint(f.e.session(harvey), harvey.ID, "core:self")
	f.e.commit(func(b *storage.Batch) error { return f.e.st.DeleteUser(b, harvey) })
	if _, err := f.e.st.APIKeyByID(context.Background(), orphan.ID); err != nil {
		t.Skipf("deleting a user also deletes its keys here: %v", err)
	}
	for name, c := range map[string]struct {
		p    *auth.Principal
		want bool
	}{
		"admin":  {f.e.session(f.bruce), true},
		"gordon": {f.e.session(f.gordon), false},
	} {
		page, err := call(specKeysAll(), f.a.allKeys, c.p, listReq{Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found := false
		for _, k := range page.Items {
			found = found || k.ID == orphan.ID
		}
		if found != c.want {
			t.Errorf("%s: orphaned key listed = %v, want %v", name, found, c.want)
		}
	}
}

func TestUserKeys_ListAndMintForADominatedUserOnly(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	gordon := e.session(f.gordon)
	page, err := call(specUserKeys(), a.userKeys, gordon, userKeysReq{ID: string(f.dick.ID), Limit: 50})
	if err != nil || page.Total != 1 || page.Items[0].OwnerUsername != "dick" {
		t.Fatalf("%+v %v", page, err)
	}
	_, err = call(specUserKeys(), a.userKeys, gordon, userKeysReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specUserKeys(), a.userKeys, gordon, userKeysReq{ID: "u-nobody"})
	wantErr(t, err, http.StatusNotFound, "not_found")

	created, err := call(specUserKeyNew(), a.createUserKey, gordon, userKeyCreateReq{ID: string(f.dick.ID), Body: KeyCreate{Name: "grapnel", Scopes: []string{"gotham:patrol"}}})
	if err != nil || !strings.HasPrefix(created.Body.Key, "mck_") || created.Body.APIKey.Owner != string(f.dick.ID) {
		t.Fatalf("%+v %v", created, err)
	}
	// A scope the target's roles do not hold, though gordon holds it.
	_, err = call(specUserKeyNew(), a.createUserKey, gordon, userKeyCreateReq{ID: string(f.dick.ID), Body: KeyCreate{Name: "n", Scopes: []string{"gotham:signal"}}})
	if err == nil {
		t.Fatal("a key was minted for dick with a scope his roles do not hold")
	}
	_, err = call(specUserKeyNew(), a.createUserKey, gordon, userKeyCreateReq{ID: string(f.bruce.ID), Body: KeyCreate{Name: "n", Scopes: []string{"gotham:patrol"}}})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specUserKeyNew(), a.createUserKey, gordon, userKeyCreateReq{ID: "u-nobody", Body: KeyCreate{Name: "n", Scopes: []string{"gotham:patrol"}}})
	wantErr(t, err, http.StatusNotFound, "not_found")
	// Through an API key: refused before any key exists.
	before := len(e.keysOfOwner(f.dick.ID))
	gk := e.principal(f.gordon, auth.MethodAPIKey, "core:self", "core:users.manage", "gotham:patrol")
	if _, err := call(specUserKeyNew(), a.createUserKey, gk, userKeyCreateReq{ID: string(f.dick.ID), Body: KeyCreate{Name: "n", Scopes: []string{"gotham:patrol"}}}); err == nil {
		t.Fatal("a manager's API key minted a key for another user")
	}
	if after := len(e.keysOfOwner(f.dick.ID)); after != before {
		t.Fatalf("dick has %d keys, want %d", after, before)
	}
}

func (e *env) keysOfOwner(owner auth.UserID) []*auth.APIKey {
	e.t.Helper()
	var out []*auth.APIKey
	var failure error
	for k, err := range e.svc.APIKeys(context.Background(), owner) {
		if err != nil {
			failure = err
			continue
		}
		out = append(out, k)
	}
	if failure != nil {
		e.t.Fatal(failure)
	}
	return out
}

func TestUserKeyCreate_ValidateRefusesWhatMeRefuses(t *testing.T) {
	if (&userKeyCreateReq{ID: "u-dick"}).Validate() == nil {
		t.Fatal("a key with no name and no scopes validates")
	}
	if err := (&userKeyCreateReq{ID: "u-dick", Body: KeyCreate{Name: "n", Scopes: []string{"gotham:patrol"}, ExpiresIn: "24h"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestKeySpec_RefusesABadExpiryItself: Validate refuses a bad expiry first,
// but keySpec must not turn an unparseable one into the zero that means "the
// default lifetime"; it answers the same 422 on its own.
func TestKeySpec_RefusesABadExpiryItself(t *testing.T) {
	for _, bad := range []string{"soon", "-1h", "0s"} {
		_, err := KeyCreate{Name: "batphone", Scopes: []string{"core:self"}, ExpiresIn: bad}.keySpec("u-bruce")
		if _, ok := errors.AsType[*httpapi.Error](err); !ok {
			t.Errorf("expires_in %q: err = %v, want a validation problem", bad, err)
		}
	}
	ks, err := KeyCreate{Name: "batphone", Scopes: []string{"core:self"}, ExpiresIn: "720h"}.keySpec("u-bruce")
	if err != nil || ks.ExpiresIn != 720*time.Hour || ks.Owner != "u-bruce" {
		t.Fatalf("keySpec = %+v, %v", ks, err)
	}
}

// TestInviteCreate_RefusesABadTTLItself is the same rule for an invite's TTL.
func TestInviteCreate_RefusesABadTTLItself(t *testing.T) {
	f := newKeysFx(t)
	_, err := call(specInviteNew(), f.a.createInvite, f.e.session(f.gordon), inviteCreateReq{Body: InviteCreate{Username: "barbara", Role: string(rbac.RoleViewer), TTL: "tomorrow"}})
	if _, ok := errors.AsType[*httpapi.Error](err); !ok {
		t.Fatalf("err = %v, want a validation problem", err)
	}
}

// brokenBody is a ResponseWriter whose body write fails: the client went away
// after the status line.
type brokenBody struct{ h http.Header }

func (b *brokenBody) Header() http.Header       { return b.h }
func (b *brokenBody) WriteHeader(int)           {}
func (b *brokenBody) Write([]byte) (int, error) { return 0, errors.New("client went away") }

// TestWriteJSON_ALostClientIsLoggedNotSilent: a body that cannot be delivered
// is logged at debug, and never panics or rewrites the status.
func TestWriteJSON_ALostClientIsLoggedNotSilent(t *testing.T) {
	f := newKeysFx(t)
	var logs strings.Builder
	a := *f.a
	a.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	a.writeJSON(&brokenBody{h: http.Header{}}, r, http.StatusOK, map[string]string{"ok": "yes"})
	if !strings.Contains(logs.String(), "not delivered") {
		t.Fatalf("log = %q", logs.String())
	}
	if strings.Contains(logs.String(), "yes") {
		t.Fatalf("the body reached the log: %q", logs.String())
	}
}

// TestFail_AnAuthenticatedRouteNeverAnswers401ForAnAccountFailure: on a route
// whose caller is signed in, a deactivated account, a restricted one and a
// wrong password are 409 and 422, because a 401 there reads as a dead session
// and signs the user out. The sign-in routes keep their 401.
func TestFail_AnAuthenticatedRouteNeverAnswers401ForAnAccountFailure(t *testing.T) {
	f := newKeysFx(t)
	p := f.e.session(f.dick)
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrInactiveUser, http.StatusConflict, "inactive_user"},
		{auth.ErrResetRequired, http.StatusConflict, "password_reset_required"},
		{auth.ErrInvalidCredentials, http.StatusUnprocessableEntity, "invalid_credentials"},
	} {
		for _, s := range []spec{specMeKeyCreate(), specKeyRevoke(), specUserKeyNew(), specMePassword()} {
			wantErr(t, f.a.fail(context.Background(), s, p, "", fmt.Errorf("wrapped: %w", c.err)), c.status, c.code)
		}
		wantErr(t, f.a.fail(context.Background(), specLogin(), nil, "", c.err), http.StatusUnauthorized, c.code)
	}
}

// TestMintKey_ADeactivatedCallerIsNotASignedOutCaller is the same rule through
// a real route: the session is fine, the account was deactivated since.
func TestMintKey_ADeactivatedCallerIsNotASignedOutCaller(t *testing.T) {
	f := newKeysFx(t)
	p := f.e.session(f.dick)
	cur := f.e.reload(f.dick.ID)
	cur.Status = auth.StatusDisabled
	cur.Version++
	f.e.commit(func(b *storage.Batch) error { return f.e.st.PutUser(b, cur) })
	_, err := call(specMeKeyCreate(), f.a.createMyKey, p, createKeyReq{Body: KeyCreate{Name: "grapple", Scopes: []string{"core:self"}}})
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok || he.Status == http.StatusUnauthorized || he.Status < 400 || he.Status >= 500 {
		t.Fatalf("err = %v, want a 4xx that is not 401", err)
	}
}

// countingUsers counts the user lookups dominatedKeys makes.
type countingUsers struct {
	UserLookup
	n map[auth.UserID]int
}

func (c *countingUsers) User(ctx context.Context, id auth.UserID) (*auth.User, error) {
	c.n[id]++
	return c.UserLookup.User(ctx, id)
}

// TestDominatedKeys_DecidesOncePerOwnerAndKeepsOrder: the verdict on an owner is
// made once however many keys it has and does not leak onto another owner,
// whichever order the keys come in; the caller's own keys are never looked up.
func TestDominatedKeys_DecidesOncePerOwnerAndKeepsOrder(t *testing.T) {
	f := newKeysFx(t)
	cu := &countingUsers{UserLookup: f.a.Users, n: map[auth.UserID]int{}}
	a := *f.a
	a.Users = cu
	gordon := f.e.session(f.gordon)
	key := func(id string, owner *auth.User) APIKey { return APIKey{ID: id, Owner: string(owner.ID)} }
	in := []APIKey{
		key("k1", f.bruce), key("k2", f.gordon), key("k3", f.bruce), key("k4", f.dick),
		key("k5", f.gordon), key("k6", f.dick), key("k7", f.bruce),
	}
	got, err := a.dominatedKeys(context.Background(), gordon, in)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range got {
		ids = append(ids, k.ID)
	}
	if want := []string{"k2", "k4", "k5", "k6"}; !slices.Equal(ids, want) {
		t.Errorf("kept %v, want %v", ids, want)
	}
	if cu.n[f.bruce.ID] != 1 || cu.n[f.dick.ID] != 1 || cu.n[f.gordon.ID] != 0 {
		t.Errorf("lookups = %v, want one each for bruce and dick and none for gordon", cu.n)
	}
}
