// file: core/auth/sessions.go
// version: 1.3.0
// guid: 587ac104-093e-402c-80f7-30ea7e13591d
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

// IssueSession opens a session for u (§4.3). The raw mcs_ token is returned
// once, in IssuedSession.Token; the store keeps only its SHA-256 and the
// public ID. u must be active.
func (s *Service) IssueSession(ctx context.Context, u *User, origin SessionOrigin, remember bool, client ClientInfo) (*IssuedSession, error) {
	return s.issueSession(ctx, u, origin, remember, client, nil)
}

// issueSession is IssueSession with also, when non-nil, staging into the
// session's batch after the record (a sign-in's audit record).
func (s *Service) issueSession(ctx context.Context, u *User, origin SessionOrigin, remember bool, client ClientInfo,
	also func(b *storage.Batch, rec *SessionRecord) error,
) (*IssuedSession, error) {
	if u == nil || u.ID == "" {
		return nil, fmt.Errorf("%w: a session needs a user", ErrBadInput)
	}
	if u.Status != StatusActive {
		return nil, fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	}
	now := s.now().UTC()
	var out *IssuedSession
	err := s.commitRetrying(ctx, func(b *storage.Batch) error {
		rec, issued, err := s.stageSession(b, u, origin, remember, client, now)
		if err != nil {
			return err
		}
		if also != nil {
			if err := also(b, rec); err != nil {
				return err
			}
		}
		out = issued
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// stageSession stages into b a session for u opened at now, stamped with
// u.CredentialEpoch: core.auth.session.remember_ttl when remember, else
// core.auth.session.ttl. A caller that changes u in the same batch (a reset
// link's epoch bump, an invite's new account) passes the record as staged.
func (s *Service) stageSession(b *storage.Batch, u *User, origin SessionOrigin, remember bool, client ClientInfo, now time.Time,
) (*SessionRecord, *IssuedSession, error) {
	st := s.settings()
	ttl := st.SessionTTL
	if remember {
		ttl = st.RememberTTL
	}
	rec := &SessionRecord{
		UserID: u.ID, Origin: origin, CredentialEpoch: u.CredentialEpoch,
		CreatedAt: now, ExpiresAt: now.Add(ttl), LastSeen: now,
		ClientAddr: client.Addr, UserAgent: client.UserAgent,
	}
	tok, err := s.d.Store.IssueSession(b, rec)
	if err != nil {
		return nil, nil, err
	}
	return rec, &IssuedSession{Token: string(tok), PublicID: rec.PublicID, ExpiresAt: rec.ExpiresAt}, nil
}

// Sessions lists user's open sessions as views: never a token, only the
// public ID that revoke takes (§4.3, early fix 1). Current marks the session
// the request in ctx presented.
func (s *Service) Sessions(ctx context.Context, user UserID) iter.Seq2[SessionView, error] {
	current := ""
	if p, ok := PrincipalFrom(ctx); ok && p.Method.sessionMethod() {
		current = p.CredentialID
	}
	now := s.now()
	return func(yield func(SessionView, error) bool) {
		for rec, err := range s.d.Store.ScanUserSessions(ctx, user) {
			if err != nil {
				if !yield(SessionView{}, err) {
					return
				}
				continue
			}
			if !now.Before(rec.ExpiresAt) {
				continue
			}
			v := SessionView{
				PublicID: rec.PublicID, UserAgent: rec.UserAgent, ClientAddr: rec.ClientAddr, Origin: rec.Origin,
				CreatedAt: rec.CreatedAt, LastSeen: rec.LastSeen, ExpiresAt: rec.ExpiresAt,
				Current: current != "" && rec.PublicID == current,
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}

// sessionMethod reports whether m is a session credential's method.
func (m Method) sessionMethod() bool { return m == MethodSession || m == MethodSessionDelegated }

// RevokeSession deletes the session named by publicID: the actor's own, or
// another user's with core:users.manage, an interactive method and
// dominance. Its journal line is appended into the revoking batch (§4.12).
func (s *Service) RevokeSession(ctx context.Context, actor *Principal, publicID string) error {
	if _, err := actorUser(actor); err != nil {
		return err
	}
	return s.commitInvalidating(ctx, func(b *storage.Batch, inv *invalidations) error {
		rec, err := s.d.Store.SessionByPublicID(ctx, publicID)
		if err != nil {
			return err
		}
		if err := s.mayManage(ctx, actor, rec.UserID); err != nil {
			return err
		}
		if err := s.d.Store.DeleteSession(b, rec); err != nil {
			return err
		}
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.SessionRevoked, Subject: ref(refKindSession, rec.PublicID)})
		inv.credential(rec.UserID, rec.PublicID, journal.SessionRevoked)
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionSessionRevoke, Outcome: audit.Success, Target: ref(refKindSession, rec.PublicID),
			Details: map[string]any{"user": string(rec.UserID)},
		})
	})
}

// RevokeAllSessions signs user out everywhere (§3.2, §4.12). One batch bumps
// the user's credential epoch, so every session and one-time token issued
// before it is refused (and deleted when presented), deletes the user's
// session records and the temp-login links the user created (their creator's
// epoch moved, so they would be refused anyway), and journals
// credential_epoch.bumped. except, when it
// names a live session of user at the user's current epoch, survives: it is
// re-stamped with the new epoch in the same batch ("sign out my other
// devices"). An except that is gone, expired or already epoch-stale keeps
// nothing (re-stamping a stale session would revive one a password change
// killed); an except of another user is refused (ErrBadInput). API keys are
// not epoch-bound and are left alone (§4.12).
//
// The epoch is credential state, so the actor needs an interactive method
// even for its own sessions (MayChangeCredentials, §3.1); another user's
// also need core:users.manage and dominance. _system is refused.
func (s *Service) RevokeAllSessions(ctx context.Context, actor *Principal, user UserID, except string) error {
	if _, err := actorUser(actor); err != nil {
		return err
	}
	if !s.interactive(actor.Method) {
		return fmt.Errorf("%w: signing out everywhere needs an interactive sign-in", ErrDenied)
	}
	now := s.now().UTC()
	return s.commitInvalidating(ctx, func(b *storage.Batch, inv *invalidations) error {
		if err := s.mayManage(ctx, actor, user); err != nil {
			return err
		}
		u, err := s.d.Store.GetUser(ctx, user)
		if err != nil {
			return err
		}
		if err := mutable(u); err != nil {
			return err
		}
		keep, err := s.keptSession(ctx, u, except, now)
		if err != nil {
			return err
		}
		up := *u
		up.Version = u.Version + 1
		up.CredentialEpoch = u.CredentialEpoch + 1
		up.UpdatedAt = now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return err
		}
		revoked := 0
		for rec, err := range s.d.Store.ScanUserSessions(ctx, user) {
			if err != nil {
				if s.leftUndecodable("session", user, err) {
					continue
				}
				return err
			}
			if keep != nil && rec.PublicID == keep.PublicID {
				continue
			}
			if err := s.d.Store.DeleteSession(b, rec); err != nil {
				return err
			}
			inv.credential(user, rec.PublicID, journal.CredentialEpochBumped)
			revoked++
		}
		links, err := s.deleteCreatedLinks(ctx, b, user)
		if err != nil {
			return err
		}
		details := map[string]any{"all": true, "revoked": revoked, "links_deleted": links}
		if keep != nil {
			if err := s.d.Store.RestampSession(b, keep, up.CredentialEpoch); err != nil {
				return err
			}
			details["kept"] = keep.PublicID
		}
		subject := ref(refKindUser, string(user))
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionSessionRevoke, Outcome: audit.Success, Target: subject, Details: details,
		})
	})
}

// keptSession resolves RevokeAllSessions' except against u: the session to
// re-stamp, or nil when except is empty, gone, expired or not at u's epoch.
func (s *Service) keptSession(ctx context.Context, u *User, except string, now time.Time) (*SessionRecord, error) {
	if except == "" {
		return nil, nil
	}
	rec, err := s.d.Store.SessionByPublicID(ctx, except)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	case rec.UserID != u.ID:
		return nil, fmt.Errorf("%w: session %s is not %s's", ErrBadInput, except, u.ID)
	case rec.CredentialEpoch != u.CredentialEpoch || !now.Before(rec.ExpiresAt):
		return nil, nil
	}
	return rec, nil
}

// Revalidate reports whether p, a principal bound earlier and held by a
// long-lived request (an SSE stream), still stands (§3.2): its user exists,
// is active and is not SignInRestricted; a session principal's session still
// exists, is unexpired and is at the user's current epoch (so a self-service
// password change, which re-stamps the caller's own session, keeps that
// stream); a key principal's key is still active and unexpired, whatever the
// epoch (keys are not epoch-bound, §4.12); an identity extension's principal
// stands until its notAfter (Assertion.ExpiresAt, or
// core.auth.extension_max_age after its AuthTime: ErrExpired at or after
// it; one with no notAfter, which only PrincipalFor can build, is ErrExpired
// at once); a principal of any other method stands while the user's epoch is the
// one it was built at; and a compat surface's own principal or an identity
// extension's does not stand while its user must reset a password
// (ErrResetRequired, surfaceStanding; F4-08 A14). It returns nil,
// the refusal (ErrRevoked, ErrExpired, ErrStaleEpoch, ErrInactiveUser,
// ErrResetRequired, ErrInvalidCredentials), or ErrStoreUnavailable.
//
// It answers from the store (the user and the credential, two point reads)
// rather than from the event-invalidated generation table §3.2 describes,
// until Deps.Events is wired; the answers are the same.
func (s *Service) Revalidate(ctx context.Context, p *Principal) error {
	err := s.revalidate(ctx, p)
	s.met.invalidated(ctx, err)
	return err
}

func (s *Service) revalidate(ctx context.Context, p *Principal) error {
	if s.authOffBinding(p) {
		return nil
	}
	_, _, err := s.stands(ctx, p)
	return err
}

// authOffBinding reports p is the auth-off binding while the mode is on: it
// has no user, credential or epoch to go stale (§4.11), so it stands exactly
// while the mode does.
func (s *Service) authOffBinding(p *Principal) bool {
	return p != nil && p.Method == MethodAuthDisabled && s.authOff
}

// stands is Revalidate's check of a principal with a user, returning the user
// and, for a key principal, the key it read, both as the store holds them now.
func (s *Service) stands(ctx context.Context, p *Principal) (*User, *APIKey, error) {
	if p == nil || p.User == nil || p.User.ID == "" {
		return nil, nil, fmt.Errorf("%w: no principal", ErrInvalidCredentials)
	}
	// An identity extension's credential has its own end (its token's exp, or
	// core.auth.extension_max_age after AuthTime), which no store record
	// carries: without this a stream outlives it. Only stage 12 sets it, so an
	// extension principal with none (PrincipalFor is exported and builds one
	// for any registered ext: method) has no end at all and is refused too.
	if bounded := !p.notAfter.IsZero(); (bounded && !s.now().Before(p.notAfter)) || (!bounded && p.Method.isExtension()) {
		return nil, nil, fmt.Errorf("%w: %s credential %s", ErrExpired, p.Method, p.CredentialID)
	}
	u, err := s.d.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return nil, nil, revalidateErr("user "+string(p.User.ID), err)
	}
	if err := standing(u); err != nil {
		return nil, nil, err
	}
	keyID, isKey := p.keyRef()
	if !isKey && s.surfaceMethod(p.Method) {
		// A credential the app verified itself (a compat surface's own, an
		// identity extension's) ends while its user must reset a password, as
		// stage 12 and Compat.PrincipalFor refuse to build it (F4-08 A14).
		if err := surfaceStanding(u); err != nil {
			return nil, nil, err
		}
	}
	now := s.now()
	switch {
	case isKey:
		k, err := s.d.Store.APIKeyByID(ctx, keyID)
		if err != nil {
			return nil, nil, revalidateErr("key "+keyID, err)
		}
		if k.Owner != u.ID {
			return nil, nil, fmt.Errorf("%w: key %s is not %s's", ErrRevoked, keyID, u.ID)
		}
		if err := keyUsable(k, now); err != nil {
			return nil, nil, err
		}
		return u, k, nil
	case p.Method.sessionMethod():
		rec, err := s.d.Store.SessionByPublicID(ctx, p.CredentialID)
		if err != nil {
			return nil, nil, revalidateErr("session "+p.CredentialID, err)
		}
		switch {
		case rec.UserID != u.ID:
			return nil, nil, fmt.Errorf("%w: session %s is not %s's", ErrRevoked, p.CredentialID, u.ID)
		case !now.Before(rec.ExpiresAt):
			return nil, nil, fmt.Errorf("%w: session %s", ErrExpired, p.CredentialID)
		case rec.CredentialEpoch != u.CredentialEpoch:
			return nil, nil, fmt.Errorf("%w: session %s", ErrStaleEpoch, p.CredentialID)
		}
	case u.CredentialEpoch != p.User.CredentialEpoch:
		return nil, nil, fmt.Errorf("%w: %s", ErrStaleEpoch, u.ID)
	}
	return u, nil, nil
}

// Refresh re-resolves p, a principal bound earlier and held by a long-lived
// request (an SSE stream's Deps.Refresh, F6-03): it returns the principal p's
// credential would be built as now, with a fresh checker, so a role change
// (which moves neither the epoch nor the rbac generation) reaches it. p must
// still stand, exactly as Revalidate decides (its user active and
// unrestricted; its session alive and at the user's epoch; its key active,
// unexpired and its user's; an identity extension's principal before its
// notAfter; any other method at the epoch it was built at; a compat
// surface's own principal and an identity extension's not while its user must
// reset a password, surfaceStanding). The answer is built through
// PrincipalFor with p's own Method, over the scopes p was built with that
// the stored key still holds for a key principal (core's or a compat
// surface's), and otherwise over the scopes p was built with: a narrowed
// identity-extension or compat principal stays narrowed, and nil stays the
// method's own rule. It never widens: a principal PrincipalFor did not build
// carries no record of its narrowing and is refused. CredentialID, the key
// reference, AuthTime, Assurance, ClientAddr and the extension expiry are
// p's; the expiry is never extended. The auth-off binding is answered afresh
// while the mode is on. Refresh trusts its caller to pass the request's own
// principal (the one stage 12 bound to it), as Revalidate does. Errors are
// Revalidate's (a store failure is ErrStoreUnavailable, anything else
// definite) and PrincipalFor's. It reads the user and at most one credential
// and records no metric (Revalidate counts the beat).
func (s *Service) Refresh(ctx context.Context, p *Principal) (*Principal, error) {
	if s.authOffBinding(p) {
		np, _ := s.authDisabledPrincipal(ClientInfo{})
		np.ClientAddr = p.ClientAddr
		return np, nil
	}
	// p is whatever the caller holds, and a holder can write its exported
	// fields: Refresh rebuilds for the user, method and credential it names, so
	// it believes them only while they are what auth sealed (principalSeal).
	if err := p.checkSeal(); err != nil {
		return nil, err
	}
	u, k, err := s.stands(ctx, p)
	if err != nil {
		return nil, err
	}
	scopes := p.scopes
	switch {
	case k != nil:
		if len(k.Scopes) == 0 {
			// Never stored (R3-1); nil would be the owner's whole role set.
			return nil, fmt.Errorf("%w: key %s has no scope", ErrInvalidCredentials, k.ID)
		}
		if scopes, err = keyScopes(p.scopes, k); err != nil {
			return nil, err
		}
	}
	np, err := s.PrincipalFor(ctx, u, p.Method, scopes, u.CredentialEpoch)
	if err != nil {
		return nil, err
	}
	np.CredentialID, np.keyID = p.CredentialID, p.keyID
	np.AuthTime, np.Assurance, np.ClientAddr = p.AuthTime, p.Assurance, p.ClientAddr
	np.notAfter = p.notAfter // carried, never extended
	np.sealIdentity()        // the fields just set are core's, so they are sealed
	return np, nil
}

// keyScopes is a refreshed key principal's scopes: the ones it was built with
// that the stored key still holds, so a refresh never widens past either
// (the key's scopes are immutable in authstore today; this holds by
// construction if that changes). An empty intersection is refused, because
// PrincipalFor would read nil as the owner's whole role set; a built key
// principal always records its scopes (PrincipalFor refuses a key without).
func keyScopes(recorded []rbac.Permission, k *APIKey) ([]rbac.Permission, error) {
	out := make([]rbac.Permission, 0, len(recorded))
	for _, sc := range recorded {
		if slices.Contains(k.Scopes, sc) && !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: key %s no longer holds any scope its principal was built with", ErrRevoked, k.ID)
	}
	return out, nil
}

// revalidateErr maps a Revalidate point read's error: a missing record is a
// revoked credential, an undecodable one is refused, anything else is the
// store's.
func revalidateErr(what string, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%w: %s is gone", ErrRevoked, what)
	case errors.Is(err, ErrUndecodableRecord):
		return fmt.Errorf("%w: %s cannot be decoded", ErrInvalidCredentials, what)
	case errors.Is(err, ErrStoreUnavailable):
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

// surfaceStanding is the extra user rule of a credential the app verified
// itself rather than core: a compat surface's own principal
// (Compat.PrincipalFor; not its key principal, which follows stage 12's key
// rules) and an identity extension's (stage 12's extension stage; owner ruling
// 2026-11-20, F4-08 A14). A user whose password must be reset is refused:
// core cannot tell whether the outside credential predates the reset, and the
// reset is the admin's statement that the account's credentials are not to be
// trusted until it is done. stands applies it on every Revalidate and Refresh,
// so a stream of such a principal ends at its next beat.
func surfaceStanding(u *User) error {
	if u.PasswordResetRequired {
		return fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
	}
	return nil
}

// standing is the user half of every credential check: the user is active
// and not SignInRestricted (§4.2, §4.14).
func standing(u *User) error {
	if u.Status != StatusActive {
		return fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	}
	if u.SignInRestricted {
		return fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
	}
	return nil
}

// PrincipalFor builds the principal of a credential that resolved to u
// through method m (§3.2, G15). It applies exactly what core credentials get:
// u must be active, not SignInRestricted (ErrResetRequired), and at epoch
// (ErrStaleEpoch); the checker is rbac.Service.Compile over u's roles, the
// credential's scopes and m's interactivity, never anything the caller
// supplies. For MethodAPIKey scopes must be non-empty: they are refused
// here, before Compile, because nil means "every role permission" there.
// For other methods nil is the role permissions and an empty non-nil list is
// refused.
func (s *Service) PrincipalFor(ctx context.Context, u *User, m Method, scopes []rbac.Permission, epoch int64) (*Principal, error) {
	if u == nil || u.ID == "" {
		return nil, fmt.Errorf("%w: no user", ErrInvalidCredentials)
	}
	if !s.knownMethod(m) {
		return nil, fmt.Errorf("%w: unregistered method %q", ErrMethodSpec, m)
	}
	if err := standing(u); err != nil {
		return nil, err
	}
	if epoch != u.CredentialEpoch {
		return nil, fmt.Errorf("%w: %s", ErrStaleEpoch, u.ID)
	}
	if (m == MethodAPIKey && len(scopes) == 0) || (scopes != nil && len(scopes) == 0) {
		return nil, fmt.Errorf("%w: a key credential carries at least one scope", ErrInvalidCredentials)
	}
	p := &Principal{
		User:   u,
		Method: m,
		Perms: s.d.RBAC.Compile(rbac.CheckerInput{
			Roles: slices.Clone(u.Roles), Scopes: slices.Clone(scopes), Interactive: s.interactive(m),
		}),
		scopes: slices.Clone(scopes),
	}
	p.sealIdentity()
	return p, nil
}
