// file: core/internal/authhttp/users.go
// version: 1.0.0
// guid: 2b8d5e17-9c40-4a3f-b1e6-5a07c3d8f194
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Users, invites, account actions and other users' sessions (05-authn §4.13).
// Every write is Interactive and goes to a Service method that applies
// core:users.manage, dominance and CanAssign itself; the guards here are the
// route's declared requirement and the handler's own copy of it.

func (a *api) userRoutes() []route {
	return []route{
		jsonRoute(specUsers(), a.users),
		jsonRoute(specUser(), a.user),
		jsonRoute(specInvites(), a.invites),
		jsonRoute(specInviteNew(), a.createInvite),
		jsonRoute(specInviteDelete(), a.deleteInvite),
		jsonRoute(specDisable(), a.setStatus(specDisable(), auth.StatusDisabled)),
		jsonRoute(specEnable(), a.setStatus(specEnable(), auth.StatusActive)),
		jsonRoute(specUserDelete(), a.deleteUser),
		jsonRoute(specReset(), a.link(specReset(), a.Accounts.MintResetLink)),
		jsonRoute(specRoles(), a.setRoles),
		jsonRoute(specTemp(), a.link(specTemp(), a.Accounts.MintTempLogin)),
		jsonRoute(specUserSessions(), a.userSessions),
		jsonRoute(specUserSessionDelete(), a.revokeUserSession),
	}
}

type usersReq struct {
	Status string `query:"status"`
	Role   string `query:"role"`
	httpapi.PageParams
}

func (a *api) users(ctx context.Context, p *auth.Principal, in *httpapi.In[usersReq]) (httpapi.Page[User], error) {
	var all []User
	f := auth.UserFilter{Status: auth.UserStatus(in.Req.Status), Role: rbac.RoleName(in.Req.Role)}
	for u, err := range a.Accounts.Users(ctx, f) {
		if err != nil {
			return httpapi.Page[User]{}, a.fail(ctx, specUsers(), p, "", err)
		}
		all = append(all, userBody(u))
	}
	return pageOf(all, in.Req.PageParams), nil
}

type userReq struct {
	ID string `path:"id"`
}

func (a *api) user(ctx context.Context, p *auth.Principal, in *httpapi.In[userReq]) (User, error) {
	u, err := a.Users.User(ctx, auth.UserID(in.Req.ID))
	if err != nil {
		return User{}, a.fail(ctx, specUser(), p, "", err)
	}
	return userBody(u), nil
}

func (a *api) invites(ctx context.Context, p *auth.Principal, in *httpapi.In[listReq]) (httpapi.Page[Invite], error) {
	var all []Invite
	for v, err := range a.Invites.Invites(ctx) {
		if err != nil {
			return httpapi.Page[Invite]{}, a.fail(ctx, specInvites(), p, "", err)
		}
		all = append(all, inviteBody(v))
	}
	return pageOf(all, in.Req.PageParams), nil
}

type inviteCreateReq struct {
	Body InviteCreate `body:"json"`
}

func (r *inviteCreateReq) Validate() error {
	var fields []httpapi.FieldError
	if r.Body.Username == "" {
		fields = append(fields, httpapi.Field("/body/username", "required", "an invite needs a username"))
	}
	if r.Body.Role == "" {
		fields = append(fields, httpapi.Field("/body/role", "required", "an invite needs a role"))
	}
	if r.Body.TTL != "" {
		if d, err := time.ParseDuration(r.Body.TTL); err != nil || d <= 0 {
			fields = append(fields, httpapi.Field("/body/ttl", "invalid", "a positive duration such as 72h"))
		}
	}
	if len(fields) > 0 {
		return httpapi.Invalid(fields...)
	}
	return nil
}

func (a *api) createInvite(ctx context.Context, p *auth.Principal, in *httpapi.In[inviteCreateReq]) (httpapi.Created[InviteCreated], error) {
	b := in.Req.Body
	spec := auth.InviteSpec{Username: b.Username, Email: b.Email, Role: rbac.RoleName(b.Role)}
	if b.TTL != "" {
		spec.TTL, _ = time.ParseDuration(b.TTL) // Validate checked it
	}
	link, v, err := a.Invites.CreateInvite(ctx, p, spec)
	if err != nil {
		return httpapi.Created[InviteCreated]{}, a.fail(ctx, specInviteNew(), p, "", err)
	}
	return httpapi.Created[InviteCreated]{Location: routeBase + "/users/invites/" + v.ID, Body: InviteCreated{Link: link, Invite: inviteBody(v)}}, nil
}

func (a *api) deleteInvite(ctx context.Context, p *auth.Principal, in *httpapi.In[userReq]) (httpapi.NoContent, error) {
	if err := a.Invites.DeleteInvite(ctx, p, in.Req.ID); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specInviteDelete(), p, "", err)
	}
	return httpapi.NoContent{}, nil
}

// setStatus is the handler of enable and disable.
func (a *api) setStatus(s spec, st auth.UserStatus) func(context.Context, *auth.Principal, *httpapi.In[userReq]) (User, error) {
	return func(ctx context.Context, p *auth.Principal, in *httpapi.In[userReq]) (User, error) {
		if err := a.Accounts.SetStatus(ctx, p, auth.UserID(in.Req.ID), st); err != nil {
			return User{}, a.fail(ctx, s, p, in.Req.ID, err)
		}
		return a.reread(ctx, s, p, in.Req.ID)
	}
}

// reread answers the user as it is after a write.
func (a *api) reread(ctx context.Context, s spec, p *auth.Principal, id string) (User, error) {
	u, err := a.Users.User(ctx, auth.UserID(id))
	if err != nil {
		return User{}, a.fail(ctx, s, p, id, err)
	}
	return userBody(u), nil
}

func (a *api) deleteUser(ctx context.Context, p *auth.Principal, in *httpapi.In[userReq]) (httpapi.NoContent, error) {
	if err := a.Accounts.DeleteUser(ctx, p, auth.UserID(in.Req.ID)); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specUserDelete(), p, in.Req.ID, err)
	}
	return httpapi.NoContent{}, nil
}

// link is the handler of the two routes that mint a one-time link for a user:
// the link is in the response and nowhere else.
func (a *api) link(s spec, mint func(context.Context, *auth.Principal, auth.UserID) (string, error)) func(context.Context, *auth.Principal, *httpapi.In[userReq]) (LinkCreated, error) {
	return func(ctx context.Context, p *auth.Principal, in *httpapi.In[userReq]) (LinkCreated, error) {
		l, err := mint(ctx, p, auth.UserID(in.Req.ID))
		if err != nil {
			return LinkCreated{}, a.fail(ctx, s, p, in.Req.ID, err)
		}
		return LinkCreated{Link: l}, nil
	}
}

type rolesReq struct {
	ID   string       `path:"id"`
	Body RolesReplace `body:"json"`
}

func (r *rolesReq) Validate() error {
	if len(r.Body.Roles) == 0 {
		return httpapi.Invalid(httpapi.Field("/body/roles", "required", "a user needs at least one role"))
	}
	return nil
}

func (a *api) setRoles(ctx context.Context, p *auth.Principal, in *httpapi.In[rolesReq]) (User, error) {
	roles := make([]rbac.RoleName, 0, len(in.Req.Body.Roles))
	for _, r := range in.Req.Body.Roles {
		roles = append(roles, rbac.RoleName(r))
	}
	if err := a.Accounts.SetRoles(ctx, p, auth.UserID(in.Req.ID), roles); err != nil {
		return User{}, a.fail(ctx, specRoles(), p, in.Req.ID, err)
	}
	return a.reread(ctx, specRoles(), p, in.Req.ID)
}

type userSessionsReq struct {
	ID string `path:"id"`
	httpapi.PageParams
}

func (a *api) userSessions(ctx context.Context, p *auth.Principal, in *httpapi.In[userSessionsReq]) (httpapi.Page[Session], error) {
	u, err := a.target(ctx, specUserSessions(), p, in.Req.ID)
	if err != nil {
		return httpapi.Page[Session]{}, err
	}
	all, err := a.sessionsOf(ctx, u.ID)
	if err != nil {
		return httpapi.Page[Session]{}, a.fail(ctx, specUserSessions(), p, in.Req.ID, err)
	}
	return pageOf(all, in.Req.PageParams), nil
}

type userSessionReq struct {
	ID       string `path:"id"`
	PublicID string `path:"publicID"`
}

func (a *api) revokeUserSession(ctx context.Context, p *auth.Principal, in *httpapi.In[userSessionReq]) (httpapi.NoContent, error) {
	u, err := a.target(ctx, specUserSessionDelete(), p, in.Req.ID)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	// The session must be this user's: a public ID of anyone else's is the
	// answer a missing one gets.
	theirs, err := a.sessionsOf(ctx, u.ID)
	if err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specUserSessionDelete(), p, in.Req.ID, err)
	}
	if !slices.ContainsFunc(theirs, func(s Session) bool { return s.PublicID == in.Req.PublicID }) {
		return httpapi.NoContent{}, notFound()
	}
	if err := a.Self.RevokeSession(ctx, p, in.Req.PublicID); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specUserSessionDelete(), p, in.Req.ID, err)
	}
	return httpapi.NoContent{}, nil
}

func needView() rbac.Requirement { return rbac.Need(scopeUsersView) }

func interactiveOnly() httpapi.Guards { return httpapi.Guards{Interactive: true} }

func specUsers() spec {
	return spec{method: http.MethodGet, path: "/users", name: "listUsers", perm: needView(), summary: "List users", doc: "Filter by status or role."}
}

func specUser() spec {
	return spec{method: http.MethodGet, path: "/users/{id}", name: "getUser", perm: needView(), summary: "Read one user"}
}

func specInvites() spec {
	return spec{method: http.MethodGet, path: "/users/invites", name: "listInvites", perm: needManage(), needUser: true, summary: "List open invites", doc: "Never a token or a link."}
}

func specInviteNew() spec {
	return spec{method: http.MethodPost, path: "/users/invites", name: "createInvite", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Invite a user", doc: "The role must be one the caller may assign. The link is shown once."}
}

func specInviteDelete() spec {
	return spec{method: http.MethodDelete, path: "/users/invites/{id}", name: "deleteInvite", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Delete an open invite", success: http.StatusNoContent}
}

func specDisable() spec {
	return spec{method: http.MethodPost, path: "/users/{id}/disable", name: "disableUser", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Disable a user"}
}

func specEnable() spec {
	return spec{method: http.MethodPost, path: "/users/{id}/enable", name: "enableUser", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Enable a user"}
}

func specUserDelete() spec {
	return spec{method: http.MethodDelete, path: "/users/{id}", name: "deleteUser", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Delete a user", success: http.StatusNoContent}
}

func specReset() spec {
	return spec{method: http.MethodPost, path: "/users/{id}/reset-password", name: "resetUserPassword", perm: needManage(), needUser: true, guards: interactiveOnly(),
		summary: "Mint a password reset link for a user",
		doc:     "There is no route that sets another user's password. The link is shown once and ends every session of the user."}
}

func specRoles() spec {
	return spec{method: http.MethodPut, path: "/users/{id}/roles", name: "setUserRoles", perm: needManage(), needUser: true, guards: interactiveOnly(), summary: "Replace a user's roles"}
}

func specTemp() spec {
	return spec{method: http.MethodPost, path: "/users/{id}/temp-login", name: "mintTempLogin", perm: needManage(), needUser: true, guards: interactiveOnly(),
		summary: "Mint a temp-login link for a user", doc: "The link is shown once."}
}

func specUserSessions() spec {
	return spec{method: http.MethodGet, path: "/users/{id}/sessions", name: "listUserSessions", perm: needManage(), needUser: true, summary: "List a user's open sessions"}
}

func specUserSessionDelete() spec {
	return spec{method: http.MethodDelete, path: "/users/{id}/sessions/{publicID}", name: "revokeUserSession", perm: needManage(), needUser: true, guards: interactiveOnly(),
		summary: "End one of a user's sessions", success: http.StatusNoContent}
}
