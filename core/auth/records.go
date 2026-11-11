// file: core/auth/records.go
// version: 1.2.0
// guid: 056d2e0c-400d-404d-99ff-15b759dd0403
// last-edited: 2026-11-11

package auth

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// FoldUsername is the form a username is unique under and looked up by:
// lower-cased, nothing else. It is HEAD's idx:user:username: key
// (internal/books/store/pebble_store_auth.go CreateUser), kept exactly so an
// imported user is found under the name it signed in with before the cutover.
func FoldUsername(name string) string { return strings.ToLower(name) }

// SessionOrigin says how a session was opened (05-authn §3.4).
type SessionOrigin string

const (
	OriginPassword   SessionOrigin = "password"
	OriginTempLogin  SessionOrigin = "delegated:temp_login"
	OriginInvite     SessionOrigin = "delegated:invite"
	originOIDCPrefix               = "oidc:"
)

// OriginOIDC is the origin of a session an OIDC or OAuth sign-in opened.
func OriginOIDC(provider string) SessionOrigin { return SessionOrigin(originOIDCPrefix + provider) }

// Method is the authentication method a session of this origin carries: the
// user's own sign-in (password, OIDC) is MethodSession, a delegated session
// (temp-login, invite) or one of unknown origin is MethodSessionDelegated
// (§3.1, fail closed).
func (o SessionOrigin) Method() Method {
	s := string(o)
	if o == OriginPassword || (len(s) > len(originOIDCPrefix) && s[:len(originOIDCPrefix)] == originOIDCPrefix) {
		return MethodSession
	}
	return MethodSessionDelegated
}

// SessionRecord is one stored session (§3.4). The token never appears in it:
// the store keeps the token's SHA-256 beside the record, not in this type.
type SessionRecord struct {
	PublicID        string        // ULID, safe to show and used for revoke
	UserID          UserID        //
	Origin          SessionOrigin // password, oidc:<provider>, delegated:temp_login, delegated:invite
	CredentialEpoch int64
	CreatedAt       time.Time
	ExpiresAt       time.Time
	LastSeen        time.Time
	ClientAddr      netip.Addr
	UserAgent       string
	Version         int64
}

// IssuedSession is a freshly opened session: the raw token is returned once
// (§3.2, §4.1).
type IssuedSession struct {
	Token     string // "mcs_...", returned once (§4.1)
	PublicID  string // ULID, safe to show, used for revoke
	ExpiresAt time.Time
}

// SessionView is what a session listing shows (§3.2): never a token, never a
// hash (TestSessions_ListNeverReturnsToken).
type SessionView struct {
	PublicID, UserAgent            string
	ClientAddr                     netip.Addr
	Origin                         SessionOrigin
	CreatedAt, LastSeen, ExpiresAt time.Time
	Current                        bool
}

// KeySpec is a request to create an API key (§3.2).
type KeySpec struct {
	Owner     UserID
	Name      string
	Scopes    []rbac.Permission // REQUIRED non-empty, no wildcards (05-rbac §4.5); the stored record is never empty (R3-1)
	ExpiresIn time.Duration     // 0 = default; > max = error
	// Purpose and Module are deferred-build members (§3.5).
}

// KeyStatus is an API key's status (§3.2).
type KeyStatus string

const (
	KeyActive            KeyStatus = "active"
	KeyDisabled          KeyStatus = "disabled"
	KeyNeedsConfirmation KeyStatus = "needs_confirmation" // set by the post-restore hook (§4.14); refused until an admin re-enables
	KeyRevoked           KeyStatus = "revoked"
)

// valid reports a status the store may hold.
func (s KeyStatus) valid() bool {
	switch s {
	case KeyActive, KeyDisabled, KeyNeedsConfirmation, KeyRevoked:
		return true
	}
	return false
}

// rank orders statuses by restriction: revoked > needs_confirmation >
// disabled > active (§4.14 step 3, SEC7-8). An unknown status ranks with
// revoked (fail closed).
func (s KeyStatus) rank() int {
	switch s {
	case KeyActive:
		return 0
	case KeyDisabled:
		return 1
	case KeyNeedsConfirmation:
		return 2
	}
	return 3
}

// MoreRestrictive returns the more restrictive of a and b (§4.14 step 3).
func MoreRestrictive(a, b KeyStatus) KeyStatus {
	if b.rank() > a.rank() {
		return b
	}
	return a
}

// APIKey is one stored API key. Its secret is never held: the store keeps
// the SHA-256 of the raw value beside the record (§4.1). HashPrefix is the
// first 8 hex characters of that hash, HEAD's display identifier (§4.4).
type APIKey struct {
	ID         string
	Owner      UserID
	Name       string
	Scopes     []rbac.Permission // never empty (R3-1)
	Status     KeyStatus
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time // zero unless revoked
	LastUsedAt time.Time
	LastUsedIP netip.Addr
	UseCount   int64
	// Imported marks a key an app imported at its cutover (§4.1); its raw
	// value keeps the app's format.
	Imported   bool
	HashPrefix string
	Version    int64
}

// OneTimeKind is the family of a one-time token (§3.4).
type OneTimeKind string

const (
	OneTimeInvite    OneTimeKind = "invite"
	OneTimeTempLogin OneTimeKind = "temp_login"
	OneTimeBootstrap OneTimeKind = "bootstrap"
)

// OneTimeToken is one stored single-use token (§3.4, §4.1). ID is a public
// ULID (revoking an invite names it); the secret is never held.
type OneTimeToken struct {
	ID        string
	Kind      OneTimeKind
	UserID    UserID // temp-login: the target user; invite: none; bootstrap: none
	CreatedBy UserID // invite and temp-login: the actor who minted it
	// Username is an invite's account name, chosen by its creator (HEAD's
	// CreateInvite, handlers/user.go): accepting it creates that account.
	Username string
	Role     rbac.RoleName
	Email    string
	// CredentialEpoch is the epoch a bump of which kills the token (§4.12):
	// for a temp-login the target's at issue, for an invite its creator's.
	CredentialEpoch int64
	// CreatorEpoch is a temp-login's creator's credential epoch at issue
	// (§4.12, §3.2): consuming the link is refused unless the creator is
	// still at it, still active and still allowed to mint the link, so a
	// creator who was disabled, signed out everywhere, merged away or
	// demoted takes its outstanding links with it. An invite keeps its
	// creator's epoch in CredentialEpoch; this field is the temp-login's.
	CreatorEpoch int64
	// Reset marks a temp-login token minted as a reset link
	// (Service.MintResetLink): consuming it takes a new password, which is
	// stored with a credential epoch bump in the session's own batch, and
	// clears PasswordResetRequired. A plain temp-login takes no password.
	Reset bool
	// ClearsRestriction marks a reset link an owner-verified actor minted:
	// consuming it clears SignInRestricted (§4.14). It is set only with
	// Reset; a token carrying it without Reset is refused.
	ClearsRestriction bool
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

// ExternalIdentity links an OIDC or OAuth subject to a user (§4.7).
type ExternalIdentity struct {
	Provider  string
	Subject   string
	UserID    UserID
	Email     string
	CreatedAt time.Time
}

// UserMergePlan is the auth half of an admin user merge; books builds it from
// its preview (internal/usermerge stays in ABO).
type UserMergePlan struct {
	Source, Target UserID
	Rows           []MergeRow
}

// MergeRow is one record of a merge plan (§3.1).
type MergeRow struct {
	Kind string // "apikey", "identity", "inviteallow"; invites are deleted, never re-owned (DATA4-9)
	ID   string
	Seen []byte // the stored bytes the preview read (the Expect value)
}

// Merge row kinds.
const (
	MergeAPIKey      = "apikey"
	MergeIdentity    = "identity"
	MergeInviteAllow = "inviteallow"
)

// Failure is what stage 12 records when an authenticator refused a presented
// credential (05-http-api §4.2). Stage 13 turns it into a response.
type Failure struct {
	Code   string // "invalid_credential", "expired", "revoked", "inactive_user"
	Cookie bool   // the refused credential came from the session cookie (§4.3 cookie rule)
}

// Failure codes (§6).
const (
	CodeInvalidCredential = "invalid_credential"
	CodeExpired           = "expired"
	CodeRevoked           = "revoked"
	CodeInactiveUser      = "inactive_user"
	CodeResetRequired     = "password_reset_required"
)

type failureKey struct{}

// withFailure records f in ctx.
func withFailure(ctx context.Context, f Failure) context.Context {
	return context.WithValue(ctx, failureKey{}, f)
}

// FailureFrom returns the failure stage 12 recorded, and false when none.
func FailureFrom(ctx context.Context) (Failure, bool) {
	f, ok := ctx.Value(failureKey{}).(Failure)
	return f, ok
}

// ClientInfo is what auth knows of the caller's connection. Addr is the
// client address httpapi resolved (trusted-proxy aware, 05-http-api §3.5);
// Peer is the TCP peer. Forwarded is true when the request carried any
// forwarding header (Forwarded, X-Forwarded-For, X-Real-IP or the configured
// client-IP header); TrustedProxy when Peer is a trusted proxy or tunnel
// connector.
type ClientInfo struct {
	Addr         netip.Addr
	Peer         netip.Addr
	UserAgent    string
	Forwarded    bool
	TrustedProxy bool
	// HTTPS is §4.3's Secure rule: the request arrived over TLS, or from a
	// trusted proxy that reported https. httpapi's resolver sets it; the
	// session cookie's Secure attribute and its name follow it.
	HTTPS bool
	// OnLink: Addr is a global IPv6 address inside an on-link prefix of this
	// host (a home LAN numbered from the ISP's global /64). Only the resolver
	// can know it, because it reads the host's interfaces. Such an address,
	// like a private, link-local or loopback one, is a throttle source of its
	// own rather than one member of its /64 (sourceKey), so one LAN device
	// cannot spend the others' budgets. The default client (no Deps.Client)
	// leaves it false, so until the kernel wires httpapi's resolver the
	// devices of such a LAN share one source.
	OnLink bool
}

// Local is §4.6 item 4 (SEC-30): a loopback TCP peer, no forwarding header,
// and a peer that is not a trusted proxy or tunnel connector. Behind a local
// reverse proxy every request has a loopback peer, so the peer alone proves
// nothing.
func (c ClientInfo) Local() bool {
	return c.Peer.IsValid() && c.Peer.Unmap().IsLoopback() && !c.Forwarded && !c.TrustedProxy
}

// sourceKey is the throttle source of c (§4.9, HEAD's clientPrefixKey): the
// resolved address for IPv4 and for a LAN IPv6 address, the /64 of a public
// IPv6 address, so a caller who owns a /64 cannot mint 2^64 failure budgets.
// "" when no address is known: only the account is then charged.
func (c ClientInfo) sourceKey() string { return c.prefixKey(64) }

// saturatedKey is the key of the login ceilings' saturated admission (HEAD's
// clientSaturatedKey): sourceKey with a public IPv6 address grouped by its
// /48, the allocation an ISP hands one customer site, because there the
// per-visitor refusal is all that keeps one caller from holding a seat per
// /64.
func (c ClientInfo) saturatedKey() string { return c.prefixKey(48) }

// prefixKey is c's address, or for a public IPv6 address its prefix of bits.
// Private (ULA), link-local, loopback and OnLink addresses are kept whole:
// every device of an IPv6 LAN shares one /64.
func (c ClientInfo) prefixKey(bits int) string {
	if !c.Addr.IsValid() {
		return ""
	}
	a := c.Addr.WithZone("").Unmap()
	if a.Is6() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsLoopback() && !c.OnLink {
		return netip.PrefixFrom(a, bits).Masked().String()
	}
	return a.String()
}

// connectorKey is the login ceilings' connector bucket of c: the TCP peer when
// it is a trusted proxy or tunnel connector, "" for any other peer (direct
// callers are covered by their own budget and the global ceiling).
func (c ClientInfo) connectorKey() string {
	if !c.TrustedProxy || !c.Peer.IsValid() {
		return ""
	}
	return c.Peer.WithZone("").Unmap().String()
}

// NewUser is a request to create a user (§3.2).
type NewUser struct {
	Username      string
	Email         string
	EmailVerified bool
	DisplayName   string
	Password      string // "" = no password (an OIDC-only account)
	Roles         []rbac.RoleName
}

// UserFilter narrows Users (§3.1). The zero value lists everyone.
type UserFilter struct {
	Status UserStatus    // "" = any
	Role   rbac.RoleName // "" = any
}

// LoginInput is a password sign-in (§3.2).
type LoginInput struct {
	Username string
	Password string
	Remember bool
	Client   ClientInfo
}

// SetupInput creates the first admin (§4.6 item 3).
type SetupInput struct {
	Username       string
	Password       string
	Email          string
	BootstrapToken string
	Client         ClientInfo
}

// BootstrapResult is what a bootstrap redeem returns (§4.6 item 2).
type BootstrapResult struct {
	Key       string // the raw mck_ key, shown once
	KeyID     string
	ExpiresAt time.Time
	User      *User
	// GeneratedPassword is set only when the redeem created the admin.
	GeneratedPassword string
}

// InviteSpec is a request to create an invite (§4.13). The creator picks
// the account's username, as at HEAD (handlers/user.go CreateInvite).
type InviteSpec struct {
	Username string
	Email    string // optional; the account's e-mail, unverified
	Role     rbac.RoleName
	TTL      time.Duration // 0 = core.auth.invite.ttl
}

// AcceptInviteInput accepts an invite with the new account's password, as at
// HEAD (handlers/user.go AcceptInvite: token and password).
type AcceptInviteInput struct {
	Token    string
	Password string
	Client   ClientInfo
}

// InviteView is an open invite as shown: never its token (§4.13).
type InviteView struct {
	ID        string
	Username  string
	Email     string
	Role      rbac.RoleName
	CreatedBy UserID
	CreatedAt time.Time
	ExpiresAt time.Time
}
