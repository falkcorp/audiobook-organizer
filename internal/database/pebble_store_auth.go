// file: internal/database/pebble_store_auth.go
// version: 1.0.0
// guid: d9815a3d-0997-4c62-89a2-73f3c57e7fa9
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

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

func (p *PebbleStore) UpdateUser(user *User) error {
	user.UpdatedAt = time.Now()
	data, _ := json.Marshal(user)
	return p.db.Set([]byte("u:"+user.ID), data, pebble.Sync)
}

func (p *PebbleStore) ListUsers() ([]User, error) {
	prefix := []byte("u:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var users []User
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":idx:") || strings.Contains(key, ":username:") || strings.Contains(key, ":email:") {
			continue
		}
		var u User
		if err := json.Unmarshal(iter.Value(), &u); err != nil {
			continue
		}
		users = append(users, u)
	}
	return users, nil
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

func (p *PebbleStore) GetRoleByName(name string) (*Role, error) {
	lower := util.NormalizeAuthor(name)
	v, closer, err := p.db.Get([]byte("idx:role:name:" + lower))
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
	if role == nil || role.Name == "" {
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
	// Uniqueness check on name.
	lower := strings.ToLower(role.Name)
	if existing, closer, err := p.db.Get([]byte("idx:role:name:" + lower)); err == nil {
		closer.Close()
		if string(existing) != role.ID {
			return nil, fmt.Errorf("role name already exists")
		}
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
	if err := b.Set([]byte("idx:role:name:"+lower), []byte(role.ID), nil); err != nil {
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
	b := p.db.NewBatch()
	if err := b.Delete([]byte("role:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:role:name:"+util.NormalizeAuthor(r.Name)), nil); err != nil {
		b.Close()
		return err
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
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("idx:apikey:user:" + userID + ":"),
		UpperBound: []byte("idx:apikey:user:" + userID + ":~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []APIKey
	prefix := "idx:apikey:user:" + userID + ":"
	for iter.First(); iter.Valid(); iter.Next() {
		keyID := strings.TrimPrefix(string(iter.Key()), prefix)
		k, err := p.GetAPIKey(keyID)
		if err != nil || k == nil {
			continue
		}
		out = append(out, *k)
	}
	return out, nil
}

func (p *PebbleStore) ListAllAPIKeys() ([]APIKey, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("apikey:"),
		UpperBound: []byte("apikey:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []APIKey
	for iter.First(); iter.Valid(); iter.Next() {
		var k APIKey
		if err := json.Unmarshal(iter.Value(), &k); err != nil {
			continue
		}
		out = append(out, k)
	}
	return out, nil
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
// leaving Status (and idx: entries) untouched. Used by the rotation grace
// window (SEC-1/PROC-6): the old key gets a short future ExpiresAt instead
// of being revoked outright, so the pre-existing middleware expiry check
// retires it naturally after the grace period.
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

func (p *PebbleStore) TouchAPIKeyLastUsed(id string, at time.Time, ip string) error {
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
	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(ttl), IP: ip, UserAgent: userAgent, Revoked: false, Version: 1}
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
