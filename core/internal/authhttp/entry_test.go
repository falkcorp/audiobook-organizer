// file: core/internal/authhttp/entry_test.go
// version: 1.0.0
// guid: c8d1f503-2b79-4e46-a0c3-6f94e7b15d82
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

func decode[T any](t testing.TB, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, w.Body.String())
	}
	return v
}

func entryHandler(t *testing.T, e *env) http.Handler {
	t.Helper()
	return publicHandler(t, e.api(), httpapi.LimitOptions{})
}

func TestStatus_SaysOnlyWhetherThereAreUsers(t *testing.T) {
	e := newEnv(t)
	st := decode[Status](t, serve(entryHandler(t, e), "GET", core+"/auth/status", nil))
	if st.HasUsers || !st.SetupRequired || st.AuthDisabled {
		t.Fatalf("empty store: %+v", st)
	}
	e.user("bruce", "brucebruce", rbac.RoleAdmin)
	st = decode[Status](t, serve(entryHandler(t, e), "GET", core+"/auth/status", nil))
	if !st.HasUsers || st.SetupRequired {
		t.Fatalf("with a user: %+v", st)
	}
}

// A sign-in sets the cookie and returns no token in its body: an echoed token
// would hand the page's script a copy of the credential HttpOnly withholds.
func TestLogin_SetsTheCookieAndReturnsNoToken(t *testing.T) {
	e := newEnv(t)
	e.user("bruce", "brucebruce", rbac.RoleAdmin)
	h := entryHandler(t, e)
	w := serve(h, "POST", core+"/auth/login", LoginRequest{Username: "bruce", Password: "brucebruce"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	ck := cookieNamed(w, sessionCookieName(e))
	if ck == nil || !strings.HasPrefix(ck.Value, "mcs_") || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode && ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie %+v", ck)
	}
	if strings.Contains(w.Body.String(), ck.Value) || strings.Contains(w.Body.String(), "mcs_") || strings.Contains(strings.ToLower(w.Body.String()), "token") {
		t.Fatalf("the body carries a token: %s", w.Body.String())
	}
	var keys map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["public_id"]; !ok || len(keys) != 2 {
		t.Fatalf("body keys %v, want public_id and expires_at", keys)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a credential-setting response is cacheable")
	}
	// The cookie opens a session whose public ID the body names.
	pid := keys["public_id"].(string)
	if !slices.Contains(e.sessionIDs("u-bruce"), pid) {
		t.Fatalf("no session %s for bruce", pid)
	}
}

// An unknown name, a wrong password and an account that has no password are
// one answer: nothing tells a caller which names exist.
func TestLogin_SameAnswerWhetherTheAccountExistsOrNot(t *testing.T) {
	e := newEnv(t)
	e.user("bruce", "brucebruce", rbac.RoleAdmin)
	e.user("selina", "", rbac.RoleViewer) // no password
	h := entryHandler(t, e)
	var first *httptest.ResponseRecorder
	for _, c := range []LoginRequest{
		{Username: "bruce", Password: "wrong"}, {Username: "nobody", Password: "wrong"}, {Username: "selina", Password: "wrong"},
	} {
		w := serve(h, "POST", core+"/auth/login", c)
		wantProblem(t, w, 401, "invalid_credentials")
		if len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s: a refused sign-in set a cookie", c.Username)
		}
		if first == nil {
			first = w
			continue
		}
		var a, b map[string]any
		_ = json.Unmarshal(first.Body.Bytes(), &a)
		_ = json.Unmarshal(w.Body.Bytes(), &b)
		delete(a, "request_id")
		delete(b, "request_id")
		delete(a, "instance")
		delete(b, "instance")
		if !jsonEqual(a, b) {
			t.Fatalf("%s: %v differs from %v", c.Username, b, a)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestLogin_RefusesAMalformedBody(t *testing.T) {
	e := newEnv(t)
	h := entryHandler(t, e)
	if w := serve(h, "POST", core+"/auth/login", nil); w.Code < 400 || w.Code >= 500 {
		t.Fatalf("no body: %d", w.Code)
	}
	w := serve(h, "POST", core+"/auth/login", nil, func(r *http.Request) {
		r.Body = httptest.NewRequest("POST", "/", strings.NewReader(`{"username":`)).Body
		r.Header.Set("Content-Type", "application/json")
	})
	if w.Code != 400 {
		t.Fatalf("truncated body: %d %s", w.Code, w.Body.String())
	}
	w = serve(h, "POST", core+"/auth/login", nil, func(r *http.Request) {
		r.Body = httptest.NewRequest("POST", "/", strings.NewReader(`{"username":"a","password":"b","admin":true}`)).Body
		r.Header.Set("Content-Type", "application/json")
	})
	if w.Code < 400 {
		t.Fatalf("an unknown field was accepted: %d", w.Code)
	}
}

// A throttle refusal is a 429 with Retry-After, never a 401 that would let a
// guesser tell a throttled source from a wrong password.
func TestLogin_ThrottleAnswers429WithRetryAfter(t *testing.T) {
	e := newEnv(t)
	a := e.api(func(d *Deps) { d.Entry = refusing{Entry: d.Entry, err: &auth.ThrottledError{RetryAfter: 7}} })
	h := publicHandler(t, a, httpapi.LimitOptions{})
	w := serve(h, "POST", core+"/auth/login", LoginRequest{Username: "bruce", Password: "x"})
	wantProblem(t, w, 429, "throttled")
	if w.Header().Get("Retry-After") != "7" {
		t.Fatalf("Retry-After %q", w.Header().Get("Retry-After"))
	}
}

// refusing is an Entry whose sign-in calls fail with err.
type refusing struct {
	Entry
	err error
}

func (r refusing) Login(context.Context, auth.LoginInput) (*auth.IssuedSession, error) {
	return nil, r.err
}

func TestLogout_EndsTheSessionAndClearsTheCookie(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	p, _ := e.withSession(bruce)
	a := e.api()
	h := a.rawHandler(specLogout(), a.logout)
	w := serve(h, "POST", core+"/auth/logout", nil, asPrincipal(p))
	if w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	ck := cookieNamed(w, sessionCookieName(e))
	if ck == nil || ck.Value != "" || ck.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", ck)
	}
	if slices.Contains(e.sessionIDs(bruce.ID), p.CredentialID) {
		t.Fatal("the session survives logout")
	}
	// Logging out twice still clears the cookie: the session being gone is
	// what logout wants.
	if w := serve(h, "POST", core+"/auth/logout", nil, asPrincipal(p)); w.Code != 204 {
		t.Fatalf("second logout: %d %s", w.Code, w.Body.String())
	}
	// No credential at all: 401, and the route is not a Public one.
	wantProblem(t, serve(h, "POST", core+"/auth/logout", nil), 401, "authentication_required")
}

// A key principal that calls logout revokes nothing: its CredentialID is a key,
// not a session.
func TestLogout_WithAnAPIKeyRevokesNothing(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	p, _ := e.withSession(bruce)
	_, k := e.mint(p, bruce.ID, "core:self")
	kp := e.principal(bruce, auth.MethodAPIKey, "core:self")
	kp.CredentialID = k.ID
	a := e.api()
	w := serve(a.rawHandler(specLogout(), a.logout), "POST", core+"/auth/logout", nil, asPrincipal(kp))
	if w.Code != 204 {
		t.Fatalf("%d", w.Code)
	}
	if got := e.key(k.ID); got.Status != auth.KeyActive {
		t.Fatalf("logout through a key changed the key to %s", got.Status)
	}
}

func (e *env) mint(actor *auth.Principal, owner auth.UserID, scopes ...rbac.Permission) (string, *auth.APIKey) {
	e.t.Helper()
	raw, k, err := e.svc.CreateAPIKey(context.Background(), actor, auth.KeySpec{Owner: owner, Name: "utility belt", Scopes: scopes})
	if err != nil {
		e.t.Fatalf("CreateAPIKey %v: %v", scopes, err)
	}
	return raw, k
}

func (e *env) key(id string) *auth.APIKey {
	e.t.Helper()
	k, err := e.st.APIKeyByID(context.Background(), id)
	if err != nil {
		e.t.Fatalf("APIKeyByID %s: %v", id, err)
	}
	return k
}

// A closed setup is one 403 whatever the reason: no token where one is needed,
// or a user already there. A wrong token is the bootstrap's own 401, and the
// presented token is never echoed.
func TestSetup_ClosedIsOneAnswerAndAWrongTokenIsBootstrapInvalid(t *testing.T) {
	e := newEnv(t)
	h := entryHandler(t, e)
	w1 := serve(h, "POST", core+"/auth/setup", SetupRequest{Username: "bruce", Password: "correct horse battery"})
	wantProblem(t, w1, 403, "setup_closed")
	w := serve(h, "POST", core+"/auth/setup", SetupRequest{Username: "bruce", Password: "correct horse battery", BootstrapToken: "mcb_nope"})
	wantProblem(t, w, 401, "bootstrap_invalid")
	if strings.Contains(w.Body.String(), "mcb_nope") || strings.Contains(w.Body.String(), "correct horse") {
		t.Fatalf("a secret is echoed: %s", w.Body.String())
	}
	e.user("alfred", "alfredalfred", rbac.RoleAdmin)
	w2 := serve(entryHandler(t, e), "POST", core+"/auth/setup", SetupRequest{Username: "bruce", Password: "correct horse battery", BootstrapToken: "mcb_nope"})
	wantProblem(t, w2, 403, "setup_closed")
	var a, b map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &a)
	_ = json.Unmarshal(w2.Body.Bytes(), &b)
	if a["detail"] != b["detail"] {
		t.Fatalf("details differ: %v vs %v", a["detail"], b["detail"])
	}
}

func TestSetup_LocalFirstAdminIsCreatedAndOpensNoSession(t *testing.T) {
	e := newEnv(t)
	e.setupNeedsToken = false
	h := entryHandler(t, e)
	w := serve(h, "POST", core+"/auth/setup", SetupRequest{Username: "bruce", Password: "correct horse battery"}, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:4000" })
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	u := decode[User](t, w)
	if u.Username != "bruce" || len(u.Roles) != 1 || u.Roles[0] != string(rbac.RoleAdmin) || !u.HasPassword {
		t.Fatalf("%+v", u)
	}
	if len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "correct horse") {
		t.Fatalf("setup set a cookie or echoed the password: %v %s", w.Result().Cookies(), w.Body.String())
	}
	// Setup is now closed, even locally.
	h = entryHandler(t, e)
	wantProblem(t, serve(h, "POST", core+"/auth/setup", SetupRequest{Username: "robin", Password: "correct horse battery"}, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:4000" }), 403, "setup_closed")
}

func TestBootstrap_AWrongTokenIsOneAnswer(t *testing.T) {
	e := newEnv(t)
	h := entryHandler(t, e)
	w := serve(h, "POST", core+"/auth/bootstrap", BootstrapRequest{Token: "mcb_not_a_token"})
	wantProblem(t, w, 401, "bootstrap_invalid")
	if strings.Contains(w.Body.String(), "mcb_not_a_token") {
		t.Fatal("the presented token is echoed")
	}
}

// The invite link is a fragment: only its token half reaches a server, and it
// reaches it in a body. A lookup previews the account, an accept creates it and
// opens a session, a replay is the same refusal as garbage.
func TestInviteLink_FragmentEndToEnd(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.api()
	h := publicHandler(t, a, httpapi.LimitOptions{})
	created, err := guard(specInviteNew(), a.createInvite)(context.Background(), in(e.session(bruce), inviteCreateReq{Body: InviteCreate{Username: "dick", Email: "dick@example.test", Role: string(roleDetective)}}))
	if err != nil {
		t.Fatal(err)
	}
	link := created.Body.Link
	token, ok := strings.CutPrefix(link, "/invite#token=")
	if !ok || token == "" {
		t.Fatalf("link %q is not /invite#token=...", link)
	}
	if strings.Contains(created.Location, token) {
		t.Fatal("the Location header carries the token")
	}
	if b, _ := json.Marshal(created.Body.Invite); strings.Contains(string(b), token) {
		t.Fatal("the invite record carries the token")
	}
	pv := decode[InvitePreview](t, serve(h, "POST", core+"/auth/invites/lookup", InviteLookup{Token: token}))
	if pv.Username != "dick" || pv.Role != string(roleDetective) {
		t.Fatalf("preview %+v", pv)
	}
	w := serve(h, "POST", core+"/auth/invites/accept", InviteAccept{Token: token, Password: "correct horse battery"})
	if w.Code != 200 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	if ck := cookieNamed(w, sessionCookieName(e)); ck == nil || !strings.HasPrefix(ck.Value, "mcs_") {
		t.Fatalf("accept set no session cookie: %+v", ck)
	}
	opened := decode[SessionOpened](t, w)
	if opened.User == nil || opened.User.Username != "dick" {
		t.Fatalf("%+v", opened)
	}
	replay := serve(h, "POST", core+"/auth/invites/accept", InviteAccept{Token: token, Password: "correct horse battery"})
	garbage := serve(h, "POST", core+"/auth/invites/accept", InviteAccept{Token: "mci_garbage", Password: "correct horse battery"})
	wantProblem(t, replay, 401, "link_invalid")
	wantProblem(t, garbage, 401, "link_invalid")
	wantProblem(t, serve(h, "POST", core+"/auth/invites/lookup", InviteLookup{Token: token}), 401, "link_invalid")
}

// An invite is checked again when it is accepted: its creator must still be
// able to assign its role (SEC3-2). A commissioner who is demoted after
// issuing it never yields an account in a role the commissioner can no longer
// assign.
func TestInviteAcceptRechecksCanAssign(t *testing.T) {
	e := newEnv(t)
	e.user("bruce", "brucebruce", rbac.RoleAdmin)
	gordon := e.user("gordon", "gordongordon", roleCommissioner)
	a := e.api()
	h := publicHandler(t, a, httpapi.LimitOptions{})
	created, err := guard(specInviteNew(), a.createInvite)(context.Background(), in(e.session(gordon), inviteCreateReq{Body: InviteCreate{Username: "dick", Role: string(roleDetective)}}))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.Body.Link, "/invite#token=")
	// Gordon is demoted behind the service's back, as an admin would.
	cur := e.reload(gordon.ID)
	cur.Roles, cur.Version = []rbac.RoleName{rbac.RoleViewer}, cur.Version+1
	e.commit(func(b *storageBatch) error { return e.st.PutUser(b, cur) })
	w := serve(h, "POST", core+"/auth/invites/accept", InviteAccept{Token: token, Password: "correct horse battery"})
	wantProblem(t, w, 401, "link_invalid")
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a refused accept set a cookie")
	}
	if _, err := e.st.UserByUsername(context.Background(), "dick"); err == nil {
		t.Fatal("the account was created")
	}
	var denied bool
	for _, e := range e.audit.recordedAndEmitted() {
		denied = denied || (e.Action == "core.invite.accept" && e.Outcome == audit.Denied)
	}
	if !denied {
		t.Fatalf("no core.invite.accept denied event: %v %v", e.audit.recordedActions(), e.audit.emittedActions())
	}
}

// A temp-login link is a fragment too, and spends once.
func TestTempLoginLink_FragmentEndToEnd(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	e.user("dick", "dickdick", roleDetective)
	a := e.api()
	h := publicHandler(t, a, httpapi.LimitOptions{})
	l, err := guard(specTemp(), a.link(specTemp(), a.Accounts.MintTempLogin))(context.Background(), in(e.session(bruce), userReq{ID: "u-dick"}))
	if err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(l.Link, "/login/temp#token=")
	if !ok || token == "" {
		t.Fatalf("link %q is not /login/temp#token=...", l.Link)
	}
	w := serve(h, "POST", core+"/auth/temp-login/consume", TempLoginConsume{Token: token})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if ck := cookieNamed(w, sessionCookieName(e)); ck == nil || !strings.HasPrefix(ck.Value, "mcs_") {
		t.Fatalf("no session cookie: %+v", ck)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("the body carries the link's token")
	}
	again := serve(h, "POST", core+"/auth/temp-login/consume", TempLoginConsume{Token: token})
	bad := serve(h, "POST", core+"/auth/temp-login/consume", TempLoginConsume{Token: "mct_garbage"})
	wantProblem(t, again, 401, "link_invalid")
	wantProblem(t, bad, 401, "link_invalid")
}

// A reset link needs a new password; the consumption sets it, ends every other
// session, and the old password no longer signs in.
func TestResetLink_SetsThePasswordAndEndsOtherSessions(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	dick := e.user("dick", "dickdick", roleDetective)
	oldSession, _ := e.withSession(dick)
	a := e.api()
	h := publicHandler(t, a, httpapi.LimitOptions{})
	l, err := guard(specReset(), a.link(specReset(), a.Accounts.MintResetLink))(context.Background(), in(e.session(bruce), userReq{ID: string(dick.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(l.Link, "/login/temp#token=")
	w := serve(h, "POST", core+"/auth/temp-login/consume", TempLoginConsume{Token: token, NewPassword: "a brand new passphrase"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "brand new") {
		t.Fatal("the new password is echoed")
	}
	wantProblem(t, serve(h, "POST", core+"/auth/login", LoginRequest{Username: "dick", Password: "dickdick"}), 401, "invalid_credentials")
	if w := serve(h, "POST", core+"/auth/login", LoginRequest{Username: "dick", Password: "a brand new passphrase"}); w.Code != 200 {
		t.Fatalf("new password: %d %s", w.Code, w.Body.String())
	}
	// The reset moved the credential epoch, which is what makes every session
	// that predates it (stage 12 compares) refused.
	if after := e.reload(dick.ID); after.CredentialEpoch <= oldSession.User.CredentialEpoch {
		t.Fatalf("credential epoch %d did not move past %d", after.CredentialEpoch, oldSession.User.CredentialEpoch)
	}
}

// fail maps every sentinel the Service returns to the status and code of
// 05-authn §6, and hides the rest.
func TestFail_MapsEverySentinel(t *testing.T) {
	a := newEnv(t).api()
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrInvalidCredentials, 401, "invalid_credentials"},
		{auth.ErrResetRequired, 401, "password_reset_required"},
		{auth.ErrInactiveUser, 401, "inactive_user"},
		{auth.ErrBootstrapInvalid, 401, "bootstrap_invalid"},
		{auth.ErrSetupClosed, 403, "setup_closed"},
		{auth.ErrInviteInvalid, 400, "invite_invalid"},
		{auth.ErrDominance, 403, "dominance_required"},
		{auth.ErrScopeEscalation, 403, "scope_escalation"},
		{auth.ErrDenied, 403, "permission_denied"},
		{auth.ErrLastAdmin, 409, "last_admin"},
		{auth.ErrKeyRevoked, 409, "key_revoked"},
		{auth.ErrUserExists, 409, "user_exists"},
		{auth.ErrStaleEpoch, 409, "stale_account"},
		{auth.ErrNotFound, 404, "not_found"},
		{auth.ErrBadInput, 400, "bad_request"},
		{auth.ErrBusy, 503, "busy"},
		{auth.ErrStoreUnavailable, 503, "auth_unavailable"},
		{auth.ErrThrottled, 429, "throttled"},
	} {
		wrapped := errors.Join(errors.New("detail naming user u-bruce and key mck_secret"), c.err)
		got := a.fail(context.Background(), specLogin(), nil, "", wrapped)
		wantErr(t, got, c.status, c.code)
		var he *httpapi.Error
		errors.As(got, &he)
		if strings.Contains(he.Detail, "mck_secret") || strings.Contains(he.Detail, "u-bruce") {
			t.Errorf("%v: the Service's detail reached the response: %q", c.err, he.Detail)
		}
	}
	unmapped := errors.New("pebble: disk on fire at /var/lib/x")
	if got := a.fail(context.Background(), specLogin(), nil, "", unmapped); !errors.Is(got, unmapped) {
		t.Fatalf("an unmapped error is wrapped or lost: %v", got)
	}
	_ = time.Second
}
