<!-- file: docs/specs/platform-core/05-authn.md -->
<!-- version: 1.0.0 -->
<!-- guid: 73c4823d-8269-4c1d-8809-4006a6da6f0d -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authentication (`core/auth`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(gaps G1–G15; the full disposition table is in [05-rbac](05-rbac.md) §13, the authN-owned rows are
repeated in §13 here), [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Permissions/roles/audit: [05-rbac](05-rbac.md). Route chain: [05-http-api](05-http-api.md) §4.2.
Secret storage: [05-config](05-config.md) (`core/secrets`).

## 1. Purpose & scope

`core/auth` answers "who is calling, by which method, with what assurance" for every app built on
core, and owns the lifecycle of every core credential:

- **users** (single tenant, multi-user, D7), passwords, statuses, credential epochs;
- **sessions** (browser), **API keys** (automation), **bootstrap token** (first admin / recovery),
  **invites** and **temp-login links** (delegated sessions);
- **OIDC/OAuth** sign-in (generic OIDC + GitHub OAuth2) ending in a core session;
- the **authenticator extension point** for optional identity sources (Cloudflare Access, mTLS
  client certificates — G11) and for app compatibility surfaces (ABS — G15);
- login throttling and lockout policy (G14), and the **auth-off** mode semantics.

It does **not** decide what a principal may do (that is `rbac`), does not store secrets other than
its own credential hashes (that is `secrets`), and does not own app protocols (ABS JWTs stay in the
`books` module).

## 2. Current state at HEAD

| Topic | Today | Anchor |
|---|---|---|
| User model | `User{ID, Username, Email, PasswordHashAlgo, PasswordHash, Roles []string, Status, CredentialEpoch, EmailSource, Version}` | `internal/database/store.go:734-780` |
| Auth plumbing | `auth.WithUser/WithPermissions/WithMethod/Can`; `Method` enum `session`, `session_delegated`, `api_key`, `cf_access`, `abs`; `WithMethod` can only downgrade interactivity; `MayChangeCredentials` allowlist | `internal/auth/context.go:33-110,171-178` |
| Request authentication | `RequireAuth`: store nil → pass; **0 users → pass everything**; presented `abk_` key wins; CF-bound user skips session; session lookup by **raw id**, revoked/expired, epoch, status; worker-only confinement | `internal/server/middleware/auth.go:152-262,289-306` |
| Per-request `CountUsers()` | called by `RequireAuth` and again by every `HoldsPermission` | `internal/server/middleware/auth.go:160-169,498-504` |
| Sessions | id = ULID (`crypto/rand`, 80 random bits), stored **unhashed** as Pebble key `sess:<id>` + `idx:sess:user:<user>:<id>`; TTL 24 h, remember-me 7 d | `internal/database/pebble_store_auth.go:1656-1675`, `internal/database/pebble_store.go:1050-1056`, `raw/10` §2.3 |
| Session listing leaks tokens | `GET /api/v1/auth/sessions` returns the embedded `database.Session` (field `id` = bearer token) of **every** live session of the caller; the route has no permission and no credential guard, so an API key can read the user's session tokens | `internal/server/handlers/auth.go:513-537`, `raw/09` §4 rows 838-839 |
| API keys | `abk_` + base64url(32 B); SHA-256 hex stored; mandatory expiry (default 30 d, max 365 d); `TouchAPIKeyLastUsed` synchronous | `internal/database/apikey_token.go:16-35`, `internal/server/handlers/apikeys.go:27-33`, `internal/server/middleware/auth.go:309-400` |
| Key minted by a key | scopes checked ⊆ caller's effective set, **but empty scopes skip the check** and an empty-scope key means "all role permissions" | `internal/server/handlers/apikeys.go:199-226`, `internal/server/middleware/auth.go:405-408` |
| Bootstrap | `abbs_` token, 10 min TTL, 0600 file in secure state dir, hash in settings, single use under mutex; exchanged at public `POST /api/v1/auth/bootstrap` for an admin API key (8 h default, 24 h max); admin = first active user whose role holds `users.manage`, else generated | `internal/server/bootstrap.go:40,140-170,211-292,305-470,484-560` |
| First-run setup | public `POST /api/v1/auth/setup` creates the first admin (409 once a user exists); whole API open while 0 users | `internal/server/handlers/auth.go:267-292`, `raw/10` §5 |
| Temp-login | 16-byte hex tokens in a **process-global map**, 15 min, single use; creates a delegated 24 h session | `internal/server/auth_temp_login.go:30-75,161-227` |
| Invites | raw token as Pebble key `invite:<token>`, bound to `RoleID`, rate-limited lookup/accept | `internal/database/pebble_store_auth.go:1347,1362`, `internal/server/invite_rate_limit.go:334-465` |
| Passwords | argon2id m=64 MiB t=3 p=2, 16-byte salt; bcrypt verify + rehash; min length 8 | `internal/server/absauth/password.go:40-47,131-190`, `internal/server/handlers/auth.go:279-282` |
| OAuth/OIDC | GitHub OAuth2 (no PKCE), Google OIDC (go-oidc, PKCE S256); signed state; link by `(provider, subject)`; email allowlist + invite grants; default role; **no generic OIDC** | `internal/oauth/providers.go:23-180`, `internal/oauth/oauth.go:32-75`, `internal/oauth/resolve.go:65-205`, `internal/oauth/state.go:55-132` |
| Cloudflare Access | fail-open binder before `RequireAuth`; JWT verified (issuer, AUD, expiry); allowlisted email resolves a user; service tokens verify but resolve none | `internal/server/middleware/cfaccess.go:20-139`, `internal/oauth/cfaccess.go:74-97` |
| Owner proof | owner actions need a verified CF Access JWT whose email equals `owner_email` | `internal/auth/owner.go:31-108`, `internal/server/middleware/owner_guard.go:51-89` |
| CF policy editing / service tokens | `internal/cfaccesspolicy` (edits Access policy rules), `internal/cfservicetoken` (mints guest tokens) | `internal/cfaccesspolicy/client.go:83-402` |
| mTLS | CA/PSK dir + client-cert TLS config + stdio bridge; **not** HTTP auth; only importer is `cmd/mtls-bridge` (MCP bridge) | `internal/mtls/transport.go:15-57`, `cmd/mtls-bridge/main.go:1-25` |
| Throttling | ABS/web login throttle: per-IP failure counter (15 / 15 min), per-account soft delay after 5 (200 ms steps, max 2 s), LRU 10,000 keys with pinning; aggregate login ceiling; bootstrap and invite limiters; client key from CF-Connecting-IP only when believable | `internal/server/absauth/throttle.go:50-153`, `internal/server/login_ceiling.go`, `internal/server/bootstrap_rate_limit.go:194`, `internal/server/client_trust.go:312` |
| ABS compat auth | separate `ABSRequireAuth` (HS256 JWT + refresh, `abk_` keys, CF), separate `abs_sess:` store, resolves to the same `User` and role union | `internal/server/middleware/absauth.go:621,778-800`, `internal/database/pebble_store_abssession.go:49` |
| Auth off | `enable_auth=false`: auth + `s.perm` + `adminRole` are pass-through; "always-authenticated" admin routes still 401 | `internal/server/server_lifecycle.go:1693-1698,1843-1848`, `internal/server/wire_admin_role.go:18-45` |
| Session store hashing of other secrets | API keys and bootstrap hashed; sessions, invites, temp-login **raw** | `raw/10` §7 |

## 3. Go API

Package `github.com/falkcorp/mediacore/auth` (layer with `rbac`, `audit`, `tasks`; imports `rbac`,
`audit`, `storage`, `config`, `secrets`, `log`, `metrics`, `events`). Subpackages:
`auth/password`, `auth/oidc`, `auth/cfaccess` (optional extension), `auth/clientcert` (optional
extension). Storage keyspace `c/auth/` (05-storage).

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
	DisplayName     string
	Roles           []rbac.RoleName
	Status          UserStatus
	CredentialEpoch int64 // bumped on password change/reset, disable, enable, factory credential reset
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
// (internal/auth/context.go:63-90); unknown methods are non-interactive and
// may not change credentials (fail closed). Extensions declare their own
// classification at registration (MethodSpec).
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
	User         *User           // nil only for MethodAuthDisabled
	Method       Method
	CredentialID string          // session public id or API key id; never a secret
	Perms        rbac.Checker    // roles ∩ key scopes, compiled (05-rbac §4.4)
	Assurance    Assurance
	AuthTime     time.Time       // when the credential was issued / sign-in happened
	ClientAddr   netip.Addr
}

func WithPrincipal(ctx context.Context, p *Principal) context.Context
func PrincipalFrom(ctx context.Context) (*Principal, bool)

// System returns the principal used by background tasks (the _system user,
// internal/auth/seed.go:134-152), with an explicit permission set; never
// reachable over HTTP.
func System(perms rbac.Checker) *Principal
```

### 3.2 Service

```go
type Service struct{ /* unexported */ }

func New(ctx context.Context, opts Options, deps Deps) (*Service, error)

type Deps struct {
	Store   Store          // over storage keyspace c/auth/
	RBAC    *rbac.Service  // effective permissions
	Audit   audit.Logger
	Secrets secrets.Store  // OIDC client secrets, cookie keys
	Events  events.Bus     // publishes core.principal.invalidated.v1
	Clock   func() time.Time
	Log     *slog.Logger
	Metrics *metrics.Registry
	StateDir *os.Root      // bootstrap token file (os.Root-confined)
}

// Users.
func (s *Service) CreateUser(ctx context.Context, in NewUser) (*User, error)
func (s *Service) User(ctx context.Context, id UserID) (*User, error)
func (s *Service) UserByUsername(ctx context.Context, name string) (*User, error)
func (s *Service) Users(ctx context.Context, f UserFilter) iter.Seq2[*User, error]
func (s *Service) SetPassword(ctx context.Context, id UserID, newPassword string, opt SetPasswordOpts) error
func (s *Service) SetStatus(ctx context.Context, id UserID, st UserStatus) error   // bumps epoch on disable/enable
func (s *Service) SetRoles(ctx context.Context, actor *Principal, id UserID, roles []rbac.RoleName) error // rbac escalation rules, 05-rbac §4.7
func (s *Service) HasUsers() bool // atomic flag, no store read

// Password sign-in. Throttled (§4.9); returns ErrInvalidCredentials for unknown
// user or wrong password with equal timing (dummy-hash derivation kept).
func (s *Service) Login(ctx context.Context, in LoginInput) (*IssuedSession, error)

// Sessions.
func (s *Service) IssueSession(ctx context.Context, u *User, origin SessionOrigin, remember bool, client ClientInfo) (*IssuedSession, error)
func (s *Service) Sessions(ctx context.Context, user UserID) iter.Seq2[SessionView, error] // never contains a token
func (s *Service) RevokeSession(ctx context.Context, actor *Principal, publicID string) error
func (s *Service) RevokeAllSessions(ctx context.Context, user UserID, except string) error

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
	Scopes    []rbac.Permission   // REQUIRED non-empty, no wildcards (05-rbac §4.5)
	ExpiresIn time.Duration  // 0 = default; > max = error
}

// Bootstrap (§4.6).
func (s *Service) RedeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error)

// Compatibility surfaces and extensions build principals through this; it
// applies status, epoch, role resolution and scope intersection exactly like
// core credentials (G15).
func (s *Service) PrincipalFor(ctx context.Context, u *User, m Method, scopes []rbac.Permission, epoch int64) (*Principal, error)

// Middleware returns http-api stage 11 (binds a principal or leaves the request
// anonymous; never writes a response except 503 on store failure).
func (s *Service) Middleware() func(http.Handler) http.Handler

// Revalidate reports whether a long-lived principal (an SSE stream) still
// stands: user active, epoch unchanged, credential not revoked. Cheap: served
// from an in-memory generation table invalidated by events.
func (s *Service) Revalidate(ctx context.Context, p *Principal) error
```

### 3.3 Authenticators (extension point, G11/G15)

```go
// Authenticator inspects a request and either returns a Principal, or
// ErrNoCredential to let the next one try, or another error to refuse the
// request (401, fail closed: a presented-but-invalid credential never falls
// through to a weaker one).
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
	ErrNoCredential        = errors.New("auth: no credential presented")
	ErrInvalidCredentials  = errors.New("auth: invalid credentials")
	ErrExpired             = errors.New("auth: credential expired")
	ErrRevoked             = errors.New("auth: credential revoked")
	ErrInactiveUser        = errors.New("auth: user not active")
	ErrStaleEpoch          = errors.New("auth: credentials changed since issue")
	ErrThrottled           = errors.New("auth: throttled")
	ErrBootstrapInvalid    = errors.New("auth: bootstrap token invalid, expired or spent")
	ErrScopeEscalation     = errors.New("auth: requested scopes exceed caller's")
	ErrLastAdmin           = errors.New("auth: change would leave no active core:admin holder")
	ErrStoreUnavailable    = errors.New("auth: credential store unavailable")
)
```

Optional extensions shipped by core (each a separate package, wired only when configured):

```go
package cfaccess // github.com/falkcorp/mediacore/auth/cfaccess

// New verifies Cf-Access-Jwt-Assertion / CF_Authorization (issuer, AUD, expiry,
// JWKS via go-oidc), resolves an allowlisted email to a user through
// auth.Service.PrincipalFor, and, when OwnerEmail matches, sets AssuranceOwner.
func New(ctx context.Context, cfg Config, svc *auth.Service) (auth.Authenticator, error)

type Config struct {
	TeamDomain string
	AUD        string
	OwnerEmail string   // optional
	TrustClientIPHeader bool // when verified, believe the CDN client-IP header (feeds httpapi client address)
}
```

```go
package clientcert // github.com/falkcorp/mediacore/auth/clientcert

// New authenticates a TLS client certificate (listener uses
// tls.VerifyClientCertIfGiven with the configured CA pool) and maps it to a
// user by a configured rule (SAN URI "mediacore:user:<username>" or exact
// subject CN). Method "ext:clientcert", non-interactive.
func New(cfg Config, svc *auth.Service) (auth.Authenticator, error)
```

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
| Legacy (accepted, never issued) | `abk_…` keys; raw-ULID session cookies | until their own expiry | migrated hashes (§11) |

The prefixes are core's, not per-app: one binary may host several modules, and a key is a
core credential. `auth.Options.LegacyKeyPrefixes` (ABO: `["abk_"]`) lets the API-key
authenticator recognise old keys (the "presented key wins" rule needs to know a key when it sees
one, `internal/server/middleware/auth.go:76-99`).

### 4.2 Request authentication order (stage 11)

1. If `HasUsers()` is false: bind nothing (anonymous); http-api stage 12 then allows only routes
   marked `rbac.Public` plus the setup/bootstrap routes (§4.6). **The API is no longer open while
   there are zero users** (today: everything reachable, `middleware/auth.go:165-169`).
2. An API key presented anywhere a credential is accepted (Bearer, session cookie value, and —
   only on facade groups — `X-Api-Key` / `apikey` query) wins, replacing anything else. Kept from
   `middleware/auth.go:173-176`.
3. Registered extension authenticators in `Order` (e.g. `cfaccess`). `ErrNoCredential` → next;
   other error → 401 (a *presented* invalid assertion does not fall through — unlike today's
   fail-open CF binder; the fail-open behaviour existed only because CF was optional, and is kept by
   returning `ErrNoCredential` when no assertion is present).
4. Session token from Bearer or the session cookie.
5. Nothing → anonymous.

On success: user `Status` must be active; epoch must match (sessions, temp-login, invites); key
must be active and unexpired; permissions = `rbac.Effective(user)` ∩ key scopes. Worker-only
confinement (`middleware/auth.go:271-306`) is no longer path-based: a credential whose scopes are
exactly `books:fingerprint.worker` simply holds nothing else, and since every route now declares a
permission (R6), the 36 "any authenticated" holes it plugged no longer exist; routes that remain
`rbac.AnyAuthenticated` are refused to principals whose effective set is empty or worker-only by the
rule "`AnyAuthenticated` requires at least one permission in the route owner's namespace or
`core:self`" (05-rbac §4.10).

### 4.3 Sessions (hashed at rest)

- Issued by password login, OIDC callback, temp-login consumption, invite acceptance.
- Cookie: name `core.auth.session.cookie_name` (default `mc_session`; ABO sets `session_id` for
  continuity), `HttpOnly`, `SameSite=Strict`, `Secure` when the request is HTTPS or via a trusted
  https proxy, `Path=/`. Bearer accepted for non-browser clients.
- Lookup is by `SHA-256(token)`; the token never touches storage, logs or API responses. Listing
  returns `SessionView` with `PublicID`; revoke takes `PublicID`. This closes the token leak through
  `GET /api/v1/auth/sessions` (§2).
- `LastSeen` is updated at most once per minute per session (in-memory coalescing, written with
  `NoSync`), so authentication stays a read on the hot path.
- Expiry sweep: core task `core.auth.sessions.prune` (hourly; deletes expired/revoked records).

### 4.4 API keys

- Create: `Scopes` required and non-empty, no wildcards, each scope must be held by the actor's
  effective set (05-rbac §4.5). A key-authenticated actor may create keys only for itself, with
  scopes ⊆ its own key's effective set and expiry clamped to its own key's expiry (today's clamp,
  `handlers/apikeys.go:40-60`, kept). This closes the scoped-key → full-key escalation (§2).
- Creating a key for another user requires `core:users.manage`, an interactive method (guard), and
  scopes ⊆ the **target's** role permissions and ⊆ the actor's.
- Last-use tracking stays synchronous in the request goroutine (reason recorded at
  `middleware/auth.go:366-382`), coalesced per key per minute.
- Display: `id`, name, first 8 chars of the hash as identifier (today, `raw/10` §7), scopes,
  expiry, last used; the raw value is shown once.

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
   TTL `core.auth.bootstrap.key_ttl` (8 h default, 24 h max — today's values). Audit:
   `core.auth.bootstrap.redeemed`.
3. `POST /api/core/v1/auth/setup` (first admin with chosen password) is reachable only while
   `HasUsers()` is false **and** the request presents the bootstrap token, unless
   `core.auth.setup_requires_token=false` *and* the peer is loopback. The shell's setup wizard asks
   for the token and tells the operator where the file is. (Today setup is public with no token,
   `handlers/auth.go:267-292`.)
4. Multiple apps sharing one DB is not a supported topology (D3/03 §3.7: one process per app owns
   its storage); in the multi-app-in-one-binary case there is one `auth.Service`, so one bootstrap
   token, which is what G10 asked for.
5. The optional startup read-only key (`bootstrap.go:52-131`) becomes
   `core.auth.bootstrap.readonly_key.enabled` producing a key with the scopes listed in
   `core.auth.bootstrap.readonly_key.scopes` (default `["core:system.view"]`), not "all `*.view`".

### 4.7 OIDC / OAuth

- `auth/oidc` provides **generic OIDC** (discovery via `github.com/coreos/go-oidc/v3` v3.21.0,
  already a dependency, `go.mod:9`; `golang.org/x/oauth2` v0.37.0) configured as a list, plus a
  `github` kind for GitHub's non-OIDC OAuth2. Google becomes one configured OIDC provider
  (issuer `accounts.google.com`).
- PKCE S256 for every provider, including GitHub (GitHub supports PKCE; today it is omitted,
  `raw/10` §2.3). Signed state codec kept (`internal/oauth/state.go:55-132`), keyed from
  `secrets`, 10 min TTL.
- Linking strictly by `(provider, subject)`; email is never a join key, except the existing
  verified-email takeover rule (`internal/oauth/resolve.go:177-205`) which is kept and audited
  (`core.auth.email_displaced`).
- Admission: provider allowlist of emails/domains, plus invite grants (`inviteallow:` today) via the
  `ExtraAllowed` hook (`internal/oauth/oauth.go:63-68`); new users get the provider's
  `default_role` (must exist and be assignable without escalation: it may not contain
  `core:admin`).
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
- `auth/clientcert` is a new optional authenticator for operators who want mTLS on the HTTP
  listener. `internal/mtls` (CA/PSK provisioning, stdio bridge) and `cmd/mtls-bridge` stay in ABO:
  they bridge an MCP protocol between hosts and have nothing to do with HTTP identity
  (`cmd/mtls-bridge/main.go:1-25`).

### 4.9 Throttling and lockout (G14)

- **No hard account lockout** (it is a denial-of-service lever against known usernames). Instead,
  kept from `absauth/throttle.go:50-153`: per-source failure counter (15 failures / 15 min → 429 for
  the window), per-account soft delay after 5 failures (200 ms steps, cap 2 s), aggregate ceiling
  across all sources (`login_ceiling.go`), LRU of 10,000 keys with pinning of saturated entries.
- One `auth.Throttle` instance per `Service` (not package state, R5) applied to: password login,
  bootstrap redeem, setup, invite lookup/accept, temp-login consume, OIDC callback.
- Client address comes from `httpapi` (`TrustedProxies` + `ClientIPHeader`, 05-http-api §3.5),
  replacing the Cloudflare-tunnel-specific derivation; `auth/cfaccess` can mark the CDN header as
  believable for verified requests (today's rule, `middleware/cfaccess.go:24-41`).
- State is in-memory: a restart resets counters. Documented as core behaviour (single process per
  storage); persisting counters would turn every failed login into a durable write.

### 4.10 Compatibility identities (G15)

App protocol auth stays in the app. ABO's ABS surface keeps its HS256 JWTs, refresh derivation and
`abs_sess:` sessions inside `books` (a legacy key claim, 05-storage), and its routes are a
`LegacyAuthOwn` mount (05-http-api §8.2). Every ABS credential resolves through
`auth.Service.PrincipalFor(ctx, user, "books.abs", scopes, epoch)`, so status, credential epoch,
role resolution and scope intersection are core's — replacing the duplicate role walk
`absEffectivePermissions` (`middleware/absauth.go:778-800`). ABS's use of `abk_` keys calls the core
API-key authenticator directly. Method `books.abs` is registered as non-interactive,
`MayChangeCredentials=false` (today's `MethodABS` semantics).

The *arr facade (08) uses core API keys presented as `X-Api-Key`, `apikey` query or Bearer; key
extraction from the query string is enabled **only** on facade groups, and the access log's query
redaction covers `apikey`.

### 4.11 Auth-off mode

`core.auth.mode = required | disabled` (default `required`; ABO's `enable_auth` aliases it).

- `disabled`: stage 11 binds a synthetic principal `{Method: auth_disabled, Perms: all scopes
  except those declared AuthRequired}`. `AuthRequired` is a permission-definition flag
  (05-rbac §3.2) set on destructive and credential scopes — exactly today's "always-authenticated"
  admin route class (`wire_admin_role.go:18-45`): store wipes, debug edits, credential surfaces.
  Those routes answer 401 with auth disabled, as today.
- `disabled` refuses to start when a listener is bound to a non-loopback address unless
  `core.auth.disabled_allow_remote=true` (today: a warning only, `server_lifecycle.go:1847`).
  This is a deliberate tightening; ABO's compatibility note is in 09-abo-on-core.
- Audit records in disabled mode carry `method=auth_disabled` and the client address.

### 4.12 Credential epochs and revocation

- `User.CredentialEpoch` bumps on password change/reset, disable, enable, and "sign out
  everywhere". Sessions, temp-login and invite tokens carry the epoch at issue; mismatch → refused
  and deleted (`middleware/auth.go:219-234` semantics kept). Self-service password change re-stamps
  the caller's own session in the same batch (today's `keepSessionID`).
- API keys are **not** epoch-bound (automation survives a password change, as today) but die on
  disable.
- Role or role-definition changes do not bump epochs; permissions are recomputed from an `rbac`
  generation counter (05-rbac §9), so changes apply on the next request.
- Every revocation publishes `core.principal.invalidated.v1{user_id, credential_id?, reason}` on
  `events`; `realtime` closes affected streams (05-realtime §4.6), `Revalidate` uses it.

### 4.13 Core auth routes (all under `/api/core/v1`)

| Method + path | Perm | Guards |
|---|---|---|
| `GET /auth/status` | Public | — |
| `POST /auth/setup` | Public (zero users + token) | `RateAuth` |
| `POST /auth/bootstrap` | Public | `RateAuth` |
| `POST /auth/login` / `POST /auth/logout` | Public / AnyAuthenticated | `RateAuth` |
| `GET /auth/providers`, `GET /auth/oidc/{provider}/start`, `…/callback` | Public | `RateAuth` |
| `GET /me`, `PATCH /me` | AnyAuthenticated / AnyAuthenticated | `PATCH`: Interactive |
| `PUT /me/password` | AnyAuthenticated | Interactive |
| `GET /me/sessions`, `DELETE /me/sessions/{publicID}` | AnyAuthenticated | `DELETE`: Interactive |
| `GET/POST /me/api-keys`, `PATCH/DELETE /me/api-keys/{id}` | AnyAuthenticated | key may manage only keys it may create (§4.4) |
| `GET /users`, `GET /users/{id}` | `core:users.view` | — |
| `POST /users/invites`, `DELETE /users/invites/{id}`, `POST /users/{id}/disable`, `…/enable`, `…/reset-password`, `PUT /users/{id}/roles` | `core:users.manage` | Interactive |
| `GET /users/{id}/sessions`, `DELETE …/{publicID}`, `GET/POST /users/{id}/api-keys` | `core:users.manage` | Interactive for writes |
| `GET /auth/invites/{token}`, `POST /auth/invites/accept` | Public | `RateAuth` |
| `POST /users/{id}/temp-login` | `core:users.manage` | Interactive |
| `GET /auth/temp-login` (consume, sets cookie, redirects) | Public | `RateAuth` |

## 5. Lifecycle

1. `app.New`: `rbac.Service` first (catalog frozen after module registration), then `auth.New`
   (loads `HasUsers`, seeds `_system`, stamps legacy keys lacking expiry — today's
   `stampNeverExpiringAPIKeys`, `internal/server/apikey_expiry_stamp.go`), then extensions.
2. Mint bootstrap token (§4.6) before listeners open.
3. Register core auth routes on `CoreGroup`; register `core.auth.sessions.prune`,
   `core.auth.onetime.prune` tasks (hourly).
4. Run: middleware serves; throttle sweeper and `LastSeen` flusher goroutines (one each) run until
   the app context ends; the flusher drains pending `LastSeen` writes on shutdown (bounded 2 s).

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| No credential | anonymous; route decides (401 if non-Public) | closed | |
| Presented credential invalid/expired/revoked | 401 with `code` (`invalid_credential`, `expired`, `revoked`), no fall-through to weaker credential | closed | today's key-wins rule, generalized |
| Store read error during authentication | 503 `auth_unavailable` | closed | never degrade to anonymous-with-access |
| `HasUsers` false | only Public + setup/bootstrap | closed | today it is fully open (§4.2) |
| User inactive / epoch stale | 401 `inactive_user` / `expired` (session deleted) | closed | |
| Extension authenticator internal error (JWKS fetch fails) | treat as `ErrNoCredential` + warn + `mediacore_auth_extension_errors_total`; other authenticators still run | open (to the next method) | an outage of an *optional* IdP must not lock out password/key users; a presented assertion that *verifies false* is still 401 |
| Throttle tripped | 429 `throttled` + `Retry-After` | closed | |
| Password hash semaphore saturated (> 2 s wait) | 503 `busy` + `Retry-After` | closed | memory protection |
| Bootstrap token wrong/expired/spent | 401 `bootstrap_invalid`, uniform message | closed | |
| Bootstrap file write fails at start | log error, bootstrap unavailable this run; setup still requires token → operator restarts after fixing perms | closed | |
| Key create with escalation | 403 `scope_escalation` | closed | |
| Remove last `core:admin` holder | 409 `last_admin` | closed | lockout protection |
| Audit write fails for a sync auth event (login success, key create) | the operation fails 503 (same storage batch) | closed | credential changes must be attributable |
| Audit write fails for async event (login failure, denial) | counted, dropped | open | availability under attack |
| `LastSeen` flush fails | log, retry next tick | open | non-security metadata |
| OIDC callback state invalid/expired | 400 `oauth_state_invalid` | closed | CSRF |
| `disabled` mode on a non-loopback listener without opt-in | boot error | closed | |

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

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.auth.mode` | enum `required`/`disabled` | `required` | no |
| `core.auth.disabled_allow_remote` | bool | false | no |
| `core.auth.session.ttl` | duration | 24h | yes (new sessions) |
| `core.auth.session.remember_ttl` | duration | 168h | yes |
| `core.auth.session.idle_timeout` | duration | 0 (off) | yes |
| `core.auth.session.cookie_name` | string | `mc_session` | no |
| `core.auth.password.min_length` | int | 8 | yes |
| `core.auth.apikey.default_ttl` | duration | 720h | yes |
| `core.auth.apikey.max_ttl` | duration | 8760h | yes |
| `core.auth.apikey.legacy_prefixes` | []string | `[]` (ABO: `["abk_"]`) | no |
| `core.auth.bootstrap.enabled` | bool | true | no |
| `core.auth.bootstrap.token_ttl` | duration | 10m | no |
| `core.auth.bootstrap.key_ttl` | duration | 8h (max 24h) | yes |
| `core.auth.bootstrap.readonly_key.enabled` / `.scopes` | bool / []scope | false / `["core:system.view"]` | no |
| `core.auth.setup_requires_token` | bool | true | yes |
| `core.auth.login.max_failures_per_source` | int | 15 | yes |
| `core.auth.login.window` | duration | 15m | yes |
| `core.auth.login.soft_threshold` / `.soft_step` / `.soft_max` | int / duration / duration | 5 / 200ms / 2s | yes |
| `core.auth.invite.ttl` | duration | 72h | yes |
| `core.auth.temp_login.ttl` | duration | 15m | yes |
| `core.auth.oidc.redirect_base_url` | URL | — | yes |
| `core.auth.oidc.providers[]` | list `{id, kind (oidc/github), issuer, client_id, client_secret (secret ref), scopes, allowed_emails, allowed_domains, default_role}` | `[]` | yes (provider set rebuilt) |
| `core.auth.owner.required` | bool | derived (§4.8) | no |
| `core.auth.extensions.cfaccess.{team_domain,aud,owner_email}` | strings | unset (off) | no |
| `core.auth.extensions.clientcert.{enabled,ca_file,mapping}` | bool/path/enum | off | no |

ABO aliases (05-config): `enable_auth`, `oauth_*`, `cf_access_team_domain`, `cf_access_aud`,
`owner_email`, `oauth_default_role`, `bootstrap_key_ttl` (`internal/config/config.go:1558,1586-1613,1753`).

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_auth_attempts_total` | counter | `method`, `outcome` (`success`,`invalid`,`expired`,`revoked`,`inactive`,`throttled`) |
| `mediacore_auth_sessions_active` | gauge | — |
| `mediacore_auth_api_keys_active` | gauge | `legacy` (`true`/`false`) |
| `mediacore_auth_throttled_total` | counter | `surface` (`login`,`bootstrap`,`setup`,`invite`,`temp_login`,`oidc`) |
| `mediacore_auth_password_hash_seconds` | histogram | `op` (`hash`,`verify`) |
| `mediacore_auth_password_queue_wait_seconds` | histogram | — |
| `mediacore_auth_bootstrap_redemptions_total` | counter | `outcome` |
| `mediacore_auth_extension_errors_total` | counter | `extension` |
| `mediacore_auth_principal_invalidations_total` | counter | `reason` |

## 10. Tests

| Test | Asserts |
|---|---|
| `TestSession_TokenNeverStored` | after `IssueSession`, no storage value or key contains the token bytes |
| `TestSessions_ListNeverReturnsToken` | JSON of `GET /me/sessions` has no field equal to any live token (regression for `handlers/auth.go:513-537`) |
| `TestSession_LegacyRawULIDCookieValidatesAfterMigration` | migrated legacy session works via hash lookup |
| `TestCredentialFormat_ChecksumAndPrefix` (+ `FuzzParseCredential`) | prefix, length, CRC; malformed → `ErrNoCredential` vs `ErrInvalidCredentials` correctly |
| `TestAPIKey_PresentedKeyWins` | key + session + CF assertion → method `api_key` |
| `TestAPIKey_CreateRequiresScopes` / `TestAPIKey_KeyCannotWiden` | empty scopes 400; key with `[books:view]` cannot mint `[books:edit]` or empty (regression for `handlers/apikeys.go:199-226`) |
| `TestAPIKey_LegacyAbkAcceptedUntilExpiry` | |
| `TestFirstRun_OnlyPublicAndSetupReachable` | zero users: every non-Public route 401 |
| `TestSetup_RequiresBootstrapToken` | |
| `TestBootstrap_SingleUseConcurrent` | `testing/synctest`, 50 concurrent redeems, exactly 1 success |
| `TestBootstrap_AdminIsCoreAdminHolder` | picks a `core:admin` holder, not a `users.manage` holder |
| `TestCredentialEpoch_KillsSessionsAndOneTimeTokens` | |
| `TestThrottle_SourceAndAccount` | `synctest` clock; soft delays, 429, LRU pinning |
| `TestPasswordHash_BoundedConcurrency` | at most `NumCPU` verifies in flight |
| `TestAuthDisabled_AuthRequiredScopesStill401` | |
| `TestAuthDisabled_RefusesNonLoopbackWithoutOptIn` | |
| `TestOIDC_LinksBySubjectNotEmail` / `TestOIDC_PKCEForAllProviders` | fake IdP (httptest) |
| `TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough` | |
| `TestCFAccess_JWKSOutageFallsThrough` | |
| `TestOwnerGuard_RequiresOwnerAssurance` | |
| `TestPrincipalFor_CompatAppliesEpochAndScopes` | the ABS path (G15) |
| `TestLastAdmin_CannotBeDisabledOrDemoted` | |
| `TestNoGlobals` (archtest) | temp-login map and throttles are instance state (R5) |

## 11. Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/auth/context.go`, `owner.go`, `user_active.go` | `core/auth` (`Principal`, `Method`, assurance) |
| `internal/server/middleware/auth.go` (`RequireAuth`, key auth, `EffectivePermissionsFor`) | `auth.Service.Middleware` + `rbac.Service.Effective`; `RequirePermission`/`RequireAdmin` → httpapi stage 12 |
| `internal/server/middleware/cfaccess.go`, `internal/oauth/cfaccess.go` | `core/auth/cfaccess` |
| `internal/oauth/{oauth,providers,resolve,state}.go` | `core/auth/oidc` |
| `internal/server/absauth/password.go` | `core/auth/password` |
| `internal/server/absauth/throttle.go`, `internal/server/login_ceiling.go`, `bootstrap_rate_limit.go`, `invite_rate_limit.go` | `core/auth` throttle (instance) |
| `internal/server/bootstrap.go` | `core/auth` bootstrap (§4.6) |
| `internal/server/auth_temp_login.go`, `auth_accept_invite.go`, `auth_reset_password.go` | core auth routes + one-time tokens |
| `internal/server/handlers/{auth,apikeys,oauth_login,user,invite_delivery}.go` | core auth/users routes (§4.13) |
| `internal/server/absauth/{token,refresh,audit}.go`, `middleware/absauth.go`, `handlers/abs` | stay in ABO `books` (compat surface), calling `PrincipalFor` |
| `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest service tokens | stay in ABO |
| `internal/mtls`, `cmd/mtls-bridge` | stay in ABO |
| `internal/security/safehttp` | `core/integrations/providerhttp` (05-metadata); `safepath`, `pathvalidation` → `core/library` (05-library) — not auth |

Stored data follows 09-abo-on-core §9.2.3 (adopt into core keyspaces + same-batch reverse mirror
for the compatibility window, then contract C4 deletes legacy rows):

| Legacy family | New | Transform |
|---|---|---|
| `u:` + `idx:user:username:` | `c/auth/user/…`, `c/auth/idx/…` | user IDs and role IDs unchanged (`admin`, `editor`, `viewer`, `fp-worker`; 05-rbac §6.2) |
| `apikey:` + `idx:apikey:` | `c/auth/apikey/…` | hash reused as-is (already SHA-256 of raw); scopes translated (05-rbac §6.1); empty scopes → `LegacyFullScope=true` |
| `sess:<raw>` + `idx:sess:user:` | `c/auth/session/h/<sha256(raw)>` + new `PublicID` | not copied: legacy rows are read as a fallback until they expire (max 7 d); new sessions are hashed only (see below) |
| `invite:<raw>` | `c/auth/onetime/invite/<sha256>` | |
| `oauthid:`, `inviteallow:` | `c/auth/ident/…`, `c/auth/inviteallow/…` | |
| bootstrap hash in settings | `c/auth/bootstrap` | regenerated at first start anyway |
| temp-login map | — | in-memory today; nothing to migrate |

**Session mirror — decision needed (cost surfaced).** 09 §9.2.3 proposes mirroring every new
session *raw* into legacy `sess:` so a rollback keeps users signed in. That keeps bearer tokens in
plaintext at rest for the whole window (two minor releases), which is the G12 defect this section
exists to remove, and the repo's own diagnostics command prints `sess:` values
(05-storage, out-of-scope defects). Recommended instead: **do not mirror sessions**; on rollback
users sign in again once (API keys, which automation uses, are mirrored and keep working). The
alternative is 09's mirror with an explicit end date. Owner decides; this spec's tests
(`TestSession_TokenNeverStored`) assume the recommendation.

## 12. Open items

1. WebAuthn/passkeys (`github.com/go-webauthn/webauthn` v0.18.2 exists) as a second factor setting
   `AssuranceMFA` — not in this operation; the `Assurance` bits leave room.
2. Raising `password.min_length` to 12 for new installs (ABO keeps 8 via its config).
3. Whether `core.auth.setup_requires_token=true` ships as ABO's default immediately or after one
   release of the shell wizard showing the token flow (09-abo-on-core).
4. Idle-timeout default (today none).
5. Session mirror during the compatibility window (§11): recommendation "no raw mirror" vs
   09-abo-on-core §9.2.3's raw mirror.

## 13. Gap disposition (authN rows; full table in 05-rbac §13)

| Gap | Disposition |
|---|---|
| G9 auth wiring inline, closes over `*Server` and global `AppConfig.EnableAuth` | **Fixed**: `auth.Service` instance with explicit `Deps`/`Options`; `core.auth.mode` read through `config` (§3.2, §4.11) |
| G10 bootstrap assumes one binary, admin = first `users.manage` holder | **Fixed**: core-owned, app-agnostic bootstrap; admin = `core:admin` holder (§4.6) |
| G11 owner/CF machinery embedded in auth | **Fixed**: `Authenticator`/`OwnerVerifier` extension point; `auth/cfaccess` optional; policy editing and service tokens stay in ABO (§4.8) |
| G12 sessions unhashed (secrets part → 05-config) | **Fixed for auth credentials**: every session/invite/temp-login/bootstrap/key stored as SHA-256 (§4.1, §4.3); config secrets → `core/secrets` (05-config) |
| G14 rate limit/lockout tied to tunnel topology, in memory | **Fixed + documented**: instance throttle, client address from `httpapi` trusted-proxy config, no hard lockout, in-memory by design (§4.9) |
| G15 two parallel auth stacks | **Fixed**: compat stays in app, resolves via `PrincipalFor` (§4.10) |

## Objections to 03

None. (03 §3.3 places middleware in `auth`; it is a plain `func(http.Handler) http.Handler`, so
`auth` does not import `httpapi`, consistent with R4.)

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
