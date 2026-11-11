// file: core/auth/restore.go
// version: 1.1.0
// guid: 2b7d94e1-6a30-4c58-b9f2-e41c8d05a763
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Post-restore hook and every-boot writes (05-authn §3.2, §4.14, SEC-16).

// RestoreDeps are the collaborators of PostRestore, RestrictLastAdmin and
// BootWrites. They run at P4d, before rbac.Service and auth.New exist, so
// they take the Store and the few service-level pieces they need directly.
// NewBatch, Settings, RoleGrantsAdmin and Log are coined by F4-02b
// (seams.d/F4-02.md).
type RestoreDeps struct {
	Store Store // the Store built in P4c
	// Previous is a read-only Store over the pre-swap store of a staged
	// restore (§4.14 step 3); nil when there is none.
	Previous    Store
	Revocations journal.RevocationJournal // the lines the hook appends (§4.12)
	StateFiles  StateFiles                // bootstrap token and read-only key files (COR10-7); nil writes no file
	Clock       func() time.Time
	// NewBatch is an indexed batch over Store's keyspace (authstore.Store.NewBatch).
	NewBatch func() *storage.Batch
	// Settings are the live core.auth.* values; nil takes every default.
	Settings func() Settings
	// RoleGrantsAdmin reports whether the named role's permissions grant
	// core:admin (rbac.GrantsAdmin over the stored role; an unknown role
	// grants nothing). BootWrites needs it to pick the read-only key's owner
	// before rbac.Service exists; nil means no admin can be found and the
	// key is skipped.
	RoleGrantsAdmin func(ctx context.Context, name rbac.RoleName) (bool, error)
	Log             *slog.Logger
}

// RestoreCounts is the per-family count of rows a hook revoked, deleted or
// restricted (the keys below), which audit's own hook records as
// core.restore.revoke.
type RestoreCounts map[string]int

// The RestoreCounts families.
const (
	CountKeysRevoked        = "api_keys_revoked"
	CountKeysDisabled       = "api_keys_disabled"
	CountKeysUnconfirmed    = "api_keys_unconfirmed"
	CountUsersDisabled      = "users_disabled"
	CountUsersDeleted       = "users_deleted"
	CountPasswordsBlanked   = "passwords_blanked"
	CountIdentitiesUnlinked = "identities_unlinked"
	CountSessionsRevoked    = "sessions_revoked"
	CountSignInsDeleted     = "signins_deleted"
	CountEpochsBumped       = "epochs_bumped"
	CountUsersRestricted    = "users_restricted"
)

func (d RestoreDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock().UTC()
	}
	return time.Now().UTC()
}

func (d RestoreDeps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d RestoreDeps) settings() Settings {
	if d.Settings == nil {
		return Settings{}.withDefaults()
	}
	return d.Settings().withDefaults()
}

func (d RestoreDeps) check(need ...string) error {
	var missing []string
	if d.Store == nil {
		missing = append(missing, "Store")
	}
	if d.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	for _, n := range need {
		if n == "Revocations" && d.Revocations == nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("auth: restore: missing deps %v", missing)
	}
	return nil
}

// commit runs stage over a fresh batch and commits it Sync, again on
// storage.ErrConflict. stage re-reads everything it decides on.
func (d RestoreDeps) commit(ctx context.Context, stage func(b *storage.Batch) error) error {
	return storage.RetryOnConflict(ctx, conflictRetries, func(ctx context.Context) (err error) {
		b := d.NewBatch()
		defer func() {
			if cerr := b.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}()
		if err := stage(b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// line appends r into b before the batch commits (§4.12): a failed Append
// records a gap instead and never blocks the restriction.
func (d RestoreDeps) line(ctx context.Context, b *storage.Batch, r journal.Revocation) {
	r.At = d.now()
	if err := d.Revocations.Append(ctx, b, r); err != nil {
		d.log().Warn("auth: revocation journal append failed; recording a gap", "kind", string(r.Kind), "err", err)
		if gerr := d.Revocations.Gap(b, r.At); gerr != nil {
			d.log().Error("auth: revocation journal gap failed", "kind", string(r.Kind), "err", gerr)
		}
	}
}

// keyRank orders key statuses by restriction (§4.14 step 3): revoked >
// needs_confirmation > disabled > active.
func keyRank(s KeyStatus) int {
	switch s {
	case KeyRevoked:
		return 3
	case KeyNeedsConfirmation:
		return 2
	case KeyDisabled:
		return 1
	}
	return 0
}

// restorePlan is one pass over the identity store: the state read, the
// restrictions decided and the lines they owe the journal. Nothing is
// written until stage.
type restorePlan struct {
	d      RestoreDeps
	now    time.Time
	counts RestoreCounts

	users     map[UserID]*User
	userDirty map[UserID]bool
	blank     map[UserID]bool // hash to blank
	deleted   map[UserID]bool
	keys      map[string]*APIKey
	keyDirty  map[string]bool
	idents    map[[2]string]*ExternalIdentity
	identDel  map[[2]string]bool
	sessions  []*SessionRecord // journaled session revocations to delete
	lines     []journal.Revocation
}

// load reads every user, key and identity (an undecodable record is skipped:
// it cannot be named, and auth already refuses it).
func (d RestoreDeps) load(ctx context.Context) (*restorePlan, error) {
	p := &restorePlan{
		d: d, now: d.now(), counts: RestoreCounts{},
		users: map[UserID]*User{}, userDirty: map[UserID]bool{}, blank: map[UserID]bool{}, deleted: map[UserID]bool{},
		keys: map[string]*APIKey{}, keyDirty: map[string]bool{},
		idents: map[[2]string]*ExternalIdentity{}, identDel: map[[2]string]bool{},
	}
	for u, err := range d.Store.ScanUsers(ctx) {
		if err != nil {
			if leftUndecodable(d.log(), "user", "", err) {
				continue
			}
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		cp := *u
		p.users[u.ID] = &cp
	}
	for k, err := range d.Store.ScanAPIKeys(ctx, "") {
		if err != nil {
			if leftUndecodable(d.log(), "apikey", "", err) {
				continue
			}
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		cp := *k
		p.keys[k.ID] = &cp
	}
	for id, err := range d.Store.ScanIdentities(ctx, "") {
		if err != nil {
			if leftUndecodable(d.log(), "identity", "", err) {
				continue
			}
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		p.idents[[2]string{id.Provider, id.Subject}] = id
	}
	return p, nil
}

func (p *restorePlan) count(family string) { p.counts[family]++ }

// owe records a line the hook itself owes the journal (a restriction the
// journal did not already hold).
func (p *restorePlan) owe(derived bool, r journal.Revocation) {
	if derived {
		p.lines = append(p.lines, r)
	}
}

// setKey moves key id to status st when that is more restrictive than its
// current one; a revoked key is terminal and never leaves revoked.
func (p *restorePlan) setKey(id string, st KeyStatus, derived bool) {
	k := p.keys[id]
	if k == nil || keyRank(st) <= keyRank(k.Status) {
		return
	}
	k.Status = st
	p.keyDirty[id] = true
	subject := ref(refKindAPIKey, id)
	switch st {
	case KeyRevoked:
		if k.RevokedAt.IsZero() {
			k.RevokedAt = p.now
		}
		p.count(CountKeysRevoked)
		p.owe(derived, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: subject})
	case KeyNeedsConfirmation:
		p.count(CountKeysUnconfirmed)
		p.owe(derived, journal.Revocation{Kind: journal.APIKeyDisabled, Subject: subject})
	default:
		p.count(CountKeysDisabled)
		p.owe(derived, journal.Revocation{Kind: journal.APIKeyDisabled, Subject: subject})
	}
}

// disableUser disables an active or pending user and never lifts a status.
func (p *restorePlan) disableUser(id UserID, epoch int64, derived bool) {
	u := p.users[id]
	if u == nil || p.deleted[id] || u.Status == StatusSystem {
		return
	}
	if epoch > u.CredentialEpoch {
		u.CredentialEpoch = epoch
		p.userDirty[id] = true
	}
	if u.Status == StatusDisabled {
		return
	}
	u.Status = StatusDisabled
	p.userDirty[id] = true
	p.count(CountUsersDisabled)
	p.owe(derived, journal.Revocation{Kind: journal.UserDisabled, Subject: ref(refKindUser, string(id)), Epoch: u.CredentialEpoch})
}

// carryStanding carries the pre-swap user's credential epoch and sign-in
// restrictions over the restored row, each the more restrictive of the two: a
// user who signed out everywhere, changed a password or had a key revoked
// after the archive must not get the older epoch back (a credential minted at
// the higher epoch stays refused), and a restriction set after the archive
// stays set. A raised epoch is journaled; the flags have no journal kind (the
// PasswordChanged line of a blanked hash is the one that marks a reset).
func (p *restorePlan) carryStanding(prev *User) {
	u := p.users[prev.ID]
	if u == nil || p.deleted[prev.ID] || u.Status == StatusSystem {
		return
	}
	if prev.CredentialEpoch > u.CredentialEpoch {
		u.CredentialEpoch = prev.CredentialEpoch
		p.userDirty[u.ID] = true
		p.owe(true, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: ref(refKindUser, string(u.ID)), Epoch: u.CredentialEpoch})
	}
	if prev.SignInRestricted && !u.SignInRestricted {
		u.SignInRestricted = true
		p.userDirty[u.ID] = true
		p.count(CountUsersRestricted)
	}
	if prev.PasswordResetRequired && !u.PasswordResetRequired {
		u.PasswordResetRequired = true
		p.userDirty[u.ID] = true
	}
}

// blankPassword blanks id's restored hash and requires a reset.
func (p *restorePlan) blankPassword(id UserID, epoch int64, derived bool) {
	u := p.users[id]
	if u == nil || p.deleted[id] || u.Status == StatusSystem {
		return
	}
	if epoch > u.CredentialEpoch {
		u.CredentialEpoch = epoch
	}
	if !p.blank[id] || !u.PasswordResetRequired {
		p.blank[id], u.PasswordResetRequired, u.HasPassword = true, true, false
		p.userDirty[id] = true
		p.count(CountPasswordsBlanked)
		p.owe(derived, journal.Revocation{Kind: journal.PasswordChanged, Subject: ref(refKindUser, string(id)), Epoch: u.CredentialEpoch})
	}
}

// unlink deletes identity link key.
func (p *restorePlan) unlink(key [2]string, derived bool) {
	id := p.idents[key]
	if id == nil || p.identDel[key] {
		return
	}
	p.identDel[key] = true
	p.count(CountIdentitiesUnlinked)
	p.owe(derived, journal.Revocation{
		Kind: journal.IdentityUnlinked, Subject: ref(refKindUser, string(id.UserID)), Detail: []string{id.Provider, id.Subject},
	})
}

// deleteUser deletes id and, at stage time, every credential it owns.
func (p *restorePlan) deleteUser(id UserID) {
	u := p.users[id]
	if u == nil || p.deleted[id] || u.Status == StatusSystem {
		return
	}
	p.deleted[id] = true
	p.count(CountUsersDeleted)
	for kid, k := range p.keys {
		if k.Owner == id {
			delete(p.keys, kid)
		}
	}
	for key, ident := range p.idents {
		if ident.UserID == id {
			delete(p.idents, key)
		}
	}
}

// reapply applies one journaled revocation. Role kinds are rbac's hook's;
// restore, gap and restore_aborted lines name no row.
func (p *restorePlan) reapply(ctx context.Context, r journal.Revocation) error {
	switch r.Kind {
	case journal.APIKeyRevoked:
		p.setKey(r.Subject.ID, KeyRevoked, false)
	case journal.APIKeyDisabled:
		p.setKey(r.Subject.ID, KeyDisabled, false)
	case journal.UserDisabled:
		p.disableUser(UserID(r.Subject.ID), r.Epoch, false)
	case journal.UserDeleted:
		p.deleteUser(UserID(r.Subject.ID))
	case journal.CredentialEpochBumped:
		if u := p.users[UserID(r.Subject.ID)]; u != nil && !p.deleted[u.ID] && r.Epoch > u.CredentialEpoch {
			u.CredentialEpoch = r.Epoch
			p.userDirty[u.ID] = true
		}
	case journal.PasswordChanged:
		p.blankPassword(UserID(r.Subject.ID), r.Epoch, false)
	case journal.IdentityUnlinked:
		// Detail is [provider] or [provider, subject]: a provider alone
		// names every link of the user at that provider.
		for key, id := range p.idents {
			switch {
			case len(r.Detail) == 0 || id.UserID != UserID(r.Subject.ID) || id.Provider != r.Detail[0]:
			case len(r.Detail) > 1 && id.Subject != r.Detail[1]:
			default:
				p.unlink(key, false)
			}
		}
	case journal.SessionRevoked:
		rec, err := p.d.Store.SessionByPublicID(ctx, r.Subject.ID)
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrUndecodableRecord):
		case err != nil:
			return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		default:
			p.sessions = append(p.sessions, rec)
			p.count(CountSessionsRevoked)
		}
	}
	return nil
}

// preSwap applies §4.14 step 3: the more restrictive of the restored and the
// pre-swap state, whatever the journal held.
func (p *restorePlan) preSwap(ctx context.Context) error {
	prev := p.d.Previous
	for k, err := range prev.ScanAPIKeys(ctx, "") {
		if err != nil {
			if leftUndecodable(p.d.log(), "apikey", "", err) {
				continue
			}
			return fmt.Errorf("%w: pre-swap keys: %w", ErrStoreUnavailable, err)
		}
		p.setKey(k.ID, k.Status, true)
	}
	seen := map[UserID]bool{}
	for u, err := range prev.ScanUsers(ctx) {
		if err != nil {
			if leftUndecodable(p.d.log(), "user", "", err) {
				continue
			}
			return fmt.Errorf("%w: pre-swap users: %w", ErrStoreUnavailable, err)
		}
		seen[u.ID] = true
		if p.users[u.ID] == nil || u.Status == StatusSystem {
			continue
		}
		if u.Status != StatusActive && u.Status != StatusPending {
			p.disableUser(u.ID, u.CredentialEpoch, true)
		}
		p.carryStanding(u)
		then, err := prev.PasswordHash(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("%w: pre-swap password: %w", ErrStoreUnavailable, err)
		}
		now, err := p.d.Store.PasswordHash(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("%w: restored password: %w", ErrStoreUnavailable, err)
		}
		if then != now {
			p.blankPassword(u.ID, u.CredentialEpoch, true)
		}
	}
	// A user the pre-swap store no longer holds was deleted there.
	for id, u := range p.users {
		if !seen[id] && u.Status != StatusSystem {
			p.disableUser(id, 0, true)
		}
	}
	kept := map[[2]string]bool{}
	for id, err := range prev.ScanIdentities(ctx, "") {
		if err != nil {
			if leftUndecodable(p.d.log(), "identity", "", err) {
				continue
			}
			return fmt.Errorf("%w: pre-swap identities: %w", ErrStoreUnavailable, err)
		}
		kept[[2]string{id.Provider, id.Subject}] = true
	}
	for key := range p.idents {
		if !kept[key] {
			p.unlink(key, true)
		}
	}
	return nil
}

// unconfirmKeys is the nil-coverage fallback: every restored key still active
// or disabled is refused until an admin re-enables it. A revoked key stays
// revoked (DATA4-13).
func (p *restorePlan) unconfirmKeys() {
	for _, id := range slices.Sorted(mapKeys(p.keys)) {
		if st := p.keys[id].Status; st == KeyActive || st == KeyDisabled {
			p.setKey(id, KeyNeedsConfirmation, true)
		}
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// restrictUser restricts id as §4.14's restricted last admin: password hash
// blanked with a reset required, keys needs_confirmation, sign-in restricted
// (every authenticator refuses it), external-identity links deleted. A
// revoked key stays revoked.
func (p *restorePlan) restrictUser(id UserID) error {
	u := p.users[id]
	if u == nil || p.deleted[id] {
		return fmt.Errorf("%w: user %s", ErrNotFound, id)
	}
	if u.Status == StatusSystem {
		return fmt.Errorf("%w: the system user %s cannot be restricted", ErrBadInput, id)
	}
	if !u.SignInRestricted {
		u.SignInRestricted = true
		p.userDirty[id] = true
		p.count(CountUsersRestricted)
	}
	p.blankPassword(id, 0, true)
	for _, kid := range slices.Sorted(mapKeys(p.keys)) {
		if k := p.keys[kid]; k.Owner == id && (k.Status == KeyActive || k.Status == KeyDisabled) {
			p.setKey(kid, KeyNeedsConfirmation, true)
		}
	}
	for key, ident := range p.idents {
		if ident.UserID == id {
			p.unlink(key, true)
		}
	}
	return nil
}

// stage writes the plan into b, journal lines first.
func (p *restorePlan) stage(ctx context.Context, b *storage.Batch) error {
	st := p.d.Store
	for _, r := range p.lines {
		p.d.line(ctx, b, r)
	}
	for _, id := range slices.Sorted(mapKeys(p.deleted)) {
		u, err := st.GetUser(ctx, id)
		if err != nil {
			return err
		}
		if err := stageDeleteCredentials(ctx, st, p.d.log(), b, id); err != nil {
			return err
		}
		if err := st.DeleteUser(b, u); err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(mapKeys(p.userDirty)) {
		if p.deleted[id] {
			continue
		}
		cur, err := st.GetUser(ctx, id)
		if err != nil {
			return err
		}
		up := *p.users[id]
		up.Version, up.UpdatedAt = cur.Version+1, p.now
		if p.blank[id] {
			err = st.SetPassword(b, &up, "")
		} else {
			err = st.PutUser(b, &up)
		}
		if err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(mapKeys(p.keyDirty)) {
		k := p.keys[id]
		if k == nil {
			continue
		}
		cur, err := st.APIKeyByID(ctx, id)
		if err != nil {
			return err
		}
		next := *k
		next.Version = cur.Version + 1
		if err := st.PutAPIKey(b, &next); err != nil {
			return err
		}
	}
	for key := range p.identDel {
		if id := p.idents[key]; id != nil {
			if err := st.DeleteIdentity(b, id); err != nil {
				return err
			}
		}
	}
	for _, rec := range p.sessions {
		if p.deleted[rec.UserID] {
			continue // the user's delete took it with the rest of its credentials
		}
		if err := st.DeleteSession(b, rec); err != nil {
			return err
		}
	}
	return nil
}

// PostRestore is auth's journal.RestoreHook body (§4.14, SEC-16), run in P4d
// after the journal re-apply and before any service is built. It
//
//  1. re-applies every rc.Revocations line (key revokes and disables, user
//     disables and deletes with their credentials, epoch bumps, session
//     revocations; a password.changed user has the restored hash blanked and
//     a reset required; an identity.unlinked link is deleted);
//  2. when d.Previous is set, takes the more restrictive of the restored and
//     the pre-swap state whatever the journal held, and, when rc.Revocations
//     is nil, sets every restored active or disabled key needs_confirmation
//     (a revoked key stays revoked);
//  3. deletes every session and one-time token and bumps every user's epoch.
//
// It is idempotent: a crash before the restore journal reaches "applied"
// re-runs it. The restrictions it derives itself (steps 2) are journaled so a
// later restore keeps them; the lines it re-applies are already journaled.
// It returns per-family counts; audit's hook records them.
func PostRestore(ctx context.Context, d RestoreDeps, rc journal.RestoreContext) (RestoreCounts, error) {
	if err := d.check("Revocations"); err != nil {
		return nil, err
	}
	var lines []journal.Revocation
	if rc.Revocations != nil {
		for r := range rc.Revocations {
			lines = append(lines, r)
		}
	}
	counts := RestoreCounts{}
	err := d.commit(ctx, func(b *storage.Batch) error {
		p, err := d.load(ctx)
		if err != nil {
			return err
		}
		for _, r := range lines {
			if err := p.reapply(ctx, r); err != nil {
				return err
			}
		}
		if d.Previous != nil {
			if err := p.preSwap(ctx); err != nil {
				return err
			}
		}
		if rc.Revocations == nil {
			p.unconfirmKeys()
		}
		if err := p.stage(ctx, b); err != nil {
			return err
		}
		counts = p.counts
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: post-restore: %w", err)
	}
	// Step 3 commits after the restrictions: a crash between the two leaves
	// them applied and the sign-ins to be deleted on the re-run.
	err = d.commit(ctx, func(b *storage.Batch) error {
		n, err := d.Store.RevokeSignInsBefore(b, time.Time{})
		if err != nil {
			return err
		}
		bumped := 0
		for u, err := range d.Store.ScanUsers(ctx) {
			if err != nil {
				if leftUndecodable(d.log(), "user", "", err) {
					continue
				}
				return err
			}
			if u.Status == StatusSystem {
				continue
			}
			up := *u
			up.CredentialEpoch, up.Version, up.UpdatedAt = u.CredentialEpoch+1, u.Version+1, d.now()
			if err := d.Store.PutUser(b, &up); err != nil {
				return err
			}
			bumped++
		}
		counts[CountSignInsDeleted], counts[CountEpochsBumped] = n, bumped
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: post-restore: invalidate sign-ins: %w", err)
	}
	return counts, nil
}

// RestrictLastAdmin restricts user id as §4.14's restricted last admin: the
// password hash blanked with a reset required, API keys needs_confirmation,
// SignInRestricted set so every authenticator (password, OIDC, identity
// extensions, compat) refuses it until an owner-verified reset link or the
// offline reset-password command clears it, and its external-identity links
// deleted; each restriction is journaled. rbac's restore hook, which decides
// that a re-applied demotion would remove the last active core:admin holder
// and keeps the role, calls it through the kernel's adapter (rbac cannot
// import auth). It is idempotent.
func RestrictLastAdmin(ctx context.Context, d RestoreDeps, id UserID) (RestoreCounts, error) {
	if err := d.check("Revocations"); err != nil {
		return nil, err
	}
	var counts RestoreCounts
	err := d.commit(ctx, func(b *storage.Batch) error {
		p, err := d.load(ctx)
		if err != nil {
			return err
		}
		if err := p.restrictUser(id); err != nil {
			return err
		}
		if err := p.stage(ctx, b); err != nil {
			return err
		}
		counts = p.counts
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: restrict last admin: %w", err)
	}
	return counts, nil
}
