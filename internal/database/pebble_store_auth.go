// file: internal/database/pebble_store_auth.go
// version: 1.16.0
// guid: d9815a3d-0997-4c62-89a2-73f3c57e7fa9
// last-edited: 2026-10-19

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
// idx:user:email:<lowercase email> -> user id is the VERIFIED-email index: the
// addresses an SSO login may be auto-linked to by email (oauth.ResolveUser,
// through GetUserByVerifiedEmail). Only trusted paths write it: CreateUser
// (first-run setup, bootstrap, the seeded system user, and an IdP login whose
// email the IdP verified -- CreateUser has no other callers, and a new caller
// must be one of those kinds), ConsumeInvite for an address the invitation was
// emailed to, and MarkUserEmailVerified for an IdP login whose verified email
// equals the account's. A self-service email change (UpdateUser) never writes
// it; it only drops the key for the address the user gave up.
//
// The verified set keeps this key, rather than moving to a new prefix, so a
// rollback to an older build, whose ResolveUser reads this key, still sees only
// trusted addresses.
//
// Email uniqueness is not this index: User.Email is unique among all users,
// compared case-insensitively by scanning the rows (emailHeldByOther), because
// a self-set address has no key.
const userEmailIndexPrefix = "idx:user:email:"

// emailHeldByOther reports whether any user other than exceptID has lower
// (already lowercased and trimmed) as its current email. User counts are
// small, so this scans the rows instead of trusting an index.
func (p *PebbleStore) emailHeldByOther(lower, exceptID string) (bool, error) {
	if lower == "" {
		return false, nil
	}
	users, _, err := p.listUsers()
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.ID != exceptID && strings.EqualFold(strings.TrimSpace(u.Email), lower) {
			return true, nil
		}
	}
	return false, nil
}

// CreateUser creates a user and records its email as verified (see the email
// keys above), so it is for trusted callers only. An email another user
// already has is refused with an error wrapping ErrUserEmailTaken.
func (p *PebbleStore) CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error) {
	// inviteMu guards every email uniqueness check-then-write (CreateUser,
	// UpdateUser, CreateInvite, ConsumeInvite), so none of them can claim an
	// address between another's check and its write.
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	lowerUser := strings.ToLower(username)
	lowerEmail := strings.ToLower(strings.TrimSpace(email))

	// uniqueness checks
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username already exists")
	}
	if taken, err := p.emailHeldByOther(lowerEmail, ""); err != nil {
		return nil, err
	} else if taken {
		return nil, fmt.Errorf("email already exists: %w", ErrUserEmailTaken)
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
	// verified (the verified key) or proven (emailProvenFor: an invite grant
	// naming them, a linked identity of theirs recorded with the address, or
	// the operator's trusted list), or when any user row cannot be decoded (a
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
	lowerEmail := strings.ToLower(strings.TrimSpace(email))
	if lowerEmail == "" {
		return nil, nil, fmt.Errorf("create user displacing unverified email: email required")
	}
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, nil, fmt.Errorf("username already exists")
	}
	if v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + lowerEmail)); err == nil {
		holder := string(v)
		closer.Close()
		if u, err := p.GetUserByID(holder); err != nil {
			return nil, nil, err
		} else if u != nil && strings.EqualFold(strings.TrimSpace(u.Email), lowerEmail) {
			return nil, nil, fmt.Errorf("email is another account's verified email: %w", ErrUserEmailTaken)
		}
	} else if err != pebble.ErrNotFound {
		return nil, nil, err
	}
	users, badKeys, err := p.listUsers()
	if err != nil {
		return nil, nil, err
	}
	if len(badKeys) > 0 {
		return nil, nil, fmt.Errorf("%w: %w: %d row(s) unreadable, so a holder of the address cannot be ruled out",
			ErrUserEmailTaken, ErrUndecodableUserRows, len(badKeys))
	}
	var opSet map[string]bool
	if list, ok := p.currentOperatorEmails(); ok {
		opSet = operatorSet(list)
	}
	var holders []User
	for i := range users {
		u := users[i]
		if !strings.EqualFold(strings.TrimSpace(u.Email), lowerEmail) {
			continue
		}
		proven, err := p.emailProvenFor(&u, lowerEmail, opSet)
		if err != nil {
			return nil, nil, err
		}
		if proven {
			return nil, nil, fmt.Errorf("email is proven for another account: %w", ErrUserEmailTaken)
		}
		holders = append(holders, u)
	}
	now := time.Now()
	b := p.db.NewBatch()
	defer b.Close()
	var displaced []string
	for _, u := range holders {
		u.Email = ""
		u.EmailSource = ""
		u.UpdatedAt = now
		data, err := json.Marshal(u)
		if err != nil {
			return nil, nil, err
		}
		if err := b.Set([]byte("u:"+u.ID), data, nil); err != nil {
			return nil, nil, err
		}
		displaced = append(displaced, u.ID)
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

// GetUserByVerifiedEmail returns the user whose VERIFIED email is email (see
// the email keys above), or nil. A self-set address is never found here. The
// user must also still have that address, so a stale key finds nothing.
func (p *PebbleStore) GetUserByVerifiedEmail(email string) (*User, error) {
	lower := strings.ToLower(strings.TrimSpace(email))
	if lower == "" {
		return nil, nil
	}
	// Apply any change to the operator's trusted list first, so a removed
	// address stops resolving before this answers. Fails closed.
	if err := p.EnsureVerifiedEmailIndex(); err != nil {
		return nil, fmt.Errorf("verified-email index: %w", err)
	}
	u, err := p.getUserByIndex(userEmailIndexPrefix + lower)
	if err != nil || u == nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(u.Email), lower) {
		return nil, nil
	}
	return u, nil
}

// VerifiedEmailMarker records that an identity provider verified a user's
// current email. Not on Store; resolve it with AsCapability.
type VerifiedEmailMarker interface {
	// MarkUserEmailVerified records email as verified for userID when it is
	// the user's current email and no other user's verified email, and sets
	// the user's EmailSource to EmailSourceIdP. Anything else is a no-op
	// (false, nil): it never takes an address from someone.
	MarkUserEmailVerified(userID, email string) (bool, error)
}

var _ VerifiedEmailMarker = (*PebbleStore)(nil)

// MarkUserEmailVerified implements VerifiedEmailMarker.
func (p *PebbleStore) MarkUserEmailVerified(userID, email string) (bool, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock() // the row's EmailSource is rewritten
	defer userWriteMu.Unlock()
	lower := strings.ToLower(strings.TrimSpace(email))
	if lower == "" {
		return false, nil
	}
	u, err := p.GetUserByID(userID)
	if err != nil || u == nil || !strings.EqualFold(strings.TrimSpace(u.Email), lower) {
		return false, err
	}
	key := []byte(userEmailIndexPrefix + lower)
	keyed := false
	if v, closer, err := p.db.Get(key); err == nil {
		holder := string(v)
		closer.Close()
		if holder == userID {
			keyed = true
		} else if other, err := p.GetUserByID(holder); err != nil {
			return false, err
		} else if other != nil && strings.EqualFold(strings.TrimSpace(other.Email), lower) {
			return false, nil // someone else's verified address
		}
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

// userWriteMu serializes every rewrite of an existing user row (UpdateUser and
// the field-scoped credential ops below). Pebble has no compare-and-swap, so
// each op re-reads the row under this lock and changes only its own fields.
// Without it a write built from a copy read before a password change or a
// deactivation could land after it and put the old password, the old status or
// the old CredentialEpoch back.
var userWriteMu sync.Mutex

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

// ErrUserNotFound is returned by the field-scoped user ops for an unknown id.
var ErrUserNotFound = errors.New("database: user not found")

// ErrUserStale is returned by SetUserPassword when the stored password hash is
// no longer the one the caller verified: another change landed in between.
var ErrUserStale = errors.New("database: user changed since it was read")

// ErrUserProtectedField is returned by UpdateUser for a write that would change
// the password hash or algorithm, Status or CredentialEpoch. Those change only
// through SetUserPassword, SetUserStatus and BumpUserCredentialEpoch, which bump
// the epoch on the fresh row. A caller that hits this either tried to change
// one of them through the whole-row write (a bug) or is writing back a copy
// read before one of those ops landed (re-read and retry).
var ErrUserProtectedField = errors.New("database: UpdateUser cannot change password, status or credential epoch")

// ErrUserEmailTaken is returned by UpdateUser when another account already
// has the new email (compared case-insensitively).
var ErrUserEmailTaken = errors.New("database: another account already uses that email address")

// UpdateUser rewrites a user row's ordinary fields (email, roles, ...). Under
// userWriteMu it re-reads the stored row and refuses (ErrUserProtectedField) a
// write whose password hash, algorithm, Status or CredentialEpoch differs from
// it, so a whole-row write can never change, or revert, any of them. An unknown
// id is ErrUserNotFound.
//
// An email change is refused (ErrUserEmailTaken) when any other user's current
// email matches the new one case-insensitively. A change never makes the new
// address verified: UpdateUser is the self-service path (PATCH /auth/me), so it
// never writes the verified-email key, and it drops the key for the address the
// user gave up, in the same batch as the row, when that key points here. (A
// self-set address that was verified would let anyone take over another
// person's first SSO login by typing their address into a profile.) The
// store owns User.EmailSource: an email change sets it to EmailSourceSelf,
// and otherwise the stored value is kept whatever the caller sent.
//
// inviteMu is taken first (lock order inviteMu, then userWriteMu): it guards
// every email uniqueness check-then-write, so none of CreateUser, CreateInvite
// or ConsumeInvite can claim the address between this check and this write.
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
		user.Status != stored.Status || user.CredentialEpoch != stored.CredentialEpoch {
		return fmt.Errorf("%w (user %s)", ErrUserProtectedField, user.ID)
	}
	oldEmail := strings.ToLower(strings.TrimSpace(stored.Email))
	newEmail := strings.ToLower(strings.TrimSpace(user.Email))
	if newEmail != oldEmail {
		if taken, err := p.emailHeldByOther(newEmail, user.ID); err != nil {
			return err
		} else if taken {
			return ErrUserEmailTaken
		}
	}
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

// mutateUserCredentials re-reads userID under userWriteMu, applies fn to the
// fresh row, bumps CredentialEpoch by one and writes it back. fn may veto the
// write by returning an error. Each call bumps once, so N racing calls from the
// same starting row end at N distinct epochs, and every session stamped with any
// earlier one is refused.
func (p *PebbleStore) mutateUserCredentials(userID string, fn func(*User) error) (*User, error) {
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
	stored.CredentialEpoch++
	stored.UpdatedAt = time.Now()
	data, _ := json.Marshal(stored)
	if err := p.db.Set([]byte("u:"+userID), data, pebble.Sync); err != nil {
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
	SetUserPassword(userID, expectedHash, algo, hash string) (*User, error)
	SetUserStatus(userID, status string) (*User, error)
	BumpUserCredentialEpoch(userID string) (*User, error)
}

// SetUserPassword sets a new password hash and bumps CredentialEpoch in one
// write, returning the updated row. A non-empty expectedHash makes it a
// compare-and-swap: the write happens only if the stored hash is still the one
// the caller verified the current password against, otherwise ErrUserStale. The
// self-service change passes it; an admin reset, which verifies no password,
// passes "".
func (p *PebbleStore) SetUserPassword(userID, expectedHash, algo, hash string) (*User, error) {
	return p.mutateUserCredentials(userID, func(u *User) error {
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
	return p.mutateUserCredentials(userID, func(u *User) error {
		u.Status = status
		return nil
	})
}

// BumpUserCredentialEpoch ends every ABS session of userID without changing the
// password or status (the admin reset link), returning the updated row.
func (p *PebbleStore) BumpUserCredentialEpoch(userID string) (*User, error) {
	return p.mutateUserCredentials(userID, func(*User) error { return nil })
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

// CreateInvite stores a new invite. An invite with an email is refused when a
// user already has that email (ErrInviteEmailTaken): emails are unique, and
// accepting it would give a second account the same address.
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
	invite.Email = strings.ToLower(strings.TrimSpace(invite.Email))
	if taken, err := p.emailHeldByOther(invite.Email, ""); err != nil {
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

// ConsumeInvite creates the invitee's user and marks the invite used, in one
// batch under inviteMu, so two concurrent accepts of one token cannot both
// pass the used check.
//
// What happens to the invite's email (all in the same batch):
//   - It is bound to the new account (user.Email plus the verified-email
//     key; refused if another user has it) only when the invitation was emailed to exactly
//     that address (inviteEmailDelivered). Otherwise the account is created
//     with no email and EmailBinding records why; an admin can set it later.
//   - A bound email also gets an SSO grant (InviteEmailGrantStore) only when
//     the invite's creator is still an active admin (inviteCreatorMayGrant).
//     A users.manage holder who is not an admin can invite, but cannot widen
//     who may sign in through SSO or Cloudflare Access.
func (p *PebbleStore) ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	inv, err := p.GetInvite(token)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invite not found")
	}
	if inv.UsedAt != nil {
		return nil, fmt.Errorf("invite already used")
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("invite expired")
	}
	lowerUser := strings.ToLower(inv.Username)
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username %q taken since invite was created", inv.Username)
	}
	inviteEmail := strings.ToLower(strings.TrimSpace(inv.Email))
	lowerEmail := "" // the email bound to the new account, if any
	grantSSO := false
	switch {
	case inviteEmail == "":
	case !inviteEmailDelivered(inv):
		inv.EmailBinding = InviteEmailNotEmailed
	default:
		if taken, err := p.emailHeldByOther(inviteEmail, ""); err != nil {
			return nil, err
		} else if taken {
			return nil, ErrInviteEmailTaken
		}
		lowerEmail = inviteEmail
		inv.EmailBinding = InviteEmailBound
		creator, err := p.GetUserByID(inv.CreatedByUserID)
		if err != nil {
			return nil, err
		}
		// Trust assumption: the creator can read this invite's token (it is
		// in the create response and the pending list), so they could accept
		// it themselves and receive the grant for an address they do not
		// control. That gives them nothing new only because the grant is
		// limited to admins, who can already give any address access
		// directly. If grants are ever opened to lower roles, the token must
		// stop being shown for emailed invites, or be rotated on send.
		if inviteCreatorMayGrant(creator) {
			grantSSO = true
			inv.SSOGrant = InviteSSOGranted
		} else {
			inv.SSOGrant = InviteSSOCreatorNotAdmin
		}
	}

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

	userData, err := json.Marshal(user)
	if err != nil {
		return nil, err
	}
	invData, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}

	b := p.db.NewBatch()
	if err := b.Set([]byte("u:"+id), userData, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if lowerEmail != "" {
		// Delivered to this address, so holding the token proves control of
		// it: recorded as verified (see the email keys).
		if err := b.Set([]byte(userEmailIndexPrefix+lowerEmail), []byte(id), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if grantSSO {
		grant, err := json.Marshal(InviteEmailGrant{UserID: id, InviteToken: token, GrantedAt: now})
		if err != nil {
			b.Close()
			return nil, err
		}
		if err := b.Set([]byte(inviteEmailGrantPrefix+lowerEmail), grant, nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Set([]byte("invite:"+token), invData, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Delete([]byte("idx:invite:username:"+lowerUser), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
}

// Sessions
func (p *PebbleStore) CreateSession(userID, ip, userAgent string, ttl time.Duration) (*Session, error) {
	return p.CreateSessionWithOrigin(userID, ip, userAgent, ttl, "")
}

// CreateSessionWithOrigin is CreateSession recording the session's origin.
func (p *PebbleStore) CreateSessionWithOrigin(userID, ip, userAgent string, ttl time.Duration, origin string) (*Session, error) {
	// The id is the session cookie's value: a credential, so not newULID.
	id, err := newUnlinkedULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(ttl), IP: ip, UserAgent: userAgent, Revoked: false, Version: 1, Origin: origin}
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
