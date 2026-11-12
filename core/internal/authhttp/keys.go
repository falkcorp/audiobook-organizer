// file: core/internal/authhttp/keys.go
// version: 1.0.0
// guid: 6f1a3c84-2e9d-4b07-a5c8-1d4e7b920a63
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// API keys addressed by ID, outside /me (05-authn §4.13): every user's keys for
// a manager, any key by ID for its owner or a manager who dominates the owner.

func (a *api) keyRoutes() []route {
	return []route{
		jsonRoute(specKeysAll(), a.allKeys),
		jsonRoute(specKeyGet(), a.getKey),
		jsonRoute(specKeyStatus(), a.setKeyStatus),
		jsonRoute(specKeyRevoke(), a.revokeKey),
		jsonRoute(specUserKeys(), a.userKeys),
		jsonRoute(specUserKeyNew(), a.createUserKey),
	}
}

func (a *api) allKeys(ctx context.Context, p *auth.Principal, in *httpapi.In[listReq]) (httpapi.Page[APIKey], error) {
	all, err := a.keysOf(ctx, "", true)
	if err != nil {
		return httpapi.Page[APIKey]{}, a.fail(ctx, specKeysAll(), p, "", err)
	}
	return pageOf(all, in.Req.PageParams), nil
}

// keyByID finds a key among every owner's.
func (a *api) keyByID(ctx context.Context, s spec, p *auth.Principal, id string) (*auth.APIKey, error) {
	for k, err := range a.Self.APIKeys(ctx, "") {
		if err != nil {
			return nil, a.fail(ctx, s, p, "", err)
		}
		if k.ID == id {
			return k, nil
		}
	}
	return nil, notFound()
}

// reachable finds key id for p, or the answer a missing key gets: a key that
// is another user's and that p may not manage is indistinguishable from one
// that does not exist. A write that passes here still goes to the Service,
// which enforces dominance and the interactive requirement and records the
// refusal; a read has no Service call, so it applies dominance itself.
func (a *api) reachable(ctx context.Context, s spec, p *auth.Principal, id string, write bool) (*auth.APIKey, error) {
	k, err := a.keyByID(ctx, s, p, id)
	if err != nil {
		return nil, err
	}
	if k.Owner == p.User.ID {
		return k, nil
	}
	if !holds(p, needManage()) {
		return nil, notFound()
	}
	if !write {
		owner, err := a.Users.User(ctx, k.Owner)
		if err != nil {
			return nil, a.fail(ctx, s, p, string(k.Owner), err)
		}
		if a.Dominance.Dominates(p.Perms, owner.Roles) != nil {
			return nil, notFound()
		}
	}
	return k, nil
}

func (a *api) ownerName(ctx context.Context, owner auth.UserID) string {
	if u, err := a.Users.User(ctx, owner); err == nil {
		return u.Username
	}
	return ""
}

func (a *api) getKey(ctx context.Context, p *auth.Principal, in *httpapi.In[keyReq]) (APIKey, error) {
	k, err := a.reachable(ctx, specKeyGet(), p, in.Req.ID, false)
	if err != nil {
		return APIKey{}, err
	}
	name := ""
	if k.Owner != p.User.ID {
		name = a.ownerName(ctx, k.Owner)
	}
	return keyBody(k, name), nil
}

func (a *api) setKeyStatus(ctx context.Context, p *auth.Principal, in *httpapi.In[keyStatusReq]) (APIKey, error) {
	k, err := a.reachable(ctx, specKeyStatus(), p, in.Req.ID, true)
	if err != nil {
		return APIKey{}, err
	}
	return a.changeKey(ctx, specKeyStatus(), p, k.Owner, k.ID, auth.KeyStatus(in.Req.Body.Status))
}

func (a *api) revokeKey(ctx context.Context, p *auth.Principal, in *httpapi.In[keyReq]) (httpapi.NoContent, error) {
	k, err := a.reachable(ctx, specKeyRevoke(), p, in.Req.ID, true)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	if err := a.Self.RevokeAPIKey(ctx, p, k.ID); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, specKeyRevoke(), p, string(k.Owner), err)
	}
	return httpapi.NoContent{}, nil
}

type userKeysReq struct {
	ID string `path:"id"`
	httpapi.PageParams
}

// target loads the user a /users/{id}/... route acts on and applies dominance
// over it, so a read of the user's credential metadata is held to the rule its
// writes are.
func (a *api) target(ctx context.Context, s spec, p *auth.Principal, id string) (*auth.User, error) {
	u, err := a.Users.User(ctx, auth.UserID(id))
	if err != nil {
		return nil, a.fail(ctx, s, p, "", err)
	}
	if err := a.Dominance.Dominates(p.Perms, u.Roles); err != nil {
		return nil, a.fail(ctx, s, p, id, auth.ErrDominance)
	}
	return u, nil
}

func (a *api) userKeys(ctx context.Context, p *auth.Principal, in *httpapi.In[userKeysReq]) (httpapi.Page[APIKey], error) {
	u, err := a.target(ctx, specUserKeys(), p, in.Req.ID)
	if err != nil {
		return httpapi.Page[APIKey]{}, err
	}
	all, err := a.keysOf(ctx, u.ID, true)
	if err != nil {
		return httpapi.Page[APIKey]{}, a.fail(ctx, specUserKeys(), p, in.Req.ID, err)
	}
	return pageOf(all, in.Req.PageParams), nil
}

type userKeyCreateReq struct {
	ID   string    `path:"id"`
	Body KeyCreate `body:"json"`
}

func (r *userKeyCreateReq) Validate() error { return r.Body.validate() }

func (a *api) createUserKey(ctx context.Context, p *auth.Principal, in *httpapi.In[userKeyCreateReq]) (httpapi.Created[KeyCreated], error) {
	// The Service refuses a key for a user the actor does not dominate and
	// records it; a user that does not exist is the same answer as on reads.
	if _, err := a.Users.User(ctx, auth.UserID(in.Req.ID)); err != nil {
		return httpapi.Created[KeyCreated]{}, a.fail(ctx, specUserKeyNew(), p, "", err)
	}
	return a.mintKey(ctx, specUserKeyNew(), p, auth.UserID(in.Req.ID), in.Req.Body, routeBase+"/api-keys/")
}

func needManage() rbac.Requirement { return rbac.Need(scopeUsersManage) }

func specKeysAll() spec {
	return spec{method: http.MethodGet, path: "/api-keys", name: "listAllAPIKeys", perm: needManage(), needUser: true, summary: "List every user's API keys", doc: "Each key carries its owner's username. Never the key or its hash."}
}

func specKeyGet() spec {
	return spec{method: http.MethodGet, path: "/api-keys/{id}", name: "getAPIKey", perm: needSelf(), needUser: true, summary: "Read one API key by ID", doc: "The owner's, or a manager's who dominates the owner. Any other caller gets the answer a missing key gets."}
}

func specKeyStatus() spec {
	return spec{method: http.MethodPatch, path: "/api-keys/{id}", name: "setAPIKeyStatus", perm: needSelf(), needUser: true, summary: "Disable, re-enable or revoke any key by ID", doc: "Re-enabling needs an interactive sign-in; another user's key needs an interactiveOnly() manager who dominates the owner."}
}

func specKeyRevoke() spec {
	return spec{method: http.MethodDelete, path: "/api-keys/{id}", name: "revokeAPIKey", perm: needSelf(), needUser: true, summary: "Revoke any key by ID", success: http.StatusNoContent}
}

func specUserKeys() spec {
	return spec{method: http.MethodGet, path: "/users/{id}/api-keys", name: "listUserAPIKeys", perm: needManage(), needUser: true, summary: "List one user's API keys"}
}

func specUserKeyNew() spec {
	return spec{method: http.MethodPost, path: "/users/{id}/api-keys", name: "createUserAPIKey", perm: needManage(), needUser: true, guards: httpapi.Guards{Interactive: true},
		summary: "Mint an API key for another user",
		doc:     "Needs dominance over the user and scopes the user's roles hold too. The key is shown once."}
}
