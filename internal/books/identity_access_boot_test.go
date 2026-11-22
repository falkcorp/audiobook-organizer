// file: internal/books/identity_access_boot_test.go
// version: 0.1.0
// guid: 2f6b9c43-7e1a-4d58-b0c9-a35e8d14f726
// last-edited: 2026-11-21

package books

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// The ABS surface's Cloudflare Access sign-in under core identity (F4-08 B10n),
// through a real composed boot: core's kernel, books' Register, the ABS routes,
// and a fake Cloudflare Access verifier bound into the late handle the ABS
// surface now shares with the Access extension. The person is resolved by core's
// account resolver under books' Access admission policy, never through books'
// user store.

const (
	accessTeam = "batcave.example.test"
	accessAUD  = "cf-aud-gotham"

	accessAlfred = "alfred-assertion"
	accessJoker  = "joker-assertion"
	accessUnver  = "unverified-assertion"
	accessSvc    = "service-token-assertion"

	accessVerifierPKCE = "audiobooth-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
)

// fakeAccessVerifier is the cfaccess.CFVerifier a test binds in place of the
// network verifier: it accepts exactly the assertions it was given.
type fakeAccessVerifier struct {
	mu     sync.Mutex
	byTok  map[string]*legacyoauth.IdentityClaims
	svcTok map[string]string // assertion -> service token common_name
	calls  int
}

func newFakeAccessVerifier() *fakeAccessVerifier {
	f := &fakeAccessVerifier{byTok: map[string]*legacyoauth.IdentityClaims{}, svcTok: map[string]string{accessSvc: "family"}}
	person := func(sub, email string, verified bool) *legacyoauth.IdentityClaims {
		return &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: sub, Email: email, EmailVerified: verified}
	}
	f.byTok[accessAlfred] = person("cf|alfred", "alfred@example.test", true)
	f.byTok[accessJoker] = person("cf|joker", "joker@example.test", true)
	f.byTok[accessUnver] = person("cf|alfred-2", "alfred@example.test", false)
	return f
}

func (f *fakeAccessVerifier) Verify(_ context.Context, raw string) (*legacyoauth.IdentityClaims, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if cn, ok := f.svcTok[raw]; ok {
		return nil, &cfaccess.NonIdentityAssertionError{Subject: "svc", CommonName: cn}
	}
	if c, ok := f.byTok[raw]; ok {
		return c, nil
	}
	return nil, errors.New("fake: the signature is invalid")
}

func (f *fakeAccessVerifier) VerifyIssued(ctx context.Context, raw string) (*cfaccess.VerifiedAssertion, error) {
	c, err := f.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &cfaccess.VerifiedAssertion{Claims: c, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, nil
}

func (f *fakeAccessVerifier) verifyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sha256Challenge is the S256 PKCE challenge of a verifier.
func sha256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// bootAccess is the identity flip with the ABS surface on in modes cf and jwt,
// the Access application set (so the ABS verifier branch is the one taken),
// alfred on the allow-list and, when fake is not nil, the late verifier rebound to
// it; nil leaves the verifier books bound from the config (the network one).
func bootAccess(t *testing.T, fake *fakeAccessVerifier, edit func(*config.Config)) *runningApp {
	t.Helper()
	t.Cleanup(SetIdentityFlipForTest(true))
	t.Setenv("ABS_OIDC_REDIRECT_URIS", "")
	// Sign-in settings are operator-only: their layer is the environment, under
	// the canonical names the flip's rename map gives HEAD's.
	t.Setenv("ABO_BOOKS_ABS_AUTH_MODES", "cf,jwt")
	t.Setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_TEAM_DOMAIN", accessTeam)
	t.Setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_AUD", accessAUD)
	t.Setenv("ABO_BOOKS_CFACCESS_ALLOWED_EMAILS", "alfred@example.test")
	return runComposedWith(t, func(c *config.Config) {
		c.EnableAuth = true
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-composed-app-secret-0123456789abcdef"
		c.ABSAuthModes = "cf,jwt"
		c.CFAccessTeamDomain = accessTeam
		c.CFAccessAUD = accessAUD
		c.OAuthAllowedEmails = "alfred@example.test"
		if edit != nil {
			edit(c)
		}
	}, nil, nil, func(c *composed) {
		if fake != nil {
			require.NoError(t, c.mod.idw.verifier.Bind(fake))
		}
	})
}

// absGet is one request to the ABS surface through the root handler.
func absGet(t *testing.T, r *runningApp, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.0.2.10:41000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// absMe is GET /api/me with the assertion; the body's user id is core's.
func absMe(t *testing.T, r *runningApp, assertion string) (int, string, string) {
	t.Helper()
	rec := absGet(t, r, "/api/me", map[string]string{cfaccess.CFAccessHeader: assertion})
	var body struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	}
	return rec.Code, body.ID, rec.Body.String()
}

// coreAdmin is a signed-in core administrator (the bootstrap account): its
// request helper reaches core's own routes with the session cookie.
type coreAdmin struct {
	t      *testing.T
	r      *runningApp
	cookie *http.Cookie
}

func newCoreAdmin(t *testing.T, r *runningApp) *coreAdmin {
	t.Helper()
	user, pass := redeemBootstrap(t, r.composed)
	rec := post(t, r.app.Handler(), "/api/core/v1/auth/login", map[string]string{"username": user, "password": pass}, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	a := &coreAdmin{t: t, r: r}
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			a.cookie = ck
		}
	}
	require.NotNil(t, a.cookie, "no session cookie")
	return a
}

func (a *coreAdmin) do(method, path string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// users is core's user list as [id, email] pairs by id.
func (a *coreAdmin) users() map[string]string {
	a.t.Helper()
	rec := a.do(http.MethodGet, "/api/core/v1/users")
	require.Equal(a.t, http.StatusOK, rec.Code, rec.Body.String())
	var env struct {
		Data  []struct{ ID, Email string } `json:"data"`
		Items []struct{ ID, Email string } `json:"items"`
		Users []struct{ ID, Email string } `json:"users"`
	}
	require.NoError(a.t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	rows := append(append(env.Data, env.Items...), env.Users...)
	require.NotEmpty(a.t, rows, "core's user list: %s", rec.Body.String())
	out := map[string]string{}
	for _, u := range rows {
		out[u.ID] = u.Email
	}
	return out
}

// TestAccessBoot_ListedPersonResolvesToACoreUser: a verified person on the
// allow-list is admitted on an ABS route as the core user the Access resolver
// found or created: 200, mode cf, the same user on the next request, an account
// that exists in core's user list.
func TestAccessBoot_ListedPersonResolvesToACoreUser(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, fake, nil)
	admin := newCoreAdmin(t, r)
	before := admin.users()

	code, id, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, body)
	require.NotEmpty(t, id)
	require.Positive(t, fake.verifyCalls(), "the request never reached the bound verifier: the ABS surface built its own")

	after := admin.users()
	require.Equal(t, len(before)+1, len(after), "core did not gain exactly the person: %v -> %v", before, after)
	require.Equal(t, "alfred@example.test", after[id], "the response's user is not core's account for the person")

	code2, id2, body2 := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code2, body2)
	require.Equal(t, id, id2, "a second sign-in resolved to another user")
	require.Equal(t, len(after), len(admin.users()), "a second sign-in created another account")
}

// TestAccessBoot_PersonNotOnTheListIsRefusedAndCreatesNothing: not on the
// allow-list, or an unverified address, is HEAD's 403 and core gains no user.
func TestAccessBoot_PersonNotOnTheListIsRefusedAndCreatesNothing(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, fake, nil)
	admin := newCoreAdmin(t, r)
	before := admin.users()

	for name, tok := range map[string]string{"not listed": accessJoker, "unverified address": accessUnver} {
		code, _, body := absMe(t, r, tok)
		require.Equal(t, http.StatusForbidden, code, "%s: %s", name, body)
		require.Contains(t, body, "identity is not authorized for this server", name)
	}
	require.Equal(t, before, admin.users(), "a refused person created or changed a core account")
}

// TestAccessBoot_NonZeroEpochStillAdmits: the principal is admitted at the epoch
// of the record core resolved. The person's account goes through a disable and an
// enable (two epoch bumps), so a credential stamped with epoch 0 is stale against
// core's and 401s; the sign-in still works.
func TestAccessBoot_NonZeroEpochStillAdmits(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, fake, nil)
	admin := newCoreAdmin(t, r)
	code, id, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, body)

	rec := admin.do(http.MethodPost, "/api/core/v1/users/"+id+"/disable")
	require.Less(t, rec.Code, 300, rec.Body.String())
	code, _, body = absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusForbidden, code, "a disabled account must be refused: %s", body)
	rec = admin.do(http.MethodPost, "/api/core/v1/users/"+id+"/enable")
	require.Less(t, rec.Code, 300, rec.Body.String())

	code, id2, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, "after a disable and enable (epoch 2): %s", body)
	require.Equal(t, id, id2)
}

// TestAccessBoot_UnreboundHandleVerifiesWithTheNetworkVerifier: with no rebind
// the ABS surface verifies through the verifier books built from the config (the
// network one against the team's keys), not through anything a test seam put
// there: an assertion that is no signed token is refused as a forged one is
// (fail closed, 401), and the fake, never bound, is never asked.
func TestAccessBoot_UnreboundHandleVerifiesWithTheNetworkVerifier(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, nil, nil)
	for _, tok := range []string{accessAlfred, "not-a-jwt", "a.b.c"} {
		code, _, body := absMe(t, r, tok)
		require.Equal(t, http.StatusUnauthorized, code, "%q: %s", tok, body)
	}
	require.Zero(t, fake.verifyCalls())
}

var (
	accessConsentReqRe  = regexp.MustCompile(`name="req" value="([^"]+)"`)
	accessConsentCSRFRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
)

// TestAccessBoot_AppHandoffMintsACodeThatExchangesForTheCoreUser: the A7 flow
// (hop 1 the app's service token, hop 2 the browser's person assertion and the
// consent page, hop 3 the callback) after the identity binds: hop 2 mints a code
// for the core user the person resolves to, and the code exchanges for an ABS
// session for that user.
func TestAccessBoot_AppHandoffMintsACodeThatExchangesForTheCoreUser(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, fake, nil)
	admin := newCoreAdmin(t, r)
	before := admin.users()

	sum := sha256Challenge(accessVerifierPKCE)
	q := url.Values{}
	q.Set("client_id", "AudioBooth")
	q.Set("response_type", "code")
	q.Set("scope", "openid")
	q.Set("redirect_uri", "audiobooth://oauth")
	q.Set("code_challenge", sum)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "client-state-1")

	// Hop 1: the app's own client, service token only.
	hop1 := absGet(t, r, "/auth/openid?"+q.Encode(), map[string]string{cfaccess.CFAccessHeader: accessSvc})
	require.Equal(t, http.StatusFound, hop1.Code, hop1.Body.String())
	loc, err := url.Parse(hop1.Header().Get("Location"))
	require.NoError(t, err)
	browser := loc.RequestURI()

	// Hop 2: the browser, a person.
	nav := map[string]string{cfaccess.CFAccessHeader: accessAlfred, "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	page := absGet(t, r, browser, nav)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	reqID, csrf := accessConsentReqRe.FindStringSubmatch(page.Body.String()), accessConsentCSRFRe.FindStringSubmatch(page.Body.String())
	require.NotNil(t, reqID, page.Body.String())
	require.NotNil(t, csrf, page.Body.String())
	form := url.Values{"req": {reqID[1]}, "csrf": {csrf[1]}, "decision": {"allow"}}
	consent := httptest.NewRequest(http.MethodPost, "/auth/openid/browser", strings.NewReader(form.Encode()))
	consent.RemoteAddr = "192.0.2.10:41000"
	consent.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	consent.Header.Set("Origin", "https://example.com")
	consent.Header.Set(cfaccess.CFAccessHeader, accessAlfred)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, consent)
	require.Contains(t, []int{http.StatusFound, http.StatusSeeOther}, rec.Code, rec.Body.String())
	cb, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	code := cb.Query().Get("code")
	require.NotEmpty(t, code, "hop 2 delivered no code: %s", cb)

	// Hop 3: the app redeems it, service token only.
	exch := absGet(t, r, "/auth/openid/callback?code="+url.QueryEscape(code)+"&code_verifier="+url.QueryEscape(accessVerifierPKCE)+"&state=client-state-1",
		map[string]string{cfaccess.CFAccessHeader: accessSvc})
	require.Equal(t, http.StatusOK, exch.Code, exch.Body.String())
	var session struct {
		User struct {
			ID          string `json:"id"`
			AccessToken string `json:"accessToken"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(exch.Body.Bytes(), &session), exch.Body.String())
	require.NotEmpty(t, session.User.AccessToken, exch.Body.String())

	after := admin.users()
	require.Equal(t, len(before)+1, len(after), "core did not gain exactly the person")
	require.Equal(t, "alfred@example.test", after[session.User.ID], "the session is not for core's account of the person")

	// The token it issued works on an ABS route as that user.
	me := absGet(t, r, "/api/me", map[string]string{"Authorization": "Bearer " + session.User.AccessToken})
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())
	require.Contains(t, me.Body.String(), session.User.ID)
}
