// file: core/auth/sessions.go
// version: 1.2.0
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
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
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
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
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
// epoch (keys are not epoch-bound, §4.12); a principal of any other method
// stands while the user's epoch is the one it was built at. It returns nil,
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
	if p == nil || p.User == nil || p.User.ID == "" {
		return fmt.Errorf("%w: no principal", ErrInvalidCredentials)
	}
	u, err := s.d.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return revalidateErr("user "+string(p.User.ID), err)
	}
	if err := standing(u); err != nil {
		return err
	}
	now := s.now()
	keyID, isKey := p.keyRef()
	switch {
	case isKey:
		k, err := s.d.Store.APIKeyByID(ctx, keyID)
		if err != nil {
			return revalidateErr("key "+keyID, err)
		}
		if k.Owner != u.ID {
			return fmt.Errorf("%w: key %s is not %s's", ErrRevoked, keyID, u.ID)
		}
		return keyUsable(k, now)
	case p.Method.sessionMethod():
		rec, err := s.d.Store.SessionByPublicID(ctx, p.CredentialID)
		if err != nil {
			return revalidateErr("session "+p.CredentialID, err)
		}
		switch {
		case rec.UserID != u.ID:
			return fmt.Errorf("%w: session %s is not %s's", ErrRevoked, p.CredentialID, u.ID)
		case !now.Before(rec.ExpiresAt):
			return fmt.Errorf("%w: session %s", ErrExpired, p.CredentialID)
		case rec.CredentialEpoch != u.CredentialEpoch:
			return fmt.Errorf("%w: session %s", ErrStaleEpoch, p.CredentialID)
		}
	case u.CredentialEpoch != p.User.CredentialEpoch:
		return fmt.Errorf("%w: %s", ErrStaleEpoch, u.ID)
	}
	return nil
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
	return &Principal{
		User:   u,
		Method: m,
		Perms: s.d.RBAC.Compile(rbac.CheckerInput{
			Roles: slices.Clone(u.Roles), Scopes: slices.Clone(scopes), Interactive: s.interactive(m),
		}),
	}, nil
}
