// file: core/internal/authhttp/me.go
// version: 1.0.0
// guid: c3e72a95-0f4b-4d18-8b6a-7d1e9f305b24
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The caller's own profile, password, sessions and API keys (05-authn §4.13).
// Every one needs a user: the auth-disabled principal has none (§4.11).

func (a *api) selfRoutes() []route {
	return []route{
		jsonRoute(specMe(), a.me),
		jsonRoute(specMeEd(), a.updateMe),
		jsonRoute(specMePassword(), a.changePassword),
		jsonRoute(specMeSessions(), a.mySessions),
		jsonRoute(specMeSessionRevoke(), a.revokeMySession),
		jsonRoute(specMeKeys(), a.myKeys),
		jsonRoute(specMeKeyCreate(), a.createMyKey),
		jsonRoute(specMeKeyStatus(), a.setMyKeyStatus),
		jsonRoute(specMeKeyRevoke(), a.revokeMyKey),
	}
}

func (a *api) me(_ context.Context, p *auth.Principal, _ *httpapi.In[noBody]) (Me, error) {
	perms := []string{}
	if p.Perms != nil {
		for _, c := range p.Perms.Concrete() {
			perms = append(perms, string(c))
		}
	}
	return Me{User: userBody(p.User), Permissions: perms, Method: string(p.Method), OwnerVerified: p.Assurance&auth.AssuranceOwner != 0}, nil
}

type updateMeReq struct {
	Body ProfilePatch `body:"json"`
}

// Validate refuses an empty patch.
func (r *updateMeReq) Validate() error {
	if r.Body.DisplayName == nil && r.Body.Email == nil {
		return httpapi.Invalid(httpapi.Field("/body", "required", "set display_name or email"))
	}
	return nil
}

func (a *api) updateMe(ctx context.Context, p *auth.Principal, in *httpapi.In[updateMeReq]) (User, error) {
	if a.Profiles == nil {
		return User{}, &httpapi.Error{Status: http.StatusNotImplemented, Code: "profile_edit_unavailable", Detail: "this app has no profile writer"}
	}
	u, err := a.Profiles.UpdateProfile(ctx, p, ProfileUpdate{DisplayName: in.Req.Body.DisplayName, Email: in.Req.Body.Email})
	if err != nil {
		return User{}, a.fail(ctx, specMeEd(), p, "", err)
	}
	return userBody(u), nil
}

type passwordReq struct {
	Body PasswordChange `body:"json"`
}

func (a *api) changePassword(ctx context.Context, p *auth.Principal, in *httpapi.In[passwordReq]) (httpapi.NoContent, error) {
	err := a.Self.ChangeOwnPassword(ctx, p, in.Req.Body.Current, in.Req.Body.New)
	switch {
	case err == nil:
		return httpapi.NoContent{}, nil
	case errors.Is(err, auth.ErrInvalidCredentials):
		// The caller is signed in: a wrong current password is a validation
		// failure of this request, not a reason for the page to drop its
		// session as a 401 would.
		return httpapi.NoContent{}, httpapi.Invalid(httpapi.Field("/body/current", "incorrect", "the current password is wrong"))
	case errors.Is(err, auth.ErrBadInput):
		return httpapi.NoContent{}, httpapi.Invalid(httpapi.Field("/body/new", "invalid", "both passwords are required and the new one must meet the minimum length"))
	}
	return httpapi.NoContent{}, a.fail(ctx, specMePassword(), p, "", err)
}

type listReq struct {
	httpapi.PageParams
}

func (a *api) mySessions(ctx context.Context, p *auth.Principal, in *httpapi.In[listReq]) (httpapi.Page[Session], error) {
	all, err := a.sessionsOf(ctx, p.User.ID)
	if err != nil {
		return httpapi.Page[Session]{}, a.fail(ctx, specMeSessions(), p, "", err)
	}
	return pageOf(all, in.Req.PageParams), nil
}

// sessionsOf collects user's open sessions.
func (a *api) sessionsOf(ctx context.Context, user auth.UserID) ([]Session, error) {
	var out []Session
	for v, err := range a.Self.Sessions(ctx, user) {
		if err != nil {
			return nil, err
		}
		out = append(out, sessionBody(v))
	}
	return out, nil
}

type sessionReq struct {
	PublicID string `path:"publicID"`
}

func (a *api) revokeMySession(ctx context.Context, p *auth.Principal, in *httpapi.In[sessionReq]) (httpapi.NoContent, error) {
	// /me/sessions/{publicID} ends the caller's own session only: a manager's
	// reach over another user's is /users/{id}/sessions/{publicID}.
	mine, err := a.sessionsOf(ctx, p.User.ID)
	if err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specMeSessionRevoke(), p, "", err)
	}
	if !slices.ContainsFunc(mine, func(s Session) bool { return s.PublicID == in.Req.PublicID }) {
		return httpapi.NoContent{}, notFound()
	}
	if err := a.Self.RevokeSession(ctx, p, in.Req.PublicID); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specMeSessionRevoke(), p, "", err)
	}
	return httpapi.NoContent{}, nil
}

func (a *api) myKeys(ctx context.Context, p *auth.Principal, in *httpapi.In[listReq]) (httpapi.Page[APIKey], error) {
	all, err := a.keysOf(ctx, p.User.ID, false)
	if err != nil {
		return httpapi.Page[APIKey]{}, a.fail(ctx, specMeKeys(), p, "", err)
	}
	return pageOf(all, in.Req.PageParams), nil
}

// keysOf collects owner's keys; owner "" is every key, each with its owner's
// username (withOwners).
func (a *api) keysOf(ctx context.Context, owner auth.UserID, withOwners bool) ([]APIKey, error) {
	names := map[auth.UserID]string{}
	var out []APIKey
	for k, err := range a.Self.APIKeys(ctx, owner) {
		if err != nil {
			return nil, err
		}
		name := ""
		if withOwners {
			var ok bool
			if name, ok = names[k.Owner]; !ok {
				if u, err := a.Users.User(ctx, k.Owner); err == nil {
					name = u.Username
				} else if !errors.Is(err, auth.ErrNotFound) {
					return nil, err
				}
				names[k.Owner] = name
			}
		}
		out = append(out, keyBody(k, name))
	}
	return out, nil
}

type createKeyReq struct {
	Body KeyCreate `body:"json"`
}

func (r *createKeyReq) Validate() error { return r.Body.validate() }

func (k KeyCreate) validate() error {
	var fields []httpapi.FieldError
	if k.Name == "" {
		fields = append(fields, httpapi.Field("/body/name", "required", "a key needs a name"))
	}
	if len(k.Scopes) == 0 {
		fields = append(fields, httpapi.Field("/body/scopes", "required", "a key needs at least one scope"))
	}
	if k.ExpiresIn != "" {
		if d, err := time.ParseDuration(k.ExpiresIn); err != nil || d <= 0 {
			fields = append(fields, httpapi.Field("/body/expires_in", "invalid", "a positive duration such as 720h"))
		}
	}
	if len(fields) > 0 {
		return httpapi.Invalid(fields...)
	}
	return nil
}

// keySpec turns a validated body into the Service's request for owner.
func (k KeyCreate) keySpec(owner auth.UserID) auth.KeySpec {
	spec := auth.KeySpec{Owner: owner, Name: k.Name}
	for _, s := range k.Scopes {
		spec.Scopes = append(spec.Scopes, rbac.Permission(s))
	}
	if k.ExpiresIn != "" {
		spec.ExpiresIn, _ = time.ParseDuration(k.ExpiresIn) // Validate checked it
	}
	return spec
}

func (a *api) createMyKey(ctx context.Context, p *auth.Principal, in *httpapi.In[createKeyReq]) (httpapi.Created[KeyCreated], error) {
	return a.mintKey(ctx, specMeKeyCreate(), p, p.User.ID, in.Req.Body, routeBase+"/me/api-keys/")
}

// mintKey creates a key for owner and answers it once. The Service decides
// whether the actor may: scopes held, key actors only for themselves, a
// manager's interactive method and dominance.
func (a *api) mintKey(ctx context.Context, s spec, p *auth.Principal, owner auth.UserID, body KeyCreate, location string) (httpapi.Created[KeyCreated], error) {
	raw, k, err := a.Self.CreateAPIKey(ctx, p, body.keySpec(owner))
	if err != nil {
		return httpapi.Created[KeyCreated]{}, a.fail(ctx, s, p, string(owner), err)
	}
	return httpapi.Created[KeyCreated]{Location: location + k.ID, Body: KeyCreated{Key: raw, APIKey: keyBody(k, "")}}, nil
}

type keyReq struct {
	ID string `path:"id"`
}

type keyStatusReq struct {
	ID   string    `path:"id"`
	Body KeyStatus `body:"json"`
}

// Validate refuses a status the route does not set: a key goes to active
// (re-enable or confirm), disabled or revoked.
func (r *keyStatusReq) Validate() error {
	switch auth.KeyStatus(r.Body.Status) {
	case auth.KeyActive, auth.KeyDisabled, auth.KeyRevoked:
		return nil
	}
	return httpapi.Invalid(httpapi.Field("/body/status", "invalid_enum", "must be active, disabled or revoked"))
}

// ownKey finds one of owner's keys by ID, or notFound: the same answer for a
// key that does not exist and one that is another user's.
func (a *api) ownKey(ctx context.Context, s spec, p *auth.Principal, owner auth.UserID, id string) (*auth.APIKey, error) {
	for k, err := range a.Self.APIKeys(ctx, owner) {
		if err != nil {
			return nil, a.fail(ctx, s, p, "", err)
		}
		if k.ID == id {
			return k, nil
		}
	}
	return nil, notFound()
}

func (a *api) setMyKeyStatus(ctx context.Context, p *auth.Principal, in *httpapi.In[keyStatusReq]) (APIKey, error) {
	if _, err := a.ownKey(ctx, specMeKeyStatus(), p, p.User.ID, in.Req.ID); err != nil {
		return APIKey{}, err
	}
	return a.changeKey(ctx, specMeKeyStatus(), p, p.User.ID, in.Req.ID, auth.KeyStatus(in.Req.Body.Status))
}

// changeKey applies a status change and answers the key as it is now.
func (a *api) changeKey(ctx context.Context, s spec, p *auth.Principal, owner auth.UserID, id string, st auth.KeyStatus) (APIKey, error) {
	if err := a.Self.SetAPIKeyStatus(ctx, p, id, st); err != nil {
		return APIKey{}, a.fail(ctx, s, p, string(owner), err)
	}
	k, err := a.ownKey(ctx, s, p, owner, id)
	if err != nil {
		return APIKey{}, err
	}
	return keyBody(k, ""), nil
}

func (a *api) revokeMyKey(ctx context.Context, p *auth.Principal, in *httpapi.In[keyReq]) (httpapi.NoContent, error) {
	if _, err := a.ownKey(ctx, specMeKeyRevoke(), p, p.User.ID, in.Req.ID); err != nil {
		return httpapi.NoContent{}, err
	}
	if err := a.Self.RevokeAPIKey(ctx, p, in.Req.ID); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specMeKeyRevoke(), p, string(p.User.ID), err)
	}
	return httpapi.NoContent{}, nil
}

func needSelf() rbac.Requirement { return rbac.Need(scopeSelf) }

func specMe() spec {
	return spec{method: http.MethodGet, path: "/me", name: "getMe", perm: needSelf(), needUser: true, summary: "The signed-in user, its scopes and how it signed in"}
}

func specMeEd() spec {
	return spec{method: http.MethodPatch, path: "/me", name: "updateMe", perm: needSelf(), needUser: true, guards: httpapi.Guards{Interactive: true},
		summary: "Change the caller's own profile",
		doc:     "An e-mail change is an identity change: it clears the verified mark and revokes what the old address granted."}
}

func specMePassword() spec {
	return spec{method: http.MethodPut, path: "/me/password", name: "changeMyPassword", perm: needSelf(), needUser: true,
		guards: httpapi.Guards{Interactive: true}, summary: "Change the caller's own password", success: http.StatusNoContent,
		doc: "Self only: {current, new}. There is no target-user field; an admin sends a reset link instead. Every other session of the account ends."}
}

func specMeSessions() spec {
	return spec{method: http.MethodGet, path: "/me/sessions", name: "listMySessions", perm: needSelf(), needUser: true,
		summary: "List the caller's open sessions", doc: "Public IDs only, never a token."}
}

func specMeSessionRevoke() spec {
	return spec{method: http.MethodDelete, path: "/me/sessions/{publicID}", name: "revokeMySession", perm: needSelf(), needUser: true,
		guards: httpapi.Guards{Interactive: true}, summary: "End one of the caller's sessions", success: http.StatusNoContent}
}

func specMeKeys() spec {
	return spec{method: http.MethodGet, path: "/me/api-keys", name: "listMyAPIKeys", perm: needSelf(), needUser: true,
		summary: "List the caller's API keys", doc: "Never the key itself or its hash."}
}

func specMeKeyCreate() spec {
	return spec{method: http.MethodPost, path: "/me/api-keys", name: "createMyAPIKey", perm: needSelf(), needUser: true,
		summary: "Mint an API key for the caller",
		doc:     "The key is shown once. Every scope must be held by the caller now; a key mints only for itself and only within its own expiry (05-authn §4.4)."}
}

func specMeKeyStatus() spec {
	return spec{method: http.MethodPatch, path: "/me/api-keys/{id}", name: "setMyAPIKeyStatus", perm: needSelf(), needUser: true,
		summary: "Disable, re-enable or revoke one of the caller's keys",
		doc:     "A key manages only keys it may create; re-enabling needs an interactive sign-in, disabling and revoking do not (a restriction widens nothing)."}
}

func specMeKeyRevoke() spec {
	return spec{method: http.MethodDelete, path: "/me/api-keys/{id}", name: "revokeMyAPIKey", perm: needSelf(), needUser: true,
		summary: "Revoke one of the caller's keys", success: http.StatusNoContent}
}
