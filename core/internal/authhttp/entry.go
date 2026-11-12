// file: core/internal/authhttp/entry.go
// version: 1.0.1
// guid: 2f6a8c41-9e03-4b75-a7d8-1c5b0e93f4a6
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The routes a caller meets before it holds a session (05-authn §4.13).

func (a *api) entryRoutes() []route {
	return []route{
		jsonRoute(specStatus(), a.status),
		jsonRoute(specSetup(), a.setup),
		jsonRoute(specBootstrap(), a.bootstrap),
		jsonRoute(specProviders(), a.providers),
		jsonRoute(specInviteLookup(), a.inviteLookup),
		a.rawRoute(specLogin(), httpapi.RawDoc{Request: httpapi.BodyOf[LoginRequest](), Success: httpapi.BodyOf[SessionOpened]()}, a.login),
		a.rawRoute(specLogout(), httpapi.RawDoc{Status: http.StatusNoContent}, a.logout),
		a.rawRoute(specInviteAccept(), httpapi.RawDoc{Request: httpapi.BodyOf[InviteAccept](), Success: httpapi.BodyOf[SessionOpened]()}, a.inviteAccept),
		a.rawRoute(specTempLoginConsume(), httpapi.RawDoc{Request: httpapi.BodyOf[TempLoginConsume](), Success: httpapi.BodyOf[SessionOpened]()}, a.tempLoginConsume),
	}
}

// noBody is a request type that binds nothing.
type noBody struct{}

func (a *api) status(_ context.Context, _ *auth.Principal, _ *httpapi.In[noBody]) (Status, error) {
	has := a.Entry.HasUsers()
	return Status{HasUsers: has, AuthDisabled: a.Entry.Posture().Disabled, SetupRequired: !has}, nil
}

type setupReq struct {
	Body SetupRequest `body:"json"`
}

func (a *api) setup(ctx context.Context, _ *auth.Principal, in *httpapi.In[setupReq]) (User, error) {
	b := in.Req.Body
	u, err := a.Entry.Setup(ctx, auth.SetupInput{
		Username: b.Username, Password: b.Password, Email: b.Email, BootstrapToken: b.BootstrapToken, Client: a.clientOf(in.Request),
	})
	if err != nil {
		return User{}, a.fail(ctx, specSetup(), nil, "", err)
	}
	return userBody(u), nil
}

type bootstrapReq struct {
	Body BootstrapRequest `body:"json"`
}

func (a *api) bootstrap(ctx context.Context, _ *auth.Principal, in *httpapi.In[bootstrapReq]) (BootstrapResult, error) {
	res, err := a.Entry.RedeemBootstrap(ctx, in.Req.Body.Token, a.clientOf(in.Request))
	if err != nil {
		return BootstrapResult{}, a.fail(ctx, specBootstrap(), nil, "", err)
	}
	out := BootstrapResult{Key: res.Key, KeyID: res.KeyID, ExpiresAt: res.ExpiresAt, GeneratedPassword: res.GeneratedPassword}
	if res.User != nil {
		out.User = userBody(res.User)
	}
	return out, nil
}

func (a *api) providers(_ context.Context, _ *auth.Principal, _ *httpapi.In[noBody]) (Providers, error) {
	out := Providers{Providers: []Provider{}}
	if a.Flow == nil {
		return out, nil
	}
	for _, p := range a.Flow.Providers() {
		out.Providers = append(out.Providers, Provider{ID: p.ID, Kind: p.Kind})
	}
	return out, nil
}

type inviteLookupReq struct {
	Body InviteLookup `body:"json"`
}

func (a *api) inviteLookup(ctx context.Context, _ *auth.Principal, in *httpapi.In[inviteLookupReq]) (InvitePreview, error) {
	v, err := a.Entry.LookupInvite(ctx, in.Req.Body.Token, a.clientOf(in.Request))
	if err != nil {
		return InvitePreview{}, a.linkFail(ctx, specInviteLookup(), err)
	}
	return InvitePreview{Username: v.Username, Email: v.Email, Role: string(v.Role), ExpiresAt: v.ExpiresAt}, nil
}

// login is POST /auth/login. The token reaches the browser in the cookie only:
// echoing it in the body would hand an XSS-readable copy to the page.
func (a *api) login(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	var body LoginRequest
	if err := httpapi.DecodeJSON(w, r, maxBody, &body); err != nil {
		return err
	}
	c := a.clientOf(r)
	is, err := a.Entry.Login(r.Context(), auth.LoginInput{Username: body.Username, Password: body.Password, Remember: body.Remember, Client: c})
	if err != nil {
		return a.fail(r.Context(), specLogin(), nil, "", err)
	}
	a.writeSession(w, r, a.Cookies.SessionCookie(c, is.Token, is.ExpiresAt), SessionOpened{PublicID: is.PublicID, ExpiresAt: is.ExpiresAt})
	return nil
}

// logout is POST /auth/logout: the caller's own session is deleted, and the
// cookie is cleared whatever the credential was.
func (a *api) logout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if p != nil && p.User != nil && (p.Method == auth.MethodSession || p.Method == auth.MethodSessionDelegated) {
		// A session that is already gone is what logout wants; the cookie is
		// cleared below either way.
		if err := a.Self.RevokeSession(r.Context(), p, p.CredentialID); err != nil && !errors.Is(err, auth.ErrNotFound) {
			return a.fail(r.Context(), specLogout(), p, "", err)
		}
	}
	http.SetCookie(w, a.Cookies.SessionCookie(a.clientOf(r), "", time.Time{}))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// inviteAccept is POST /auth/invites/accept.
func (a *api) inviteAccept(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	var body InviteAccept
	if err := httpapi.DecodeJSON(w, r, maxBody, &body); err != nil {
		return err
	}
	c := a.clientOf(r)
	u, is, err := a.Entry.AcceptInvite(r.Context(), auth.AcceptInviteInput{Token: body.Token, Password: body.Password, Client: c})
	if err != nil {
		return a.linkFail(r.Context(), specInviteAccept(), err)
	}
	ub := userBody(u)
	a.writeSession(w, r, a.Cookies.SessionCookie(c, is.Token, is.ExpiresAt), SessionOpened{PublicID: is.PublicID, ExpiresAt: is.ExpiresAt, User: &ub})
	return nil
}

// tempLoginConsume is POST /auth/temp-login/consume.
func (a *api) tempLoginConsume(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	var body TempLoginConsume
	if err := httpapi.DecodeJSON(w, r, maxBody, &body); err != nil {
		return err
	}
	c := a.clientOf(r)
	is, err := a.Entry.ConsumeTempLogin(r.Context(), body.Token, body.NewPassword, c)
	if err != nil {
		return a.linkFail(r.Context(), specTempLoginConsume(), err)
	}
	a.writeSession(w, r, a.Cookies.SessionCookie(c, is.Token, is.ExpiresAt), SessionOpened{PublicID: is.PublicID, ExpiresAt: is.ExpiresAt})
	return nil
}

// writeSession answers a handler that opened a session: the cookie, no-store
// (the response sets a credential), and the JSON body.
func (a *api) writeSession(w http.ResponseWriter, r *http.Request, cookie *http.Cookie, body SessionOpened) {
	http.SetCookie(w, cookie)
	a.writeJSON(w, r, http.StatusOK, body)
}

// writeJSON writes a credential-bearing response: never cacheable, never
// sniffed.
func (a *api) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(sb.String())); err != nil {
		// The status line is out; the client went away mid-body.
		a.log.DebugContext(r.Context(), "authhttp: the response body was not delivered", slog.Any("error", err))
	}
}

func specStatus() spec {
	return spec{method: http.MethodGet, path: "/auth/status", name: "getAuthStatus", perm: rbac.Public,
		summary: "Whether the app has users and whether authentication is on"}
}

func specSetup() spec {
	return spec{method: http.MethodPost, path: "/auth/setup", name: "setupFirstAdmin", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Create the first administrator",
		doc:     "Open only while no user exists, and then only with the bootstrap token or from a local request (05-authn §4.6). Every refusal is the same 403 setup_closed."}
}

func specBootstrap() spec {
	return spec{method: http.MethodPost, path: "/auth/bootstrap", name: "redeemBootstrapToken", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Exchange the bootstrap token for an administrator API key",
		doc:     "The key and a generated password (when the account is created) are shown once. A wrong, expired or spent token is the same 401 bootstrap_invalid."}
}

func specLogin() spec {
	return spec{method: http.MethodPost, path: "/auth/login", name: "login", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Sign in with a username and password",
		doc:     "Sets the session cookie; the body carries no token. An unknown name, a wrong password and an account without a password are the same 401 invalid_credentials."}
}

func specLogout() spec {
	return spec{method: http.MethodPost, path: "/auth/logout", name: "logout", perm: rbac.AnyAuthenticated, rate: httpapi.RateAuth,
		summary: "End the current session and clear its cookie", success: http.StatusNoContent}
}

func specProviders() spec {
	return spec{method: http.MethodGet, path: "/auth/providers", name: "listAuthProviders", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "List the enabled OIDC providers", doc: "Ids and kinds only: never a client id or a secret."}
}

func specInviteLookup() spec {
	return spec{method: http.MethodPost, path: "/auth/invites/lookup", name: "lookupInvite", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Show what an invite token would create",
		doc:     "The token travels in the body. An unknown, spent or expired token is the same 401 link_invalid."}
}

func specInviteAccept() spec {
	return spec{method: http.MethodPost, path: "/auth/invites/accept", name: "acceptInvite", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Accept an invite: create the account and open a session",
		doc:     "Sets the session cookie. The invite is re-checked at this moment: its creator must still exist and still be able to assign its role."}
}

func specTempLoginConsume() spec {
	return spec{method: http.MethodPost, path: "/auth/temp-login/consume", name: "consumeTempLogin", perm: rbac.Public, rate: httpapi.RateAuth,
		summary: "Spend a temp-login or reset link and open a session",
		doc:     "Sets the session cookie. A reset link needs new_password, a plain link refuses it."}
}
