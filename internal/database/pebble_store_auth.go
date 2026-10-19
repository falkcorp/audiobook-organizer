// file: internal/database/pebble_store_auth.go
// version: 1.9.0
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
func (p *PebbleStore) CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error) {
	lowerUser := strings.ToLower(username)
	lowerEmail := strings.ToLower(email)

	// uniqueness checks
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username already exists")
	}
	if _, closer, err := p.db.Get([]byte("idx:user:email:" + lowerEmail)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("email already exists")
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
	if err := b.Set([]byte("idx:user:email:"+lowerEmail), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
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

func (p *PebbleStore) GetUserByEmail(email string) (*User, error) {
	return p.getUserByIndex("idx:user:email:" + strings.ToLower(email))
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

// UpdateUser rewrites a user row's ordinary fields (email, roles, ...). Under
// userWriteMu it re-reads the stored row and refuses (ErrUserProtectedField) a
// write whose password hash, algorithm, Status or CredentialEpoch differs from
// it, so a whole-row write can never change, or revert, any of them. An unknown
// id is ErrUserNotFound.
func (p *PebbleStore) UpdateUser(user *User) error {
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
	runUserWriteAfterReadHook()
	user.UpdatedAt = time.Now()
	data, _ := json.Marshal(user)
	return p.db.Set([]byte("u:"+user.ID), data, pebble.Sync)
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

func (p *PebbleStore) CreateInvite(invite *Invite) (*Invite, error) {
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

func (p *PebbleStore) ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error) {
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

	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &User{
		ID: id, Username: inv.Username, Email: inv.Email,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: []string{inv.RoleID}, Status: "active",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	inv.UsedAt = &now

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
	if inv.Email != "" {
		if err := b.Set([]byte("idx:user:email:"+strings.ToLower(inv.Email)), []byte(id), nil); err != nil {
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
