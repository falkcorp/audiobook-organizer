<!-- file: docs/specs/platform-core/05-authn.md -->
<!-- version: 1.10.0 -->
<!-- guid: 73c4823d-8269-4c1d-8809-4006a6da6f0d -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authentication (`core/auth`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(gaps G1–G15; the full disposition table is in [05-rbac](05-rbac.md) §13, the authN-owned rows are
repeated in §13 here), [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Permissions/roles/audit: [05-rbac](05-rbac.md). Route chain: [05-http-api](05-http-api.md) §4.2.
Secret storage: [05-config](05-config.md) (`core/secrets`). Layers (L0–L9), `entity.Ref` and naming:
[03-architecture](03-architecture.md). Stage numbers for the rollout are 09-abo-on-core's (the auth
engine moves in exactly one stage, 09 S4). Stored data follows round-4 resolution R4-1 (refining R3-1):
`auth.Store` is an SDK interface (§3.4); core ships a native implementation over `c/auth/`, and an app
with pre-core identities (ABO) passes its own implementation, written in the app over the app's rows.
Under resolution A-2 ABO's store keeps HEAD's stored representation through S10 — raw `sess:` rows and
HEAD's one-time tokens, `abk_` keys holding legacy atoms, roles holding legacy atoms only — and stores
nothing beside them; the hashing, `mck_` keys and native scopes stay the native design and reach ABO
only at the identity hardening cutover after S10 (§11.2). Core holds no app key name or row format.

## 1. Purpose & scope

`core/auth` answers "who is calling, by which method, with what assurance" for every app built on
core, and owns the lifecycle of every core credential:

- **users** (single tenant, multi-user, D7), passwords, statuses, credential epochs;
- **sessions** (browser), **API keys** (automation), **bootstrap token** (first admin / recovery),
  **invites** and **temp-login links** (delegated sessions);
- **OIDC/OAuth** sign-in (generic OIDC + GitHub OAuth2) ending in a core session;
- the **authenticator extension point** for optional identity sources (Cloudflare Access — G11) and
  for app compatibility surfaces (ABS — G15);
- login throttling and lockout policy (G14), the **auth-off** mode semantics, and the
  **post-restore** credential reset.

It does **not** decide what a principal may do (that is `rbac`), does not store secrets other than
its own credential hashes (that is `secrets`), and does not own app protocols (ABS JWTs stay in the
`books` module).

## 2. Current state at HEAD

| Topic | Today | Anchor |
|---|---|---|
| User model | `User{ID, Username, Email, PasswordHashAlgo, PasswordHash, Roles []string, Status, CredentialEpoch, EmailSource, Version}` | `internal/database/store.go:734-780` |
| Auth plumbing | `auth.WithUser/WithPermissions/WithMethod/Can`; `Method` enum `session`, `session_delegated`, `api_key`, `cf_access`, `abs`; `WithMethod` can only downgrade interactivity; `MayChangeCredentials` allowlist | `internal/auth/context.go:33-110,171-178` |
| Request authentication | `RequireAuth`: store nil → pass; **0 users → pass everything**; presented `abk_` key wins; CF-bound user skips session; session lookup by **raw id**, revoked/expired, epoch, status; worker-only confinement | `internal/server/middleware/auth.go:152-262,265-298` |
| Per-request `CountUsers()` | called by `RequireAuth` and again by every `HoldsPermission` | `internal/server/middleware/auth.go:160-169,498-504` |
| Sessions | id = ULID (`crypto/rand`, 80 random bits), stored **unhashed** as Pebble key `sess:<id>` + `idx:sess:user:<user>:<id>`; TTL 24 h, remember-me 7 d | `internal/database/pebble_store_auth.go:1656-1675`, `internal/database/pebble_store.go:1050-1056`, `raw/10` §2.3 |
| Session cookie | name `session_id`, `Path=/`, `HttpOnly`, `SameSite=Strict`, `Secure` on HTTPS; no `__Host-` prefix | `internal/server/handlers/auth.go:226-247` |
| Session listing leaks tokens | `GET /api/v1/auth/sessions` returns the embedded `database.Session` (field `id` = bearer token) of **every** live session of the caller; the route has no permission and no credential guard, so an API key can read the user's session tokens | `internal/server/handlers/auth.go:513-537`, `raw/09` §4 rows 838-839 |
| API keys | `abk_` + base64url(32 B); SHA-256 hex stored; mandatory expiry (default 30 d, max 365 d); `TouchAPIKeyLastUsed` synchronous | `internal/database/apikey_token.go:16-35`, `internal/server/handlers/apikeys.go:27-33`, `internal/server/middleware/auth.go:309-400` |
| Key minted by a key | scopes checked ⊆ caller's effective set, **but empty scopes skip the check** and an empty-scope key means "all role permissions" | `internal/server/handlers/apikeys.go:199-226`, `internal/server/middleware/auth.go:405-408` |
| Bootstrap | `abbs_` token, 10 min TTL, 0600 file in secure state dir, hash in settings, single use under mutex; exchanged at public `POST /api/v1/auth/bootstrap` for an admin API key (8 h default, 24 h max); admin = first active user whose role holds `users.manage`, else generated | `internal/server/bootstrap.go:40,140-170,211-292,305-470,484-560` |
| First-run setup | public `POST /api/v1/auth/setup` creates the first admin (409 once a user exists); whole API open while 0 users | `internal/server/handlers/auth.go:267-292`, `raw/10` §5 |
| Temp-login | 16-byte hex tokens in a **process-global map**, 15 min, single use; creates a delegated 24 h session; any `users.manage` holder may mint one for any user | `internal/server/auth_temp_login.go:30-75,161-227` |
| Reset link / set password | reset hands the caller a temp-login link for any user; `PUT /api/v1/auth/me/password` with `user_id` sets any user's password for `users.manage` holders | `internal/server/auth_reset_password.go:18-60` |
| Invites | raw token as Pebble key `invite:<token>`, bound to `RoleID`, rate-limited lookup/accept | `internal/database/pebble_store_auth.go:1347,1362`, `internal/server/invite_rate_limit.go:334-465` |
| Passwords | argon2id m=64 MiB t=3 p=2, 16-byte salt; bcrypt verify + rehash; min length 8 | `internal/server/absauth/password.go:40-47,131-190`, `internal/server/handlers/auth.go:279-282` |
| OAuth/OIDC | GitHub OAuth2 (no PKCE), Google OIDC (go-oidc, PKCE S256); signed state; link by `(provider, subject)`, and an unseen subject links to **any** account with the same verified email; email allowlist + invite grants; default role; **no generic OIDC** | `internal/oauth/providers.go:23-180`, `internal/oauth/oauth.go:32-75`, `internal/oauth/resolve.go:65-205`, `internal/oauth/state.go:55-132` |
| Cloudflare Access | fail-open binder before `RequireAuth`; JWT verified (issuer, AUD, expiry); allowlisted email resolves a user; service tokens verify but resolve none | `internal/server/middleware/cfaccess.go:20-139`, `internal/oauth/cfaccess.go:74-97` |
| Owner proof | owner actions need a verified CF Access JWT whose email equals `owner_email` | `internal/auth/owner.go:31-108`, `internal/server/middleware/owner_guard.go:51-89` |
| CF policy editing / service tokens | `internal/cfaccesspolicy` (edits Access policy rules), `internal/cfservicetoken` (mints guest tokens) | `internal/cfaccesspolicy/client.go:83-402` |
| mTLS | CA/PSK dir + client-cert TLS config + stdio bridge; **not** HTTP auth; only importer is `cmd/mtls-bridge` (MCP bridge) | `internal/mtls/transport.go:15-57`, `cmd/mtls-bridge/main.go:1-25` |
| Throttling | ABS/web login throttle: per-IP failure counter (15 / 15 min), per-account soft delay after 5 (200 ms steps, max 2 s), LRU 10,000 keys with pinning; aggregate login ceiling; bootstrap and invite limiters; client key from CF-Connecting-IP only when the peer is a trusted tunnel connector (`TUNNEL_CONNECTOR_ADDRS`, zoned link-local allowed) | `internal/server/absauth/throttle.go:50-153`, `internal/server/login_ceiling.go`, `internal/server/bootstrap_rate_limit.go:194`, `internal/server/client_trust.go:32,91-130,201,312` |
| ABS compat auth | separate `ABSRequireAuth` (HS256 JWT + refresh, `abk_` keys, CF), separate `abs_sess:` store, resolves to the same `User` and role union; applied per route, not to every ABS-prefixed path | `internal/server/middleware/absauth.go:621,778-800`, `internal/database/pebble_store_abssession.go:49`, `internal/server/middleware/basicauth.go:51-70` |
| Auth off | `enable_auth=false`: auth + `s.perm` + `adminRole` are pass-through; "always-authenticated" admin routes still 401; the credential guard is a pass-through too, so credential routes (keys, temp-login, `POST /backup/restore`) are open; an in-handler `auth.Can` returns false (no permission set is bound) unless the site has its own auth-off bypass | `internal/server/server_lifecycle.go:1693-1698,1843-1848`, `internal/server/wire_admin_role.go:18-45`, `internal/server/credential_routes.go:79-82`, `internal/auth/context.go:171-178` |
| Session store hashing of other secrets | API keys and bootstrap hashed; sessions, invites, temp-login **raw** | `raw/10` §7 |
| How the legacy build reads stored grants | role permissions are unioned and compared by **exact string** (an unknown string such as `core:metrics.read` grants nothing in a positive check); but worker confinement is a negative test — a credential is worker-only only if **every** string is `fingerprint.worker` — so an extra string can widen (SEC4-3); a key's non-empty scope list is intersected with them, an empty one means all role permissions; a key is refused only for status `revoked` or `inactive`; a user only for a non-empty status other than `active` | `internal/server/middleware/auth.go:271-297,335-344,405-455`, `internal/auth/user_active.go` |

## 3. Go API

Package `github.com/falkcorp/mediacore/auth` (layer L4 with `rbac`, `audit`, `tasks`; imports `rbac`,
`audit`, `entity`, `storage`, `config`, `secrets`, `log`, `metrics`, `events`, and the leaf
`backup/journal` (L4; imports `entity`, `storage`) for the `RestoreHook`, `RestoreContext`,
`Revocation` and `RevocationJournal` types — not `backup` itself, which lands at 09 S8, while `auth`
lands at S4; `events` and `backup/journal` therefore land at S4 too, COR2-9). Subpackages:
`auth/password`, `auth/oidc`, and the conformance kit `auth/authtest` (`StoreSuite`). Cloudflare Access
is not a core package (§3.3: ABO's `internal/cfaccess` behind the authenticator extension point).
Storage (R4-1, §3.4, §11): every identity record is read and written through `auth.Store`. The native
implementation `core/internal/authstore` keeps them in the core keyspace `c/auth/` (`hello`,
`coretest`, new apps); ABO's `books` module passes a legacy implementation through
`module.Manifest.LegacyStores.Auth` (06 §6.4.2) that keeps ABO's identity rows in HEAD's representation
(A-2). No storage grant is involved during the extraction; core holds no app key name or row format.
What only such a legacy store can give meaning to — its optional capabilities and their conformance
suite — is not in `auth` but in `legacy`
(resolution R6-3, §3.4), the one core package outside the API promise. The `_system` principal
constructor lives in `core/internal/authsys` (not SDK, §3.1).

**Landing (COR3-3, COR4-11).** The `auth` leaf types `Principal`, `Method`, `MethodSpec`, `Assurance`,
`User`, `UserID` and `UserStatus` land at 09 S2 with `httpapi` (05-http-api §3), next to the rbac leaf
types they name (`rbac.Checker`, `rbac.RoleName`, 05-rbac §3); `Store`, `Service`, the authenticators
and the native store land at S4.

### 3.1 Identities and principals

```go
package auth

import (
	"context"
	"iter"
	"net/http"
	"net/netip"
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
	// A legacy-backed store derives it from HEAD's fields: a blanked
	// password_hash under a password algorithm other than "oauth" (§11).
	PasswordResetRequired bool
	// SignInRestricted: every authenticator — password, OIDC and identity
	// extensions — refuses this user (401 password_reset_required) until an
	// owner-verified reset link or the offline "<app> users reset-password"
	// clears it; set on a last admin a restore had to keep (05-rbac §7.1,
	// §4.14, SEC4-12). Native stores only: HEAD's rows have no field for it, so
	// a legacy-backed store reports false and PutUser ignores it (§11.2).
	SignInRestricted bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Version         int64
}

// Method says how the request was authenticated. Core methods are constants;
// identity extensions use "ext:<name>", app compatibility surfaces "<moduleID>.<name>".
type Method string

const (
	MethodNone             Method = ""
	MethodSession          Method = "session"           // the user's own interactive sign-in (password or OIDC)
	MethodSessionDelegated Method = "session_delegated" // temp-login, invite, or session of unknown origin
	MethodAPIKey           Method = "api_key"
	MethodAuthDisabled     Method = "auth_disabled"     // synthetic principal in auth-off mode (§4.11)
)

// Interactive and MayChangeCredentials keep today's allowlist semantics
// (internal/auth/context.go:63-90): only MethodSession is interactive and may
// change credentials among core methods. MethodAuthDisabled is explicitly
// non-interactive and MayChangeCredentials=false (fail closed). This is a
// deliberate tightening: HEAD's credential guard is a pass-through with auth
// off (credential_routes.go:79-82), recorded in 09 §9.2.6 (§4.11, SEC5-8).
// Unknown methods are non-interactive and may not change credentials.
// Extensions declare their own classification at registration (MethodSpec).
func (m Method) Interactive() bool
func (m Method) MayChangeCredentials() bool

type Assurance uint8

const (
	AssuranceNone  Assurance = 0
	AssuranceOwner Assurance = 1 << iota // an OwnerVerifier proved this principal is the deployment owner
	AssuranceMFA                         // reserved: a second factor was presented (WebAuthn, later)
)

// Principal is the authenticated caller bound into the request context.
type Principal struct {
	User         *User        // nil only for MethodAuthDisabled
	Method       Method
	CredentialID string       // session public id or API key id; never a secret
	Perms        rbac.Checker // always built by core: rbac.Service.Compile(roles, key scopes, interactive) (05-rbac §4.4); never supplied by an authenticator (§3.3)
	Assurance    Assurance
	AuthTime     time.Time    // when the credential was issued / sign-in happened
	ClientAddr   netip.Addr
}

func WithPrincipal(ctx context.Context, p *Principal) context.Context
func PrincipalFrom(ctx context.Context) (*Principal, bool)

// Identities is the read-only identity view a module receives as
// module.Deps.Auth (06 §6.4.4, COR-21): lookups and the request principal, no
// credential, session or user mutation. *Service implements it.
type Identities interface {
	User(ctx context.Context, id UserID) (*User, error)
	UserByUsername(ctx context.Context, name string) (*User, error)
	Users(ctx context.Context, f UserFilter) iter.Seq2[*User, error]
	PrincipalFrom(ctx context.Context) (*Principal, bool) // same as the package function
}

// Admin is the participant surface for multi-owner commits that rewrite
// identity rows (05-storage §5S.3.2, DATA-18); a module receives it as
// module.Deps.AuthAdmin. *Service implements it. Every method re-checks the
// ctx principal (core:users.manage, Interactive, dominance over both users,
// and the owner rule of §4.8), so holding the handle grants nothing.
type Admin interface {
	// StageUserMerge binds the auth Store to the caller's batch b and has it
	// rewrite the owner of every record in plan from Source to Target in the
	// store's one stored format (Store.StageMerge, R4-1), disables Source and
	// bumps its CredentialEpoch. A native store stages one Expect per record on
	// the bytes the plan was built from; ABO's legacy store keeps HEAD's own
	// conflict check under HEAD's locks (resolution A-3). Invites the store holds for Source that an older
	// build issued are deleted, not re-owned, and the deletion is listed in
	// the merge's audit record (DATA4-9). The caller commits; a record changed
	// since the plan was built fails the commit with storage.ErrConflict,
	// which the caller reports as auth.ErrMergeConflict (today's
	// UserMergeConflictError under userMergeApplyMu/inviteMu/apiKeyMu,
	// internal/database/user_merge.go:507-531).
	StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) error
}

// UserMergePlan is the auth half of an admin user merge; books builds it from
// its preview (internal/usermerge stays in ABO).
type UserMergePlan struct {
	Source, Target UserID
	Rows           []MergeRow
}

type MergeRow struct {
	Kind string // "apikey", "identity", "inviteallow"; legacy invites are deleted, never re-owned (DATA4-9)
	ID   string
	Seen []byte // the stored bytes the preview read (the Expect value)
}

// Failure is what stage 12 records when an authenticator refused a presented
// credential (05-http-api §4.2). Stage 13 turns it into a response.
type Failure struct {
	Code   string // "invalid_credential", "expired", "revoked", "inactive_user"
	Cookie bool   // the refused credential came from the session cookie (§4.3 cookie rule)
}

func FailureFrom(ctx context.Context) (Failure, bool)
```

**No public system principal (SEC-26).** v1.0.0's public `auth.System(perms)` let any module mint a
principal with any checker. The background principal (the `_system` user,
`internal/auth/seed.go:134-152`) is now built only by `core/internal/authsys.Principal(perms
[]rbac.Permission)`, which the `tasks` executor calls with the running definition's declared
permissions (05-rbac §3.4, `authz.ForSystem`). Apps cannot import it (Go `internal/` rule);
`archtest.NoSystemPrincipalOutsideKernel` restricts core callers to `tasks` and `kernel`, and flags
composite literals of `auth.Principal` outside `core/auth`, `core/internal/...` and `coretest`.

### 3.2 Service

```go
type Service struct{ /* unexported */ }

func New(ctx context.Context, opts Options, deps Deps) (*Service, error)

type Deps struct {
	Store    Store          // native core/internal/authstore over c/auth/, or the app's legacy store from module.Manifest.LegacyStores.Auth (§3.4, §11, R4-1)
	RBAC     *rbac.Service  // checker compilation, CanAssign, Dominates
	Audit    audit.Logger
	Secrets  secrets.Store  // OIDC client secrets, derived keys (05-config)
	Events   *events.Bus    // publishes core.principal.invalidated.v1
	Revocations journal.RevocationJournal // §4.12: Append(ctx, b, r) into the revoking batch b before it commits (05-backup §5B.3.6)
	Clock    func() time.Time
	Log      *slog.Logger
	Metrics  *metrics.Scope // "mediacore.auth" (05-metrics)
	StateDir *os.Root       // bootstrap token file (os.Root-confined)
}

// Options is the app's compile-time auth configuration, passed as
// app.Options.Auth (06 §6.4.5, landing at S4). The zero value is what new
// apps and hello get.
type Options struct {
	LegacyKeyPrefixes []string       // old API-key prefixes the key authenticator recognises (§4.1; ABO ["abk_"])
	InviteLinkForm    InviteLinkForm // InviteLinkFragment (zero value) or InviteLinkPath (§4.13; ABO InviteLinkPath from S4 to S9; the S4 legacy SPA also reads the fragment form, FE5-4)
	// Defaults of the app's own auth config keys (§8); operator layers and
	// overrides still win.
	DefaultCookieName          string // core.auth.session.cookie_name; "" = "<AppID>_session" (ABO "session_id")
	DefaultCookieNoHostPrefix  bool   // true sets core.auth.session.cookie_host_prefix's default to false (ABO true)
	DefaultDisabledAllowRemote bool   // core.auth.disabled_allow_remote's default (§4.11; ABO true, SEC4-8)
}

// InviteLinkForm is the shape of invite links auth mints (§4.13, FE4-2).
type InviteLinkForm uint8

const (
	InviteLinkFragment InviteLinkForm = iota // "/invite#token=…" (default)
	InviteLinkPath                           // "/invite/<token>", the pre-shell SPA's form
)

// Users. Every method that acts on another user takes the actor and enforces
// 05-rbac §4.7 rule 5 (dominance) inside the service (SEC-4).
func (s *Service) CreateUser(ctx context.Context, actor *Principal, in NewUser) (*User, error)
func (s *Service) User(ctx context.Context, id UserID) (*User, error)
func (s *Service) UserByUsername(ctx context.Context, name string) (*User, error)
func (s *Service) Users(ctx context.Context, f UserFilter) iter.Seq2[*User, error]
func (s *Service) ChangeOwnPassword(ctx context.Context, actor *Principal, current, next string) error // self only, Interactive
func (s *Service) SetStatus(ctx context.Context, actor *Principal, id UserID, st UserStatus) error      // bumps epoch on disable/enable
func (s *Service) DeleteUser(ctx context.Context, actor *Principal, id UserID) error
func (s *Service) SetRoles(ctx context.Context, actor *Principal, id UserID, roles []rbac.RoleName) error // CanAssign on added and removed roles + dominance
func (s *Service) MintTempLogin(ctx context.Context, actor *Principal, target UserID) (link string, err error)
func (s *Service) MintResetLink(ctx context.Context, actor *Principal, target UserID) (link string, err error)
func (s *Service) HasUsers() bool // atomic flag, no store read

// Password sign-in. Throttled (§4.9); returns ErrInvalidCredentials for unknown
// user or wrong password with equal timing (dummy-hash derivation kept).
func (s *Service) Login(ctx context.Context, in LoginInput) (*IssuedSession, error)

// Sessions.
func (s *Service) IssueSession(ctx context.Context, u *User, origin SessionOrigin, remember bool, client ClientInfo) (*IssuedSession, error)
func (s *Service) Sessions(ctx context.Context, user UserID) iter.Seq2[SessionView, error] // never contains a token
func (s *Service) RevokeSession(ctx context.Context, actor *Principal, publicID string) error
func (s *Service) RevokeAllSessions(ctx context.Context, actor *Principal, user UserID, except string) error

type IssuedSession struct {
	Token     string // returned once, in the store's format: native "mcs_..." (§4.1); ABO HEAD's raw ULID (§11)
	PublicID  string // ULID, safe to show, used for revoke
	ExpiresAt time.Time
}

type SessionView struct {
	PublicID, UserAgent string
	ClientAddr          netip.Addr
	Origin              SessionOrigin
	CreatedAt, LastSeen, ExpiresAt time.Time
	Current             bool
}

// API keys.
func (s *Service) CreateAPIKey(ctx context.Context, actor *Principal, spec KeySpec) (raw string, key *APIKey, err error)
func (s *Service) APIKeys(ctx context.Context, owner UserID) iter.Seq2[*APIKey, error]
func (s *Service) SetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error // monotone toward revoked (§4.4)
func (s *Service) RevokeAPIKey(ctx context.Context, actor *Principal, id string) error

type KeySpec struct {
	Owner     UserID
	Name      string
	Scopes    []rbac.Permission // REQUIRED non-empty, no wildcards (05-rbac §4.5); the stored row is never empty (R3-1); on a legacy-backed store a union of whole legacy atoms, else 422 no_legacy_atom (§4.4)
	ExpiresIn time.Duration     // 0 = default; > max = error
	// Purpose and Module are deferred-build members (§3.5).
}

type KeyStatus string

const (
	KeyActive            KeyStatus = "active"
	KeyDisabled          KeyStatus = "disabled"
	KeyNeedsConfirmation KeyStatus = "needs_confirmation" // set by the post-restore hook (§4.14); refused until an admin re-enables; native stores only: a legacy-backed store writes KeyDisabled instead (§4.4)
	KeyRevoked           KeyStatus = "revoked"
)

// Bootstrap (§4.6).
func (s *Service) RedeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error)

// Compatibility surfaces and extensions build principals through this; it
// applies status, epoch, role resolution and scope intersection exactly like
// core credentials (G15). m must be a registered method (§3.3).
func (s *Service) PrincipalFor(ctx context.Context, u *User, m Method, scopes []rbac.Permission, epoch int64) (*Principal, error)

// CheckCredentialFor applies, for a surface that authenticates itself (an
// OwnAuth route, which stages 12-13 skip), the rules stage 12/13 would have
// applied to p on a route requiring perm: key status and expiry, and
// confinement — a Confined() principal is refused unless perm names one of its
// scopes (SEC2-4; 05-http-api §8.3). Returns ErrConfined or nil.
func (s *Service) CheckCredentialFor(p *Principal, perm rbac.Requirement) error

// Middleware returns http-api stage 12 (05-http-api §4.2). It runs the
// authenticators in order and STOPS at the first one that returns anything
// other than ErrNoCredential: a principal is bound, or an auth.Failure is
// recorded in the context. It never writes a response except 503
// auth_unavailable on store failure (SEC-12). Stage 13 answers 401.
func (s *Service) Middleware() func(http.Handler) http.Handler

// Revalidate reports whether a long-lived principal (an SSE stream) still
// stands: user active, epoch unchanged, credential not revoked. Cheap: served
// from an in-memory generation table invalidated by events.
func (s *Service) Revalidate(ctx context.Context, p *Principal) error

// PostRestore is auth's journal.RestoreHook (05-backup §5B.3.2). It is a
// package function, not a Service method: the kernel runs it in P4d's
// post-restore step (06 §6.5.1), after the keyring, the config overrides and
// the journal re-apply and before any service is built, over the Store (one
// stored format, R3-1; §4.14, SEC-16). rc.Revocations yields the typed journal.Revocation entries
// after rc.Boundary = min(the source's recorded journal seq, the swapped-in
// store's journal_head.seq) (05-backup §5B.3.6; clock-free, DATA2-7), or is
// nil when the journal does not cover that interval. It returns per-family counts, which audit's own hook records.
func PostRestore(ctx context.Context, d RestoreDeps, rc journal.RestoreContext) (RestoreCounts, error)

type RestoreDeps struct {
	Store       Store                      // the Store built in P4c
	// Previous is a read-only Store over the pre-swap store of a staged
	// restore (05-backup §5B.3.6); nil when there is none (a replaced store,
	// host loss). The kernel builds it by calling the live store's constructor
	// once more, over the handles storage.Admin.OpenReadOnly issues for the
	// pre-swap directory (Legacy(owner), Keyspace(prefix); every write fails
	// ErrReadOnly; 05-storage), so a constructor runs once per boot at P4c and,
	// on a staged restore, once more at P4d step 3 (COR7-8). §4.14 step 3
	// reads it (SEC5-10).
	Previous    Store
	Revocations journal.RevocationJournal  // the lines the hook appends (§4.12)
	StateDir    *os.Root                   // bootstrap token file
	Clock       func() time.Time
}

// BootWrites are auth's every-boot writes, run by the kernel in P4d step 4,
// after the post-restore step (06 write rule, DATA3-18), from 09 S4 r2:
// seed the _system user; mint the bootstrap token and, when enabled, the
// start-up read-only key (§4.6, COR4-13); and, when the Store implements
// legacy.AuthReconciler, run its open-time maintenance (ABO: HEAD's
// verified-email reconcile and one-shot key expiry stamp, §11; DATA4-10,
// DATA4-12). auth.New and a Store constructor write nothing. Through S4 r1
// none of this runs, and books' start hook keeps HEAD's own sequence instead
// (§5, COR6-3). Nothing here depends on what an older build did while
// rolled back: it read and wrote the same rows (A-2).
func BootWrites(ctx context.Context, d RestoreDeps) error

type RestoreCounts map[string]int // family -> rows revoked, deleted or restricted
```

### 3.3 Authenticators (extension point, G11/G15)

```go
// Authenticator inspects a request and returns an Assertion, or ErrNoCredential
// to let the next one try, or another error. Any result other than
// ErrNoCredential ends authentication for the request (a presented-but-invalid
// credential never falls through to a weaker one); the middleware records it
// as auth.Failure and stage 13 refuses non-Public routes with 401.
// An authenticator never returns a Principal or a Checker (SEC2-16): stage 12
// builds the Principal from the Assertion through PrincipalFor — status,
// epoch, role resolution and rbac.Compile({User.Roles, Scopes, Interactive})
// are core's — so no extension decides what the caller may do.
type Authenticator interface {
	Name() string
	Authenticate(r *http.Request) (*Assertion, error)
}

type Assertion struct {
	User         UserID
	Scopes       []rbac.Permission // nil = the user's role permissions
	CredentialID string            // never a secret
	AuthTime     time.Time
	Owner        bool              // honoured only for an extension registered with OwnerVerifier=true (below)
}

type MethodSpec struct {
	Method               Method // "ext:cfaccess", "books.abs"
	Interactive          bool
	MayChangeCredentials bool
	OwnerVerifier        bool   // the extension may assert AssuranceOwner
}

// Extension is an identity extension, registered app-level and at compile time
// through app.Options.Authenticators (06 §6.4.5). It is the ONLY place where
// Interactive, MayChangeCredentials or OwnerVerifier may be true.
type Extension struct {
	Authenticator Authenticator
	Spec          MethodSpec // Method "ext:<name>"
	Order         Order
	// OwnerSet reports whether an owner identity is configured (ABO: a
	// non-empty owner e-mail); required iff Spec.OwnerVerifier. The owner
	// gate's default and ProtectOwner both derive from it (§4.8, SEC4-14).
	OwnerSet      func() bool
}

// A module registers a compat method (a surface that authenticates itself,
// e.g. ABS) through module.Registrar.AuthMethod(spec) (06 §6.4.3). The kernel
// refuses it unless Method is "<moduleID>.<name>", Interactive,
// MayChangeCredentials and OwnerVerifier are all false. The method's name is
// what an OwnAuth legacy route names (05-http-api §8.2).

type Order int // relative position; core's API-key and session authenticators are fixed at 100 and 200

var (
	ErrNoCredential       = errors.New("auth: no credential presented")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrExpired            = errors.New("auth: credential expired")
	ErrRevoked            = errors.New("auth: credential revoked")
	ErrInactiveUser       = errors.New("auth: user not active")
	ErrStaleEpoch         = errors.New("auth: credentials changed since issue")
	ErrThrottled          = errors.New("auth: throttled")
	ErrBootstrapInvalid   = errors.New("auth: bootstrap token invalid, expired or spent")
	ErrScopeEscalation    = errors.New("auth: requested scopes exceed caller's")
	ErrDominance          = errors.New("auth: actor does not dominate the target user") // wraps rbac.ErrDominance
	ErrLastAdmin          = errors.New("auth: change would leave no active core:admin holder")
	ErrEmailInUse         = errors.New("auth: email belongs to an account this provider may not link")
	ErrStoreUnavailable   = errors.New("auth: credential store unavailable")
	ErrMergeConflict      = errors.New("auth: an identity row changed since the merge preview")
	ErrConfined           = errors.New("auth: confined credential outside its scopes")
	ErrResetRequired      = errors.New("auth: password reset required before password sign-in")
	ErrMethodSpec         = errors.New("auth: compat method must be <moduleID>.<name>, non-interactive, without credential or owner rights")
)
```

Core ships **no** identity extension. The one ABO uses, Cloudflare Access, stays in ABO as
`internal/cfaccess` ([04](04-package-mapping.md) §2.4; G11: deployment-specific): it verifies
`Cf-Access-Jwt-Assertion` / `CF_Authorization` (issuer, AUD, expiry, JWKS via go-oidc), resolves an
allowlisted e-mail to a user (its `Assertion.User`), asserts `Owner` when the owner e-mail matches,
and may mark the CDN client-IP header believable for verified requests (from S4 it also carries the
"local peer + verified Access assertion" branch of ABO's `client_trust.go`, 05-http-api §8.1).
ABO registers it in `app.Options.Authenticators` as `auth.Extension{Spec: MethodSpec{Method:
"ext:cfaccess", Interactive: true, OwnerVerifier: true}, …}`; its settings are books keys under
`modules.books.identity.*` ([09](09-abo-on-core.md) S4). The ABS method is registered by the books
module with `Registrar.AuthMethod(MethodSpec{Method: "books.abs"})`.

v1.0.0 also specified an `auth/clientcert` mTLS authenticator. It is dropped (SIMP-23): it has no
consumer, and the `Authenticator` extension point leaves room for it. ABO's mTLS MCP bridge stays in
ABO (§4.8).

### 3.4 Store

```go
// Store is the identity store (resolution R4-1). It is SDK, frozen by the
// API-stability gate. Core's native implementation, core/internal/authstore,
// keeps every record in the core keyspace c/auth/ (hello, coretest and every
// app without pre-core identities; §11 "Native store"). An app with pre-core
// identities passes its own implementation through
// module.Manifest.LegacyStores.Auth (06 §6.4.2), written in the app over the
// app's own rows: core holds no app key name or row format (03 R1).
//
// The interface is representation-neutral (resolution A-2): a credential
// crosses it as the raw presented value, and the store alone decides how it
// is minted and kept. The native store mints mcs_/mck_/mcb_/mci_/mct_ values
// and keeps only their SHA-256 (§4.1); ABO's legacy store mints and keeps
// HEAD's formats (raw ULID sessions, abk_ keys, raw invite tokens, the abbs_
// bootstrap hash, in-memory temp-login; §11). Core enforces its invariants
// above the interface — never-empty key scopes, status, epoch and expiry
// checks, dominance — and auth/authtest.StoreSuite is the conformance suite
// every implementation runs. Every write takes the caller's *storage.Batch,
// never a copy, so the staged writes commit with the journal line and audit
// record (COR2-12). Single use and lost-update safety: the native store stages
// Expect(key, seen) plus Delete for every Take* over a Serialized family, and
// Expect on the bytes it read for every write derived from a record (DATA4-9,
// SEC6-3, DATA6-8, R6-5); a legacy store keeps its app's own lock discipline
// on its legacy rows (ABO: HEAD's keyed mutexes, inviteMu, bootstrapMu and
// apiKeyMu with early fix 6; resolution A-3). The interface holds the native
// contract only: capabilities that only a pre-core app's store can give
// meaning are declared in legacy (below), outside the API promise (SIMP5-4,
// resolution R6-3; 05-storage §5S.3.11 "Legacy capabilities").
type Store interface {
	// users
	PutUser(b *storage.Batch, u *User) error
	GetUser(ctx context.Context, id UserID) (*User, error)
	UserByUsername(ctx context.Context, fold string) (*User, error)
	ScanUsers(ctx context.Context) iter.Seq2[*User, error] // an undecodable stored record yields ErrUndecodableRecord for that record only

	// sessions (§4.3)
	IssueSession(b *storage.Batch, s *SessionRecord) (token string, err error) // mints the token in the store's format and stages the record
	SessionByToken(ctx context.Context, token string) (*SessionRecord, error)
	SessionByPublicID(ctx context.Context, id string) (*SessionRecord, error)
	ScanUserSessions(ctx context.Context, u UserID) iter.Seq2[*SessionRecord, error]
	DeleteSession(b *storage.Batch, s *SessionRecord) error
	// TouchSession records a session's last use: it changes only LastSeen on
	// the current record and stages nothing when the record is absent,
	// expired or epoch-stale, so a flush after a logout or a revoke never
	// re-creates the session (DATA6-8). A store whose rows have no last-use
	// field (ABO's) stages nothing.
	TouchSession(b *storage.Batch, publicID string, at time.Time) error
	// RevokeSignInsBefore deletes every session and one-time token the store
	// holds, in whatever representation, created before t (zero t: all)
	// (§4.14 step 2).
	RevokeSignInsBefore(b *storage.Batch, t time.Time) (int, error)

	// API keys
	IssueAPIKey(b *storage.Batch, k *APIKey) (raw string, err error) // mints the key in the store's format
	PutAPIKey(b *storage.Batch, k *APIKey) error                     // status, expiry, name
	// TouchAPIKey records a key's last use (last_used_at, last_used_ip,
	// use_count), changes only those fields and stages nothing for an absent,
	// revoked or inactive key; it never undoes a revocation (§4.4; SEC6-3,
	// DATA6-8, resolution R6-5; on ABO's store through early fix 6's lock).
	TouchAPIKey(b *storage.Batch, id string, at time.Time, ip netip.Addr) error
	APIKeyByToken(ctx context.Context, raw string) (*APIKey, error)
	ScanAPIKeys(ctx context.Context, owner UserID) iter.Seq2[*APIKey, error] // owner "" = every key

	// one-time tokens: invite, temp-login, bootstrap
	IssueOneTime(b *storage.Batch, t *OneTimeToken) (token string, err error)
	TakeOneTime(b *storage.Batch, kind OneTimeKind, token string) (*OneTimeToken, error) // single use (above)

	// external identities and the invite allow-list (§4.7)
	PutIdentity(b *storage.Batch, id *ExternalIdentity) error
	DeleteIdentity(b *storage.Batch, id *ExternalIdentity) error
	IdentityBySubject(ctx context.Context, provider, subject string) (*ExternalIdentity, error)
	InviteAllowed(ctx context.Context, email string) (bool, error)

	// StageMerge rewrites the owner of every record of plan (Admin.StageUserMerge).
	StageMerge(b *storage.Batch, plan UserMergePlan) error
}

var ErrUndecodableRecord = errors.New("auth: stored identity record cannot be decoded")

type SessionRecord struct {
	PublicID        string        // safe to show and used for revoke; ABO: early fix 1's derived handle (§11.1)
	UserID          UserID
	Origin          SessionOrigin // password, oidc:<provider>, delegated:temp_login, delegated:invite; ABO: HEAD's Origin field, "" = delegated
	CredentialEpoch int64
	CreatedAt, ExpiresAt, LastSeen time.Time
	ClientAddr      netip.Addr
	UserAgent       string
	Version         int64
}

// KeyLegacyState is APIKey.Legacy, set only for a key read from a
// legacy-backed store: the legacy atoms its row holds (empty =
// LegacyFullScope; 05-rbac §4.5, §6.3), whose canonical expansion is then in
// APIKey.Scopes. Its type stays in auth because legacy imports auth.
type KeyLegacyState struct {
	Atoms []string
}
```

**Legacy capabilities of the identity store (`legacy`, resolution R6-3, SIMP6-2).** Every identifier
that exists only to serve a pre-core app's identity store lives in the core package `legacy` (with its
test-support sub-package `legacy/legacytest`), which the API-stability gate excludes as it excludes
`kernel/` (03 §3.3 "Legacy-only identifiers"; 12 SC1). Removing them at the identity hardening cutover
is therefore not a breaking change, and nothing in `auth`'s frozen surface names them:

```go
package legacy

// AuthReconciler is implemented by an identity store with its own every-boot
// maintenance, run by auth.BootWrites at P4d step 4 into b (ABO: HEAD's
// open-time identity writers, 05-authn §11). Absent = nothing to do.
type AuthReconciler interface {
	Reconcile(ctx context.Context, b *storage.Batch) error
}

// AuthAtomWriter is implemented by an identity store whose key rows an older
// build reads and that therefore holds legacy atoms (05-rbac §6.3): auth calls
// IssueAPIKeyAtoms instead of IssueAPIKey with the atoms of 05-rbac §6.3's
// write mapping (never empty), and refuses a key that mapping cannot
// represent (422 no_legacy_atom). Absent = IssueAPIKey with canonical scopes.
type AuthAtomWriter interface {
	IssueAPIKeyAtoms(b *storage.Batch, k *auth.APIKey, atoms []string) (raw string, err error)
}
```

`legacy` (L5) imports `auth` for the types it names (and for `legacy.Stores.Auth`'s `auth.Store`), so
`auth` never imports it (03 §3.3). The kernel asserts only the capabilities 03 §3.3 lists, none of
them this section's (SIMP8-3), and hands the store to `auth` as an `auth.Store`; every capability of
this section is detected only by `auth` itself, through its mirrors (SIMP7-5). The service and the
package function `BootWrites` name a capability only through an **unexported** interface of their own
with the identical method set. The mirror test is 03 §3.3's (resolution R7-7, COR7-2, SIMP7-1):
auth's `export_test.go` (`package auth`) only exports a test alias of each mirror, and
`legacy_mirror_test.go` in auth's external test package (`package auth_test`, which may import
`legacy`) asserts that each alias and its `legacy` counterpart are mutually assignable, so a renamed or
retyped member fails auth's tests. `legacytest.AuthStoreSuite` (one suite for the store, a case per
capability: `Reconcile`, the atom writer; SIMP6-6) drives the service over an implementation.

### 3.5 Deferred-build members

These land with their consumer. Deferral applies to features inside built packages as well as to packages (SIMP2-6, 03 §3.4). These
members of `auth` are specified so 08 and 10 can rely on them, and are **not built** at 09 S4:

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `auth.KeySpec.Purpose`, `auth.KeyPurpose`, `auth.PurposeGeneral`, `auth.PurposeFacade`, `auth.KeySpec.Module` | the stored key record's `purpose` field; tests `TestAPIKey_FacadePurposeBoundToModule`, `TestFacadeKey_RefusedOnABS` (SEC-5) | `arrfacade` (08) | a `PurposeFacade` key names a `Module`; every scope is one of that module's `<m>:arr.*` scopes (all `Confining`, 05-rbac §4.10); it is accepted only on that module's facade group (stage 12), and a general key holding `<m>:arr.*` plus anything else is refused there (08 §8.5's "every scope is arr" rule as a second check); `PurposeGeneral` is the zero value and the stored `purpose` field defaults to it; display adds purpose |

Also deferred, with no Go identifier of its own (not checked by the archtest; COR5-16, SIMP5-6):

- `X-Api-Key` header and `apikey` query-key extraction — with `arrfacade` (08): enabled **only** on
  facade groups (§4.2 step 2); httpapi's 05-logging redaction already covers `apikey`.
- Feed token — with the first TV/Movies app ([10](10-tv-movies-proof.md) G9): a per-route opt-in
  credential in the query string for clients that cannot send headers (iCal, OPDS): scoped,
  read-only, revocable, stored hashed like a key, accepted only on routes that opt in.

Until they land, SEC-5 holds trivially (no facade key can be minted) and HEAD's extraction rule is
kept exactly: Bearer and the session cookie only (`APIKeyFromRequest`,
`internal/server/middleware/auth.go:83-98`). The additions are additive and pass the API-stability
gate. The table is planning information (resolution A-4: no archtest checks the absence of these
members); the PR that builds a deferred member deletes that member's row.

## 4. Behaviour

### 4.1 Credential formats

These are the **native** store's formats. All secrets are 32 bytes from `crypto/rand`, encoded
base64url without padding (43 chars), with a type prefix and a 6-char CRC32 checksum suffix (base62) so
secret scanners can recognise and offline-validate a leaked value without a database. Stored only as
`SHA-256(raw)`; 256-bit random input makes a slow KDF unnecessary and keeps verification O(1). A
legacy-backed store mints and keeps its app's own formats instead, because the store decides the
representation (§3.4; resolution A-2).

| Credential | Format | Lifetime | Stored as |
|---|---|---|---|
| Session | `mcs_<43>_<6>` | 24 h; remember-me 7 d; sliding `LastSeen` (idle timeout optional) | `SHA-256` key + public ULID |
| API key | `mck_<43>_<6>` | default 30 d, max 365 d (today's values, `handlers/apikeys.go:27-33`) | `SHA-256` |
| Bootstrap | `mcb_<43>_<6>` | 10 min, single use | `SHA-256` as a native one-time token in the store + 0600 file |
| Invite | `mci_<43>_<6>` | default 72 h (today, `handlers/user.go:177-181`), single use | `SHA-256` |
| Temp-login | `mct_<43>_<6>` | 15 min, single use | `SHA-256` in storage (not a process map, R5) |
| ABO's legacy store, through S10 (issued and accepted) | HEAD's: raw-ULID session cookies, `abk_…` keys, raw invite tokens, `abbs_` bootstrap token, temp-login tokens in memory | HEAD's lifetimes (the same defaults as above) | HEAD's rows: sessions and invites raw, keys and bootstrap as SHA-256 (§11) |

The native prefixes are core's, not per-app: one binary may host several modules, and a key is a
core credential. `auth.Options.LegacyKeyPrefixes` (ABO: `["abk_"]`) lets the API-key
authenticator recognise the keys an app's legacy store issues (the "presented key wins" rule needs to know a key when it sees
one, `internal/server/middleware/auth.go:76-99`). One-time tokens never travel in a URL path
(§4.13, SEC-21).

### 4.2 Request authentication order (stage 12)

0. If `core.auth.mode=disabled`: bind the auth-disabled principal (§4.11) and **ignore every
   presented credential** — a stolen key does not regain `AuthRequired` scopes in disabled mode
   (SEC-2).
1. If `HasUsers()` is false: bind nothing (anonymous); http-api stage 13 then allows only routes
   marked `rbac.Public` plus the setup/bootstrap routes (§4.6). **The API is no longer open while
   there are zero users** (today: everything reachable, `middleware/auth.go:165-169`).
2. An API key presented anywhere a credential is accepted (Bearer or session cookie value; the
   facade's `X-Api-Key` / `apikey` query extraction is a deferred-build member, §3.5) wins,
   replacing anything else. Kept from `middleware/auth.go:173-176`.
3. Extension authenticators from `app.Options.Authenticators` in `Order` (e.g. `cfaccess`). `ErrNoCredential` → next. A
   *presented* invalid assertion does not fall through — unlike today's fail-open CF binder; the
   fail-open behaviour existed only because CF was optional, and is kept by returning
   `ErrNoCredential` when no assertion is present.
4. Session token from Bearer or the session cookie (§4.3).
5. Nothing → anonymous.

**Stop rule (SEC-12).** The first authenticator that returns anything other than `ErrNoCredential`
ends the sequence. Success binds the principal; an error records `auth.Failure{Code, Cookie}` in the
context and leaves the request otherwise anonymous. Stage 12 itself writes no response (except 503
`auth_unavailable`); stage 13 answers 401 with the failure code on non-Public routes and applies the
cookie rule of §4.3 on Public routes. So "key plus a valid session" with an invalid key is a 401, as
today's key-wins rule requires (`middleware/auth.go:76-99`), and never silently a session request.

On success: user `Status` must be active; epoch must match (sessions, temp-login, invites); key
must be `active` and unexpired; the principal is built by core from the credential or the
extension's `Assertion` (§3.3) — the checker is always
`rbac.Service.Compile({User.Roles, Scopes, Interactive: method.Interactive()})`, whatever
authenticator ran (SEC2-16); `AssuranceOwner` is set only when an extension registered with
`OwnerVerifier: true` asserts `Owner`.

**Worker confinement (SEC-3).** Confinement is not path-based and not a namespace heuristic:
`books:fingerprint.worker` (and, when the facade lands, every `<m>:arr.*` scope) is `Confining`
(05-rbac §3.2), and a principal holding only confining scopes satisfies only requirements that name
one of them — never `AnyAuthenticated` (05-rbac §4.10). This reproduces today's worker path
confinement (`middleware/auth.go:265-298`) for every route stage 13 runs on, including the 36 that had
no permission; for `OwnAuth` routes, which stage 13 skips, `CheckCredentialFor` applies the same rule
(§4.10, SEC2-4).

### 4.3 Sessions (hashed at rest in the native store)

- Issued by password login, OIDC callback, temp-login consumption, invite acceptance.
- **Cookie (SEC-11).** Name `core.auth.session.cookie_name`, default `<AppID>_session` (03 §3.6
  app ID), so two apps on one host never read or overwrite each other's cookie (cookies are not
  isolated by port). When the cookie is set with `Secure`, its name gets the `__Host-` prefix (which
  forces `Path=/` and no `Domain`) unless `core.auth.session.cookie_host_prefix=false`. ABO sets
  `cookie_name=session_id` and `cookie_host_prefix=false` for continuity with today's cookie
  (`handlers/auth.go:226-247`). Attributes: `HttpOnly`, `SameSite=Strict`, `Secure` when the
  request is HTTPS or came through a trusted https proxy, `Path=/` — apps are served at the origin
  root only; core has no base-path support, as ABO has none today (FE-19, 05-http-api §4.1). Bearer
  accepted for non-browser clients.
- `SameSite=Strict` is **site**-scoped, so it does not protect against a sibling app or a
  sibling subdomain on the same site. Unsafe requests are therefore also checked by httpapi's
  cross-origin stage (05-http-api §4.2 stage 7, SEC-10), which exempts **no credential type**:
  non-browser clients pass because they send no `Sec-Fetch-Site`/`Origin`, and Basic credentials,
  which browsers attach cross-site like cookies, are not a bypass (FE2-23). 07 refers to this
  section and to §6 instead of restating them (an unknown cookie is the cookie failure rule below,
  never "treated as absent").
- **Cookie failure rule (SEC-11, FE-6).** When the only credential presented is the session cookie
  and it is unknown, expired, revoked or epoch-stale, the failure is recorded with `Cookie=true`.
  On a `Public` route the request proceeds anonymous and the response clears the cookie, so a stale
  cookie never blocks login, setup, bootstrap or invite pages. On a non-Public route the answer is
  401 with the failure code (and the cookie is cleared). An invalid Bearer or API-key credential is
  a 401 on every route, Public included.
- The native store looks a session up by `SHA-256(token)`; the token never touches its storage. On
  every store the token never reaches logs or API responses: listing returns `SessionView` with
  `PublicID`, and revoke takes `PublicID`. This closes the token leak through
  `GET /api/v1/auth/sessions` (§2); ABO's `PublicID` is early fix 1's derived handle (§11.1).
- **Rollback (resolution A-2).** ABO's legacy store keeps HEAD's session rows through S10: raw-ULID
  `sess:` rows with HEAD's `Origin`, epoch, revoked and expiry fields (§11). An older build therefore
  lists, revokes and honours exactly the sessions this build issued, and this build does the same with
  the sessions an older build issued while rolled back, each checked on use for epoch, status,
  revocation and expiry. A rollback signs no one out and a session ended on either side stays ended, so
  no boot-time revoke step exists. Raw tokens at rest stay as at HEAD (no regression) until the identity
  hardening cutover hashes them (§11.2).
- `LastSeen` is updated at most once per minute per session: the coalescer keeps only `(publicID, at)`
  per session and flushes through `Store.TouchSession`, which changes only `LastSeen` on the current
  record and skips one that a logout or revoke deleted (DATA6-8), written with `NoSync`, so
  authentication stays a read on the hot path. ABO's rows have no last-use field, so its store stages
  nothing and `SessionView.LastSeen` is zero there, as HEAD shows none.
- Expiry sweep: from 09 S5, when `tasks` lands, core task `core.auth.sessions.prune` on
  `tasks.ScheduleFromConfig("core.auth.schedules.session_prune")` (a declared `tasks.ScheduleConfig`
  key in auth's own namespace, default every 1 h; resolution R2-7); deletes expired/revoked records.
  At S4 sessions and one-time tokens are pruned on read: an expired or revoked record is refused and
  deleted in the same batch, so S4's growth is bounded by one stage of sign-ins (COR4-13).

### 4.4 API keys

- Create: `Scopes` required and non-empty, no wildcards, each scope must be held by the actor's
  effective set (05-rbac §4.5). The native store mints an `mck_` key and keeps its canonical scopes.
- **ABO's keys keep HEAD's representation (resolution A-2).** ABO's legacy store implements
  `legacy.AuthAtomWriter` (§3.4): a key created here is an `abk_` key (HEAD's format,
  `internal/database/apikey_token.go:16-35`) whose row's `scopes` list holds the legacy atoms of
  05-rbac §6.3's write mapping, never a namespaced string and never empty. A requested set that is not a
  union of whole atom expansions (a core-only scope such as `core:metrics.read`, or part of an atom's
  expansion) is refused with 422 `no_legacy_atom` (`rbac.ErrNoLegacyAtom`); `["core:admin"]` is written
  as every atom, HEAD's admin key (05-rbac §6.3). Every ABO key is therefore a legacy key on reading
  (`APIKey.Legacy`, `rbac.CheckerInput.LegacyKey`): an empty list, which only an older build writes, is
  `LegacyFullScope` (05-rbac §4.5); on legacy surfaces an `s.perm(atom)` route, an in-handler atom check
  and an adapted legacy task definition admit the key iff the atom is in its stored atoms (or the list is
  empty) **and** the owner's roles satisfy the atom's legacy reach (`rbac.LegacyAtom`, 05-rbac §3.1,
  §6.3; SEC5-7), which on ABO is HEAD's own check; native routes read the atoms' canonical expansion.
  An older build after a rollback reads every key this build created and grants it exactly its atoms,
  and this build reads every key an older build created the same way. Scopes are fixed at creation (no
  scope-edit route), so a key's stored list keeps its atoms, or its empty list, for its whole life.
- A key-authenticated actor may create keys only for itself, with
  scopes ⊆ its own key's effective set and expiry clamped to its own key's expiry (today's clamp,
  `handlers/apikeys.go:40-60`, kept). This closes the scoped-key → full-key escalation (§2).
- Creating a key for another user requires `core:users.manage`, an interactive method (guard),
  scopes ⊆ the **target's** role permissions and ⊆ the actor's, and that the actor dominates the
  target (05-rbac §4.7 rule 5).
- **Purpose (SEC-5, 08 O9)** is a deferred-build member (§3.5): no facade key exists before
  `arrfacade` lands.
- Last-use tracking stays synchronous in the request goroutine (reason recorded at
  `middleware/auth.go:366-382`), coalesced per key per minute. It writes only `last_used_at`,
  `last_used_ip` and `use_count`, through `Store.TouchAPIKey`.
- **No key writer undoes a revocation (resolutions R6-5, A-3; SEC6-3).** A legacy key row is one JSON
  value, so even a three-field last-use write is a read-modify-write of the whole row, `status` and
  `revoked_at` included. ABO's legacy store keeps HEAD's lock discipline: every `apikey:` writer —
  `TouchAPIKey`, status and expiry writes, the post-restore restrictions, the user merge — runs under
  `apiKeyMu`, which early fix 6 extends to the two HEAD writers that lacked it (§11.1), so a revoke and
  a touch never interleave. The native store stages `Expect` on the bytes it read for every key writer
  and drops a conflicting touch, which is advisory (§3.4). Either way a request with a key can never
  undo that key's revocation.
- Display: `id`, name, first 8 chars of the hash as identifier (today, `raw/10` §7), scopes,
  expiry, last used, status; the raw value is shown once.
- **Status transitions are monotone toward revoked.** `revoked` is terminal: `SetAPIKeyStatus`
  refuses any transition out of it (409 `key_revoked`), and `needs_confirmation` can only go to
  `active` (admin re-enable, interactive, audited) or `revoked`. **On ABO's store the statuses are
  HEAD's:** `active` ↔ `"active"`, `disabled` ↔ `"inactive"`, `revoked` ↔ `"revoked"` with `revoked_at`
  set. HEAD's rows have no field that tells a restriction apart from a disable, so a
  `needs_confirmation` restriction (§4.14) is written as `"inactive"` and read back as `disabled`; the
  key's owner or an admin re-enables it as any disabled key, as at HEAD
  (`handlers/apikeys.go:336-372`). The legacy auth path refuses `"inactive"` and `"revoked"`
  (`middleware/auth.go:335-344`), so an older build never accepts a restricted key, and early fix 3
  makes a row with `revoked_at` set terminal on every build that carries it (§11.1). Admin-only
  confirmation of a restored key is native-only until the identity hardening cutover (§11.2).

### 4.5 Passwords

`auth/password` keeps argon2id m=64 MiB, t=3, p=2, 16-byte salt, PHC string, bcrypt verify-only
with transparent rehash, `oauth`/empty hash never authenticates (`absauth/password.go:40-47,131-190`).
Minimum length `core.auth.password.min_length` (default 8 — today's value; raising it is open item 2).
Hashing runs on a bounded semaphore (`runtime.NumCPU()` permits) because each verify allocates
64 MiB: an unbounded login burst would otherwise exhaust memory before the throttle trips.

### 4.6 Bootstrap and first run (G10: app-agnostic)

Core owns it; nothing refers to an app, a module or `users.manage`.

1. At every start, if `core.auth.bootstrap.enabled` (default true), `BootWrites` (P4d step 4, 06's
   write rule; COR4-13; from S4 r2 — through S4 r1 books' start hook runs HEAD's `InitBootstrapToken`,
   §5, COR6-3) mints the token through `Store.IssueOneTime` (kind bootstrap) in the store's format
   — native `mcb_…` with its hash as a one-time token; ABO HEAD's `abbs_` token with its hash and
   expiry in HEAD's settings row (§11) — writes `bootstrap-token` (mode 0600) via `os.Root` in the
   app's state dir, and logs the **path and expiry only** (never the value; today's rule,
   `bootstrap.go:165-175`).
2. `POST /api/core/v1/auth/bootstrap` (Public, `RateAuth`, per-client limiter): validate then spend
   atomically (`TakeOneTime`: in the native store `Expect` + delete in one batch over a `Serialized`
   family; in ABO's store under HEAD's `bootstrapMu`, A-3), then find or create the admin: **first active user holding `core:admin`**
   (role-resolved, 05-rbac §4.6); none → create `admin` (suffixing on collision) with role
   `admin` and a generated password returned once. Returns an API key scoped `["core:admin"]` (on
   ABO an `abk_` key holding every atom, HEAD's bootstrap key, `bootstrap.go:402`; 05-rbac §6.3),
   TTL `core.auth.bootstrap.key_ttl` (8 h default, 24 h max — today's values). The key is
   non-interactive, so `Sensitive` scopes stay out of its reach (05-rbac §4.4). Audit:
   `core.bootstrap.redeem` (05-rbac §7.1).
3. `POST /api/core/v1/auth/setup` (first admin with chosen password) is reachable only while
   `HasUsers()` is false **and** the request presents the bootstrap token, unless
   `core.auth.setup_requires_token=false` *and* the request is **local** (below). The shell's setup
   wizard asks for the token and tells the operator where the file is. (Today setup is public with
   no token, `handlers/auth.go:267-292`.)
4. **"Local" (SEC-30)** means: the TCP peer is a loopback address, **and** the request carries no
   forwarding header (`Forwarded`, `X-Forwarded-For`, `X-Real-IP`, the configured
   `core.http.client_ip_header`), **and** the peer is not a trusted proxy or tunnel connector
   (05-http-api §3.5). Behind a local reverse proxy or tunnel connector every internet request has a
   loopback peer (`peer_allowlist.go:34`), so the peer alone proves nothing.
5. Multiple apps sharing one DB is not a supported topology (D3/03 §3.7: one process per app owns
   its storage); in the multi-app-in-one-binary case there is one `auth.Service`, so one bootstrap
   token, which is what G10 asked for.
6. The optional startup read-only key (`bootstrap.go:52-131`) becomes
   `core.auth.bootstrap.readonly_key.enabled` producing a key with the scopes listed in
   `core.auth.bootstrap.readonly_key.scopes` (default `["core:system.view"]`), not "all `*.view`",
   minted by `BootWrites` at P4d step 4 like the token (COR4-13).

### 4.7 OIDC / OAuth

- `auth/oidc` provides **generic OIDC** (discovery via `github.com/coreos/go-oidc/v3` v3.21.0,
  already a dependency, `go.mod:9`; `golang.org/x/oauth2` v0.37.0) configured as a list, plus a
  `github` kind for GitHub's non-OIDC OAuth2. Google becomes one configured OIDC provider
  (issuer `accounts.google.com`).
- PKCE S256 for every provider, including GitHub (GitHub supports PKCE; today it is omitted,
  `raw/10` §2.3). Signed state codec kept (`internal/oauth/state.go:55-132`), keyed by
  `secrets.DeriveKey("mediacore/auth/oidc-state/v1")`, 10 min TTL. 05-config derives such keys from
  a derivation key that master-key rotation does not replace (SEC-28), so a rotation does not
  invalidate in-flight sign-ins.
- Linking is by `(provider, subject)`. **Linking an unseen subject to an existing account by email
  (SEC-7)** happens only when (a) the provider entry sets `link_existing_by_email: true` (default
  `false`; the key is `ProtectAdmin`, §8), (b) the IdP asserts `email_verified=true`, and (c) the
  matched account holds no `core:admin` and has no password — or the user proves that account's
  password in the same flow. Otherwise an unseen subject whose email matches an existing account is
  refused with `email_in_use` ("sign in and link from your profile"); it never takes the account.
  ABO's two migrated providers (Google, GitHub) get `link_existing_by_email: true`, which keeps
  today's behaviour for that known trust set (`internal/oauth/resolve.go:97`) under the
  admin/password guard. The verified-email displacement rule (`resolve.go:177-205`) is kept under
  the same guard and audited (`core.identity.displace`).
- Admission: provider allowlist of emails/domains, plus invite grants (ABO's invite allow-list rows today, §11) via the
  `ExtraAllowed` hook (`internal/oauth/oauth.go:63-68`); new users get the provider's
  `default_role` (must exist and be assignable without escalation: it may not contain
  `core:admin`, and the key is `ProtectAdmin`).
- Ends in `IssueSession(origin = oidc:<provider>)` → `MethodSession` (interactive).
- Routes: `GET /api/core/v1/auth/providers` (Public), `GET …/auth/oidc/{provider}/start`,
  `GET …/auth/oidc/{provider}/callback` (Public, `RateAuth`).

### 4.8 Owner assurance and optional identity extensions (G11)

- `RequireOwner` (`internal/auth/owner.go:44-108`) becomes the route guard `Guards.Owner`: the
  principal must carry `AssuranceOwner`, which only an `OwnerVerifier` authenticator can set.
  core ships none; ABO's `internal/cfaccess` is the one in use; deployments without one have no owner assurance, so
  owner-guarded routes (user merge, factory reset and HEAD's store reset `POST /system/reset`, restore while an owner is set — 9 routes today,
  `raw/10` §2.4) fall back to `Guards.Interactive` + `core:admin` **only if**
  `core.auth.owner.required=false`. Its default is true exactly when an OwnerVerifier is configured
  **and** an owner identity is set (`Extension.OwnerSet`), else false — HEAD gates on "owner set"
  (`ownerGateWhenOwnerSet`, `wire_system_routes.go:44-46,65`). This keeps today's behaviour where
  `owner_email` unset means no owner gate, also when Access is configured without an owner, where
  nobody can hold `AssuranceOwner` and restore, factory reset and user merge would otherwise be
  unreachable (SEC4-14).
- **Owner trust root (SEC3-6).** HEAD makes five fields owner-only once an owner exists, "whatever
  the caller's auth method" (`ownerTrustRootFields`, `internal/config/protected_fields.go:209-227`:
  `owner_email`, `cf_access_team_domain`, `cf_access_aud`, `enable_auth`, `oauth_allowed_emails`),
  because a second admin or a stolen admin session could otherwise repoint Access at a team it
  controls and mint the owner's assertion. Their successors are all `ProtectOwner` (05-config): books'
  `modules.books.identity.{owner_email, cf_access_team_domain, cf_access_aud}`, `core.auth.mode`, the
  `allowed_emails` and `allowed_domains` of every `core.auth.oidc.providers[]` entry — plus
  `core.auth.owner.required`, which would otherwise let a non-owner admin drop the owner gate of
  restore, factory reset and user merge. The protection is active while an `OwnerVerifier` extension
  is configured and an owner identity is set (HEAD's "once `owner_email` is set") — the same
  derivation as the gate's default above (SEC4-14); otherwise these keys are `ProtectAdmin` (§8).
- Core holds no Cloudflare code. ABO's `internal/cfaccess` holds verification and owner proof (04 §2.4); Cloudflare **policy editing** (`internal/cfaccesspolicy`)
  and **guest service tokens** (`internal/cfservicetoken`, `internal/server/guest_service_tokens.go`)
  are deployment features of ABO and stay in the app (`books` module or an ABO-only `deploy`
  package), using `auth.Service` APIs; they are not core.
- `internal/mtls` (CA/PSK provisioning, stdio bridge) and `cmd/mtls-bridge` stay in ABO: they
  bridge an MCP protocol between hosts and have nothing to do with HTTP identity
  (`cmd/mtls-bridge/main.go:1-25`). Core ships no client-certificate authenticator (SIMP-23).

### 4.9 Throttling and lockout (G14)

- **No hard account lockout** (it is a denial-of-service lever against known usernames). Instead,
  kept from `absauth/throttle.go:50-153`: per-source failure counter (15 failures / 15 min → 429 for
  the window), per-account soft delay after 5 failures (200 ms steps, cap 2 s), aggregate ceiling
  across all sources (`login_ceiling.go`), LRU of 10,000 keys with pinning of saturated entries.
- One `auth.Throttle` instance per `Service` (not package state, R5) applied to: password login,
  bootstrap redeem, setup, invite lookup/accept, temp-login consume, OIDC callback.
- Client address comes from `httpapi` (05-http-api §3.5): trusted proxies are prefixes **or
  zoned addresses**, `TUNNEL_CONNECTOR_ADDRS` is aliased onto them, and the client-IP header is
  believed only from them (SEC-18; zone semantics of `client_trust.go:133-201` kept). Without that
  migration every tunnelled request would share the connector's address, and one attacker's 15
  failures would throttle every user. An extension authenticator (ABO's `internal/cfaccess`) can mark the CDN header as believable for
  verified requests (today's rule, `middleware/cfaccess.go:24-41`).
- State is in-memory: a restart resets counters. Documented as core behaviour (single process per
  storage); persisting counters would turn every failed login into a durable write.

### 4.10 Compatibility identities (G15)

App protocol auth stays in the app. ABO's ABS surface keeps its HS256 JWTs, refresh derivation and
`abs_sess:` sessions inside `books` (a legacy key claim, 05-storage). Its routes live in the
`books` legacy mount and are marked **per route** `OwnAuth` in the legacy manifest (05-http-api
§8.2, SEC-14), exactly the routes whose gin chain runs `ABSRequireAuth` today; `/ping`, `/status`,
`/login` and the other ABS paths without it keep core's authentication and the Basic perimeter
(`basicauth.go:51-70`). Every ABS credential resolves through
`auth.Service.PrincipalFor(ctx, user, "books.abs", scopes, epoch)`, so status, credential epoch,
role resolution and scope intersection are core's — replacing the duplicate role walk
`absEffectivePermissions` (`middleware/absauth.go:778-800`). ABS's use of `abk_` keys calls the core
API-key authenticator directly, and that direct path applies **every** stage 12 rule — key status
(`revoked`, `disabled`, `needs_confirmation` refused), expiry, and (once it exists) purpose — not a
subset (SEC2-4). Method `books.abs` is registered by the books module with
`Registrar.AuthMethod` as non-interactive, `MayChangeCredentials=false` (today's `MethodABS`
semantics; the kernel refuses anything else, §3.3).

**Confinement on the ABS surface (SEC2-4).** Today the ABS group refuses a worker-only credential
explicitly (`middleware/absauth.go:629-632`). Stage 13 never runs on `OwnAuth` routes, so after
`PrincipalFor` the ABS adapter calls `CheckCredentialFor(p, route.Perm)`, which refuses (ABS JSON
403) a `Confined()` principal unless the route's manifest `Perm` names one of its scopes
(05-http-api §8.3). An `fp-worker` key or a worker user's ABS login therefore reaches no ABS route.

The *arr facade's key purpose and `X-Api-Key`/`apikey` extraction are deferred-build members (§3.5).

### 4.11 Auth-off mode

`core.auth.mode = required | disabled` (default `required`; ABO's `enable_auth` aliases it).

- `disabled`: stage 12 binds a synthetic principal `{Method: auth_disabled, Perms:
  authz.AuthDisabled(catalog)}` and ignores presented credentials (§4.2 step 0). That checker holds
  every concrete scope that is neither `AuthRequired` nor `Sensitive`, has **no admin flag** and no
  implication from `core:admin` (05-rbac §4.11, SEC-2). `core:admin` itself is `AuthRequired`, so
  the always-authenticated class — store wipes, debug edits, credential surfaces
  (`wire_admin_role.go:28-38`) — answers 401 with auth disabled, as today; read-only admin
  introspection stays open, as today's `adminRole` pass-through (05-rbac §6.2).
- `MethodAuthDisabled` is non-interactive and may not change credentials, so every
  `Guards.Interactive` route (password, keys, sessions of others, temp-login, backup restore) is
  refused. **This is a deliberate auth-off tightening, not parity (SEC5-8):** HEAD's credential guard
  is a pass-through with auth off (`internal/server/credential_routes.go:79-82`), so those routes are
  open there. Credential surfaces should not be open to a LAN without auth; 09 §9.2.6 records the
  change with release-note text from S4, and the auth-off half of `TestLegacyRoleReachUnchanged`
  excludes these routes and asserts their refusal. Legacy `s.perm` routes and adapted legacy task
  definitions keep HEAD's auth-off reach, and in-handler atom checks keep HEAD's auth-off denial
  (05-rbac §4.11).
- `disabled` refuses to start when a listener is bound to a non-loopback address **or** any trusted
  proxy or tunnel connector is configured (a local proxy makes remote callers look local, SEC-30),
  unless `core.auth.disabled_allow_remote=true` (today: a warning only, `server_lifecycle.go:1847`).
  An app sets that key's default through `auth.Options.DefaultDisabledAllowRemote` (`app.Options.Auth`, 06 §6.4.5), as it does the cookie name: new apps keep
  `false`; **ABO's default is `true`** (SEC4-8), because a Docker container always listens on a
  non-loopback address and every ABO install running `enable_auth=false` (Basic-only behind the compose
  `BASIC_AUTH_*`, or LAN-only) would otherwise crash-loop at S4 r2. While `disabled` runs
  remote-reachable, health reports `degraded` (`auth_disabled_remote`) with a banner, as HEAD's warning
  did; the release note states it and 09 §9.2.6 records it (`TestS4_AuthOffDockerBoots`, image job).
- **Turning auth on with auth off: HEAD's behaviour on ABO (SEC4-8, resolution A-2).** The auth switch
  keeps HEAD's precedence on ABO: a stored `enable_auth` beats the legacy `ENABLE_AUTH` (05-config §1.12.4's
  `BelowStored`), in both directions, as at HEAD (`internal/config/config.go:2435`,
  `persistence.go:915`). A stored `disabled` is common: every install that ever saved settings while
  running with `ENABLE_AUTH=false` holds `enable_auth=false` in its blob, because HEAD's save marshals the
  whole effective config (`internal/config/persistence.go:1932-1977`). Two ways turn auth on:
  - **the settings page and API, which write the stored field, exactly as at HEAD.** HEAD's rule is
    `if !prior.EnableAuth || auth.MethodFromContext(ctx).MayChangeCredentials() { return true }`
    (`internal/config/update_service.go:781` @748fd787a): while auth is off, every protected field may
    be changed, after HEAD's owner-trust-root check, which is not skipped with auth off
    (`:772-779`). On ABO, legacy-backed through S10, the auth-disabled principal may therefore store
    `enable_auth: true` (`core.auth.mode` = `required`) through the settings page or the `/api/v1/config`
    alias under that same rule; it is written to the legacy `enable_auth` field in place, so every build
    reads it after a rollback. SEC4-8's `ProtectAdmin` refusal of the auth-disabled principal applies to
    native apps only (05-config owns the check; `TestAuthEnable_MatchesHEAD`);
  - **the canonical `<APP>_CORE_AUTH_MODE=required`**, an operator-layer value that locks the key
    (05-config §1.3.7). No build before S4 r1 reads it, and nothing writes it back into the blob, so
    after a rollback below S4 r1 auth follows the stored `enable_auth`. The release note says: "To turn
    authentication on so that it stays on if you roll back, use the settings page; `ABO_CORE_AUTH_MODE`
    is not read by releases before S4." 09's canonical-only row carries this cost.
  An operator value that disables auth keeps its ordinary precedence, so a stale `ENABLE_AUTH=false` left
  in an environment can never turn off auth that the UI turned on. When a `BelowStored` legacy name of a
  key at `ProtectAdmin` or higher is set and differs from the effective value, the boot logs at warn and
  raises the health token `config_legacy_name_shadowed` naming the key and its canonical variable
  (05-config owns the check). 09 §9.2.6 records that ABO keeps HEAD's auth-enable behaviour through S10.
  The hardening of this precedence is the auth-precedence cutover (§11.2).
- **Health tokens before S8 (SEC7-12).** `auth_disabled_remote` and `config_legacy_name_shadowed` exist
  from S4 r2, before core health's component snapshot (S8); until then they are carried, beside
  `basic_gate_no_password`, by HEAD's degraded token list
  (`internal/server/handlers/system/handler.go:222-275`), as 05-health states.
- Audit records in disabled mode carry `method=auth_disabled` and the client address.

### 4.12 Credential epochs and revocation

- `User.CredentialEpoch` bumps on password change/reset, disable, enable, "sign out everywhere"
  and restore (§4.14). Sessions, temp-login and invite tokens carry the epoch at issue; mismatch →
  refused and deleted (`middleware/auth.go:219-234` semantics kept). Self-service password change
  re-stamps the caller's own session in the same batch (today's `keepSessionID`).
- API keys are **not** epoch-bound (automation survives a password change, as today) but die on
  disable.
- Role or role-definition changes do not bump epochs; permissions are recomputed from an `rbac`
  generation counter (05-rbac §9), so changes apply on the next request.
- Every revocation publishes `core.principal.invalidated.v1{user_id, credential_id?, reason}` on
  `events`; `realtime` closes affected streams (05-realtime §4.6), `Revalidate` uses it.
- Every restriction is first recorded with `Deps.Revocations.Append(ctx, b, journal.Revocation{At, Kind,
  Subject, Object, Detail, Epoch})` — `b` is the revoking batch, into which `Append` stages
  `c/backup/journal_head` — **before** that batch commits. Auth's kinds: `apikey.revoked`,
  `apikey.disabled` (any status change away from `active`, `needs_confirmation` included),
  `user.disabled`, `user.deleted`, `credential_epoch.bumped`, `session.revoked`, `password.changed`
  (password change or reset; SEC2-11, DATA2-7's `credential.changed`) and `identity.unlinked`
  (`Detail` = provider); `Subject` is `entity.Ref{core, user|apikey|session, <id>}`. If `Append`
  fails, the revocation still commits and `Revocations.Gap(b, at)` is staged into the same batch
  (05-backup §5B.3.6), so a restore over that interval treats the journal as incomplete. (`Object`
  and `Detail` are 05-backup's SEC2-10 additions; role kinds are 05-rbac §7.1's.)
- **Hook lines name their restore (DATA8-7).** A line the post-restore hook (§4.14) appends carries the
  seq of its restore's `restore` line in the field `Revocation.RestoreSeq` (JSON `restore_seq`, 05-backup
  §5B.3.6); its `Detail` keeps only the kind-specific elements. The
  hooks commit in several batches (auth, rbac, audit), so a later hook's failure that swaps back leaves
  auth's committed lines between the `restore` line and its `restore_aborted` line. 05-backup's boot
  re-apply verifies such lines but never re-applies them, and the exact span comes from `restore_seq`.
  So the original store's next boot bumps no epoch and sets no key `needs_confirmation`
  (`TestRestoreFailureSwapsBack`, 05-backup).
- **One stored copy (R4-1).** A revocation — interactive, by task, by journal re-application or by
  the post-restore hook (§4.14) — goes through the `Store` in the revoking batch. In ABO's legacy store
  it writes the legacy row itself (`status`, `revoked_at`, `credential_epoch`, `password_hash`, the role
  list) and nothing else. There is no second copy that a boot could rebuild over it, and every build
  that can open the store reads the same field, so the restriction holds after a restart and after a
  rollback alike. A session revocation deletes the store's session row, which every build reads (§4.3).

### 4.13 Core auth routes (all under `/api/core/v1`)

Registered by `core/internal/authhttp` (L6, imported only by `kernel`, 03 §3.3); `auth` itself does
not import `httpapi`. "Dominance" = 05-rbac §4.7 rule 5, enforced in `auth.Service` (SEC-4). No core
route carries a one-time token in its path; tokens travel in the request body (SEC-21). Links:
temp-login links are `/login/temp#token=…` from 09 S4; invite links keep the `/invite/<token>` path
form until the shell stage (09 S9), because HEAD's SPA opens only that form
(`web/src/App.tsx:201-206`; FE4-2), and are `/invite#token=…` from S9. The form is the compile-time
`auth.Options.InviteLinkForm` — `InviteLinkFragment` (the default) or `InviteLinkPath` — which ABO sets
to `InviteLinkPath` from S4 and drops in the S9 PR that adopts the shell. **The reader ships before
the writer (FE5-4):** the S4 build's legacy SPA already reads the fragment form too — its invite guard
accepts `pathname === '/invite'` as well as `/invite/…`, and an `/invite` route prefers `#token=` and
removes it with `history.replaceState` (07 §7.4.3) — so an invite minted on S9 (a HEAD invite row on
ABO, valid on every build) opens after an S9 → S8 rollback; 09 §9.2.1's cost table
carries the row, and `TestLegacyInviteLink_EndToEnd` and the R-test row assert it. A fragment never
reaches a server; a path-form invite link reaches it only as the page request, as at HEAD, redacted
from the access log and spans by 05-logging's `log.DefaultPathRules()` (`/invite/{token}`), and the
page posts the token in a body.

| Method + path | Perm | Guards |
|---|---|---|
| `GET /auth/status` | Public | — |
| `POST /auth/setup` | Public (zero users + token or local, §4.6) | `RateAuth` |
| `POST /auth/bootstrap` | Public | `RateAuth` |
| `POST /auth/login` / `POST /auth/logout` | Public / AnyAuthenticated | `RateAuth` |
| `GET /auth/providers`, `GET /auth/oidc/{provider}/start`, `…/callback` | Public | `RateAuth` |
| `GET /me`, `PATCH /me` | `core:self` / `core:self` | `PATCH`: Interactive |
| `PUT /me/password` (self only: `{current, new}`; no target-user field) | `core:self` | Interactive |
| `GET /me/sessions`, `DELETE /me/sessions/{publicID}` | `core:self` | `DELETE`: Interactive |
| `GET/POST /me/api-keys`, `PATCH/DELETE /me/api-keys/{id}` | `core:self` | key may manage only keys it may create (§4.4) |
| `GET /users`, `GET /users/{id}` | `core:users.view` | — |
| `POST /users/invites`, `DELETE /users/invites/{id}` | `core:users.manage` | Interactive; invite role via `CanAssign` |
| `POST /users/{id}/disable`, `…/enable`, `DELETE /users/{id}`, `POST /users/{id}/reset-password`, `PUT /users/{id}/roles` | `core:users.manage` | Interactive; dominance |
| `GET /users/{id}/sessions`, `DELETE …/{publicID}`, `GET/POST /users/{id}/api-keys` | `core:users.manage` | Interactive for writes; dominance |
| `POST /users/{id}/temp-login` | `core:users.manage` | Interactive; dominance |
| `POST /auth/invites/lookup` `{token}`, `POST /auth/invites/accept` `{token, …}` | Public | `RateAuth` |
| `POST /auth/temp-login/consume` `{token}` (sets cookie) | Public | `RateAuth` |

The legacy `PUT /api/v1/auth/me/password` stays **one gin route** of the legacy mount, with and
without a `user_id` body (COR3-12): one ServeMux pattern cannot be both an alias and a gin route
(`ErrDuplicateRoute`), and an alias cannot hand the `user_id` case (sets another user's password,
`auth_reset_password.go:48-50`) to gin (05-http-api §4.6, SEC2-5). From 09 S4 the handler calls the
service for both cases — `ChangeOwnPassword` without `user_id`, the dominance-checked set-password
path with it; the core API has no "set another user's password" route — an admin uses the reset
link.

**Invites are re-checked on accept (SEC3-2).** An invite row carries `RoleID` and `CreatedByUserID`
(`handlers/user.go:155-197,239`), and on ABO it may have been created by an older build (before S4 or
during a rollback), which runs no `CanAssign`. Accepting any invite on this build re-runs
`rbac.CanAssign` for the creator over `RoleID` and refuses (`invite_invalid`, audited `core.invite.accept` with `Outcome: denied`) when the check fails or the creator no
longer exists, so an invite into a role its creator could not assign natively never yields an
account.

**Legacy links (SEC2-6, FE2-8, FE3-5, FE4-2).** Legacy `GET /auth/temp-login?token=` is answered from
S4 by a server-side `302` to `/login/temp#token=<token>` with `Referrer-Policy: no-referrer` (`token`
is in 05-logging's `log.DefaultRedactQuery()`). Invite links: from S4 to S8 `GET /invite/<token>` is
the legacy SPA's page, served by the legacy `/` fallback as at HEAD; from S9, when the shell owns `/`,
books' manifest adds `GET /invite/{token}` (Public, `RateAuth`) answering a `302` to
`/invite#token=<token>` with `no-referrer`, for path-form links issued before the shell. Both
302s are books **legacy manifest routes** for their windows (09 §9.6 rows for `/auth/temp-login`,
S4 + max(W, temp-login TTL), and `/invite/{token}`, S9 + max(W, invite TTL)); `invite` joins `login`
in ABO's `LegacyMount.SPAShared` at S9, so `GET /invite` stays the shell page while the more specific
pattern answers the redirect (05-http-api §3.5, §8.2); `ui.Handler` implements no redirect. Between 09
S4 and S9 ABO's pre-shell SPA serves the UI: the same S4 build that switches invite creation to core
keeps `AcceptInvite` on its `/invite/:token` route — rendered for signed-in and signed-out visitors
alike — reading the path parameter and calling `POST /api/core/v1/auth/invites/lookup|accept` with the
token in the body, adds the `/invite` fragment reader above (FE5-4), and adds a `/login/temp` route to the legacy SPA's unauthenticated **and**
authenticated branches that reads `#token=` and calls `POST /api/core/v1/auth/temp-login/consume`
(today the unauthenticated branch sends `*` to `/login`, which drops the fragment,
`web/src/App.tsx:249-250`). `TestLegacyTempLoginLink_EndToEnd` and `TestLegacyInviteLink_EndToEnd`
(an invite created on S4 r2, opened signed out, accepted, signed in; opened signed in, the invite page
and not `NotFound`) are in 09 S4's done-check. 09 S4's R-test cell reads "an invite link created on cur
opens in cur's UI; prev-release UI tabs (cell a) still open a legacy `/invite/<token>` link" — on
ABO an invite created on cur is a HEAD invite row, valid on every prev (§11); the S9 R-test row "invite minted on S9 (`/invite#token=`),
roll back to S8, open signed out → invite page, accepted" asserts the reader-before-writer order
(FE5-4, 07 §7.4.3).

**Invite delivery (SIMP2-4, R-F).** Invite e-mail uses a `mail.Sender`. Until 09 S7 it is built from
the legacy SMTP settings, as today; from S7 it is built from the `notify/email` integration instance
(05-notify, 05-metadata), so auth never depends on `notify` before notify exists.

### 4.14 Post-restore hook (SEC-16)

`PostRestore` is auth's `journal.RestoreHook`. The kernel runs it in P4d's post-restore step (06
§6.5.1), only on a boot that applied a restore or found a replaced store (below), in the order of
COR3-2 and COR5-2: **after** the config store's legacy `Open` (`legacy.ConfigOpener`, 05-config §1.3.3), `secrets.Open` and `Store.Attach` (05-config; COR6-10) (so
journal lines are verified with the keyring and the journal is read from the effective
`core.backup.dir`) and after the
journal tail was re-applied; before any service is built and before listeners open; it fails boot on
error. It writes through the `Store` in its one stored format (R4-1): there is no mirror to stage
through and no confirming rebuild.

1. **Re-apply** every `journal.Revocation` in `rc.Revocations` — the entries after `rc.Boundary`,
   the **minimum** of the source's recorded journal seq (the manifest's `JournalSeq`, the reverted
   restore's `staged` record or the checkpoint's `journal_seq`) and the swapped-in store's own
   `journal_head.seq` (05-backup §5B.3.6 "Coverage at restore"; clock-free, DATA2-7): key
   revocations and disables, user disables and deletes, epoch bumps, session revocations (role kinds
   are 05-rbac's hook). For each `password.changed` user, blank the restored `password_hash` (an empty
   hash never authenticates in any build, `absauth/password.go:131-190`) and set
   `PasswordResetRequired` (a native store's field; ABO's store derives it from the blanked hash, §3.1,
   §11), which only selects the 401 code (it may be the pre-compromise password,
   SEC2-11); for each `identity.unlinked` entry, delete the re-appeared `(provider, subject)` link.
2. **Invalidate restored sign-ins.** Delete every session and every one-time token the store holds
   (invite, temp-login, bootstrap), in whatever representation (`Store.RevokeSignInsBefore` with zero
   time): on ABO HEAD's `sess:` and `invite:` rows, so neither this build nor an older build after a
   rollback honours one. Bump every user's `CredentialEpoch` (HEAD's own `credential_epoch` field on
   ABO). Everyone signs in again once.
3. **Pre-swap restrictions win whenever there is a pre-swap store (SEC5-10, SEC6-4).** When
   `RestoreDeps.Previous` is non-nil (a staged restore: the pre-swap store holds every restriction,
   journaled or not — an older build's interval, an `Append` that failed, an interval foreign-open
   detection missed), the hook takes the more restrictive of the restored and the pre-swap state,
   **whatever `rc.Revocations` holds**: a key revoked, disabled or `needs_confirmation` in the pre-swap
   store keeps the most restrictive of its two statuses (`revoked` > `needs_confirmation` > `disabled` >
   `active`; a key revoked there is revoked, not `needs_confirmation`; SEC7-8); a user disabled or
   deleted there is disabled; a user whose pre-swap
   `password_hash` differs from the restored one gets the step-1 treatment (hash blanked,
   `PasswordResetRequired`); an external-identity link absent there is deleted. With full coverage this
   equals step 1 for journaled kinds, so it costs nothing on the common path. Roles are 05-rbac §7.1's
   hook, which applies the same rule. **Then, when `rc.Revocations` is nil** (the journal is
   unavailable, broken or does not cover the interval: a foreign, host-loss or replaced-store restore —
   never a staged restore's own swap, which appends no `gap` line and keeps its coverage, resolution
   R6-4, DATA6-2), set every restored API key whose status is still `active` or `disabled` to
   `needs_confirmation`: refused until an admin re-enables it from the keys page. A key restored as
   `revoked` stays revoked — terminal, never re-enableable (DATA4-13). **On ABO's store (A-2) the
   restriction is HEAD's own `"inactive"` status** (§4.4), never `"revoked"`, which early fix 3 makes
   terminal: every build refuses the key, its owner or an admin re-enables it like any disabled key,
   and its atoms are untouched, so a re-enabled key keeps exactly its pre-restore reach (an empty list
   stays `LegacyFullScope`; 05-rbac §4.5). 05-backup's restore record states the same choice.
4. Return the counts per family; audit's restore hook records them as `core.restore.revoke`
   (05-rbac §7.1 action names).

**Restricted last admin (SEC3-15, SEC4-12).** When rbac's hook keeps the last active `core:admin`
holder's role (05-rbac §7.1), this hook's restriction of that account is: password hash blanked with
`password_reset_required`, API keys `needs_confirmation` (`"inactive"` on ABO), on a native store
`SignInRestricted` set (every authenticator, OIDC and identity extensions included, refuses it until an
owner-verified reset link or the offline `<app> users reset-password` clears it), and its
external-identity links deleted (journaled `identity.unlinked`), so no build can sign it in through
OAuth. The residual — Cloudflare Access admitting it, which resolves users by allow-listed e-mail — is
named in the restore report; on ABO it applies to this build too, because HEAD's rows cannot hold
`SignInRestricted`, until the identity hardening cutover (§11.2). **The offline command is not a foreign open (resolution R7-4, DATA7-11).**
`<app> users reset-password` opens the store through `kernel.OpenOffline` (05-storage §5S.4 "Offline
open"): the read-only checks of P2 and P3 (storage's steps 1–4: the guard, the floor and the claims),
refusing on `CheckReader`'s swap tokens and on pending P4 migrations; it writes the user row through
`auth.Store`. It computes the boot verdicts as a serve boot does and, at a clean close, stages the owed
entries a serve boot would stage before it promotes its own Pebble open stamp and sidecar identity
(05-storage §5S.4 owns the list; resolution R8-2). So the command's **own** open is never read as
foreign, and an earlier foreign open is never erased by it.

**Replaced store, and restores by an older build (DATA3-8, SEC3-3, SEC3-10, DATA4-7, SEC4-6).** An
older build (after a rollback) restores with its own code, which neither journals nor runs hooks.
**Which store is "replaced" is 05-backup §5B.3.6 step (2)'s classification, and only that** (DATA6-5):
it decides from store identity and content evidence, not from journal-head lag (a concurrent-commit
head regression or a crash after `Append` also leaves the head behind) — the content marker
(`c/backup/served_boot` against `backup.location.json`'s pair, SEC5-6), the journal's committed end
anchored outside the store (`backup.location.json`'s `head`: a store whose `head_before` is below it is
replaced, whatever its marker, which also catches an in-place restore of an archive the last journaling
boot itself made, DATA6-4) and the lineage verdict; 05-backup (2) is the one classifier. A store that a staged
restore or a relocation moved (another generation or directory, with the evidence above saying "same
content") is **not** replaced and runs no hook (DATA5-7). A restore whose hook fails swaps back and
leaves the original store classified "same store" on the next boot: 05-backup writes `served_boot`,
the location pair and the `restore` line's evidence only after the hooks commit, and an aborted
restore's line is neither resumed nor evidence (DATA6-3). The replaced-store test and the re-apply read
the store's head before any line is appended on this boot (SEC4-5).

- Same store, head behind the journal's last revocation line: the verified tail is re-applied
  (narrowing, idempotent) and **no hook runs**.
- Replaced per 05-backup (2), with no staged restore (an older build's restore, a file copy, a
  checkpoint copied by hand): the kernel re-applies the verified tail first, so every known revocation
  holds, and then runs this hook with `KindExternal` and **`rc.Revocations == nil`** — steps 2 and 3:
  restored sessions and invites deleted, every epoch bumped, every active or disabled key
  `needs_confirmation` (on ABO HEAD's `"inactive"`). The verified tail is not passed as coverage even when it verifies: the journal
  lives in the backup directory, whose end only the pre-swap store's head anchored, and a journal
  truncated after the archive's boundary verifies cleanly (SEC4-6; 05-backup §5B.3.6, 14 K22).
- Chain break (a missing, edited or unverifiable line) with the head behind: the same fallback, and
  health reports `revocation_journal_broken`: a store replaced outside a staged restore fails closed
  (SEC3-10).

What an older build itself allows while it serves (a pre-S4 build re-activating a revoked key,
honouring restored sessions) is that build's behaviour; the early fixes of §11.1 close the worst of
it in the legacy code.

## 5. Lifecycle

1. **P4c** builds the `Store`: the app's legacy store from `module.Manifest.LegacyStores.Auth` when given
   (ABO), else the native `core/internal/authstore`. A build whose choice differs from the claim
   ledger's `backends` record fails boot with `ErrLegacyStoreMissing` (05-storage); because books
   supplies its store in every S4 release, a rollback from S4 r2 to r1 never meets a missing grant
   (DATA4-2). A store constructor writes nothing (DATA4-10). Its capabilities (`legacy.AuthReconciler`,
   `legacy.AuthAtomWriter`, §3.4) are detected only by `auth`, through its
   unexported mirrors over the store built here; the kernel asserts only the capabilities 03 §3.3 lists
   (SIMP7-5, SIMP8-3, §3.4).
2. **P4d, post-DAG reconcilers** (06 §6.5.1 is the order; COR3-2), from 09 S4 r2: step 1 is
   the config store's legacy `Open` (`legacy.ConfigOpener`: ABO's key guard and encryption init), then
   `secrets.Open(…, Secrets())` (`legacy.ConfigSecrets`) (keyring, `k0-legacy` capture), then the override store's `SetSecrets` and `Attach` (`Load`, read-only)
   (R3-1, R4-6, COR5-2); then 05-backup's
   revocation-journal step, whose auth kinds write through auth's Store (the replaced-store test and
   re-apply read the head before this boot appends a `gap` or `restore` line, SEC4-5); then, on a boot
   that applied a restore or found a replaced store, `auth.PostRestore` (§4.14); then
   `auth.BootWrites` — seed `_system`, mint the bootstrap token and the optional read-only key (§4.6),
   and run the store's `legacy.AuthReconciler` when it has one (ABO: HEAD's open-time identity writers,
   §11) — moved out of P5 so P5 writes nothing (DATA3-18, COR4-13). There is no tasks step and no
   identity rebuild.
   **At S4 r1 (dark, COR4-4)** auth's part of P4d only runs, as step 1c (06 §6.5.1, COR5-15), the read-only decode census over the
   Store's scans (config's step 1 runs as on every core build) (records reported `ErrUndecodableRecord` are counted and listed for the probe P1;
   nothing is written by core); the journal re-apply's auth kinds, `PostRestore` and `BootWrites` are
   skipped and logged, gin still performs every revocation without journaling, and r1 therefore does
   not list the `journal` duty (05-backup), so its interval becomes a `gap` line at the first r2 boot.
   **HEAD's Start-time identity writers keep their home through r1 (COR6-3).** HEAD's `Server.Start`
   runs `seedRolesAndTokens` on every boot (`internal/server/server_lifecycle.go:355,1098-1140`
   @748fd787a): `auth.SeedRoles` (the four seed roles), `auth.SeedSystemUser` (`_system`),
   `stampNeverExpiringAPIKeys`, `InitBootstrapToken` (a new 10-minute token file and hash on every
   start) and the optional start-up read-only key. Their successors (P7's seed-role reconcile, 05-rbac
   §4.8, and `BootWrites`) are skipped at r1, so through S4 r1 books keeps that whole sequence in its
   start hook (06 Run step 2) over books' own code — exactly as for `ReconcileVerifiedEmails` (COR5-7)
   — and the S4 r2 flip PR deletes it in the change that turns on P7 seeding and `BootWrites`. The
   `seed.go` and `bootstrap.go` moves into core (§11, 04 S4) land with that r2 PR, so r1 still has
   HEAD's code to call. Without this, a fresh store booted on r1 would have no seed roles (gin's HEAD
   authorizer, `internal/server/middleware/auth.go:430-455`, would grant the first admin nothing), and
   an r1 restart would mint no bootstrap token, breaking the bootstrap runbook once the previous token's
   10 minutes passed. `TestDarkRelease_HEADSeedWritersKeepRunning` is the r1 done-check.
3. **P5**: `rbac.Service` first (catalog frozen after module registration), then `auth.New` (loads
   `HasUsers`; writes nothing), then the extensions from `app.Options.Authenticators` (§3.3).
4. **P7**: the module compat methods recorded in P6 (`Registrar.AuthMethod`) are handed to `auth`,
   which validates them (`ErrMethodSpec`, §3.3; COR4-13).
5. From S4 r2 `kernel` registers the core auth routes through `internal/authhttp` on `CoreGroup`. From
   09 S5, when `tasks` lands, it registers `core.auth.sessions.prune` and `core.auth.onetime.prune` on
   `tasks.ScheduleFromConfig("core.auth.schedules.session_prune")` and
   `ScheduleFromConfig("core.auth.schedules.onetime_prune")` (declared `tasks.ScheduleConfig` keys,
   default every 1 h; resolution R2-7); at S4 expired records are pruned on read (§4.3, COR4-13).
6. Run: middleware serves; throttle sweeper and `LastSeen` flusher goroutines (one each) run until
   the app context ends; the flusher drains pending `LastSeen` writes on shutdown (bounded 2 s).

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| No credential | anonymous; route decides (401 `authentication_required` if non-Public) | closed | |
| Presented Bearer/API-key credential invalid/expired/revoked | `Failure` recorded; 401 with `code` (`invalid_credential`, `expired`, `revoked`) on every route; no fall-through to a weaker credential | closed | today's key-wins rule, generalized (SEC-12) |
| Only the session cookie presented, and it fails | Public route: anonymous + cookie cleared; non-Public: 401 with the code + cookie cleared | closed | a stale or sibling cookie must not block login (SEC-11, FE-6) |
| Store read error during authentication | 503 `auth_unavailable` | closed | never degrade to anonymous-with-access |
| `HasUsers` false | only Public + setup/bootstrap | closed | today it is fully open (§4.2) |
| User inactive / epoch stale | 401 `inactive_user` / `expired` (session deleted) | closed | |
| Extension authenticator internal error (JWKS fetch fails) | treat as `ErrNoCredential` + warn + `mediacore_auth_extension_errors_total`; other authenticators still run | open (to the next method) | an outage of an *optional* IdP must not lock out password/key users; a presented assertion that *verifies false* is still 401 |
| Throttle tripped | 429 `throttled` + `Retry-After` | closed | |
| Password hash semaphore saturated (> 2 s wait) | 503 `busy` + `Retry-After` | closed | memory protection |
| Login with wrong password (Public route) | 401 `invalid_credentials` | closed | a Public auth flow's 401 is shown in place, not a session reset (FE-7, 05-http-api §4.3) |
| Password login for a user with `PasswordResetRequired` | 401 `password_reset_required` (shown in place) | closed | the restored hash may be a replaced password (SEC2-11) |
| Status change on a revoked key | 409 `key_revoked` | closed | revocation is monotone; the legacy code refuses it too once early fix 3 ships (§11.1) |
| Compat method that is interactive, may change credentials, verifies owners or is outside `<moduleID>.` | boot error `ErrMethodSpec` | closed | only app-level extensions carry those rights (SEC2-16) |
| Confined principal on an `OwnAuth` route not naming its scope | ABS 403 (`ErrConfined`) | closed | SEC2-4 |
| Stored identity record cannot be decoded by the store (unknown status, malformed JSON; `ErrUndecodableRecord`) | the record is treated as restricted (user inactive, key refused) and counted; boot continues | closed | fail closed on data an older build wrote |
| Store backend differs from the claim ledger's `backends` record | boot error `ErrLegacyStoreMissing` (05-storage) | closed | a build without the app's legacy store must not serve its store (R4-1) |
| User with `SignInRestricted` | every authenticator refuses: 401 `password_reset_required` | closed | restricted last admin (SEC4-12) |
| Legacy invite whose creator no longer may assign its role | `invite_invalid`, audited | closed | SEC3-2 |
| Replaced store, as 05-backup §5B.3.6 (2) classifies it (content marker, out-of-store journal head, lineage), verified tail or not; a relocated store is not replaced and runs no hook | tail re-applied, then the hook with `rc.Revocations == nil`: every session and invite deleted, every active or disabled key `needs_confirmation` (on ABO HEAD's `"inactive"`), every epoch bumped; a chain break adds health `revocation_journal_broken` | closed | SEC3-10, SEC4-6, SEC5-6, DATA6-4, DATA6-5, A-2 |
| Staged restore (a pre-swap store exists), with or without journal coverage | the pre-swap store's restrictions win over the restored rows (§4.14 step 3; a key keeps the most restrictive of its two statuses); its own swap appends no `gap` line, so its coverage holds | closed | SEC5-10, SEC6-4, DATA6-2, SEC7-8 |
| A key's last-use write races a revoke or status change | native store: the touch's `Expect` conflicts and the touch is dropped; ABO's store: both run under `apiKeyMu` (early fix 6); the revoke stands either way | closed | SEC6-3, resolutions R6-5, A-3 |
| A `LastSeen` flush for a session that was logged out or revoked | `TouchSession` stages nothing; the session stays deleted | closed | DATA6-8 |
| Bootstrap token wrong/expired/spent | 401 `bootstrap_invalid`, uniform message | closed | |
| Bootstrap file write fails at start | log error, bootstrap unavailable this run; setup still requires token → operator restarts after fixing perms | closed | |
| Key create with escalation | 403 `scope_escalation` | closed | |
| Key create on a legacy-backed store with a set that is not a union of whole legacy atoms | 422 `no_legacy_atom` (`rbac.ErrNoLegacyAtom`), nothing written | closed | the row is the only copy an older build reads (A-2, 05-rbac §6.3) |
| Action on a user the actor does not dominate | 403 `dominance_required` | closed | SEC-4 |
| Facade key off its facade group, or general key on a facade group (deferred-build, §3.5) | 403 `permission_denied` | closed | SEC-5 |
| OIDC subject whose email matches an unlinkable account | 409 `email_in_use` | closed | SEC-7 |
| Remove last `core:admin` holder | 409 `last_admin` | closed | lockout protection |
| Audit write fails for a sync auth event (login success, key create) | the operation fails 503 (same storage batch) | closed | credential changes must be attributable |
| Audit write fails for async event (login failure, denial) | counted, dropped | open | availability under attack |
| `LastSeen` flush fails | log, retry next tick | open | non-security metadata |
| OIDC callback state invalid/expired | 400 `oauth_state_invalid` | closed | CSRF |
| `disabled` mode on a non-loopback listener, or with a trusted proxy/connector, without opt-in | boot error (ABO's default opts in; then health `degraded` `auth_disabled_remote`) | closed | SEC-30, SEC4-8 |
| Post-restore hook fails | boot error | closed | a restore must never bring revoked credentials back |

## 7. Concurrency contract

- `Service` is goroutine-safe. Authentication on the hot path performs: one hash, one storage
  point read (session or key), one user read (cached by `(id, version)` in a bounded LRU, 4,096
  entries, invalidated by user writes and `core.principal.invalidated.v1`), one compiled
  permission lookup (05-rbac §9). No per-request goroutines.
- `HasUsers` is an `atomic.Bool` set by the first `CreateUser` commit; cleared only by a reset of the
  store, which from S2b is core's staged, restart-bound reset of either kind (`ResetStore` behind HEAD's
  `POST /system/reset`, `ResetFactory` behind the factory reset; 05-storage §5S.3.15, DATA6-6, resolution
  R8-1): the next boot is a fresh install, so `auth.New` loads `HasUsers` false.
- Of two concurrent consumers of one token exactly one commits. The native store's `TakeOneTime`
  stages `Expect(key, seen)` plus `Delete` in the caller's batch over its `Serialized` families
  (`c/auth/onetime/`, `c/auth/session/`); ABO's legacy store keeps HEAD's own mutexes for its legacy
  rows, `inviteMu` for invites (`internal/database/user_merge.go:510-520`) and `bootstrapMu` for the
  bootstrap token (resolution A-3; DATA4-9). Tests with `testing/synctest`:
  `TestBootstrap_SingleUseConcurrent`, `TestLegacyInviteDoubleAcceptOneWins`.
- Password hashing: bounded semaphore (`runtime.NumCPU()`).
- `LastSeen` coalescer: one goroutine, a map `publicID → at` under a mutex (never whole records),
  flushed every 60 s and on stop through `Store.TouchSession` (DATA6-8). API-key last use: coalesced per
  key per minute, written through `Store.TouchAPIKey`: with `Expect`, dropped on conflict, in the native
  store; under `apiKeyMu` in ABO's (§4.4, SEC6-3, A-3).
- Throttle: one instance, mutex-sharded LRU, one sweeper goroutine; all stopped by the app context
  (`wg.Go` in `Service.Run`).

## 8. Config keys

`Protection` levels are defined by 05-config; `admin` = `ProtectAdmin` (requires `core:admin` plus an
interactive method). Every key that decides who may sign in, with which role, or from where is
`admin` (SEC-6; it mirrors ABO's `FieldSignIn` class, `internal/config/protected_fields.go:53-66`).
`owner` = `ProtectOwner`: the successors of HEAD's owner trust root (§4.8), owner-only while an
`OwnerVerifier` is configured and an owner identity is set, else `admin`.

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.auth.mode` | enum `required`/`disabled`. On ABO through S10 the legacy `enable_auth` / `ENABLE_AUTH` keep HEAD's behaviour exactly: a stored value beats the legacy env name, and the auth-disabled principal may store `required` under HEAD's `!prior.EnableAuth` rule (§4.11, resolution A-2); the canonical `ABO_CORE_AUTH_MODE` is an additional operator name that locks | `required` | no | owner (SEC3-6, §4.8); native apps: SEC4-8's rule (no change by the auth-disabled principal) |
| `core.auth.disabled_allow_remote` | bool | false (ABO: true, §4.11) | no | admin |
| `core.auth.session.ttl` | duration | 24h | yes (new sessions) | none |
| `core.auth.session.remember_ttl` | duration | 168h | yes | none |
| `core.auth.session.idle_timeout` | duration | 0 (off) | yes | none |
| `core.auth.session.cookie_name` | string | `<AppID>_session` (ABO: `session_id`) | no | admin |
| `core.auth.session.cookie_host_prefix` | bool | true (ABO: false) | no | admin |
| `core.auth.password.min_length` | int | 8 | yes | admin |
| `core.auth.apikey.default_ttl` | duration | 720h | yes | none |
| `core.auth.apikey.max_ttl` | duration | 8760h | yes | admin |
| `core.auth.apikey.legacy_prefixes` | []string | `[]` (ABO: `["abk_"]`) | no | admin |
| `core.auth.bootstrap.enabled` | bool | true | no | admin |
| `core.auth.bootstrap.token_ttl` | duration | 10m | no | admin |
| `core.auth.bootstrap.key_ttl` | duration | 8h (max 24h) | yes | admin |
| `core.auth.bootstrap.readonly_key.enabled` / `.scopes` | bool / []scope | false / `["core:system.view"]` | no | admin |
| `core.auth.setup_requires_token` | bool | true | yes | admin |
| `core.auth.login.max_failures_per_source` | int | 15 | yes | admin |
| `core.auth.login.window` | duration | 15m | yes | admin |
| `core.auth.login.soft_threshold` / `.soft_step` / `.soft_max` | int / duration / duration | 5 / 200ms / 2s | yes | admin |
| `core.auth.invite.ttl` | duration | 72h | yes | none |
| `core.auth.temp_login.ttl` | duration | 15m | yes | admin |
| `core.auth.schedules.session_prune` / `core.auth.schedules.onetime_prune` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` | yes (re-read every poll) | none |
| `core.auth.oidc.redirect_base_url` | URL | — | yes | admin |
| `core.auth.oidc.providers[]` | list `{id, kind (oidc/github), issuer, client_id, client_secret (secret ref), scopes, allowed_emails, allowed_domains, default_role, link_existing_by_email}` | `[]` | yes (provider set rebuilt) | admin; `allowed_emails` / `allowed_domains`: owner (SEC3-6) |
| `core.auth.owner.required` | bool | derived: true iff an OwnerVerifier is configured and an owner identity is set (§4.8, SEC4-14) | no | owner (SEC3-6) |

Cloudflare Access settings are not core keys: `cf_access_team_domain`, `cf_access_aud` and
`owner_email` become books keys under `modules.books.identity.*`, read by ABO's `internal/cfaccess` (09 S4);
all three are `ProtectOwner` (05-config §1.3.7 owner row, SEC3-6).
ABO aliases (05-config; window per 09 §9.2.1): `enable_auth`, `oauth_*`, `oauth_default_role`, `bootstrap_key_ttl`
(`internal/config/config.go:1558,1586-1613,1753`).

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_auth_attempts_total` | counter | `method`, `outcome` (`success`,`invalid`,`expired`,`revoked`,`inactive`,`throttled`) |
| `mediacore_auth_sessions_active` | gauge | — |
| `mediacore_auth_api_keys_active` | gauge | `legacy` (`true`/`false`) (`purpose` is added with the deferred facade key, §3.5) |
| `mediacore_auth_throttled_total` | counter | `surface` (`login`,`bootstrap`,`setup`,`invite`,`temp_login`,`oidc`) |
| `mediacore_auth_password_hash_seconds` | histogram | `op` (`hash`,`verify`) |
| `mediacore_auth_password_queue_wait_seconds` | histogram | — |
| `mediacore_auth_bootstrap_redemptions_total` | counter | `outcome` |
| `mediacore_auth_extension_errors_total` | counter | `extension` |
| `mediacore_auth_principal_invalidations_total` | counter | `reason` |

Names are shown under core's default metric namespace `mediacore`; an app passes its own as an
`app.Options` value, ABO HEAD's `audiobook_organizer`, so nothing is renamed (resolution A-5, 05-metrics).

## 10. Tests

| Test | Asserts |
|---|---|
| `TestSession_TokenNeverStored` | after `IssueSession`, no storage value or key contains the token bytes |
| `TestSessions_ListNeverReturnsToken` | JSON of `GET /me/sessions` has no field equal to any live token (regression for `handlers/auth.go:513-537`) |
| `TestLegacyStore_SessionsKeepHEADRows` | (ABO) a session issued by this build is a raw-ULID `sess:` row with HEAD's fields and `Origin`; `GET /me/sessions` shows its derived handle, never the token; revoking by that handle deletes the row; a HEAD-issued session is checked for epoch, status, revocation and expiry on use and is not rewritten (resolution A-2) |
| `TestCookie_SiblingAppCookieDoesNotBlockLogin` | two apps on one host: distinct default cookie names; a foreign or stale cookie on a Public route → anonymous + cleared (SEC-11, FE-6) |
| `TestStage12_InvalidKeyPlusValidSessionIs401` | stop rule (SEC-12) |
| `TestCredentialFormat_ChecksumAndPrefix` (+ `FuzzParseCredential`) | prefix, length, CRC; malformed → `ErrNoCredential` vs `ErrInvalidCredentials` correctly |
| `TestAPIKey_PresentedKeyWins` | key + session + CF assertion → method `api_key` |
| `TestAPIKey_CreateRequiresScopes` / `TestAPIKey_KeyCannotWiden` | empty scopes 400; key with `[books:view]` cannot mint `[books:edit]` or empty (regression for `handlers/apikeys.go:199-226`) |
| `TestAPIKey_FacadePurposeBoundToModule` (deferred-build, §3.5) | facade key refused off its facade group; general key with extra scopes refused on it (SEC-5) |
| `TestAPIKey_RevokedIsTerminal` | `SetAPIKeyStatus(active)` on a revoked key → 409; `needs_confirmation` → only `active` (admin, interactive) or `revoked` |
| `TestOneFormat_LegacyRowsReadByPrevBuild` | (ABO, in `make ci` with `prev` built from `PREV_REF` and cached by tag, OPS4-6) rollback cells against the previous release's real binary on a store written by this build: a session issued here stays signed in there and one ended there stays ended here; an invite created here is accepted there; a key created here is an `abk_` key whose row holds only its atoms and grants exactly those there; a disabled, restricted (`"inactive"`) or revoked key is refused there; a user disabled or epoch-bumped here is refused there; roll forward: every row prev wrote (a key re-enabled or revoked, a session issued) is read as is (resolution A-2) |
| `TestAPIKey_StoredScopesNeverEmpty` | property test over ABO's legacy store: every key row written for a new key has a non-empty `scopes` list of legacy atoms only, never a namespaced string; a requested set that is not a union of whole atom expansions → 422 `no_legacy_atom` and no row (SEC-1, R4-1, SEC4-3, A-2) |
| `TestLegacyStore_BootstrapKeepsHEADFormat` | (ABO) the bootstrap token minted at P4d is HEAD's `abbs_` token with its hash in HEAD's settings row; it redeems once (`synctest`, under `bootstrapMu`) for an `abk_` key holding every atom, which reads as `["core:admin"]`; every start replaces the hash, as at HEAD (A-2) |
| `TestRestoredLegacyKey_ReenabledKeepsLegacySemantics` | (ABO) an empty-scope `abk_` key and an admin's `["settings.manage"]` `abk_` key, a nil-coverage restore: both are HEAD's `"inactive"`, never `"revoked"`, and refused; re-enabled by the owner or an admin, each has the same legacy-route and op-start reach as before the restore, the empty-scope key `LegacyFullScope` again (resolution A-2) |
| `TestRevokeRacesLastUse_NeverUnrevokes` | (ABO, `synctest`) a revoke and a `TouchAPIKey` racing on one key: both run under `apiKeyMu` (early fix 6), the key ends `revoked` in every interleaving (SEC6-3, R6-5, A-3) |
| `TestKeyLastUseNeverRevertsRevoke` / `TestLastSeenFlushNeverResurrectsRevokedSession` | (`authtest.StoreSuite`, `synctest`) a coalesced key touch after a revoke leaves the key revoked; a `LastSeen` flush after a logout or an admin revoke re-creates no session (DATA6-8) |
| `TestAuthEnable_MatchesHEAD` | (ABO, against HEAD's behaviour) blob `enable_auth=false`, env `ENABLE_AUTH=true`: auth stays off, as at HEAD; with auth off, `PUT /api/v1/config {enable_auth: true}` by the auth-disabled principal succeeds when no owner is set and is refused by the owner trust root when one is, as HEAD's `update_service.go:772-781`; after it the blob holds `enable_auth: true` and `prev` boots with auth on; blob `enable_auth: true`, env `ENABLE_AUTH=false`: auth stays on and health `config_legacy_name_shadowed` names `core.auth.mode`; `ABO_CORE_AUTH_MODE=required` over a stored `false`: auth on and locked; on a native app the auth-disabled principal cannot change `core.auth.mode` (SEC4-8, SEC7-2, resolution A-2) |
| `TestLegacyScopedKey_KeepsLegacyRouteReach` | an admin's `abk_` key scoped `["settings.manage"]` passes every `settings.manage` legacy route and starts a `settings.manage` adapted op; the same key of a custom-role owner without `core:admin` does not; on native routes it holds the expansion only (SEC5-7; cell of `TestLegacyRoleReachUnchanged`) |
| `TestLegacySPA_InviteFragmentReader` (the fragment cell of `TestLegacyInviteLink_EndToEnd`; the name 09 S4's done-check uses, FE6-5) | S4 r2: the legacy SPA opens `/invite#token=<t>` signed out and signed in, removes the fragment with `history.replaceState`, and accepts; the S9 R-test row: an invite minted on S9 opens after a rollback to S8 (FE5-4) |
| `TestRestore_PreSwapRevocationsWin` | staged restore with nil coverage, and with complete coverage over an interval detection missed: a key revoked and a user disabled after the archive, unjournaled, stay revoked and disabled; a key `disabled` or `needs_confirmation` in the pre-swap store and `active` in the archive keeps the pre-swap status; a changed password blanks the restored hash; on ABO every restricted status is a HEAD status (SEC5-10, SEC6-4, SEC7-8) |
| `TestStagedRestoreKeepsCoverage` | (with 05-backup) a staged restore after a clean run: no `gap` line for its own swap, `rc.Revocations` non-nil, only keys revoked after the archive are revoked, no key is set `needs_confirmation` (resolution R6-4, DATA6-2) |
| `TestSession_NativeOnlyRecord` | (native store) no session record holds the token bytes; a lookup by a token whose hash is unknown is refused |
| `TestPostRestoreSurvivesRestart` | restore, restart: the re-revoked key, a restored session cookie and a restored invite are refused, and their rows are gone (SEC2-3, DATA2-4) |
| `TestReplacedStore_RunsPostRestoreHook` / `TestReplacedStore_TruncatedJournalKeysNeedConfirmation` | store file-copied back to an older generation (no staged restore): the next boot re-applies the verified tail and runs the hook with `rc.Revocations == nil` — every active or disabled key `needs_confirmation` (on ABO `"inactive"`), every epoch bumped — also when the journal was truncated after the archive and verifies cleanly; same generation with the head behind (a crash after `Append`) re-applies the tail only, no hook (DATA3-8, SEC3-10, DATA4-7, SEC4-6) |
| `TestLegacyInviteAcceptRechecksCanAssign` | a legacy invite whose creator cannot `CanAssign` its `RoleID`, or whose creator is deleted, is refused on accept (SEC3-2) |
| `TestOwnerTrustRootKeysAreOwnerProtected` | port of HEAD's owner-trust-root test: with an owner set, a non-owner admin session cannot change `core.auth.mode`, a provider's `allowed_emails`/`allowed_domains`, `core.auth.owner.required` or the three `modules.books.identity.*` keys (SEC3-6) |
| `TestLegacyTempLoginLink_EndToEnd` | admin reset on the S4 build → legacy `/auth/temp-login?token=` link → 302 to `/login/temp#token=` → legacy SPA page consumes it → signed in (FE3-5) |
| `TestBootWrites_RunInP4d` | `_system` seeding, the bootstrap mint, the read-only key and the store's `Reconcile` happen after the checkpoint, in P4d step 4; P5 and the store constructor write nothing (DATA3-18, COR4-13, DATA4-10) |
| `TestRestore_PostArchivePasswordChangeNotReverted` / `TestRevocationCommittedAfterCheckpointReapplied` | a password changed after the archive → `password_reset_required`, old password refused; a revocation journaled before and committed after the checkpoint is re-applied (SEC2-11, DATA2-7) |
| `TestStage12_ExtensionPermsIgnored` / `TestModuleAuthMethod_CannotBeInteractive` | an authenticator's assertion cannot raise its checker; a module compat method declared interactive or with credential/owner rights → `ErrMethodSpec` (SEC2-16) |
| `TestConfining_WorkerKeyRefusedOnABS` | `CheckCredentialFor` refuses an `fp-worker` key and a worker ABS login on ABS routes not naming the worker scope (SEC2-4) |
| `TestLegacyInviteLink_RedirectsToFragment` | from S9: `GET /invite/<token>` → 302 to `/invite#token=…` with `Referrer-Policy: no-referrer`; token absent from access log and spans (SEC2-6, FE4-2) |
| `TestLegacyInviteLink_EndToEnd` | S4 r2: an invite created by core is a `/invite/<token>` link; opened signed out in the legacy SPA it is accepted and the invitee is signed in; opened signed in it shows the invite page, not `NotFound` (FE4-2) |
| `TestAPIKey_LegacyAbkAcceptedUntilExpiry` | |
| `TestFirstRun_OnlyPublicAndSetupReachable` | zero users: every non-Public route 401 |
| `TestSetup_RequiresBootstrapToken` / `TestSetup_LoopbackExceptionRefusesForwarded` | loopback peer with a forwarding header, or a trusted-proxy peer, is not local (SEC-30) |
| `TestBootstrap_SingleUseConcurrent` | `testing/synctest`, 50 concurrent redeems, exactly 1 success |
| `TestBootstrap_AdminIsCoreAdminHolder` | picks a `core:admin` holder, not a `users.manage` holder |
| `TestCredentialEpoch_KillsSessionsAndOneTimeTokens` | |
| `TestThrottle_SourceAndAccount` | `synctest` clock; soft delays, 429, LRU pinning |
| `TestPasswordHash_BoundedConcurrency` | at most `NumCPU` verifies in flight |
| `TestAuthDisabled_AuthRequiredScopesStill401` | |
| `TestAuthDisabled_InteractiveGuardRefuses` / `TestAuthDisabled_PresentedKeyIgnored` | SEC-2; the refusal is the recorded auth-off tightening of §4.11 (HEAD's guard is a pass-through, SEC5-8) |
| `TestAuthDisabled_RefusesNonLoopbackOrProxyWithoutOptIn` | SEC-30 |
| `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser` | SEC-4 (also listed in 05-rbac) |
| `TestOIDC_LinksBySubjectNotEmail` / `TestOIDC_PKCEForAllProviders` | fake IdP (httptest) |
| `TestOIDC_UntrustedProviderCannotLinkByEmail` | provider without `link_existing_by_email`, or a target holding `core:admin` or a password → `email_in_use` (SEC-7) |
| `TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough` | |
| `TestCFAccess_JWKSOutageFallsThrough` | |
| `TestOwnerGuard_RequiresOwnerAssurance` | |
| `TestPrincipalFor_CompatAppliesEpochAndScopes` | the ABS path (G15) |
| `TestLastAdmin_CannotBeDisabledOrDemoted` | |
| `TestPostRestore_RevokedKeyStaysRevoked` / `TestPostRestore_NoJournalKeysNeedConfirmation` / `TestRestoreNilCoverageKeepsRevokedTerminal` | SEC-16; refused by the previous release too; with nil coverage a key restored as revoked stays revoked, only active or disabled keys become `needs_confirmation` (DATA4-13) |
| `TestAccessLog_NoInviteToken` | (in 05-http-api) no one-time token in access log, journal or span attributes (SEC-21) |
| `TestNoGlobals` (archtest) | temp-login state and throttles are instance state (R5) |
| `TestNativeStore_RoundTrip` / `authtest.StoreSuite` / `legacytest.AuthStoreSuite` | the native store and ABO's legacy store pass `StoreSuite` (ABO's with HEAD fixtures, today's `TestGrantedFamilyGolden` cases); ABO's also passes `legacytest.AuthStoreSuite`, one case per capability (`Reconcile`, the atom writer), which drives the service so a drift from auth's unexported twins fails it (SIMP4-3, R4-1, SIMP5-4, SIMP5-9, SIMP6-6, R6-3) |
| `TestDarkReleaseWritesNoIdentityRow` / `TestDarkReleaseRecordsJournalGap` | S4 r1: core writes no identity row (the census reads every record through books' store and writes none; `PostRestore` and `BootWrites` do not run); r1 lists no `journal` duty, so the first r2 boot appends a `gap` line (COR4-4, COR6-3) |
| `TestDarkRelease_HEADSeedWritersKeepRunning` | S4 r1 done-check: a fresh store booted on r1 has the four seed roles and `_system`; every r1 restart writes a new bootstrap token file whose token redeems; the expiry stamp keeps HEAD's one-shot flag; at r2 the books start-hook call is gone and P7/`BootWrites` produce the same rows (COR6-3) |
| `TestRollbackR2ToR1Boots` | a store served by S4 r2 boots on S4 r1: books supplies the same store, no missing-grant or backend error (DATA4-2) |
| `TestLegacyInviteDoubleAcceptOneWins` / `TestUserMergeWithLegacyInvite` | `synctest`: two concurrent accepts of one legacy invite, exactly one account; a merge whose source has an open legacy invite succeeds and deletes it (DATA4-9) |
| `TestS4_AuthOffDockerBoots` | (image job) ABO with `enable_auth=false` in a container boots at S4 r2 and reports `auth_disabled_remote` (SEC4-8) |
| `TestOwnerGate_AccessWithoutOwnerFallsBack` | Access configured, no owner identity: restore, factory reset and user merge fall back to Interactive + `core:admin`; owner keys are `ProtectAdmin` (SEC4-14) |
| `TestRestrictedLastAdmin_SSORefused` | a last admin kept by a restore cannot sign in by password, key or OIDC until a reset; on a native store an identity extension refuses it too (`SignInRestricted`), on ABO's store its keys are `"inactive"` and the Cloudflare Access residual is named in the restore report (SEC4-12, A-2) |
| `TestVerifiedEmailReconcileInBootWrites` / `TestExpiryStamp_OneShot` | (ABO) HEAD's verified-email reconcile runs in the store's `legacy.AuthReconciler` at P4d step 4; the key expiry stamp honours HEAD's one-shot done flag (DATA4-10, DATA4-12) |

## 11. Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/auth/context.go`, `user_active.go` | `core/auth` (`Principal`, `Method`, assurance) |
| `internal/auth/owner.go` | ABO `internal/cfaccess` (owner proof, G11; 04 §2.4) |
| `internal/server/middleware/auth.go` (`RequireAuth`, key auth, `EffectivePermissionsFor`) | `auth.Service.Middleware` + `rbac.Service.Compile`; `RequirePermission`/`RequireAdmin` → httpapi stage 13; inside the legacy mount from 09 S4, `RequireAuth` becomes the read-only bridge of 05-http-api §8.3 |
| `internal/server/middleware/{cfaccess,cfbounce,owner_guard}.go`, `internal/oauth/cfaccess.go` | ABO `internal/cfaccess` (04 §2.4), registered in `app.Options.Authenticators` as an `auth.Extension` (§3.3); not core |
| `internal/oauth/{oauth,providers,resolve,state}.go` | `core/auth/oidc` |
| `internal/server/absauth/password.go` | `core/auth/password` |
| `internal/server/absauth/throttle.go`, `internal/server/login_ceiling.go`, `bootstrap_rate_limit.go`, `invite_rate_limit.go` | `core/auth` throttle (instance) |
| `internal/server/bootstrap.go` | `core/auth` bootstrap (§4.6) |
| `internal/server/auth_temp_login.go`, `auth_accept_invite.go`, `auth_reset_password.go` | core auth routes + one-time tokens; dominance-checked service calls |
| `internal/server/handlers/{auth,apikeys,oauth_login,user,invite_delivery}.go` | core auth/users routes (§4.13) |
| `internal/server/absauth/{token,refresh,audit}.go`, `middleware/absauth.go`, `handlers/abs` | stay in ABO `books` (compat surface, per-route `OwnAuth`), calling `PrincipalFor` |
| `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest service tokens | stay in ABO |
| `internal/mtls`, `cmd/mtls-bridge` | stay in ABO |
| `internal/security/safehttp` | `core/integrations/providerhttp` (05-metadata); `safepath`, `pathvalidation` → `core/library` (05-library) — not auth |

**Stored data: store interface, HEAD's representation (resolutions R4-1, A-2).** Core auth does not copy
ABO's identity data and holds none of its key names or row formats: every record goes through
`auth.Store` (§3.4). There is no adoption copy, no mirror, no rebuild at boot, no claim hand-over, no
claim-mode level step for identity and **no storage grant** during the extraction (grants for these
families belong to the identity hardening cutover, §11.2), so an older build after a rollback and this
build after a roll-forward read the same bytes.

**Native store (apps without pre-core identities; SIMP4-3).** `core/internal/authstore`, landing with
`auth` at 09 S4, used by `hello`, `coretest` and every app that passes no legacy store. Values are
`codec.JSON[T](1)` of the core type:

| Key | Value |
|---|---|
| `c/auth/user/<id>`; index `c/auth/user/name/<fold>` → id | `User` (with its password hash and role list) |
| `c/auth/apikey/<id>`; `c/auth/apikey/h/<sha256>` → id; `c/auth/apikey/u/<user>/<id>` | `APIKey` |
| `c/auth/identity/<provider>/<subject>`; `c/auth/identity/u/<user>/<provider>/<subject>` | `ExternalIdentity` |
| `c/auth/inviteallow/<folded e-mail>` | allow-list entry |
| `c/auth/session/h/<sha256>`; `c/auth/session/id/<publicID>` → hash; `c/auth/session/u/<user>/<publicID>` (`Serialized`) | `SessionRecord` with its token hash |
| `c/auth/onetime/<kind>/<sha256>` (`Serialized`) | `OneTimeToken` (invite, temp-login, bootstrap) |

It implements none of the `legacy` capabilities of §3.4. `TestNativeStore_RoundTrip` runs
`authtest.StoreSuite` over it.

**ABO's legacy store (books-supplied).** The `books` module passes `module.Manifest.LegacyStores.Auth`
(06 §6.4.5): ABO's existing identity store code — the auth methods of `internal/database` stay in ABO
behind the interface instead of moving into core (04 §4.5) — over ABO's rows in **HEAD's representation
through S10**, through books' own claims, with HEAD's lock discipline (resolution A-3). Write
exclusivity on those families is an ABO archtest (only the legacy-store package writes them), not a
storage grant. ABO runs `authtest.StoreSuite` with HEAD fixtures. Nothing is stored beside the legacy
rows, and nothing is added to them (SEC4-3: an extra string in a legacy row can widen on an older
build). Every restriction is a field HEAD reads: `status` (`"inactive"`/`"revoked"`), `revoked_at`,
`credential_epoch` and a blanked `password_hash`. A rollback therefore costs nothing: no re-login, no
voided invite, no refused key.

| Legacy family (ABO) | Read and written as | Notes |
|---|---|---|
| `u:` + `idx:user:username:` (and the other `idx:` user sub-prefixes) | the legacy `User` row (`internal/database/store.go:734-780`) | IDs and role IDs unchanged (`admin`, `editor`, `viewer`, `fp-worker`; 05-rbac §6.2); `StatusDisabled` ↔ `"inactive"`, `pending`/`system` are statuses an older build refuses (`user_active.go`); `EmailVerified` derives from `email_source`; `PasswordResetRequired` = a blanked `password_hash` under an algorithm other than `oauth` (`internal/server/absauth/password.go:31-34,165-170`); `SignInRestricted` is not representable (§3.1, §11.2) |
| `apikey:` + `idx:apikey:` | the legacy `APIKey` row (`store.go:982-997`) | hash reused as-is (already SHA-256 of raw); new keys are `abk_` keys whose `scopes` hold the atoms of 05-rbac §6.3's write mapping, never empty (§4.4); read as `APIKey.Legacy` atoms with their canonical expansion, an empty list `LegacyFullScope`; statuses HEAD's (§4.4); every writer under `apiKeyMu` (early fix 6) |
| `sess:<raw>` + `idx:sess:user:` | HEAD's session rows, read and written | issued as HEAD issues them (ULID from `crypto/rand`, `pebble_store_auth.go:1656-1675`; `Origin` as HEAD records it, `""` = delegated; TTL 24 h, remember-me 7 d); checked on use for epoch, status, revoked and expiry; `PublicID` is early fix 1's derived handle; HEAD's expired-row retention is kept; raw at rest as at HEAD until §11.2 |
| `invite:<raw>` + `idx:invite:username:` | HEAD's invite rows, read and written | created as HEAD creates them; accept re-checks `CanAssign` (§4.13, SEC3-2) and deletes the row under `inviteMu`; a user merge deletes the source's invites, never re-owns them (DATA4-9) |
| `oauthid:`, `idx:oauth:user:`, `inviteallow:` | the legacy rows | the invite allow-list feeds OIDC admission (§4.7) |
| bootstrap hash in settings | HEAD's row, read and written | the store's `IssueOneTime` writes HEAD's `abbs_` hash and expiry, regenerated at every start as at HEAD; redeemed under `bootstrapMu` (§4.6) |
| temp-login map | instance memory of the store, as at HEAD | HEAD's tokens live in a process map (`auth_temp_login.go:30-75`); the legacy store keeps them per instance (R5: no package state), lost on restart as at HEAD |

**HEAD's open-time identity writers (DATA4-10, DATA4-12).** `initPebbleStore` writes on every open;
its identity part moves into the legacy store's `Reconcile` (the `legacy.AuthReconciler` capability, §3.4), run by `BootWrites` at P4d step 4 from S4 r2 (through S4 r1 books' start hook runs HEAD's own sequence, §5, COR6-3):
`ReconcileVerifiedEmails` (restamps `email_source` on user rows an older build rewrote, and the e-mail
index; `internal/database/pebble_store_verified_email_backfill.go:310-330`) and the key expiry stamp
(`stampNeverExpiringAPIKeys`, `internal/server/apikey_expiry_stamp.go:23,40-54`), which keeps HEAD's
**one-shot** semantics by reading HEAD's own done flag (`setting:system:migration:apikey_expiry_stamp_v1_done`)
and afterwards only logs never-expiring keys found later. The books module's constructor writes
nothing (05-storage §5S.9.3 lists HEAD's other open-time writers).

### 11.1 Early fixes in the legacy code (independent of the extraction)

These are live defects at HEAD (Out-of-scope defects below, and 05-rbac's). This is the **single
list**: 09 and 14 cite "the six early fixes of 05-authn §11.1" instead of restating it (SEC5-11).
They ship as hotfixes in the current code as early as S0, tracked in
`todo.d/2026-11-04_platform_core_followups.md` (one SECURITY (hotfix) entry each; early fix 5 is the
Basic-gate empty-password entry, SEC7-11), and are
**not** a rollback floor: nothing in the extraction depends on them, no level step or `Floor`
refers to them, and R3-1 needs no legacy-side guard. They matter for rollbacks only in the ordinary
way — the oldest release a deployment should roll back to is the first one that carries them, which
the release notes name.

1. **Session tokens exposed by `GET /api/v1/auth/sessions`** — return a view without `id` and with a
   derived handle for revoke (defect 1 below).
2. **A scoped API key can mint an unscoped key** — when the caller is an API key, require non-empty
   scopes ⊆ the key's effective set (defect 2).
3. **A revoked API key can be re-activated** — `PATCH /api/v1/auth/api-keys/:id`
   (`handlers/apikeys.go:336-372`) answers 409 for a key with `status == "revoked"` or a non-nil
   `revoked_at`; `SetAPIKeyStatus` (`pebble_store_auth.go:1226-1247`) refuses any transition to
   `active` for such a row, so no other caller bypasses the handler; **both** key-auth paths refuse a row
   with `revoked_at` set whatever its `status`: the web path (`middleware/auth.go:335-344`) and the ABS
   path (`internal/server/middleware/absauth.go` `ResolveAPIKey`, its own `switch key.Status`) (defect
   3, SEC4-11).
4. **`RequireAdmin` and the role-name checks ignore API-key scopes** — refuse an `api_key` method whose
   non-empty scopes do not include every atom, in `RequireAdmin`, `adminRole` and the three in-handler
   checks `handlers/user.go:285,453` and the user-merge actor check `mergeActor`
   (`internal/server/handlers/user_merge.go:62-75`, its `adminRole` test at `:67`, SEC8-7); requiring
   `auth.Can(users.manage)` instead is not enough: a key an admin scoped to `users.manage` passes
   `mergeActor` on the two merge reads that are not credential routes, `GET /users/:id/footprint` and
   `GET /users/merges` (the merge POSTs are credential routes and refuse it; 05-rbac "Out-of-scope
   defects" item 1, SEC4-11). From S4 the ABO merge handler checks `Can(core:admin)` with key scoping (05-rbac §4.6;
   `TestUserMerge_ScopedAdminKeyRefused`, legacy then core).
5. **The Basic gate accepts an empty password** — when `basic_auth_enabled` is true and the expected
   password is empty, HEAD's gate compares two empty strings and admits `<username>:`
   (`internal/server/middleware/basicauth.go:81-87`). Hotfix: every non-exempt route answers 401, health
   reports `degraded` (`basic_gate_no_password`) and the startup log names the setting
   (`TestLegacyBasicGate_EmptyPasswordRefuses`, in ABO's current code). The Basic password is **not
   moved** during the extraction: HEAD already stores it encrypted, and books' legacy config store
   reads that row in place (resolution R5-1(b), `internal/config/persistence.go:1955-1958`), so no
   rollback strands the gate. The hotfix closes the remaining case, a password cleared by any means
   (an operator, HEAD's key-regeneration delete path, a partial reset) on a rollback target from S0
   on; core's stage 8 refuses the same state from S3 (05-http-api §4.2, resolution R5-1(c);
   `TestBasicGate_EmptyPasswordRefuses`) (DATA4-1, SEC4-1, OPS4-1, SEC5-4, DATA5-12).
6. **A request with a key can undo that key's revocation** (resolution R6-5, SEC6-3) —
   `TouchAPIKeyLastUsed` holds `apiKeyMu` while it reads the whole `apikey:` row, bumps
   `last_used_at`, `last_used_ip` and `use_count`, and writes the whole row back
   (`internal/database/pebble_store_auth.go:1284-1302`), on every key-authenticated request
   (`middleware/auth.go:382`); `SetAPIKeyStatus`, behind DELETE and PATCH, does its own whole-row
   read-modify-write **without** the lock (`pebble_store_auth.go:1226-1247`), and so does
   `SetAPIKeyExpiry` (`:1254-1267`). A revoke that commits between a touch's read and its write is
   overwritten with the pre-revoke row (`status: active`, no `revoked_at`), silently; a holder of a
   stolen key widens the window by sending requests in a loop. Hotfix: `SetAPIKeyStatus`,
   `SetAPIKeyExpiry` and every other `apikey:` writer take `apiKeyMu` (the lock's one other user, user
   merge, `user_merge.go:528`, already does), so every writer of the family is serialized by one lock;
   test `TestLegacyRevokeRacesTouch_NeverUnrevokes`, in ABO's current code. Books' legacy store keeps
   that lock in the core era (§4.4; resolution A-3).
   Tracked as a hotfix in `todo.d/2026-11-04_platform_core_followups.md` (defect 4 below).

The revocation journal is not on this list: its lines are MAC'd with a key from the S3 keyring
(05-backup §5B.3.6), so it ships with the build that has the keyring (DATA3-19).

### 11.2 Identity hardening cutover (post-S10)

The extraction leaves ABO's identity stores in HEAD's representation (resolution A-2). The hardening
the native design gives hello, TV and Movies reaches ABO later, as named one-way cutovers on N15's
floored machinery (05-storage §5S.3.7's per-owner level guard, its own pre-migration checkpoint, a floor
at the cutover release, decided on measured data, 02 decision 13). 12 and 14 list them; each is the only
step that needs core to read books' rows, so the storage grants for these families belong to it.

| Cutover | What moves | What it removes |
|---|---|---|
| Identity | sessions and one-time tokens hashed (`mcs_`, `mci_`, `mct_`, `mcb_`; §4.1), keys re-issued or re-stored as `mck_` with native scopes, roles with native scopes (core-only scopes, `SeedDigest`/`PendingSeed`, the seed-role operator diff and the lift of `ErrSeedRoleLocked`, 05-rbac §3.6, §4.8), `needs_confirmation` and `SignInRestricted` as stored fields, the native `LastSeen` | `legacy.Stores.Auth`/`Roles`, `legacy.AuthReconciler`, `legacy.AuthAtomWriter`, `legacy.RBACAtomWriter` and their `legacytest` suites (outside the API promise, so no v1 break, R6-3); `app.Options.LegacyAmbientScopes`; `LegacyFullScope` for any key still holding an empty list |
| Secrets | plaintext blob secrets encrypted into `core/secrets` (05-config) | the plaintext fields |
| Auth precedence | operator-layer precedence for the auth switch on ABO (the native rule of §4.11 and SEC4-8) | HEAD's stored-value-wins precedence and `!prior.EnableAuth` rule for `enable_auth` |

Until then, raw session and invite tokens stay at rest exactly as at HEAD (no regression, no
improvement), and ABO's roles and keys cannot hold core-only scopes (05-rbac §6.3).

## 12. Open items

1. WebAuthn/passkeys (`github.com/go-webauthn/webauthn` v0.18.2 exists) as a second factor setting
   `AssuranceMFA` — not in this operation; the `Assurance` bits leave room.
2. Raising `password.min_length` to 12 for new installs (ABO keeps 8 via its config).
3. Whether `core.auth.setup_requires_token=true` ships as ABO's default immediately or after one
   release of the shell wizard showing the token flow (09-abo-on-core).
4. Idle-timeout default (today none).

## 13. Gap disposition (authN rows; full table in 05-rbac §13)

| Gap | Disposition |
|---|---|
| G9 auth wiring inline, closes over `*Server` and global `AppConfig.EnableAuth` | **Fixed**: `auth.Service` instance with explicit `Deps`/`Options`; `core.auth.mode` read through `config` (§3.2, §4.11) |
| G10 bootstrap assumes one binary, admin = first `users.manage` holder | **Fixed**: core-owned, app-agnostic bootstrap; admin = `core:admin` holder (§4.6) |
| G11 owner/CF machinery embedded in auth | **Fixed**: `Authenticator`/`OwnerVerifier` extension point; Cloudflare Access verification, owner proof, policy editing and service tokens stay in ABO (`internal/cfaccess`, `internal/cfaccesspolicy`, `internal/cfservicetoken`; §3.3, §4.8) |
| G12 sessions unhashed (secrets part → 05-config) | **Fixed in the native store**: every session/invite/temp-login/bootstrap/key stored as SHA-256 (§4.1, §4.3); config secrets → `core/secrets` (05-config). **For ABO deferred** to the identity and secrets hardening cutovers (§11.2, resolution A-2): through S10 ABO keeps HEAD's raw session and invite rows |
| G14 rate limit/lockout tied to tunnel topology, in memory | **Fixed + documented**: instance throttle, client address from `httpapi` trusted-proxy config with the connector set migrated, no hard lockout, in-memory by design (§4.9) |
| G15 two parallel auth stacks | **Fixed**: compat stays in app, resolves via `PrincipalFor` (§4.10) |

## Objections to 03

None open. 03 §3.7 (v1.6.0) states one stored format per family and raises a level only for a real
format change (books' own migrations and future cutovers), which is what §11 relies on, and 03 §3.3
"Legacy-only identifiers" defines the `legacy` package this file's capabilities live in (R6-3).

(03 §3.3 places middleware in `auth`; it is a plain `func(http.Handler) http.Handler`, so `auth` (L4)
does not import `httpapi` (L6), consistent with R4.)

## Out-of-scope defects found

These are live in ABO at HEAD and are fixed by this design, but ABO should not wait for the
extraction; each is a small hotfix in the current code and is on the early-fix list of §11.1:

1. **Session tokens exposed by `GET /api/v1/auth/sessions`** (`internal/server/handlers/auth.go:525-536`
   embeds `database.Session`, whose `id` is the bearer token, `internal/database/store.go:1044-1045`).
   Any credential of a user — including an API key, since the route has no permission and no
   credential guard (`raw/09` §4) — can read the user's live session tokens and replay one as
   `Authorization: Bearer`, obtaining `MethodSession` and therefore `MayChangeCredentials`, which
   defeats both the key's scopes and the credential guard. Hotfix: return a view without `id` and
   with a derived handle (e.g. SHA-256 prefix) for revoke.
2. **Scoped API key can mint an unscoped key**: `POST /api/v1/auth/api-keys` with `"scopes": []`
   skips the subset check (`internal/server/handlers/apikeys.go:199-210`) and an empty scope list
   means "all role permissions" (`internal/server/middleware/auth.go:405-408`). Hotfix: when the
   caller is an API key, require non-empty scopes ⊆ the key's effective set.
3. **A revoked API key can be re-activated** (verified by the round-2 lead,
   `todo.d/2026-11-04_platform_core_followups.md`): `PATCH /api/v1/auth/api-keys/:id
   {"status":"active"}` (`internal/server/wire_auth_routes.go:73`, credential-guarded, so any
   interactive session of the key owner) reaches `handlers/apikeys.go:336-372`, which never looks at
   the current status, and `SetAPIKeyStatus` (`internal/database/pebble_store_auth.go:1226-1247`)
   sets `Status = "active"` from any state while the auth path checks only `Status`
   (`middleware/auth.go:335-344`; `RevokedAt` is never consulted). Hotfix = §11.1 item 3. The
   `users.manage` / `settings.manage` reach is latent at HEAD (only `admin` holds those atoms,
   05-rbac "Out-of-scope defects"); this build writes those atoms only into role rows whose native
   permissions include `core:admin` (05-rbac §6.1 row rule), so a rollback cannot extend that reach,
   and from S4 r2 its own legacy routes gated by those atoms require `core:admin` (05-rbac §6.3, R4-3).
4. **A key's last-use write can silently undo its revocation** (round 6, SEC6-3; confirmed by the
   round-6 lead): `TouchAPIKeyLastUsed` serializes on `apiKeyMu`
   (`internal/database/pebble_store_auth.go:1284-1302`) but `SetAPIKeyStatus`
   (`pebble_store_auth.go:1226-1247`) and `SetAPIKeyExpiry` (`:1254-1267`) do not, so a revoke landing
   inside a touch's read-modify-write is lost with no log. Hotfix = §11.1 item 6.

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SEC-1 — native keys mirrored revoked unless exact legacy re-expansion; side record (§11). (superseded in round 3 by R3-1)
- SEC-2 — disabled mode ignores presented credentials; `MethodAuthDisabled` non-interactive; checker without admin (§3.1, §4.2, §4.11).
- SEC-3 — confinement via `Confining` scopes; namespace heuristic removed (§4.2).
- SEC-4 — actor-taking service methods with dominance; `PUT /me/password` self only; route table guards (§3.2, §4.13).
- SEC-5 — `KeySpec.Purpose`/`Module` normative; facade keys bound to their facade group (§3.2, §4.4).
- SEC-6 — Protection column; sign-in keys `ProtectAdmin` (§8).
- SEC-7 — `link_existing_by_email` per provider (default false) with admin/password guard (§4.7).
- SEC-10 — SameSite limits stated; cookie requests covered by httpapi cross-origin stage (§4.3).
- SEC-11 — default cookie `<AppID>_session`, `__Host-` prefix; cookie failure rule (§4.3, §6).
- SEC-12 — stop rule: first non-`ErrNoCredential` result ends authentication, `auth.Failure` recorded, stage 13 answers (§3.2, §3.3, §4.2).
- SEC-14 — ABS routes are per-route `OwnAuth` (§4.10).
- SEC-16 — post-restore hook; `needs_confirmation` key status (§3.2, §4.14).
- SEC-18 — client address from trusted prefixes or zoned connector addresses (§4.9).
- SEC-19 — no raw session mirror; upgrade-on-use fallback, removed after S4 + 7 days (§11).
- SEC-21 — no one-time token in a URL path; lookup/consume by POST body (§4.13).
- SEC-26 — public `auth.System` removed; `core/internal/authsys` + archtest (§3.1).
- SEC-28 — OIDC state key from the rotation-stable derivation key (§4.7).
- SEC-30 — "local" excludes forwarded and trusted-proxy requests; auth-off refuses with a proxy configured (§4.6, §4.11).
- OPS-4 — same fix as SEC-1. (superseded in round 3 by R3-1)
- OPS-11 — auth engine moves in exactly one stage (09 S4); legacy-mount bridge referenced (header, §11).
- DATA-3 — legacy authoritative, rebuild at boot, side records (§11). (superseded in round 3 by R3-1)
- COR-19 — layers per 03 v1.2.0 (§3).
- Cross-cluster (R-A) — auth routes registered from `internal/authhttp` (§4.13, §5).
- COR-20 — `*events.Bus`, `*metrics.Scope` in `Deps` (§3.2).
- SIMP-6 — prune tasks on `tasks.Schedule` (§4.3, §5).
- SIMP-23 — `auth/clientcert` dropped (§3.3, §4.8, §8).
- FE-6 — per-app cookie names; foreign cookie never causes a 401 on Public routes (§4.3).
- FE-7 — Public auth flows answer 401 with domain codes, shown in place (§6).
- FE-19 — served at the origin root only; cookie `Path=/` (§4.3).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (COR-21) — `auth.Identities` (read-only module view) defined for `module.Deps.Auth`.
- integration pass: R-B request (DATA-18) — `auth.Admin.StageUserMerge(ctx, b, plan)` participant with `UserMergePlan`/`MergeRow` and per-row `Expect`; handed to modules as `module.Deps.AuthAdmin` (06).
- integration pass: R-B request (SEC-16) — `PostRestore` implements `backup.RestoreHook` over `backup.RestoreContext`; revocations recorded with `RevocationJournal.Append` as typed `backup.Revocation` before commit, `Gap` on failure (§3.2, §4.12, §4.14); `RestoreInfo` removed.
- integration pass: R-H request (SEC-1, DATA-3) — §11: mirror-disabled keys that are still `Fresh` keep their side record and stay active natively; "revoked" → "disabled" to match 05-storage. (superseded in round 3 by R3-1)
- integration pass: R-H request (G11) — Cloudflare Access is ABO's `internal/cfaccess` (04 §2.4), not `core/auth/cfaccess`; its keys are `modules.books.identity.*` (09 S4); `owner.go` row corrected.

Round 2 (2026-11-04), findings applied to this file:

- R2-1 — §11.1 identity Teach build: key status guard, `users.manage` dominance and `settings.manage` bound in legacy handlers, journal of revocations and restores, rollback floor; key status monotone natively (§4.4, §11.1, §6, §10, out-of-scope defect 3). (superseded in round 3 by R3-1)
- R2-2 — every revocation and post-restore write staged through the mirror; restored legacy sessions/invites deleted; confirming rebuild (§3.2, §4.12, §4.14, §6, §10). (superseded in round 3 by R3-1)
- SEC2-1 / DATA2-3 — revoked projection form (`status: revoked`, `mirror_reason`, sentinel scope), side `Restriction`, most-restrictive rebuild, `LegacyFullScope` only for never-mirrored rows (§11, §10). (superseded in round 3 by R3-1)
- SEC2-3 / DATA2-4 — post-restore hook writes legacy rows in the same batch, deletes legacy `sess:`/`invite:` rows, `needs_confirmation` as the revoked form (§4.14). (superseded in round 3 by R3-1)
- DATA2-5 — Teach build journals its revocations and restores (§11.1). (superseded in round 3 by R3-1)
- SEC2-11 / DATA2-7 — `password.changed` and `identity.unlinked` journaled; `PasswordResetRequired`; re-application from the manifest's journal offset (§3.1, §4.12, §4.14, §6).
- SEC2-10 — `journal.Revocation` with `Object`/`Detail` used (§4.12).
- SEC2-4 — `CheckCredentialFor`; the direct ABS key path applies every stage 12 rule; ABS confinement (§3.2, §4.2, §4.10, §10).
- SEC2-5 — legacy `me/password` with `user_id` stays a gin route, not aliased (§4.13). (refined in round 3: COR3-12 / FE3-5)
- SEC2-6 / FE2-8 — legacy invite and temp-login links 302 to fragment form with `no-referrer`; S4 SPA reads `#token=`; R-test cell (§4.13, §10). (refined in round 3: COR3-12 / FE3-5)
- SEC2-16 — authenticators return an `Assertion`; core compiles `Perms`; extensions only via `app.Options.Authenticators`; compat methods via `Registrar.AuthMethod`, non-interactive (§3.1, §3.3, §4.2, §6, §10).
- SEC2-19 — the Access-assertion client-trust branch moves into `internal/cfaccess` at S4 (§3.3).
- SIMP2-6 / R-F — key purpose, query-key extraction, feed token and facade tests are deferred-build members (§3.2, §3.5, §4.2, §4.4, §4.10, §9).
- SIMP2-4 / R-F — invite `mail.Sender` from legacy SMTP until S7, then from the `notify/email` instance (§4.13).
- SIMP2-1 — prune tasks on declared `core.auth.*.prune_schedule` keys (§4.3, §5, §8).
- DATA2-8 — `invite:` + `idx:invite:username:` `AgingOut`, not mirrored; native invites outside mirror ranges (§11). (superseded in round 3 by R3-1)
- DATA2-24 — the identity rebuild is a kernel post-DAG reconciler (§5, §11). (superseded in round 3 by R3-1)
- DATA2-29 — bootstrap: native `c/auth/bootstrap` only, legacy hash `AgingOut` (§11). (superseded in round 3 by R3-1)
- COR2-9 — journal types from the leaf `backup/journal`; `events` lands with `auth` at S4 (§3).
- COR2-12 — `Store` writes take `*storage.Batch` (§3.4).
- FE2-23 — stage 7 exempts no credential type; 07 refers here for cookie rules (§4.3).
- COR2-19 — audit action names follow 05-rbac §7.1's present-tense grammar (§4.14).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §11.1: identity Teach build = S4 r1 at `core` level 3; S4 r2's adopt steps stamp `Floor` 3; flip S4 r3 at level 4 (09 v1.2.0 renumbering). (superseded in round 3 by R3-1)
- integration pass (round 2): §11.1 item 3 + test: lead decision — the R2-1 bound extends to the legacy write path that `core:integrations.manage` reaches on rollback (secrets cleared on a URL host change, private targets admin-only); `TestTeachBuild_IntegrationURLBound`. (superseded in round 3 by R3-1)
- integration pass (round 2): `RevocationJournal.Append(ctx, b, r)` with the revoking batch; `rc.Boundary` min rule in §3 and §4.14; `Deps.MirrorViews` (`storage.MirrorView`); mirror names `auth.user`/`auth.apikey`/`auth.oauth`/`auth.inviteallow`; `Restriction` terms replace `MirrorDisabled`/`LegacyProjection.Disabled`. (superseded in round 3 by R3-1)
- integration pass (round 2): Schedule keys `core.auth.schedules.session_prune` / `onetime_prune` (R2-7 form).

Round 3 (2026-11-04), findings applied to this file:

- R3-1 — one stored format: legacy-format adapter over the granted identity families, `c/auth/` core-only for sessions, one-time tokens and bootstrap; `Deps.Mirror`/`MirrorViews`, `RestoreDeps.Mirror`, `Store.PutSide`, mirror/side-record/rebuild text and the `MirrorDisabled`/`Fresh` exception deleted; native-only data as namespaced scopes or extra JSON members, restrictions only in fields old builds read; new keys canonical-only, never empty; key status ↔ legacy `inactive`/`revoked`; §11.1 is now the early-fix list (`todo.d`), not a Teach build or floor; cutover deferred (header, §2, §3, §3.1, §3.2, §3.4, §4.3, §4.4, §4.6, §4.12, §4.14, §5, §6, §10, §11, §11.1, defects).
- COR3-2 / DATA3-5 — post-restore and journal re-apply run after `secrets.Open`, config import and `AttachOverrides`; journal from the effective `core.backup.dir` (§3.2, §4.14, §5).
- DATA3-8 / SEC3-3 — Teach-build restores superseded by R3-1; a replaced store (older build's restore, file copy) runs the post-restore hook over the re-applied tail (§4.14, §10).
- SEC3-10 — unverifiable tail on a replaced store fails closed: keys `needs_confirmation`, epochs bumped, health `revocation_journal_broken` (§4.14, §6, §10).
- DATA3-18 — `_system` seeding and the expiry stamp moved from P5 into P4d (`BootWrites`) (§3.2, §5, §10).
- DATA3-19 — the revocation journal is not on the early-fix list (needs the S3 keyring) (§11.1).
- SEC3-1 / SEC3-2 — resolved by R3-1 (no role mirror, no Teach-build assignment check); legacy invites re-run `CanAssign` on accept (§4.13, §6, §10); atom reach rule in 05-rbac §6.1.
- SEC3-4 — superseded by R3-1: the Teach-build integration bound is gone; legacy `settings.manage` is written only for `core:admin` roles (05-rbac §6.1).
- SEC3-6 — owner trust root restored: successors of HEAD's five owner-only fields plus `core.auth.owner.required` are `ProtectOwner` (§4.8, §8, §10).
- SEC3-11 — mirror-disabled residue removed (no revoked form, sentinel scope or `mirror_reason`) (§11).
- SEC3-16 / FE3-5 — legacy invite and temp-login 302s are books manifest routes for the whole window; `invite` in `SPAShared`; legacy `/login/temp` route in both SPA branches; R-test cell reworded; `TestLegacyTempLoginLink_EndToEnd` (§4.13, §10).
- SEC3-17 — Teach-build tests removed from this file's list (§10).
- COR3-12 — `PUT /api/v1/auth/me/password` is one gin route for both cases, no alias (§4.13).
- FE3-18 — legacy-link windows named as 09 §9.6 rows (§4.13).
- SIMP3-2 — deferral checked by the deferred-members archtest from S2, not an S7 grep (§3.5).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): objection on 03 §3.7 closed (03 v1.6.0 applies R3-1); §5 and §4.14 follow 06 v1.4's P4d order (`secrets.Open` → `AttachOverrides` → journal re-apply → post-restore hooks → `auth.BootWrites` → compat records; no config import, no tasks step).

Round 4 (2026-11-04), findings applied to this file:

- R4-1 / SIMP4-2 / SIMP4-3 — `auth.Store` is the SDK interface (rewritten: no app key names, batch-taking `Take*`, `RevokeSignInsBefore`, `DeleteLegacySignIns`, `StageMerge`, `Reconcile`); native `core/internal/authstore` over `c/auth/` with a native store table, used by `hello`, `coretest` and new apps; ABO's legacy store supplied by books; no storage grant (grants move to the deferred cutover); native-only data in a books-owned extension family with `legacy_ref`, suspended when an older build changed the row; `ErrLegacyStoreMissing`; `authtest.StoreSuite`, `TestNativeBackend_RoundTrip` (header, §3, §3.2, §3.4, §4.1, §4.3, §4.4, §4.6, §4.12, §4.14, §5, §6, §10, §11).
- SEC4-3 — no namespaced string in a legacy key row; HEAD's negative worker test noted in §2 (§2, §4.4, §11).
- COR4-4 / DATA4-2 — S4 r1 runs only the read-only census through books' store; P4d auth writes start at r2; r1 lists no `journal` duty; r2 → r1 cannot meet a missing grant; tests (§5, §10).
- COR4-11 — S2 auth leaf adds `Assurance`, `User`, `UserID`, `UserStatus` (§3).
- COR4-13 — prune tasks from S5 (pruned on read at S4); bootstrap mint and read-only key in `BootWrites` (P4d step 4); compat methods at P7 (§3.2, §4.3, §4.6, §5).
- COR4-19 / SIMP4-1 — heading `Deferred-build members`; generated list from every such heading, package-qualified matching, per-member lifetime (§3.5).
- DATA4-1 / SEC4-1 / OPS4-1 — early fix 5: the Basic gate fails closed on an empty password, as defence in depth behind R4-2's sentinel (§11.1).
- DATA4-7 / SEC4-6 — replaced store decided from store identity; a replaced store's hook gets `rc.Revocations == nil` after the tail is re-applied, even when the tail verifies; tests (§4.14, §6, §10).
- DATA4-9 — `Take*` stage `Expect` + delete over `Serialized` families; a merge deletes legacy invites, `MergeRow.Kind` without `invite`; tests (§3.1, §3.4, §7, §10, §11).
- DATA4-10 / DATA4-12 — HEAD's verified-email reconcile and one-shot expiry stamp run in the legacy store's `Reconcile` at P4d step 4; constructors write nothing (§3.2, §5, §11).
- DATA4-13 — nil-coverage step 3 applies only to active or disabled keys; revoked stays terminal (§4.14, §10).
- DATA4-14 / SEC4-9 — native sessions issued before a rollback interval are revoked on the first boot after it; "both ways" now holds (§4.3, §10, §11).
- OPS4-6 — `TestOneFormat_LegacyRowsReadByPrevBuild` runs in `make ci` with `prev` from `PREV_REF` (§10).
- SEC4-8 — ABO's default `disabled_allow_remote=true` with health `auth_disabled_remote`; auth is turned on with auth off through the operator layers; tests (§4.11, §6, §8, §10).
- SEC4-11 — early fix 3 names both key paths; fix 4 keeps only the scope test, applied to `RequireAdmin`, `adminRole` and the handler checks (§11.1).
- SEC4-12 — `User.SignInRestricted` for a restricted last admin; identity links deleted; Access residual stated (§3.1, §4.14, §6, §10).
- SEC4-14 — owner gate default and `ProtectOwner` both derive from "verifier configured and owner set" (`Extension.OwnerSet`) (§3.3, §4.8, §8, §10).
- FE4-2 — invite links keep the `/invite/<token>` path form until S9 (`auth.Options.InviteLinkForm`, ABO `InviteLinkPath` from S4 to S9); the `/invite/{token}` 302 and `SPAShared` `invite` start at S9; `TestLegacyInviteLink_EndToEnd` (§4.13, §10).

Integration pass (round 4):

- integration pass (round 4): `auth.Options` and `InviteLinkForm` defined (§3.2), passed as `app.Options.Auth`; ABO's `disabled_allow_remote=true` default set through `DefaultDisabledAllowRemote`; revoke-after-rollback keyed on the served ledger's `auth` duty (09 §9.2.1 row 2); ABO key names (`sess:`, `inviteallow:`) removed from §4.7 and §10.

Round 5 (2026-11-04), findings applied to this file:

- SEC5-1 / DATA5-5 (R5-6) — a no-atom key's legacy row holds one reserved no-scope string, never an empty list, and mirrors the real status (the `inactive` special case is gone); `LegacyFullScope` only for an empty row without an extension record; a key with an extension takes scopes only from it; `TestSuspendedNativeKey_NeverLegacyFullScope` (§4.4, §11, §10).
- SEC5-2 / DATA5-3 (R5-6) — stale keys derive status from the legacy row first: a revocation by an older build stays `revoked`; `needs_confirmation` keeps legacy `"revoked"` with a stamp, and the one sanctioned store exit is named; `TestStaleKey_RevokedOnPrevStaysRevoked` (§3.2, §4.4, §4.14, §6, §10).
- DATA5-2 / SEC5-12 / DATA5-16 (R5-6) — guarded projections for `apikey:` and `u:` (security-relevant fields only; last-use fields excluded); "SHA-256 of the row bytes" replaced by `legacy_ref.fp`; `TestKeyUseOnPrev_DoesNotSuspend` (§4.4, §11, §10).
- DATA5-4 — suspended keys leave that state only through `Service.ConfirmAPIKey` / `KeyConfirmer` (`Extension.Confirm`); `set_status` → 409 `key_suspended`; the user record has no Widening field, so no user confirmation is needed (the reviewer's `ConfirmUser` is unnecessary under that classification); `TestSuspendedKey_ConfirmClears` (§3.2, §3.4, §6, §10, §11).
- SIMP5-4 — `auth.Store` holds the native contract only; `LegacySignIns`, `Reconciler`, `KeyConfirmer` are optional interfaces detected at P4c; `authtest.LegacySignInsSuite` (§3.2, §3.4, §5, §10, §11).
- SIMP5-9 — `TestNativeBackend_RoundTrip` renamed `TestNativeStore_RoundTrip` (§10, §11).
- COR5-16 / SIMP5-6 — deferred-members table: first column only package-qualified identifiers, other items in "Also deferred (not checked)"; heading match after an optional section number (§3.5).
- SEC5-5 / COR5-3 / DATA5-6 (R5-5) — revoke-after-rollback uses this boot's start, is idempotent on re-detection and fails closed when 05-storage cannot rule out a foreign open; ledger kept until consumers commit; inode-reuse and failed-boot test cells (§3.2, §4.3, §10).
- SEC5-6 — a store with user rows and no cut-off record after an r2 boot is a replaced store; content-marker classification cited; `TestMissingCutOffAfterServedIsReplacedStore` (§3.4, §4.14, §6, §10).
- SEC5-7 — legacy keys without an extension keep HEAD's intersect reach on legacy routes and adapted ops (`rbac.LegacyAtom`); `TestLegacyScopedKey_KeepsLegacyRouteReach` (§4.4, §11, §10).
- SEC5-8 — HEAD's credential guard is a pass-through with auth off (`credential_routes.go:79-82`); the `Guards.Interactive` refusal is a recorded tightening (§2, §3.1, §4.11, §10).
- SEC5-10 — staged restore with nil coverage: pre-swap restrictions win (keys, user status, passwords, identity links) via `RestoreDeps.Previous`; `TestRestore_PreSwapRevocationsWin` (§3.2, §4.14, §6, §10).
- SEC5-4 / DATA5-12 / OPS5-2 (R5-1(b),(c)) — early fix 5 rewritten: the Basic password is not moved; core's stage 8 refuses an enabled gate with no password; hotfix test renamed `TestLegacyBasicGate_EmptyPasswordRefuses`, the core test lives in 05-http-api (§11.1).
- SEC5-11 — §11.1 declared the single list that other files cite (§11.1).
- COR5-2 — P4d step 1 order: `OverrideStore.Open` → `secrets.Open(…, OverrideStore.Secrets())` → `SetSecrets`/`Attach` (§4.14, §5).
- COR5-15 — the S4 r1 census is P4d step 1c (§5).
- DATA5-18 — books retention sweep for expired `sess:` rows; claim `AgingOut` from the fallback-removal release (§11).
- FE5-4 — option (a), matching 07 and 09: the S4 legacy SPA gains the `/invite#token=` reader, so invites minted on S9 open after a rollback to S8; core keeps minting the path form until S9; fragment test cell and S9 R-test row (§3.2, §4.13, §10).
- MOD5-5 / SIMP5-14 — `log.DefaultPathRules()` / `log.DefaultRedactQuery()` cited as functions (§4.13).
- integration pass (round 5): §4.14 and §5 cite the config store's legacy `Open` and `Secrets` as the optional `config.LegacyOpener` / `config.LegacySecrets` (05-storage §5S.3.11 "Legacy capabilities"), not as `OverrideStore` members.

Round 6 (2026-11-04), findings applied to this file:

- SEC6-3 (R6-5) — early fix 6 (every `apikey:` writer takes `apiKeyMu`; evidence `pebble_store_auth.go:1226-1247` vs `:1284-1302`); books' store runs every `apikey:` writer under a commit-time `Expect`, last use dropped on conflict; `CheckedWrites` for row 3 from S4 r2 requested; defect 4; tests (§3.4, §4.4, §6, §7, §10, §11, §11.1, defects).
- SEC6-1 (R6-6) — the auth-enable switch is exempt from "stored value wins": `core.auth.mode` declares `config.SafeLegacyValue` (`required`), so any operator value enabling auth takes effect and locks; disabling values keep HEAD's precedence; `config_legacy_name_shadowed`; `TestAuthOn_LegacyAliasOverridesStoredOff` (§4.11, §8, §10).
- SEC6-2 — one scope-source predicate (`scopes_source: canonical` only at native creation); restore fallback and stale stamp write `scopes_source: legacy` records that keep legacy semantics; confirm shows legacy scopes; tests (§3.2, §4.4, §4.14, §10, §11).
- SEC6-4 — detection names its real signals (full sidecar stat plus the Pebble open stamp; lineage is a content signal); pre-swap restrictions win whenever `Previous` is non-nil; test (§4.3, §4.14, §6, §10).
- SEC6-7 / DATA6-13 — the "r2 boot" evidence is a `served_boot` pair in `backup.location.json` (§3.4, §4.14, §10).
- COR6-2 / DATA6-14 — `RestoreDeps.Duties` (`Owed`, `StageDone`); revoke-after-rollback uses `Owed.Detected` and clears the duty in its batch (§3.2, §4.3, §5, §10).
- COR6-3 — through S4 r1 books' start hook keeps HEAD's `seedRolesAndTokens`; r2 deletes it; dark-release test narrowed to core's writes; r1 done-check `TestDarkRelease_HEADSeedWritersKeepRunning` (§3.2, §5, §10, §11).
- COR6-10 — `AttachOverrides` replaced by `Store.Attach` (§4.14).
- DATA6-2 (R6-4) — a staged restore's own swap is not a foreign open, appends no `gap` and keeps coverage; `TestStagedRestoreKeepsCoverage` (§4.3, §4.14, §6, §10).
- DATA6-3 — a restore whose hook fails leaves the original store "same store" (§4.14).
- DATA6-4 — out-of-store journal head as replaced-store evidence; the owed-`auth` consumer also deletes legacy sign-ins (§3.2, §4.3, §4.14, §6, §10).
- DATA6-5 — "replaced" is 05-backup (2)'s classification only; a relocated store runs no hook (§4.14, §6).
- DATA6-6 — `HasUsers` after core's staged factory reset (§7).
- DATA6-8 — `Store.TouchSession` / `Store.TouchAPIKey` (`GetForUpdate`, usage fields only, skip deleted/revoked); flushers use them; tests (§3.4, §4.3, §6, §7, §10).
- FE6-5 — the fragment cell is named `TestLegacySPA_InviteFragmentReader`, as in 09 S4 (§10).
- SIMP6-4 — `auth.KeyConfirmer` deleted; confirmation through the shared `ExtConfirmer` (`"apikey/"+id`) (§3.2, §3.4, §10).
- SIMP6-6 — one `legacytest.AuthStoreSuite` with a case per capability replaces `authtest.LegacySignInsSuite` (§3.4, §10).
- R6-3 / SIMP6-2 — `auth.LegacySignIns` → `legacy.AuthSignIns`, `auth.Reconciler` → `legacy.AuthReconciler`, in the `legacy` package outside the API promise; the kernel detects them at P4c and `auth` names them through unexported twins checked by `export_test.go`; legacy sessions/invites returned as `SessionRecord`/`OneTimeToken`; cutover removal is not a v1 break; config capability references renamed `legacy.ConfigOpener`/`legacy.ConfigSecrets` (§3, §3.4, §4.14, §5, §11).
- integration pass (round 6): one name for the auth-on test, `TestAuthOn_LegacyAliasEnablesOverStoredOff` (was `TestAuthOn_LegacyAliasOverridesStoredOff`), as 09 S4 and 05-config cite it. Already applied: `TestLegacySPA_InviteFragmentReader`, `TestRestoredLegacyKey_ReenabledKeepsLegacySemantics`, `TestRevokeRacesLastUse_NeverUnrevokes`; the owed-`auth` consumer's `DeleteLegacySignIns`; `TouchSession`/`TouchAPIKey`; `RestoreDeps.Duties`.

Round 7 (2026-11-04), findings applied to this file:

- SEC7-1 (R7-1) — §4.11: an operator-enabled auth over a stored `enable_auth: false` is written back as `enable_auth: true` at P4d step 1 (audited `core.config.legacy_narrowed`), the Basic gate likewise only with a stored password; S4 r1's `AppConfig.EnableAuth` follows the effective `core.auth.mode` (S3 façade projection), so r1 writes too; release note; §6 row; `TestRollback_OperatorEnabledAuthStaysOn` (§4.11, §6, §10).
- SEC7-2 (R7-1) — a stored value equal to the default (`enable_auth: true`) still beats a `BelowStored` legacy value; an unset writes the default explicitly; the auth-on test gains the equal-to-default case (§4.11, §10).
- DATA7-9 (R7-5) — `DeleteLegacySignIns` runs only when the cut-off exists and predates `Owed.Since` (zero `Since` fails closed); the first r2 boot writes the cut-off in the `BootWrites` batch and deletes nothing; `TestFirstR2BootKeepsLegacySessions` (§3.2, §4.3, §6, §10, §11).
- DATA7-10 (R7-4) — the "user rows but no cut-off" replaced-store signal is deleted (§3.4 comment, §4.14, §6 row, `TestMissingCutOffAfterServedIsReplacedStore`); 05-backup (2)(a) cited as the one classifier (§3.4, §4.14, §6, §10).
- DATA7-11 (R7-4) — the offline `users reset-password` opens through `kernel.OpenOffline` and is not a foreign open (§4.3, §4.14).
- COR7-8 (R7-4) — `RestoreDeps.Previous` is built by calling the constructor once more over `storage.Admin.OpenReadOnly`'s handles; "once per boot" restated (§3.2).
- COR7-2 / SIMP7-1 (R7-7) — the mirror test is 03 §3.3's: `export_test.go` exports aliases, `legacy_mirror_test.go` in `package auth_test` asserts both directions (§3.4).
- SIMP7-5 — the kernel asserts only the capabilities it drives; auth detects its own through its mirrors (§3.4, §5).
- SEC7-7 — status derivation from the legacy row first and early fix 3 apply on every fingerprint mismatch; a `scopes_source: legacy` record's restriction stands until `ConfirmAPIKey`; §6 row; `TestLegacyRecordStale_ReactivatedRowStaysRestricted` (§4.4, §6, §10, §11).
- SEC7-8 — pre-swap restrictions carry `disabled` and `needs_confirmation` too (most restrictive status wins); test extended (§4.14, §6, §10).
- SEC7-11 — the `todo.d` fragment holds all six hotfix entries (early fix 5 at the Basic-gate entry); §11.1 says so.
- SEC7-12 — `auth_disabled_remote` and `config_legacy_name_shadowed` are carried by HEAD's degraded token list until S8 (§4.11).
- SEC7-13 — "a command-line flag aliasing either" removed: env or operator file only (§4.11).
- DATA7-18 — the open stamp signal reads "differs from both the recorded number and its `pending`", as 05-storage (§4.3).
- integration pass (round 7): §8 `core.auth.mode` row and §4.11 declare `config.NarrowStored` (from S4 r1), and §4.11 states the Basic-gate narrowing from S3 (R-E request, R7-1); §11 "ABO's legacy store": the `Expect`/`GetForUpdate` each extension-family writer stages, per 05-storage §5S.3.11's table (R-B request, R7-3).

Round 8 (2026-11-04), findings applied to this file:

- R8-2 / SEC8-4 / DATA8-3 — §4.3, §4.14: `OpenOffline` computes the boot verdicts and, after a foreign open, a missing or unreadable record or a behind lineage, stages the owed entries before promoting; the offline `users reset-password` therefore never erases a foreign open; its checks are P2–P3's (storage steps 1–4) plus the swap tokens; `TestNativeSessionsRevokedAfterRollback` gains the offline case (§10).
- DATA8-10 — §3.2, §4.3: a zero `Since` from an absent previous record never deletes legacy sign-ins; only an unreadable record or a cut-off older than a non-zero `Since` does; `TestFirstR2BootKeepsLegacySessions` gains the retried-first-boot case.
- DATA8-13 — §4.3: `DeleteLegacySignIns` is introduced under R7-5's condition; on a replaced store `PostRestore` step 2 deletes legacy sign-ins.
- DATA8-7 — §4.12: hook-appended revocation lines carry `restore_seq=<seq>` in `Detail`, so a swapped-back restore's lines are never re-applied.
- SEC8-1 (R8-4) — §4.11: the Basic-gate narrowing writes `true` whenever effective `true` (fail-closed); `TestRollback_OperatorEnabledAuthStaysOn` covers stored, canonical-only and env-only passwords and an absent field (SEC8-12).
- SIMP8-6 — §4.11, §8: `core.auth.mode` declares `NarrowStored("core.auth.mode", nil, nil)`, reusing its `SafeLegacyValue` predicate.
- SEC8-7 — §11.1 item 4: `mergeActor` (`user_merge.go:62-75`) added to early fix 4; from S4 the merge handler checks `Can(core:admin)` with key scoping.
- SIMP8-3 — §3.4, §5: "the capabilities it drives" replaced by "the capabilities 03 §3.3 lists".
- R8-1 — §4.8, §7: HEAD's `POST /system/reset` named among the owner-guarded routes; `HasUsers` is cleared by either reset kind.
- integration pass (round 8): §4.11 and §8 declare `config.NarrowStored("core.auth.mode", nil)`, with no `when` parameter (R-E request); §4.12: hook-appended lines carry their restore's seq in `Revocation.RestoreSeq` (`restore_seq`), not as a `Detail` element, as 05-backup defines it (DATA8-7, R-B request).

Advisor (2026-11-04), resolutions and findings applied to this file:

- A-2 (ADV-3, ADV-S1) — header, §3, §3.1–§3.4, §4.1, §4.3, §4.4, §4.6, §4.11–§4.14, §5–§10, §11, §13: ABO's identity store keeps HEAD's representation through S10 (raw `sess:` rows and HEAD's one-time tokens, `abk_` keys holding legacy atoms, HEAD's statuses, HEAD's bootstrap hash, in-memory temp-login); `auth.Store` made representation-neutral (`IssueSession`/`SessionByToken`, `IssueAPIKey`/`APIKeyByToken`, `IssueOneTime`/`TakeOneTime` by raw value); deleted for ABO: the extension records with `legacy_ref.fp`, Widening fields, suspension, `ConfirmAPIKey` and the shared confirm capability, `legacy.AuthSignIns` with the upgrade-on-use fallback, `DeleteLegacySignIns` and the session cut-off, the `auth` revoke-after-rollback duty and `RestoreDeps.Duties`, `["none"]`, ABO `mck_` keys, the R6-6 exemption, the `NarrowStored` declaration and their tests and rows; core-namespaced key scopes are the API-level mapping through `rbac.LegacyAtom` (`legacy.AuthAtomWriter`, 422 `no_legacy_atom`); a nil-coverage restore sets ABO keys to HEAD's `"inactive"` and deletes every session (R-B's choice (b)); the restricted last admin's Cloudflare Access residual applies on ABO; new §11.2 "Identity hardening cutover (post-S10)"; the native design and the six early fixes kept.
- A-2 / lead decision (R-E) — §4.11, §8, §10: ABO's auth enable is HEAD's exactly (stored value wins; the auth-disabled principal may store `enable_auth: true` under HEAD's `!prior.EnableAuth` rule, `update_service.go:781`); SEC4-8's restriction is native-only; `TestAuthOn_LegacyAliasEnablesOverStoredOff` rewritten as `TestAuthEnable_MatchesHEAD`; `ABO_CORE_AUTH_MODE`'s rollback cost goes to 09's canonical-only row.
- A-3 (ADV-S2) — §3.4, §4.4, §4.6, §6, §7, §11.1: ABO's legacy rows keep HEAD's mutexes (`apiKeyMu` with early fix 6, `inviteMu`, `bootstrapMu`); `Expect`-based single use and lost-update checks are native-store only; the `apikey:` `CheckedWrites` request withdrawn.
- A-4 (ADV-S3) — §3.5: no deferred-members archtest.
- A-5 (ADV-S4) — §9: metric names under the app's namespace; the legacy-session fallback counter deleted.
- A-14 (ADV-S8) — §5: the P4d compat usage-record step removed.
