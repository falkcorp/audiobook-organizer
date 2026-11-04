<!-- file: docs/specs/platform-core/05-authn.md -->
<!-- version: 1.5.0 -->
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
with pre-core identities (ABO) passes its own implementation, written in the app over the app's rows,
which keeps every family in its one stored format and puts native-only data in an app-owned extension
family that older builds never read (§11). Core holds no app key name or row format.

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
`module.Manifest.LegacyStores.Auth` (06 §6.4.2) that keeps ABO's identity rows in their legacy format and
native-only data in a books-owned extension family. No storage grant is involved during the
extraction; core holds no app key name or row format. The `_system` principal constructor lives in
`core/internal/authsys` (not SDK, §3.1).

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
	PasswordResetRequired bool
	// SignInRestricted: every authenticator — password, OIDC and identity
	// extensions — refuses this user (401 password_reset_required) until an
	// owner-verified reset link or the offline "<app> users reset-password"
	// clears it; set on a last admin a restore had to keep (05-rbac §7.1,
	// §4.14, SEC4-12).
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
	// bumps its CredentialEpoch, with one Expect per record on the bytes the
	// plan was built from. Invites the store holds for Source that an older
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
	Scopes    []rbac.Permission // REQUIRED non-empty, no wildcards (05-rbac §4.5); the stored row is never empty (R3-1)
	ExpiresIn time.Duration     // 0 = default; > max = error
	// Purpose and Module are deferred-build members (§3.5).
}

type KeyStatus string

const (
	KeyActive            KeyStatus = "active"
	KeyDisabled          KeyStatus = "disabled"
	KeyNeedsConfirmation KeyStatus = "needs_confirmation" // set by the post-restore hook (§4.14); refused until an admin re-enables; stored as legacy status "revoked" (§11)
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
	Revocations journal.RevocationJournal  // BootWrites' revoke-after-rollback lines
	StateDir    *os.Root                   // bootstrap token file
	Clock       func() time.Time
}

// BootWrites are auth's every-boot writes, run by the kernel in P4d step 4,
// after the post-restore step (06 write rule, DATA3-18), from 09 S4 r2:
// seed the _system user; mint the bootstrap token and, when enabled, the
// start-up read-only key (§4.6, COR4-13); on the boot that follows a boot
// without the journal duty or a foreign open, revoke every session and
// native one-time token issued before that interval (§4.3, DATA4-14,
// SEC4-9); and run Store.Reconcile, the store's own open-time maintenance
// (ABO: HEAD's verified-email reconcile and one-shot key expiry stamp, §11;
// DATA4-10, DATA4-12). auth.New and a Store constructor write nothing.
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
// app's own rows: core holds no app key name or row format (03 R1). Core
// enforces its invariants above the interface — never-empty key scopes,
// upgrade-on-use checks, status, epoch and expiry checks, dominance — and
// auth/authtest.StoreSuite is the conformance suite every implementation
// runs. Every write takes the caller's *storage.Batch, never a copy, so the
// staged writes commit with the journal line and audit record (COR2-12).
// Every Take* stages Expect(key, seen) plus Delete in that batch over a
// Serialized family, so of two concurrent consumers of one token exactly one
// commits (DATA4-9).
type Store interface {
	// users
	PutUser(b *storage.Batch, u *User) error
	GetUser(ctx context.Context, id UserID) (*User, error)
	UserByUsername(ctx context.Context, fold string) (*User, error)
	ScanUsers(ctx context.Context) iter.Seq2[*User, error] // an undecodable stored record yields ErrUndecodableRecord for that record only

	// sessions: hashed, never a raw token (§4.3)
	PutSession(b *storage.Batch, s *SessionRecord) error
	SessionByHash(ctx context.Context, h [32]byte) (*SessionRecord, error)
	SessionByPublicID(ctx context.Context, id string) (*SessionRecord, error)
	ScanUserSessions(ctx context.Context, u UserID) iter.Seq2[*SessionRecord, error]
	DeleteSession(b *storage.Batch, s *SessionRecord) error
	// RevokeSignInsBefore deletes every session and native one-time token
	// created before t (zero t: all) (§4.3 rollback rule, §4.14 step 2).
	RevokeSignInsBefore(b *storage.Batch, t time.Time) (int, error)

	// API keys, looked up by SHA-256 of the raw key
	PutAPIKey(b *storage.Batch, k *APIKey) error
	APIKeyByHash(ctx context.Context, h [32]byte) (*APIKey, error)
	ScanAPIKeys(ctx context.Context, owner UserID) iter.Seq2[*APIKey, error] // owner "" = every key

	// native one-time tokens, keyed by token hash: invite, temp-login, bootstrap
	PutOneTime(b *storage.Batch, t *OneTimeToken) error
	TakeOneTime(b *storage.Batch, kind OneTimeKind, h [32]byte) (*OneTimeToken, error)

	// Credentials an older build of the app issued: accepted for their own
	// lifetime, never written (§4.3, §4.13, §11). A native store has none and
	// answers ErrNotFound. TakeLegacySession accepts only a session created
	// before the store first served a build with this rule (its cut-off).
	TakeLegacySession(b *storage.Batch, raw string) (*LegacySession, error)
	TakeLegacyInvite(b *storage.Batch, raw string) (*LegacyInvite, error)
	DeleteLegacySignIns(b *storage.Batch) (int, error) // every legacy session and legacy invite (§4.14 step 2)

	// external identities and the invite allow-list (§4.7)
	PutIdentity(b *storage.Batch, id *ExternalIdentity) error
	DeleteIdentity(b *storage.Batch, id *ExternalIdentity) error
	IdentityBySubject(ctx context.Context, provider, subject string) (*ExternalIdentity, error)
	InviteAllowed(ctx context.Context, email string) (bool, error)

	// StageMerge rewrites the owner of every record of plan (Admin.StageUserMerge).
	StageMerge(b *storage.Batch, plan UserMergePlan) error

	// Reconcile is the implementation's own every-boot maintenance, run by
	// BootWrites at P4d step 4 into b; a native store does nothing (§11).
	Reconcile(ctx context.Context, b *storage.Batch) error
}

var ErrUndecodableRecord = errors.New("auth: stored identity record cannot be decoded")

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

### 3.5 Deferred-build members

These land with their consumer. Deferral applies to features inside built packages as well as to packages (SIMP2-6, 03 §3.4). These
members of `auth` are specified so 08 and 10 can rely on them, and are **not built** at 09 S4:

| Member | Lands with | Contract |
|---|---|---|
| `KeySpec.Purpose KeyPurpose` (`PurposeGeneral` zero, `PurposeFacade`) and `KeySpec.Module` | `arrfacade` (08) | a `PurposeFacade` key names a `Module`; every scope is one of that module's `<m>:arr.*` scopes (all `Confining`, 05-rbac §4.10); it is accepted only on that module's facade group (stage 12), and a general key holding `<m>:arr.*` plus anything else is refused there (08 §8.5's "every scope is arr" rule as a second check); the stored key record gains a `purpose` field that defaults to general; display adds purpose |
| `X-Api-Key` header and `apikey` query-key extraction | `arrfacade` (08) | enabled **only** on facade groups (§4.2 step 2); httpapi's 05-logging redaction already covers `apikey` |
| Feed token | the first TV/Movies app ([10](10-tv-movies-proof.md) G9) | a per-route opt-in credential in the query string for clients that cannot send headers (iCal, OPDS): scoped, read-only, revocable, stored hashed like a key, accepted only on routes that opt in |
| `TestAPIKey_FacadePurposeBoundToModule`, `TestFacadeKey_RefusedOnABS` | `arrfacade` | SEC-5 |

Until they land, SEC-5 holds trivially (no facade key can be minted) and HEAD's extraction rule is
kept exactly: Bearer and the session cookie only (`APIKeyFromRequest`,
`internal/server/middleware/auth.go:83-98`). The additions are additive and pass the API-stability
gate. Their absence is checked by the deferred-members archtest (`archtest.DeferredMembersAbsent`,
03 §3.4, 09 S2) from the stage that builds `auth` rather than first at S7 (SIMP3-2). Its list is
generated from every `Deferred-build members` heading under `docs/specs/platform-core/` (03 §3.4 is
the one definition) and matches package-qualified identifiers (`auth.KeySpec.Purpose`,
`auth.PurposeFacade`), never bare names; the PR that builds a deferred member deletes that member's
entry, and the test is deleted when the list is empty (COR4-19, SIMP4-1).

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
| Bootstrap | `mcb_<43>_<6>` | 10 min, single use | `SHA-256` as a native one-time token in the store + 0600 file |
| Invite | `mci_<43>_<6>` | default 72 h (today, `handlers/user.go:177-181`), single use | `SHA-256` |
| Temp-login | `mct_<43>_<6>` | 15 min, single use | `SHA-256` in storage (not a process map, R5) |
| Legacy (accepted, never issued) | `abk_…` keys; raw-ULID session cookies; raw legacy invite tokens | until their own expiry | §11 |

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
- Lookup is by `SHA-256(token)`; the token never touches storage, logs or API responses. Listing
  returns `SessionView` with `PublicID`; revoke takes `PublicID`. This closes the token leak through
  `GET /api/v1/auth/sessions` (§2).
- **Native-only records (R4-1).** Session records are hashed and live only where the store keeps
  native-only data — `c/auth/session/…` in the native store, books' extension family in ABO's legacy
  store — which no older build reads; they are never projected into the app's legacy session rows
  (SEC-19). A rollback therefore costs every browser user one sign-in; legacy `abk_` API keys, which
  existing automation uses, keep working because they stay in their legacy rows (§4.4). Sessions an
  older build issues during a rollback are not honoured after the roll-forward (§11, upgrade-on-use
  cut-off).
- **Re-login both ways (DATA4-14, SEC4-9).** Native sessions issued before a rollback are invisible to
  the older build, which can neither list nor revoke them (`internal/server/handlers/auth.go:540-570`),
  so a session the user ended there (a lost device, a "log out") would come back on roll-forward. The
  first boot that follows a boot without the `journal` duty or a foreign open (05-storage §5S.3.7; the
  boot on which 05-backup appends a `gap` line) therefore revokes, in `BootWrites`, every session and
  native one-time token created before that interval (`Store.RevokeSignInsBefore`, one batch,
  journaled `session.revoked` and audited `core.session.revoke_after_rollback`). A rollback then costs
  one sign-in on the way back as well.
- `LastSeen` is updated at most once per minute per session (in-memory coalescing, written with
  `NoSync`), so authentication stays a read on the hot path.
- Expiry sweep: from 09 S5, when `tasks` lands, core task `core.auth.sessions.prune` on
  `tasks.ScheduleFromConfig("core.auth.schedules.session_prune")` (a declared `tasks.ScheduleConfig`
  key in auth's own namespace, default every 1 h; resolution R2-7); deletes expired/revoked records.
  At S4 sessions and one-time tokens are pruned on read: an expired or revoked record is refused and
  deleted in the same batch, so S4's growth is bounded by one stage of sign-ins (COR4-13).

### 4.4 API keys

- Create: `Scopes` required and non-empty, no wildcards, each scope must be held by the actor's
  effective set (05-rbac §4.5). The key's canonical scopes are native-only data (R4-1). In ABO's legacy
  store the key is a legacy key row whose `scopes` list holds **only** the legacy atoms the canonical
  scopes admit under the row rule (05-rbac §6.3) — never a namespaced string — while the canonical
  scopes live in books' extension record; a key with no admitted atom is written with legacy status
  `"inactive"`, which every build refuses, its real status kept in the extension. The legacy list is
  therefore never empty where the row is active, so no build that reads it can take it for "all role
  permissions" (`middleware/auth.go:405-408`), and an older build grants from it at most what the row
  rule admitted. Such a key carries the `mck_`
  prefix, which older builds do not recognise as a key at all (`APIKeyFromRequest` accepts only
  `abk_`, `:76-99`), so it does not authenticate after a rollback; legacy `abk_` keys keep working
  there. Scopes are fixed at creation (no scope-edit route), so a legacy key's stored list keeps its
  legacy atoms, or its empty list (`LegacyFullScope`, 05-rbac §4.5), for its whole life. A key-authenticated actor may create keys only for itself, with
  scopes ⊆ its own key's effective set and expiry clamped to its own key's expiry (today's clamp,
  `handlers/apikeys.go:40-60`, kept). This closes the scoped-key → full-key escalation (§2).
- Creating a key for another user requires `core:users.manage`, an interactive method (guard),
  scopes ⊆ the **target's** role permissions and ⊆ the actor's, and that the actor dominates the
  target (05-rbac §4.7 rule 5).
- **Purpose (SEC-5, 08 O9)** is a deferred-build member (§3.5): no facade key exists before
  `arrfacade` lands.
- Last-use tracking stays synchronous in the request goroutine (reason recorded at
  `middleware/auth.go:366-382`), coalesced per key per minute.
- Display: `id`, name, first 8 chars of the hash as identifier (today, `raw/10` §7), scopes,
  expiry, last used, status; the raw value is shown once.
- **Status transitions are monotone toward revoked.** `revoked` is terminal: `SetAPIKeyStatus`
  refuses any transition out of it (409 `key_revoked`), and `needs_confirmation` can only go to
  `active` (admin re-enable, interactive, audited) or `revoked`. In ABO's legacy row `revoked` and
  `needs_confirmation` are both status `"revoked"` with `revoked_at` set (books' extension record tells
  them apart), and `disabled` is `"inactive"`: the legacy auth path refuses exactly those two statuses
  (`middleware/auth.go:335-344`), so an older build never accepts a restricted key. A key whose
  extension the store has suspended (an older build changed its row, §11) reads as
  `needs_confirmation` until an admin re-enables it. The legacy re-activation defect is fixed in the legacy code itself by
  early fix 3 (§11.1), independently of the extraction.

### 4.5 Passwords

`auth/password` keeps argon2id m=64 MiB, t=3, p=2, 16-byte salt, PHC string, bcrypt verify-only
with transparent rehash, `oauth`/empty hash never authenticates (`absauth/password.go:40-47,131-190`).
Minimum length `core.auth.password.min_length` (default 8 — today's value; raising it is open item 2).
Hashing runs on a bounded semaphore (`runtime.NumCPU()` permits) because each verify allocates
64 MiB: an unbounded login burst would otherwise exhaust memory before the throttle trips.

### 4.6 Bootstrap and first run (G10: app-agnostic)

Core owns it; nothing refers to an app, a module or `users.manage`.

1. At every start, if `core.auth.bootstrap.enabled` (default true), `BootWrites` (P4d step 4, 06's
   write rule; COR4-13) mints `mcb_…`, writes `bootstrap-token` (mode 0600) via `os.Root` in the
   app's state dir, stores hash + expiry as a native one-time token of kind bootstrap through the
   `Store` (an app's own legacy bootstrap hash is never read by core, §11), and logs the **path and
   expiry only** (never the value; today's rule, `bootstrap.go:165-175`).
2. `POST /api/core/v1/auth/bootstrap` (Public, `RateAuth`, per-client limiter): validate then spend
   atomically (`TakeOneTime` = `Expect` + delete in one batch over a `Serialized` family — replaces the
   process mutex `bootstrapMu`), then find or create the admin: **first active user holding `core:admin`**
   (role-resolved, 05-rbac §4.6); none → create `admin` (suffixing on collision) with role
   `admin` and a generated password returned once. Returns an API key scoped `["core:admin"]`,
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
  core ships none; ABO's `internal/cfaccess` is the one in use; deployments without one have no owner assurance, so
  owner-guarded routes (user merge, factory reset, restore while an owner is set — 9 routes today,
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
  `Guards.Interactive` route (password, keys, sessions of others, temp-login) is refused — today's
  credential guard refuses for the same reason (`credential_guard.go:28`).
- `disabled` refuses to start when a listener is bound to a non-loopback address **or** any trusted
  proxy or tunnel connector is configured (a local proxy makes remote callers look local, SEC-30),
  unless `core.auth.disabled_allow_remote=true` (today: a warning only, `server_lifecycle.go:1847`).
  An app sets that key's default through `auth.Options`, as it does the cookie name: new apps keep
  `false`; **ABO's default is `true`** (SEC4-8), because a Docker container always listens on a
  non-loopback address and every ABO install running `enable_auth=false` (Basic-only behind the compose
  `BASIC_AUTH_*`, or LAN-only) would otherwise crash-loop at S4 r2. While `disabled` runs
  remote-reachable, health reports `degraded` (`auth_disabled_remote`) with a banner, as HEAD's warning
  did; the release note states it and 09 §9.2.6 records it (`TestS4_AuthOffDockerBoots`, image job).
- **Turning auth on with auth off (SEC4-8).** `ProtectAdmin` keys need `core:admin` and an interactive
  method, which the auth-disabled principal never has, so with auth off no request can change them —
  `core.auth.mode` included — where HEAD let protected fields change with auth off
  (`internal/config/update_service.go:781`). The path is the operator layers: set
  `core.auth.mode=required` (ABO's alias `enable_auth=true`) in the environment or the config file,
  which lock the key (05-config), and restart. The release note names it, 09 §9.2.6 records it, and the
  auth-off half of `TestLegacyRoleReachUnchanged` asserts the 403 for protected keys through the
  `/api/v1/config` alias.
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
- **One stored copy (R4-1).** A revocation — interactive, by task, by journal re-application or by
  the post-restore hook (§4.14) — goes through the `Store` in the revoking batch. In ABO's legacy store
  it writes the legacy row itself (`status`, `revoked_at`, `credential_epoch`, `password_hash`, the role
  list), and only the restriction's native detail goes to books' extension record. There is no second
  copy that a boot could rebuild over it, and every build that can open the store reads the same
  field, so the restriction holds after a restart and after a rollback alike. Session revocations touch
  only native-only session records, which older builds never read (§4.3).

### 4.13 Core auth routes (all under `/api/core/v1`)

Registered by `core/internal/authhttp` (L6, imported only by `kernel`, 03 §3.3); `auth` itself does
not import `httpapi`. "Dominance" = 05-rbac §4.7 rule 5, enforced in `auth.Service` (SEC-4). No core
route carries a one-time token in its path; tokens travel in the request body (SEC-21). Links:
temp-login links are `/login/temp#token=…` from 09 S4; invite links keep the `/invite/<token>` path
form until the shell stage (09 S9), because the pre-shell SPA opens only that form
(`web/src/App.tsx:201-206`; FE4-2), and are `/invite#token=…` from S9. The form is the compile-time
`auth.Options.InviteLinkForm` — `InviteLinkFragment` (the default) or `InviteLinkPath` — which ABO sets
to `InviteLinkPath` from S4 and drops in the S9 PR that adopts the shell. A fragment never reaches a
server; a path-form invite link reaches it only as the page request, as at HEAD, redacted from the
access log and spans by 05-logging's `DefaultPathRules` (`/invite/{token}`), and the page posts the
token in a body.

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

**Legacy invites are re-checked on accept (SEC3-2).** A legacy invite (created by an older
build, before S4 or during a rollback) carries `RoleID` and `CreatedByUserID`
(`handlers/user.go:155-197,239`). Accepting it on this build re-runs `rbac.CanAssign` for the creator
over `RoleID` and refuses (`invite_invalid`, audited `core.invite.accept` with `Outcome: denied`) when the check fails or the creator no
longer exists, so an invite into a role its creator could not assign natively never yields an
account.

**Legacy links (SEC2-6, FE2-8, FE3-5, FE4-2).** Legacy `GET /auth/temp-login?token=` is answered from
S4 by a server-side `302` to `/login/temp#token=<token>` with `Referrer-Policy: no-referrer` (`token`
is in 05-logging's `DefaultRedactQuery`). Invite links: from S4 to S9 `GET /invite/<token>` is the
legacy SPA's page, served by the legacy `/` fallback as at HEAD; from S9, when the shell owns `/` and
core mints fragment links, books' manifest adds `GET /invite/{token}` (Public, `RateAuth`) answering a
`302` to `/invite#token=<token>` with `no-referrer`, for path-form links issued before the shell. Both
302s are books **legacy manifest routes** for their windows (09 §9.6 rows for `/auth/temp-login`,
S4 + max(W, temp-login TTL), and `/invite/{token}`, S9 + max(W, invite TTL)); `invite` joins `login`
in ABO's `LegacyMount.SPAShared` at S9, so `GET /invite` stays the shell page while the more specific
pattern answers the redirect (05-http-api §3.5, §8.2); `ui.Handler` implements no redirect. Between 09
S4 and S9 ABO's pre-shell SPA serves the UI: the same S4 build that switches invite creation to core
keeps `AcceptInvite` on its `/invite/:token` route — rendered for signed-in and signed-out visitors
alike — reading the path parameter and calling `POST /api/core/v1/auth/invites/lookup|accept` with the
token in the body, and adds a `/login/temp` route to the legacy SPA's unauthenticated **and**
authenticated branches that reads `#token=` and calls `POST /api/core/v1/auth/temp-login/consume`
(today the unauthenticated branch sends `*` to `/login`, which drops the fragment,
`web/src/App.tsx:249-250`). `TestLegacyTempLoginLink_EndToEnd` and `TestLegacyInviteLink_EndToEnd`
(an invite created on S4 r2, opened signed out, accepted, signed in; opened signed in, the invite page
and not `NotFound`) are in 09 S4's done-check. 09 S4's R-test cell reads "an invite link created on cur
opens in cur's UI; prev-release UI tabs (cell a) still open a legacy `/invite/<token>` link" — an
invite created on cur is a native one-time token and is void on prev (§11).

**Invite delivery (SIMP2-4, R-F).** Invite e-mail uses a `mail.Sender`. Until 09 S7 it is built from
the legacy SMTP settings, as today; from S7 it is built from the `notify/email` integration instance
(05-notify, 05-metadata), so auth never depends on `notify` before notify exists.

### 4.14 Post-restore hook (SEC-16)

`PostRestore` is auth's `journal.RestoreHook`. The kernel runs it in P4d's post-restore step (06
§6.5.1), only on a boot that applied a restore or found a replaced store (below), in the order of
COR3-2: **after** `secrets.Open` and `AttachOverrides` (so journal lines are
verified with the keyring and the journal is read from the effective `core.backup.dir`) and after the
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
   `PasswordResetRequired` (native-only data: in ABO's store an extension member, §11), which only
   selects the 401 code (it may be the pre-compromise password,
   SEC2-11); for each `identity.unlinked` entry, delete the re-appeared `(provider, subject)` link.
2. **Invalidate restored sign-ins.** Revoke every native session and every native one-time token
   (invite, temp-login, bootstrap; `Store.RevokeSignInsBefore` with zero time), and **delete every
   legacy session and legacy invite** the store holds (`Store.DeleteLegacySignIns`; otherwise the
   upgrade-on-use session fallback and the in-place invite lookup of §11 would honour them, and so
   would an older build after a rollback). Bump every user's `CredentialEpoch`. Everyone signs in
   again once.
3. When `rc.Revocations` is nil (the journal is unavailable, broken or does not cover the interval:
   a foreign, host-loss or replaced-store restore), set every restored API key whose status is
   `active` or `disabled` to `needs_confirmation`: refused until an admin re-enables it from the keys
   page. A key restored as `revoked` stays revoked — terminal, never re-enableable (DATA4-13). In ABO's
   legacy row the restricted key is status `"revoked"` with `revoked_at` set (§4.4), which every build
   refuses.
4. Return the counts per family; audit's restore hook records them as `core.restore.revoke`
   (05-rbac §7.1 action names).

**Restricted last admin (SEC3-15, SEC4-12).** When rbac's hook keeps the last active `core:admin`
holder's role (05-rbac §7.1), this hook's restriction of that account is: password hash blanked with
`password_reset_required`, API keys `needs_confirmation`, `SignInRestricted` set (every authenticator,
OIDC and identity extensions included, refuses it until an owner-verified reset link or the offline
`<app> users reset-password` clears it), and its external-identity links deleted (journaled
`identity.unlinked`), so an older build cannot sign it in through OAuth either. The residual — an
older build admitting it through Cloudflare Access, which resolves users by allow-listed e-mail — is
named in the restore report.

**Replaced store, and restores by an older build (DATA3-8, SEC3-3, SEC3-10, DATA4-7, SEC4-6).** An
older build (after a rollback) restores with its own code, which neither journals nor runs hooks.
05-backup decides "replaced" from **store identity**, not from journal-head lag (a concurrent-commit
head regression or a crash after `Append` also leaves the head behind): every journaling boot records
the storage generation and the DB directory's identity, and the replaced-store test and the re-apply
read the store's head before any line is appended on this boot (SEC4-5).

- Same generation, head behind the journal's last revocation line: the verified tail is re-applied
  (narrowing, idempotent) and **no hook runs**.
- Different generation with no staged restore (an older build's restore, a file copy, a checkpoint
  copied by hand): the kernel re-applies the verified tail first, so every known revocation holds, and
  then runs this hook with `KindExternal` and **`rc.Revocations == nil`** — steps 2 and 3: restored
  sessions and legacy invites deleted, every epoch bumped, every active or disabled key
  `needs_confirmation`. The verified tail is not passed as coverage even when it verifies: the journal
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
   (DATA4-2). A store constructor writes nothing (DATA4-10).
2. **P4d, post-DAG reconcilers** (06 §6.5.1 is the order; COR3-2), from 09 S4 r2: `secrets.Open`
   (the keyring only) and then config's `AttachOverrides` (R3-1, R4-6); then 05-backup's
   revocation-journal step, whose auth kinds write through auth's Store (the replaced-store test and
   re-apply read the head before this boot appends a `gap` or `restore` line, SEC4-5); then, on a boot
   that applied a restore or found a replaced store, `auth.PostRestore` (§4.14); then
   `auth.BootWrites` — seed `_system`, mint the bootstrap token and the optional read-only key (§4.6),
   revoke sign-ins from before a rollback interval (§4.3), and run `Store.Reconcile` (ABO: HEAD's
   open-time identity writers, §11) — moved out of P5 so P5 writes nothing (DATA3-18, COR4-13); last,
   the compat usage records buffered in P0. There is no tasks step and no identity rebuild.
   **At S4 r1 (dark, COR4-4)** P4d only runs, after step 1, the read-only decode census over the
   Store's scans (records reported `ErrUndecodableRecord` are counted and listed for the probe P1;
   nothing is written); the journal re-apply's auth kinds, `PostRestore` and `BootWrites` are skipped
   and logged, gin still performs every revocation without journaling, and r1 therefore does not list
   the `journal` duty (05-backup), so its interval becomes a `gap` line at the first r2 boot.
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
| An identity extension is stale against its legacy row (ABO: an older build changed the row) | granting data ignored, restrictions kept: the key reads `needs_confirmation` until an admin re-enables it; health `degraded` (`auth_extension_suspended`) | closed | no silent widening on roll-forward (R4-1) |
| User with `SignInRestricted` | every authenticator refuses: 401 `password_reset_required` | closed | restricted last admin (SEC4-12) |
| Legacy invite whose creator no longer may assign its role | `invite_invalid`, audited | closed | SEC3-2 |
| Replaced store (different generation), verified tail or not | tail re-applied, then the hook with `rc.Revocations == nil`: every active or disabled key `needs_confirmation`, every epoch bumped; a chain break adds health `revocation_journal_broken` | closed | SEC3-10, SEC4-6 |
| Bootstrap token wrong/expired/spent | 401 `bootstrap_invalid`, uniform message | closed | |
| Bootstrap file write fails at start | log error, bootstrap unavailable this run; setup still requires token → operator restarts after fixing perms | closed | |
| Key create with escalation | 403 `scope_escalation` | closed | |
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
- `HasUsers` is an `atomic.Bool` set by the first `CreateUser` commit; cleared only by factory reset.
- Every `Take*` (one-time tokens, legacy sessions, legacy invites) stages `Expect(key, seen)` plus
  `Delete` in the caller's batch over a `Serialized` family — the native store declares
  `c/auth/onetime/` and `c/auth/session/` `Spec.Serialized`; ABO's legacy store declares its legacy
  invite and session claims and its extension family `Serialized` (05-storage §5S.9.1) — so of two
  concurrent consumers of one token exactly one commits, as HEAD's `inviteMu` guaranteed
  (`internal/database/user_merge.go:510-520`; DATA4-9). Tests with `testing/synctest`:
  `TestBootstrap_SingleUseConcurrent`, `TestLegacyInviteDoubleAcceptOneWins`.
- Password hashing: bounded semaphore (`runtime.NumCPU()`).
- `LastSeen` coalescer: one goroutine, buffered map under a mutex, flush every 60 s and on stop.
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
| `core.auth.mode` | enum `required`/`disabled` | `required` | no | owner (SEC3-6, §4.8) |
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
| `mediacore_auth_legacy_session_fallback_total` | counter | `outcome` (`upgraded`,`refused`) |

## 10. Tests

| Test | Asserts |
|---|---|
| `TestSession_TokenNeverStored` | after `IssueSession`, no storage value or key contains the token bytes |
| `TestSessions_ListNeverReturnsToken` | JSON of `GET /me/sessions` has no field equal to any live token (regression for `handlers/auth.go:513-537`) |
| `TestSession_LegacyRawULIDCookieUpgradesOnUse` | a legacy `sess:` cookie created before the cut-off (epoch, status, revoked, expiry checked) yields a hashed `mcs_` session, a new cookie, and the legacy row deleted (SEC-19) |
| `TestCookie_SiblingAppCookieDoesNotBlockLogin` | two apps on one host: distinct default cookie names; a foreign or stale cookie on a Public route → anonymous + cleared (SEC-11, FE-6) |
| `TestStage12_InvalidKeyPlusValidSessionIs401` | stop rule (SEC-12) |
| `TestCredentialFormat_ChecksumAndPrefix` (+ `FuzzParseCredential`) | prefix, length, CRC; malformed → `ErrNoCredential` vs `ErrInvalidCredentials` correctly |
| `TestAPIKey_PresentedKeyWins` | key + session + CF assertion → method `api_key` |
| `TestAPIKey_CreateRequiresScopes` / `TestAPIKey_KeyCannotWiden` | empty scopes 400; key with `[books:view]` cannot mint `[books:edit]` or empty (regression for `handlers/apikeys.go:199-226`) |
| `TestAPIKey_FacadePurposeBoundToModule` (deferred-build, §3.5) | facade key refused off its facade group; general key with extra scopes refused on it (SEC-5) |
| `TestAPIKey_RevokedIsTerminal` | `SetAPIKeyStatus(active)` on a revoked key → 409; `needs_confirmation` → only `active` (admin, interactive) or `revoked` |
| `TestOneFormat_LegacyRowsReadByPrevBuild` | (ABO, in `make ci` with `prev` built from `PREV_REF` and cached by tag, OPS4-6) rollback cells against the previous release's real binary on a store written by this build: a native key's legacy row holds only admitted legacy atoms (no namespaced string) or status `inactive`, and grants at most those atoms there; a disabled, revoked and `needs_confirmation` key is refused there (`inactive` / `revoked`); a user disabled or epoch-bumped here is refused there; roll forward: nothing was widened, and an extension whose row prev changed is suspended |
| `TestAPIKey_StoredScopesNeverEmpty` | property test over ABO's legacy store: every legacy key row written for a new key has a non-empty `scopes` list of admitted legacy atoms only, or status `inactive`; never a namespaced string (SEC-1, R4-1, SEC4-3) |
| `TestSession_NativeOnlyRecord` | no session issued by this build appears in the app's legacy session rows; a legacy session created after the store's cut-off is refused by the upgrade-on-use fallback (§11) |
| `TestPostRestoreSurvivesRestart` | restore, restart: the re-revoked key, a restored legacy `sess:` cookie and a restored legacy invite are refused, and their legacy rows are gone (SEC2-3, DATA2-4) |
| `TestReplacedStore_RunsPostRestoreHook` / `TestReplacedStore_TruncatedJournalKeysNeedConfirmation` | store file-copied back to an older generation (no staged restore): the next boot re-applies the verified tail and runs the hook with `rc.Revocations == nil` — every active or disabled key `needs_confirmation`, every epoch bumped — also when the journal was truncated after the archive and verifies cleanly; same generation with the head behind (a crash after `Append`) re-applies the tail only, no hook (DATA3-8, SEC3-10, DATA4-7, SEC4-6) |
| `TestLegacyInviteAcceptRechecksCanAssign` | a legacy invite whose creator cannot `CanAssign` its `RoleID`, or whose creator is deleted, is refused on accept (SEC3-2) |
| `TestOwnerTrustRootKeysAreOwnerProtected` | port of HEAD's owner-trust-root test: with an owner set, a non-owner admin session cannot change `core.auth.mode`, a provider's `allowed_emails`/`allowed_domains`, `core.auth.owner.required` or the three `modules.books.identity.*` keys (SEC3-6) |
| `TestLegacyTempLoginLink_EndToEnd` | admin reset on the S4 build → legacy `/auth/temp-login?token=` link → 302 to `/login/temp#token=` → legacy SPA page consumes it → signed in (FE3-5) |
| `TestBootWrites_RunInP4d` | `_system` seeding, the bootstrap mint, the read-only key, the revoke-after-rollback step and `Store.Reconcile` happen after the checkpoint, in P4d step 4; P5 and the store constructor write nothing (DATA3-18, COR4-13, DATA4-10) |
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
| `TestPostRestore_RevokedKeyStaysRevoked` / `TestPostRestore_NoJournalKeysNeedConfirmation` / `TestRestoreNilCoverageKeepsRevokedTerminal` | SEC-16; refused by the previous release too; with nil coverage a key restored as revoked stays revoked, only active or disabled keys become `needs_confirmation` (DATA4-13) |
| `TestAccessLog_NoInviteToken` | (in 05-http-api) no one-time token in access log, journal or span attributes (SEC-21) |
| `TestNoGlobals` (archtest) | temp-login state and throttles are instance state (R5) |
| `TestNativeBackend_RoundTrip` / `authtest.StoreSuite` | the native store and ABO's legacy store pass the same suite (ABO's with HEAD fixtures, today's `TestGrantedFamilyGolden` cases) (SIMP4-3, R4-1) |
| `TestIdentityExtension_SuspendedAfterPrevWrite` | (ABO) a key's or user's legacy row changed by the previous release → on roll-forward the key reads `needs_confirmation`, user restrictions stay, health `auth_extension_suspended`; nothing wider than the legacy row (R4-1) |
| `TestDarkReleaseWritesNoIdentityRow` / `TestDarkReleaseRecordsJournalGap` | S4 r1: the census reads every record through books' store and writes none; r1 lists no `journal` duty, so the first r2 boot appends a `gap` line (COR4-4) |
| `TestRollbackR2ToR1Boots` | a store served by S4 r2 boots on S4 r1: books supplies the same store, no missing-grant or backend error (DATA4-2) |
| `TestLegacyInviteDoubleAcceptOneWins` / `TestUserMergeWithLegacyInvite` | `synctest`: two concurrent accepts of one legacy invite, exactly one account; a merge whose source has an open legacy invite succeeds and deletes it (DATA4-9) |
| `TestNativeSessionsRevokedAfterRollback` | a session issued on cur, a rollback boot on prev, roll forward: the session is refused, audited `core.session.revoke_after_rollback` (DATA4-14, SEC4-9) |
| `TestS4_AuthOffDockerBoots` | (image job) ABO with `enable_auth=false` in a container boots at S4 r2 and reports `auth_disabled_remote` (SEC4-8) |
| `TestOwnerGate_AccessWithoutOwnerFallsBack` | Access configured, no owner identity: restore, factory reset and user merge fall back to Interactive + `core:admin`; owner keys are `ProtectAdmin` (SEC4-14) |
| `TestRestrictedLastAdmin_SSORefused` | a last admin kept by a restore cannot sign in by password, key, OIDC or an identity extension until a reset clears `SignInRestricted` (SEC4-12) |
| `TestVerifiedEmailReconcileInBootWrites` / `TestExpiryStamp_OneShot` | (ABO) HEAD's verified-email reconcile runs in `Store.Reconcile` at P4d step 4; the key expiry stamp honours HEAD's one-shot done flag (DATA4-10, DATA4-12) |

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

**Stored data: store interface, one format per family (resolution R4-1, refining R3-1; supersedes
cross-cutting resolution 5 and R2-1/R2-2 here).** Core auth does not copy ABO's identity data and holds
none of its key names or row formats: every record goes through `auth.Store` (§3.4). There is no
adoption copy, no mirror, no rebuild at boot, no claim hand-over, no claim-mode level step for identity
and **no storage grant** during the extraction (grants for these families belong to the deferred
cutover), so an older build after a rollback and this build after a roll-forward read the same bytes.

**Native store (apps without pre-core identities; SIMP4-3).** `core/internal/authstore`, landing with
`auth` at 09 S4, used by `hello`, `coretest` and every app that passes no legacy store. Values are
`codec.JSON[T](1)` of the core type:

| Key | Value |
|---|---|
| `c/auth/user/<id>`; index `c/auth/user/name/<fold>` → id | `User` (with its password hash and role list) |
| `c/auth/apikey/<id>`; `c/auth/apikey/h/<sha256>` → id; `c/auth/apikey/u/<user>/<id>` | `APIKey` |
| `c/auth/identity/<provider>/<subject>`; `c/auth/identity/u/<user>/<provider>/<subject>` | `ExternalIdentity` |
| `c/auth/inviteallow/<folded e-mail>` | allow-list entry |
| `c/auth/session/h/<sha256>`; `c/auth/session/id/<publicID>` → hash; `c/auth/session/u/<user>/<publicID>` (`Serialized`) | `SessionRecord` |
| `c/auth/onetime/<kind>/<sha256>` (`Serialized`) | `OneTimeToken` (invite, temp-login, bootstrap) |

It has no legacy credentials (`TakeLegacy*` answer `ErrNotFound`) and `Reconcile` does nothing.
`TestNativeBackend_RoundTrip` runs `authtest.StoreSuite` over it.

**ABO's legacy store (books-supplied).** The `books` module passes `module.Manifest.LegacyStores.Auth`
(06 §6.4.5): ABO's existing identity store code — the auth methods of `internal/database` stay in ABO
behind the interface instead of moving into core (04 §4.5) — over ABO's rows in their legacy format,
through books' own claims. Write exclusivity on those families is an ABO archtest (only the
legacy-store package writes them), not a storage grant. ABO runs `authtest.StoreSuite` with HEAD
fixtures. What the legacy format cannot carry goes to a **books-owned extension family that older
builds never read — never additively into legacy rows** (SEC4-3: an extra string in a legacy row can
widen on an older build):

- **Extension records** extend one legacy row each, keyed by its primary key, never repeat a field the
  row holds, and carry `legacy_ref`, the SHA-256 of the row bytes they were written with, in the same
  batch (the extension-record rule of 05-storage §5S.3.11): a key's canonical scopes, the
  `needs_confirmation` distinction, `password_reset_required`, `SignInRestricted` and display data.
  When the row's hash differs from `legacy_ref` — an older build changed the row while rolled back —
  the extension is **suspended**: its granting data is ignored and its restrictions stay in force, so
  the record is never wider than the legacy row alone; a key reads `needs_confirmation` until an admin
  re-enables it (audited `core.apikey.set_status`), and health reports `degraded`
  (`auth_extension_suspended`). A record whose row is gone is an orphan, ignored and deleted by the
  next write of that key.
- **Standalone native-only records**: hashed sessions, native one-time tokens (invite, temp-login,
  bootstrap) and the session cut-off record. A rollback costs one sign-in per browser and voids invites
  and temp-login links issued by this build (resend) — stated in the release note.
- **Every restriction is also in a field the legacy build reads**: `status` (`"inactive"`/`"revoked"`),
  `revoked_at`, `credential_epoch` and a blanked `password_hash`; a new key's legacy `scopes` list
  holds only admitted legacy atoms and is never empty while the row is active (§4.4).

| Legacy family (ABO) | Read and written as | Notes |
|---|---|---|
| `u:` + `idx:user:username:` (and the other `idx:` user sub-prefixes) | the legacy `User` row (`internal/database/store.go:734-780`) | IDs and role IDs unchanged (`admin`, `editor`, `viewer`, `fp-worker`; 05-rbac §6.2); `StatusDisabled` ↔ `"inactive"`, `pending`/`system` are statuses an older build refuses (`user_active.go`); `EmailVerified` derives from `email_source`; `PasswordResetRequired` and `SignInRestricted` = blanked hash in the row + the extension member (§4.14) |
| `apikey:` + `idx:apikey:` | the legacy `APIKey` row (`store.go:982-997`) | hash reused as-is (already SHA-256 of raw); legacy atoms translated on read (05-rbac §6.1); empty legacy scopes → `LegacyFullScope=true` (05-rbac §4.5), kept as written; new keys: admitted legacy atoms in the row, canonical scopes in the extension, never empty (§4.4); status mapping §4.4 |
| `sess:<raw>` + `idx:sess:user:` | read and deleted, never written | **no raw session in any family this build writes** (SEC-19). A presented legacy raw-ULID cookie is looked up with epoch, status, revoked and expiry checks and accepted only if the row was created before the store's cut-off record (in the extension family, written once by the first r2 boot); on a hit the service issues a hashed `mcs_` session, sets the cookie and deletes the legacy row in one batch (`TakeLegacySession`: `Expect` + delete, DATA4-9). Sessions an older build issues during a rollback are newer than the cut-off and are refused after the roll-forward, and native sessions from before the rollback are revoked (§4.3): rollback = re-login, both ways. Legacy sessions live at most 7 d (`handlers/auth.go:90-96`); the fallback's code is removed by the first release after S4 + 7 days |
| `invite:<raw>` + `idx:invite:username:` | read in place by presented token, deleted on accept, on expiry and by §4.14; never written | the raw token is never copied (SEC-19's reasoning); accept re-checks `CanAssign` (§4.13, SEC3-2) and stages `Expect` + delete (DATA4-9); a user merge deletes the source's legacy invites, never re-owns them. Native invites are hash-only in the extension family and void on an older build |
| `oauthid:`, `idx:oauth:user:`, `inviteallow:` | the legacy rows | the invite allow-list feeds OIDC admission (§4.7) |
| bootstrap hash in settings | not read by core | books' row; core's bootstrap hash is a native one-time token, regenerated at every start; an older build regenerates its own |
| temp-login map | — | in-memory today; nothing to migrate |

**HEAD's open-time identity writers (DATA4-10, DATA4-12).** `initPebbleStore` writes on every open;
its identity part moves into the legacy store's `Reconcile`, run by `BootWrites` at P4d step 4:
`ReconcileVerifiedEmails` (restamps `email_source` on user rows an older build rewrote, and the e-mail
index; `internal/database/pebble_store_verified_email_backfill.go:310-330`) and the key expiry stamp
(`stampNeverExpiringAPIKeys`, `internal/server/apikey_expiry_stamp.go:23,40-54`), which keeps HEAD's
**one-shot** semantics by reading HEAD's own done flag (`setting:system:migration:apikey_expiry_stamp_v1_done`)
and afterwards only logs never-expiring keys found later. The books module's constructor writes
nothing (05-storage §5S.9.3 lists HEAD's other open-time writers).

`TestMirror_NarrowKeyNeverBecomesEmptyScope` and the rebuild tests of round 2 are replaced by
`TestAPIKey_StoredScopesNeverEmpty` and `TestOneFormat_LegacyRowsReadByPrevBuild` (§10).

**Deferred: cutover.** Moving ABO's identity families to the native store and deleting the legacy
rows is **not part of the extraction**. It is a later, per-family, one-way step behind the single
per-owner level guard (05-storage §5S.3.7), after its own pre-migration checkpoint, decided on
measured data (02 decision 13), and the only step that needs core to read books' rows — so the storage
grant for these families belongs to it; its floor is the cutover release itself. Until then no level
step exists for identity (the level guard still applies to books' own migrations).

### 11.1 Early fixes in the legacy code (independent of the extraction)

These are live defects at HEAD (Out-of-scope defects below, and 05-rbac's). They ship as hotfixes in
the current code as early as S0, tracked in `todo.d/2026-11-04_platform_core_followups.md`, and are
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
   non-empty scopes do not include every atom, in `RequireAdmin`, `adminRole` and the two in-handler
   checks `handlers/user.go:285,453`; requiring `auth.Can(users.manage)` instead is not enough (05-rbac
   "Out-of-scope defects" item 1, SEC4-11).
5. **The Basic gate accepts an empty password** — when `basic_auth_enabled` is true and the expected
   password is empty, HEAD's gate compares two empty strings and admits `<username>:`
   (`internal/server/middleware/basicauth.go:81-87`). Hotfix: every non-exempt route answers 401, health
   reports `degraded` (`basic_auth_no_password`) and the startup log names the setting
   (`TestBasicGate_EmptyPasswordRefuses`). This is defence in depth: the primary mechanism is resolution
   R4-2 — the first core write of the Basic password leaves a never-matching sentinel, never an empty
   value, in the legacy row and any `config.yaml` copy (05-config §1.12.2), so the older gate refuses
   every credential until re-entered — and the hotfix also covers a password cleared by any other means
   on a rollback target from S0 on (DATA4-1, SEC4-1, OPS4-1).

The revocation journal is not on this list: its lines are MAC'd with a key from the S3 keyring
(05-backup §5B.3.6), so it ships with the build that has the keyring (DATA3-19).

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
| G12 sessions unhashed (secrets part → 05-config) | **Fixed for auth credentials**: every session/invite/temp-login/bootstrap/key stored as SHA-256 (§4.1, §4.3); sessions only in the core-only family, never as raw rows (§11); config secrets → `core/secrets` (05-config) |
| G14 rate limit/lockout tied to tunnel topology, in memory | **Fixed + documented**: instance throttle, client address from `httpapi` trusted-proxy config with the connector set migrated, no hard lockout, in-memory by design (§4.9) |
| G15 two parallel auth stacks | **Fixed**: compat stays in app, resolves via `PrincipalFor` (§4.10) |

## Objections to 03

None open. 03 §3.7 (v1.6.0) states one stored format per family and raises a level only for a real
format change (books' own migrations and future cutovers), which is what §11 relies on.

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
