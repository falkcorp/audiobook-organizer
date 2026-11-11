// file: core/auth/middleware_test.go
// version: 1.0.0
// guid: d0f1edfd-530e-47d8-b256-7194b9109d0f
// last-edited: 2026-11-10

package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// seen is what the handler after stage 12 observed.
type seen struct {
	called  bool
	p       *auth.Principal
	fail    auth.Failure
	failed  bool
	checker rbac.Checker
	actor   audit.Actor
}

func serve(t *testing.T, svc *auth.Service, r *http.Request) (*httptest.ResponseRecorder, seen) {
	t.Helper()
	var got seen
	h := svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.called = true
		got.p, _ = auth.PrincipalFrom(r.Context())
		got.fail, got.failed = auth.FailureFrom(r.Context())
		got.checker, _ = rbac.CheckerFrom(r.Context())
		got.actor, _ = audit.ActorFrom(r.Context())
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, got
}

func request(bearer, cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/gotham/cases", nil)
	r.RemoteAddr = "192.0.2.10:44123"
	r.Header.Set("User-Agent", "Batcomputer/7")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.DefaultSessionCookie, Value: cookie})
	}
	return r
}

func (f *fx) withExtensions(t *testing.T, exts ...auth.Extension) (*auth.Service, error) {
	t.Helper()
	return auth.New(context.Background(), auth.Options{ImportedKeyPrefixes: []string{"abk_"}}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Extensions: exts,
	})
}

func (f *fx) issue(t *testing.T, u *auth.User) *auth.IssuedSession {
	t.Helper()
	is, err := f.svc.IssueSession(context.Background(), u, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return is
}

// SEC-12: a presented invalid key never falls through to the valid session
// beside it; the request is refused (stage 13's 401), not served as the
// session's user.
func TestStage12_InvalidKeyPlusValidSessionIs401(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	is := f.issue(t, dick)
	forged, err := auth.MintCredential(auth.KindAPIKey) // well formed, never stored
	if err != nil {
		t.Fatal(err)
	}
	broken := forged[:len(forged)-1] + "X"
	for name, key := range map[string]string{"unknown key": forged, "bad checksum": broken, "abk_ never imported": "abk_" + strings.Repeat("Q", 43)} {
		w, got := serve(t, f.svc, request(key, is.Token))
		if !got.called || w.Code != http.StatusOK {
			t.Fatalf("%s: stage 12 wrote %d", name, w.Code)
		}
		if got.p != nil {
			t.Fatalf("%s: an invalid key plus a valid session bound %s", name, got.p.Method)
		}
		if !got.failed || got.fail.Code != auth.CodeInvalidCredential || got.fail.Cookie {
			t.Fatalf("%s: failure %+v (recorded %v), want invalid_credential from the header", name, got.fail, got.failed)
		}
	}
}

// §4.2 item 2: a valid key wins over a valid session, in the header or as
// the cookie's value.
func TestAPIKey_PresentedKeyWins(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	is := f.issue(t, dick)
	raw, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	for name, r := range map[string]*http.Request{
		"key header, session cookie": request(raw, is.Token),
		"session bearer, key cookie": request(is.Token, raw),
	} {
		_, got := serve(t, f.svc, r)
		if got.p == nil || got.p.Method != auth.MethodAPIKey || got.p.CredentialID != k.ID {
			t.Fatalf("%s: bound %+v, want the key", name, got.p)
		}
		if got.checker == nil || got.checker.Has("gotham:cases.read") || !got.checker.Has("gotham:patrol") {
			t.Fatalf("%s: the bound checker is not the key's scopes", name)
		}
		if got.actor.CredentialID != k.ID || got.actor.UserID != string(dick.ID) || got.actor.ClientAddr != "192.0.2.10" {
			t.Fatalf("%s: audit actor %+v", name, got.actor)
		}
	}
}

// Sessions from the cookie or as Bearer bind an interactive principal; a
// stale cookie on its own is a cookie failure (§4.3 cookie rule).
func TestStage12_Sessions(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	is := f.issue(t, dick)
	for name, r := range map[string]*http.Request{"cookie": request("", is.Token), "bearer": request(is.Token, "")} {
		_, got := serve(t, f.svc, r)
		if got.p == nil || got.p.Method != auth.MethodSession || got.p.CredentialID != is.PublicID || !got.checker.Has("gotham:cases.read") {
			t.Fatalf("%s: bound %+v", name, got.p)
		}
	}
	stale, _ := auth.MintCredential(auth.KindSession)
	_, got := serve(t, f.svc, request("", stale))
	if got.p != nil || !got.failed || got.fail.Code != auth.CodeInvalidCredential || !got.fail.Cookie {
		t.Fatalf("stale cookie: principal %v failure %+v", got.p, got.fail)
	}
	_, got = serve(t, f.svc, request("", ""))
	if got.p != nil || got.failed {
		t.Fatalf("no credential: principal %v failure %+v", got.p, got.fail)
	}
}

// Each refusal records its code: a revoked key, an expired session (deleted
// on read), a disabled user, and a session of an older credential epoch.
func TestStage12_RefusalsRecordTheirCode(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick", "gotham.detective")
	raw, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
		t.Fatal(err)
	}
	if _, got := serve(t, f.svc, request(raw, "")); got.fail.Code != auth.CodeRevoked {
		t.Fatalf("revoked key: %+v", got.fail)
	}

	old := f.issue(t, dick)
	f.advance(auth.DefaultSessionTTL + time.Second)
	if _, got := serve(t, f.svc, request("", old.Token)); got.fail.Code != auth.CodeExpired || !got.fail.Cookie {
		t.Fatalf("expired session: %+v", got.fail)
	}
	if _, err := f.st.SessionByPublicID(ctx, old.PublicID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the expired session was not pruned on read: %v", err)
	}

	is := f.issue(t, dick)
	u := f.reload(dick.ID)
	u.CredentialEpoch, u.Version = u.CredentialEpoch+1, u.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	if _, got := serve(t, f.svc, request(is.Token, "")); got.fail.Code != auth.CodeRevoked {
		t.Fatalf("epoch-stale session: %+v", got.fail)
	}

	jason := f.user("jason", "gotham.detective")
	jraw, _ := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	u = f.reload(jason.ID)
	u.Status, u.Version = auth.StatusDisabled, u.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	if _, got := serve(t, f.svc, request(jraw, "")); got.fail.Code != auth.CodeInactiveUser {
		t.Fatalf("disabled user's key: %+v", got.fail)
	}
}

// An app's imported key (ABO's abk_) verifies by the SHA-256 the app's rows
// held, without re-issue (§4.1, §11).
func TestImportedKey_AbkVerifies(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	raw := "abk_" + base64.RawURLEncoding.EncodeToString(buf)
	f.commit(func(b *storage.Batch) error {
		return f.st.ImportAPIKey(b, &auth.APIKey{
			ID: "key-oracle", Owner: dick.ID, Name: "Oracle relay", Scopes: []rbac.Permission{"gotham:patrol"},
			Status: auth.KeyActive, CreatedAt: f.clock(), ExpiresAt: f.clock().Add(time.Hour), Imported: true,
		}, auth.HashCredential(raw))
	})
	_, got := serve(t, f.svc, request(raw, ""))
	if got.p == nil || got.p.Method != auth.MethodAPIKey || got.p.CredentialID != "key-oracle" {
		t.Fatalf("imported key bound %+v (failure %+v)", got.p, got.fail)
	}
}

// With no user yet stage 12 binds nothing and records nothing, whatever is
// presented: stage 13 then admits only Public and setup routes (§4.2 item 1).
func TestFirstRun_OnlyPublicAndSetupReachable(t *testing.T) {
	f := newFx(t)
	key, _ := auth.MintCredential(auth.KindAPIKey)
	_, got := serve(t, f.svc, request(key, ""))
	if !got.called || got.p != nil || got.failed {
		t.Fatalf("first run: principal %v failure %v", got.p, got.failed)
	}
}

// failingStore fails every credential read: stage 12 answers 503
// auth_unavailable and never serves the request anonymous (SEC-12).
type failingStore struct{ auth.Store }

var errDiskOnFire = errors.New("the Batcave's disks are on fire")

func (failingStore) APIKeyByToken(context.Context, string) (*auth.APIKey, error) {
	return nil, errDiskOnFire
}

func (failingStore) SessionByToken(context.Context, string) (*auth.SessionRecord, error) {
	return nil, errDiskOnFire
}

func TestStage12_StoreErrorIs503(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	is := f.issue(t, dick)
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: failingStore{f.st}, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := auth.MintCredential(auth.KindAPIKey)
	for name, r := range map[string]*http.Request{"key": request(key, ""), "session": request("", is.Token)} {
		w, got := serve(t, svc, r)
		if got.called || w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"auth_unavailable"`) {
			t.Fatalf("%s: called=%v status=%d body=%s", name, got.called, w.Code, w.Body)
		}
	}
}

// Last use is written at most once a minute per credential.
func TestStage12_TouchCoalescedPerMinute(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	raw, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	for range 3 {
		serve(t, f.svc, request(raw, ""))
	}
	if got := f.key(k.ID).UseCount; got != 1 {
		t.Fatalf("three requests in a minute wrote %d last uses, want 1", got)
	}
	f.advance(time.Minute)
	serve(t, f.svc, request(raw, ""))
	if got := f.key(k.ID); got.UseCount != 2 || !got.LastUsedAt.Equal(f.clock()) || got.LastUsedIP.String() != "192.0.2.10" {
		t.Fatalf("after a minute: %+v", got)
	}
}

// fakeExt is an identity extension asserting a fixed result.
type fakeExt struct {
	a   *auth.Assertion
	err error
}

func (fakeExt) Name() string { return "signal" }
func (e fakeExt) Authenticate(*http.Request) (*auth.Assertion, error) {
	return e.a, e.err
}

// SEC2-16: an extension proves who the caller is, never what it may do. The
// checker is compiled from the user's roles (narrowed by asserted scopes),
// Owner counts only for an OwnerVerifier, and an extension's refusal stops
// the sequence before the session.
func TestStage12_ExtensionPermsIgnored(t *testing.T) {
	f := newFx(t)
	barbara := f.user("barbara", rbac.RoleViewer)
	is := f.issue(t, barbara)
	ext := func(a *auth.Assertion, err error, owner bool) auth.Extension {
		e := auth.Extension{Authenticator: fakeExt{a: a, err: err}, Order: 150,
			Spec: auth.MethodSpec{Method: "ext:signal", Interactive: true, OwnerVerifier: owner}}
		if owner {
			e.OwnerSet = func() bool { return true }
		}
		return e
	}
	svc, err := f.withExtensions(t, ext(&auth.Assertion{User: barbara.ID, Owner: true, CredentialID: "cf-1"}, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	_, got := serve(t, svc, request("", ""))
	if got.p == nil || got.p.Method != "ext:signal" || got.p.Assurance != auth.AssuranceNone ||
		!got.checker.Has("gotham:patrol") || got.checker.Has("gotham:signal") || got.checker.Has(rbac.CoreAdmin) {
		t.Fatalf("extension principal %+v", got.p)
	}
	svc, _ = f.withExtensions(t, ext(&auth.Assertion{User: barbara.ID, Owner: true}, nil, true))
	if _, got := serve(t, svc, request("", "")); got.p == nil || got.p.Assurance&auth.AssuranceOwner == 0 {
		t.Fatalf("an OwnerVerifier's owner assertion was dropped: %+v", got.p)
	}
	svc, _ = f.withExtensions(t, ext(nil, auth.ErrExpired, false))
	if _, got := serve(t, svc, request("", is.Token)); got.p != nil || got.fail.Code != auth.CodeExpired {
		t.Fatalf("an extension's refusal fell through to the session: %+v %+v", got.p, got.fail)
	}
	svc, _ = f.withExtensions(t, ext(nil, auth.ErrNoCredential, false))
	if _, got := serve(t, svc, request("", is.Token)); got.p == nil || got.p.Method != auth.MethodSession {
		t.Fatalf("ErrNoCredential did not pass on to the session: %+v", got.p)
	}
}

// §3.3: only ext:<name> methods register as extensions, OwnerSet comes with
// OwnerVerifier, no app hand-off, no core order, no duplicate.
func TestExtension_RegistrationRules(t *testing.T) {
	f := newFx(t)
	ok := auth.Extension{Authenticator: fakeExt{}, Spec: auth.MethodSpec{Method: "ext:signal"}, Order: 150}
	for name, e := range map[string]auth.Extension{
		"no authenticator":       {Spec: ok.Spec, Order: 150},
		"module-shaped method":   {Authenticator: fakeExt{}, Spec: auth.MethodSpec{Method: "gotham.abs"}, Order: 150},
		"owner without OwnerSet": {Authenticator: fakeExt{}, Spec: auth.MethodSpec{Method: "ext:signal", OwnerVerifier: true}, Order: 150},
		"core order":             {Authenticator: fakeExt{}, Spec: ok.Spec, Order: auth.OrderSession},
	} {
		if _, err := f.withExtensions(t, e); !errors.Is(err, auth.ErrMethodSpec) {
			t.Errorf("%s: New = %v, want ErrMethodSpec", name, err)
		}
	}
	if _, err := f.withExtensions(t, ok, ok); !errors.Is(err, auth.ErrMethodSpec) {
		t.Errorf("duplicate method: %v", err)
	}
	if _, err := f.withExtensions(t, ok); err != nil {
		t.Errorf("a well-formed extension was refused: %v", err)
	}
}
