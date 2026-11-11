// file: core/auth/sessions.go
// version: 1.0.0
// guid: 587ac104-093e-402c-80f7-30ea7e13591d
// last-edited: 2026-11-11

package auth

import (
	"context"
	"fmt"
	"iter"
	"slices"

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
	st := s.settings()
	ttl := st.SessionTTL
	if remember {
		ttl = st.RememberTTL
	}
	now := s.now().UTC()
	var out *IssuedSession
	err := s.commitRetrying(ctx, func(b *storage.Batch) error {
		rec := &SessionRecord{
			UserID: u.ID, Origin: origin, CredentialEpoch: u.CredentialEpoch,
			CreatedAt: now, ExpiresAt: now.Add(ttl), LastSeen: now,
			ClientAddr: client.Addr, UserAgent: client.UserAgent,
		}
		tok, err := s.d.Store.IssueSession(b, rec)
		if err != nil {
			return err
		}
		if also != nil {
			if err := also(b, rec); err != nil {
				return err
			}
		}
		out = &IssuedSession{Token: string(tok), PublicID: rec.PublicID, ExpiresAt: rec.ExpiresAt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
	if u.Status != StatusActive {
		return nil, fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	}
	if u.SignInRestricted {
		return nil, fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
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
