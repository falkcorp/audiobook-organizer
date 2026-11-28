// file: core/coretest/identity.go
// version: 0.1.0
// guid: 5e2b9d47-1c63-4a80-b7f4-9d0a3e68c215
// last-edited: 2026-11-21

package coretest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
)

// This file is coretest's authentication half (06 §6.4.8, F4-08 B5): WithUser's
// principals, As's credential and AssertPermission's ephemeral users, all made
// through core auth and rbac's own services over the identity boot the harness
// declares (boot in harness.go). Nothing here reaches around them: a user is a
// stored auth user holding a stored custom role, and the credential As hands a
// Client is an interactive session core auth issued for that user, so stage 12
// binds the same principal a browser sign-in would and stage 13 judges it.

// Fixed names of the harness's own actor and ephemeral users. They are
// deterministic so a Reboot over the same store finds them again instead of
// failing on a taken name.
const (
	actorName    = "coretest-actor"
	lackingName  = "coretest-lacking"
	holdingStem  = "coretest-holding-"
	roleSlugStem = "coretest-"
)

// identity is the harness's hold on the two services the identity boot built.
type identity struct {
	auth  *auth.Service
	rbac  *rbac.Service
	actor *auth.Principal // the administrator the harness seeds as; never handed to a test
	probe auth.ClientInfo // the client info the session cookie is named for
}

// newIdentity reads the services off the kernel and builds the seeding actor.
//
// The actor is core:admin through an interactive credential, compiled by the
// rbac service from its own admin role: it exists only so the services' own
// rules (CreateRole's holder check, CreateUser's users.manage and CanAssign)
// pass for the harness. It is not a stored user, no request carries it, and a
// test can reach it through neither As nor Anonymous.
func newIdentity(k *kernel.Kernel) (*identity, error) {
	a, r, ok := k.IdentityServices()
	if !ok {
		return nil, errors.New("coretest: the App built no identity services (kernel.Config.IdentityBoot is off)")
	}
	perms := r.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if perms == nil || !perms.Has(rbac.CoreAdmin) {
		return nil, errors.New("coretest: the rbac service compiled no administrator checker (the admin seed role is missing)")
	}
	probe := httptest.NewRequest(http.MethodGet, "/", nil)
	return &identity{
		auth: a,
		rbac: r,
		actor: &auth.Principal{
			User:   &auth.User{ID: actorName, Username: actorName, Status: auth.StatusActive},
			Method: auth.MethodSession,
			Perms:  perms,
		},
		probe: httpapi.ClientInfoFrom(probe),
	}, nil
}

// seed stores a user named name holding exactly perms and returns a session
// cookie for it. perms is deduplicated; none means a user with no role at
// all, which holds nothing.
//
// The role is custom.coretest-<digest of the sorted set>: one role per
// distinct set, shared by every user of that set, and found again on a Reboot.
// A role or user that already exists is reused only when it holds exactly
// what was asked: a stale one with other permissions is an error, never a
// silent grant.
func (id *identity) seed(ctx context.Context, name string, perms []rbac.Permission) (*http.Cookie, error) {
	set := slices.Clone(perms)
	slices.Sort(set)
	set = slices.Compact(set)
	var roles []rbac.RoleName
	if len(set) > 0 {
		role, err := id.role(ctx, set)
		if err != nil {
			return nil, err
		}
		roles = []rbac.RoleName{role}
	}
	u, err := id.auth.CreateUser(ctx, id.actor, auth.NewUser{Username: name, Roles: roles})
	switch {
	case errors.Is(err, auth.ErrUserExists):
		if u, err = id.auth.UserByUsername(ctx, name); err != nil {
			return nil, fmt.Errorf("coretest: user %q exists but cannot be read: %w", name, err)
		}
		if !slices.Equal(u.Roles, roles) {
			return nil, fmt.Errorf("coretest: user %q already exists with roles %v, not %v", name, u.Roles, roles)
		}
	case err != nil:
		return nil, fmt.Errorf("coretest: create user %q: %w", name, err)
	}
	// OriginPassword is the user's own interactive sign-in: auth.MethodSession,
	// the one core method that is Interactive, so an Interactive guard treats
	// the Client as a person at a browser. A delegated origin (temp login,
	// invite) would refuse every such guard.
	issued, err := id.auth.IssueSession(ctx, u, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		return nil, fmt.Errorf("coretest: open a session for %q: %w", name, err)
	}
	return id.auth.SessionCookie(id.probe, issued.Token, issued.ExpiresAt), nil
}

// role is the custom role holding exactly set (sorted, deduplicated, not
// empty), created if absent.
func (id *identity) role(ctx context.Context, set []rbac.Permission) (rbac.RoleName, error) {
	sum := sha256.Sum256([]byte(joinPerms(set)))
	name := rbac.RoleName("custom." + roleSlugStem + hex.EncodeToString(sum[:6]))
	_, err := id.rbac.CreateRole(ctx, id.actor.Perms, rbac.RoleInput{
		Name: name, Title: "coretest " + joinPerms(set), Permissions: set,
	})
	switch {
	case err == nil:
		return name, nil
	case errors.Is(err, rbac.ErrRoleExists):
	default:
		return "", fmt.Errorf("coretest: create the role for %v: %w", set, err)
	}
	r, err := id.rbac.Role(ctx, name)
	if err != nil {
		return "", fmt.Errorf("coretest: read role %s: %w", name, err)
	}
	have := slices.Clone(r.Permissions())
	slices.Sort(have)
	if !slices.Equal(slices.Compact(have), set) {
		return "", fmt.Errorf("coretest: role %s already exists holding %v, not %v", name, have, set)
	}
	return name, nil
}

func joinPerms(set []rbac.Permission) string {
	s := make([]string, len(set))
	for i, p := range set {
		s[i] = string(p)
	}
	return strings.Join(s, ",")
}

// seedAll seeds WithUser's principals, in name order, and records each cookie.
func (h *Harness) seedAll() error {
	for _, name := range sortedKeys(h.users) {
		ck, err := h.id.seed(h.tb.Context(), name, h.users[name])
		if err != nil {
			return fmt.Errorf("WithUser(%q): %w", name, err)
		}
		h.cookies[name] = ck
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ephemeral is a client for a user seeded now, for AssertPermission: a user
// nobody named, holding exactly perms.
func (h *Harness) ephemeral(t testing.TB, name string, perms ...rbac.Permission) *Client {
	t.Helper()
	ck, err := h.id.seed(t.Context(), name, perms)
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	return &Client{handler: h.app.Handler(), cookie: ck}
}

// holdingName is the ephemeral user holding exactly perm.
func holdingName(perm rbac.Permission) string {
	sum := sha256.Sum256([]byte(perm))
	return holdingStem + hex.EncodeToString(sum[:6])
}
