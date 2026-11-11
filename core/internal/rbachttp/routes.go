// file: core/internal/rbachttp/routes.go
// version: 1.0.0
// guid: 37af6151-4a38-40cb-a9b2-cba6bf81da4d
// last-edited: 2026-11-10

// Package rbachttp is the HTTP surface of core's roles (05-rbac §4.7, §4.8,
// F4-05): the roles list and read, create, patch and delete of a custom role,
// and POST /roles/{name}/accept-seed, registered on httpapi's core group
// (/api/core/v1). Each route is a thin adapter over *rbac.Service, which owns
// every rule: escalation (the actor must hold every permission a write
// touches), the admin lock, the seed-role lock, the last-admin rule and the
// version check. This package passes the principal's own checker as the
// service's actor and maps its refusals to statuses; it adds no reach of its
// own and is never the only guard.
//
// The package is dark until F4-08 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package rbachttp

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The scopes the routes declare (05-rbac §4.7's table). Both are catalog
// scopes (rbac.CoreScopes); roles.manage is Sensitive, so only an interactive
// principal holds it (§4.4).
const (
	permView   rbac.Permission = "core:roles.view"
	permManage rbac.Permission = "core:roles.manage"
)

// maxBody bounds a role body: a title, a description and a permission list.
const maxBody = 64 << 10

// RoleService is what the routes call on *rbac.Service. It is the six
// methods of 05-rbac §3.4's role CRUD and AcceptSeed, so a test can pass the
// real service or a fake.
type RoleService interface {
	Roles(ctx context.Context) iter.Seq2[*rbac.Role, error]
	Role(ctx context.Context, id rbac.RoleName) (*rbac.Role, error)
	CreateRole(ctx context.Context, actor rbac.Checker, in rbac.RoleInput) (*rbac.Role, error)
	UpdateRole(ctx context.Context, actor rbac.Checker, id rbac.RoleName, in rbac.RoleInput, ifVersion int64) (*rbac.Role, error)
	DeleteRole(ctx context.Context, actor rbac.Checker, id rbac.RoleName, ifVersion int64) error
	AcceptSeed(ctx context.Context, actor rbac.Checker, id rbac.RoleName, ifVersion int64) (*rbac.Role, error)
}

// Emitter is the one method of audit.Logger the routes use: the denial a route
// itself writes (05-rbac §8: a 403 dominance_required is audited where it is
// written). audit.Logger satisfies it.
type Emitter interface {
	Emit(ctx context.Context, e audit.Event)
}

// Deps is what Register needs. Roles serves every route (nil makes each
// answer 503). Audit receives the denials a route records itself (nil
// records nothing). Log receives skipped-row warnings (nil discards).
//
// The service records its own changes in the batch that commits them, and
// its own scope_escalation denial; the route records neither, so no refusal is
// written twice.
type Deps struct {
	Roles RoleService
	Audit Emitter
	Log   *slog.Logger
}

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	roles RoleService
	audit Emitter
	log   *slog.Logger
	base  string // the group's base path, for Location and the audit route
}

// Register registers the roles routes on g, which must be the router's
// CoreGroup().
//
// Every write carries Guards.Interactive (05-rbac §4.7: "all writes:
// Guards.Interactive, If-Match on version"). A role grants reach over every
// user who holds it, so no API key, delegated session or auth-disabled
// principal may write one; the key's scopes could never name roles.manage
// anyway (Sensitive), and the guard states the rule where the route is
// declared. No route carries Guards.Owner: the spec does not ask for it.
func Register(g *httpapi.Group, deps Deps) {
	a := &api{roles: deps.Roles, audit: deps.Audit, log: deps.Log, base: g.Base()}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	tags := []string{"roles"}
	view := rbac.Need(permView)
	manage := rbac.Need(permManage)
	write := httpapi.Guards{Interactive: true}

	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/roles", Name: "listRoles", Perm: view, Tags: tags,
		Summary: "List the roles, seed and custom, with their permissions and pending seed additions",
		Doc:     "Newest release additions waiting for an administrator are in pending_seed (05-rbac §4.8). A stored role that cannot be decoded is left out and logged.",
	}, a.list)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/roles/{name}", Name: "getRole", Perm: view, Tags: tags,
		Summary: "Read a role",
		Doc:     "version is what a write names in If-Match.",
	}, a.get)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/roles", Name: "createRole", Perm: manage, Tags: tags, Guards: write,
		MaxBody: maxBody, Success: http.StatusCreated, Summary: "Create a custom role",
		Doc: "name is custom.<slug>. The caller must hold every permission it names (a wildcard needs the pattern itself or core:admin): 403 scope_escalation otherwise. A name in use is 409 role_exists.",
	}, a.create)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPatch, Path: "/roles/{name}", Name: "updateRole", Perm: manage, Tags: tags, Guards: write,
		MaxBody: maxBody, Summary: "Change a custom role's title, description or permissions",
		Doc: "Needs If-Match: the role's version (428 without it, 412 when it is stale). A field the body omits keeps its value; permissions replaces the set. The caller must hold every permission of the old and the new set (403 scope_escalation). admin is 409 role_locked and a seed role 409 seed_role_locked. A change that takes core:admin from the last role that grants it to an active user is 409 last_admin.",
	}, a.update)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodDelete, Path: "/roles/{name}", Name: "deleteRole", Perm: manage, Tags: tags, Guards: write,
		Success: http.StatusNoContent, Summary: "Delete an unassigned custom role",
		Doc: "Needs If-Match: the role's version. A role any user holds is 409 role_in_use: ?reassign= is not built, so take the role from its holders first. admin is 409 role_locked and a seed role 409 seed_role_locked.",
	}, a.remove)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/roles/{name}/accept-seed", Name: "acceptRoleSeed", Perm: manage, Tags: tags, Guards: write,
		Summary: "Make a seed role's pending release additions effective",
		Doc:     "The one write a seed role allows (05-rbac §4.8). Needs If-Match: the role's version. The caller must hold every permission of the resulting set (403 scope_escalation). A role with nothing pending is 409 no_pending_seed.",
	}, a.acceptSeed)
}

// ---- handlers -------------------------------------------------------------

func (a *api) list(ctx context.Context, in *httpapi.In[listReq]) (httpapi.Page[RbacRole], error) {
	if err := a.guard(in.Principal, permView, false); err != nil {
		return httpapi.Page[RbacRole]{}, err
	}
	var all []RbacRole
	for r, err := range a.roles.Roles(ctx) {
		switch {
		case err == nil:
			all = append(all, view(r))
		case errors.Is(err, rbac.ErrUndecodableRole):
			// §6.3: an undecodable row is logged and skipped, and grants
			// nothing. Its error names the row, not its contents.
			a.log.WarnContext(ctx, "rbachttp: a stored role cannot be decoded and is left out of the list", "error", err)
		default:
			// The cause stays in the log; the answer is a fixed 503.
			a.log.ErrorContext(ctx, "rbachttp: listing roles failed", "error", err)
			return httpapi.Page[RbacRole]{}, unavailable()
		}
	}
	return page(all, in.Req.PageParams), nil
}

func (a *api) get(ctx context.Context, in *httpapi.In[roleReq]) (RbacRole, error) {
	if err := a.guard(in.Principal, permView, false); err != nil {
		return RbacRole{}, err
	}
	id, err := roleID(in.Req.Name)
	if err != nil {
		return RbacRole{}, err
	}
	r, err := a.roles.Role(ctx, id)
	if err != nil {
		return RbacRole{}, mapError(err, "")
	}
	return view(r), nil
}

func (a *api) create(ctx context.Context, in *httpapi.In[createReq]) (httpapi.Created[RbacRole], error) {
	var zero httpapi.Created[RbacRole]
	if err := a.guard(in.Principal, permManage, true); err != nil {
		return zero, err
	}
	b := in.Req.Body
	r, err := a.roles.CreateRole(ctx, in.Principal.Perms, rbac.RoleInput{
		Name: rbac.RoleName(b.Name), Title: b.Title, Description: b.Description, Permissions: perms(b.Permissions),
	})
	if err != nil {
		return zero, a.fail(ctx, in.Principal, http.MethodPost, "/roles", permManage, rbac.RoleName(b.Name), err, "/body/permissions")
	}
	return httpapi.Created[RbacRole]{Location: a.base + "/roles/" + url.PathEscape(string(r.Name)), Body: view(r)}, nil
}

func (a *api) update(ctx context.Context, in *httpapi.In[patchReq]) (RbacRole, error) {
	if err := a.guard(in.Principal, permManage, true); err != nil {
		return RbacRole{}, err
	}
	id, err := roleID(in.Req.Name)
	if err != nil {
		return RbacRole{}, err
	}
	ver, err := ifVersion(in.Req.IfMatch)
	if err != nil {
		return RbacRole{}, err
	}
	cur, err := a.roles.Role(ctx, id)
	if err != nil {
		return RbacRole{}, mapError(err, "")
	}
	// The service refuses these two too (it reads the role again under its
	// write lock); answering here keeps the code of a refused edit independent
	// of the body: UpdateRole checks the permission list against the catalog
	// first, and a seed role holding a scope of a disabled module would
	// otherwise answer 422 where 05-rbac §8 says 409 seed_role_locked.
	switch {
	case cur.Locked || id == rbac.RoleAdmin:
		return RbacRole{}, mapError(rbac.ErrLockedRole, "")
	case cur.Origin != rbac.OriginCustom:
		return RbacRole{}, mapError(rbac.ErrSeedRoleLocked, "")
	}
	// PATCH over a service whose UpdateRole replaces the whole role: merge the
	// fields present into the role read above. A concurrent write cannot slip
	// between the read and the write unnoticed: ver is the version the client
	// saw, and the service refuses it (412) unless the role is still at it.
	// A read at a different version than ver is refused the same way, so the
	// merge never mixes two versions.
	next := rbac.RoleInput{Name: id, Title: cur.Title, Description: cur.Description, Permissions: cur.Permissions()}
	b := in.Req.Body
	if b.Title != nil {
		next.Title = *b.Title
	}
	if b.Description != nil {
		next.Description = *b.Description
	}
	permField := ""
	if b.Permissions != nil {
		next.Permissions, permField = perms(*b.Permissions), "/body/permissions"
	}
	r, err := a.roles.UpdateRole(ctx, in.Principal.Perms, id, next, ver)
	if err != nil {
		return RbacRole{}, a.fail(ctx, in.Principal, http.MethodPatch, "/roles/{name}", permManage, id, err, permField)
	}
	return view(r), nil
}

func (a *api) remove(ctx context.Context, in *httpapi.In[versionReq]) (httpapi.NoContent, error) {
	if err := a.guard(in.Principal, permManage, true); err != nil {
		return httpapi.NoContent{}, err
	}
	id, err := roleID(in.Req.Name)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	ver, err := ifVersion(in.Req.IfMatch)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	if err := a.roles.DeleteRole(ctx, in.Principal.Perms, id, ver); err != nil {
		return httpapi.NoContent{}, a.fail(ctx, in.Principal, http.MethodDelete, "/roles/{name}", permManage, id, err, "")
	}
	return httpapi.NoContent{}, nil
}

func (a *api) acceptSeed(ctx context.Context, in *httpapi.In[versionReq]) (RbacRole, error) {
	if err := a.guard(in.Principal, permManage, true); err != nil {
		return RbacRole{}, err
	}
	id, err := roleID(in.Req.Name)
	if err != nil {
		return RbacRole{}, err
	}
	ver, err := ifVersion(in.Req.IfMatch)
	if err != nil {
		return RbacRole{}, err
	}
	r, err := a.roles.AcceptSeed(ctx, in.Principal.Perms, id, ver)
	if err != nil {
		return RbacRole{}, a.fail(ctx, in.Principal, http.MethodPost, "/roles/{name}/accept-seed", permManage, id, err, "")
	}
	return view(r), nil
}

// ---- helpers --------------------------------------------------------------

// roleID is the path's {name}: 404 for one too long to be a role (no role has
// it; the value is not echoed).
func roleID(name string) (rbac.RoleName, error) {
	if len(name) > maxNameLen {
		return "", mapError(rbac.ErrUnknownRole, "")
	}
	return rbac.RoleName(name), nil
}

// fail maps a service refusal to its answer and records the one the route
// itself must: a dominance refusal is audited where the 403 is written (05-rbac
// §8; seams.d/F4-02.md). The service audits scope_escalation itself, in the
// call, so that refusal is not recorded again here. No role write returns
// ErrDominance today (§4.7 rule 5 is auth.Service's, for actions on users);
// the case keeps the rule where the status is written, should one begin to.
func (a *api) fail(ctx context.Context, p *auth.Principal, method, pattern string, perm rbac.Permission, role rbac.RoleName, err error, permField string) error {
	if errors.Is(err, rbac.ErrDominance) && !errors.Is(err, rbac.ErrEscalation) && a.audit != nil {
		ev := audit.Event{
			Action: "core.permission.deny", Outcome: audit.Denied, Permission: string(perm),
			Route: method + " " + a.base + pattern, Reason: "dominance_required",
			Target: entity.Ref{Module: "core", Kind: "role", ID: string(role)},
		}
		// The actor the middleware bound to the context, else the principal
		// the handler was given: either way the event names who was refused.
		if bound, ok := audit.ActorFrom(ctx); ok {
			ev.Actor = bound
		} else if p != nil {
			ev.Actor = actorOf(p)
		}
		a.audit.Emit(ctx, ev)
	}
	return mapError(err, permField)
}

// actorOf is the audit actor of p, for a context no middleware bound one to.
func actorOf(p *auth.Principal) audit.Actor {
	a := audit.Actor{Method: string(p.Method), CredentialID: p.CredentialID}
	if p.User != nil {
		a.UserID, a.Username = string(p.User.ID), p.User.Username
	}
	if p.ClientAddr.IsValid() {
		a.ClientAddr = p.ClientAddr.String()
	}
	return a
}

// page is items[offset:offset+limit] as httpapi's default list shape.
func page[T any](items []T, p httpapi.PageParams) httpapi.Page[T] {
	total := len(items)
	lo := min(max(p.Offset, 0), total)
	hi := min(lo+p.Limit, total)
	out := slices.Clone(items[lo:hi])
	if out == nil {
		out = []T{}
	}
	return httpapi.Page[T]{Items: out, Count: len(out), Limit: p.Limit, Offset: p.Offset, Total: int64(total)}
}
