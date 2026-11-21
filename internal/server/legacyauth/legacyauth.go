// file: internal/server/legacyauth/legacyauth.go
// version: 0.1.0
// guid: 3f8a1c6d-5e24-4b97-9a0e-7c2d6b4f1e83
// last-edited: 2026-11-20

// Package legacyauth is the bridge between core's identity stages and the
// books gin mount (05-http-api §8.3; F4-08 brief item 2). Inside gin, HEAD's
// auth chain keeps its positions and each element becomes an adapter over
// the decision core's stages 12-13 already took:
//
//   - FromCorePrincipal fills HEAD's context values (internal/auth's
//     WithUser, WithPermissions, WithMethod) from the principal stage 12
//     bound. It never authenticates: it reads auth.PrincipalFrom and nothing
//     on the request.
//   - Can, CanReadSide and Check replace HEAD's in-handler auth.Can.
//   - RequirePerm, RequireAdmin, RequireCredentialChange and RequireOwner are
//     the gin adapters of s.perm, RequireAdmin, credGuard and RequireOwner.
//   - ABSRequireAuth is the ABS surface's adapter: it resolves through books'
//     method-bound auth.Compat handle, then CheckCredentialFor.
//
// Nothing calls this package yet: the flip's bridge adoption (F4-08 B7)
// wires it. The atom -> requirement table is books' (05-rbac §6.1) and is
// injected (Resolver), so this package never imports internal/books.
//
// Every check refuses by default: no principal, no checker, an atom the table
// does not know, a resolved requirement of a kind no checker evaluates
// (COR6-1), or a method the bridge does not map. The auth-disabled principal
// gets HEAD's auth-off outcome where 05-rbac §4.11 keeps it and core's where
// it tightens it (see each function).
package legacyauth

import (
	"context"
	"errors"
	"fmt"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Refusals the bridge makes before rbac is asked. A permission refusal is
// rbac's *DeniedError (it matches rbac.ErrDenied).
var (
	// ErrUnauthenticated: no principal is bound, or the one bound cannot be
	// mapped to HEAD's user. The gin adapters answer it 401.
	ErrUnauthenticated = errors.New("legacyauth: no principal")
	// ErrNoChecker: a principal is bound but no checker is; stage 12 binds
	// both, so this is a wiring fault, and it is refused.
	ErrNoChecker = errors.New("legacyauth: no permission checker bound")
	// ErrUnknownAtom: the atom table has no checkable entry for the atom.
	ErrUnknownAtom = errors.New("legacyauth: unknown permission atom")
	// ErrUnmappedMethod: the principal's method has no HEAD equivalent the
	// bridge maps (an identity extension or a compat surface); see legacyMethod.
	ErrUnmappedMethod = errors.New("legacyauth: principal method has no legacy mapping")
	// ErrAuthDisabledDenied: an in-handler atom check of the auth-disabled
	// principal, which HEAD's auth.Can always refused (05-rbac §4.11). It
	// matches rbac.ErrDenied.
	ErrAuthDisabledDenied = fmt.Errorf("%w: the auth-disabled principal holds no legacy atom", rbac.ErrDenied)
)

// Resolver is books' atom table (05-rbac §6.1), injected so the bridge does
// not import internal/books.
type Resolver interface {
	// Requirement is the atom's entry: its route requirement, or with
	// readSide its read-side entry (core:users.view for users.manage), which
	// equals the route requirement for an atom that has none. ok is false
	// for an atom the table does not know.
	Requirement(atom string, readSide bool) (rbac.Requirement, bool)
	// Atoms lists every atom the table maps.
	Atoms() []string
}

// UserLookup reads HEAD's user record of a core user, for the handlers that
// still read internal/auth.UserFromContext. It returns an error, or a nil
// user, when there is none.
type UserLookup func(ctx context.Context, id coreauth.UserID) (*database.User, error)

// OwnerRequired reports core.auth.owner.required as auth.Service resolves it
// (05-authn §4.8). The kernel's value is injected because the service keeps
// the resolution unexported.
type OwnerRequired func() bool

// Bridge carries the injected tables. Its zero value is not usable: build it
// with New.
type Bridge struct {
	atoms         Resolver
	users         UserLookup
	ownerRequired OwnerRequired
}

// New builds a Bridge. atoms and users are required. ownerRequired may be
// nil, which reads as "required": the owner guard then never falls back to
// the interactive admin (RequireOwner).
func New(atoms Resolver, users UserLookup, ownerRequired OwnerRequired) (*Bridge, error) {
	if atoms == nil || users == nil {
		return nil, errors.New("legacyauth: New needs an atom table and a user lookup")
	}
	return &Bridge{atoms: atoms, users: users, ownerRequired: ownerRequired}, nil
}

// resolve is the atom's checkable entry. Only AllOf and AnyOf are accepted:
// a table entry is a set of native scopes, and an entry of any other kind
// (Public or AnyAuthenticated would admit callers the atom never admitted, a
// legacy kind is one no checker evaluates, COR6-1) is refused as unknown.
func (b *Bridge) resolve(atom string, readSide bool) (rbac.Requirement, error) {
	r, ok := b.atoms.Requirement(atom, readSide)
	if !ok {
		return rbac.Requirement{}, fmt.Errorf("%w: %q", ErrUnknownAtom, atom)
	}
	switch r.Kind() {
	case rbac.KindAllOf, rbac.KindAnyOf:
		return r, nil
	default:
		return rbac.Requirement{}, fmt.Errorf("%w: %q resolves to %s", ErrUnknownAtom, atom, r)
	}
}

// authDisabled reports the synthetic principal of core.auth.mode=disabled
// (05-authn §4.11).
func authDisabled(p *coreauth.Principal) bool { return p.Method == coreauth.MethodAuthDisabled }

// legacyMethod maps a core method to HEAD's. The three core credential
// methods carry the same names and meaning at HEAD. Every other method is
// refused (ErrUnmappedMethod) rather than guessed: HEAD's checks mix
// allowlists (Interactive, MayChangeCredentials, opsv3's verifiedForApproval)
// with denylists (APIKeyMayActAsAdmin, the empty-scope key rule, both
// "!= api_key"), so no single legacy value is the narrowest for an unknown
// method, and HEAD records an ABS-surface key as api_key, which a compat
// principal's method does not say. The flip decides cf_access and the ABS
// methods with the information it has.
func legacyMethod(m coreauth.Method) (auth.Method, bool) {
	switch m {
	case coreauth.MethodSession:
		return auth.MethodSession, true
	case coreauth.MethodSessionDelegated:
		return auth.MethodSessionDelegated, true
	case coreauth.MethodAPIKey:
		return auth.MethodAPIKey, true
	}
	return auth.MethodNone, false
}

// FromCorePrincipal returns ctx with HEAD's context values filled from the
// principal stage 12 bound (05-http-api §8.3 RequireAuth row): the user
// (internal/auth.WithUser, from users), the atoms whose table route
// requirement the bound checker satisfies (WithPermissions) and the method
// (WithMethod). It never authenticates.
//
// The auth-disabled principal fills nothing, as HEAD binds no user,
// permission or method with auth off (SEC5-9). A missing principal, a
// principal without a user, a missing checker, an unmapped method, a user
// the lookup cannot find or finds under another ID or not active, are each
// refused with ErrUnauthenticated (or ErrNoChecker / ErrUnmappedMethod, which
// a gin adapter answers 401 too) and nothing is filled.
//
// The atom set is computed with the checker's Satisfies, never with
// rbac.Require: it is a question about every atom, not a refusal, and must
// not audit a denial per atom the caller lacks.
func (b *Bridge) FromCorePrincipal(ctx context.Context) (context.Context, error) {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok {
		return ctx, ErrUnauthenticated
	}
	if authDisabled(p) {
		return ctx, nil
	}
	if p.User == nil {
		return ctx, fmt.Errorf("%w: principal has no user", ErrUnauthenticated)
	}
	method, ok := legacyMethod(p.Method)
	if !ok {
		return ctx, fmt.Errorf("%w: %q", ErrUnmappedMethod, p.Method)
	}
	c, ok := rbac.CheckerFrom(ctx)
	if !ok {
		return ctx, ErrNoChecker
	}
	u, err := b.users(ctx, p.User.ID)
	switch {
	case err != nil:
		return ctx, fmt.Errorf("%w: user lookup: %w", ErrUnauthenticated, err)
	case u == nil || u.ID != string(p.User.ID):
		return ctx, fmt.Errorf("%w: no legacy user record for the principal", ErrUnauthenticated)
	case u.Status != "" && u.Status != "active":
		// HEAD refuses an inactive user (RequireAuth's "inactive user"); a
		// legacy record that disagrees with core's standing refuses too.
		return ctx, fmt.Errorf("%w: legacy user record is not active", ErrUnauthenticated)
	}
	var perms []auth.Permission
	for _, atom := range b.atoms.Atoms() {
		r, err := b.resolve(atom, false)
		if err == nil && c.Satisfies(r) {
			perms = append(perms, atom)
		}
	}
	ctx = auth.WithUser(ctx, u)
	ctx = auth.WithPermissions(ctx, perms)
	return auth.WithMethod(ctx, method), nil
}

// Check is the in-handler check of a legacy atom (05-http-api §8.3 in-handler
// row; 05-tasks §5T.3.7's rbac.LegacyCheck): nil when the caller satisfies
// the atom's table entry (its read-side entry when readSide). It refuses a
// missing principal (ErrUnauthenticated), an unknown atom (ErrUnknownAtom),
// the auth-disabled principal (ErrAuthDisabledDenied: HEAD's auth.Can is false
// with no permission set bound, 05-rbac §4.11; the sites whose HEAD code
// bypasses its check with auth off keep their own bypass), and anything
// rbac.RequireRequirement refuses, no checker bound included. A refusal by
// rbac is audited through the service stage 12 bound.
func (b *Bridge) Check(ctx context.Context, atom string, readSide bool) error {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	r, err := b.resolve(atom, readSide)
	if err != nil {
		return err
	}
	if authDisabled(p) {
		return fmt.Errorf("%w: %q", ErrAuthDisabledDenied, atom)
	}
	return rbac.RequireRequirement(ctx, r)
}

// Can is Check of the atom's route requirement as a bool: the replacement of
// HEAD's raw auth.Can(ctx, atom).
func (b *Bridge) Can(ctx context.Context, atom string) bool { return b.Check(ctx, atom, false) == nil }

// CanReadSide is Can for a site HEAD judged by the atom's read-side successor
// (05-rbac §6.1: core:users.view for users.manage).
func (b *Bridge) CanReadSide(ctx context.Context, atom string) bool {
	return b.Check(ctx, atom, true) == nil
}
