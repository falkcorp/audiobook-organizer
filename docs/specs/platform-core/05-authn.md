<!-- file: docs/specs/platform-core/05-authn.md -->
<!-- version: 1.1.0 -->
<!-- guid: 73c4823d-8269-4c1d-8809-4006a6da6f0d -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authentication (`core/auth`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(gaps G1–G15; the full disposition table is in [05-rbac](05-rbac.md) §13, the authN-owned rows are
repeated in §13 here), [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Permissions/roles/audit: [05-rbac](05-rbac.md). Route chain: [05-http-api](05-http-api.md) §4.2.
Secret storage: [05-config](05-config.md) (`core/secrets`). Layers and naming: [03-architecture](03-architecture.md)
v1.2.0. Stage numbers for the rollout are 09-abo-on-core's (the auth engine moves in exactly one
stage, 09 S4).

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
| Auth off | `enable_auth=false`: auth + `s.perm` + `adminRole` are pass-through; "always-authenticated" admin routes still 401; credential routes refuse because no method is set | `internal/server/server_lifecycle.go:1693-1698,1843-1848`, `internal/server/wire_admin_role.go:18-45`, `internal/server/credential_guard.go:28` |
| Session store hashing of other secrets | API keys and bootstrap hashed; sessions, invites, temp-login **raw** | `raw/10` §7 |

## 3. Go API

Package `github.com/falkcorp/mediacore/auth` (layer L4 with `rbac`, `audit`, `tasks`; imports `rbac`,
`audit`, `entity`, `storage`, `config`, `secrets`, `log`, `metrics`, `events`). Subpackages:
`auth/password`, `auth/oidc`, `auth/cfaccess` (optional extension). Storage keyspace `c/auth/`
(05-storage). The `_system` principal constructor lives in `core/internal/authsys` (not SDK, §3.1).

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
// non-interactive and MayChangeCredentials=false (fail closed, as today's
// credential guard refuses with no method set, credential_guard.go:28).
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
	Perms        rbac.Checker // rbac.Service.Compile(roles, key scopes, interactive) (05-rbac §4.4)
	Assurance    Assurance
	AuthTime     time.Time    // when the credential was issued / sign-in happened
	ClientAddr   netip.Addr
}

func WithPrincipal(ctx context.Context, p *Principal) context.Context
func PrincipalFrom(ctx context.Context) (*Principal, bool)

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
	Store    Store          // over storage keyspace c/auth/
	RBAC     *rbac.Service  // checker compilation, CanAssign, Dominates
	Audit    audit.Logger
	Secrets  secrets.Store  // OIDC client secrets, derived keys (05-config)
	Events   *events.Bus    // publishes core.principal.invalidated.v1
	Clock    func() time.Time
	Log      *slog.Logger
	Metrics  *metrics.Scope // "mediacore.auth" (05-metrics)
	StateDir *os.Root       // bootstrap token file (os.Root-confined)
}

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
	Token     string // returned once; "mcs_..." (§4.1)
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
func (s *Service) SetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error
func (s *Service) RevokeAPIKey(ctx context.Context, actor *Principal, id string) error

type KeySpec struct {
	Owner     UserID
	Name      string
	Purpose   KeyPurpose        // PurposeGeneral (zero) or PurposeFacade (§4.4, SEC-5)
	Module    string            // required iff Purpose == PurposeFacade: the facade's module ID
	Scopes    []rbac.Permission // REQUIRED non-empty, no wildcards (05-rbac §4.5)
	ExpiresIn time.Duration     // 0 = default; > max = error
}

type KeyPurpose uint8

const (
	PurposeGeneral KeyPurpose = iota
	PurposeFacade             // every scope is <Module>:arr.*; accepted only on that module's facade group
)

type KeyStatus string

const (
	KeyActive            KeyStatus = "active"
	KeyDisabled          KeyStatus = "disabled"
	KeyNeedsConfirmation KeyStatus = "needs_confirmation" // set by the post-restore hook (§4.14); refused until an admin re-enables
	KeyRevoked           KeyStatus = "revoked"
)

// Bootstrap (§4.6).
func (s *Service) RedeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error)

// Compatibility surfaces and extensions build principals through this; it
// applies status, epoch, role resolution and scope intersection exactly like
// core credentials (G15).
func (s *Service) PrincipalFor(ctx context.Context, u *User, m Method, scopes []rbac.Permission, epoch int64) (*Principal, error)

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

// PostRestore is called by the kernel after a backup restore swap, before
// listeners open (§4.14, SEC-16).
func (s *Service) PostRestore(ctx context.Context, in RestoreInfo) error

type RestoreInfo struct {
	ArchiveCreatedAt time.Time
	Revocations      iter.Seq[Revocation] // from 05-backup's out-of-DB revocation journal; nil when unavailable
}
```

### 3.3 Authenticators (extension point, G11/G15)

```go
// Authenticator inspects a request and returns a Principal, or ErrNoCredential
// to let the next one try, or another error. Any result other than
// ErrNoCredential ends authentication for the request (a presented-but-invalid
// credential never falls through to a weaker one); the middleware records it
// as auth.Failure and stage 13 refuses non-Public routes with 401.
type Authenticator interface {
	Name() string
	Authenticate(r *http.Request) (*Principal, error)
}

// OwnerVerifier is implemented by an authenticator that can prove the
// deployment owner (sets AssuranceOwner).
type OwnerVerifier interface{ VerifiesOwner() bool }

type MethodSpec struct {
	Method               Method // "ext:cfaccess", "books.abs"
	Interactive          bool
	MayChangeCredentials bool   // only extensions proving a person's own sign-in may set this
}

// Registered by app options (extensions) or module registrars (compat methods).
type Registrar interface {
	AddAuthenticator(a Authenticator, spec MethodSpec, order Order) error
	AddMethod(spec MethodSpec) error // compat surfaces that authenticate themselves
}

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
)
```

Optional extension shipped by core (a separate package, wired only when configured):

```go
package cfaccess // github.com/falkcorp/mediacore/auth/cfaccess

// New verifies Cf-Access-Jwt-Assertion / CF_Authorization (issuer, AUD, expiry,
// JWKS via go-oidc), resolves an allowlisted email to a user through
// auth.Service.PrincipalFor, and, when OwnerEmail matches, sets AssuranceOwner.
func New(ctx context.Context, cfg Config, svc *auth.Service) (auth.Authenticator, error)

type Config struct {
	TeamDomain          string
	AUD                 string
	OwnerEmail          string // optional
	TrustClientIPHeader bool   // when verified, believe the CDN client-IP header (feeds httpapi client address)
}
```

v1.0.0 also specified an `auth/clientcert` mTLS authenticator. It is dropped (SIMP-23): it has no
consumer, and the `Authenticator` extension point leaves room for it. ABO's mTLS MCP bridge stays in
ABO (§4.8).

### 3.4 Store

```go
// Store is implemented by core/internal/authstore over storage keyspace c/auth/.
type Store interface {
	// users: c/auth/user/<id>, c/auth/idx/username/<fold>, c/auth/idx/email/<fold>
	PutUser(b storage.Batch, u *User) error
	GetUser(ctx context.Context, id UserID) (*User, error)
	UserByUsername(ctx context.Context, fold string) (*User, error)
	ScanUsers(ctx context.Context) iter.Seq2[*User, error]

	// sessions: c/auth/session/h/<sha256(token)> -> record; c/auth/session/id/<publicID> -> hash; c/auth/session/u/<user>/<publicID>
	PutSession(b storage.Batch, s *SessionRecord) error
	SessionByHash(ctx context.Context, h [32]byte) (*SessionRecord, error)
	SessionByPublicID(ctx context.Context, id string) (*SessionRecord, error)
	ScanUserSessions(ctx context.Context, u UserID) iter.Seq2[*SessionRecord, error]
	DeleteSession(b storage.Batch, s *SessionRecord) error

	// api keys: c/auth/apikey/<id>, c/auth/apikey/h/<sha256(raw)> -> id, c/auth/apikey/u/<user>/<id>
	PutAPIKey(b storage.Batch, k *APIKey) error
	APIKeyByHash(ctx context.Context, h [32]byte) (*APIKey, error)

	// invites, temp-login, bootstrap, oauth identities: all keyed by token hash
	PutOneTime(b storage.Batch, t *OneTimeToken) error
	TakeOneTime(ctx context.Context, kind OneTimeKind, h [32]byte) (*OneTimeToken, error) // atomic get+delete
	PutIdentity(b storage.Batch, id *ExternalIdentity) error
	IdentityBySubject(ctx context.Context, provider, subject string) (*ExternalIdentity, error)

	// compatibility window only (§11): native-only data keyed by legacy ID
	PutSide(b storage.Batch, family, legacyID string, v []byte) error
}

type SessionRecord struct {
	Hash            [32]byte
	PublicID        string
	UserID          UserID
	Origin          SessionOrigin // password, oidc:<provider>, delegated:temp_login, delegated:invite, legacy
	CredentialEpoch int64
	CreatedAt, ExpiresAt, LastSeen time.Time
	ClientAddr      netip.Addr
	UserAgent       string
	Version         int64
}
```

## 4. Behaviour

### 4.1 Credential formats

All secrets are 32 bytes from `crypto/rand`, encoded base64url without padding (43 chars), with a
type prefix and a 6-char CRC32 checksum suffix (base62) so secret scanners can recognise and
offline-validate a leaked value without a database. Stored only as `SHA-256(raw)`; 256-bit random
input makes a slow KDF unnecessary and keeps verification O(1).

| Credential | Format | Lifetime | Stored as |
|---|---|---|---|
| Session | `mcs_<43>_<6>` | 24 h; remember-me 7 d; sliding `LastSeen` (idle timeout optional) | `SHA-256` key + public ULID |
| API key | `mck_<43>_<6>` | default 30 d, max 365 d (today's values, `handlers/apikeys.go:27-33`) | `SHA-256` |
| Bootstrap | `mcb_<43>_<6>` | 10 min, single use | `SHA-256` in `c/auth/bootstrap` + 0600 file |
| Invite | `mci_<43>_<6>` | default 72 h (today, `handlers/user.go:177-181`), single use | `SHA-256` |
| Temp-login | `mct_<43>_<6>` | 15 min, single use | `SHA-256` in storage (not a process map, R5) |
| Legacy (accepted, never issued) | `abk_…` keys; raw-ULID session cookies; raw `invite:` tokens | until their own expiry | §11 |

The prefixes are core's, not per-app: one binary may host several modules, and a key is a
core credential. `auth.Options.LegacyKeyPrefixes` (ABO: `["abk_"]`) lets the API-key
authenticator recognise old keys (the "presented key wins" rule needs to know a key when it sees
one, `internal/server/middleware/auth.go:76-99`). One-time tokens never travel in a URL path
(§4.13, SEC-21).

### 4.2 Request authentication order (stage 12)

0. If `core.auth.mode=disabled`: bind the auth-disabled principal (§4.11) and **ignore every
   presented credential** — a stolen key does not regain `AuthRequired` scopes in disabled mode
   (SEC-2).
1. If `HasUsers()` is false: bind nothing (anonymous); http-api stage 13 then allows only routes
   marked `rbac.Public` plus the setup/bootstrap routes (§4.6). **The API is no longer open while
   there are zero users** (today: everything reachable, `middleware/auth.go:165-169`).
2. An API key presented anywhere a credential is accepted (Bearer, session cookie value, and —
   only on facade groups — `X-Api-Key` / `apikey` query) wins, replacing anything else. Kept from
   `middleware/auth.go:173-176`.
3. Registered extension authenticators in `Order` (e.g. `cfaccess`). `ErrNoCredential` → next. A
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
must be `active` and unexpired; a `PurposeFacade` key is accepted only on its module's facade group;
the checker is `rbac.Service.Compile({Roles, Scopes, Interactive: method.Interactive()})`.

**Worker and facade confinement (SEC-3, SEC-5).** Confinement is not path-based and not a
namespace heuristic: `books:fingerprint.worker` and every `<m>:arr.*` scope are `Confining`
(05-rbac §3.2), and a principal holding only confining scopes satisfies only requirements that name
one of them — never `AnyAuthenticated` (05-rbac §4.10). This reproduces today's worker path
confinement (`middleware/auth.go:265-298`) for every route, including the 36 that had no permission.

### 4.3 Sessions (hashed at rest)

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
  sibling subdomain on the same site. Cookie-authenticated unsafe requests are therefore also
  checked by httpapi's cross-origin stage (05-http-api §4.2 stage 7, SEC-10).
- **Cookie failure rule (SEC-11, FE-6).** When the only credential presented is the session cookie
  and it is unknown, expired, revoked or epoch-stale, the failure is recorded with `Cookie=true`.
  On a `Public` route the request proceeds anonymous and the response clears the cookie, so a stale
  cookie never blocks login, setup, bootstrap or invite pages. On a non-Public route the answer is
  401 with the failure code (and the cookie is cleared). An invalid Bearer or API-key credential is
  a 401 on every route, Public included.
- Lookup is by `SHA-256(token)`; the token never touches storage, logs or API responses. Listing
  returns `SessionView` with `PublicID`; revoke takes `PublicID`. This closes the token leak through
  `GET /api/v1/auth/sessions` (§2).
- `LastSeen` is updated at most once per minute per session (in-memory coalescing, written with
  `NoSync`), so authentication stays a read on the hot path.
- Expiry sweep: core task `core.auth.sessions.prune` (hourly, `tasks.Schedule`; deletes
  expired/revoked records).

### 4.4 API keys

- Create: `Scopes` required and non-empty, no wildcards, each scope must be held by the actor's
  effective set (05-rbac §4.5). A key-authenticated actor may create keys only for itself, with
  scopes ⊆ its own key's effective set and expiry clamped to its own key's expiry (today's clamp,
  `handlers/apikeys.go:40-60`, kept). This closes the scoped-key → full-key escalation (§2).
- Creating a key for another user requires `core:users.manage`, an interactive method (guard),
  scopes ⊆ the **target's** role permissions and ⊆ the actor's, and that the actor dominates the
  target (05-rbac §4.7 rule 5).
- **Purpose (SEC-5, 08 O9 made normative).** `PurposeFacade` keys name a `Module`; every scope must
  be one of that module's `<m>:arr.*` scopes (all `Confining`), and the key is accepted only on that
  module's facade group. A general key holding `<m>:arr.*` scopes plus anything else is refused on
  the facade (08 §8.5's "every scope is arr" rule stays as a second check).
- Last-use tracking stays synchronous in the request goroutine (reason recorded at
  `middleware/auth.go:366-382`), coalesced per key per minute.
- Display: `id`, name, purpose, first 8 chars of the hash as identifier (today, `raw/10` §7),
  scopes, expiry, last used, status; the raw value is shown once.

### 4.5 Passwords

`auth/password` keeps argon2id m=64 MiB, t=3, p=2, 16-byte salt, PHC string, bcrypt verify-only
with transparent rehash, `oauth`/empty hash never authenticates (`absauth/password.go:40-47,131-190`).
Minimum length `core.auth.password.min_length` (default 8 — today's value; raising it is open item 2).
Hashing runs on a bounded semaphore (`runtime.NumCPU()` permits) because each verify allocates
64 MiB: an unbounded login burst would otherwise exhaust memory before the throttle trips.

### 4.6 Bootstrap and first run (G10: app-agnostic)

Core owns it; nothing refers to an app, a module or `users.manage`.

1. At every start, if `core.auth.bootstrap.enabled` (default true), mint `mcb_…`, write
   `bootstrap-token` (mode 0600) via `os.Root` in the app's state dir, store hash + expiry in
   `c/auth/bootstrap`, log the **path and expiry only** (never the value; today's rule,
   `bootstrap.go:165-175`).
2. `POST /api/core/v1/auth/bootstrap` (Public, `RateAuth`, per-client limiter): validate then spend
   atomically (`TakeOneTime` = get+delete in one batch — replaces the process mutex
   `bootstrapMu`), then find or create the admin: **first active user holding `core:admin`**
   (role-resolved, 05-rbac §4.6); none → create `admin` (suffixing on collision) with role
   `admin` and a generated password returned once. Returns an API key scoped `["core:admin"]`,
   TTL `core.auth.bootstrap.key_ttl` (8 h default, 24 h max — today's values). The key is
   non-interactive, so `Sensitive` scopes stay out of its reach (05-rbac §4.4). Audit:
   `core.auth.bootstrap.redeemed`.
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
   `core.auth.bootstrap.readonly_key.scopes` (default `["core:system.view"]`), not "all `*.view`".

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
  the same guard and audited (`core.auth.email_displaced`).
- Admission: provider allowlist of emails/domains, plus invite grants (`inviteallow:` today) via the
  `ExtraAllowed` hook (`internal/oauth/oauth.go:63-68`); new users get the provider's
  `default_role` (must exist and be assignable without escalation: it may not contain
  `core:admin`, and the key is `ProtectAdmin`).
- Ends in `IssueSession(origin = oidc:<provider>)` → `MethodSession` (interactive).
- Routes: `GET /api/core/v1/auth/providers` (Public), `GET …/auth/oidc/{provider}/start`,
  `GET …/auth/oidc/{provider}/callback` (Public, `RateAuth`).

### 4.8 Owner assurance and optional identity extensions (G11)

- `RequireOwner` (`internal/auth/owner.go:44-108`) becomes the route guard `Guards.Owner`: the
  principal must carry `AssuranceOwner`, which only an `OwnerVerifier` authenticator can set.
  `auth/cfaccess` is the only one shipped; deployments without it have no owner assurance, so
  owner-guarded routes (user merge, factory reset, restore while an owner is set — 9 routes today,
  `raw/10` §2.4) fall back to `Guards.Interactive` + `core:admin` **only if**
  `core.auth.owner.required=false` (default false when no OwnerVerifier is configured, true when
  one is). This keeps today's behaviour where `owner_email` unset means no owner gate.
- `auth/cfaccess` = verification only. Cloudflare **policy editing** (`internal/cfaccesspolicy`)
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
  failures would throttle every user. `auth/cfaccess` can mark the CDN header as believable for
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
API-key authenticator directly. Method `books.abs` is registered as non-interactive,
`MayChangeCredentials=false` (today's `MethodABS` semantics).

The *arr facade (08) uses core `PurposeFacade` API keys presented as `X-Api-Key`, `apikey` query or
Bearer; key extraction from the query string is enabled **only** on facade groups, and httpapi's
access-log and span redaction covers `apikey` and `token` query values (05-http-api §4.2 stages 2
and 4).

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
  `Guards.Interactive` route (password, keys, sessions of others, temp-login) is refused — today's
  credential guard refuses for the same reason (`credential_guard.go:28`).
- `disabled` refuses to start when a listener is bound to a non-loopback address **or** any trusted
  proxy or tunnel connector is configured (a local proxy makes remote callers look local, SEC-30),
  unless `core.auth.disabled_allow_remote=true` (today: a warning only, `server_lifecycle.go:1847`).
  This is a deliberate tightening; ABO's compatibility note is in 09-abo-on-core.
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

### 4.13 Core auth routes (all under `/api/core/v1`)

Registered by `core/internal/authhttp` (L6, imported only by `kernel`, 03 §3.3); `auth` itself does
not import `httpapi`. "Dominance" = 05-rbac §4.7 rule 5, enforced in `auth.Service` (SEC-4). No route carries a one-time
token in its path; tokens travel in the request body, and the shell reads them from the URL
fragment of the links it sends (`/invite#token=…`, `/login/temp#token=…`), which browsers never send
to a server (SEC-21).

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

The legacy `PUT /api/v1/auth/me/password` with a `user_id` body (sets another user's password,
`auth_reset_password.go:48-50`) is served during the window by the legacy mount, which from 09 S4
calls the dominance-checked service; the core API has no "set another user's password" route — an
admin uses the reset link. Legacy `GET /auth/temp-login?token=` and `/invite/:token` links stay
valid as aliases for the window (09 S4), and httpapi redacts the `token` query value and the legacy
invite path (05-http-api §4.2).

### 4.14 Post-restore hook (SEC-16)

`PostRestore` runs in the kernel after a restore swap and before listeners open, in one storage
batch per family, failing boot on error:

1. Re-apply every revocation recorded after the archive's `CreatedAt` (key revocations, user
   disables, role removals) from 05-backup's out-of-DB revocation journal (cross-cutting
   resolution 8), so a key revoked after the backup stays revoked.
2. Revoke every session and every one-time token (invite, temp-login, bootstrap); bump every user's
   `CredentialEpoch`. Everyone signs in again once.
3. When the revocation journal is unavailable or does not cover the whole interval (a foreign or
   host-loss restore), set every API key to `needs_confirmation`: refused until an admin re-enables
   it from the keys page. Otherwise keys stay active.
4. Audit `core.auth.restore_revoked` with counts per family.

## 5. Lifecycle

1. `app.New`: `rbac.Service` first (catalog frozen after module registration), then `auth.New`
   (loads `HasUsers`, seeds `_system`, stamps legacy keys lacking expiry — today's
   `stampNeverExpiringAPIKeys`, `internal/server/apikey_expiry_stamp.go` — and, during the
   compatibility window, rebuilds the core copies from legacy rows, §11), then extensions.
2. Mint bootstrap token (§4.6) before listeners open.
3. `kernel` registers the core auth routes through `internal/authhttp` on `CoreGroup`; register `core.auth.sessions.prune`,
   `core.auth.onetime.prune` tasks (hourly, `tasks.Schedule`).
4. Run: middleware serves; throttle sweeper and `LastSeen` flusher goroutines (one each) run until
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
| Bootstrap token wrong/expired/spent | 401 `bootstrap_invalid`, uniform message | closed | |
| Bootstrap file write fails at start | log error, bootstrap unavailable this run; setup still requires token → operator restarts after fixing perms | closed | |
| Key create with escalation | 403 `scope_escalation` | closed | |
| Action on a user the actor does not dominate | 403 `dominance_required` | closed | SEC-4 |
| Facade key off its facade group, or general key on a facade group | 403 `permission_denied` | closed | SEC-5 |
| OIDC subject whose email matches an unlinkable account | 409 `email_in_use` | closed | SEC-7 |
| Remove last `core:admin` holder | 409 `last_admin` | closed | lockout protection |
| Audit write fails for a sync auth event (login success, key create) | the operation fails 503 (same storage batch) | closed | credential changes must be attributable |
| Audit write fails for async event (login failure, denial) | counted, dropped | open | availability under attack |
| `LastSeen` flush fails | log, retry next tick | open | non-security metadata |
| OIDC callback state invalid/expired | 400 `oauth_state_invalid` | closed | CSRF |
| `disabled` mode on a non-loopback listener, or with a trusted proxy/connector, without opt-in | boot error | closed | SEC-30 |
| Post-restore hook fails | boot error | closed | a restore must never bring revoked credentials back |

## 7. Concurrency contract

- `Service` is goroutine-safe. Authentication on the hot path performs: one hash, one storage
  point read (session or key), one user read (cached by `(id, version)` in a bounded LRU, 4,096
  entries, invalidated by user writes and `core.principal.invalidated.v1`), one compiled
  permission lookup (05-rbac §9). No per-request goroutines.
- `HasUsers` is an `atomic.Bool` set by the first `CreateUser` commit; cleared only by factory reset.
- One-time tokens are spent with an atomic get+delete in a single storage batch; concurrent
  redeems of one bootstrap token: exactly one succeeds (test with `testing/synctest`).
- Password hashing: bounded semaphore (`runtime.NumCPU()`).
- `LastSeen` coalescer: one goroutine, buffered map under a mutex, flush every 60 s and on stop.
- Throttle: one instance, mutex-sharded LRU, one sweeper goroutine; all stopped by the app context
  (`wg.Go` in `Service.Run`).

## 8. Config keys

`Protection` levels are defined by 05-config; `admin` = `ProtectAdmin` (requires `core:admin` plus an
interactive method). Every key that decides who may sign in, with which role, or from where is
`admin` (SEC-6; it mirrors ABO's `FieldSignIn` class, `internal/config/protected_fields.go:53-66`).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.auth.mode` | enum `required`/`disabled` | `required` | no | admin |
| `core.auth.disabled_allow_remote` | bool | false | no | admin |
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
| `core.auth.oidc.redirect_base_url` | URL | — | yes | admin |
| `core.auth.oidc.providers[]` | list `{id, kind (oidc/github), issuer, client_id, client_secret (secret ref), scopes, allowed_emails, allowed_domains, default_role, link_existing_by_email}` | `[]` | yes (provider set rebuilt) | admin |
| `core.auth.owner.required` | bool | derived (§4.8) | no | admin |
| `core.auth.extensions.cfaccess.{team_domain,aud,owner_email}` | strings | unset (off) | no | admin |

ABO aliases (05-config; window per 09 §9.2.1): `enable_auth`, `oauth_*`, `cf_access_team_domain`,
`cf_access_aud`, `owner_email`, `oauth_default_role`, `bootstrap_key_ttl`
(`internal/config/config.go:1558,1586-1613,1753`).

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_auth_attempts_total` | counter | `method`, `outcome` (`success`,`invalid`,`expired`,`revoked`,`inactive`,`throttled`) |
| `mediacore_auth_sessions_active` | gauge | — |
| `mediacore_auth_api_keys_active` | gauge | `legacy` (`true`/`false`), `purpose` |
| `mediacore_auth_throttled_total` | counter | `surface` (`login`,`bootstrap`,`setup`,`invite`,`temp_login`,`oidc`) |
| `mediacore_auth_password_hash_seconds` | histogram | `op` (`hash`,`verify`) |
| `mediacore_auth_password_queue_wait_seconds` | histogram | — |
| `mediacore_auth_bootstrap_redemptions_total` | counter | `outcome` |
| `mediacore_auth_extension_errors_total` | counter | `extension` |
| `mediacore_auth_principal_invalidations_total` | counter | `reason` |
| `mediacore_auth_legacy_session_fallback_total` | counter | `outcome` (`upgraded`,`refused`) |

## 10. Tests

| Test | Asserts |
|---|---|
| `TestSession_TokenNeverStored` | after `IssueSession`, no storage value or key contains the token bytes |
| `TestSessions_ListNeverReturnsToken` | JSON of `GET /me/sessions` has no field equal to any live token (regression for `handlers/auth.go:513-537`) |
| `TestSession_LegacyRawULIDCookieUpgradesOnUse` | a legacy `sess:` cookie (epoch, status, revoked, expiry checked) yields a hashed `mcs_` session, a new cookie, and the legacy row deleted (SEC-19) |
| `TestCookie_SiblingAppCookieDoesNotBlockLogin` | two apps on one host: distinct default cookie names; a foreign or stale cookie on a Public route → anonymous + cleared (SEC-11, FE-6) |
| `TestStage12_InvalidKeyPlusValidSessionIs401` | stop rule (SEC-12) |
| `TestCredentialFormat_ChecksumAndPrefix` (+ `FuzzParseCredential`) | prefix, length, CRC; malformed → `ErrNoCredential` vs `ErrInvalidCredentials` correctly |
| `TestAPIKey_PresentedKeyWins` | key + session + CF assertion → method `api_key` |
| `TestAPIKey_CreateRequiresScopes` / `TestAPIKey_KeyCannotWiden` | empty scopes 400; key with `[books:view]` cannot mint `[books:edit]` or empty (regression for `handlers/apikeys.go:199-226`) |
| `TestAPIKey_FacadePurposeBoundToModule` | facade key refused off its facade group; general key with extra scopes refused on it (SEC-5) |
| `TestAPIKey_LegacyAbkAcceptedUntilExpiry` | |
| `TestFirstRun_OnlyPublicAndSetupReachable` | zero users: every non-Public route 401 |
| `TestSetup_RequiresBootstrapToken` / `TestSetup_LoopbackExceptionRefusesForwarded` | loopback peer with a forwarding header, or a trusted-proxy peer, is not local (SEC-30) |
| `TestBootstrap_SingleUseConcurrent` | `testing/synctest`, 50 concurrent redeems, exactly 1 success |
| `TestBootstrap_AdminIsCoreAdminHolder` | picks a `core:admin` holder, not a `users.manage` holder |
| `TestCredentialEpoch_KillsSessionsAndOneTimeTokens` | |
| `TestThrottle_SourceAndAccount` | `synctest` clock; soft delays, 429, LRU pinning |
| `TestPasswordHash_BoundedConcurrency` | at most `NumCPU` verifies in flight |
| `TestAuthDisabled_AuthRequiredScopesStill401` | |
| `TestAuthDisabled_InteractiveGuardRefuses` / `TestAuthDisabled_PresentedKeyIgnored` | SEC-2 |
| `TestAuthDisabled_RefusesNonLoopbackOrProxyWithoutOptIn` | SEC-30 |
| `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser` | SEC-4 (also listed in 05-rbac) |
| `TestOIDC_LinksBySubjectNotEmail` / `TestOIDC_PKCEForAllProviders` | fake IdP (httptest) |
| `TestOIDC_UntrustedProviderCannotLinkByEmail` | provider without `link_existing_by_email`, or a target holding `core:admin` or a password → `email_in_use` (SEC-7) |
| `TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough` | |
| `TestCFAccess_JWKSOutageFallsThrough` | |
| `TestOwnerGuard_RequiresOwnerAssurance` | |
| `TestPrincipalFor_CompatAppliesEpochAndScopes` | the ABS path (G15) |
| `TestLastAdmin_CannotBeDisabledOrDemoted` | |
| `TestPostRestore_RevokedKeyStaysRevoked` / `TestPostRestore_NoJournalKeysNeedConfirmation` | SEC-16 |
| `TestAccessLog_NoInviteToken` | (in 05-http-api) no one-time token in access log, journal or span attributes (SEC-21) |
| `TestNoGlobals` (archtest) | temp-login state and throttles are instance state (R5) |

## 11. Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/auth/context.go`, `owner.go`, `user_active.go` | `core/auth` (`Principal`, `Method`, assurance) |
| `internal/server/middleware/auth.go` (`RequireAuth`, key auth, `EffectivePermissionsFor`) | `auth.Service.Middleware` + `rbac.Service.Compile`; `RequirePermission`/`RequireAdmin` → httpapi stage 13; inside the legacy mount from 09 S4, `RequireAuth` becomes the read-only bridge of 05-http-api §8.3 |
| `internal/server/middleware/cfaccess.go`, `internal/oauth/cfaccess.go` | `core/auth/cfaccess` |
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

**Stored data (cross-cutting resolution 5; DATA-3, DATA-5, SEC-1, SEC-19).** The identity families are
small. During the compatibility window (09 §9.2.1) **legacy rows stay authoritative**: the new build
writes the legacy row, its `idx:` rows and the core row in one batch, and rebuilds the core copy
from legacy at every boot, so a change made by an older build after a rollback (a deactivation, a
revoked key, a password reset that bumped the epoch) is always honoured. Native-only data lives in
core side records keyed by the legacy ID, merged at rebuild and dropped when the legacy row is gone
or revoked. Contract C4 (09) stops the mirror and deletes the legacy rows.

| Legacy family | New | Transform |
|---|---|---|
| `u:` + `idx:user:username:` (and the other `idx:` user sub-prefixes) | `c/auth/user/…`, `c/auth/idx/…` | user IDs and role IDs unchanged (`admin`, `editor`, `viewer`, `fp-worker`; 05-rbac §6.2); mirrored both ways as above |
| `apikey:` + `idx:apikey:` | `c/auth/apikey/…` | hash reused as-is (already SHA-256 of raw); scopes translated (05-rbac §6.1); empty scopes → `LegacyFullScope=true`; native keys mirrored with legacy atoms only on exact re-expansion, otherwise mirrored **revoked** with canonical scopes and `Purpose` in the side record (05-rbac §6.3) |
| `sess:<raw>` + `idx:sess:user:` | `c/auth/session/h/<sha256>` + new `PublicID` | **no raw mirror** (SEC-19): new sessions are hashed only. A presented legacy raw-ULID cookie is looked up in `sess:` with epoch, status, revoked and expiry checks; on a hit the service issues a hashed `mcs_` session, sets the cookie and deletes the legacy row in one batch (upgrade on use). Legacy sessions live at most 7 d (`handlers/auth.go:90-96`), so the fallback matches nothing after that; its code is removed by the first contract PR after S4 + 7 days, independent of C4. Cost: a rollback signs users out once; API keys, which automation uses, keep working |
| `invite:<raw>` | `c/auth/onetime/invite/<sha256>` | legacy invites stay readable in place by presented token until they expire (72 h); native invites are hash-only and not mirrored (a rollback within an invite's TTL voids invites issued by the new build; resend) |
| `oauthid:`, `inviteallow:` | `c/auth/ident/…`, `c/auth/inviteallow/…` | mirrored both ways as above |
| bootstrap hash in settings | `c/auth/bootstrap` | regenerated at first start anyway |
| temp-login map | — | in-memory today; nothing to migrate |

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
| G11 owner/CF machinery embedded in auth | **Fixed**: `Authenticator`/`OwnerVerifier` extension point; `auth/cfaccess` optional; policy editing and service tokens stay in ABO (§4.8) |
| G12 sessions unhashed (secrets part → 05-config) | **Fixed for auth credentials**: every session/invite/temp-login/bootstrap/key stored as SHA-256 (§4.1, §4.3); no raw session mirror (§11); config secrets → `core/secrets` (05-config) |
| G14 rate limit/lockout tied to tunnel topology, in memory | **Fixed + documented**: instance throttle, client address from `httpapi` trusted-proxy config with the connector set migrated, no hard lockout, in-memory by design (§4.9) |
| G15 two parallel auth stacks | **Fixed**: compat stays in app, resolves via `PrincipalFor` (§4.10) |

## Objections to 03

None. (03 §3.3 places middleware in `auth`; it is a plain `func(http.Handler) http.Handler`, so
`auth` (L4) does not import `httpapi` (L6), consistent with R4.)

## Out-of-scope defects found

These are live in ABO at HEAD and are fixed by this design, but ABO should not wait for the
extraction; each is a small hotfix in the current code:

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

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SEC-1 — native keys mirrored revoked unless exact legacy re-expansion; side record (§11).
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
- OPS-4 — same fix as SEC-1.
- OPS-11 — auth engine moves in exactly one stage (09 S4); legacy-mount bridge referenced (header, §11).
- DATA-3 — legacy authoritative, rebuild at boot, side records (§11).
- COR-19 — layers per 03 v1.2.0 (§3).
- Cross-cluster (R-A) — auth routes registered from `internal/authhttp` (§4.13, §5).
- COR-20 — `*events.Bus`, `*metrics.Scope` in `Deps` (§3.2).
- SIMP-6 — prune tasks on `tasks.Schedule` (§4.3, §5).
- SIMP-23 — `auth/clientcert` dropped (§3.3, §4.8, §8).
- FE-6 — per-app cookie names; foreign cookie never causes a 401 on Public routes (§4.3).
- FE-7 — Public auth flows answer 401 with domain codes, shown in place (§6).
- FE-19 — served at the origin root only; cookie `Path=/` (§4.3).
