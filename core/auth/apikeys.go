// file: core/auth/apikeys.go
// version: 1.0.0
// guid: 61144126-f293-45db-8152-bf01124f696a
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// CreateAPIKey mints an mck_ key (§4.4). spec.Scopes is required, non-empty
// and wildcard-free, and every scope must be held, at this moment, by the
// actor's effective set: its roles as stored now, intersected with its own
// key's scopes when it presented a key. An empty effective set admits no
// scope, so a key whose owner lost the roles behind its scopes mints nothing
// (early fix 2, SEC9-5). A key actor mints only for itself and no later than
// its own key expires. A key for another user needs core:users.manage
// through an interactive method, dominance over the target, and scopes the
// target's roles hold too.
func (s *Service) CreateAPIKey(ctx context.Context, actor *Principal, spec KeySpec) (string, *APIKey, error) {
	self, err := actorUser(actor)
	if err != nil {
		return "", nil, err
	}
	// Refused here, before any Compile: a nil or empty list is a session's
	// "every role permission" to rbac, never a key's (F4-02 rule).
	if len(spec.Scopes) == 0 {
		return "", nil, fmt.Errorf("%w: an API key needs at least one scope", ErrBadInput)
	}
	scopes := slices.Clone(spec.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	if err := rbac.CheckKeyScopes(s.d.RBAC.Catalog(), scopes); err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrBadInput, err)
	}
	st := s.settings()
	ttl := spec.ExpiresIn
	switch {
	case ttl < 0:
		return "", nil, fmt.Errorf("%w: a negative expiry", ErrBadInput)
	case ttl == 0:
		ttl = st.KeyDefaultTTL
	case ttl > st.KeyMaxTTL:
		return "", nil, fmt.Errorf("%w: expiry %s exceeds the maximum %s", ErrBadInput, ttl, st.KeyMaxTTL)
	}
	now := s.now().UTC()
	expires := now.Add(ttl)
	owner := spec.Owner
	if owner == "" {
		owner = self
	}

	// The actor's effective set now: fresh roles, and the fresh key it
	// presented (a key revoked or narrowed since the request began counts as
	// it is now).
	held, keyExpiry, err := s.effectiveSet(ctx, actor)
	if err != nil {
		return "", nil, err
	}
	if actor.Method == MethodAPIKey {
		if owner != self {
			return "", nil, fmt.Errorf("%w: a key-authenticated caller mints keys only for itself", ErrDenied)
		}
		if expires.After(keyExpiry) {
			expires = keyExpiry
		}
	}
	for _, p := range scopes {
		if !held.Has(p) || !actor.Perms.Has(p) {
			return "", nil, fmt.Errorf("%w: %s", ErrScopeEscalation, p)
		}
	}
	if owner != self {
		if err := s.mayManage(ctx, actor, owner); err != nil {
			return "", nil, err
		}
	}
	target, err := s.d.Store.GetUser(ctx, owner)
	if err != nil {
		return "", nil, err
	}
	if target.Status != StatusActive {
		return "", nil, fmt.Errorf("%w: %s", ErrInactiveUser, owner)
	}
	if owner != self {
		roles := s.d.RBAC.Compile(rbac.CheckerInput{Roles: target.Roles})
		for _, p := range scopes {
			if !roles.Has(p) {
				return "", nil, fmt.Errorf("%w: %s is outside the target's roles", ErrScopeEscalation, p)
			}
		}
	}

	key := &APIKey{Owner: owner, Name: spec.Name, Scopes: scopes, Status: KeyActive, CreatedAt: now, ExpiresAt: expires}
	var raw string
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		k := *key
		k.Scopes = slices.Clone(scopes)
		r, err := s.d.Store.IssueKey(b, &k)
		if err != nil {
			return err
		}
		if err := s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionAPIKeyCreate, Outcome: audit.Success, Target: ref(refKindAPIKey, k.ID),
			Details: map[string]any{"owner": string(owner), "scopes": scopeStrings(scopes)},
		}); err != nil {
			return err
		}
		raw, *key = string(r), k
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return raw, key, nil
}

// effectiveSet compiles actor's effective set from the store as it is now:
// the user's stored roles and, for a key actor, its stored key's scopes. It
// also returns the presented key's expiry (zero for other methods).
func (s *Service) effectiveSet(ctx context.Context, actor *Principal) (rbac.Checker, time.Time, error) {
	u, err := s.d.Store.GetUser(ctx, actor.User.ID)
	if err != nil {
		return nil, time.Time{}, err
	}
	if u.Status != StatusActive {
		return nil, time.Time{}, fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	}
	in := rbac.CheckerInput{Roles: u.Roles, Interactive: s.interactive(actor.Method)}
	var expiry time.Time
	if actor.Method == MethodAPIKey {
		k, err := s.d.Store.APIKeyByID(ctx, actor.CredentialID)
		if err != nil {
			return nil, time.Time{}, err
		}
		if err := keyUsable(k, s.now()); err != nil {
			return nil, time.Time{}, err
		}
		if len(k.Scopes) == 0 { // never stored (R3-1); refused before Compile all the same
			return nil, time.Time{}, fmt.Errorf("%w: key %s has no scope", ErrInvalidCredentials, k.ID)
		}
		in.Scopes, expiry = k.Scopes, k.ExpiresAt
	}
	return s.d.RBAC.Compile(in), expiry, nil
}

// keyUsable is the key half of stage 12's success rule: active and
// unexpired.
func keyUsable(k *APIKey, now time.Time) error {
	switch k.Status {
	case KeyActive:
	case KeyRevoked:
		return fmt.Errorf("%w: key %s", ErrRevoked, k.ID)
	default:
		return fmt.Errorf("%w: key %s is %s", ErrRevoked, k.ID, k.Status)
	}
	if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
		return fmt.Errorf("%w: key %s", ErrExpired, k.ID)
	}
	return nil
}

// APIKeys lists owner's keys ("" lists every key). It never yields a secret.
func (s *Service) APIKeys(ctx context.Context, owner UserID) iter.Seq2[*APIKey, error] {
	return s.d.Store.ScanAPIKeys(ctx, owner)
}

// SetAPIKeyStatus changes a key's status, monotone toward revoked (§4.4):
// revoked is terminal (ErrKeyRevoked for any other target), and
// needs_confirmation goes only to active or revoked; needs_confirmation
// itself is set only by the post-restore hook. A change away from active
// appends its journal line into the same batch before commit (§4.12). A
// write that raced a last-use touch is re-read and re-staged, so the
// revocation always lands (early fix 3, SEC6-3).
//
// Who may write (§4.13's key rows): the key's owner, or another user with
// core:users.manage through an interactive method and dominance (mayManage,
// which therefore refuses a key actor any other owner's key). Two rules
// more, so a key never widens what its owner's other credentials reach:
//   - a key actor manages only keys it may create (§4.4, §4.13): every scope
//     of the target is held by its effective set as stored now, intersected
//     with its own key's scopes (ErrScopeEscalation), whatever the change;
//   - any change into active — re-enabling a disabled key, a manager's
//     disable included, or confirming a restored one — needs an interactive
//     method (ErrDenied), as HEAD's credential guard on PATCH required.
//
// Disabling or revoking needs neither interactivity nor more than ownership
// for a session: a restriction never widens anything.
func (s *Service) SetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error {
	if _, err := actorUser(actor); err != nil {
		return err
	}
	if !st.valid() || st == KeyNeedsConfirmation {
		return fmt.Errorf("%w: key status %q", ErrBadInput, st)
	}
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		k, err := s.d.Store.APIKeyByID(ctx, id)
		if err != nil {
			return err
		}
		if err := s.mayManage(ctx, actor, k.Owner); err != nil {
			return err
		}
		if actor.Method == MethodAPIKey {
			if err := s.keyMayManage(ctx, actor, k); err != nil {
				return err
			}
		}
		if err := statusTransition(k.Status, st); err != nil {
			return err
		}
		if k.Status == st {
			return nil
		}
		if st == KeyActive && !s.interactive(actor.Method) {
			return fmt.Errorf("%w: re-enabling a key needs an interactive sign-in", ErrDenied)
		}
		next := *k
		next.Status, next.Version = st, k.Version+1
		if st == KeyRevoked {
			next.RevokedAt = s.now().UTC()
		}
		if err := s.d.Store.PutAPIKey(b, &next); err != nil {
			return err
		}
		switch st {
		case KeyRevoked:
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: ref(refKindAPIKey, k.ID)})
		case KeyDisabled:
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.APIKeyDisabled, Subject: ref(refKindAPIKey, k.ID)})
		}
		// A revocation is its own action (05-rbac §8: core.apikey.revoke);
		// every other change is core.apikey.set_status.
		action := actionAPIKeyStatus
		if st == KeyRevoked {
			action = actionAPIKeyRevoke
		}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: action, Outcome: audit.Success, Target: ref(refKindAPIKey, k.ID),
			Details: map[string]any{"from": string(k.Status), "to": string(st)},
		})
	})
}

// keyMayManage is §4.4's "a key may manage only keys it may create" for a
// key actor over k, a key of its own owner: every scope of k is held by the
// actor's effective set now (its stored roles intersected with its stored
// key's scopes) and by the checker it presented, the test CreateAPIKey
// applies to a new key's scopes.
func (s *Service) keyMayManage(ctx context.Context, actor *Principal, k *APIKey) error {
	held, _, err := s.effectiveSet(ctx, actor)
	if err != nil {
		return err
	}
	for _, p := range k.Scopes {
		if !held.Has(p) || !actor.Perms.Has(p) {
			return fmt.Errorf("%w: key %s holds %s, which the managing key could not create", ErrScopeEscalation, k.ID, p)
		}
	}
	return nil
}

// RevokeAPIKey is SetAPIKeyStatus(…, KeyRevoked).
func (s *Service) RevokeAPIKey(ctx context.Context, actor *Principal, id string) error {
	return s.SetAPIKeyStatus(ctx, actor, id, KeyRevoked)
}

// statusTransition is §4.4's transition rule.
func statusTransition(from, to KeyStatus) error {
	switch {
	case from == to:
		return nil
	case from == KeyRevoked:
		return fmt.Errorf("%w: a revoked key stays revoked", ErrKeyRevoked)
	case from == KeyNeedsConfirmation && to != KeyActive && to != KeyRevoked:
		return fmt.Errorf("%w: an unconfirmed key goes only to active or revoked", ErrKeyRevoked)
	}
	return nil
}

// CheckCredentialFor applies, for a surface that authenticates itself (an
// OwnAuth route, which stages 12-13 skip), what stage 12/13 would have
// applied to p on a route requiring perm: a key principal's key must still
// be active and unexpired, and a Confined() principal satisfies only a
// requirement naming one of its scopes (SEC2-4; 05-http-api §8.3).
func (s *Service) CheckCredentialFor(p *Principal, perm rbac.Requirement) error {
	if p == nil || p.Perms == nil {
		return fmt.Errorf("%w: no principal", ErrInvalidCredentials)
	}
	if p.Method == MethodAPIKey {
		k, err := s.d.Store.APIKeyByID(context.Background(), p.CredentialID)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: key %s", ErrRevoked, p.CredentialID)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if err := keyUsable(k, s.now()); err != nil {
			return err
		}
	}
	if p.Perms.Confined() && !p.Perms.Satisfies(perm) {
		return fmt.Errorf("%w: %s", ErrConfined, perm)
	}
	return nil
}

func scopeStrings(ps []rbac.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
