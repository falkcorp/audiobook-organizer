// file: core/internal/authhttp/oidc_test.go
// version: 1.0.0
// guid: 5e2b8d03-7a16-4c94-b0f7-93d1a46c8e27
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/httpapi"
)

// fakeFlow stands in for auth/oidc's *Flow: what these tests pin is what the
// routes do with its answers (the cookie, the redirect, the status), not the
// Flow's own checks, which auth/oidc's tests own.
type fakeFlow struct {
	mu        sync.Mutex
	start     func(oidc.StartRequest) (*oidc.Started, error)
	callback  func(oidc.CallbackRequest) (*oidc.Completed, error)
	providers []oidc.ProviderInfo
	starts    []oidc.StartRequest
	callbacks []oidc.CallbackRequest
}

func (f *fakeFlow) Start(_ context.Context, r oidc.StartRequest) (*oidc.Started, error) {
	f.mu.Lock()
	f.starts = append(f.starts, r)
	f.mu.Unlock()
	return f.start(r)
}

func (f *fakeFlow) Callback(_ context.Context, r oidc.CallbackRequest) (*oidc.Completed, error) {
	f.mu.Lock()
	f.callbacks = append(f.callbacks, r)
	f.mu.Unlock()
	return f.callback(r)
}

func (f *fakeFlow) Providers() []oidc.ProviderInfo { return f.providers }

func (f *fakeFlow) calls() (starts, callbacks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts), len(f.callbacks)
}

func okStart(*testing.T) func(oidc.StartRequest) (*oidc.Started, error) {
	return func(oidc.StartRequest) (*oidc.Started, error) {
		return &oidc.Started{Location: "https://idp.example.test/authorize?state=s1", Binding: "signed-binding", MaxAge: 600}, nil
	}
}

func oidcHandler(t *testing.T, ff *fakeFlow, lim httpapi.LimitOptions) (http.Handler, *env) {
	t.Helper()
	e := newEnv(t)
	a := e.api(func(d *Deps) { d.Flow = ff; d.OIDCCallbackBase = headBase })
	return publicHandler(t, a, lim), e
}

// sessionCookieName is the name stage 12 reads the session under for an HTTP
// request.
func sessionCookieName(e *env) string {
	return e.svc.SessionCookie(auth.ClientInfo{}, "", time.Time{}).Name
}

func TestOIDCStart_ForwardsEveryParameterAndSetsTheBindingCookie(t *testing.T) {
	ff := &fakeFlow{start: okStart(t), providers: []oidc.ProviderInfo{{ID: "penguin", Kind: "oidc"}}}
	h, e := oidcHandler(t, ff, httpapi.LimitOptions{})
	for _, base := range []string{core + "/auth/oidc", headBase} {
		ff.starts = nil
		q := "?return=%2Fbooks&callback=batapp%3A%2F%2Fcb&code_challenge=" + strings.Repeat("A", 43) + "&code_challenge_method=S256&state=app-state"
		w := serve(h, "GET", base+"/Penguin/start"+q, nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "https://idp.example.test/authorize?state=s1" {
			t.Fatalf("%s: %d Location %q", base, w.Code, w.Header().Get("Location"))
		}
		got := ff.starts[0]
		want := oidc.StartRequest{Provider: "penguin", Return: "/books", RedirectURI: "batapp://cb", CodeChallenge: strings.Repeat("A", 43), CodeChallengeMethod: "S256", AppState: "app-state"}
		if got != want {
			t.Fatalf("%s: Start got %+v, want %+v", base, got, want)
		}
		ck := cookieNamed(w, sessionCookieName(e)+"_oidc")
		if ck == nil || ck.Value != "signed-binding" || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge != 600 || ck.Path != "/" {
			t.Fatalf("%s: binding cookie %+v", base, ck)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: a redirect that sets a binding is cacheable", base)
		}
	}
	// redirect_uri wins over its alias.
	ff.starts = nil
	serve(h, "GET", headBase+"/penguin/start?redirect_uri=batapp%3A%2F%2Fone&callback=batapp%3A%2F%2Ftwo", nil)
	if ff.starts[0].RedirectURI != "batapp://one" {
		t.Fatalf("redirect_uri %q", ff.starts[0].RedirectURI)
	}
}

// An unregistered redirect_uri or a bad challenge is answered inline: no
// Location, no cookie. Redirecting to an unvalidated URI to say it is
// unvalidated would be an open redirect.
func TestOIDCStart_RefusalsAreInlineWithNoLocationAndNoCookie(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{oidc.ErrBadRedirect, 400, "bad_redirect_uri"},
		{oidc.ErrBadChallenge, 400, "bad_code_challenge"},
		{oidc.ErrAppStateTooLong, 400, "app_state_too_long"},
		{oidc.ErrUnknownProvider, 404, "not_found"},
		{oidc.ErrNotConfigured, 404, "not_found"},
	} {
		ff := &fakeFlow{start: func(oidc.StartRequest) (*oidc.Started, error) { return nil, c.err }}
		h, _ := oidcHandler(t, ff, httpapi.LimitOptions{})
		w := serve(h, "GET", headBase+"/penguin/start?redirect_uri=https%3A%2F%2Fevil.example.test%2F", nil)
		wantProblem(t, w, c.status, c.code)
		if w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%v: Location %q cookies %v", c.err, w.Header().Get("Location"), w.Result().Cookies())
		}
	}
}

// The hand-off: the app's own code and state in the redirect, and no web
// session: no session cookie and no session in the store.
func TestOIDCHandoff_CallbackRedirectsToTheAppAndOpensNoWebSession(t *testing.T) {
	ff := &fakeFlow{callback: func(r oidc.CallbackRequest) (*oidc.Completed, error) {
		return &oidc.Completed{Location: "batapp://cb?code=app-code&state=app-state"}, nil
	}}
	h, e := oidcHandler(t, ff, httpapi.LimitOptions{})
	bind := &http.Cookie{Name: sessionCookieName(e) + "_oidc", Value: "signed-binding"}
	w := serve(h, "GET", headBase+"/Penguin/callback?code=c1&state=s1", nil, func(r *http.Request) { r.AddCookie(bind) })
	if w.Code != http.StatusFound || w.Header().Get("Location") != "batapp://cb?code=app-code&state=app-state" {
		t.Fatalf("%d Location %q", w.Code, w.Header().Get("Location"))
	}
	if ck := cookieNamed(w, sessionCookieName(e)); ck != nil {
		t.Fatalf("the hand-off set a session cookie: %+v", ck)
	}
	cleared := cookieNamed(w, bind.Name)
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("binding cookie not cleared: %+v", cleared)
	}
	got := ff.callbacks[0]
	if got.Provider != "penguin" || got.Code != "c1" || got.State != "s1" || got.Binding != "signed-binding" || got.IdPError != "" {
		t.Fatalf("Callback got %+v", got)
	}
}

func TestOIDCCallback_WebSessionSetsTheCookieAndLandsOnASameSitePath(t *testing.T) {
	for _, c := range []struct{ ret, want string }{
		{"/books", "/books"}, {"", "/"}, {"//evil.example.test", "/"}, {"https://evil.example.test", "/"}, {"/a\r\nSet-Cookie: x=y", "/"}, {`/\evil`, "/"},
	} {
		ff := &fakeFlow{callback: func(oidc.CallbackRequest) (*oidc.Completed, error) {
			return &oidc.Completed{Session: &auth.IssuedSession{Token: "mcs_batman", ExpiresAt: time.Now().Add(time.Hour)}, Return: c.ret}, nil
		}}
		h, e := oidcHandler(t, ff, httpapi.LimitOptions{})
		w := serve(h, "GET", headBase+"/penguin/callback?code=c&state=s", nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != c.want {
			t.Fatalf("return %q: %d Location %q, want %q", c.ret, w.Code, w.Header().Get("Location"), c.want)
		}
		ck := cookieNamed(w, sessionCookieName(e))
		if ck == nil || ck.Value != "mcs_batman" || !ck.HttpOnly {
			t.Fatalf("session cookie %+v", ck)
		}
	}
}

// Each way a callback can fail leaves the binding cookie cleared; the ones a
// person can act on send the browser to the sign-in page with a short code.
func TestOIDCCallback_FailuresClearTheBindingAndMapToTheirAnswer(t *testing.T) {
	for _, c := range []struct {
		err      error
		status   int
		location string
		code     string
	}{
		{oidc.ErrDeniedByProvider, 302, "/login?error=oauth_denied", ""},
		{oidc.ErrNoCode, 302, "/login?error=oauth_no_code", ""},
		{oidc.ErrExchange, 302, "/login?error=oauth_exchange_failed", ""},
		{oidc.ErrMintFailed, 302, "/login?error=oauth_code_mint_failed", ""},
		{oidc.ErrEmailNotVerified, 302, "/login?error=oauth_not_authorized", ""},
		{oidc.ErrEmailNotAllowed, 302, "/login?error=oauth_not_authorized", ""},
		{auth.ErrInactiveUser, 302, "/login?error=oauth_not_authorized", ""},
		{oidc.ErrSetupRequired, 302, "/login?error=oauth_not_authorized", ""},
		{oidc.ErrStateInvalid, 400, "", "oauth_state_invalid"},
		{auth.ErrEmailInUse, 409, "", "email_in_use"},
		{oidc.ErrUnknownProvider, 404, "", "not_found"},
		{auth.ErrStoreUnavailable, 503, "", "auth_unavailable"},
		{errors.New("the identity provider's own message, with a client secret"), 500, "", ""},
	} {
		ff := &fakeFlow{callback: func(oidc.CallbackRequest) (*oidc.Completed, error) { return nil, c.err }}
		h, e := oidcHandler(t, ff, httpapi.LimitOptions{})
		w := serve(h, "GET", headBase+"/penguin/callback?code=c&state=s", nil)
		if w.Code != c.status {
			t.Fatalf("%v: status %d, want %d: %s", c.err, w.Code, c.status, w.Body.String())
		}
		if c.location != "" && w.Header().Get("Location") != c.location {
			t.Fatalf("%v: Location %q, want %q", c.err, w.Header().Get("Location"), c.location)
		}
		if c.code != "" {
			wantProblem(t, w, c.status, c.code)
		}
		if ck := cookieNamed(w, sessionCookieName(e)+"_oidc"); ck == nil || ck.MaxAge >= 0 {
			t.Fatalf("%v: binding cookie not cleared: %+v", c.err, ck)
		}
		if ck := cookieNamed(w, sessionCookieName(e)); ck != nil {
			t.Fatalf("%v: a failed callback set a session cookie", c.err)
		}
		if strings.Contains(w.Body.String(), "client secret") || strings.Contains(w.Header().Get("Location"), "secret") {
			t.Fatalf("%v: the provider's message reached the response", c.err)
		}
	}
}

// The F4-03 review's hard gate: the callback runs behind the auth rate limit.
// Every failed callback costs an identity-provider call, a log line and an
// audit row, and anyone can get a valid binding for free from the start route,
// so a spent budget must answer 429 before the handler (and the Flow) runs.
func TestOIDCCallback_RunsBehindTheAuthRateLimit(t *testing.T) {
	for _, base := range []string{headBase, ""} {
		ff := &fakeFlow{callback: func(oidc.CallbackRequest) (*oidc.Completed, error) { return nil, oidc.ErrStateInvalid }}
		e := newEnv(t)
		a := e.api(func(d *Deps) { d.Flow = ff; d.OIDCCallbackBase = base })
		const burst = 3
		h := publicHandler(t, a, httpapi.LimitOptions{RateLimitEnabled: true, RateLimitPerMinute: 1, RateLimitBurst: burst})
		path := base + "/penguin/callback"
		if base == "" {
			path = core + "/auth/oidc/penguin/callback"
		}
		for i := range burst {
			if w := serve(h, "GET", path+"?code=c&state=s", nil); w.Code != 400 {
				t.Fatalf("base %q request %d: %d, want the handler's 400", base, i+1, w.Code)
			}
		}
		w := serve(h, "GET", path+"?code=c&state=s", nil)
		wantProblem(t, w, http.StatusTooManyRequests, "rate_limited")
		if w.Header().Get("Retry-After") == "" || !strings.Contains(w.Header().Get("RateLimit-Policy"), `"auth"`) {
			t.Fatalf("base %q: 429 without Retry-After or the auth policy: %v", base, w.Header())
		}
		if _, cb := ff.calls(); cb != burst {
			t.Fatalf("base %q: the Flow ran %d times for %d allowed requests: the limit did not stop the handler", base, cb, burst)
		}
		// The limit is per client: another address still gets through.
		w = serve(h, "GET", path+"?code=c&state=s", nil, func(r *http.Request) { r.RemoteAddr = "192.0.2.77:4000" })
		if w.Code != 400 {
			t.Fatalf("base %q: another client was limited: %d", base, w.Code)
		}
	}
	// The route table agrees: both callbacks and both starts are RateAuth.
	_, infos := registered(t, newEnv(t), headBase)
	for _, ri := range infos {
		if strings.HasSuffix(ri.Pattern, "/callback") || strings.HasSuffix(ri.Pattern, "/start") {
			if ri.RateClass != httpapi.RateAuth {
				t.Errorf("%s %s is %v, not RateAuth", ri.Method, ri.Pattern, ri.RateClass)
			}
		}
	}
}

func TestProviders_ListIdsAndKindsOnly(t *testing.T) {
	ff := &fakeFlow{providers: []oidc.ProviderInfo{{ID: "penguin", Kind: "oidc"}, {ID: "riddler", Kind: "google"}}}
	h, _ := oidcHandler(t, ff, httpapi.LimitOptions{})
	w := serve(h, "GET", core+"/auth/providers", nil)
	if w.Code != 200 || w.Body.String() != `{"providers":[{"id":"penguin","kind":"oidc"},{"id":"riddler","kind":"google"}]}`+"\n" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}
