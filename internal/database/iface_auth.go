// file: internal/database/iface_auth.go
// version: 1.1.0
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

// APIKeyReader looks up and lists API keys.
type APIKeyReader interface {
	GetAPIKey(id string) (*APIKey, error)
	GetAPIKeyByHash(hash string) (*APIKey, error)
	ListAPIKeysForUser(userID string) ([]APIKey, error)
	ListAllAPIKeys() ([]APIKey, error)
}

// APIKeyWriter issues new API keys.
type APIKeyWriter interface {
	CreateAPIKey(key *APIKey) (*APIKey, error)
}

// APIKeyLifecycleStore revokes keys and adjusts status/expiry, including the
// rotation grace window where an old key keeps working until a deadline.
type APIKeyLifecycleStore interface {
	RevokeAPIKey(id string) error
	SetAPIKeyStatus(id, status string, at time.Time) error
	// SetAPIKeyExpiry updates only the ExpiresAt field on an existing key,
	// leaving Status untouched. Used for the rotation grace window, where the
	// old key must keep working until `at` rather than being revoked
	// immediately (SEC-1/PROC-6).
	SetAPIKeyExpiry(id string, at time.Time) error
}

// APIKeyUsageRecorder records last-used telemetry for a key.
type APIKeyUsageRecorder interface {
	TouchAPIKeyLastUsed(id string, at time.Time, ip string) error
}

// APIKeyStore covers APIKey CRUD and revocation.
//
// Split into the 4 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type APIKeyStore interface {
	APIKeyReader
	APIKeyWriter
	APIKeyLifecycleStore
	APIKeyUsageRecorder
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
