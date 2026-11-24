// file: core/auth/admin_keys.go
// version: 0.1.0
// guid: 3d6a8f12-b47c-4e95-a0d8-2c71e5f9b406
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/rbac"
)

// KeyAdmin is the API-key half of the actor-checked identity participant a
// module is handed (F4-08 B10z): the key routes a legacy app keeps under its
// own paths (ABO's /api/v1/auth/api-keys) serve through it once its accounts
// are core's. It is embedded in Admin.
//
// Every method acts as the principal bound on ctx (ctxActor): actor must be
// that principal, so holding the handle grants nothing, and each one applies
// the rule core's own key routes apply (authhttp keys.go, me.go). No method
// returns a hash or a raw key except AdminCreateAPIKey, once.
type KeyAdmin interface {
	// AdminCreateAPIKey is CreateAPIKey made as the principal bound on ctx:
	// scopes held by its effective set now, a key actor only for itself and
	// no later than its own key expires, another owner only through
	// core:users.manage, an interactive method and dominance, with scopes the
	// target's roles hold.
	AdminCreateAPIKey(ctx context.Context, actor *Principal, spec KeySpec) (string, *APIKey, error)
	// AdminSetAPIKeyStatus and AdminRevokeAPIKey are SetAPIKeyStatus and
	// RevokeAPIKey made as the principal bound on ctx.
	AdminSetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error
	AdminRevokeAPIKey(ctx context.Context, actor *Principal, id string) error
	// AdminAPIKeys lists the keys the actor may read: its own, and with all
	// set, those of every owner whose roles it dominates while it holds
	// core:users.manage (a key whose owner is gone only for core:admin), the
	// rule of GET /api/core/v1/api-keys. With all set and no
	// core:users.manage it lists its own. The actor check runs inside the
	// sequence: a refusal is its only element. A record the store yields as
	// an error (an undecodable one, ErrUndecodableRecord) is passed on, and
	// the listing goes on while the caller keeps ranging, so one bad record
	// does not hide the rest.
	AdminAPIKeys(ctx context.Context, actor *Principal, all bool) iter.Seq2[*APIKey, error]
	// AdminAPIKey is one key by ID under the same rule. A missing key is
	// ErrNotFound; a key that exists but that the actor may not read is
	// ErrDenied, so a legacy route that answered 404 and 403 apart (HEAD's
	// GET /api/v1/auth/api-keys/:id) keeps doing so. Core's own route
	// answers both 404.
	AdminAPIKey(ctx context.Context, actor *Principal, id string) (*APIKey, error)
	// KeyOwnerScopes is the most a key for owner may hold: owner's stored
	// roles as a role-only checker compiles them (its Concrete scopes, and
	// core:admin when the roles grant it). The actor may ask for itself, or
	// for another user under mayManage (core:users.manage through an
	// interactive method and dominance). An app that maps its own legacy
	// scope names onto core's reads the owner's set here.
	KeyOwnerScopes(ctx context.Context, actor *Principal, owner UserID) ([]rbac.Permission, error)
}

var _ KeyAdmin = (*Service)(nil)

// AdminCreateAPIKey is KeyAdmin.AdminCreateAPIKey.
func (s *Service) AdminCreateAPIKey(ctx context.Context, actor *Principal, spec KeySpec) (string, *APIKey, error) {
	p, err := s.ctxActor(ctx, actor)
	if err != nil {
		return "", nil, err
	}
	return s.CreateAPIKey(ctx, p, spec)
}

// AdminSetAPIKeyStatus is KeyAdmin.AdminSetAPIKeyStatus.
func (s *Service) AdminSetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error {
	p, err := s.ctxActor(ctx, actor)
	if err != nil {
		return err
	}
	return s.SetAPIKeyStatus(ctx, p, id, st)
}

// AdminRevokeAPIKey is KeyAdmin.AdminRevokeAPIKey.
func (s *Service) AdminRevokeAPIKey(ctx context.Context, actor *Principal, id string) error {
	p, err := s.ctxActor(ctx, actor)
	if err != nil {
		return err
	}
	return s.RevokeAPIKey(ctx, p, id)
}

// keyReader decides, once per owner, whether p may read that owner's keys.
type keyReader struct {
	s       *Service
	p       *Principal
	verdict map[UserID]bool
}

func (s *Service) keyReaderFor(ctx context.Context, actor *Principal) (*keyReader, error) {
	p, err := s.ctxActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	self, err := actorUser(p)
	if err != nil {
		return nil, err
	}
	return &keyReader{s: s, p: p, verdict: map[UserID]bool{self: true}}, nil
}

// manager reports whether p reads other owners' keys at all.
func (r *keyReader) manager() bool { return r.p.Perms.Has(permUsersManage) }

// may reports whether p may read owner's keys: its own; another owner's only
// for core:users.manage and dominance over the owner's roles; an owner that
// no longer exists only for core:admin.
func (r *keyReader) may(ctx context.Context, owner UserID) (bool, error) {
	if ok, known := r.verdict[owner]; known {
		return ok, nil
	}
	ok := false
	if r.manager() {
		u, err := r.s.d.Store.GetUser(ctx, owner)
		switch {
		case errors.Is(err, ErrNotFound) || errors.Is(err, ErrUndecodableRecord):
			ok = r.p.Perms.Has(rbac.CoreAdmin)
		case err != nil:
			return false, err
		default:
			ok = r.s.d.RBAC.Dominates(r.p.Perms, u.Roles) == nil
		}
	}
	r.verdict[owner] = ok
	return ok, nil
}

// AdminAPIKeys is KeyAdmin.AdminAPIKeys.
func (s *Service) AdminAPIKeys(ctx context.Context, actor *Principal, all bool) iter.Seq2[*APIKey, error] {
	return func(yield func(*APIKey, error) bool) {
		r, err := s.keyReaderFor(ctx, actor)
		if err != nil {
			yield(nil, err)
			return
		}
		owner := r.p.User.ID
		if all && r.manager() {
			owner = ""
		}
		for k, err := range s.d.Store.ScanAPIKeys(ctx, owner) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			ok, err := r.may(ctx, k.Owner)
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if ok && !yield(k, nil) {
				return
			}
		}
	}
}

// AdminAPIKey is KeyAdmin.AdminAPIKey.
func (s *Service) AdminAPIKey(ctx context.Context, actor *Principal, id string) (*APIKey, error) {
	r, err := s.keyReaderFor(ctx, actor)
	if err != nil {
		return nil, err
	}
	k, err := s.d.Store.APIKeyByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ok, err := r.may(ctx, k.Owner)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: api key %s belongs to a user whose keys the caller may not read", ErrDenied, id)
	}
	return k, nil
}

// KeyOwnerScopes is KeyAdmin.KeyOwnerScopes.
func (s *Service) KeyOwnerScopes(ctx context.Context, actor *Principal, owner UserID) ([]rbac.Permission, error) {
	p, err := s.ctxActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if err := s.mayManage(ctx, p, owner); err != nil {
		return nil, err
	}
	u, err := s.d.Store.GetUser(ctx, owner)
	if err != nil {
		return nil, err
	}
	roles := s.d.RBAC.Compile(rbac.CheckerInput{Roles: u.Roles})
	out := roles.Concrete()
	if roles.Has(rbac.CoreAdmin) {
		out = append(out, rbac.CoreAdmin)
	}
	return out, nil
}
