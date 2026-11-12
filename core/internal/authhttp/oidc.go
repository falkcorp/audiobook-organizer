// file: core/internal/authhttp/oidc.go
// version: 1.0.0
// guid: 8b04e7d9-1c36-4a52-b9f8-6e2d5a017c93
// last-edited: 2026-11-10

package authhttp

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// signInPath is the page a failed callback sends the browser back to, with the
// short error code the page turns into a message (HEAD's redirectToLogin).
const signInPath = "/login"

// bindingSuffix names the OIDC binding cookie: the session cookie's name, which
// is per-app and carries the __Host- prefix on HTTPS, plus this suffix.
const bindingSuffix = "_oidc"

// The OIDC routes. Both start paths and both callback paths are Public and
// RateAuth. The callback is RateAuth because every failed callback costs an
// identity-provider call, a log line and an audit row, and anyone can get a
// valid binding for free from the start route: a spent budget answers 429
// before the handler runs (the F4-03 review's hard gate,
// TestOIDCCallback_RunsBehindTheAuthRateLimit).

// oidcSpecs returns the start and callback specs for a fixed-group base: HEAD's
// two exact patterns, relative to it.
func fixedSpecs() (start, callback spec) {
	start, callback = specOIDCStart(), specOIDCCallbackNative()
	start.path, start.name = "/{provider}/start", "startOIDCHead"
	callback.path, callback.name = "/{provider}/callback", "completeOIDCHead"
	return start, callback
}

func (a *api) oidcRoutes() []route {
	doc := httpapi.RawDoc{Status: http.StatusFound, Query: []string{"return", "redirect_uri", "callback", "code_challenge", "code_challenge_method", "state"}}
	cb := httpapi.RawDoc{Status: http.StatusFound, Query: []string{"code", "state", "error"}}
	out := []route{a.rawRoute(specOIDCStart(), doc, a.oidcStart)}
	if a.OIDCCallbackBase == "" {
		return append(out, a.rawRoute(specOIDCCallbackNative(), cb, a.oidcCallback))
	}
	start, callback := fixedSpecs()
	for _, rt := range []route{a.rawRoute(start, doc, a.oidcStart), a.rawRoute(callback, cb, a.oidcCallback)} {
		rt.fixed = true
		out = append(out, rt)
	}
	return out
}

// bindingCookie is the short-lived cookie that ties a sign-in to the browser
// that began it: HttpOnly, SameSite=Lax (the callback is a top-level
// navigation from the provider's site, which Strict would drop), Secure on
// HTTPS and then under the __Host- prefix.
func (a *api) bindingCookie(c auth.ClientInfo, value string, maxAge int) *http.Cookie {
	name := a.Cookies.SessionCookie(c, "", time.Time{}).Name + bindingSuffix
	ck := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: c.HTTPS, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	if value == "" {
		ck.MaxAge = -1
	}
	return ck
}

// oidcStart is the one handler of both start paths: the same parameters, the
// same native-app rules. It answers an unregistered redirect_uri inline, never
// by redirecting to it (bouncing to an unvalidated URI to report that it is
// unvalidated is itself an open redirect).
func (a *api) oidcStart(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	if a.Flow == nil {
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such provider"}
	}
	q := r.URL.Query()
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	if redirectURI == "" {
		redirectURI = strings.TrimSpace(q.Get("callback"))
	}
	started, err := a.Flow.Start(r.Context(), oidc.StartRequest{
		Provider: strings.ToLower(r.PathValue("provider")), Return: q.Get("return"), RedirectURI: redirectURI,
		CodeChallenge: strings.TrimSpace(q.Get("code_challenge")), CodeChallengeMethod: strings.TrimSpace(q.Get("code_challenge_method")),
		AppState: q.Get("state"),
	})
	if err != nil {
		if he, ok := oidcInline(err); ok {
			return he
		}
		return a.fail(r.Context(), specOIDCStart(), nil, "", err)
	}
	http.SetCookie(w, a.bindingCookie(a.clientOf(r), started.Binding, started.MaxAge))
	redirect(w, started.Location)
	return nil
}

// oidcCallback finishes a sign-in. The binding cookie is cleared whatever the
// outcome. The native-app branch redirects to the location the hand-off
// minted and sets no session cookie: no web session exists on it.
func (a *api) oidcCallback(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	if a.Flow == nil {
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such provider"}
	}
	c := a.clientOf(r)
	var binding string
	if ck, err := r.Cookie(a.bindingCookie(c, "", 0).Name); err == nil {
		binding = ck.Value
	}
	q := r.URL.Query()
	done, err := a.Flow.Callback(r.Context(), oidc.CallbackRequest{
		Provider: strings.ToLower(r.PathValue("provider")), Code: q.Get("code"), State: q.Get("state"),
		Binding: binding, IdPError: q.Get("error"), Client: c,
	})
	http.SetCookie(w, a.bindingCookie(c, "", 0))
	if err != nil {
		if he, ok := oidcInline(err); ok {
			return he
		}
		if code := oidcRedirectCode(err); code != codeOAuthFailed {
			redirect(w, signInPath+"?error="+url.QueryEscape(code))
			return nil
		}
		return a.fail(r.Context(), specOIDCCallbackNative(), nil, "", err)
	}
	if done.Session == nil {
		// The native-app hand-off: the app's own code, before any session.
		redirect(w, done.Location)
		return nil
	}
	http.SetCookie(w, a.Cookies.SessionCookie(c, done.Session.Token, done.Session.ExpiresAt))
	redirect(w, safeReturn(done.Return))
	return nil
}

// redirect answers 302 with Location, never cacheable.
func redirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

// safeReturn is the post-login path: a same-site absolute path or the root.
// oidc already drops anything else; this is the route's own check of what it
// is about to put in a Location header.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

func specOIDCStart() spec {
	return spec{method: http.MethodGet, path: "/auth/oidc/{provider}/start", name: "startOIDC", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Send the browser to an identity provider",
		doc:     "redirect_uri (alias callback) with code_challenge selects the native-app branch: an unlisted redirect_uri is an inline 400 with no Location, a method other than S256 is a 400."}
}

func specOIDCCallbackNative() spec {
	return spec{method: http.MethodGet, path: "/auth/oidc/{provider}/callback", name: "completeOIDC", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Finish a sign-in at the provider's redirect",
		doc:     "Opens a web session, or on the native-app branch mints the app's code before any session exists and redirects to the app."}
}
