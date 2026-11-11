// file: core/auth/store.go
// version: 1.0.0
// guid: c69ddd70-3f1b-4605-9604-d1be29ba81d9
// last-edited: 2026-11-11

package auth

import (
	"context"
	"iter"
	"net/netip"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Store is the identity store (05-authn §3.4). It is SDK, frozen by the
// API-stability gate, because other apps and coretest build on it. Core's
// implementation, core/internal/authstore, keeps every record in the core
// keyspace c/auth/ (§11 "Native store"), and it is the only implementation the
// kernel builds. A credential crosses the interface as the raw presented
// value: the store mints mcs_/mck_/mcb_/mci_/mct_ values and keeps only their
// SHA-256 (§4.1). Core enforces its invariants above the interface:
// never-empty key scopes, status, epoch and expiry checks, dominance.
// auth/authtest.StoreSuite is the conformance suite. Every write takes the
// caller's *storage.Batch, never a copy, so the staged writes commit with the
// journal line and audit record (COR2-12). Single use and lost-update safety:
// the store stages Expect(key, seen) plus Delete for every Take* over a
// Serialized family, and Expect on the bytes it read for every write derived
// from a record (DATA4-9, SEC6-3, DATA6-8, R6-5), so a write built from a
// stale read fails the commit with storage.ErrConflict instead of overwriting
// a newer record.
//
// The spec prints Store as one interface of 25 methods; core CI refuses an
// interface declaring more than 8 entries (interface-width), so Store embeds
// one interface per record family. The method set is the spec's, plus the
// members F4-02 coins (seams.d/F4-02.md): DeleteUser, GuardUser,
// ScanIdentities, ScanOneTime and DeleteOneTime.
//
// A lookup of an absent record returns an error wrapping ErrNotFound.
type Store interface {
	UserStore
	SessionStore
	APIKeyStore
	OneTimeStore
	IdentityStore
	// StageMerge rewrites the owner of every record of plan (Admin.StageUserMerge).
	StageMerge(b *storage.Batch, plan UserMergePlan) error
}

// UserStore is the user family of Store.
type UserStore interface {
	// PutUser stages u. u.Version must be the stored Version + 1, or 1 for a
	// user that is not stored. The password hash is kept as stored unless
	// SetPassword staged a new one; PutUser never changes it.
	PutUser(b *storage.Batch, u *User) error
	// SetPassword stages u with the PHC hash given ("" = no password), with
	// the same version rule as PutUser.
	SetPassword(b *storage.Batch, u *User, hash string) error
	// DeleteUser stages the deletion of u and its username index. u.Version
	// must equal the stored version.
	DeleteUser(b *storage.Batch, u *User) error
	// GuardUser stages an Expect that u is still stored at u.Version, so the
	// batch fails with storage.ErrConflict when the user changed first (the
	// last-admin rule reads other users through it, 05-rbac §9).
	GuardUser(b *storage.Batch, u *User) error
	GetUser(ctx context.Context, id UserID) (*User, error)
	// PasswordHash returns the stored PHC hash of id ("" when none).
	PasswordHash(ctx context.Context, id UserID) (string, error)
	UserByUsername(ctx context.Context, fold string) (*User, error)
	ScanUsers(ctx context.Context) iter.Seq2[*User, error] // an undecodable stored record yields ErrUndecodableRecord for that record only
}

// SessionStore is the session family of Store (§4.3).
type SessionStore interface {
	IssueSession(b *storage.Batch, s *SessionRecord) (token Token, err error) // mints the token and stages the record
	SessionByToken(ctx context.Context, token Token) (*SessionRecord, error)
	SessionByPublicID(ctx context.Context, id string) (*SessionRecord, error)
	ScanUserSessions(ctx context.Context, u UserID) iter.Seq2[*SessionRecord, error]
	// DeleteSession stages the deletion of s, with an Expect on the stored
	// bytes; an absent session stages nothing.
	DeleteSession(b *storage.Batch, s *SessionRecord) error
	// TouchSession records a session's last use: it changes only LastSeen on
	// the current record and stages nothing when the record is absent,
	// expired or epoch-stale, so a flush after a logout or a revoke never
	// re-creates the session (DATA6-8).
	TouchSession(b *storage.Batch, publicID string, at time.Time) error
	// RestampSession stages s with CredentialEpoch epoch (the self-service
	// password change keeps the caller's own session, §4.12).
	RestampSession(b *storage.Batch, s *SessionRecord, epoch int64) error
	// RevokeSignInsBefore deletes every session and one-time token created
	// before t (zero t: all) (§4.14 step 2).
	RevokeSignInsBefore(b *storage.Batch, t time.Time) (int, error)
}

// APIKeyStore is the API-key family of Store (§4.4).
type APIKeyStore interface {
	IssueKey(b *storage.Batch, k *APIKey) (raw Token, err error) // mints an mck_ key
	PutAPIKey(b *storage.Batch, k *APIKey) error                 // status, expiry, name
	// TouchAPIKey records a key's last use (last_used_at, last_used_ip,
	// use_count), changes only those fields and stages nothing for an absent,
	// revoked, disabled, unconfirmed or expired key; it never undoes a
	// revocation (§4.4; SEC6-3, DATA6-8, resolution R6-5).
	TouchAPIKey(b *storage.Batch, id string, at time.Time, ip netip.Addr) error
	APIKeyByToken(ctx context.Context, raw Token) (*APIKey, error)
	APIKeyByID(ctx context.Context, id string) (*APIKey, error)
	ScanAPIKeys(ctx context.Context, owner UserID) iter.Seq2[*APIKey, error] // owner "" = every key
	// DeleteAPIKey stages the deletion of k (a deleted user's keys).
	DeleteAPIKey(b *storage.Batch, k *APIKey) error
}

// OneTimeStore is the one-time token family of Store: invite, temp-login,
// bootstrap.
type OneTimeStore interface {
	IssueOneTime(b *storage.Batch, t *OneTimeToken) (token Token, err error)
	TakeOneTime(b *storage.Batch, kind OneTimeKind, token Token) (*OneTimeToken, error) // single use (above)
	// LookupOneTime reads a token without spending it (invite lookup).
	LookupOneTime(ctx context.Context, kind OneTimeKind, token Token) (*OneTimeToken, error)
	ScanOneTime(ctx context.Context, kind OneTimeKind) iter.Seq2[*OneTimeToken, error]
	// DeleteOneTime stages the deletion of the token of kind whose public ID
	// is id, with an Expect on its stored bytes.
	DeleteOneTime(b *storage.Batch, kind OneTimeKind, id string) error
}

// IdentityStore is the external-identity and invite allow-list family of
// Store (§4.7).
type IdentityStore interface {
	PutIdentity(b *storage.Batch, id *ExternalIdentity) error
	DeleteIdentity(b *storage.Batch, id *ExternalIdentity) error
	IdentityBySubject(ctx context.Context, provider, subject string) (*ExternalIdentity, error)
	ScanIdentities(ctx context.Context, user UserID) iter.Seq2[*ExternalIdentity, error] // user "" = every identity
	InviteAllowed(ctx context.Context, email string) (bool, error)
}

// Importer is how an app's cutover step brings the identities it held before
// core into the native store, once, in the release where core auth takes over
// (ABO: step M3 of R2, §11). core/internal/authstore implements it. Every
// Import* keeps the record's ID, so app rows that reference a user or a key
// stay valid, and refuses a record that breaks a Store invariant (an unknown
// status, a key with no scopes, a key or identity whose user was not
// imported) with ErrImportRejected. There is no Import for sessions or
// one-time tokens: a cutover deletes them (§11).
type Importer interface {
	// ImportUser stores u with its stored password hash as it is (PHC
	// argon2id, bcrypt, or "" for an account without a password; §4.5) and
	// its credential epoch, so passwords and epoch-bound app tokens (ABS)
	// keep working.
	ImportUser(b *storage.Batch, u *User, passwordHash string) error
	// ImportAPIKey stores k under the SHA-256 of its raw value, which the
	// app's rows already hold, so the key verifies without being re-issued
	// (§4.1). k.Scopes are native and non-empty; status, expiry, name, owner
	// and last use are carried over.
	ImportAPIKey(b *storage.Batch, k *APIKey, tokenSHA256 [32]byte) error
	ImportIdentity(b *storage.Batch, id *ExternalIdentity) error
	ImportInviteAllow(b *storage.Batch, email string) error
}
