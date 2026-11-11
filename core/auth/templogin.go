// file: core/auth/templogin.go
// version: 1.0.0
// guid: 224ca491-2171-423a-bce4-a4654be50c1f
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// Temp-login and reset links (05-authn §3.2, §4.1, §4.12, §4.13, §4.14).
//
// A temp-login link opens a delegated session (OriginTempLogin, so
// MethodSessionDelegated: not interactive) for the user an administrator
// named. A reset link is a temp-login link that also sets a new password: it
// is how an administrator resets another user's password, since the core API
// has no "set another user's password" route (§4.13), and how a user with
// PasswordResetRequired (or, from an owner-verified actor, SignInRestricted)
// gets back in. Both are single use, stored as their SHA-256 (onetime/
// temp_login, Serialized), carry the target's credential epoch at issue,
// and live core.auth.temp_login.ttl. The links are fragment links, so the
// token never reaches a server log (§4.13).
const (
	actionTempLoginMint    = "core.templogin.mint"
	actionTempLoginConsume = "core.templogin.consume"
	reasonExpired          = "expired"
	reasonStaleEpoch       = "stale_epoch"
)

// tempLoginLink is the fragment link a temp-login or reset token travels in.
func tempLoginLink(token string) string { return "/login/temp#token=" + token }

// MintTempLogin mints a temp-login link for target (§3.2): core:users.manage
// through an interactive method and dominance over target. target must be
// active and need neither a password reset nor a restore's restriction
// lifted (a reset link does that). The token carries target's current
// credential epoch, so a password change, disable or sign-out-everywhere
// before it is consumed kills it.
func (s *Service) MintTempLogin(ctx context.Context, actor *Principal, target UserID) (string, error) {
	return s.mintTempLogin(ctx, actor, target, false)
}

// MintResetLink mints a reset link for target (§3.2): the rules of
// MintTempLogin, except that the target may need a password reset. Minting
// bumps target's credential epoch in the token's own batch, so every
// session and earlier one-time token of the user dies at once, and the link
// carries the new epoch. Consuming it takes a new password. A link minted by
// an actor carrying AssuranceOwner also clears SignInRestricted when
// consumed (§4.14); for a restricted target, any other actor is refused
// (ErrResetRequired), since its link could not let the user in.
func (s *Service) MintResetLink(ctx context.Context, actor *Principal, target UserID) (string, error) {
	return s.mintTempLogin(ctx, actor, target, true)
}

func (s *Service) mintTempLogin(ctx context.Context, actor *Principal, target UserID, reset bool) (string, error) {
	what := "mint a temp-login link"
	if reset {
		what = "mint a reset link"
	}
	if err := s.mayManageUsers(actor, what); err != nil {
		return "", err
	}
	clears := reset && actor.Assurance&AssuranceOwner != 0
	ttl := s.settings().TempLoginTTL
	now := s.now().UTC()
	var token string
	err := s.commitRetrying(ctx, func(b *storage.Batch) error {
		u, err := s.d.Store.GetUser(ctx, target)
		if err != nil {
			return err
		}
		if err := mutable(u); err != nil {
			return err
		}
		if u.Status != StatusActive {
			return fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
		}
		if err := s.dominates(actor, u); err != nil {
			return err
		}
		switch {
		case u.SignInRestricted && !clears:
			return fmt.Errorf("%w: only an owner-verified reset link lifts %s's sign-in restriction", ErrResetRequired, u.ID)
		case u.PasswordResetRequired && !reset:
			return fmt.Errorf("%w: %s needs a reset link", ErrResetRequired, u.ID)
		}
		epoch := u.CredentialEpoch
		if reset {
			up := *u
			up.Version = u.Version + 1
			up.CredentialEpoch = u.CredentialEpoch + 1
			up.UpdatedAt = now
			if err := s.d.Store.PutUser(b, &up); err != nil {
				return err
			}
			epoch = up.CredentialEpoch
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: ref(refKindUser, string(u.ID)), Epoch: epoch})
		}
		t := &OneTimeToken{
			Kind: OneTimeTempLogin, UserID: u.ID, CreatedBy: actor.User.ID, CredentialEpoch: epoch,
			Reset: reset, ClearsRestriction: clears, CreatedAt: now, ExpiresAt: now.Add(ttl),
		}
		tok, err := s.d.Store.IssueOneTime(b, t)
		if err != nil {
			return err
		}
		token = string(tok)
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionTempLoginMint, Outcome: audit.Success, Target: ref(refKindUser, string(u.ID)),
			Details: map[string]any{"link": t.ID, "reset": reset, "clears_restriction": clears},
		})
	})
	if err != nil {
		return "", err
	}
	return tempLoginLink(token), nil
}

// ConsumeTempLogin spends a temp-login or reset token and opens a delegated
// session (OriginTempLogin, core.auth.session.ttl) for its user (§4.13).
// newPassword is required for a reset link and refused for a plain one
// (ErrBadInput); either mismatch, a malformed request, a short password and
// a busy hasher leave the token unspent.
//
// The source's throttle (§4.9) refuses a spent budget first; a token that
// does not parse or is not stored is a failed credential check, charged to
// the source (ErrInvalidCredentials). A stored token is spent whatever the
// answer: past its expiry (ErrExpired), its user gone (ErrInvalidCredentials)
// or not active (ErrInactiveUser), the user's epoch moved since issue
// (ErrStaleEpoch), or the user restricted beyond what the token lifts
// (ErrResetRequired) each delete it, audited, and open nothing.
//
// A reset link stores the new password in the session's own batch: the
// epoch is bumped (so the session carries the new epoch and every other
// credential of the user dies), PasswordResetRequired is cleared and, when
// the link carries ClearsRestriction, SignInRestricted with it;
// password.changed and credential_epoch.bumped are journaled.
func (s *Service) ConsumeTempLogin(ctx context.Context, token, newPassword string, client ClientInfo) (*IssuedSession, error) {
	source := client.sourceKey()
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		s.consumeRefused(ctx, client, reasonThrottled)
		return nil, throttled(LoginRetryAfter)
	}
	t, err := s.lookupOneTime(ctx, OneTimeTempLogin, KindTempLogin, token)
	if err != nil {
		if chargeable(err) {
			th.RecordFailure("", source)
		}
		if errors.Is(err, ErrInvalidCredentials) {
			s.consumeRefused(ctx, client, reasonInvalidCredentials)
		}
		return nil, err
	}
	switch {
	case t.Reset && newPassword == "":
		return nil, fmt.Errorf("%w: a reset link takes a new password", ErrBadInput)
	case !t.Reset && newPassword != "":
		return nil, fmt.Errorf("%w: a temp-login link takes no password", ErrBadInput)
	}
	hash := ""
	if t.Reset {
		if err := s.checkNewPassword(newPassword); err != nil {
			return nil, err
		}
		if hash, err = s.hashNew(ctx, newPassword); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	var out *IssuedSession
	var refusal error
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		refusal = nil
		t, err := s.d.Store.TakeOneTime(b, OneTimeTempLogin, Token(token)) // parsed by lookupOneTime
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: the link was spent concurrently", ErrInvalidCredentials)
		}
		if err != nil {
			return err
		}
		u, err := s.d.Store.GetUser(ctx, t.UserID)
		switch {
		case errors.Is(err, ErrNotFound):
			refusal = fmt.Errorf("%w: the link's user is gone", ErrInvalidCredentials)
		case err != nil:
			return err
		default:
			refusal = tempLoginRefusal(t, u, now)
		}
		target := ref(refKindUser, string(t.UserID))
		if refusal != nil {
			// The take is committed alone: a refused token is spent.
			return s.d.Audit.RecordIn(ctx, b, audit.Event{
				Action: actionTempLoginConsume, Outcome: audit.Failure, Reason: refusalReason(refusal), Target: target,
				Actor:   audit.Actor{ClientAddr: addrString(client.Addr), UserAgent: client.UserAgent},
				Details: map[string]any{"link": t.ID, "reset": t.Reset},
			})
		}
		if t.Reset {
			up := *u
			up.Version = u.Version + 1
			up.CredentialEpoch = u.CredentialEpoch + 1
			up.HasPassword = true
			up.PasswordResetRequired = false
			if t.ClearsRestriction {
				up.SignInRestricted = false
			}
			up.UpdatedAt = now
			if err := s.d.Store.SetPassword(b, &up, hash); err != nil {
				return err
			}
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.PasswordChanged, Subject: target, Epoch: up.CredentialEpoch})
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: target, Epoch: up.CredentialEpoch})
			u = &up
		}
		rec, issued, err := s.stageSession(b, u, OriginTempLogin, false, client, now)
		if err != nil {
			return err
		}
		out = issued
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionTempLoginConsume, Outcome: audit.Success, Target: target,
			Actor: audit.Actor{
				UserID: string(u.ID), Username: u.Username, Method: string(MethodSessionDelegated), CredentialID: rec.PublicID,
				ClientAddr: addrString(client.Addr), UserAgent: client.UserAgent,
			},
			Details: map[string]any{"link": t.ID, "reset": t.Reset, "cleared_restriction": t.Reset && t.ClearsRestriction},
		})
	})
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	return out, nil
}

// lookupOneTime reads the stored token of kind that raw names without
// spending it. A raw value that is not a well-formed credential of ck, or
// names no stored token, is ErrInvalidCredentials, a failed credential check
// the caller charges to the source (chargeable). A stored record that cannot
// be decoded is refused as ErrInvalidCredentials too, wrapping
// ErrUndecodableRecord: the credential may be genuine, so it is not charged.
// Any other failure is ErrStoreUnavailable.
func (s *Service) lookupOneTime(ctx context.Context, kind OneTimeKind, ck CredentialKind, raw string) (*OneTimeToken, error) {
	if err := ParseCredential(ck, raw); err != nil {
		return nil, fmt.Errorf("%w: malformed %s token", ErrInvalidCredentials, kind)
	}
	t, err := s.d.Store.LookupOneTime(ctx, kind, Token(raw))
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, fmt.Errorf("%w: unknown %s token", ErrInvalidCredentials, kind)
	case errors.Is(err, ErrUndecodableRecord):
		return nil, fmt.Errorf("%w: %s token: %w", ErrInvalidCredentials, kind, err)
	case errors.Is(err, ErrStoreUnavailable):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return t, nil
}

// chargeable reports whether a lookupOneTime refusal is a failed credential
// check the throttle counts.
func chargeable(err error) bool {
	return errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, ErrUndecodableRecord)
}

// tempLoginRefusal is why consuming t for u, as just read, opens nothing, or
// nil when it may.
func tempLoginRefusal(t *OneTimeToken, u *User, now time.Time) error {
	switch {
	case !now.Before(t.ExpiresAt):
		return fmt.Errorf("%w: the link expired at %s", ErrExpired, t.ExpiresAt.Format(time.RFC3339))
	case t.ClearsRestriction && !t.Reset:
		return fmt.Errorf("%w: a restriction-lifting link that is not a reset link", ErrInvalidCredentials)
	case u.Status != StatusActive:
		return fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	case u.CredentialEpoch != t.CredentialEpoch:
		return fmt.Errorf("%w: %s's credentials changed since the link was minted", ErrStaleEpoch, u.ID)
	case u.SignInRestricted && !t.ClearsRestriction:
		return fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
	case u.PasswordResetRequired && !t.Reset:
		return fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
	}
	return nil
}

// refusalReason is the audit reason of a one-time token refusal.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrExpired):
		return reasonExpired
	case errors.Is(err, ErrStaleEpoch):
		return reasonStaleEpoch
	case errors.Is(err, ErrInactiveUser):
		return reasonInactive
	case errors.Is(err, ErrResetRequired):
		return reasonResetRequired
	}
	return reasonInvalidCredentials
}

// consumeRefused audits, on the async path, a temp-login consume refused
// before any token resolved (throttled, or a token that names nothing).
func (s *Service) consumeRefused(ctx context.Context, c ClientInfo, reason string) {
	s.d.Audit.Emit(ctx, audit.Event{
		Action: actionTempLoginConsume, Outcome: audit.Failure, Reason: reason,
		Actor: audit.Actor{ClientAddr: addrString(c.Addr), UserAgent: c.UserAgent},
	})
}
