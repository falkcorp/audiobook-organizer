// file: internal/database/pebble_store_auth.go
// version: 1.27.0
// guid: d9815a3d-0997-4c62-89a2-73f3c57e7fa9
// last-edited: 2026-10-30

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

var authLog = logger.New("database.auth")

// ErrUndecodableUserRows is returned by ListUsersStrict when one or more user
// rows could not be decoded. ListUsers skips such a row (logged); a caller
// that must account for EVERY user -- the user-state probe the hard deletes
// use (merge.UserStateProbe) -- uses ListUsersStrict and fails closed, since
// a user it cannot see may hold state on the book about to be deleted.
var ErrUndecodableUserRows = errors.New("database: undecodable user rows")

// StrictUserLister is ListUsers that fails instead of skipping an
// undecodable user row. Resolve it with AsCapability: it is not on Store.
type StrictUserLister interface {
	ListUsersStrict() ([]User, error)
}

// Users & Auth

// The email keys.
//
// idx:user:email:<lowercase email> -> user id caches the VERIFIED email: the
// addresses an SSO login may be auto-linked to by email (oauth.ResolveUser,
// through GetUserByVerifiedEmail). The answer itself is computed live from
// each account's stamped EmailSource (emailProven in
// pebble_store_verified_email_backfill.go); the key is kept in step with it
// so that a rollback to an older build, whose ResolveUser reads this key and
// nothing else, still sees only verified addresses. The paths that make an
// address verified write it in their own batch: CreateUser (first-run setup,
// bootstrap, the seeded system user, and an IdP login whose email the IdP
// verified -- CreateUser has no other callers, and a new caller must be one of
// those kinds), ConsumeInvite for an address an admin's invitation was emailed
// to, MarkUserEmailVerified for an IdP login, and SetUserEmailAsAdmin. A
// self-service email change (UpdateUser) never writes it; it only drops the
// key for the address the user gave up.
//
// Email uniqueness is enforced among PROVEN holders only: at most one account
// holds a given address proven (emailProven). Every path that makes an
// address proven (CreateUser, ConsumeInvite, SetUserEmailAsAdmin,
// MarkUserEmailVerified, CreateUserDisplacingUnverifiedEmail) refuses, or
// does nothing, when another account already holds it proven
// (provenHolderOtherLocked). A self-set address (UpdateUser) is never proven
// (emailProven trusts only the stamped source), so it is not checked at all:
// any account may type in any address, including one another account holds
// proven, and the copy links no SSO login and blocks nothing. Refusing it
// would let any signed-in user probe the profile form for which addresses
// have verified accounts, and would protect nothing.
const userEmailIndexPrefix = "idx:user:email:"

// provenHolderOtherLocked reports whether any user other than exceptID holds
// lower (already normalized) proven (emailProven). It sees the rows the
// verified lookup sees (verifiedHolderLocked): an unreadable user row is
// skipped and logged, as it is there. That row cannot be linked to by an SSO
// login while it is unreadable, and if it is ever repaired into a second
// proven holder, the lookup reports a conflict and links nobody. (The
// displacement paths are stricter, unprovenHoldersLocked: they rewrite other
// accounts' rows, so they refuse when any row is unreadable.) Caller holds
// inviteMu.
func (p *PebbleStore) provenHolderOtherLocked(lower, exceptID string) (bool, error) {
	id, err := p.provenHolderOtherIDLocked(lower, exceptID)
	return id != "", err
}

// provenHolderOtherIDLocked is provenHolderOtherLocked returning the id of
// the first other user that holds lower proven, or "" for none. Caller holds
// inviteMu.
func (p *PebbleStore) provenHolderOtherIDLocked(lower, exceptID string) (string, error) {
	if lower == "" {
		return "", nil
	}
	users, badKeys, err := p.listUsers()
	if err != nil {
		return "", err
	}
	if len(badKeys) > 0 {
		authLog.Warn("verified-email uniqueness check: %d user row(s) unreadable and skipped", len(badKeys))
	}
	for i := range users {
		if users[i].ID == exceptID {
			continue
		}
		if emailProven(&users[i], lower) {
			return users[i].ID, nil
		}
	}
	return "", nil
}

// emailHeldByAny reports whether any readable user row has lower (already
// normalized) as its current email, proven or not. ConsumeInvite and
// CreateUserDisplacingUnverifiedEmail use it only to skip the displacement
// pass when nobody holds the address.
func (p *PebbleStore) emailHeldByAny(lower string) (bool, error) {
	if lower == "" {
		return false, nil
	}
	users, _, err := p.listUsers()
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if util.NormalizeEmail(u.Email) == lower {
			return true, nil
		}
	}
	return false, nil
}

// CreateUser creates a user and records its email as verified (see the email
// keys above), so it is for trusted callers only. An email another user
// already holds proven is refused with an error wrapping ErrUserEmailTaken;
// accounts that only typed it in keep their unverified copy.
func (p *PebbleStore) CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error) {
	// inviteMu guards every email uniqueness check-then-write (CreateUser,
	// UpdateUser, CreateInvite, ConsumeInvite), so none of them can claim an
	// address between another's check and its write.
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	lowerUser := strings.ToLower(username)
	lowerEmail := util.NormalizeEmail(email)

	// uniqueness checks
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username already exists")
	}
	if taken, err := p.provenHolderOtherLocked(lowerEmail, ""); err != nil {
		return nil, err
	} else if taken {
		return nil, fmt.Errorf("email already verified for another account: %w", ErrUserEmailTaken)
	}

	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &User{
		ID: id, Username: username, Email: email,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: roles, Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if lowerEmail != "" {
		// Setup, bootstrap and the system user. An IdP auto-create upgrades
		// this to EmailSourceIdP through MarkUserEmailVerified right after.
		user.EmailSource = EmailSourceSetup
	}
	data, _ := json.Marshal(user)
	b := p.db.NewBatch()
	if err := b.Set([]byte("u:"+id), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if lowerEmail != "" {
		if err := b.Set([]byte(userEmailIndexPrefix+lowerEmail), []byte(id), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
}

// UnverifiedEmailDisplacer lets an identity-provider login take its verified
// address back from accounts that only typed it in. Not on Store; resolve it
// with AsCapability.
type UnverifiedEmailDisplacer interface {
	// CreateUserDisplacingUnverifiedEmail is CreateUser for a login whose
	// IdP-verified email other accounts hold unverified. In one batch it
	// clears the address from every such account (each keeps its account,
	// password and sessions, and loses only the address) and creates the
	// user with the address recorded as verified. It returns the new user
	// and the ids of the accounts that lost the address. Nothing is written,
	// and the error wraps ErrUserEmailTaken, when any holder has the address
	// proven (emailProven), or when any user row cannot be decoded (a
	// holder that cannot be read must not be skipped by the check).
	CreateUserDisplacingUnverifiedEmail(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, []string, error)
}

var _ UnverifiedEmailDisplacer = (*PebbleStore)(nil)

// CreateUserDisplacingUnverifiedEmail implements UnverifiedEmailDisplacer.
func (p *PebbleStore) CreateUserDisplacingUnverifiedEmail(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, []string, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock() // the holders' rows are rewritten
	defer userWriteMu.Unlock()
	lowerUser := strings.ToLower(username)
	lowerEmail := util.NormalizeEmail(email)
	if lowerEmail == "" {
		return nil, nil, fmt.Errorf("create user displacing unverified email: email required")
	}
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, nil, fmt.Errorf("username already exists")
	}
	// Only an address someone holds needs the strict scan: it refuses on an
	// unreadable row, which must not deny every first login when nobody
	// holds the address at all.
	var holders []User
	if held, err := p.emailHeldByAny(lowerEmail); err != nil {
		return nil, nil, err
	} else if held {
		if holders, err = p.unprovenHoldersLocked(lowerEmail); err != nil {
			return nil, nil, err
		}
	}
	now := time.Now()
	b := p.db.NewBatch()
	defer b.Close()
	displaced, err := clearEmailFromHolders(b, holders, now)
	if err != nil {
		return nil, nil, err
	}
	id, err := newULID()
	if err != nil {
		return nil, nil, err
	}
	user := &User{
		ID: id, Username: username, Email: email,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: roles, Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
		EmailSource: EmailSourceIdP,
	}
	data, err := json.Marshal(user)
	if err != nil {
		return nil, nil, err
	}
	if err := b.Set([]byte("u:"+id), data, nil); err != nil {
		return nil, nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		return nil, nil, err
	}
	if err := b.Set([]byte(userEmailIndexPrefix+lowerEmail), []byte(id), nil); err != nil {
		return nil, nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, nil, err
	}
	return user, displaced, nil
}

// unprovenHoldersLocked returns every user whose current email is lower, for
// a displacement. It refuses (wrapping ErrUserEmailTaken) when any holder has
// the address proven (emailProven), and when any user row cannot be
// decoded, since a holder that cannot be read must not be skipped. Caller
// holds inviteMu and userWriteMu.
func (p *PebbleStore) unprovenHoldersLocked(lower string) ([]User, error) {
	users, badKeys, err := p.listUsers()
	if err != nil {
		return nil, err
	}
	if len(badKeys) > 0 {
		return nil, fmt.Errorf("%w: %w: %d row(s) unreadable, so a holder of the address cannot be ruled out",
			ErrUserEmailTaken, ErrUndecodableUserRows, len(badKeys))
	}
	var holders []User
	for i := range users {
		u := users[i]
		if util.NormalizeEmail(u.Email) != lower {
			continue
		}
		if emailProven(&u, lower) {
			return nil, fmt.Errorf("email is proven for another account: %w", ErrUserEmailTaken)
		}
		holders = append(holders, u)
	}
	return holders, nil
}

// clearEmailFromHolders stages, in b, each holder losing its email (and its
// EmailSource); each keeps its account, password and sessions. It returns
// their ids. The holders came from unprovenHoldersLocked, so none has a
// verified-email key to drop.
func clearEmailFromHolders(b *GatedBatch, holders []User, now time.Time) ([]string, error) {
	var displaced []string
	for _, u := range holders {
		u.Email = ""
		u.EmailSource = ""
		u.UpdatedAt = now
		data, err := json.Marshal(u)
		if err != nil {
			return nil, err
		}
		if err := b.Set([]byte("u:"+u.ID), data, nil); err != nil {
			return nil, err
		}
		displaced = append(displaced, u.ID)
	}
	return displaced, nil
}

func (p *PebbleStore) GetUserByID(id string) (*User, error) {
	v, closer, err := p.db.Get([]byte("u:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var u User
	if err := json.Unmarshal(v, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (p *PebbleStore) getUserByIndex(idx string) (*User, error) {
	v, closer, err := p.db.Get([]byte(idx))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	id := string(v)
	return p.GetUserByID(id)
}

func (p *PebbleStore) GetUserByUsername(username string) (*User, error) {
	return p.getUserByIndex("idx:user:username:" + strings.ToLower(username))
}

// GetUserByVerifiedEmail returns the one user whose current email is email
// and proven (emailProven), or nil. It is computed from the user rows on
// every call (user counts are small), not read from the idx:user:email
// cache; it then repairs that cache key if it disagrees. A self-set address
// is never found here.
func (p *PebbleStore) GetUserByVerifiedEmail(email string) (*User, error) {
	lower := util.NormalizeEmail(email)
	if lower == "" {
		return nil, nil
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	u, err := p.verifiedHolderLocked(lower)
	if err != nil {
		return nil, err
	}
	if err := p.syncVerifiedEmailKeyLocked(lower, u); err != nil {
		// The answer stands; only the rollback cache is behind, and the next
		// lookup or store open repairs it.
		authLog.Warn("verified-email cache: repairing a key failed: %v", err)
	}
	return u, nil
}

// VerifiedEmailMarker records that an identity provider verified a user's
// current email. Not on Store; resolve it with AsCapability.
type VerifiedEmailMarker interface {
	// MarkUserEmailVerified records email as verified for userID when it is
	// the user's current email and no other account holds it proven, and sets
	// the user's EmailSource to EmailSourceIdP. Anything else is a no-op
	// (false, nil): it never takes an address from someone.
	MarkUserEmailVerified(userID, email string) (bool, error)
}

var _ VerifiedEmailMarker = (*PebbleStore)(nil)

// MarkUserEmailVerified implements VerifiedEmailMarker.
func (p *PebbleStore) MarkUserEmailVerified(userID, email string) (bool, error) {
	if p.emailAlreadyIdPVerified(userID, email) {
		return false, nil // the common repeat login: no locks needed
	}
	if p.emailProvenForOtherByKey(userID, email) {
		// Another account holds the address proven: a no-op, decided without
		// the locks or the full user scan, which every Cloudflare Access
		// request of a self-stamped account would otherwise pay.
		return false, nil
	}
	if markEmailVerifiedLockedHook != nil {
		markEmailVerifiedLockedHook()
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock() // the row's EmailSource is rewritten
	defer userWriteMu.Unlock()
	lower := util.NormalizeEmail(email)
	if lower == "" {
		return false, nil
	}
	u, err := p.GetUserByID(userID)
	if err != nil || u == nil || util.NormalizeEmail(u.Email) != lower {
		return false, err
	}
	// Another account holding the address proven wins, whatever the cache
	// key says: checking only the key let two accounts take turns rewriting
	// it, each login pointing it at itself.
	if taken, err := p.provenHolderOtherLocked(lower, userID); err != nil {
		return false, err
	} else if taken {
		return false, nil // someone else's verified address
	}
	key := []byte(userEmailIndexPrefix + lower)
	keyed := false
	if v, closer, err := p.db.Get(key); err == nil {
		keyed = string(v) == userID
		closer.Close()
	} else if err != pebble.ErrNotFound {
		return false, err
	}
	if keyed && u.EmailSource == EmailSourceIdP {
		return false, nil // already verified by an identity provider
	}
	b := p.db.NewBatch()
	defer b.Close()
	if !keyed {
		if err := b.Set(key, []byte(userID), nil); err != nil {
			return false, err
		}
	}
	if u.EmailSource != EmailSourceIdP {
		u.EmailSource = EmailSourceIdP
		u.UpdatedAt = time.Now()
		data, err := json.Marshal(u)
		if err != nil {
			return false, err
		}
		if err := b.Set([]byte("u:"+userID), data, nil); err != nil {
			return false, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// emailAlreadyIdPVerified is MarkUserEmailVerified's unlocked fast path: true
// when the cache key for email already names userID and the row already
// holds email with EmailSource idp, so there is nothing to write. Any doubt
// (a read error, a mismatch) returns false and the locked path decides.
func (p *PebbleStore) emailAlreadyIdPVerified(userID, email string) bool {
	lower := util.NormalizeEmail(email)
	if lower == "" {
		return false
	}
	v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + lower))
	if err != nil {
		return false
	}
	keyed := string(v) == userID
	closer.Close()
	if !keyed {
		return false
	}
	u, err := p.GetUserByID(userID)
	return err == nil && u != nil && u.EmailSource == EmailSourceIdP &&
		util.NormalizeEmail(u.Email) == lower
}

// emailProvenForOtherByKey is MarkUserEmailVerified's second unlocked fast
// path: true when the cache key for email names a different user and that
// user's own row holds email proven (emailProven). The answer is then a no-op
// whatever else is true, because another proven holder always wins, so the key
// is only a hint: the decision is the other row's own proof. A missing or
// stale key, a read error or an unproven row returns false and the locked path
// decides (it also covers two proven holders, where the cache has no key).
func (p *PebbleStore) emailProvenForOtherByKey(userID, email string) bool {
	lower := util.NormalizeEmail(email)
	if lower == "" {
		return false
	}
	v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + lower))
	if err != nil {
		return false
	}
	other := string(v)
	closer.Close()
	if other == "" || other == userID {
		return false
	}
	u, err := p.GetUserByID(other)
	return err == nil && u != nil && emailProven(u, lower)
}

// markEmailVerifiedLockedHook, when set (tests only), runs each time
// MarkUserEmailVerified falls through its fast paths to the locked path.
var markEmailVerifiedLockedHook func()

// userWriteMu serializes every rewrite of an existing user row (UpdateUser and
// the field-scoped credential ops below). Pebble has no compare-and-swap, so
// each op re-reads the row under this lock and changes only its own fields.
// Without it a write built from a copy read before a password change or a
// deactivation could land after it and put the old password, the old status or
// the old CredentialEpoch back.
var userWriteMu sync.Mutex

// sessionWriteMu serializes every read-modify-write of an existing sess: row:
// RevokeSession (read, set Revoked, write) and the keepSessionID re-stamp in
// mutateUserCredentials. Without it the two can interleave on one session, and
// the re-stamp's write (built from a copy read before the revoke) would put
// Revoked back to false: a lost update that resurrects a revoked session.
// CreateSession only writes a brand-new id and DeleteExpiredSessions only deletes,
// so neither needs it. Lock order: userWriteMu, then sessionWriteMu; nothing
// takes them the other way round.
var sessionWriteMu sync.Mutex

// userWriteAfterReadHook, when set, runs inside every user rewrite between the
// re-read and the write: the window userWriteMu exists to close. Tests use it to
// force a competing writer into exactly that window, so a missing lock fails
// deterministically instead of by chance. Nil (no-op) in production.
var userWriteAfterReadHook atomic.Pointer[func()]

func runUserWriteAfterReadHook() {
	if hook := userWriteAfterReadHook.Load(); hook != nil {
		(*hook)()
	}
}

// sameTimePtr compares two optional instants by value, not by pointer or
// location (a time that went through JSON comes back in another Location).
func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// ErrUserNotFound is returned by the field-scoped user ops for an unknown id.
var ErrUserNotFound = errors.New("database: user not found")

// ErrUserStale is returned by SetUserPassword when the stored password hash is
// no longer the one the caller verified: another change landed in between.
var ErrUserStale = errors.New("database: user changed since it was read")

// ErrUserProtectedField is returned by UpdateUser for a write that would change
// the password hash or algorithm, Status, CredentialEpoch, MergedIntoUserID or
// MergedAt. The first four change only through SetUserPassword, SetUserStatus
// and BumpUserCredentialEpoch, which bump the epoch on the fresh row; the merge
// fields only through RetireMergedUser and UnretireMergedUser. A caller that
// hits this either tried to change one of them through the whole-row write (a
// bug) or is writing back a copy read before one of those ops landed (re-read
// and retry).
var ErrUserProtectedField = errors.New("database: UpdateUser cannot change password, status, credential epoch or merge state")

// ErrUserEmailTaken is returned when an address is about to be made proven
// for an account while another account already holds it proven, or when an
// unreadable user row means that cannot be ruled out.
var ErrUserEmailTaken = errors.New("database: another account already has that email address verified")

// UpdateUser rewrites a user row's ordinary fields (email, roles, ...). Under
// userWriteMu it re-reads the stored row and refuses (ErrUserProtectedField) a
// write whose password hash, algorithm, Status, CredentialEpoch,
// MergedIntoUserID or MergedAt differs from it, so a whole-row write can never
// change, or revert, any of them. An unknown
// id is ErrUserNotFound.
//
// An email change is never refused for being another account's address,
// proven or not (see the email keys). A change never makes the new address
// verified: UpdateUser is the self-service path (PATCH /auth/me), so it
// never writes the verified-email key, and it drops the key for the address the
// user gave up, in the same batch as the row, when that key points here. (A
// self-set address that was verified would let anyone take over another
// person's first SSO login by typing their address into a profile.) The
// store owns User.EmailSource: an email change sets it to EmailSourceSelf,
// and otherwise the stored value is kept whatever the caller sent.
//
// inviteMu is taken first (lock order inviteMu, then userWriteMu): the
// proven-holder checks of the other email writers read this row under it, so
// this change cannot land between one of their checks and its write.
func (p *PebbleStore) UpdateUser(user *User) error {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	stored, err := p.GetUserByID(user.ID)
	if err != nil {
		return err
	}
	if stored == nil {
		// Users are created by CreateUser, which also writes the username and
		// email indexes; a write here would leave an unindexed row.
		return fmt.Errorf("%w: %s", ErrUserNotFound, user.ID)
	}
	if user.PasswordHash != stored.PasswordHash || user.PasswordHashAlgo != stored.PasswordHashAlgo ||
		user.Status != stored.Status || user.CredentialEpoch != stored.CredentialEpoch ||
		user.MergedIntoUserID != stored.MergedIntoUserID || !sameTimePtr(user.MergedAt, stored.MergedAt) {
		return fmt.Errorf("%w (user %s)", ErrUserProtectedField, user.ID)
	}
	oldEmail := util.NormalizeEmail(stored.Email)
	newEmail := util.NormalizeEmail(user.Email)
	// EmailSource is the store's to set: a self-service change makes it
	// "self", anything else keeps what is stored, whatever the caller sent.
	user.EmailSource = stored.EmailSource
	if newEmail != oldEmail {
		user.EmailSource = ""
		if newEmail != "" {
			user.EmailSource = EmailSourceSelf
		}
	}
	runUserWriteAfterReadHook()
	user.UpdatedAt = time.Now()
	data, _ := json.Marshal(user)
	if newEmail == oldEmail {
		return p.db.Set([]byte("u:"+user.ID), data, pebble.Sync)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte("u:"+user.ID), data, nil); err != nil {
		return err
	}
	if oldEmail != "" {
		if v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + oldEmail)); err == nil {
			pointsHere := string(v) == user.ID
			closer.Close()
			if pointsHere {
				if err := b.Delete([]byte(userEmailIndexPrefix+oldEmail), nil); err != nil {
					return err
				}
			}
		} else if err != pebble.ErrNotFound {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// AdminEmailSetter sets or clears another user's email on an admin's word.
// Not on Store; resolve it with AsCapability.
type AdminEmailSetter interface {
	// SetUserEmailAsAdmin sets userID's email to email ("" clears it) with
	// EmailSource admin, so the address counts as verified: the caller must
	// have checked that the acting user holds the admin role, who can
	// already give any address access directly. An address another account
	// holds proven is refused with ErrUserEmailTaken, even when it is already
	// this user's (re-setting it would make it proven for two accounts); clear
	// it there or merge the accounts first. Accounts that only typed it in
	// keep their unverified copy. An unknown id is ErrUserNotFound. It
	// returns the email the user had.
	SetUserEmailAsAdmin(userID, email string) (previous string, err error)
}

var _ AdminEmailSetter = (*PebbleStore)(nil)

// SetUserEmailAsAdmin implements AdminEmailSetter. In one batch it rewrites
// the row, drops the verified-email key for the old address when it names
// this user, and writes the key for the new one.
func (p *PebbleStore) SetUserEmailAsAdmin(userID, email string) (string, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	stored, err := p.GetUserByID(userID)
	if err != nil {
		return "", err
	}
	if stored == nil {
		return "", fmt.Errorf("%w: %s", ErrUserNotFound, userID)
	}
	previous := stored.Email
	email = strings.TrimSpace(email)
	oldLower := util.NormalizeEmail(stored.Email)
	newLower := util.NormalizeEmail(email)
	// Checked even when the address is unchanged: the write below makes it
	// proven for this user, so another proven holder must not exist.
	if taken, err := p.provenHolderOtherLocked(newLower, userID); err != nil {
		return "", err
	} else if taken {
		return "", ErrUserEmailTaken
	}
	runUserWriteAfterReadHook()
	stored.Email = email
	stored.EmailSource = ""
	if email != "" {
		stored.EmailSource = EmailSourceAdmin
	}
	stored.UpdatedAt = time.Now()
	data, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte("u:"+userID), data, nil); err != nil {
		return "", err
	}
	if oldLower != "" && oldLower != newLower {
		if v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + oldLower)); err == nil {
			pointsHere := string(v) == userID
			closer.Close()
			if pointsHere {
				if err := b.Delete([]byte(userEmailIndexPrefix+oldLower), nil); err != nil {
					return "", err
				}
			}
		} else if err != pebble.ErrNotFound {
			return "", err
		}
	}
	if newLower != "" {
		if err := b.Set([]byte(userEmailIndexPrefix+newLower), []byte(userID), nil); err != nil {
			return "", err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return "", err
	}
	return previous, nil
}

// mutateUserCredentials re-reads userID under userWriteMu, applies fn to the
// fresh row, bumps CredentialEpoch by one and writes it back. fn may veto the
// write by returning an error. Each call bumps once, so N racing calls from the
// same starting row end at N distinct epochs, and every session stamped with any
// earlier one is refused.
//
// keepSessionID, when not empty, names ONE web session of userID that survives
// the bump: its CredentialEpoch is re-stamped to the new epoch in the SAME batch
// as the user row, so no request can ever see the new epoch beside the old stamp
// (a parallel request in that gap would be refused and revoke the caller's own
// session). It is re-stamped only if it is a live session of userID that is
// current under the epoch being replaced: an unknown, revoked, expired,
// foreign or already-stale session is left alone, so the re-stamp can never
// launder a session the old epoch had already killed.
func (p *PebbleStore) mutateUserCredentials(userID, keepSessionID string, fn func(*User) error) (*User, error) {
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	stored, err := p.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("%w: %s", ErrUserNotFound, userID)
	}
	if err := fn(stored); err != nil {
		return nil, err
	}
	runUserWriteAfterReadHook()
	var keep *Session
	if keepSessionID != "" {
		sessionWriteMu.Lock() // held through the commit below: see sessionWriteMu
		defer sessionWriteMu.Unlock()
		s, err := p.GetSession(keepSessionID)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if s != nil && s.UserID == userID && !s.Revoked && now.Before(s.ExpiresAt) && s.CredentialsCurrent(stored) {
			keep = s
		}
	}
	stored.CredentialEpoch++
	stored.UpdatedAt = time.Now()
	data, _ := json.Marshal(stored)
	if keep == nil {
		if err := p.db.Set([]byte("u:"+userID), data, pebble.Sync); err != nil {
			return nil, err
		}
		return stored, nil
	}
	keep.CredentialEpoch = stored.CredentialEpoch
	sessData, _ := json.Marshal(keep)
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte("u:"+userID), data, nil); err != nil {
		return nil, err
	}
	if err := b.Set([]byte("sess:"+keep.ID), sessData, nil); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return stored, nil
}

// UserCredentialWriter is the only way to change a user's password, status or
// CredentialEpoch: each op re-reads the row under userWriteMu, changes only its
// own fields and bumps the epoch in the same write. Resolve it with
// AsCapability: it is deliberately not on Store (the Store width baseline only
// goes down), and a store without it cannot change credentials at all.
type UserCredentialWriter interface {
	SetUserPassword(userID, expectedHash, algo, hash, keepSessionID string) (*User, error)
	SetUserStatus(userID, status string) (*User, error)
	BumpUserCredentialEpoch(userID string) (*User, error)
}

// SetUserPassword sets a new password hash and bumps CredentialEpoch in one
// write, returning the updated row. A non-empty expectedHash makes it a
// compare-and-swap: the write happens only if the stored hash is still the one
// the caller verified the current password against, otherwise ErrUserStale. The
// self-service change passes it; an admin reset, which verifies no password,
// passes "".
//
// A non-empty keepSessionID is the caller's own web session on a self-service
// change: it is re-stamped with the new epoch in the same batch as the password
// (see mutateUserCredentials), so the person who changed their password stays
// signed in while every other web session and every ABS session of the account
// dies. An admin reset passes "": the admin's session belongs to another user.
func (p *PebbleStore) SetUserPassword(userID, expectedHash, algo, hash, keepSessionID string) (*User, error) {
	return p.mutateUserCredentials(userID, keepSessionID, func(u *User) error {
		if expectedHash != "" && u.PasswordHash != expectedHash {
			return fmt.Errorf("%w: password of %s changed since it was verified", ErrUserStale, userID)
		}
		u.PasswordHashAlgo, u.PasswordHash = algo, hash
		return nil
	})
}

// SetUserStatus sets Status and bumps CredentialEpoch in one write, returning
// the updated row. It bumps on every call, deactivation and reactivation alike:
// a reactivated account starts with no live ABS session even if a cleanup
// revoke failed somewhere along the way.
func (p *PebbleStore) SetUserStatus(userID, status string) (*User, error) {
	return p.mutateUserCredentials(userID, "", func(u *User) error {
		u.Status = status
		return nil
	})
}

// BumpUserCredentialEpoch ends every ABS and web session of userID without
// changing the password or status (the admin reset link), returning the updated
// row.
func (p *PebbleStore) BumpUserCredentialEpoch(userID string) (*User, error) {
	return p.mutateUserCredentials(userID, "", func(*User) error { return nil })
}

// UserPasswordRehasher re-stores a verified password under a stronger algorithm
// without a lost-update race. Resolve it with AsCapability: it is not on Store.
type UserPasswordRehasher interface {
	RehashUserPassword(userID, verifiedHash, algo, hash string) (bool, error)
}

// RehashUserPassword replaces the user's password hash with (algo, hash) only if
// the stored hash is still verifiedHash, the one the caller just checked the
// plaintext against, and reports whether it wrote. It re-reads under userWriteMu
// and changes only the two hash fields on the fresh row, so a concurrent password
// change (different hash: nothing written) or deactivation (other fields: kept)
// is never overwritten by the caller's older copy. It does not move
// CredentialEpoch: the password itself is unchanged.
func (p *PebbleStore) RehashUserPassword(userID, verifiedHash, algo, hash string) (bool, error) {
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	stored, err := p.GetUserByID(userID)
	if err != nil {
		return false, err
	}
	if stored == nil || stored.PasswordHash != verifiedHash {
		return false, nil
	}
	runUserWriteAfterReadHook()
	stored.PasswordHashAlgo = algo
	stored.PasswordHash = hash
	stored.UpdatedAt = time.Now()
	data, _ := json.Marshal(stored)
	if err := p.db.Set([]byte("u:"+userID), data, pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// ListUsers lists every user whose row decodes. An undecodable row is logged
// and skipped; ListUsersStrict reports it instead.
func (p *PebbleStore) ListUsers() ([]User, error) {
	users, bad, err := p.listUsers()
	if err != nil {
		return nil, err
	}
	for _, k := range bad {
		authLog.Warn("ListUsers: skipping undecodable user row %s", logger.SanitizeLogValue(k))
	}
	return users, nil
}

// ListUsersStrict is ListUsers, but any undecodable user row is an error
// (ErrUndecodableUserRows) naming how many and the first key. The decodable
// users are returned alongside it.
func (p *PebbleStore) ListUsersStrict() ([]User, error) {
	users, bad, err := p.listUsers()
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return users, fmt.Errorf("%w: %d row(s), first %s", ErrUndecodableUserRows, len(bad), bad[0])
	}
	return users, nil
}

// listUsers returns the decodable users and the keys of the rows that did
// not decode.
func (p *PebbleStore) listUsers() ([]User, []string, error) {
	prefix := []byte("u:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	var users []User
	var bad []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":idx:") || strings.Contains(key, ":username:") || strings.Contains(key, ":email:") {
			continue
		}
		var u User
		if err := json.Unmarshal(iter.Value(), &u); err != nil {
			bad = append(bad, key)
			continue
		}
		users = append(users, u)
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	return users, bad, nil
}

func (p *PebbleStore) GetRoleByID(id string) (*Role, error) {
	v, closer, err := p.db.Get([]byte("role:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var r Role
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// roleNameIndexKey is the name-index key for a role. CreateRole writes it,
// GetRoleByName reads it and DeleteRole removes it; all three must go through
// this one function so they cannot disagree. Until 2026-09-12 CreateRole wrote
// strings.ToLower(name) with no trim, so a padded name like " Editor " was
// stored under a key the trimming lookup and delete never reached.
//
// NormalizeString, not NormalizeAuthor: role names are not person names, and
// NormalizeAuthor collapses internal whitespace since 2026-09-12. Role keys
// keep plain trim+lowercase.
func roleNameIndexKey(name string) string {
	return "idx:role:name:" + util.NormalizeString(name)
}

func (p *PebbleStore) GetRoleByName(name string) (*Role, error) {
	v, closer, err := p.db.Get([]byte(roleNameIndexKey(name)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return p.GetRoleByID(string(v))
}

func (p *PebbleStore) ListRoles() ([]Role, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("role:"),
		UpperBound: []byte("role:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []Role
	for iter.First(); iter.Valid(); iter.Next() {
		// Skip name-index entries (separate prefix already, but be defensive).
		var r Role
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (p *PebbleStore) CreateRole(role *Role) (*Role, error) {
	// A whitespace-only name normalizes to "" and would claim the bare
	// "idx:role:name:" key, so it is rejected like an empty one.
	if role == nil || util.NormalizeString(role.Name) == "" {
		return nil, fmt.Errorf("role name required")
	}
	if role.ID == "" {
		// Seed roles use their name as ID; others get a ULID.
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		role.ID = id
	}
	// Uniqueness check on name. A read error fails closed: treating it as
	// "no such key" would let a second role claim the same name.
	nameKey := []byte(roleNameIndexKey(role.Name))
	existing, err := p.getValueCopy(nameKey)
	if err != nil {
		return nil, fmt.Errorf("role name uniqueness check: %w", err)
	}
	if existing != nil && string(existing) != role.ID {
		return nil, fmt.Errorf("role name already exists")
	}
	now := time.Now()
	if role.CreatedAt.IsZero() {
		role.CreatedAt = now
	}
	role.UpdatedAt = now
	if role.Version == 0 {
		role.Version = 1
	}
	data, err := json.Marshal(role)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("role:"+role.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set(nameKey, []byte(role.ID), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return role, nil
}

func (p *PebbleStore) UpdateRole(role *Role) error {
	if role == nil || role.ID == "" {
		return fmt.Errorf("role id required")
	}
	role.UpdatedAt = time.Now()
	role.Version++
	data, err := json.Marshal(role)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("role:"+role.ID), data, pebble.Sync)
}

func (p *PebbleStore) DeleteRole(id string) error {
	r, err := p.GetRoleByID(id)
	if err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	if r.IsSeed {
		return fmt.Errorf("cannot delete seed role %q", r.Name)
	}
	// Remove the name-index entry only if it points at this role. A row
	// written before 2026-09-12 with a padded name (" Editor ") sits under an
	// untrimmed key, and its normalized key ("editor") may belong to a
	// different role; deleting by key alone would take that role's entry.
	nameKey := []byte(roleNameIndexKey(r.Name))
	owner, err := p.getValueCopy(nameKey)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("role:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if owner != nil && string(owner) == id {
		if err := b.Delete(nameKey, nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) CreateAPIKey(key *APIKey) (*APIKey, error) {
	if key == nil || key.UserID == "" {
		return nil, fmt.Errorf("api key: user_id required")
	}
	if key.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		key.ID = id
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now()
	}
	if key.Status == "" {
		key.Status = "active"
	}
	data, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("apikey:"+key.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:apikey:user:"+key.UserID+":"+key.ID), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if key.TokenHash != "" {
		if err := b.Set([]byte("idx:apikey:hash:"+key.TokenHash), []byte(key.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return key, nil
}

func (p *PebbleStore) GetAPIKey(id string) (*APIKey, error) {
	v, closer, err := p.db.Get([]byte("apikey:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var k APIKey
	if err := json.Unmarshal(v, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

func (p *PebbleStore) GetAPIKeyByHash(hash string) (*APIKey, error) {
	v, closer, err := p.db.Get([]byte("idx:apikey:hash:" + hash))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keyID := string(v)
	closer.Close()
	return p.GetAPIKey(keyID)
}

func (p *PebbleStore) ListAPIKeysForUser(userID string) ([]APIKey, error) {
	// A missing record (the getter's (nil, nil)) is a stale index entry and is
	// skipped. An unreadable member is NOT skipped silently: the readable keys
	// come back together with an *UnreadableMembersError naming it (see that
	// type). An index-scan error is a hard failure with nil rows. Until
	// 2026-09-12 every member error was skipped and the scan's own read error
	// was never checked, so an unreadable member came back as a short list.
	prefix := "idx:apikey:user:" + userID + ":"
	var out []APIKey
	unreadable := &UnreadableMembersError{Op: "ListAPIKeysForUser " + userID}
	if err := forEachKeyInRange(p.db, []byte(prefix), []byte(prefix+"~"), func(key, _ []byte) error {
		keyID := strings.TrimPrefix(string(key), prefix)
		k, err := p.GetAPIKey(keyID)
		if err != nil {
			unreadable.add(keyID, fmt.Errorf("reading key %s: %w", keyID, err))
			return nil
		}
		if k != nil {
			out = append(out, *k)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, unreadable.orNil()
}

// ListAllAPIKeys returns every API key. An undecodable record is reported in
// an *UnreadableMembersError alongside the keys that did decode; an iterator
// error fails the call. Until 2026-09-12 an undecodable record was dropped
// silently and the iterator's own error was never checked, so a truncated scan
// looked like a complete one.
func (p *PebbleStore) ListAllAPIKeys() ([]APIKey, error) {
	const prefix = "apikey:"
	var out []APIKey
	unreadable := &UnreadableMembersError{Op: "ListAllAPIKeys"}
	if err := forEachKeyInRange(p.db, []byte(prefix), []byte(prefix+"~"), func(key, value []byte) error {
		var k APIKey
		if err := json.Unmarshal(value, &k); err != nil {
			id := strings.TrimPrefix(string(key), prefix)
			unreadable.add(id, fmt.Errorf("decoding key %s: %w", id, err))
			return nil
		}
		out = append(out, k)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ListAllAPIKeys: %w", err)
	}
	return out, unreadable.orNil()
}

func (p *PebbleStore) RevokeAPIKey(id string) error {
	return p.SetAPIKeyStatus(id, "revoked", time.Now())
}

func (p *PebbleStore) SetAPIKeyStatus(id, status string, at time.Time) error {
	k, err := p.GetAPIKey(id)
	if err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	k.Status = status
	switch status {
	case "revoked":
		k.RevokedAt = &at
	case "inactive":
		k.DeactivatedAt = &at
	case "active":
		k.DeactivatedAt = nil
	}
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("apikey:"+id), data, pebble.Sync)
}

// SetAPIKeyExpiry updates only the ExpiresAt field on an existing key,
// leaving Status (and idx: entries) untouched. Used by the one-time expiry
// stamp for legacy keys: the key gets a future ExpiresAt instead of being
// revoked, and the middleware expiry check retires it when that time passes.
func (p *PebbleStore) SetAPIKeyExpiry(id string, at time.Time) error {
	k, err := p.GetAPIKey(id)
	if err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	k.ExpiresAt = &at
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("apikey:"+id), data, pebble.Sync)
}

// TouchAPIKeyLastUsed stamps an API key's last-use metadata and increments its
// use counter.
//
// Serialized on apiKeyMu because the body is a read-modify-write: read the key,
// bump UseCount, write it back. Every authenticated request touches its key, so
// concurrent requests on the SAME key overlap constantly — unguarded, two
// requests both read UseCount=N and both write N+1, and one is lost. Measured
// before the lock: 200 concurrent touches recorded 37, losing 163 of them. The
// same window makes LastUsedAt/LastUsedIP the values of whichever goroutine
// wrote last rather than of the most recent request.
//
// A mutex rather than an atomic because the state is a whole record, not a
// single word (see the "shape of the state" rule in the Go standards); it is
// held across a Pebble read and a NoSync write, both memtable-speed.
func (p *PebbleStore) TouchAPIKeyLastUsed(id string, at time.Time, ip string) error {
	p.apiKeyMu.Lock()
	defer p.apiKeyMu.Unlock()

	k, err := p.GetAPIKey(id)
	if err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	k.LastUsedAt = &at
	k.LastUsedIP = ip
	k.UseCount++
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("apikey:"+id), data, pebble.NoSync)
}

// CreateInvite stores a new invite. An invite with an email is refused
// (ErrInviteEmailTaken) when another account holds that address proven:
// accepting it could never bind the address. An account that only typed it
// in does not block the invite; accepting a delivered admin invite takes the
// address from it (ConsumeInvite).
func (p *PebbleStore) CreateInvite(invite *Invite) (*Invite, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	if invite == nil || invite.Token == "" {
		return nil, fmt.Errorf("invite: token required")
	}
	if invite.Username == "" {
		return nil, fmt.Errorf("invite: username required")
	}
	if invite.RoleID == "" {
		return nil, fmt.Errorf("invite: role_id required")
	}
	if invite.CreatedAt.IsZero() {
		invite.CreatedAt = time.Now()
	}
	if invite.ExpiresAt.IsZero() {
		invite.ExpiresAt = invite.CreatedAt.Add(7 * 24 * time.Hour)
	}
	invite.Email = util.NormalizeEmail(invite.Email)
	if taken, err := p.provenHolderOtherLocked(invite.Email, ""); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrInviteEmailTaken
	}
	lower := strings.ToLower(invite.Username)
	if v, closer, err := p.db.Get([]byte("idx:invite:username:" + lower)); err == nil {
		existingToken := string(v)
		closer.Close()
		if existingToken != invite.Token {
			return nil, fmt.Errorf("invite already pending for username %q", invite.Username)
		}
	}
	data, err := json.Marshal(invite)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("invite:"+invite.Token), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:invite:username:"+lower), []byte(invite.Token), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return invite, nil
}

func (p *PebbleStore) GetInvite(token string) (*Invite, error) {
	v, closer, err := p.db.Get([]byte("invite:" + token))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var i Invite
	if err := json.Unmarshal(v, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

func (p *PebbleStore) ListActiveInvites() ([]Invite, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("invite:"),
		UpperBound: []byte("invite:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	now := time.Now()
	var out []Invite
	for iter.First(); iter.Valid(); iter.Next() {
		var i Invite
		if err := json.Unmarshal(iter.Value(), &i); err != nil {
			continue
		}
		if i.UsedAt != nil {
			continue
		}
		if now.After(i.ExpiresAt) {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

func (p *PebbleStore) DeleteInvite(token string) error {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	inv, err := p.GetInvite(token)
	if err != nil {
		return err
	}
	if inv == nil {
		return nil
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("invite:"+token), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:invite:username:"+strings.ToLower(inv.Username)), nil); err != nil {
		b.Close()
		return err
	}
	return b.Commit(pebble.Sync)
}

// inviteAcceptPlan is what accepting an invite would do: planInviteAcceptLocked
// decides it, ConsumeInvite applies it.
type inviteAcceptPlan struct {
	inv        *Invite // EmailBinding and SSOGrant decided
	lowerUser  string
	lowerEmail string // the email bound to the new account, if any
	grantSSO   bool
	displace   []User // accounts that typed the bound address in
}

// planInviteAcceptLocked decides, without writing, everything accepting token
// at now would do, or returns the refusal ConsumeInvite would return
// (IsInviteRefusal). Caller holds inviteMu; ConsumeInvite also holds
// userWriteMu, because it rewrites the displaced holders it reads here.
func (p *PebbleStore) planInviteAcceptLocked(token string, now time.Time) (*inviteAcceptPlan, error) {
	inv, err := p.GetInvite(token)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, ErrInviteNotFound
	}
	if inv.UsedAt != nil {
		return nil, ErrInviteUsed
	}
	if now.After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	lowerUser := strings.ToLower(inv.Username)
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("%w: %q", ErrInviteUsernameTaken, inv.Username)
	}
	inviteEmail := util.NormalizeEmail(inv.Email)
	lowerEmail := "" // the email bound to the new account, if any
	grantSSO := false
	var displace []User // accounts that typed the bound address in
	switch {
	case inviteEmail == "":
	case !inviteEmailDelivered(inv):
		inv.EmailBinding = InviteEmailNotEmailed
	default:
		creator, err := p.GetUserByID(inv.CreatedByUserID)
		if err != nil {
			return nil, err
		}
		// Trust assumption: whoever holds this token can accept it and
		// receive the address, verified, the SSO grant and the Cloudflare
		// guest entry. So the token of an invite with an email is shown
		// only to its creator (in the create response, a credential route
		// no API key reaches) and, in the pending list, only to an active
		// admin's own sign-in; the list never shows any token to an API
		// key, and revoke and resend take the invite's ID, not its token
		// (handlers.UserHandler inviteTokenVisible). The creator could
		// still accept it themselves, which gives them nothing new only
		// because binding requires the creator to be an admin
		// (InviteCreatorMayGrant), who can already set any user's email and
		// give any address access directly. A non-admin's invite binds
		// nothing. If binding is ever opened to lower roles, the create
		// response must stop carrying the token, or the token must be
		// rotated on send.
		if !InviteCreatorMayGrant(creator) {
			inv.EmailBinding = InviteEmailCreatorNotAdmin
			inv.SSOGrant = InviteSSOCreatorNotAdmin
			break
		}
		// Holding the token proves control of the inbox, as an IdP-verified
		// login does, so the same rule applies: accounts that only typed the
		// address in lose it, in this batch; a holder with the address
		// proven, or an unreadable user row, refuses the accept.
		if held, err := p.emailHeldByAny(inviteEmail); err != nil {
			return nil, err
		} else if held {
			holders, err := p.unprovenHoldersLocked(inviteEmail)
			if err != nil {
				if errors.Is(err, ErrUserEmailTaken) {
					return nil, fmt.Errorf("%w: %w", ErrInviteEmailTaken, err)
				}
				return nil, err
			}
			displace = holders
		}
		lowerEmail = inviteEmail
		inv.EmailBinding = InviteEmailBound
		grantSSO = true
		inv.SSOGrant = InviteSSOGranted
	}
	return &inviteAcceptPlan{inv: inv, lowerUser: lowerUser, lowerEmail: lowerEmail, grantSSO: grantSSO, displace: displace}, nil
}

// InviteAcceptPreview is what accepting an invite now would do that the public
// invitation page needs to describe. It is decided by the same plan ConsumeInvite
// applies, so the page cannot promise what accept will not do.
type InviteAcceptPreview struct {
	// GrantsSSO is true when accept would bind the invited address and grant it
	// SSO sign-in (and, where configured, the Cloudflare guest entry): the
	// invitation carries an email, it was delivered to that address and its
	// creator is an active admin.
	GrantsSSO bool
}

// InviteAcceptChecker answers whether an invite could be accepted now. Not on
// Store; resolve it with AsCapability.
type InviteAcceptChecker interface {
	// CheckInviteAcceptable returns the preview when accepting token now would
	// succeed as far as the store can tell, or the refusal ConsumeInvite would
	// return (IsInviteRefusal), or a storage error. It writes nothing.
	CheckInviteAcceptable(token string) (InviteAcceptPreview, error)
}

var _ InviteAcceptChecker = (*PebbleStore)(nil)

// CheckInviteAcceptable implements InviteAcceptChecker with the same
// planInviteAcceptLocked ConsumeInvite applies.
func (p *PebbleStore) CheckInviteAcceptable(token string) (InviteAcceptPreview, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	plan, err := p.planInviteAcceptLocked(token, time.Now())
	if err != nil {
		return InviteAcceptPreview{}, err
	}
	return InviteAcceptPreview{GrantsSSO: plan.grantSSO}, nil
}

// ConsumeInvite creates the invitee's user and marks the invite used, in one
// batch under inviteMu, so two concurrent accepts of one token cannot both
// pass the used check.
//
// What happens to the invite's email (all in the same batch) is decided by
// planInviteAcceptLocked, the same read-only function CheckInviteAcceptable
// runs for the public lookup, so the lookup and the accept cannot disagree:
//   - It is bound to the new account (user.Email plus the verified-email
//     key; refused if another user has it) only when the invitation was emailed to exactly
//     that address (inviteEmailDelivered). Otherwise the account is created
//     with no email and EmailBinding records why; an admin can set it later.
//   - A bound email also gets an SSO grant (InviteEmailGrantStore) only when
//     the invite's creator is still an active admin (InviteCreatorMayGrant).
//     A users.manage holder who is not an admin can invite, but cannot widen
//     who may sign in through SSO or Cloudflare Access.
func (p *PebbleStore) ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock() // displaced holders' rows are rewritten
	defer userWriteMu.Unlock()
	plan, err := p.planInviteAcceptLocked(token, time.Now())
	if err != nil {
		return nil, err
	}
	inv, lowerUser, lowerEmail, grantSSO, displace := plan.inv, plan.lowerUser, plan.lowerEmail, plan.grantSSO, plan.displace

	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &User{
		ID: id, Username: inv.Username, Email: lowerEmail,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: []string{inv.RoleID}, Status: "active",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if lowerEmail != "" {
		user.EmailSource = EmailSourceInvite
	}
	inv.UsedAt = &now
	inv.AcceptedUserID = id

	b := p.db.NewBatch()
	defer b.Close()
	displaced, err := clearEmailFromHolders(b, displace, now)
	if err != nil {
		return nil, err
	}
	inv.DisplacedUserIDs = displaced

	userData, err := json.Marshal(user)
	if err != nil {
		return nil, err
	}
	invData, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}

	if err := b.Set([]byte("u:"+id), userData, nil); err != nil {
		return nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		return nil, err
	}
	if lowerEmail != "" {
		// Delivered to this address by an admin, so holding the token proves
		// control of it: recorded as verified (see the email keys).
		if err := b.Set([]byte(userEmailIndexPrefix+lowerEmail), []byte(id), nil); err != nil {
			return nil, err
		}
	}
	if grantSSO {
		grant, err := json.Marshal(InviteEmailGrant{UserID: id, InviteToken: token, GrantedAt: now})
		if err != nil {
			return nil, err
		}
		if err := b.Set([]byte(inviteEmailGrantPrefix+lowerEmail), grant, nil); err != nil {
			return nil, err
		}
	}
	if err := b.Set([]byte("invite:"+token), invData, nil); err != nil {
		return nil, err
	}
	if err := b.Delete([]byte("idx:invite:username:"+lowerUser), nil); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
}

// Sessions
//
// credentialEpoch is the User.CredentialEpoch of the user record the caller
// verified this sign-in against (password check, token redemption, OAuth
// resolve): the caller's own copy, not a fresh read, so a credential change that
// lands between the check and this call leaves the session stamped with the old
// epoch and refused on its first use (Session.CredentialsCurrent).
func (p *PebbleStore) CreateSession(userID, ip, userAgent string, ttl time.Duration, credentialEpoch int64) (*Session, error) {
	return p.CreateSessionWithOrigin(userID, ip, userAgent, ttl, "", credentialEpoch)
}

// CreateSessionWithOrigin is CreateSession recording the session's origin.
func (p *PebbleStore) CreateSessionWithOrigin(userID, ip, userAgent string, ttl time.Duration, origin string, credentialEpoch int64) (*Session, error) {
	// The id is the session cookie's value: a credential, so not newULID.
	id, err := newUnlinkedULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(ttl), IP: ip, UserAgent: userAgent, Revoked: false, Version: 1, Origin: origin, CredentialEpoch: credentialEpoch}
	data, _ := json.Marshal(sess)
	b := p.db.NewBatch()
	if err := b.Set([]byte("sess:"+id), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:sess:user:"+userID+":"+id), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return sess, nil
}

func (p *PebbleStore) GetSession(id string) (*Session, error) {
	v, closer, err := p.db.Get([]byte("sess:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s Session
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *PebbleStore) RevokeSession(id string) error {
	sessionWriteMu.Lock() // the read-modify-write must not interleave with a re-stamp
	defer sessionWriteMu.Unlock()
	s, err := p.GetSession(id)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.Revoked = true
	data, _ := json.Marshal(s)
	return p.db.Set([]byte("sess:"+id), data, pebble.Sync)
}

func (p *PebbleStore) ListUserSessions(userID string) ([]Session, error) {
	prefix := []byte("idx:sess:user:" + userID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var res []Session
	for iter.First(); iter.Valid(); iter.Next() {
		sessID := strings.TrimPrefix(string(iter.Key()), "idx:sess:user:"+userID+":")
		s, err := p.GetSession(sessID)
		if err == nil && s != nil {
			res = append(res, *s)
		}
	}
	return res, nil
}

func (p *PebbleStore) DeleteExpiredSessions(now time.Time) (int, error) {
	prefix := []byte("sess:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	batch := p.db.NewBatch()
	defer batch.Close()

	deleted := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		value := append([]byte(nil), iter.Value()...)

		var sess Session
		if err := json.Unmarshal(value, &sess); err != nil {
			continue
		}
		if !sess.Revoked && sess.ExpiresAt.After(now) {
			continue
		}

		if err := batch.Delete(key, nil); err != nil {
			return deleted, err
		}
		if err := batch.Delete([]byte("idx:sess:user:"+sess.UserID+":"+sess.ID), nil); err != nil {
			return deleted, err
		}
		deleted++
	}

	if deleted == 0 {
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (p *PebbleStore) CountUsers() (int, error) {
	prefix := []byte("u:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count, nil
}
