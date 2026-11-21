// file: core/auth/offline_reset.go
// version: 0.1.0
// guid: c4a81e07-2d5b-4f93-9a6e-0b7d3c5e1f28
// last-edited: 2026-11-21

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// The offline `<app> users reset-password` write (05-authn §4.14 "The offline
// command"; F4-08 B9b). It is the one way back in for an account whose
// sign-in a restore restricted, and for an owner who has lost every other way
// in, so it is held to the same rules as the reset link it stands in for
// (MintResetLink then ConsumeTempLogin) and to nothing looser:
//
//   - the target must exist, be an ordinary account (never _system) and be
//     active. A disabled or pending account is refused, as a reset link
//     refuses it: a password is never what turns an account back on.
//   - the new password is generated here, from crypto/rand, and returned to
//     the caller once. Nothing in this package stores, logs or audits it; the
//     caller prints it to its own standard output. No password is read from a
//     flag or an environment variable, because both outlive the command in
//     the shell's history and in the process table.
//   - the credential epoch is bumped, so every session of the account and
//     every one-time token minted for it dies, and password.changed and
//     credential_epoch.bumped are journaled in the same batch, as a reset
//     link's consume does.
//   - PasswordResetRequired and SignInRestricted are cleared, which is what
//     "until ... the offline users reset-password clears it" means.
//   - nothing else changes. In particular the external-identity links a
//     restore deleted (identity.unlinked, restrictUser) stay deleted: the
//     command does not re-link an identity, and it does not touch API keys,
//     which a restore left needs_confirmation until an admin re-enables them
//     from the keys page. Roles and status are never written.
//   - the audit record is written in the same batch, Method "offline", with
//     no client address and no credential.

// auditMethodOffline is the audit Actor.Method of a write made by an offline
// command: there is no principal, only the operator who owns the state
// directory.
const auditMethodOffline = "offline"

// ErrResetPasswordTarget refuses a reset-password target that is not an
// account the command may write; the wrapped error says why.
var ErrResetPasswordTarget = errors.New("auth: reset-password target refused")

// OfflineResetDeps are what ResetPasswordOffline needs.
type OfflineResetDeps struct {
	Store Store
	// NewBatch is an indexed batch over Store's keyspace (authstore.Store.NewBatch).
	NewBatch func() *storage.Batch
	// Audit records the write in the same batch; required.
	Audit audit.Logger
	// Revocations takes the two revocation lines; required. An Append that
	// fails records a gap and the reset still commits (the journal's own
	// contract: a disk problem never blocks a security action), and the
	// result says so.
	Revocations journal.RevocationJournal
	// Settings are the live core.auth.* values; nil takes every default.
	Settings func() Settings
	Clock    func() time.Time
	// Hasher derives the stored hash; nil builds the production one.
	Hasher *password.Hasher
	Log    *slog.Logger
}

// OfflineResetResult is what a reset did. Password is the new plaintext
// password: the only place it ever exists outside the hash. It redacts itself
// from every formatter and logger, so a stray %v or slog attribute never
// carries it.
type OfflineResetResult struct {
	UserID   UserID
	Username string
	Password string
	// ClearedRestriction and ClearedResetRequired say which flags the reset
	// found set and cleared.
	ClearedRestriction   bool
	ClearedResetRequired bool
	// Epoch is the credential epoch the account has now.
	Epoch int64
	// JournalGap is true when a revocation line could not be appended and a
	// gap was recorded instead.
	JournalGap bool
}

const redactedPassword = "[redacted]"

// String is the result without its password.
func (r OfflineResetResult) String() string { return r.GoString() }

// GoString is the result without its password (the %#v form).
func (r OfflineResetResult) GoString() string {
	return fmt.Sprintf("auth.OfflineResetResult{UserID:%q Username:%q Password:%s Epoch:%d}", r.UserID, r.Username, redactedPassword, r.Epoch)
}

// LogValue keeps the password out of a structured log.
func (r OfflineResetResult) LogValue() slog.Value {
	return slog.GroupValue(slog.String("user", string(r.UserID)), slog.String("password", redactedPassword), slog.Int64("epoch", r.Epoch))
}

func (d OfflineResetDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock().UTC()
	}
	return time.Now().UTC()
}

func (d OfflineResetDeps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d OfflineResetDeps) settings() Settings {
	if d.Settings == nil {
		return Settings{}.withDefaults()
	}
	return d.Settings().withDefaults()
}

// ResetPasswordOffline resets the password of the account whose username folds
// to username (see the file comment for the rules) and returns the generated
// password. Nothing is written when it refuses.
func ResetPasswordOffline(ctx context.Context, d OfflineResetDeps, username string) (OfflineResetResult, error) {
	var res OfflineResetResult
	var missing []string
	if d.Store == nil {
		missing = append(missing, "Store")
	}
	if d.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if d.Revocations == nil {
		missing = append(missing, "Revocations")
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("auth: offline reset: missing deps %v", missing)
	}
	u, err := LookupResetTarget(ctx, d.Store, username)
	if err != nil {
		return res, err
	}
	plain, err := generatePassword(max(generatedPasswordLen, d.settings().MinPasswordLength))
	if err != nil {
		return res, err
	}
	hasher := d.Hasher
	if hasher == nil {
		hasher = password.New(password.Options{})
	}
	// Before any batch opens, so a commit retry never derives twice.
	_, hash, err := hasher.HashWithin(ctx, offlineHashWait, plain)
	if err != nil {
		return res, fmt.Errorf("auth: offline reset: hash the new password: %w", err)
	}
	now := d.now()
	err = storage.RetryOnConflict(ctx, conflictRetries, func(ctx context.Context) (err error) {
		res = OfflineResetResult{}
		b := d.NewBatch()
		defer func() {
			if cerr := b.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}()
		cur, err := d.Store.GetUser(ctx, u.ID)
		if err != nil {
			return err
		}
		// Re-checked on the row this attempt writes: it may have changed
		// since the lookup.
		if err := resetTargetRefusal(cur); err != nil {
			return err
		}
		up := *cur
		up.Version = cur.Version + 1
		up.CredentialEpoch = cur.CredentialEpoch + 1
		up.HasPassword = true
		up.PasswordResetRequired = false
		up.SignInRestricted = false
		up.UpdatedAt = now
		if err := d.Store.SetPassword(b, &up, hash); err != nil {
			return err
		}
		target := ref(refKindUser, string(up.ID))
		gap := false
		for _, kind := range []journal.Kind{journal.PasswordChanged, journal.CredentialEpochBumped} {
			if !d.line(ctx, b, journal.Revocation{Kind: kind, Subject: target, Epoch: up.CredentialEpoch, At: now}) {
				gap = true
			}
		}
		res = OfflineResetResult{
			UserID: up.ID, Username: up.Username, Password: plain,
			ClearedRestriction: cur.SignInRestricted, ClearedResetRequired: cur.PasswordResetRequired,
			Epoch: up.CredentialEpoch, JournalGap: gap,
		}
		if err := d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserPassword, Outcome: audit.Success, Target: target,
			Actor: audit.Actor{Method: auditMethodOffline},
			Details: map[string]any{
				"offline":                true,
				"cleared_restriction":    cur.SignInRestricted,
				"cleared_reset_required": cur.PasswordResetRequired,
			},
		}); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return OfflineResetResult{}, fmt.Errorf("auth: offline reset: %w", err)
	}
	return res, nil
}

// LookupResetTarget is the account an offline reset of username would write,
// or the refusal it would give: no such user (ErrNotFound), the system user or
// a disabled or pending account (ErrResetPasswordTarget). It writes nothing, so
// a caller can refuse before it creates anything.
func LookupResetTarget(ctx context.Context, st Store, username string) (*User, error) {
	name := strings.TrimSpace(username)
	if name == "" {
		return nil, fmt.Errorf("%w: a username is required", ErrBadInput)
	}
	u, err := st.UserByUsername(ctx, FoldUsername(name))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: no user named %q", ErrNotFound, name)
		}
		return nil, err
	}
	if err := resetTargetRefusal(u); err != nil {
		return nil, err
	}
	return u, nil
}

// offlineHashWait bounds the wait for a hashing permit: an offline command is
// the only hasher user in its process, so a wait is a stuck hasher.
const offlineHashWait = 10 * time.Second

// resetTargetRefusal is the rule of who an offline reset may write.
func resetTargetRefusal(u *User) error {
	if err := mutable(u); err != nil {
		return fmt.Errorf("%w: %w", ErrResetPasswordTarget, err)
	}
	if u.Status != StatusActive {
		return fmt.Errorf("%w: %w: %s is %s, and a password does not turn an account back on", ErrResetPasswordTarget, ErrInactiveUser, u.ID, u.Status)
	}
	return nil
}

// line appends r into b; a failed Append records a gap and reports false.
func (d OfflineResetDeps) line(ctx context.Context, b *storage.Batch, r journal.Revocation) (appended bool) {
	if err := d.Revocations.Append(ctx, b, r); err != nil {
		d.log().Warn("auth: revocation journal append failed; recording a gap", "kind", string(r.Kind), "err", err)
		if gerr := d.Revocations.Gap(b, r.At); gerr != nil {
			d.log().Error("auth: revocation journal gap failed", "kind", string(r.Kind), "err", gerr)
		}
		return false
	}
	return true
}
