// file: core/auth/user.go
// version: 1.0.0
// guid: a48fa1df-d1f3-442b-b579-d64c6afa13b2
// last-edited: 2026-11-06

// Package auth holds core's identity model. This file set is the leaf part
// that lands in release R1 with httpapi (05-authn §3, "Landing"): UserID,
// UserStatus, User, Method, Assurance, Principal, MethodSpec and the context
// plumbing, next to the rbac leaf types they name (rbac.Checker,
// rbac.RoleName). It imports only the standard library and rbac (same layer,
// L4). Store, Importer, Service, Identities, Admin, Compat, the
// authenticators and the native store land at F4; a member exists from the
// release that ships the package it names, and before that it is absent, not
// stubbed (06 §6.4.10).
package auth

import (
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

type UserID string // ULID

type UserStatus string

const (
	StatusActive   UserStatus = "active"
	StatusDisabled UserStatus = "disabled"
	StatusPending  UserStatus = "pending" // invited, not accepted
	StatusSystem   UserStatus = "system"  // _system attribution user; cannot authenticate
)

type User struct {
	ID              UserID
	Username        string
	Email           string
	EmailVerified   bool
	HasPassword     bool // a password hash is set (OIDC-only accounts: false)
	DisplayName     string
	Roles           []rbac.RoleName
	Status          UserStatus
	CredentialEpoch int64 // bumped on password change/reset, disable, enable, factory credential reset, restore
	// PasswordResetRequired: the stored hash may be one the user replaced after
	// an archive was taken (§4.14 step 1, SEC2-11). Password sign-in is refused
	// with 401 password_reset_required; the user signs in through a reset link.
	// The identity cutover sets it for a HEAD row with a blanked hash (§11).
	PasswordResetRequired bool
	// SignInRestricted: every authenticator — password, OIDC and identity
	// extensions — refuses this user (401 password_reset_required) until an
	// owner-verified reset link or the offline "<app> users reset-password"
	// clears it; set on a last admin a restore had to keep (05-rbac §7.1,
	// §4.14, SEC4-12).
	SignInRestricted bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Version          int64
}
