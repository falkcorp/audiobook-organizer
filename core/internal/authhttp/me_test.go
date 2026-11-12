// file: core/internal/authhttp/me_test.go
// version: 1.0.1
// guid: 7b3c9e04-1d58-4a26-b6f1-d85a02c7e913
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

func TestMe_ShowsTheCallerAndNoStoredSecret(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	hash, err := e.st.PasswordHash(context.Background(), bruce.ID)
	if err != nil || hash == "" {
		t.Fatalf("fixture: %q %v", hash, err)
	}
	a := e.api()
	p := e.session(bruce)
	got, err := call(specMe(), a.me, p, noBody{})
	if err != nil {
		t.Fatal(err)
	}
	if got.User.Username != "bruce" || got.Method != "session" || len(got.Permissions) == 0 || !slices.Contains(got.Permissions, "core:admin") || got.OwnerVerified {
		t.Fatalf("%+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), hash) || strings.Contains(strings.ToLower(string(b)), "hash") || strings.Contains(strings.ToLower(string(b)), "epoch") {
		t.Fatalf("/me carries stored credential state: %s", b)
	}
	p.Assurance = auth.AssuranceOwner
	got, _ = call(specMe(), a.me, p, noBody{})
	if !got.OwnerVerified {
		t.Fatal("owner assurance is not shown")
	}
}

func TestChangePassword_SelfOnlyAndEndsOtherSessions(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	e.user("dick", "dickdick", roleDetective)
	a := e.api()
	p, _ := e.withSession(bruce)
	other, _ := e.withSession(bruce)
	// A wrong current password is a validation failure of this request, not a
	// 401 that would sign the page out.
	_, err := call(specMePassword(), a.changePassword, p, passwordReq{Body: PasswordChange{Current: "nope", New: "a brand new passphrase"}})
	wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if _, err := call(specMePassword(), a.changePassword, p, passwordReq{Body: PasswordChange{Current: "brucebruce", New: "a brand new passphrase"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Login(context.Background(), auth.LoginInput{Username: "bruce", Password: "brucebruce", Client: peer("192.0.2.9")}); err == nil {
		t.Fatal("the old password still signs in")
	}
	if _, err := e.svc.Login(context.Background(), auth.LoginInput{Username: "bruce", Password: "a brand new passphrase", Client: peer("192.0.2.9")}); err != nil {
		t.Fatalf("the new password does not sign in: %v", err)
	}
	// The password changed is the caller's own: nobody else's did.
	if _, err := e.svc.Login(context.Background(), auth.LoginInput{Username: "dick", Password: "dickdick", Client: peer("192.0.2.9")}); err != nil {
		t.Fatalf("another user's password changed: %v", err)
	}
	if after := e.reload(bruce.ID); after.CredentialEpoch <= bruce.CredentialEpoch {
		t.Fatalf("epoch %d did not move: other sessions (%s) survive", after.CredentialEpoch, other.CredentialID)
	}
	// Too short or empty: a validation failure that names the field, not the
	// password.
	_, err = call(specMePassword(), a.changePassword, e.session(e.reload(bruce.ID)), passwordReq{Body: PasswordChange{Current: "a brand new passphrase", New: ""}})
	wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
}

func TestUpdateMe_NeedsAProfileWriter(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	name := "Batman"
	p := e.session(bruce)
	_, err := call(specMeEd(), e.api().updateMe, p, updateMeReq{Body: ProfilePatch{DisplayName: &name}})
	wantErr(t, err, http.StatusNotImplemented, "profile_edit_unavailable")
	w := &fakeProfiles{}
	a := e.api(func(d *Deps) { d.Profiles = w })
	got, err := call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{DisplayName: &name}})
	if err != nil || got.DisplayName != "Batman" || w.got.DisplayName == nil || w.got.Email != nil {
		t.Fatalf("%+v %v %+v", got, err, w.got)
	}
	if err := (&updateMeReq{}).Validate(); err == nil {
		t.Fatal("an empty patch validates")
	}
	if err := (&updateMeReq{Body: ProfilePatch{Email: &name}}).Validate(); err != nil {
		t.Fatal(err)
	}
	// A writer's error goes through the mapping, not out verbatim.
	w.err = auth.ErrEmailInUse
	_, err = call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{Email: &name}})
	if err == nil {
		t.Fatal("a failing writer answered success")
	}
}

type fakeProfiles struct {
	got ProfileUpdate
	err error
}

func (f *fakeProfiles) UpdateProfile(_ context.Context, actor *auth.Principal, in ProfileUpdate) (*auth.User, error) {
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	u := *actor.User
	if in.DisplayName != nil {
		u.DisplayName = *in.DisplayName
	}
	return &u, nil
}

func TestMySessions_NeverCarryATokenAndRevokeOnlyOwn(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	dick := e.user("dick", "dickdick", roleDetective)
	a := e.api()
	p, is := e.withSession(bruce)
	_, dickSession := e.withSession(dick)
	page, err := call(specMeSessions(), a.mySessions, p, listReq{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	if page.Total != 1 || !page.Items[0].Current || strings.Contains(string(b), is.Token) || strings.Contains(string(b), "mcs_") {
		t.Fatalf("%s", b)
	}
	// Another user's session is the answer a missing one gets.
	_, err = call(specMeSessionRevoke(), a.revokeMySession, p, sessionReq{PublicID: dickSession.PublicID})
	wantErr(t, err, http.StatusNotFound, "not_found")
	_, err = call(specMeSessionRevoke(), a.revokeMySession, p, sessionReq{PublicID: "no-such-session"})
	wantErr(t, err, http.StatusNotFound, "not_found")
	if !slices.Contains(e.sessionIDs(dick.ID), dickSession.PublicID) {
		t.Fatal("dick's session was revoked through bruce's /me")
	}
	if _, err := call(specMeSessionRevoke(), a.revokeMySession, p, sessionReq{PublicID: is.PublicID}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(e.sessionIDs(bruce.ID), is.PublicID) {
		t.Fatal("the session survives its own revocation")
	}
}

// A key is shown once, by the call that mints it; no list or read returns it,
// its hash or any part of it.
func TestMyKeys_TheKeyIsShownOnceAndNeverListed(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.api()
	p := e.session(bruce)
	created, err := call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: KeyCreate{Name: "utility belt", Scopes: []string{"core:self"}, ExpiresIn: "24h"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := created.Body.Key
	if !strings.HasPrefix(raw, "mck_") || !strings.HasSuffix(created.Location, created.Body.APIKey.ID) || created.Body.APIKey.Status != "active" || created.Body.APIKey.ExpiresAt == nil {
		t.Fatalf("%+v", created)
	}
	page, err := call(specMeKeys(), a.myKeys, p, listReq{Limit: 50})
	if err != nil || page.Total != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	stored := e.key(created.Body.APIKey.ID)
	b, _ := json.Marshal(page)
	for _, secret := range []string{raw, strings.TrimPrefix(raw, "mck_"), stored.ID + "x"} {
		if secret != "" && strings.Contains(string(b), secret) && secret != stored.ID+"x" {
			t.Fatalf("the list carries %q: %s", secret, b)
		}
	}
	if strings.Contains(strings.ToLower(string(b)), "hash") || strings.Contains(string(b), `"key"`) {
		t.Fatalf("the list names a key or a hash: %s", b)
	}
	got, err := call(specKeyGet(), a.getKey, p, keyReq{ID: stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	if bb, _ := json.Marshal(got); strings.Contains(string(bb), raw) {
		t.Fatalf("a read returns the key: %s", bb)
	}
}

func TestMyKeys_CreateRefusals(t *testing.T) {
	e := newEnv(t)
	selina := e.user("selina", "selinaselina", rbac.RoleViewer)
	a := e.api()
	p := e.session(selina)
	// A scope its owner does not hold.
	_, err := call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: KeyCreate{Name: "n", Scopes: []string{"core:users.manage"}}})
	wantErr(t, err, http.StatusForbidden, "scope_escalation")
	// A wildcard, an empty list and a bad expiry are refused by Validate.
	for _, k := range []KeyCreate{
		{Name: "", Scopes: []string{"core:self"}}, {Name: "n"}, {Name: "n", Scopes: []string{"core:self"}, ExpiresIn: "soon"}, {Name: "n", Scopes: []string{"core:self"}, ExpiresIn: "-1h"},
	} {
		if (&createKeyReq{Body: k}).Validate() == nil {
			t.Errorf("%+v validates", k)
		}
	}
	// A wildcard scope reaches the Service, which refuses it as bad input.
	_, err = call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: KeyCreate{Name: "n", Scopes: []string{"*"}}})
	wantErr(t, err, http.StatusBadRequest, "bad_request")
}

func TestMyKeys_StatusChanges(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	dick := e.user("dick", "dickdick", rbac.RoleViewer)
	a := e.api()
	p := e.session(bruce)
	_, k := e.mint(p, bruce.ID, "core:self")
	_, dicks := e.mint(e.session(dick), dick.ID, "core:self")
	// Disable, then re-enable (interactive), then revoke; revoked stays revoked.
	got, err := call(specMeKeyStatus(), a.setMyKeyStatus, p, keyStatusReq{ID: k.ID, Body: KeyStatus{Status: "disabled"}})
	if err != nil || got.Status != "disabled" {
		t.Fatalf("%+v %v", got, err)
	}
	// Another of bruce's keys cannot re-enable it: re-enabling needs an
	// interactive sign-in.
	_, k2 := e.mint(p, bruce.ID, "core:self")
	kp := e.principal(bruce, auth.MethodAPIKey, "core:self")
	kp.CredentialID = k2.ID
	_, err = call(specMeKeyStatus(), a.setMyKeyStatus, kp, keyStatusReq{ID: k.ID, Body: KeyStatus{Status: "active"}})
	wantErr(t, err, http.StatusForbidden, "permission_denied")
	got, err = call(specMeKeyStatus(), a.setMyKeyStatus, p, keyStatusReq{ID: k.ID, Body: KeyStatus{Status: "active"}})
	if err != nil || got.Status != "active" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := call(specMeKeyRevoke(), a.revokeMyKey, p, keyReq{ID: k.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = call(specMeKeyStatus(), a.setMyKeyStatus, p, keyStatusReq{ID: k.ID, Body: KeyStatus{Status: "active"}})
	wantErr(t, err, http.StatusConflict, "key_revoked")
	// Another user's key is the answer a missing one gets, on every write.
	_, err = call(specMeKeyStatus(), a.setMyKeyStatus, p, keyStatusReq{ID: dicks.ID, Body: KeyStatus{Status: "disabled"}})
	wantErr(t, err, http.StatusNotFound, "not_found")
	_, err = call(specMeKeyRevoke(), a.revokeMyKey, p, keyReq{ID: dicks.ID})
	wantErr(t, err, http.StatusNotFound, "not_found")
	if e.key(dicks.ID).Status != auth.KeyActive {
		t.Fatal("dick's key changed through bruce's /me")
	}
	// A status the route does not set.
	for _, s := range []string{"", "needs_confirmation", "deleted"} {
		if (&keyStatusReq{Body: KeyStatus{Status: s}}).Validate() == nil {
			t.Errorf("status %q validates", s)
		}
	}
}
