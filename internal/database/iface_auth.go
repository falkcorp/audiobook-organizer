// file: internal/database/iface_auth.go
// version: 1.0.0
// guid: 8be16e9f-22a9-45ca-a13c-1cca96721f90
// last-edited: 2026-08-18

package database

import (
	"time"
)

// Authentication and authorization records.
//
// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// APIKeyStore covers APIKey CRUD and revocation.
type APIKeyStore interface {
	CreateAPIKey(key *APIKey) (*APIKey, error)
	GetAPIKey(id string) (*APIKey, error)
	GetAPIKeyByHash(hash string) (*APIKey, error)
	ListAPIKeysForUser(userID string) ([]APIKey, error)
	ListAllAPIKeys() ([]APIKey, error)
	RevokeAPIKey(id string) error
	SetAPIKeyStatus(id, status string, at time.Time) error
	// SetAPIKeyExpiry updates only the ExpiresAt field on an existing key,
	// leaving Status untouched. Used for the rotation grace window, where the
	// old key must keep working until `at` rather than being revoked
	// immediately (SEC-1/PROC-6).
	SetAPIKeyExpiry(id string, at time.Time) error
	TouchAPIKeyLastUsed(id string, at time.Time, ip string) error
}

// RoleStore covers Role CRUD.
type RoleStore interface {
	GetRoleByID(id string) (*Role, error)
	GetRoleByName(name string) (*Role, error)
	ListRoles() ([]Role, error)
	CreateRole(role *Role) (*Role, error)
	UpdateRole(role *Role) error
	DeleteRole(id string) error
}

// SessionStore covers authenticated session CRUD.
type SessionStore interface {
	CreateSession(userID, ip, userAgent string, ttl time.Duration) (*Session, error)
	GetSession(id string) (*Session, error)
	RevokeSession(id string) error
	ListUserSessions(userID string) ([]Session, error)
	DeleteExpiredSessions(now time.Time) (int, error)
}

// InviteStore covers Invite CRUD and atomic consume.
type InviteStore interface {
	CreateInvite(invite *Invite) (*Invite, error)
	GetInvite(token string) (*Invite, error)
	ListActiveInvites() ([]Invite, error)
	DeleteInvite(token string) error
	ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error)
}
