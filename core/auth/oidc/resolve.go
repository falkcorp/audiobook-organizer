// file: core/auth/oidc/resolve.go
// version: 1.0.1
// guid: 1720bb8f-3301-4a84-b4b5-d3dd31f6c634
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// RoleReader reads one stored role. *rbac.Service is one. An unknown role is
// rbac.ErrUnknownRole.
type RoleReader interface {
	Role(ctx context.Context, name rbac.RoleName) (*rbac.Role, error)
}

// AccountLock is the account-write seam of *auth.Service (auth/external.go):
// the e-mail lock every account creator holds, and the first-run flag.
type AccountLock interface {
	LockEmails() (unlock func())
	NoteUserCreated()
}

// ResolverDeps are the resolver's collaborators. The resolver writes the
// identity Store itself, in batches that carry their audit record, because an
// OIDC sign-in has no actor.
type ResolverDeps struct {
	Store    auth.Store
	NewBatch func() *storage.Batch
	Audit    audit.Logger
	Roles    RoleReader
	Accounts AccountLock
	// ExtraAllowed, when set, is consulted after the provider's allowlist and
	// the invite allow-list miss. It must fail closed: an error or a doubt is
	// a refusal.
	ExtraAllowed func(ctx context.Context, email string) (bool, error)
	Log          *slog.Logger
	Clock        func() time.Time
}

// Resolver turns a provider's verified claims into a local user, applying
// admission and linking in the order of 05-authn §4.7. It has no state.
type Resolver struct {
	d       ResolverDeps
	refused *refusals
}

// NewResolver validates d.
func NewResolver(d ResolverDeps) (*Resolver, error) {
	switch {
	case d.Store == nil || d.NewBatch == nil || d.Audit == nil || d.Roles == nil || d.Accounts == nil:
		return nil, errors.New("oidc: a resolver needs Store, NewBatch, Audit, Roles and Accounts")
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &Resolver{d: d, refused: newRefusals()}, nil
}

const (
	auditModule    = "core"
	conflictTries  = 4
	maxUsernameTry = 100

	actionLink     = "core.identity.link"
	actionDisplace = "core.identity.displace"
	actionCreate   = "core.user.create"
)

// Resolve is the gate every sign-in from a provider passes, in order:
//
//  1. The provider must have verified the address (never trust an unverified
//     one), the address must be plain ASCII that NormalizeEmail leaves alone
//     but for case (ErrEmailNotPlain: a Kelvin-sign or fullwidth look-alike is
//     another person's address and must never link, displace or be admitted
//     as the ASCII one), and it must be admitted: on the policy's allowlist
//     (an address or a domain), invited, or approved by ExtraAllowed. Verified
//     is not authorized, so an identity not admitted creates nothing. A refusal
//     that depends on stored state (not admitted, address held) is remembered
//     per (provider, subject, address) for RefusalTTL and answered again with
//     no lock, no scan and no audit event.
//  2. A subject already linked, by (provider, subject), is that user.
//  3. An unseen subject whose address another account holds VERIFIED takes
//     that account only when the policy trusts the provider for it
//     (LinkExistingByEmail) and the account holds no core:admin and has no
//     password (SEC-7). Otherwise the sign-in is refused: auth.ErrEmailInUse.
//  4. Else a user with the policy's default role (which may not grant
//     core:admin) is created and linked. Accounts that only typed the address
//     in, unverified, lose it to the verified sign-in, when the provider is
//     trusted and the account passes the same guard; every such change is
//     audited as core.identity.displace, with account ids and never the address.
//
// Whoever is returned may sign in: it is active and not SignInRestricted
// (auth.ErrInactiveUser, auth.ErrResetRequired otherwise).
func (r *Resolver) Resolve(ctx context.Context, pol Policy, c Claims) (*auth.User, error) {
	if c.Provider != pol.Provider || pol.Provider == "" || c.Subject == "" {
		return nil, fmt.Errorf("%w: claims do not belong to provider %q", ErrExchange, pol.Provider)
	}
	email, plain := plainEmail(c.Email)
	if !c.EmailVerified {
		return nil, ErrEmailNotVerified
	}
	if !plain {
		return nil, ErrEmailNotPlain
	}
	if email == "" {
		return nil, ErrEmailNotVerified
	}
	key := refusalKey(pol.Provider, c.Subject, email)
	if err := r.refused.get(key, r.d.Clock()); err != nil {
		return nil, err
	}
	ok, err := r.admitted(ctx, pol, email)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	if !ok {
		r.refused.put(key, ErrEmailNotAllowed, r.d.Clock())
		return nil, ErrEmailNotAllowed
	}
	// A subject already linked, whose account needs no write, is decided
	// without the e-mail lock: every Cloudflare Access request of a signed-in
	// browser takes this path, and it must not queue behind account creation.
	if u, done, err := r.linkedFast(ctx, pol, c, email); done || err != nil {
		return u, err
	}
	// One lock for the whole decision: "no other account holds this address
	// verified" holds only against every other creator (CreateUser,
	// AcceptInvite) when they all take it.
	unlock := r.d.Accounts.LockEmails()
	defer unlock()
	// Requests that queued on the lock while the first of them decided: the
	// refusal it remembered is theirs too, with no scan and no second audit.
	if err := r.refused.get(key, r.d.Clock()); err != nil {
		return nil, err
	}
	var u *auth.User
	err = storage.RetryOnConflict(ctx, conflictTries, func(ctx context.Context) error {
		var rerr error
		u, rerr = r.resolveLocked(ctx, pol, c, email)
		return rerr
	})
	if errors.Is(err, auth.ErrEmailInUse) {
		r.refused.put(key, err, r.d.Clock())
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// plainEmail is the claim's address in the one form this package compares: it
// must be printable ASCII (no byte <= ' ' or > '~', IsOwnerEmail's rule) and
// equal to NormalizeEmail(claim) after ASCII lowercasing. NormalizeEmail folds
// compatibility characters onto ASCII (the Kelvin sign, the long s, fullwidth
// letters), so without this a different account at the provider reads as
// another person's address and could link, displace or be admitted as them.
// An empty claim is ("", true) and is the caller's to refuse.
func plainEmail(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= ' ' || raw[i] > '~' {
			return "", false
		}
	}
	lower := strings.ToLower(raw)
	if lower != auth.NormalizeEmail(raw) {
		return "", false
	}
	return lower, true
}

// admitted reports whether email may sign in through pol.
func (r *Resolver) admitted(ctx context.Context, pol Policy, email string) (bool, error) {
	if slices.ContainsFunc(pol.AllowedEmails, func(a string) bool { return auth.NormalizeEmail(a) == email }) {
		return true, nil
	}
	if _, domain, found := strings.Cut(email, "@"); found && domain != "" {
		for _, d := range pol.AllowedDomains {
			if d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@")); d != "" && d == domain {
				return true, nil
			}
		}
	}
	if inv, err := r.d.Store.InviteAllowed(ctx, email); err != nil {
		return false, err
	} else if inv {
		return true, nil
	}
	if r.d.ExtraAllowed != nil {
		return r.d.ExtraAllowed(ctx, email)
	}
	return false, nil
}

func (r *Resolver) resolveLocked(ctx context.Context, pol Policy, c Claims, email string) (*auth.User, error) {
	id, err := r.d.Store.IdentityBySubject(ctx, pol.Provider, c.Subject)
	switch {
	case err == nil:
		return r.linked(ctx, id, email)
	case !errors.Is(err, auth.ErrNotFound):
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	var verified, unverified []*auth.User
	for u, err := range r.d.Store.ScanUsers(ctx) {
		if errors.Is(err, auth.ErrUndecodableRecord) {
			// A damaged record may hold the address verified; it cannot be
			// ruled out, so nothing is linked or created beside it.
			return nil, fmt.Errorf("%w: a stored account cannot be read", auth.ErrEmailInUse)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
		}
		if !auth.SameEmail(u.Email, email) {
			continue
		}
		if u.EmailVerified {
			verified = append(verified, u)
		} else {
			unverified = append(unverified, u)
		}
	}
	if len(verified) > 0 {
		return r.linkByEmail(ctx, pol, c, email, verified)
	}
	return r.create(ctx, pol, c, email, unverified)
}

// linkedUser loads the user a linked identity points at and applies the
// status gate.
func (r *Resolver) linkedUser(ctx context.Context, id *auth.ExternalIdentity) (*auth.User, error) {
	u, err := r.d.Store.GetUser(ctx, id.UserID)
	if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrUndecodableRecord) {
		return nil, fmt.Errorf("%w: a linked identity points at a missing account", auth.ErrInvalidCredentials)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	if err := mayEnter(u); err != nil {
		return nil, err
	}
	return u, nil
}

// needsVerifiedMark reports whether a linked account holds the address the
// provider now vouches for, unverified: the one write the linked path makes.
func needsVerifiedMark(u *auth.User, email string) bool {
	return auth.SameEmail(u.Email, email) && !u.EmailVerified
}

// linkedFast is step 2 without the e-mail lock, for a sign-in that writes
// nothing: it reports done when the subject is linked and the account needs
// no verified mark, with the user or the refusal. Anything else (an unseen
// subject, a mark to write) is left to the locked path, which decides again.
func (r *Resolver) linkedFast(ctx context.Context, pol Policy, c Claims, email string) (u *auth.User, done bool, err error) {
	id, err := r.d.Store.IdentityBySubject(ctx, pol.Provider, c.Subject)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, true, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	u, err = r.linkedUser(ctx, id)
	if err != nil {
		return nil, true, err
	}
	if needsVerifiedMark(u, email) {
		return nil, false, nil
	}
	return u, true, nil
}

// linked is step 2: the user a (provider, subject) is linked to.
func (r *Resolver) linked(ctx context.Context, id *auth.ExternalIdentity, email string) (*auth.User, error) {
	u, err := r.linkedUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if needsVerifiedMark(u, email) {
		// The provider vouches for the address the account already holds:
		// record it as verified, unless another account holds it verified
		// (best effort: the sign-in is already decided).
		if taken, err := r.verifiedElsewhere(ctx, email, u.ID); err == nil && !taken {
			up := cloneUser(u)
			up.EmailVerified, up.Version = true, u.Version+1
			up.UpdatedAt = r.d.Clock().UTC()
			err := r.commit(ctx, func(b *storage.Batch) error { return r.d.Store.PutUser(b, up) })
			if err == nil {
				return up, nil
			}
			r.d.Log.Warn("oidc: could not record a verified address", "user", string(u.ID), "err", err)
		}
	}
	return u, nil
}

// linkByEmail is step 3. holders are the accounts holding the address
// verified (at most one by the e-mail rule; more is refused like a conflict).
func (r *Resolver) linkByEmail(ctx context.Context, pol Policy, c Claims, email string, holders []*auth.User) (*auth.User, error) {
	refuse := func(reason string) (*auth.User, error) {
		r.d.Audit.Emit(ctx, audit.Event{
			Action: actionLink, Outcome: audit.Denied, Reason: reason,
			Details: map[string]any{"provider": pol.Provider},
		})
		return nil, fmt.Errorf("%w: sign in and link %s from your profile", auth.ErrEmailInUse, pol.Provider)
	}
	if len(holders) != 1 || !pol.LinkExistingByEmail {
		return refuse("email_in_use")
	}
	h := holders[0]
	admin, err := r.holdsAdmin(ctx, h.Roles)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	if admin || h.HasPassword {
		return refuse("email_in_use")
	}
	if err := mayEnter(h); err != nil {
		return nil, err
	}
	now := r.d.Clock().UTC()
	err = r.commit(ctx, func(b *storage.Batch) error {
		if err := r.d.Store.GuardUser(b, h); err != nil {
			return err
		}
		if err := r.d.Store.PutIdentity(b, &auth.ExternalIdentity{
			Provider: pol.Provider, Subject: c.Subject, UserID: h.ID, Email: email, CreatedAt: now,
		}); err != nil {
			return err
		}
		return r.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionLink, Outcome: audit.Success, Target: userRef(h.ID),
			Details: map[string]any{"provider": pol.Provider, "by": "verified_email"},
		})
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// create is step 4.
func (r *Resolver) create(ctx context.Context, pol Policy, c Claims, email string, unverified []*auth.User) (*auth.User, error) {
	role := pol.DefaultRole
	if role == "" {
		role = rbac.RoleViewer
	}
	if admin, err := r.holdsAdmin(ctx, []rbac.RoleName{role}); err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	} else if admin {
		return nil, fmt.Errorf("%w: the default role of %s grants core:admin", auth.ErrBadInput, pol.Provider)
	}
	if _, err := r.d.Roles.Role(ctx, role); err != nil {
		return nil, fmt.Errorf("%w: the default role of %s cannot be read", auth.ErrBadInput, pol.Provider)
	}
	now := r.d.Clock().UTC()
	rawID, err := newULID(now, rand.Reader)
	if err != nil {
		return nil, err
	}
	name, err := r.freeUsername(ctx, email)
	if err != nil {
		return nil, err
	}
	u := &auth.User{
		ID: auth.UserID(rawID), Username: name, Email: email, EmailVerified: true,
		DisplayName: strings.TrimSpace(c.Name), Roles: []rbac.RoleName{role},
		Status: auth.StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	var displaced []*auth.User
	if pol.LinkExistingByEmail {
		for _, h := range unverified {
			admin, err := r.holdsAdmin(ctx, h.Roles)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
			}
			if !admin && !h.HasPassword {
				displaced = append(displaced, h)
			}
		}
	}
	err = r.commit(ctx, func(b *storage.Batch) error {
		for _, h := range displaced {
			up := cloneUser(h)
			up.Email, up.EmailVerified, up.Version, up.UpdatedAt = "", false, h.Version+1, now
			if err := r.d.Store.PutUser(b, up); err != nil {
				return err
			}
			if err := r.d.Audit.RecordIn(ctx, b, audit.Event{
				Action: actionDisplace, Outcome: audit.Success, Target: userRef(h.ID),
				Details: map[string]any{"provider": pol.Provider, "new_user": string(u.ID)},
			}); err != nil {
				return err
			}
		}
		rec := *u
		rec.Roles = slices.Clone(u.Roles)
		if err := r.d.Store.PutUser(b, &rec); err != nil {
			return err
		}
		if err := r.d.Store.PutIdentity(b, &auth.ExternalIdentity{
			Provider: pol.Provider, Subject: c.Subject, UserID: u.ID, Email: email, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := r.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionCreate, Outcome: audit.Success, Target: userRef(u.ID),
			Details: map[string]any{"username": name, "roles": []string{string(role)}, "origin": string(auth.OriginOIDC(pol.Provider))},
		}); err != nil {
			return err
		}
		return r.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionLink, Outcome: audit.Success, Target: userRef(u.ID),
			Details: map[string]any{"provider": pol.Provider, "by": "new_account"},
		})
	})
	if err != nil {
		return nil, err
	}
	if len(displaced) > 0 {
		ids := make([]string, len(displaced))
		for i, h := range displaced {
			ids[i] = string(h.ID)
		}
		r.d.Log.Warn("oidc: a verified sign-in took its address back from unverified accounts",
			"provider", pol.Provider, "new_user", string(u.ID), "displaced", ids)
	}
	r.d.Accounts.NoteUserCreated()
	return u, nil
}

// mayEnter is the status gate of a sign-in: the account must be active and
// not restricted (a last admin a restore kept signs in by no authenticator
// until a reset, §4.14).
func mayEnter(u *auth.User) error {
	switch {
	case u.Status != auth.StatusActive:
		return fmt.Errorf("%w: %s", auth.ErrInactiveUser, u.ID)
	case u.SignInRestricted:
		return fmt.Errorf("%w: %s", auth.ErrResetRequired, u.ID)
	}
	return nil
}

// holdsAdmin reports whether any of roles grants core:admin. An unknown role
// grants nothing; any other read error is the caller's to fail closed on.
func (r *Resolver) holdsAdmin(ctx context.Context, roles []rbac.RoleName) (bool, error) {
	for _, name := range roles {
		role, err := r.d.Roles.Role(ctx, name)
		switch {
		case errors.Is(err, rbac.ErrUnknownRole):
			continue
		case err != nil:
			return false, err
		}
		if rbac.GrantsAdmin(role.Permissions()) {
			return true, nil
		}
	}
	return false, nil
}

// verifiedElsewhere reports whether an account other than except holds email
// verified; an undecodable record counts as one.
func (r *Resolver) verifiedElsewhere(ctx context.Context, email string, except auth.UserID) (bool, error) {
	for u, err := range r.d.Store.ScanUsers(ctx) {
		if errors.Is(err, auth.ErrUndecodableRecord) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if u.ID != except && u.EmailVerified && auth.SameEmail(u.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

// freeUsername is the verified address itself, as HEAD's: unique, proven to
// belong to the person, and what they sign in with. A numeric suffix covers a
// local account that already took the exact name without holding the address.
func (r *Resolver) freeUsername(ctx context.Context, email string) (string, error) {
	base := sanitizeUsername(email)
	if base == "" {
		base = "user"
	}
	cand := base
	for i := 1; i <= maxUsernameTry; i++ {
		_, err := r.d.Store.UserByUsername(ctx, auth.FoldUsername(cand))
		switch {
		case errors.Is(err, auth.ErrNotFound):
			return cand, nil
		case err != nil && !errors.Is(err, auth.ErrUndecodableRecord):
			return "", fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
		}
		cand = fmt.Sprintf("%s%d", base, i)
	}
	return "", fmt.Errorf("%w: no free username for the address", auth.ErrUserExists)
}

// sanitizeUsername reduces an address to a conservative username charset.
func sanitizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == '@' || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// commit runs stage over a fresh batch and commits it Sync. A conflict is
// returned as storage.ErrConflict for Resolve's retry to decide again.
func (r *Resolver) commit(ctx context.Context, stage func(b *storage.Batch) error) (err error) {
	b := r.d.NewBatch()
	defer func() {
		if cerr := b.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err := stage(b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

func cloneUser(u *auth.User) *auth.User {
	c := *u
	c.Roles = slices.Clone(u.Roles)
	return &c
}

func userRef(id auth.UserID) entity.Ref {
	return entity.Ref{Module: auditModule, Kind: "user", ID: string(id)}
}

// newULID is a ULID: 48 bits of t's Unix milliseconds and 80 bits read from
// r, as 26 Crockford base32 characters. It is a copy of auth's, authstore's and
// audit's: R4 keeps the packages from sharing one without a new L0 package.
func newULID(t time.Time, r io.Reader) (string, error) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("oidc: user ID entropy: %w", err)
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(t.UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
