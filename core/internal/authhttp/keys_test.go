// file: core/internal/authhttp/keys_test.go
// version: 1.0.1
// guid: e1a47c60-3b92-4d58-8c0f-52f9b6d1a734
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
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
	page, err := call(specKeysAll(), f.a.allKeys, f.e.session(f.gordon), listReq{Limit: 50})
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
