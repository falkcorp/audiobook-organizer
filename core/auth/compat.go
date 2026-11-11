// file: core/auth/compat.go
// version: 1.2.0
// guid: 0b7e6f2d-83c4-4a19-b5d0-7e2a9c41f386
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
)

// Compatibility identities (05-authn §3.1, §4.10; G15).

// Compat is the method-bound handle for an app compatibility surface that
// authenticates itself (ABO: the ABS API, OwnAuth routes; §4.10, SEC-1). A
// module receives it as module.Deps.AuthCompat (06 §6.4.4) only when it
// registered a compat method with Registrar.AuthMethod; every call is bound
// to that method (the principal's Method is it). It gives no credential,
// session or user mutation beyond VerifyPassword's transparent rehash.
// *Service builds one per registered method (RegisterCompat).
type Compat interface {
	// VerifyPassword checks a username and password as core's password
	// sign-in does: the login ceilings and core's one Throttle instance
	// (§4.9, counted with web logins), the hashing semaphore, equal timing
	// for an unknown user (dummy-hash derivation), status,
	// PasswordResetRequired and SignInRestricted (ErrResetRequired), and a
	// transparent rehash of a bcrypt hash. It returns the user, never a
	// hash, and issues no session.
	VerifyPassword(ctx context.Context, username, password string, client ClientInfo) (*User, error)
	// AuthenticateKey applies every stage 12 key rule to a raw API key (an
	// mck_ key or an imported one, §4.1): status (disabled,
	// needs_confirmation and revoked refused), expiry, the owner's status and
	// SignInRestricted, and last-use tracking.
	AuthenticateKey(ctx context.Context, raw string, client ClientInfo) (*Principal, error)
	// PrincipalFor builds the principal for a user the surface resolved
	// itself (an ABS JWT or refresh session, a Cloudflare Access login), as
	// Service.PrincipalFor does for this method. It refuses a user that is
	// not active, whose epoch differs from epoch, or that is SignInRestricted
	// or PasswordResetRequired (ErrResetRequired). It goes through core's one
	// Throttle (§4.9): a call from a throttled source is refused first
	// (ErrThrottled), but none of its own refusals counts toward the
	// throttle: the token was verified by the surface, so a refusal is not a
	// credential guess. scopes are the surface's own narrowing of what the
	// credential may do; a scoped API key is not a surface credential, and a
	// caller holding one must go through AuthenticateKey, which applies the
	// key's stored scopes, status and expiry: scopes passed here are taken
	// as given.
	PrincipalFor(ctx context.Context, u *User, scopes []rbac.Permission, epoch int64, client ClientInfo) (*Principal, error)
	// CheckCredentialFor is Service.CheckCredentialFor (§3.2).
	CheckCredentialFor(p *Principal, perm rbac.Requirement) error
}

// RegisterCompat validates and registers moduleID's compat method spec (P7,
// §5: the kernel hands auth the methods Registrar.AuthMethod recorded) and
// returns the Compat handle bound to it. The method must be
// "<moduleID>.<name>" with a name free of space, colon and slash, must not be
// interactive, may not change credentials and may not verify owners (only an
// app-level extension carries those rights, SEC2-16), must not already be
// registered, and at most one registered method per app carries an AppHandoff
// (§4.7): each refusal is ErrMethodSpec and registers nothing.
func (s *Service) RegisterCompat(moduleID string, spec MethodSpec) (Compat, error) {
	name, ok := strings.CutPrefix(string(spec.Method), moduleID+".")
	switch {
	case moduleID == "" || strings.ContainsAny(moduleID, " :/."):
		return nil, fmt.Errorf("%w: module ID %q", ErrMethodSpec, moduleID)
	case !ok || name == "" || strings.ContainsAny(name, " :/"):
		return nil, fmt.Errorf("%w: method %q is not %s.<name>", ErrMethodSpec, spec.Method, moduleID)
	case spec.Interactive:
		return nil, fmt.Errorf("%w: method %q may not be interactive", ErrMethodSpec, spec.Method)
	case spec.MayChangeCredentials:
		return nil, fmt.Errorf("%w: method %q may not change credentials", ErrMethodSpec, spec.Method)
	case spec.OwnerVerifier:
		return nil, fmt.Errorf("%w: method %q may not verify owners", ErrMethodSpec, spec.Method)
	}
	s.methodsMu.Lock()
	defer s.methodsMu.Unlock()
	if _, dup := s.methods[spec.Method]; dup {
		return nil, fmt.Errorf("%w: method %q registered twice", ErrMethodSpec, spec.Method)
	}
	if spec.AppHandoff != nil {
		for m, other := range s.methods {
			if other.AppHandoff != nil {
				return nil, fmt.Errorf("%w: method %q carries an app hand-off but %q already does", ErrMethodSpec, spec.Method, m)
			}
		}
	}
	s.methods[spec.Method] = spec
	return &compat{s: s, method: spec.Method}, nil
}

// compat is the Compat of one registered method.
type compat struct {
	s      *Service
	method Method
}

func (c *compat) VerifyPassword(ctx context.Context, username, password string, client ClientInfo) (*User, error) {
	s := c.s
	pc, err := s.checkPassword(ctx, LoginInput{Username: username, Password: password, Client: client}, c.method)
	if err != nil {
		return nil, err
	}
	pc.finish(true)
	if pc.rehash {
		s.rehashPassword(ctx, pc.u, pc.hash, password)
	}
	s.d.Audit.Emit(ctx, audit.Event{
		Action: actionLogin, Outcome: audit.Success, Target: ref(refKindUser, string(pc.u.ID)),
		Actor: audit.Actor{
			UserID: string(pc.u.ID), Username: pc.u.Username, Method: string(c.method),
			ClientAddr: addrString(client.Addr), UserAgent: client.UserAgent,
		},
	})
	return pc.u, nil
}

func (c *compat) AuthenticateKey(ctx context.Context, raw string, client ClientInfo) (*Principal, error) {
	s := c.s
	source := client.sourceKey()
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		return nil, s.throttledOn(surfaceLogin, LoginRetryAfter)
	}
	p, refusal, err := s.resolveKey(ctx, raw, client, c.method)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if refusal != nil {
		// Only an invalid key is a credential guess; a revoked, expired or
		// restricted one is a stale credential, which a phone app polling
		// after a restore must not be locked out for (§4.9).
		if errors.Is(refusal, ErrInvalidCredentials) {
			th.RecordFailure("", source)
		}
		return nil, refusal
	}
	p.ClientAddr = client.Addr
	return p, nil
}

func (c *compat) PrincipalFor(ctx context.Context, u *User, scopes []rbac.Permission, epoch int64, client ClientInfo) (*Principal, error) {
	s := c.s
	if s.loginThrottle().SourceBlocked(client.sourceKey()) {
		return nil, s.throttledOn(surfaceLogin, LoginRetryAfter)
	}
	if u == nil || u.ID == "" {
		return nil, fmt.Errorf("%w: no user", ErrInvalidCredentials)
	}
	// The roles come from the store, never from the caller's value: a
	// module holds no way to hand core a user with roles it does not have.
	cur, err := s.d.Store.GetUser(ctx, u.ID)
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrUndecodableRecord):
		return nil, fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if cur.PasswordResetRequired {
		return nil, fmt.Errorf("%w: %s", ErrResetRequired, cur.ID)
	}
	p, err := s.PrincipalFor(ctx, cur, c.method, scopes, epoch)
	if err != nil {
		return nil, err
	}
	p.ClientAddr = client.Addr
	return p, nil
}

func (c *compat) CheckCredentialFor(p *Principal, perm rbac.Requirement) error {
	if p != nil && p.Method != c.method {
		return fmt.Errorf("%w: a principal of %q presented to %q's handle", ErrInvalidCredentials, p.Method, c.method)
	}
	return c.s.CheckCredentialFor(p, perm)
}
