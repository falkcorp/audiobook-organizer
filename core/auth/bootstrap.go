// file: core/auth/bootstrap.go
// version: 1.1.0
// guid: 9c1b0e37-5a64-4f0b-8d0e-3b1f7e4c2a95
// last-edited: 2026-11-11

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Bootstrap and first run (05-authn §4.6; G10: app-agnostic). Nothing here
// refers to an app, a module or users.manage.
const (
	// DefaultBootstrapTokenTTL is how long a bootstrap token lives, HEAD's
	// ten minutes: a restart is what mints the next one, so an unexpected
	// restart shows in the logs.
	DefaultBootstrapTokenTTL = 10 * time.Minute
	// BootstrapTokenFile is the token file's name in the app's state
	// directory (mode 0600), written through Deps.StateFiles.
	BootstrapTokenFile = "bootstrap-token"
	// BootstrapTokenLabel is the label the token file's writes carry in the
	// labelled state filesystem (coretest/crashmatrix sees it by this).
	BootstrapTokenLabel = "auth.bootstrap_token"

	actionBootstrapRedeem = "core.bootstrap.redeem"
	bootstrapKeyName      = "bootstrap"
	bootstrapAdminName    = "admin"
	// bootstrapNameTries bounds the admin-name suffix search (HEAD's 100).
	bootstrapNameTries = 100
	// generatedPasswordLen is the length of a generated admin password.
	generatedPasswordLen = 24
)

// StateFiles is auth's view of the labelled state filesystem, the
// core/internal/statefs value the kernel builds, so coretest/crashmatrix
// sees, labels and can kill auth's file writes (COR10-7). Names are
// relative to the app's state directory, and a name that leaves it is
// refused, as os.Root refused it.
type StateFiles interface {
	WriteFile(label, name string, data []byte, perm fs.FileMode) error // temp file, fsync, rename, directory fsync
	Remove(label, name string) error
}

// RedeemBootstrap exchanges the bootstrap token for an API key (§4.6 item
// 2). It is the one place a bootstrap token is checked and spent, and it
// spends it in the batch that creates what it hands out, so a failed
// creation leaves the token unspent (HEAD wrote the hash back) and a crash
// leaves either everything or nothing.
//
// The source's throttle (§4.9) refuses a spent budget first; a token that is
// malformed or not stored is a failed credential check, charged to the
// source (ErrBootstrapInvalid). A stored token is spent whatever the answer:
// past its expiry it is deleted, audited, and ErrBootstrapInvalid answers.
//
// The key belongs to the first active user holding core:admin, role-resolved
// and not SignInRestricted (a key for a restricted account is refused at
// authentication, so it would make the redeem look successful and then fail,
// the reason HEAD skipped inactive admins); that user is guarded in the
// batch, so a demotion committing first fails the commit and the retry picks
// again. With none, it creates "admin" (the first free of admin, admin-2,
// ... admin-100) with role admin and a generated password, returned once in
// GeneratedPassword. The key is scoped ["core:admin"], lives
// core.auth.bootstrap.key_ttl and is non-interactive (its Sensitive scopes
// stay out of reach, 05-rbac §4.4). Audit: core.bootstrap.redeem, and
// core.user.create for an account it created. Removing the token file is
// best effort after the commit.
func (s *Service) RedeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error) {
	res, err := s.redeemBootstrap(ctx, token, client)
	s.met.redemption(ctx, err)
	return res, err
}

func (s *Service) redeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error) {
	source := client.sourceKey()
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		s.bootstrapRefused(ctx, client, reasonThrottled, "")
		return nil, s.throttledOn(surfaceBootstrap, LoginRetryAfter)
	}
	if _, err := s.lookupBootstrap(ctx, token, source, client); err != nil {
		return nil, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()

	var (
		res     *BootstrapResult
		refusal error
		pw      string // generated once, kept across a commit retry
		hash    string
	)
	err := s.commitRetrying(ctx, func(b *storage.Batch) error {
		res, refusal = nil, nil
		t, err := s.d.Store.TakeOneTime(b, OneTimeBootstrap, Token(token)) // parsed by lookupBootstrap
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: the token was spent concurrently", ErrBootstrapInvalid)
		}
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if !now.Before(t.ExpiresAt) {
			// The take is committed alone: an expired token is spent.
			refusal = fmt.Errorf("%w: the token expired at %s", ErrBootstrapInvalid, t.ExpiresAt.Format(time.RFC3339))
			return s.auditRefusedBootstrap(ctx, b, client, reasonExpired, t.ID)
		}
		admin, taken, err := s.firstActiveAdmin(ctx)
		if err != nil {
			return err
		}
		created := admin == nil
		if created {
			if hash == "" {
				if pw, err = generatePassword(max(generatedPasswordLen, s.settings().MinPasswordLength)); err != nil {
					return err
				}
				if hash, err = s.hashNew(ctx, pw); err != nil {
					return err
				}
			}
			if admin, err = s.stageFirstAdmin(ctx, b, taken, "", hash, now); err != nil {
				return err
			}
			if err := s.auditFirstAdmin(ctx, b, admin, client, t.ID, "bootstrap"); err != nil {
				return err
			}
		} else if err := s.d.Store.GuardUser(b, admin); err != nil {
			return err
		}
		k := &APIKey{
			Owner: admin.ID, Name: bootstrapKeyName, Scopes: []rbac.Permission{rbac.CoreAdmin}, Status: KeyActive,
			CreatedAt: now, ExpiresAt: now.Add(s.settings().BootstrapKeyTTL),
		}
		raw, err := s.d.Store.IssueKey(b, k)
		if err != nil {
			return err
		}
		if err := s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionBootstrapRedeem, Outcome: audit.Success, Target: ref(refKindAPIKey, k.ID),
			Actor:   audit.Actor{ClientAddr: addrString(client.Addr), UserAgent: client.UserAgent},
			Details: map[string]any{"user": string(admin.ID), "token": t.ID, "created_user": created, "expires_at": k.ExpiresAt.Format(time.RFC3339)},
		}); err != nil {
			return err
		}
		res = &BootstrapResult{Key: string(raw), KeyID: k.ID, ExpiresAt: k.ExpiresAt, User: admin}
		if created {
			res.GeneratedPassword = pw
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	s.hasUsers.Store(true)
	s.removeBootstrapFile()
	return res, nil
}

// Setup creates the first admin with a chosen password (§4.6 items 3-4). It
// is open only while HasUsers is false (ErrSetupClosed otherwise), and the
// request must present the bootstrap token unless
// core.auth.setup_requires_token is false and the request is Local
// (ClientInfo.Local: a loopback peer, no forwarding header, not a trusted
// proxy or connector); a missing token is ErrSetupClosed, a presented one is
// always checked, whether or not it was required (a wrong token never falls
// through), and is spent in the batch that creates the admin, so an
// unspent-on-failure token cannot be replayed once a user exists.
//
// The username and password are checked and the password hashed before the
// token is looked at, so a malformed request spends nothing. The source's
// throttle (§4.9) applies as in RedeemBootstrap. The admin has role admin,
// an unverified e-mail if one is given, and is audited core.user.create. It
// returns the created user; no session is opened.
func (s *Service) Setup(ctx context.Context, in SetupInput) (*User, error) {
	source := in.Client.sourceKey()
	th := s.loginThrottle()
	if th.SourceBlocked(source) {
		s.bootstrapRefused(ctx, in.Client, reasonThrottled, "")
		return nil, s.throttledOn(surfaceSetup, LoginRetryAfter)
	}
	if s.HasUsers() {
		return nil, fmt.Errorf("%w: a user already exists", ErrSetupClosed)
	}
	needToken := s.setupRequiresToken() || !in.Client.Local()
	if needToken && in.BootstrapToken == "" {
		return nil, fmt.Errorf("%w: the bootstrap token is required", ErrSetupClosed)
	}
	name := strings.TrimSpace(in.Username)
	if name == "" {
		return nil, fmt.Errorf("%w: a username is required", ErrBadInput)
	}
	if in.Password == "" {
		return nil, fmt.Errorf("%w: a password is required", ErrBadInput)
	}
	if err := s.checkNewPassword(in.Password); err != nil {
		return nil, err
	}
	hash, err := s.hashNew(ctx, in.Password)
	if err != nil {
		return nil, err
	}
	if in.BootstrapToken != "" {
		if _, err := s.lookupBootstrap(ctx, in.BootstrapToken, source, in.Client); err != nil {
			return nil, err
		}
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()

	var out *User
	var refusal error
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		out, refusal = nil, nil
		var tokenID string
		if in.BootstrapToken != "" {
			t, err := s.d.Store.TakeOneTime(b, OneTimeBootstrap, Token(in.BootstrapToken))
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: the token was spent concurrently", ErrBootstrapInvalid)
			}
			if err != nil {
				return err
			}
			if !s.now().Before(t.ExpiresAt) {
				refusal = fmt.Errorf("%w: the token expired at %s", ErrBootstrapInvalid, t.ExpiresAt.Format(time.RFC3339))
				return s.auditRefusedBootstrap(ctx, b, in.Client, reasonExpired, t.ID)
			}
			tokenID = t.ID
		}
		taken, err := s.firstUser(ctx)
		if err != nil {
			return err
		}
		if len(taken) > 0 {
			return fmt.Errorf("%w: a user already exists", ErrSetupClosed) // nothing committed: the token stays unspent
		}
		u, err := s.stageFirstAdminNamed(ctx, b, name, strings.TrimSpace(in.Email), hash, s.now().UTC())
		if err != nil {
			return err
		}
		out = u
		return s.auditFirstAdmin(ctx, b, u, in.Client, tokenID, "setup")
	})
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	s.hasUsers.Store(true)
	if in.BootstrapToken != "" {
		s.removeBootstrapFile()
	}
	return out, nil
}

// setupRequiresToken is core.auth.setup_requires_token: true unless set false.
func (s *Service) setupRequiresToken() bool {
	if v := s.settings().SetupRequiresToken; v != nil {
		return *v
	}
	return true
}

// lookupBootstrap reads the bootstrap token raw names without spending it.
// A value that is malformed or names no stored token is a failed credential
// check charged to source (ErrBootstrapInvalid) and audited on the async
// path; a store failure is ErrStoreUnavailable and charges nothing.
func (s *Service) lookupBootstrap(ctx context.Context, raw, source string, client ClientInfo) (*OneTimeToken, error) {
	t, err := s.lookupOneTime(ctx, OneTimeBootstrap, KindBootstrap, raw)
	if err == nil {
		return t, nil
	}
	if chargeable(err) {
		s.loginThrottle().RecordFailure("", source)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		s.bootstrapRefused(ctx, client, reasonInvalidCredentials, "")
		return nil, fmt.Errorf("%w: %w", ErrBootstrapInvalid, err)
	}
	return nil, err
}

// bootstrapRefused audits, on the async path, a bootstrap redeem or setup
// refused before any token resolved.
func (s *Service) bootstrapRefused(ctx context.Context, c ClientInfo, reason, tokenID string) {
	ev := audit.Event{
		Action: actionBootstrapRedeem, Outcome: audit.Failure, Reason: reason,
		Actor: audit.Actor{ClientAddr: addrString(c.Addr), UserAgent: c.UserAgent},
	}
	if tokenID != "" {
		ev.Details = map[string]any{"token": tokenID}
	}
	s.d.Audit.Emit(ctx, ev)
}

// auditRefusedBootstrap stages the refusal record of a spent-but-refused
// token into b, which commits the take alone.
func (s *Service) auditRefusedBootstrap(ctx context.Context, b *storage.Batch, c ClientInfo, reason, tokenID string) error {
	return s.d.Audit.RecordIn(ctx, b, audit.Event{
		Action: actionBootstrapRedeem, Outcome: audit.Failure, Reason: reason,
		Actor:   audit.Actor{ClientAddr: addrString(c.Addr), UserAgent: c.UserAgent},
		Details: map[string]any{"token": tokenID},
	})
}

// firstActiveAdmin is the first user, in stored order (oldest first, the ID
// being a ULID), that holds core:admin and may sign in: active, a role that
// grants it (adminHolds), and not SignInRestricted. It also returns every
// folded username in the store, for the name search. An undecodable record
// holds nothing and its name is unknown (the store refuses a taken one).
func (s *Service) firstActiveAdmin(ctx context.Context) (*User, map[string]bool, error) {
	holds := s.adminHolds(ctx)
	var found *User
	taken := map[string]bool{}
	for u, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		taken[FoldUsername(u.Username)] = true
		if found != nil || u.SignInRestricted {
			continue
		}
		ok, err := holds(u.Roles, u.Status)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			found = u
		}
	}
	return found, taken, nil
}

// firstUser reports every folded username of a user other than _system:
// Setup's "no user exists" rescan inside its batch.
func (s *Service) firstUser(ctx context.Context) (map[string]bool, error) {
	taken := map[string]bool{}
	for u, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			// It may be a user: setup is closed, as HasUsers would not know.
			taken["?"] = true
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if u.Status != StatusSystem {
			taken[FoldUsername(u.Username)] = true
		}
	}
	return taken, nil
}

// stageFirstAdmin stages a new active admin named by the first free of
// "admin", "admin-2", ... among taken, with password hash hash.
func (s *Service) stageFirstAdmin(ctx context.Context, b *storage.Batch, taken map[string]bool, email, hash string, now time.Time) (*User, error) {
	name := bootstrapAdminName
	for n := 2; taken[FoldUsername(name)]; n++ {
		if n > bootstrapNameTries {
			return nil, fmt.Errorf("%w: no free name among admin, admin-2 ... admin-%d", ErrUserExists, bootstrapNameTries)
		}
		name = fmt.Sprintf("%s-%d", bootstrapAdminName, n)
	}
	return s.stageFirstAdminNamed(ctx, b, name, email, hash, now)
}

// stageFirstAdminNamed stages a new active user named name with role admin.
// The e-mail is recorded unverified: nothing proved it.
func (s *Service) stageFirstAdminNamed(ctx context.Context, b *storage.Batch, name, email, hash string, now time.Time) (*User, error) {
	id, err := newULID(now, rand.Reader)
	if err != nil {
		return nil, err
	}
	u := &User{
		ID: UserID(id), Username: name, Email: email, HasPassword: hash != "",
		Roles: []rbac.RoleName{rbac.RoleAdmin}, Status: StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	rec := *u
	rec.Roles = slices.Clone(u.Roles)
	if err := s.d.Store.SetPassword(b, &rec, hash); err != nil {
		return nil, err
	}
	return u, nil
}

// auditFirstAdmin stages core.user.create for an admin that setup or a
// bootstrap redeem created.
func (s *Service) auditFirstAdmin(ctx context.Context, b *storage.Batch, u *User, c ClientInfo, tokenID, via string) error {
	d := map[string]any{"username": u.Username, "roles": roleStrings(u.Roles), "via": via}
	if tokenID != "" {
		d["token"] = tokenID
	}
	return s.d.Audit.RecordIn(ctx, b, audit.Event{
		Action: actionUserCreate, Outcome: audit.Success, Target: ref(refKindUser, string(u.ID)),
		Actor:   audit.Actor{ClientAddr: addrString(c.Addr), UserAgent: c.UserAgent},
		Details: d,
	})
}

// removeBootstrapFile removes the token file after a spend, best effort: a
// token without its stored hash is inert. The log names the file, never a
// credential.
func (s *Service) removeBootstrapFile() {
	if s.d.StateFiles == nil {
		return
	}
	if err := s.d.StateFiles.Remove(BootstrapTokenLabel, BootstrapTokenFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("auth: could not remove the spent bootstrap token file", "file", BootstrapTokenFile, "err", err)
	}
}

// generatePassword is n characters of crypto/rand, base64url.
func generatePassword(n int) (string, error) {
	raw := make([]byte, (n*3+3)/4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)[:n], nil
}
