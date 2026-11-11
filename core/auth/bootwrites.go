// file: core/auth/bootwrites.go
// version: 1.1.1
// guid: c4e0a7b3-5d18-4f92-8a6c-1b9d3e7f2054
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// BootWrites (05-authn §3.2, §4.6, COR4-13).

// The system user, the start-up key and where its file lives.
const (
	// SystemUserID is the _system attribution user BootWrites seeds; it has
	// no password and never signs in.
	SystemUserID UserID = "_system"
	// ReadOnlyKeyName names the start-up read-only key, which the next boot
	// replaces.
	ReadOnlyKeyName = "Startup read-only key"
	// ReadOnlyKeyTTL is the start-up key's lifetime (HEAD's 24 h).
	ReadOnlyKeyTTL = 24 * time.Hour
	// ReadOnlyKeyFile and ReadOnlyKeyLabel are the key file and its state
	// filesystem label.
	ReadOnlyKeyFile  = "readonly-key"
	ReadOnlyKeyLabel = "auth.readonly_key"
)

// defaultReadOnlyScopes is core.auth.bootstrap.readonly_key.scopes's default.
func defaultReadOnlyScopes() []rbac.Permission { return []rbac.Permission{"core:system.view"} }

// BootWrites are auth's every-boot writes, run by the kernel in P4d step 4
// after PostRestore (DATA3-18): seed the _system user, and
//
//   - when core.auth.bootstrap.enabled (default true), delete every stored
//     bootstrap token of a previous boot, so a stale token never outlives a
//     restart; and, when a state filesystem is wired, mint a fresh one (ten
//     minutes, DefaultBootstrapTokenTTL) in the same batch and write it, mode
//     0600, to the bootstrap token file (label auth.bootstrap_token; with no
//     state filesystem nothing is minted, since nothing could deliver it);
//     the log names the file and the expiry, never the value; with it
//     disabled, delete every
//     stored bootstrap token and the file (a token minted while it was on
//     must not outlive the switch), whether or not a state filesystem is
//     wired, and RedeemBootstrap and a token-bearing Setup refuse;
//   - when core.auth.bootstrap.readonly_key.enabled, revoke the previous
//     start-up key and mint a new one, scoped by readonly_key.scopes (default
//     ["core:system.view"]; empty, wildcard and core:admin scopes are
//     refused), owned by the first active admin that is not SignInRestricted,
//     expiring in 24 h, written to the key file (label auth.readonly_key); a
//     store with no admin yet gets none until the next start; with the key
//     disabled a leftover start-up key is revoked.
//
// It is idempotent, and neither auth.New nor a Store constructor writes
// anything (DATA4-10). A file that cannot be written is logged, not fatal:
// the credential it carries is inert without it.
func BootWrites(ctx context.Context, d RestoreDeps) error {
	if err := d.check("Revocations"); err != nil {
		return err
	}
	if err := d.commit(ctx, func(b *storage.Batch) error { return stageSystemUser(ctx, d, b) }); err != nil {
		return fmt.Errorf("auth: boot writes: seed %s: %w", SystemUserID, err)
	}
	st := d.settings()
	if st.BootstrapEnabled == nil || *st.BootstrapEnabled {
		if err := bootstrapToken(ctx, d); err != nil {
			return fmt.Errorf("auth: boot writes: bootstrap token: %w", err)
		}
	} else if err := retireBootstrapToken(ctx, d); err != nil {
		return fmt.Errorf("auth: boot writes: retire the bootstrap token: %w", err)
	}
	if err := startupKey(ctx, d, st); err != nil {
		return fmt.Errorf("auth: boot writes: read-only key: %w", err)
	}
	return nil
}

// stageSystemUser stages the _system user when it is absent.
func stageSystemUser(ctx context.Context, d RestoreDeps, b *storage.Batch) error {
	_, err := d.Store.GetUser(ctx, SystemUserID)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, ErrNotFound):
		return err
	}
	now := d.now()
	return d.Store.PutUser(b, &User{
		ID: SystemUserID, Username: string(SystemUserID), Email: string(SystemUserID) + "@local", Status: StatusSystem,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	})
}

// bootstrapToken replaces the stored bootstrap token and writes its file.
// Without a state filesystem nothing could deliver a new token, but a token
// stored by an earlier boot that had one is still redeemable, so it is
// deleted all the same.
func bootstrapToken(ctx context.Context, d RestoreDeps) error {
	if d.StateFiles == nil {
		if err := deleteStoredBootstrapTokens(ctx, d); err != nil {
			return err
		}
		d.log().Info("auth: no state filesystem; the bootstrap token is not minted")
		return nil
	}
	var raw Token
	expires := d.now().Add(DefaultBootstrapTokenTTL)
	err := d.commit(ctx, func(b *storage.Batch) error {
		if err := stageDeleteBootstrapTokens(ctx, d, b); err != nil {
			return err
		}
		var err error
		raw, err = d.Store.IssueOneTime(b, &OneTimeToken{Kind: OneTimeBootstrap, CreatedAt: d.now(), ExpiresAt: expires})
		return err
	})
	if err != nil {
		return err
	}
	if err := d.StateFiles.WriteFile(BootstrapTokenLabel, BootstrapTokenFile, []byte(raw+"\n"), 0o600); err != nil {
		d.log().Warn("auth: bootstrap token file not written", "file", BootstrapTokenFile, "err", err)
		return nil
	}
	d.log().Info("auth: bootstrap token written", "file", BootstrapTokenFile, "expires_at", expires.Format(time.RFC3339))
	return nil
}

// stageDeleteBootstrapTokens stages the deletion of every stored bootstrap
// token. An undecodable one is left in place, logged: it cannot be named for
// deletion, and a redeem of it is refused as invalid.
func stageDeleteBootstrapTokens(ctx context.Context, d RestoreDeps, b *storage.Batch) error {
	for t, err := range d.Store.ScanOneTime(ctx, OneTimeBootstrap) {
		if err != nil {
			if leftUndecodable(d.log(), "onetime", "", err) {
				continue
			}
			return err
		}
		if err := d.Store.DeleteOneTime(b, OneTimeBootstrap, t.ID); err != nil {
			return err
		}
	}
	return nil
}

// deleteStoredBootstrapTokens deletes every stored bootstrap token in one
// batch.
func deleteStoredBootstrapTokens(ctx context.Context, d RestoreDeps) error {
	return d.commit(ctx, func(b *storage.Batch) error { return stageDeleteBootstrapTokens(ctx, d, b) })
}

// retireBootstrapToken is BootWrites' answer to core.auth.bootstrap.enabled
// = false: the stored tokens and the token file go, so a token minted while
// the switch was on cannot be redeemed after it was turned off. The store
// deletion needs no state filesystem; the file removal needs one and is best
// effort (a leftover file names a token that no longer exists).
func retireBootstrapToken(ctx context.Context, d RestoreDeps) error {
	if err := deleteStoredBootstrapTokens(ctx, d); err != nil {
		return err
	}
	if d.StateFiles == nil {
		return nil
	}
	if err := d.StateFiles.Remove(BootstrapTokenLabel, BootstrapTokenFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		d.log().Warn("auth: bootstrap token file not removed", "file", BootstrapTokenFile, "err", err)
	}
	return nil
}

// startupKey revokes the previous start-up key and, when enabled, mints and
// writes the new one.
func startupKey(ctx context.Context, d RestoreDeps, st Settings) error {
	scopes := st.ReadOnlyKeyScopes
	if st.ReadOnlyKey {
		if len(scopes) == 0 {
			scopes = defaultReadOnlyScopes()
		}
		if err := checkStartupScopes(scopes); err != nil {
			return err
		}
	}
	var (
		raw     Token
		expires time.Time
		minted  bool
	)
	err := d.commit(ctx, func(b *storage.Batch) error {
		minted = false
		for k, err := range d.Store.ScanAPIKeys(ctx, "") {
			if err != nil {
				if leftUndecodable(d.log(), "apikey", "", err) {
					continue
				}
				return err
			}
			if k.Name != ReadOnlyKeyName || k.Status == KeyRevoked {
				continue
			}
			next := *k
			next.Status, next.RevokedAt, next.Version = KeyRevoked, d.now(), k.Version+1
			if err := d.Store.PutAPIKey(b, &next); err != nil {
				return err
			}
			d.line(ctx, b, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: ref(refKindAPIKey, k.ID)})
		}
		if !st.ReadOnlyKey {
			return nil
		}
		owner, err := firstActiveAdmin(ctx, d)
		if err != nil {
			return err
		}
		if owner == nil {
			d.log().Info("auth: no active admin yet; the start-up read-only key is minted at the next start")
			return nil
		}
		expires = d.now().Add(ReadOnlyKeyTTL)
		raw, err = d.Store.IssueKey(b, &APIKey{
			Owner: owner.ID, Name: ReadOnlyKeyName, Scopes: append([]rbac.Permission(nil), scopes...),
			Status: KeyActive, CreatedAt: d.now(), ExpiresAt: expires,
		})
		minted = err == nil
		return err
	})
	if err != nil || !minted {
		return err
	}
	if d.StateFiles == nil {
		d.log().Info("auth: no state filesystem; the start-up read-only key file is not written", "expires_at", expires.Format(time.RFC3339))
		return nil
	}
	if err := d.StateFiles.WriteFile(ReadOnlyKeyLabel, ReadOnlyKeyFile, []byte(raw+"\n"), 0o600); err != nil {
		d.log().Warn("auth: read-only key file not written", "file", ReadOnlyKeyFile, "err", err)
		return nil
	}
	d.log().Info("auth: start-up read-only key written", "file", ReadOnlyKeyFile, "expires_at", expires.Format(time.RFC3339))
	return nil
}

// checkStartupScopes refuses a scope list no read-only key may carry.
func checkStartupScopes(scopes []rbac.Permission) error {
	for _, s := range scopes {
		p, err := rbac.ParsePermission(string(s))
		if err != nil {
			return fmt.Errorf("%w: readonly_key scope %q: %w", ErrBadInput, s, err)
		}
		if p == rbac.CoreAdmin {
			return fmt.Errorf("%w: the start-up key may not hold %s", ErrBadInput, rbac.CoreAdmin)
		}
	}
	return nil
}

// firstActiveAdmin is the first (by user ID) active core:admin holder that is
// not SignInRestricted, nil when there is none or no role reader is wired.
func firstActiveAdmin(ctx context.Context, d RestoreDeps) (*User, error) {
	if d.RoleGrantsAdmin == nil {
		return nil, nil
	}
	holds := adminHoldsWith(ctx, d.RoleGrantsAdmin)
	var found *User
	for u, err := range d.Store.ScanUsers(ctx) {
		if err != nil {
			if leftUndecodable(d.log(), "user", "", err) {
				continue
			}
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if u.SignInRestricted || (found != nil && u.ID > found.ID) {
			continue
		}
		ok, err := holds(u.Roles, u.Status)
		if err != nil {
			return nil, err
		}
		if ok {
			found = u
		}
	}
	return found, nil
}
