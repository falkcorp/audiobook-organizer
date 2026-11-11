// file: core/auth/login.go
// version: 1.0.0
// guid: ee7aa4e7-d5e2-4977-8b7e-49faa860a120
// last-edited: 2026-11-10

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/storage"
)

// loginHashWait bounds the wait for a hashing permit on the sign-in path: a
// semaphore still saturated after it answers 503 busy (05-authn §7), rather
// than queueing every login of a burst behind 64 MiB derivations.
const loginHashWait = 2 * time.Second

// LoginRetryAfter is the wait, in seconds, a sign-in refused by the throttle
// is told to take when no ceiling computed its own (HEAD's
// absauth.RetryAfterSeconds): the windows the budgets refill over are minutes
// long, and one fixed value does not say which budget refused.
const LoginRetryAfter = 60

// ThrottledError is a sign-in refused by the throttle or the login ceilings
// (errors.Is(err, ErrThrottled)), with the seconds the caller should wait
// before trying again: the route's Retry-After (at least 1).
type ThrottledError struct {
	RetryAfter int
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("%v: retry after %ds", ErrThrottled, e.RetryAfter)
}

// Unwrap makes errors.Is(err, ErrThrottled) hold.
func (e *ThrottledError) Unwrap() error { return ErrThrottled }

func throttled(retryAfter int) error {
	return &ThrottledError{RetryAfter: max(retryAfter, 1)}
}

// loginThrottle is the service's throttle with the core.auth.login.* policy
// in force (reloadable, §8).
func (s *Service) loginThrottle() *Throttle {
	s.throttle.SetLimits(s.settings().Login)
	return s.throttle
}

// Login is password sign-in (§3.2, §4.9), in HEAD's order (the login gate's
// ceilings, then handlers/auth.go Login):
//
//  1. the login ceilings admit the attempt (the visitor's in-flight cap, the
//     global and connector ceilings, and, when one is spent, the saturated
//     admission); the admission is released on every return;
//  2. a username and a password are required (ErrBadInput);
//  3. a source past its failure budget is refused before any credential work;
//  4. while saturated, the submitted name's failures decide a delay or a
//     refusal (Throttle.SaturationPolicy) before any lookup;
//  5. the user is read; a store failure is not a credential verdict, so it
//     charges nothing (ErrStoreUnavailable);
//  6. the password is verified within a 2 s wait for a hashing permit
//     (ErrBusy, charging nothing); an unknown name, an account without a
//     password and a wrong password all spend one derivation, are charged to
//     the source and the submitted name, are delayed by the account's soft
//     delay, and answer ErrInvalidCredentials, so neither the answer nor its
//     timing tells whether the name exists;
//  7. only then status (ErrInactiveUser) and a required reset or a sign-in
//     restriction (ErrResetRequired): a correct password for such an account
//     costs the same work, mints nothing and is not charged;
//  8. success clears the account and takes back this source's own failures
//     against it, opens a session stamped with the epoch of the record the
//     password was verified against, and re-stores a bcrypt hash as argon2id.
//
// Every throttle refusal is a *ThrottledError.
func (s *Service) Login(ctx context.Context, in LoginInput) (*IssuedSession, error) {
	// Everything is keyed on the normalised SUBMITTED username, never on the
	// user record, so a name that does not exist behaves exactly like one
	// that does.
	name := strings.TrimSpace(in.Username)
	account := AccountKey(name)
	source := in.Client.sourceKey()
	adm, dec := s.ceiling.Admit(source, in.Client.saturatedKey, in.Client.connectorKey())
	if dec.Warn != "" {
		s.log.Warn("auth: login ceiling reached", "ceiling", dec.Warn)
	}
	if adm == nil {
		return nil, throttled(dec.RetryAfter)
	}
	outcome := LoginOther
	defer func() { adm.Done(outcome, account) }()

	if name == "" || in.Password == "" {
		return nil, fmt.Errorf("%w: username and password are required", ErrBadInput)
	}
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		return nil, throttled(LoginRetryAfter)
	}
	if adm.Saturated() {
		d, refuse := th.SaturationPolicy(account)
		if refuse {
			outcome = LoginSaturationRefused
			return nil, throttled(LoginRetryAfter)
		}
		th.Delay(d)
	}
	u, hash, err := s.loginRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	algo := ""
	if u != nil {
		algo = password.AlgoOf(hash)
	}
	ok, rehash, err := s.hasher.VerifyLoginWithin(ctx, loginHashWait, algo, hash, in.Password)
	if errors.Is(err, password.ErrBusy) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	if u == nil || !ok {
		outcome = LoginFailed
		th.Delay(th.RecordFailure(account, source))
		return nil, ErrInvalidCredentials
	}
	if u.Status != StatusActive {
		return nil, fmt.Errorf("%w: %s", ErrInactiveUser, u.ID)
	}
	if u.PasswordResetRequired || u.SignInRestricted {
		return nil, fmt.Errorf("%w: %s", ErrResetRequired, u.ID)
	}
	th.Clear(account, source)
	// u is the record the password was just verified against, not a fresh
	// read: a password change landing between the check and the session
	// leaves the session with the old epoch, refused on first use.
	sess, err := s.IssueSession(ctx, u, OriginPassword, in.Remember, in.Client)
	if err != nil {
		return nil, err
	}
	outcome = LoginSucceeded
	if rehash {
		s.rehashPassword(ctx, u, hash, in.Password)
	}
	return sess, nil
}

// loginRecord reads the user named name and its stored hash. An unknown name
// is (nil, "", nil), not an error: the caller spends the same verification
// work on it. Any other failure is ErrStoreUnavailable. The user and the
// hash are two reads; a user deleted between them is an unknown name.
func (s *Service) loginRecord(ctx context.Context, name string) (*User, string, error) {
	u, err := s.d.Store.UserByUsername(ctx, FoldUsername(name))
	if errors.Is(err, ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	hash, err := s.d.Store.PasswordHash(ctx, u.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return u, hash, nil
}

// errRehashStale stops a rehash whose verified hash is no longer the stored
// one.
var errRehashStale = errors.New("auth: password changed since it was verified")

// rehashPassword re-stores plain as argon2id for u after a sign-in verified
// it against verified, a bcrypt hash (§4.5's transparent rehash). It is best
// effort: a busy hasher, a store failure, or a password changed since the
// verification (the stored hash is no longer verified) leaves the record
// as it is, and the sign-in has already succeeded. The epoch is not bumped:
// the password is the same, so no credential of the user goes stale.
func (s *Service) rehashPassword(ctx context.Context, u *User, verified, plain string) {
	_, next, err := s.hasher.HashWithin(ctx, loginHashWait, plain)
	if err == nil {
		err = s.commitRetrying(ctx, func(b *storage.Batch) error {
			cur, err := s.d.Store.GetUser(ctx, u.ID)
			if err != nil {
				return err
			}
			stored, err := s.d.Store.PasswordHash(ctx, u.ID)
			if err != nil {
				return err
			}
			if stored != verified {
				return errRehashStale
			}
			up := *cur
			up.Version = cur.Version + 1
			up.UpdatedAt = s.now().UTC()
			return s.d.Store.SetPassword(b, &up, next)
		})
	}
	if err != nil {
		s.log.Warn("auth: password rehash skipped", "user", string(u.ID), "err", err)
	}
}
