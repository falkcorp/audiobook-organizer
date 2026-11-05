<!-- file: docs/specs/platform-core/05-http-api.md -->
<!-- version: 1.13.4 -->
<!-- guid: fcd387ab-7a8e-49e2-a4b4-a6901aaee1e3 -->
<!-- last-edited: 2026-11-05 -->

# 05 — HTTP API framework (`core/httpapi`)

Evidence commit `748fd787a`. Raw inputs: [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §8,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Names, layers (L0–L9) and rules R1–R8 are from [03-architecture](03-architecture.md) v1.15.0; identity is
in [05-authn](05-authn.md), permissions and audit in [05-rbac](05-rbac.md), streams in
[05-realtime](05-realtime.md), and the releases R0–R3 in [09-abo-on-core](09-abo-on-core.md).

## 1. Purpose & scope

`core/httpapi` is the only way any module, the kernel, or the optional *arr facade puts a route on
the wire. It owns:

- the router (standard-library `net/http.ServeMux` with Go 1.22+ method/wildcard patterns — decision N7, confirmed in §1.1);
- typed route registration with generics and a **mandatory permission** (R6, fail-closed at registration);
- the **core-owned middleware chain**, handed to each module as an already-authenticated `Group` (fixes G8),
  including the perimeter (peer allowlist, recovery, security headers, CORS, cross-origin protection,
  Basic gate, rate limit);
- the error envelope for native routes, offset pagination with opt-in cursors and request IDs;
- OpenAPI 3.1 generation from registrations and the TypeScript client pipeline;
- the **legacy mount** (`MountLegacy`): ABO's gin engine (497 routes, +75 ABS) runs **permanently** as
  an `http.Handler` under the `books` module, behind core's perimeter, with each gin route registered
  as its own pattern from a reviewed manifest (cross-cutting resolution 2, SIMP-2). A books route
  group moves to native `httpapi` only when it is rewritten for its own reasons; there is no
  gin-retirement stage;
- the prefix-claim table that resolves path collisions and reserves `/api/v3` for *arr facades (§4.9;
  the facade is a deferred-build member, §3.6), and the place of the SPA handler in the route table (§4.10).

Out of scope: listener/TLS/HTTP-3 configuration beyond the handler (kept in `app` options,
[06-app-sdk](06-app-sdk.md)); what the SPA handler serves (`core/ui`, [07-ui-shell](07-ui-shell.md));
the *arr resource mapping ([08-arr-facade](08-arr-facade.md), a design sketch (non-normative)); health/metrics endpoints' payloads
([05-health](05-health.md), [05-metrics](05-metrics.md)). Sub-path deployment: apps are served at the
origin root only; there is no base-path option (FE-19 — ABO has none today: cookies use `Path=/`,
`internal/server/handlers/auth.go:226-247`, and the web client calls absolute `/api/...` URLs,
`web/src/utils/apiFetch.ts:116`). Operators use a host-based proxy.

### 1.1 Decision N7 confirmed: stdlib `ServeMux`, no gin in core

Measured gin feature use in non-test code at HEAD (`grep` counts over `internal/ cmd/ pkg/`):

| gin feature | uses | stdlib equivalent in this design |
|---|---|---|
| `*gin.Context` parameters | 964 | `context.Context` + typed `In[Req]` (§3.2) |
| `ShouldBindJSON` | 150 | `json.Decoder` in `Handle[Req,Resp]` |
| `binding:"required"` tags (go-playground validator) | 80, + 2 `oneof=` | `Validate() error` on the request type (§3.4); jsonschema `required` in OpenAPI |
| `c.Set`/`c.Get` | 50 / 22 | typed `context` keys (`auth.WithPrincipal`) |
| `c.File` | 13 | `http.ServeContent` / `httputil.ServeFileWithRange` (already `net/http`, `internal/httputil/rangeserve.go:186`) |
| `c.FullPath` | 8 | `(*http.Request).Pattern` (Go 1.23+) |
| `c.Stream` | 3 | `http.ResponseController` flush (§05-realtime) |
| `c.HTML` / `c.Render` | 3 / 2 | `html/template` in the few handlers that need it |
| `gin.WrapH` | 1 (`/metrics`) | not needed |
| `NoRoute` | 16 | the legacy mount's `/` fallback (§4.10), later the SPA handler |
| route groups + per-route middleware chains | everywhere | `Group` + `Route` metadata (§3.1) |

The table is what **native** routes use instead of gin; ABO's existing handlers keep gin under the
legacy mount, and core never imports gin either way. The only gin-specific *behaviour* with no
stdlib default is struct-tag validation, which native routes replace deliberately (explicit
`Validate`, visible in OpenAPI). For the **legacy** routes a second one matters: gin's tree routes a
static segment before a parameter, so HEAD has route pairs that `ServeMux` cannot register side by
side, which §4.8's collision rule handles (A-1). One gin behaviour the repo documents as a hazard disappears for
everything core wraps: gin binds a route's chain at registration, so a later `router.Use` silently
misses earlier routes (`internal/server/server_lifecycle.go:1712-1717`; the `Alt-Svc` middleware
added at `:957-960` after `setupRoutes` is the live instance, `raw/09` §2.2 note c). In `httpapi`
the chain is assembled once, after all registrations, by the kernel (§5). **No objection to N7.**

Third-party libraries considered and rejected for core: `huma/v2` v2.40.0 (typed handlers + OpenAPI
3.1 on top of `ServeMux`): its `huma.API`/`huma.Context` types would become part of every module's
signatures and therefore of the SDK stability gate (§12), and its security model is advisory
metadata, not a fail-closed permission requirement. `kin-openapi` v0.152.0, `swaggest/openapi-go`
v0.2.61: document models only; we generate a small 3.1 document ourselves.

**JSON Schema: one reflector in `core/internal/jsonschemax` (SIMP-14, SIMP2-3).** OpenAPI schemas
come from the same reflector 05-config uses for settings schemas: `github.com/google/jsonschema-go`
v0.4.3 (`jsonschema.For[T]` emits JSON Schema 2020-12, the dialect OpenAPI 3.1 uses). The wrapper is
the L0 leaf `core/internal/jsonschemax`, defined by 05-config §1.10.2 (it imports only jsonschema-go): the tag pass, the
`x-mediacore-*` keywords and an **extensible** override table, `type Overrides
map[reflect.Type]*jsonschema.Schema` with `func For[T any](o Overrides) (*jsonschema.Schema, error)`
(backed by `ForOptions.TypeSchemas`). Each package contributes the entries for its own types at its
own layer: `jsonschemax.Base()` holds `time.Time`; `config` adds `config.Duration` and `secrets.Ref`
(`config.Overrides()`; both are functions, because R5 forbids exported package-level maps); `httpapi` adds `rbac.Permission` (which it already imports) and its page types,
and reflects with `jsonschemax.Base().With(config.Overrides()).With(its own)` — one composition, so a
`config.Duration` field renders as `string/duration` in OpenAPI exactly as in the settings schema
(SIMP3-1). `httpapi` therefore imports `config` for the static `config.Overrides()` value but needs no live config
store, so a `coretest` router or the `examples/hello` route test builds without one; there is still
exactly one reflector. Verified on 2026-11-04 against the Go
module proxy and the module zip: v0.4.3 is the latest tag (published 2026-04-17, older than the
14-day release-age rule), MIT licence, one dependency (`github.com/google/go-cmp`), four releases in
the last 11 months; `TypeSchemas` exists in `jsonschema/infer.go`. **Pinning (MOD2-20).** A Go
`require` is a minimum, so "pinned exactly" needs a mechanism: Dependabot `ignore` entries (all
update types) for `github.com/google/jsonschema-go`, so it is bumped only by a deliberate PR, and
`check_module_skew.py --pin github.com/google/jsonschema-go=v0.4.3`, which asserts the resolved
version in both the root and `core/` `go list -m all` outputs. `TestSchema_Golden` remains the
after-the-fact drift check.
`go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` v0.72.0 gives core's stage 2 its server
spans; its metrics are turned off, and stage 2 records the three `http.server.*` instruments itself, with
the `go.opentelemetry.io/otel/semconv/v1.43.0/httpconv` constructors (part of the `go.opentelemetry.io/otel`
module core already requires), for **every** request, the legacy mount's included (§4.2). `otelgin`
leaves ABO's gin engine in R1, in the PR that moves the perimeter into core (13 §13.5.1); core never
imports it.

## 2. Current state at HEAD

| Topic | Today | Anchor |
|---|---|---|
| Router | `gin.New()`, trusted proxies disabled, `RedirectTrailingSlash=false`, `RedirectFixedPath=false` | `internal/server/server.go:638-640,689` |
| Registration | imperative, `setupRoutes` (`:1710`) + `wireHandlers` + 16 `wire_*_routes.go` + ~100 leftover lines; no route metadata; permissions exist only as closures in chains | `internal/server/server_lifecycle.go:1710-2135`, `internal/server/wire_handlers.go:42`, `raw/09` §2.1, §8.1 |
| Totals | 497 routes default build, +75 ABS; 15 public; 36 authenticated with no route permission; 45 `gone()` stubs | `raw/09` §1 |
| Permission helper | `s.perm(p)` returns a pass-through when `EnableAuth=false`, else `RequirePermission` | `internal/server/server_lifecycle.go:1693-1698` |
| Chain | peer allowlist (outside gin) → logger → recovery → security headers → CORS → basic auth → raw-response capture → gzip → tracing → [7 early routes] → `/api/*` 301 redirect → group: rate limit → body limit → CF Access → `RequireAuth` → per-route perm/admin/credGuard/owner | `raw/09` §2.2; `internal/server/server.go:695-731`; `internal/server/server_lifecycle.go:1746-1875` |
| Early routes outside the group chain | `/metrics`, `/health`, `/api/health`, `/api/v1/health`, `POST /api/v1/csp-report`, `/api/events`, `/auth/temp-login`, each with a hand-built auth chain | `internal/server/server_lifecycle.go:1731-1788` |
| Unauthenticated module mount (G8) | `s.router.Group("/api/v1/plugins")` — not under `api`/`protected`, so no rate limit, body limit, CF Access or `RequireAuth` | `internal/server/plugins_init.go:42`, `internal/plugin/router.go:11-26` |
| SPA serving | gin `NoRoute`, outside the API rate limiter; refuses server segments (`auth`, `socket.io`, `ping`, `status`, `logout`, `health`, `metrics`, `public`), non-GET/HEAD and case/slash variants; CF service-token recovery runs only on page navigations | `internal/server/server_lifecycle.go:2128-2141`, `internal/server/spa_fallback.go:58-125`, `internal/server/cf_recovery.go:36-60` |
| Basic-auth perimeter | global, but per-route exemption: only routes whose chain runs `ABSRequireAuth` are exempt; `/ping`, `/status`, `/login`, cover and public-session routes stay behind it | `internal/server/middleware/basicauth.go:24,51-70` |
| Client-IP trust | CDN client-IP header believed only from `TUNNEL_CONNECTOR_ADDRS` peers (IPv6 link-local **with zones** allowed); those peers are always admitted by the peer gate | `internal/server/client_trust.go:32,91-130,201`, `internal/server/peer_allowlist.go:28-34,223` |
| Cross-origin defence | cookie `SameSite=Strict` only; one handler adds a custom-header check | `internal/server/handlers/auth.go:233,246`, `internal/server/handlers/repairs/handler.go:27-28` |
| Versioning | `/api/v1/*` app API; `/api/v3/ops/*` (12 routes, own prefix); `/api/v1/operations/v2/*`; unversioned `/api/*` 301 → `/api/v1/*` with exclusions for v3, health, events, metrics and ABS-reserved paths | `internal/server/server_lifecycle.go:1791-1822,2127-2135`, `raw/09` §2.3 |
| Retirement | `gone()` 410 stubs keep auth+perm ordering, body `{"error":"gone","message":…}`, kept "one release" | `internal/server/gone.go:15-40` |
| Error shape | `httputil.ErrorResponse{error, code, status}` — a second shape for 410 | `internal/httputil/types.go:9-13`, `internal/httputil/respond.go:21-44` |
| Pagination | offset/limit, default 50, cap 1000; `ListResponse{items,count,limit,offset,total}`; the UI pages by total count | `internal/httputil/parse.go:74-90`, `internal/httputil/types.go:23-29`, `web/src/pages/Library.tsx:909-913` |
| JSON field naming | snake_case Go tags on the wire (`created_at`, `root_dir`, `op_id`) | `raw/13` §9 |
| Request IDs, idempotency keys, Deprecation/Sunset headers | none (0 non-test files mention `X-Request-ID`, `Idempotency-Key`, `Deprecation`, `Sunset`) | grep at HEAD |
| CORS | same-origin or dev Vite origin only, credentials allowed | `internal/server/server_middleware.go:56-97` |
| Security headers | nosniff, SAMEORIGIN, referrer policy, CSP + reporting on **every** response, HSTS on TLS | `internal/server/server_middleware.go:102-118`, `csp.go:57-67` |
| Rate limit | per-IP token bucket, on by default (100 rpm, burst `max(rpm/5,10)`), on the `/api` group only | `internal/server/server_lifecycle.go:1829-1838`, `internal/config/config.go:2797,2802` |
| Body limit | JSON 1 MiB / upload 10 MiB via `http.MaxBytesReader` | `internal/server/middleware/request_size.go:77`, `internal/config/config.go:2798-2799` |
| Listener | `http.Server{Handler: peerGatedHandler, ReadHeaderTimeout, WriteTimeout 0 for SSE, IdleTimeout, MaxHeaderBytes 1 MiB}` | `internal/server/server_lifecycle.go:903-920,1078` |
| Wire contract | tygo for 2 packages (~860 TS lines), opsgen enums, golden JSON contract test; ~260 hand-written TS interfaces in an 8,747-line `api.ts`; **no OpenAPI** | `tygo.yaml:12-40`, `internal/wirecontract/contract_test.go:1-20`, `web/src/services/api.ts:24`, `raw/13` §8 |
| Route-recording guards (prior art) | `credRoute`/`adminRoute` record each guarded route; tests walk the full route list and fail on unclassified routes | `internal/server/credential_routes.go:23-75`, `internal/server/wire_admin_role.go:18-104` |

## 3. Go API

Package `github.com/falkcorp/mediacore/httpapi` (layer L6; may import `auth`, `rbac`, `audit`,
`config` (for the static `config.Overrides()` value only; never a `*config.Store`, SIMP2-3,
SIMP3-1), `log`, `metrics`, `core/internal/jsonschemax`; not `storage`: with the idempotency store
deleted (§4.5) `httpapi` keeps no records). Implementation details in `core/internal/openapi` and
`core/internal/httpmw`. The import direction is `httpapi` → `internal/openapi`, never the reverse:
`internal/openapi` defines its own operation-description type, which `Router.OpenAPI` fills from
`RouteInfo` and the reflected schemas, so the two cannot form a cycle (MOD3-4).

**Minimum surface in R1 (COR2-9, COR3-3, MOD3-4).** `httpapi` lands in R1 with `internal/openapi`,
`internal/httpmw` and `internal/jsonschemax`, before `auth`, `audit` and the rest of `rbac`, which land
in R2. In R1 it needs only:

- the `rbac` leaf types `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`, `Public`,
  `AnyAuthenticated`, `Delegated`, `RoleName` and the `Checker` interface (05-rbac §3, which decides
  this list; COR4-11, MOD4-11, COR6-1). A legacy manifest route records the native requirement that
  books' atom table gives its gin permission (§8.3); in R1 nothing evaluates it, because the mount is
  `LegacyAuthGin` (stages 12–13 off), and its scopes are checked against the catalog once `rbac`
  exists;
- the `auth` leaf types `Principal`, `Method`, `MethodSpec`, `Assurance`, `User`, `UserID` and
  `UserStatus` (05-authn §3), which land next to rbac's so `In.Principal` (whose `User` and `Perms`
  fields name them) and `RouteInfo.AuthMethod` compile and `Registrar.AuthMethod` can record the ABS
  auth method (06 §6.4.10; SEC3-14, COR4-11);
- `config.Overrides()`, `log` and `metrics`.

`Deps.Auth`, `Deps.RBAC` and `Deps.Audit` are R2 additions; struct fields added later are additive
under the API-stability gate. While they are absent, `Build` refuses a native route other than
`Public`/infra (`ErrNoAuthService`). That holds through R1, which adds no native endpoint: only the
legacy mount with `LegacyAuthGin`, infra and the legacy `/` owner exist, and stages 12–13 are skipped
for the mount (§8.1).

### 3.1 Routes and groups

```go
package httpapi

import (
	"context"
	"iter"
	"net/http"

	"github.com/falkcorp/mediacore/rbac"
)

// Route is the declarative description of one operation. It is data: the kernel
// reads it for permission coverage (R6), OpenAPI, metrics labels and audit.
type Route struct {
	Method  string // http.MethodGet, ...; required
	Path    string // ServeMux path relative to the Group base, e.g. "/books/{id}"; use "{$}" for exact
	Name    string // operation name, lowerCamel, unique per module; OpenAPI operationId = <module>_<Name>
	Perm    rbac.Requirement // REQUIRED: zero value is rejected at registration (R6)
	Summary string
	Doc     string
	Tags    []string

	Guards      Guards       // extra identity requirements (interactive-only, owner step-up)
	MaxBody     int64        // 0 = core.http.body.json_max_bytes; -1 = streaming upload, handler enforces
	RateClass   RateClass    // RateDefault, RateAuth, RateExpensive, RateExempt
	ETag        bool         // opt-in: GET returns ETag from the entity Version, writes honour If-Match (§4.3)
	Success     int          // success status; 0 = 200 (201 if Resp is Created[T], 204 if NoContent)
	Lenient     bool         // accept unknown JSON fields; zero value = strict (§3.4, COR3-13)
}

// Guards are identity requirements beyond the permission.
type Guards struct {
	Interactive bool // refuse API keys, ABS tokens and auth-disabled (today's credGuard, auth.Method.MayChangeCredentials)
	Owner       bool // require an owner-assurance principal (today's RequireOwner; 05-authn §4.8)
}

type RateClass uint8

const (
	RateDefault RateClass = iota
	RateAuth              // login/setup/bootstrap/invite: tighter per-source buckets (05-authn §4.9)
	RateExpensive         // search, exports
	RateExempt            // infra probes and the SPA handler only; rejected for non-Public routes
)

// Group is a mount point whose whole core chain is already applied. A module
// receives exactly one, rooted at /api/<moduleID>/v1, from module.Registrar.HTTP().
// There is no API that yields an unauthenticated or chain-less group (G8).
type Group struct{ /* unexported: owner module, base, version, registry */ }

func (g *Group) Base() string             // "/api/books/v1"
func (g *Group) Module() string           // "books"
func (g *Group) Sub(prefix string) *Group // "/api/books/v1/series"; same chain
func (g *Group) Version(major int) *Group // "/api/books/v2"; major >= 2 (§4.6)

// Handle registers a typed JSON operation. It never panics: errors are collected
// and returned by the kernel from app.New (fail-closed boot, §5).
func Handle[Req, Resp any](g *Group, rt Route, fn func(ctx context.Context, in *In[Req]) (Resp, error))

// HandleRaw registers an operation that writes its own response (file download,
// range serving, SSE). Perm, guards, rate and body limits still apply. The
// OpenAPI entry is described by the provided RawDoc.
func HandleRaw(g *Group, rt Route, doc RawDoc, h http.Handler)

// Sugar over module.Registrar.HTTP()'s *Group (06): a deferred registration
// built from method helpers. Exactly equivalent to Handle.
type Registration interface{ apply(g *Group) }
type RouteOption func(*Route)

func Get[Req, Resp any](path string, perm rbac.Permission, fn func(context.Context, *In[Req]) (Resp, error), opts ...RouteOption) Registration
func Post[Req, Resp any](path string, perm rbac.Permission, fn func(context.Context, *In[Req]) (Resp, error), opts ...RouteOption) Registration
// Put, Patch, Delete likewise.
func Requires(r rbac.Requirement) RouteOption // Public / AnyAuthenticated / AllOf instead of perm ("" perm + no Requires = ErrNoPermission)
func Summary(s string) RouteOption
func Named(name string) RouteOption
func Register(g *Group, regs ...Registration)
```

### 3.2 Typed input and pages

```go
// In is the decoded request. Req's fields bind by tag:
//   path:"id"      -> r.PathValue("id")
//   query:"limit"  -> URL query (string, ints, bools, []string, time.Time RFC 3339, encoding.TextUnmarshaler)
//   header:"If-Match"
//   body:"json"    -> exactly one field: JSON body (methods with a body only)
// Binding is compiled once per Req type at registration (reflection cached), so a
// bad tag is a boot error, not a request-time 500.
type In[Req any] struct {
	Req       Req
	Principal *auth.Principal // never nil on non-Public routes
	RequestID string
	Request   *http.Request // escape hatch; handlers must not write to a ResponseWriter
}

// Created and NoContent are response wrappers recognised by Handle.
type Created[T any] struct {
	Location string
	Body     T
}
type NoContent struct{}

// Page is the DEFAULT list response (§4.4): today's ListResponse shape
// (internal/httputil/types.go:23-29), so the UI keeps page counts.
type Page[T any] struct {
	Items  []T   `json:"items"`
	Count  int   `json:"count"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// PageParams is embedded in list request types.
type PageParams struct {
	Limit  int `query:"limit"`  // default 50, max 1000 (today's cap, internal/httputil/parse.go:74-90)
	Offset int `query:"offset"` // default 0
}

// CursorPage is the OPT-IN list response for append-only feeds (audit, journal).
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type CursorParams struct {
	Limit  int    `query:"limit"`
	Cursor string `query:"cursor"` // opaque
}

// EncodeCursor/DecodeCursor: base64url(pos || filterHash). No HMAC: a forged
// position only starts a scan elsewhere, and every row is permission-checked
// as it is read (SIMP-8). A filter mismatch is 400 invalid_cursor.
func EncodeCursor(pos []byte, filterHash uint64) string
func DecodeCursor(s string, filterHash uint64) (pos []byte, err error)

// PaginateCursor drives an iter.Seq2 from storage into a CursorPage, stopping at limit+1.
func PaginateCursor[T any](ctx context.Context, p CursorParams, seq iter.Seq2[Positioned[T], error], filterHash uint64) (CursorPage[T], error)

type Positioned[T any] struct {
	Pos  []byte // storage key or sort key
	Item T
}
```

### 3.3 Errors (problem+json, native routes)

```go
// Problem is the native error body, RFC 9457 application/problem+json with
// extension members. Every native route and every core middleware emits it;
// legacy-mount handlers keep their own bodies (§4.3).
type Problem struct {
	Type      string       `json:"type"`             // "https://mediacore.dev/problems/<code>" stable URI, or "about:blank"
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"` // request path
	Code      string       `json:"code"`               // machine code, snake_case, stable: "not_found", "permission_denied", ...
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`              // validation details
	Required  string       `json:"required_permission,omitempty"` // on 403, the missing scope
}

type FieldError struct {
	Field   string `json:"field"` // JSON pointer, "/body/title" or "/query/limit"
	Code    string `json:"code"`  // "required", "too_long", "invalid_enum", ...
	Message string `json:"message"`
}

// Error is what handlers return; core maps it to a Problem. Plain errors map
// to 500 "internal" with Detail withheld (logged with request id).
type Error struct {
	Status int
	Code   string
	Detail string
	Fields []FieldError
	Err    error // wrapped cause, never serialized
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

// Constructors for the common cases.
func NotFound(kind, id string) *Error
func Conflict(code, detail string) *Error
func Invalid(fields ...FieldError) *Error
func Field(pointer, code, message string) FieldError // Invalid(Field("/body/name", "too_long", "must be 1..64 characters"))
func Forbidden(scope rbac.Permission) *Error
func Unavailable(detail string, retryAfter time.Duration) *Error

// Sentinel causes that modules may wrap; core maps them without a constructor.
var (
	ErrNotFound     = errors.New("httpapi: not found")    // 404 not_found
	ErrConflict     = errors.New("httpapi: conflict")     // 409 conflict
	ErrPrecondition = errors.New("httpapi: precondition") // 412 precondition_failed (If-Match)
	ErrUnavailable  = errors.New("httpapi: unavailable")  // 503 unavailable
)

// Registration-time errors returned (joined with errors.Join) by the kernel.
var (
	ErrNoPermission       = errors.New("httpapi: route has no permission requirement")
	ErrUnknownScope       = errors.New("httpapi: route permission is not in the catalog")
	ErrForeignScope       = errors.New("httpapi: route permission is outside the module namespace")
	ErrDuplicateRoute     = errors.New("httpapi: duplicate method+pattern")
	ErrPatternConflict    = errors.New("httpapi: ServeMux patterns conflict")
	ErrPrefixCollision    = errors.New("httpapi: path prefix claimed by two owners")
	ErrBadBinding         = errors.New("httpapi: invalid request binding tags")
	ErrSchema             = errors.New("httpapi: type cannot be described in OpenAPI")
	ErrJSONName           = errors.New("httpapi: JSON property name is not snake_case")
	ErrLegacyManifest     = errors.New("httpapi: legacy route missing from or inconsistent with the manifest")
	ErrLegacyAuthGin      = errors.New("httpapi: LegacyAuthGin mount not named by app.Options.LegacyAuthGinUntil")
	ErrOwnAuthMethod      = errors.New("httpapi: OwnAuth route names no auth method registered by its module")
	ErrNoAuthService      = errors.New("httpapi: non-Public native route registered before auth/rbac exist")
	ErrPublicNotAllowed   = errors.New("httpapi: module native route uses rbac.Public but is not in its Manifest.PublicRoutes")
	ErrOutsideClaim       = errors.New("httpapi: legacy route pattern outside its owner's claims")
	ErrShadow             = errors.New("httpapi: a route of one owner is strictly more specific than a route of another owner over the same paths")
)
```

### 3.4 Validation and JSON naming

```go
// Validator is implemented by request types that need more than JSON decoding.
// Return Invalid(...) or errors.Join of *FieldError-carrying errors.
type Validator interface{ Validate() error }
```

Decoding rules: `Content-Type` must be `application/json` (415 otherwise) for `body:"json"`; body is
read through `http.MaxBytesReader(MaxBody)` (413 `body_too_large`); every route not marked `Lenient`
uses `Decoder.DisallowUnknownFields` (400 `unknown_field`) — the zero value is strict (a Go `bool`
cannot default to true, so the field is the opt-out, COR3-13).

**JSON naming (FE-12, normative).** Every JSON property of a native route's request and response
types, of `Problem` and of the page types is **snake_case** (`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`).
Reasons: it is ABO's wire convention today (`raw/13` §9), so Go types that move to native routes keep
their tags and the tygo/`internal/wirecontract` goldens stay valid. `Build` fails with `ErrJSONName`
when a reflected property violates it. The *arr facade is exempt (it mirrors a foreign camelCase API,
08). Query parameters follow the same rule.

### 3.5 Kernel-facing router

```go
// Router is built by the kernel only (kernel/ imports it; modules never see it).
type Router struct{ /* mux, registry, chain, claims */ }

func NewRouter(opts Options, deps Deps) *Router

type Options struct {
	CoreGroupBase  string        // "/api/core/v1"
	TrustedProxies []TrustedPeer // client-IP header believed only from these; always admitted by stage 0 (SEC-18)
	ClientIPHeader string        // "" = RemoteAddr only; e.g. "X-Forwarded-For" or a CDN header
	CORS           CORSOptions   // allowed origins; also the trusted origins of stage 7
	Limits         LimitOptions
	OpenAPI        OpenAPIOptions
	// ServerName is server.address on every series and span stage 2 records:
	// a constant, never the request's Host (MOD10-2). The kernel fills it from
	// app.Options.ServiceName (06 §6.4.5), the value it also passes as
	// metrics.Options.ServiceName (ABO: "audiobook-organizer"; default: AppID).
	ServerName     string
}

// TrustedPeer is a prefix or a single address that may carry an IPv6 zone
// (netip.Prefix cannot): ABO's TUNNEL_CONNECTOR_ADDRS allows zoned link-local
// connector addresses (client_trust.go:91-130). Zone matching keeps
// client_trust.go:133-201 semantics.
type TrustedPeer struct {
	Prefix netip.Prefix // valid when Addr is zero
	Addr   netip.Addr   // may carry a zone
}

type Deps struct {
	Auth          *auth.Service        // R2 addition (§3, COR2-9, COR3-3)
	RBAC          *rbac.Service        // R2 addition
	Audit         audit.Logger         // R2 addition; before it, stage 7/13 refusals go to the access log (§4.2)
	Log           *slog.Logger
	Metrics       *metrics.Scope       // "mediacore.http" (05-metrics): core's own http families (§11)
	MeterProvider metric.MeterProvider // the App's Provider.MeterProvider(); stage 2 creates the three
	                                   // semconv http.server.* instruments on its "httpapi" meter (§4.2, 05-metrics §3)
}

func (r *Router) ModuleGroup(moduleID string) *Group    // /api/<id>/v1
func (r *Router) CoreGroup() *Group                     // /api/core/v1; used only by core/internal/<capability>http, uihttp included (03 §3.3, COR2-19)
func (r *Router) CoreFixedGroup(base string) *Group     // a core group at a HEAD path kept because that is cheaper (below); core/internal/authhttp only, from F4
func CoreExactPatterns() []string                       // the declared exact core patterns allowed inside a module claim (§4.9); exactly two entries, HEAD's OAuth callback and start
func (r *Router) Infra(pattern string, perm rbac.Requirement, h http.Handler) // /livez, /readyz, /metrics; non-Public perms are enforced (§4.2, SEC2-14)
func (r *Router) MountLegacy(m LegacyMount) error       // §8.2
func (r *Router) SPA(h http.Handler, o SPAOptions) error // §4.10; ErrPrefixCollision while a legacy mount ServesSPA
func (r *Router) NonSPA() func(*http.Request) bool      // §4.10, for ui.Options (FE-3, FE2-6)
func (r *Router) NonSPASegments() []string              // the first segments behind NonSPA: data for the kernel's P8 check ProblemSPASegmentCollision (06 §6.10; §4.10, FE2-7, FE3-9)
func (r *Router) Claim(owner, prefix string) error      // subtree claim, §4.9
func (r *Router) Routes() iter.Seq[RouteInfo]           // archtest, OpenAPI, docs
func (r *Router) Build() (http.Handler, error)          // registers into a fresh ServeMux; errors.Join of all registration errors
func (r *Router) OpenAPI(module string) (*openapi.Document, error)

// ClientAddrFrom returns the client address stage 0 resolved for this request
// (peer, or the trusted proxy's client-IP header) and whether it was
// attributed through a trusted proxy. From R1 the legacy gin limiters read
// it instead of re-deriving it (§8.1, SEC2-19).
func ClientAddrFrom(ctx context.Context) (addr netip.Addr, attributed bool)

// MatchedPattern returns the ServeMux pattern the legacy mount's wrapper
// matched for this request, as registered (method and host included), and
// false outside a request the wrapper hands to gin. Its one reader is the
// SEC2-22 post-match check, which runs inside gin before any handler (§8.2).
func MatchedPattern(ctx context.Context) (pattern string, ok bool)

// ReservedSegments returns the first path segments core itself serves: "api",
// "v<digits>", "livez", "readyz", "metrics", "health", "auth", "events",
// "assets", "static", "ui" (the SPA's hashed-asset and core UI paths, FE2-11).
// It is static: the kernel evaluates it at P1, before any MountLegacy (P6), so
// it carries no app segment (COR2-19). One input of module.ReservedIDs()
// (03 §3.6). A function, not an exported slice (R5).
func ReservedSegments() []string

// SPAOptions is what the kernel passes with the shell handler at P8 (06
// §6.5.1). BasicExemptAssets is stage 8's static-asset exemption for the
// SPA's own paths: the kernel passes ui.BasicExemptAssets (07 §7.6.1),
// HEAD's basicauth.go:40-46 rule; nil exempts nothing (FE5-10).
type SPAOptions struct {
	BasicExemptAssets func(path string) bool
}

type RouteInfo struct {
	Owner      string // module ID or "core"
	Method     string
	Pattern    string
	Name       string
	Perm       rbac.Requirement
	Guards     Guards
	RateClass  RateClass
	Kind       RouteKind // Native, Raw, Legacy, Infra, SPA (Facade: deferred-build, §3.6)
	OwnAuth    bool      // legacy route authenticated by its own (ABS) authenticator (§8.2)
	AuthMethod string    // the auth method an OwnAuth route names (§8.2)
}
```

**Two core paths outside `/api/core/v1`.** HEAD's OAuth/OIDC callback
`GET /api/v1/auth/oauth/{provider}/callback` (03 §3.8: unchanged because that is cheaper, not a frozen contract; CONFORM-12) keeps its path, so no identity provider needs its redirect URI
re-registered; from F4 core serves it (05-authn §4.13). `core/internal/authhttp` registers it on
`CoreFixedGroup("/api/v1/auth/oauth")`, the base ABO supplies as `app.Options.Auth.OIDCCallbackBase` (05-authn
§3.2; an app that sets none gets no fixed group), whose routes run the whole core chain like `CoreGroup()`'s and record
`RouteInfo.Owner` `core`. The path lies inside books' `/api/v1/` claim, so it is admitted only as an entry of
`CoreExactPatterns()`, the declared exception list of §4.9; `Build` refuses a `CoreFixedGroup` route whose
pattern is not on it. HEAD's OAuth start `GET /api/v1/auth/oauth/{provider}/start` is kept the same way
(SAFETY-11): ABS clients such as AudioBooth start a Google or GitHub sign-in there and D12 (c) freezes
the ABS API, so `core/internal/authhttp` registers it on the same fixed group, as the second
`CoreExactPatterns()` entry, served by core's start handler with the `AppHandoff` deep-link rules
(05-authn §4.7), exactly as `GET /api/core/v1/auth/oidc/{provider}/start` is.

**App root segments: two inputs with different meanings (FE2-11, 03 §3.6).** ABO's ABS-compatible
routes, a fixed contract that books mounts (D12 (c), §4.6), occupy two different kinds of segment.
`app.Options.ReservedAPINamespaces` holds second segments under `/api` (ABO's `/api/libraries`, …):
reserved module IDs only, known at P1. `LegacyMount.TopSegments` holds the mount's top-level server
paths (ABO's `ping`, `status`, `logout`, `socket.io`, `public`): reserved module IDs **and** `NonSPA`.
Because `ReservedSegments()` is static and evaluated at P1, before any `MountLegacy` (P6), it carries
neither input (COR2-19); the kernel validates module IDs against the static owner lists and
`ReservedAPINamespaces` at P1 and re-checks them against every recorded mount's `TopSegments` at P7,
still before listeners open. `LegacyMount.SPAShared` lists the top-level segments where the mount's
routes share a path with an SPA page and which are therefore **not** in `NonSPA`: ABO's is
`["login"]`, whose ABS route is `POST /login` while `GET /login` is the SPA page, as today (SEC3-16).
`Build` fails with `ErrPrefixCollision` when a legacy manifest route outside `/api/` has a first
segment that is in neither `ReservedSegments()`, `TopSegments` nor `SPAShared`, so a new top-level
legacy path cannot silently fall through to `index.html`.

**`NonSPA` comes from `TopSegments` only (FE3-1, resolution R3-5).** The first segment of a
`module.Manifest.LegacyPrefixes` entry is a reserved module ID, nothing more: `NonSPA` is the union of
§4.10's core inputs and every mount's `TopSegments`, and **never** includes a `SPAShared` segment. A
legacy prefix is non-SPA only for the paths it actually serves, so ABS `POST /login` never makes the
`/login` page non-SPA, and the P8 shell-segment check passes for a `SPAShared` segment that is also a
shell route (`login`) while it fails for a `TopSegments` one (06 §6.10). 06 and 07 use this model.

### 3.6 Deferred-build members

These land with `arrfacade` (08). Cross-cutting resolution 1 defers `arrfacade`, and deferral applies to features inside built packages
as well as to packages (SIMP2-6, 03 §3.4). These members of `httpapi` are specified here so that 08
can rely on them, and are **not built** in R1 or R2; they land in the PR that adds `arrfacade`. The
table is planning information (resolution A-4: no archtest checks the absence of these members):

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `httpapi.Router.FacadeGroup`, `httpapi.RouteKindFacade` | stage 13's facade clause; the OpenAPI `apiKeyHeader` (`X-Api-Key`) security scheme; tests `TestFacadeKey_RefusedOnCoreAndModuleGroups`, `TestFacadeKey_RefusedOnABS`, `TestBuild_PrefixCollision_FacadeVsFacade` | `arrfacade` (08) | `func (r *Router) FacadeGroup(owner, base string) *Group` mounts an *arr facade (§4.9) with route kind `Facade`; stage 13 refuses a facade-purpose key on every non-facade route and a non-facade key on facade routes (SEC-5, 05-authn §3.5) |

Until then SEC-5 holds trivially: no facade key can be minted (05-authn §3.5). The PR that builds a
deferred member deletes that member's row. When the facade lands, the additions are additive and pass
the API-stability gate.

## 4. Behaviour

### 4.1 Path layout

| Prefix | Owner | Notes |
|---|---|---|
| `/api/core/v1/…` | core | auth, users, roles, api-keys, sessions, audit, tasks, settings, system, events (SSE), openapi |
| `/api/<moduleID>/v1/…` | module | e.g. `/api/books/v1/books/{id}` (03 §3.6) |
| `/api/<moduleID>/v{N}/…` | module | a new major only for a breaking change made after a second app consumes the owner's API; until then (R0–R3 included) a breaking change replaces the route in one PR (§4.6) |
| `/livez`, `/readyz` (Public), `/metrics` (`core:metrics.read`, which ABO's identity migration stores on every principal that is not `Confined()`, 05-rbac §6) | core infra | payloads in 05-health / 05-metrics; probe names as 05-health defines them (FE2-6) |
| `/api/v1/…`, `/health`, the ABS-compatible root routes (§4.6); until their R2 flips also `/auth/temp-login` (F4), `/api/v3/ops/…` (F5), `/api/events` (F6) and `/api/health` (F8) | `books` legacy mount (**permanent**, §8) | gin routes from the manifest. Each R2 flip deletes the gin routes its native core routes replace (identity, settings, tasks, events, …; §8.1), with no alias; the SPA, the first-party callers and the deploy files that call them switch in the same PR |
| `/api/v1/auth/oauth/{provider}/callback` and `/api/v1/auth/oauth/{provider}/start`, from F4 | core (`CoreFixedGroup`, §3.5; the two `CoreExactPatterns()` entries, §4.9) | HEAD's OAuth/OIDC callback path, kept so no identity provider is re-registered (03 §3.8), and HEAD's start path, kept because ABS clients start there (D12 (c)); both with HEAD's native-app hand-off (§8.1 F4 row, 05-authn §4.7) |
| `/api/v3/…` (or `/<moduleID>/api/v3/…`) | *arr facade owner (`tv`/`movies`) | §4.9; deferred-build (§3.6) |
| `/` | the `books` legacy mount (`ServesSPA`) **until ABO adopts the shell build**; then `core/ui` | §4.10; never both (FE-1) |

Reserved module IDs are owned by 03 §3.6. The kernel validates against `module.ReservedIDs()`, the
union of `httpapi.ReservedSegments()` (`api`, `v<digits>`, `livez`, `readyz`, `metrics`, `health`,
`auth`, `events`, `assets`, `static`, `ui`, §3.5), `ui.ReservedSegments` (shell routes, 07; a static
function that lands in R1 with `module`, ahead of the rest of `ui`, so the equality test below
holds from R1, FE3-8),
`rbac.ReservedNamespaces()` (`core`, `custom`) and the app's two root-segment inputs
(`app.Options.ReservedAPINamespaces`, `LegacyMount.TopSegments`, §3.5); a test asserts that the three
owner lists together equal 03's row exactly.

### 4.2 Middleware chain (core-owned, fixed order)

Applied by `Router.Build` around every route; a module cannot reorder, skip or add to it (a
module needing per-route behaviour uses `Route` fields). Outermost first. "All" = every request,
including infra, the SPA handler and the legacy mount; "routes" = registered routes (native,
legacy manifest routes, and **`Infra` routes whose `perm` is not `Public`**, e.g. `/metrics`; SEC2-14).
Public infra probes (`/livez`, `/readyz`) stay Public and `RateExempt`:

| # | Stage | Scope | Replaces |
|---|---|---|---|
| 0 | Peer allowlist (`net/netip` prefixes; loopback always allowed; `TrustedProxies` always allowed — today's connector rule, `peer_allowlist.go:28-34,223`); resolves the client address (the client-IP header believed only from `TrustedProxies`) once and publishes it in the request context (`ClientAddrFrom`) for every later stage and for the legacy gin limiters (SEC2-19) | listener `http.Handler`, before routing | `internal/server/peer_allowlist.go:237` |
| 1 | Request ID: generate ULID, keep a well-formed inbound `X-Request-ID` (`^[A-Za-z0-9._-]{1,64}$`) as `client_request_id`; echo `X-Request-ID` | all | new |
| 2 | Tracing (`otelhttp`, span name = `r.Pattern`). **Span attributes carry the redacted URL** (SEC-22): `url.path`, `url.query` and `url.full` are written after `log.Redactor` (05-logging) applies its query list `log.DefaultRedactQuery()` (which is ⊇ HEAD's `credentialQueryKeys`, including `code`, `code_verifier`, `refresh_token` and `secret`, `internal/server/middleware/log_redact.go:26-37`; this file does not restate the list, SEC2-18) and its `log.DefaultPathRules()` (the books legacy invite path `/invite/{token}`, SEC2-6; both are functions returning fresh copies, MOD5-5). If `otelhttp` cannot be configured to emit only redacted values, stage 2 starts the server span itself with semconv attributes; `TestTrace_NoApikeyInSpanAttributes` decides. **HTTP metrics: one recorder for every request (D10).** `otelhttp` gets a no-op meter provider (spans only). Stage 2 creates `http.server.request.duration`, `http.server.request.body.size` and `http.server.response.body.size` **once, at `Build`**, with the `go.opentelemetry.io/otel/semconv/v1.43.0/httpconv` constructors, on core's `httpapi` meter of the App's `MeterProvider` (`Deps.MeterProvider`, 05-metrics §3), and records **every** request with them: native, `Infra`, SPA and legacy mount alike, gin's `NoRoute` and the legacy `/` fallback included. The duration histogram carries the semconv bucket boundaries (`0.005 … 10` s) as explicit advice at creation. Attributes: the normalised method, `http.response.status_code` and `server.port` (integers), `Options.ServerName` as `server.address` (never the request's `Host`, so no client can mint a series), and `http.route`. It reads the route, and names the span, after the handler returns, from one per-request carrier that it owns and puts in the context before the mux, so a later stage that clones the request (`r.WithContext`) cannot lose it. The handler wrapper of each registered route writes its pattern into the carrier, method and host stripped, before the route's own stages run: a native or `Infra` route its pattern (`/api/core/v1/tasks/runs/{id}`, semconv `http.route`), a legacy manifest route its mux pattern (`/api/v1/books/{id}`; a generalised pattern of §4.8 records the generalised pattern). A request answered before a route's wrapper ran (stage 6's preflight answer, a stage 7 refusal and the other stages before routing, fixed by `TestChain_OrderGolden`), a request the router answers with no route (an unclean `/api/` path, the legacy `/api/` redirect, an unmatched path, the `/` owner's `NonSPA` 404) and the `/` owner's pages record **no** `http.route`; a refusal by stages 10–13 records its route. `http_route` values are therefore mux patterns (`{id}`), not HEAD's gin templates (`:id`), a change D10 allows and no shipped dashboard or rule reads (05-metrics §13). `TestStage2_RecordsEveryRequest`, `TestStage2_RouteSurvivesRequestClone` and `TestStage2_ServerAddressIgnoresHost` decide (§12) | all | `tracingMiddleware`'s spans incl. invite-path redaction, `internal/server/server.go:1672`, `internal/server/middleware/trace_redact.go`; gin's `otelgin`, removed from the engine in R1 (13 §13.5.1) |
| 3 | Recovery: panic → problem 500 `internal`, stack logged redacted, span marked error, `mediacore_http_panics_total` | all | `RedactingRecovery`, `internal/server/server.go:704` |
| 4 | Access log (`slog`, the same 05-logging redaction as stage 2, skip streams — including HEAD's `SkipPaths` for the two operations streams, `server.go:696`, COR3-6). Before `audit` exists (until the identity flip F4) the stage 7 and stage 13 refusals are written here with the structured attribute `security_event` (SEC2-20) | all | `gin.LoggerWithConfig`, `internal/server/server.go:695` |
| 5 | Security headers (nosniff, frame, referrer, permissions policy; HSTS only when `r.TLS != nil` or a trusted proxy says https). **CSP on every response (FE-4, FE2-1):** when the handler has not set `Content-Security-Policy` by the time headers are written, stage 5 writes the owner's policy — the SPA handler's from `core/ui`; for the legacy mount, `LegacyMount.DefaultCSP(r)`, a function evaluated **per response** (ABO passes today's `contentSecurityPolicy()`, `csp.go:95-106,129-138`: `script-src 'self'` plus the SHA-256 of `index.html`'s inline theme script, and `connect-src 'self' https://<Access team host>` so `apiFetch` still recognises a Cloudflare Access expiry redirect, `csp.go:34-46`); otherwise `default-src 'none'; frame-ancestors 'self'`. **`Reporting-Endpoints: csp="<owner's report URL>"`** with every CSP (legacy mount: `/api/v1/csp-report`, as `server_middleware.go:109` sets it today; `ui`: `/api/core/v1/ui/csp-report`), so Chromium, which prefers `report-to`, keeps reporting. **`X-App-Build: <UI build id>`** on every response from R1 — legacy mount, native, infra, 404, 405 and 410 included (FE2-5, OPS2-13). **One source, the embedded bundle (FE3-2, FE4-1, R5-7):** the app's npm build script itself (`npm run build`, through its Vite config; after the shell flip the `@mediacore/shell/vite` preset) generates the UI build ID and embeds it as `<meta name="mediacore-build">` in `index.html`, so every build path — `make build`, the Docker stages, goreleaser, a bare `npm run build` — stamps it with no variable to carry and no side script (07 §7.6.3, FE5-1). The UI build ID is a reload discriminator, **never the binary's identity**: updater, storage and backup identify a binary by `app.BinaryID` (06 §6.4.5: the app version plus the executable's SHA-256), which stage 5 never writes (FE5-3). No Go build step receives a UI build ID: stage 5 writes the ID of the bundle the `/` owner embeds — after the shell flip `ui.Bundle.BuildID()` (07), before it the mount's `LegacyMount.BuildID` (ABO fills it from its embedded `web/dist/index.html`) — otherwise `dev`. Every build path (make, Docker, goreleaser) therefore serves the value its own bundle carries, a server whose header disagrees with its bundle cannot be built, and there is no boot-time build-ID check. `""` or `dev` (API-only and dev builds) means "unknown", which clients never treat as a mismatch. A missing `X-App-Build` counts as a mismatch only on a response that carries a `Reporting-Endpoints` header naming a `csp` group (FE4-3): HEAD sets that header on every response (`internal/server/server_middleware.go:102-109`) and stage 5 writes it with every CSP from R1, while a proxy's 502 or an Access interstitial carries neither (07 §7.5.2). `X-Request-ID` is no discriminator: HEAD never writes it, and every build that writes it also writes `X-App-Build` | all | `internal/server/server_middleware.go:102-118` |
| 6 | CORS (explicit allowlist, same-origin always) | all | `internal/server/server_middleware.go:56-97` |
| 7 | **Cross-origin protection (SEC-10, FE-5):** `net/http.CrossOriginProtection` (stdlib since Go 1.25; the toolchain pin is 1.27) on unsafe methods: refuse when `Sec-Fetch-Site` is present and not `same-origin`/`none`, or, without it, when `Origin` is present and does not match the host; trusted origins = `core.http.cors.allowed_origins`; refusal 403 `cross_origin_rejected`, audited from the identity flip F4 (before it, access log `security_event`, SEC2-20). **No credential type is exempt** (FE2-23): non-browser clients pass because they send neither `Sec-Fetch-Site` nor `Origin`, not because of the credential they carry; a browser attaches cached HTTP Basic credentials cross-site exactly like cookies, so Basic is never a bypass, and a browser cannot attach `Authorization`/`X-Api-Key` cross-origin without a CORS preflight that stage 6 refuses for unlisted origins. **Host-rewriting proxies:** over plain HTTP browsers send no `Sec-Fetch-Site`, so the `Origin` comparison decides; when the TCP peer is a `TrustedProxies` member, its `X-Forwarded-Host` replaces `r.Host` for that comparison (a proxy that rewrites `Host`, nginx's default, otherwise refuses every UI POST); from any other peer `X-Forwarded-Host` is ignored. Operators behind a proxy that sends neither may list the public origin in `core.http.cors.allowed_origins`; the first refusal raises a health warning naming that key. Applies to the legacy mount too (today's only defence there is `SameSite=Strict`, which is site-scoped and does not stop a sibling app). A development proxy that rewrites `Host` (Vite `changeOrigin: true`) must rewrite `Origin` to the target origin as well, or every dev POST over a LAN address is refused (07 §7.10, FE3-19) | all | new (supersedes per-handler checks such as `repairs/handler.go:27-28`) |
| 8 | Optional perimeter gate (HTTP Basic, off by default). Exempt **per route, never per mount** (SEC-14), with HEAD's exempt set (`basicauth.go:34,40-46,69`; COR4-9): Public infra probes; legacy routes with `OwnAuth` (§8.2; accepted only for registered auth methods, SEC2-17); legacy routes with `LegacyRoute.BasicExempt` (HEAD's two health paths, `/api/health` and `/api/v1/health`, which monitors call; from F8, which deletes them (05-health §5H.3.3), `/health`, the path their callers switch to. From F8 `/health` is a new handler, books' answer from core's health registry, and its unauthenticated answer carries only the fixed, listed token set of 05-health §5H.3.3 (HEAD's memdb and startup tokens and `/readyz`'s readiness tokens); auth, config and backup posture tokens (`auth_disabled_remote`, `config_fail_closed_override`, `revocation_journal_broken`, `restore_last_admin_kept`, `basic_gate_no_password`, …) appear only on authenticated endpoints, so a caller the gate stops learns nothing about posture through the exemption (SEC-7)); and, on the `/` owner, the paths its `LegacyMount.BasicExemptAssets` predicate matches (HEAD's static-asset rule: the `/assets/` prefix, `/favicon.ico` and the `.js`, `.css`, `.png`, `.svg`, `.woff2` suffixes; after the shell flip F9 the same predicate arrives as `SPAOptions.BasicExemptAssets` when the kernel mounts the shell with `Router.SPA`, §3.5, 07 §7.6.1, FE5-10). Everything else, including the ABS routes that run no ABS authenticator, stays behind it, as `basicauth.go:51-70` does. The gate compares the presented username and password with `core.http.basic_gate.username` and `.password`, each in constant time, as HEAD does (`basicauth.go:80-84`; §10, SEC6-9). **No password, no entry (resolution R5-1(c), SEC5-4, DATA5-12):** when `core.http.basic_gate.enabled` is true and `core.http.basic_gate.password` is unset (`ErrNotSet`), cleared, empty or unreadable (`ErrKeyUnavailable`), every non-exempt request gets 401, health reports `degraded` (`basic_gate_no_password`) and the startup log names the key; a presented empty password never matches. An operator value locks the password like any key (§10), so a password set in the environment is used however the stored row reads, which is also early fix 5's recovery from a lock-out (OPS9-7). HEAD's comparison ported as it is would admit `<username>:` (`basicauth.go:81-87`) — HEAD's key-regeneration delete path and a partial reset both produce that state (`TestBasicGate_EmptyPasswordRefuses`) | all | `internal/server/middleware/basicauth.go:24` |
| 9 | Compression (gzip; skipped for `HandleRaw` streams, `/metrics`, every response whose `Content-Type` is `text/event-stream`, and every legacy route with `LegacyRoute.NoCompress` — HEAD's exclusions `/api/events`, `/metrics`, `/api/v1/auth/accept-invite` and `^/api/v1/operations/v2/[^/]+/logs/download$`, `internal/server/server.go:1590-1606`, whose last entry prevents double-gzipped log files, COR3-6). The compressing writer implements `Unwrap` and `Flush`, so `http.ResponseController` reaches the connection | all | `compressionMiddleware`, `internal/server/server.go:725` |
| 10 | Per-client rate limit (token bucket per client address from stage 0, `ClientAddrFrom`; class from `Route.RateClass` or `LegacyRoute.RateClass`) | routes | `internal/server/middleware/ratelimit.go:96` |
| 11 | Body limit (`MaxBody`; for legacy routes an outer cap of `core.http.body.upload_max_bytes`, gin keeps its per-group limits) | routes | `internal/server/middleware/request_size.go:77` |
| 12 | Authenticate: `auth.Service.Middleware` (05-authn §4.2): binds a principal or records an `auth.Failure`; **stops at the first authenticator that returns anything other than `ErrNoCredential`**; writes no response except 503 `auth_unavailable` (SEC-12) | routes, except `OwnAuth` legacy routes and, in R1 (`LegacyAuthGin`), the whole legacy mount (§8.1) | `RequireAuth` step 3–6 + `cfMW` (`raw/10` §2.2) |
| 13 | Authorize: recorded failure → on non-Public routes 401 with the failure code; on Public routes the cookie rule of 05-authn §4.3 (anonymous + cookie cleared; an invalid Bearer/API key is still 401). No principal on non-Public → 401 `authentication_required`. `Guards`. `Perm` via the principal's checker (confinement and `Sensitive` included, 05-rbac §4.4, §4.10; a legacy route's requirement from books' atom table, §8.3, 05-rbac §6, COR5-4) → 403 problem with `required_permission`. (The facade-key clause is a deferred-build member, §3.6.) Denial → `audit` (coalesced) from the identity flip F4 | routes, same exceptions as 12 | `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner`, `abortWorkerOnlyOutsideWorkerAPI` (whose ABS half is kept in the bridge, §8.3) |
| 14 | Handler (`Handle` decode → validate → fn → encode; or the gin engine for legacy routes, behind the pattern check of §8.2) | route | — |

Stages 12–13 being **inside** the module's `Group` is what fixes G8: there is no group without
them. Public routes (`rbac.Public`) still pass 12 (so a credential, if sent, is bound and audited)
and skip the 401 in 13. A per-principal limiter (v1.0.0 stage 13) is deferred until abuse is
observed (SIMP-8); with the client address migrated correctly (SEC-18) the per-client limiter covers
today's case.

### 4.3 Response and error rules

- Success: `Content-Type: application/json`; bodies are the `Resp` value (no `{data:…}` wrapper),
  snake_case (§3.4). `Created[T]` → 201 + `Location`; `NoContent` → 204.
- Errors on native routes and core stages: always `application/problem+json` with the `Problem`
  shape (§3.3). Legacy-mount handlers (gin) keep their own bodies (`{error, code, status}`,
  `internal/httputil/types.go:9-13`); RFC 9457 is for native routes only (SIMP-8).
- **401 semantics (FE-7).** Stage 13 answers 401 only with these codes: `authentication_required`
  (no credential), `invalid_credential`, `expired`, `revoked`, `inactive_user` (a presented
  credential failed, 05-authn §6). These, and only these, mean "the session is gone". Public auth
  flows return their own domain codes — `invalid_credentials` (login), `bootstrap_invalid`,
  `invite_invalid` — and their OpenAPI operations carry `x-mediacore-permission: public`, from which
  the generated client marks the call `skipAuthRedirect` (§7); the shell shows such errors in place
  (07). 503 `auth_unavailable` means "retry", never "log out".
- 401 vs 403 split kept from `RespondPermissionDenied` (`internal/server/middleware/auth.go:510-519`):
  no principal → 401; principal without scope → 403 `permission_denied` naming the scope.
- 404/405 for unmatched paths: the router wraps `ServeMux` and, when `mux.Handler(r)` returns an
  empty pattern, answers problem 404 `not_found` or 405 `method_not_allowed` with the `Allow`
  header the mux computed (captured from the mux's own response, body discarded) — never the mux's
  plain-text body. Unmatched paths under a legacy prefix get 404 in the legacy error shape (§8.2).
  The SPA fallback rules are in §4.10.
- ETags (opt-in, `Route.ETag`): `GET` of a versioned entity returns `ETag: "<version>"`;
  `PATCH`/`PUT`/`DELETE` honour `If-Match` (412 `precondition_failed`). Storage rows already carry
  `Version` (`internal/database/store.go:744,976`).

### 4.4 Pagination

**Offset is the default (SIMP-8).** `?limit=` (default 50, max 1000 — the current cap,
`internal/httputil/parse.go:74-90`) and `?offset=`; the response is `Page[T]` with `total`, today's
`ListResponse` contract (`internal/httputil/types.go:23-29`). The UI pages by total count
(`web/src/pages/Library.tsx:909-913`, 11 `.tsx` files use `Pagination`), and library lists are
sorted by title/author/date through the search index, not in Pebble key order, so a key-order cursor
would not serve them.

**Cursor is opt-in per route** for unbounded append-only feeds read newest-first: the audit log
(05-rbac §7.6) and the system journal (05-logging). The cursor is `base64url(pos || filterHash)`
without an HMAC; a cursor replayed with different filters → 400 `invalid_cursor`. A route that
declares a cursor returns `CursorPage[T]` and no total.

### 4.5 Idempotency: task start only, owned by `tasks` (SIMP2-8)

`httpapi` keeps no idempotency store (v1.2.0's `c/http/idem/`, `Route.Idempotent`,
`ErrIdempotencyNotPOST`, `core.http.idempotency.ttl`, its prune task and
`mediacore_http_idempotency_total` are deleted). The only route that used it,
`POST /api/core/v1/tasks/definitions/{kind}/runs`, already has a persisted start-idempotency store in
`tasks` (`StartRequest.IdempotencyKey`, `c/tasks/idem/<kind>/<key>`, 05-tasks §5T.3.10), and two
stores with different TTLs could disagree. The task-start handler (`core/internal/taskhttp`) copies a
well-formed `Idempotency-Key` header (`^[A-Za-z0-9_-]{8,128}$`) into `StartRequest.IdempotencyKey`; on
a duplicate it answers `200` with the existing run's `RunView` and `Idempotent-Replayed: true` (the
current run view, which is the more useful answer than a byte-identical replay). A later non-task
side-effect route that needs idempotency adds it then, in its owner.

### 4.6 Versioning

- Within a major: additive only (new optional fields, new routes, new enum values only where the
  schema marks the enum `x-extensible-enum`). CI gate: `oasdiff` v1.35.0 `breaking` against the
  OpenAPI document of the last release tag (committed as `api/openapi/<owner>.json`, §7); a breaking
  diff fails `make ci` unless the route is in a new major. The gate serves the apps that build on
  core's API and on each other's, not ABO's HEAD clients.
- **Until a second app consumes core's API (R0–R3 included), a breaking change replaces the route in
  one PR, with its callers (D10)**: core's only consumer is ABO's SPA, built in the same release, so no
  second major is served beside the first. The keep-the-old-major rule below applies only after that
  (CONFORM-9).
- After that, a breaking change goes into a new major (`/api/<id>/v{N}`, `Group.Version`). The old
  major is deleted in a later release, which that release's note names; nothing in `httpapi` dates it,
  marks it deprecated or answers 410 for it (D10).
- **No aliases (D10).** A path that changes is deleted and registered under its new pattern in one
  PR, and the SPA, the first-party scripts and the agent skills that call it switch in the same
  PR (09). Core's own routes exist only under `/api/core/v1`, except HEAD's OAuth/OIDC callback and
  start, which keep HEAD's paths (§3.5, §4.9 `CoreExactPatterns()`; neither path moves, so neither is an
  alias); each R2 flip deletes the gin routes that its native routes
  replace (§8.1).
- **Books legacy routes** stay gin routes under the permanent legacy mount (§8); `/api/v1/…` is a
  permanent `books` prefix. A books group rewritten natively moves to `/api/books/v1/…` and its gin
  routes are deleted in the same PR. HEAD's existing `gone()` stubs are books' gin code and stay
  there (`internal/server/gone.go:15-40`); core has no `Gone`.
- **The ABS-compatible root routes are a fixed contract that books mounts (D12 (c)).** `/ping`,
  `/status`, `/login`, `/logout`, `/auth/refresh`, `/auth/openid*`, `/api/authorize`, `/api/me*`,
  `/api/libraries*`, `/api/items*`, `/api/session*`, `/public/session/{id}/track/{index}`,
  `/api/playlists*`, `/api/collections*` and the authors, series and narrators routes keep their
  paths, bodies and auth semantics, pinned by the ABS conformance suite (05-authn §4.10). They are
  manifest routes of the books mount with `OwnAuth` (§8.2), and no versioning rule of this section
  applies to them.

### 4.7 Request IDs and correlation

Request ID (stage 1) is put in the context (`httpapi.RequestID(ctx)`), as `slog` attribute
`request_id` by the core log handler (05-logging), as span attribute `http.request_id`, in every
`Problem`, and in every audit record (05-rbac §7). Background tasks started by a request record
the request ID as `origin_request_id` (05-tasks).

### 4.8 `ServeMux` specifics the router must handle

| Behaviour of `net/http.ServeMux` (Go 1.22+) | Handling |
|---|---|
| Registering two conflicting patterns panics | `Build` registers inside a function with `recover()`, converts the panic text (which names both patterns) into `ErrPatternConflict`; duplicates are detected earlier by our own map for a clearer message. Legacy routes that gin accepts but `ServeMux` cannot register side by side are resolved before registration by the collision rule below |
| Subtree pattern `/x/` redirects `/x` → `/x/` | native and legacy manifest routes use exact patterns (`{$}` where needed; gin runs with `RedirectTrailingSlash=false`, `server.go:639`); subtree patterns are allowed only for the legacy `/api/` and `/` fallbacks and the SPA |
| Unclean paths (`//x`, `/./x`, `/a/../b`) are 301-redirected to the clean path | gin does not do this (`RedirectFixedPath=false`, `server.go:640`), and the SPA fallback answers such variants itself (`spa_fallback.go:90`). The router checks `path.Clean` before the mux: an unclean `/api/` path → problem 404; any other unclean path goes to the `/` owner (legacy mount or SPA handler) unchanged, whose own normalization decides |
| More specific pattern wins across methods/paths | the basis of native routes shadowing legacy ones (§8.1) and of prefix claims (§4.9) |
| `{name...}` wildcard | translation of gin `*name`; gin `:name` → `{name}` |
| 405 + `Allow` generated automatically | wrapped into problem+json (§4.3) |
| Wildcards match **escaped** segments (`/a%2Fb` is one segment), while gin with `UseRawPath=false` routes on the decoded `r.URL.Path` (`server.go:637-641`), so the two can pick different routes for one request | the legacy handler wrapper carries the matched pattern into gin as `httpapi.MatchedPattern(ctx)` (§3.5, §8.2), and after gin routed, a request whose `c.FullPath()` is not the manifest entry of that pattern (for a generalised pattern, of one of its members) gets 404 in the legacy error shape before any gin handler runs (SEC2-22, §8.2) |

**Route collision rule (resolution A-1, ADV-1).** Gin's tree accepts two routes that `ServeMux` refuses
as a pair: gin routes a static segment before a parameter at the same depth, while two `ServeMux`
patterns that both match some path, and of which neither is more specific, conflict and panic. HEAD has
such pairs. The advisor registered the HEAD route dump (`raw/09-http-api.md` §3.1–3.2, 572 routes at
`748fd787a`) pair by pair in a go1.27.0 `ServeMux` under this section's translation (`:name` →
`{name}`, `*name` → `{name...}`, `/` → `/{$}`) and found exactly five conflicting pairs
(`reviews/spec-advisor-review.md` appendix A, re-checked against the code):

| Pattern A | Pattern B | Build | Anchors (@748fd787a) |
|---|---|---|---|
| `GET /api/v1/operations/{id}/activity` | `GET /api/v1/operations/v2/{id}` | default | `internal/server/wire_library_routes.go:48`; `internal/server/wire_operations_routes.go:27` |
| `GET /api/v1/operations/{id}/changes` | `GET /api/v1/operations/v2/{id}` | default | `wire_operations_routes.go:110`; `:27` |
| `GET /api/v1/operations/{id}/result` | `GET /api/v1/operations/v2/{id}` | default | `wire_operations_routes.go:103`; `:27` |
| `GET /api/v1/operations/{id}/results` | `GET /api/v1/operations/v2/{id}` | default | `internal/server/server_lifecycle.go:1952`; `wire_operations_routes.go:27` |
| `GET /api/me/item/{id}/remove-from-continue-listening` | `GET /api/me/item/listening-sessions/{id}` | ABS on | `internal/server/handlers/abs/handler.go:871`; `:885` (the comment at `:882-884` records that gin routes the static sibling first) |

Go reports, for example, that both patterns of the first pair match `/api/v1/operations/v2/activity`
and neither is more specific. Every default-build route above is `s.perm(library.view)`, and both ABS
routes run `ABSRequireAuth` with `loginCeiling` and no route permission. The rule:

1. **Conflict graph.** `Build` (the manifest generator of §8.2) registers every manifest route and
   every native route of the same build pair by pair as above, for each build variant (ABS on and
   off), and takes the connected components of the "conflicts with" relation. A component that holds
   routes of more than one owner fails `Build` with `ErrPatternConflict`, with no exemption: a PR that
   moves a route of a conflict component to another owner moves the whole component.
2. **Kept and folded routes.** In a component, a route stays its **own** pattern when gin routes every
   overlap with each of its conflicting partners to it (a static segment where the partner has a
   parameter, gin's tree rule). Every other route of the component is **folded**. For HEAD's five pairs
   the kept routes are `GET /api/v1/operations/v2/{id}` and `GET /api/me/item/listening-sessions/{id}`.
3. **One generalised pattern.** The folded routes, the component's **members**, register as one
   pattern: their shared path with every segment in which a member or a kept partner differs replaced
   by a wildcard named by position (`GET /api/v1/operations/{p4}/{p5}`, `GET /api/me/item/{p4}/{p5}`).
   Every kept route must be strictly more specific than it, so `…/operations/v2/{id}` stays its own,
   strictly more specific pattern, and the generalised pattern must conflict with no other route.
   Members of different segment counts, or with a `{name...}` wildcard, cannot share a pattern. Each of
   these cases fails `Build` with `ErrPatternConflict`.
4. **Identical requirements.** The members must agree in every field a stage before gin reads: `Perm`
   (the same atom-table requirement, §8.3), `Guards`, `Public`, `RateClass`, `OwnAuth` with its
   `AuthMethod`, `BasicExempt` and `NoCompress`. Any difference fails `Build` with `ErrPatternConflict`, so the judgment
   stages 8–13 make on the generalised pattern is the judgment for each member. Both of HEAD's sets meet
   this.
5. **Post-match check.** The pattern check of the previous row and of §8.2 accepts the `c.FullPath()` of
   any member of the matched pattern and refuses everything else (404, legacy shape), so a path the
   generalised pattern matches but no member does (`/api/v1/operations/x/y`) never reaches a handler.
   Every component is a legacy mount's, so the check always runs behind the mount.
6. **Real engine.** From the R1 PR that lands the mount, the test runs over `(*gin.Engine).Routes()` of
   the real engine, built with ABS on and with ABS off, never over a fixture
   (`TestLegacyManifest_RegistersAsMuxPatterns`). For each conflicting pair it also sends Go's overlap
   example through the mux and through gin and asserts that the mux pattern chosen has gin's route
   among its members, or is that route.

**HEAD's two components are books'.** The default-build one holds books' domain handlers (the activity
feed, `wire_library_routes.go:48`; the undo changes and result handlers, `wire_operations_routes.go:103,110`;
the metadata batch-candidate page, `server_lifecycle.go:1952` @748fd787a) and HEAD's
`GET /api/v1/operations/v2/{id}`; the ABS one is part of the fixed ABS contract (§4.6). Both fold as
above from R1. The tasks flip F5 deletes the `/api/v1/operations*` routes that native task routes
replace (05-tasks §5T.3.10 lists them; the Activity page moves to the native routes in that PR and polls
`GET /api/core/v1/tasks/runs` until the events flip F6).
Whatever stays re-folds by rules 1–6 in that PR with no special case, and a route left with no
conflicting partner registers as its own pattern. No core route can join a books component: core's
patterns lie under `/api/core/`, and the one fixed callback path (§3.5) conflicts with no books route.

### 4.9 Prefix claims and the `/api/v3` reservation

(The facade half of this section — `FacadeGroup` and its URL-base rule — is a deferred-build member,
§3.6; the single claim rule applies from R1.)

`Router.Claim(owner, prefix)` records every subtree an owner occupies (core groups, module groups,
facade groups, and each `module.Manifest.LegacyPrefixes` entry). `Build` fails with
`ErrPrefixCollision` when two owners claim overlapping prefixes — one rule, no claim kinds and no
yielding (SIMP-22, as 06 §6.4.2 states it).

**Patterns stay inside their owner's claims (SEC3-8).** ServeMux panics only on *conflicting*
patterns; a strictly more specific one wins silently, so a claim alone does not stop one owner from
capturing another's requests (a legacy manifest route at `GET /api/core/v1/auth/oidc/google/callback`
would receive the OIDC `code` and `state`). `Build` therefore fails with `ErrOutsideClaim` when a
legacy manifest route is outside its mount owner's `LegacyPrefixes` (a native route cannot be: its
pattern is relative to its `Group`), and with `ErrShadow` when any route of one owner is strictly more
specific than a route of another owner over the same path set. There is no exemption other than the one below. The two
fallbacks are not subtree claims: `/` (the SPA fallback, §4.10) and the legacy `/api/` redirect
handler (item 3), both matched last by `ServeMux` specificity.

**The one exception: exact core patterns inside a module claim.** `CoreExactPatterns()` (a function, R5)
declares the exact core patterns that may lie inside another owner's claim, and it holds exactly two
entries, both served by core from F4: `GET /api/v1/auth/oauth/{provider}/callback`, HEAD's OAuth/OIDC
callback, so no identity provider is re-registered (03 §3.8, 05-authn §4.13), and
`GET /api/v1/auth/oauth/{provider}/start`, HEAD's OAuth start, because ABS clients such as AudioBooth
start there and D12 (c) freezes the ABS API (05-authn §4.7; SAFETY-11). For those two patterns alone
`Build` raises neither `ErrPrefixCollision` against books' `/api/v1/` claim nor `ErrShadow` against books'
routes; it still fails with `ErrPrefixCollision` when a manifest route matches a path either pattern
matches, so the F4 PR deletes HEAD's gin callback and start handlers in the same change. Every other core
route stays under `/api/core/`. Adding an entry is a spec change: `TestCoreExactPatterns_TwoEntries` pins
the list at those two patterns.

**`/api/v3` is reserved for *arr facades**, whose clients call `<base>/api/v3/…` (`raw/16` §5.1).
ABO's HEAD `/api/v3/ops/*` (12 routes, `internal/server/server_lifecycle.go:2127-2135`) is HEAD's task
API: in R1 books still claims `/api/v3/` for it, and the tasks flip F5 deletes it with the other routes
that native task routes replace (05-tasks), dropping that claim in the same PR. ABO ships no facade, so
nothing collides in either release.

1. In **separate binaries** (the priority case) no two owners claim `/api/v3/`: ABO ships no *arr-v3
   facade (the Readarr API was `/api/v1`, and Readarr is retired, `raw/16` §6).
2. The facade is mounted through `Router.FacadeGroup(owner, base)` with `base` from config
   `modules.<id>.arrfacade.url_base` (default `""` → `/api/v3`). *arr clients support a URL base
   (Prowlarr, Seerr and Bazarr all store a base URL, `raw/16` §5.1), so in a **multi-app binary**
   the kernel **requires** a non-empty URL base for every facade (`/tv`, `/movies` → `/tv/api/v3`),
   and boot fails if two facades would share `/api/v3/`.
3. The legacy unversioned redirect (`/api/*` → `/api/v1/*`, `server_lifecycle.go:1791-1822`) is
   re-implemented as the least-specific `/api/` handler of the legacy mount, so every claimed prefix
   (`/api/core/`, `/api/books/`, a facade) is matched before it by `ServeMux` specificity and can
   never be redirected into `/api/v1`. A request the redirect rule excludes is answered 404 in the
   legacy error shape.

### 4.10 The SPA handler and `/` (FE-1, FE-3, FE-25)

- **One owner of `/`.** While the `books` legacy mount declares `ServesSPA: true`, it owns `/` and
  gin's `NoRoute` keeps serving the SPA with every protection it has today (non-SPA segments,
  non-GET/HEAD, case and slash variants, CF service-token recovery on page navigations,
  `spa_fallback.go:58-125`, `cf_recovery.go:36-60`). `Router.SPA` returns `ErrPrefixCollision` in
  that case and the kernel does not mount `ui.Handler`. From R1 the legacy `/` fallback consults
  `Router.NonSPA()` **before** it calls gin, so a path core added to the non-SPA set (core's `/livez`,
  `/readyz` and their case or slash variants) gets the router's problem 404 instead of gin's
  `NoRoute` page while gin's static `nonSPASegments` list still lacks it — one predicate serves both
  owners (FE3-13). When ABO adopts the shell build (07; the shell flip F9, in R2 if it is ready, else R3),
  the same PR drops `ServesSPA`, deletes gin's `NoRoute`/`setupStaticFiles`, and the kernel calls
  `Router.SPA(ui.Handler(...), SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})` (FE5-10). No `/` pattern is reserved by `httpapi` itself.
- **Place in the chain.** The `/` owner (SPA handler or the legacy `ServesSPA` fallback) runs stages
  0–9 only — peer allowlist, request ID, tracing, recovery, access log, headers (with the SPA's CSP),
  CORS, cross-origin protection, Basic gate, compression — and no rate limit, body limit,
  authentication or authorization: a cold load fetches index, entry and several chunks, and today the
  SPA fallback sits outside the API rate limiter (`server_lifecycle.go:2128-2141`). It is recorded as
  `RouteInfo{Kind: SPA, Perm: rbac.Public, RateClass: RateExempt}` and is in
  `archtest.RoutesHavePermissions`' known infra set.
- **What never reaches it (FE2-6).** `Router.NonSPA()` returns a predicate, with the case and
  trailing-slash normalization of `normalizeNonSPAPath`, over the union of: every claimed prefix
  (`/api/`, module and core groups); the first segment of every `Infra` pattern (`livez`, `readyz`,
  `metrics`); `health` and `metrics` explicitly (today's list, `spa_fallback.go:74-83`: a case or
  slash variant of a probe path must never answer `200 text/html`, or a monitor concludes the server
  is healthy); `auth` and `events`; and every mount's `LegacyMount.TopSegments` (ABO: `ping`,
  `status`, `logout`, `socket.io`, `public`). For ABO that reproduces today's eight segments (`auth`,
  `socket.io`, `ping`, `status`, `logout`, `health`, `metrics`, `public`) and adds core's probes.
  `assets`, `static` and `ui` are reserved IDs but **not** in `NonSPA`: they are the SPA handler's own
  path space. `LegacyMount.SPAShared` segments are excluded (§3.5). The kernel passes the predicate
  to `ui.Options` (07). Those paths, and any non-GET/HEAD request, get a problem 404 / 405 from the
  router, never `index.html`.
- **Shell routes and server segments are disjoint (FE2-7, FE3-9).** At P8 the kernel checks
  `ui.ReservedSegments() ∩ Router.NonSPASegments() = ∅` and fails boot with 06's
  `ProblemSPASegmentCollision` otherwise (one name and one test, 06 §6.10; this file only supplies the
  data): a shell page whose first segment is a server segment (the shell's former `/logout` against
  ABO's ABS `logout`) would be reachable only by client-side navigation and answer a typed URL or a
  refresh with a JSON 404. `SPAShared` segments are not in `NonSPASegments()` (§3.5). At the same P8 step the
  kernel also compares the embedded shell bundle's `ui.Bundle.AppID()` with `app.Options.AppID` and fails boot with
  06's `ProblemUIAppIDMismatch` when they differ (07 §7.6.1, FE8-7); this file supplies no data for that check.

## 5. Lifecycle

1. `app.New` builds `httpapi.Router` after `auth`, `rbac` and `audit` exist (rule R4's order; in release R1
   none of them, §3).
2. Core's capability routes are registered on `CoreGroup()` by one package per capability,
   `core/internal/<capability>http` (`authhttp`, `rbachttp`, `audithttp`, `taskhttp`, `eventhttp`,
   `confighttp`, `backuphttp`, `updaterhttp`, `healthhttp`, `libraryhttp`, `storagehttp` (from the
   identity flip F4: `POST /api/core/v1/system/factory-reset`. Its confirmation contract is the one
   `storage.ResetRequest` that 05-storage owns (resolution R9-6): the request carries `{kind, confirm:
   "RESET", delete_library_files?, library_root?, library_file_count?}`; a missing or wrong `confirm`
   answers 422 `confirm_required`, and when `delete_library_files` is true but `library_root` or
   `library_file_count` does not match the fresh preflight count, which `kernel.stageReset` compares
   with the request, the server answers 422 `library_files_confirm_required`, whose problem body
   carries the current values, and stages nothing. The same struct flows unchanged through the
   storagehttp-local `Stager.StageReset` (never `*storage.Admin`, COR8-2) and `kernel.stageReset`;
   05-rbac §6.2 states the route's guards and wire shape. HEAD's two gin reset routes,
   `POST /api/v1/system/factory-reset` (`ResetFactory`) and `POST /api/v1/system/reset`
   (`ResetStore`), are deleted in release R1, whose factory reset is offline only (`storage reset`,
   05-storage §5S.3.15) while the SPA hides its reset control; from F4 the SPA calls the native route),
   `modulehttp` for
   `GET /api/core/v1/modules` (the kernel passes it a read-only `ModulesView` — catalog snapshot,
   `restart_required`, the `ui_build_id` source: the served **shell** bundle's ID,
   `ui.Bundle.BuildID()`, and `""` while a legacy mount owns `/`, whose ID only `X-App-Build` carries
   (07 §7.5.1, FE5-7); it is not `--version-json`'s `ui_build_id`, which is the value `X-App-Build`
   reports, the embedded bundle's meta, legacy or shell, or `dev` without one (06, FE7-13) — so it
   imports nothing above L6, FE3-16), and
   `uihttp` for `ui/diagnostics` and `ui/csp-report`, so `ui` registers nothing on `CoreGroup` itself; layer L6,
   imported only by `kernel`, 03 §3.3, COR-18, COR2-19); each module's `Register(r module.Registrar)`
   receives `r.HTTP() *httpapi.Group` and, for ABO, calls `MountLegacy`. From F4 `authhttp` also
   registers the OIDC callback on `CoreFixedGroup` (§3.5). Registration only **records**; nothing touches
   the mux.
3. Kernel calls `Router.Build()`: validates every route (permission present and in catalog, scope
   namespace rule from 05-rbac §4.9, binding tags, schema generation, snake_case names, the legacy
   manifest against the gin engine, `LegacyAuthGin` and `OwnAuth` admission of §8.2, claims,
   cross-owner shadowing), validates prefix claims and the `/` owner,
   registers into a fresh `ServeMux`, assembles the chain, and returns `errors.Join` of every
   problem. Any error aborts boot **before listeners open** (03 §3.7). A module's native
   route may use `rbac.Public` only when the module's `Manifest.PublicRoutes` names it (06 §6.4.2;
   hello's `getGreeting`), else `ErrPublicNotAllowed`; the set of every `Public` route (core and
   module native, and legacy) is also a reviewed golden, `TestPublicRoutesGolden`, like
   `TestOwnAuthRoutesGolden` (COR3-3). The shell-segment check runs at P8 in the kernel
   (§4.10).
4. `App.Run` serves the handler on configured listeners. Shutdown: `http.Server.Shutdown(ctx)` with
   the app's drain deadline; SSE handlers end on `Server.RegisterOnShutdown` + `Hub.Close`
   (05-realtime).
5. Routes are immutable after `Build` (restart-bound enablement, 03 §3.7).

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Route without `Perm` | boot error `ErrNoPermission` | closed | R6 |
| Route `Perm` scope unknown / foreign namespace | boot error | closed | prevents typos and cross-module escalation (05-rbac §4.9) |
| Pattern conflict that the collision rule of §4.8 cannot resolve (a cross-owner component, members with different requirements or shapes, a generalised pattern that conflicts again) / duplicate / prefix collision (a manifest route over the `CoreExactPatterns()` entry included) / two owners of `/` | boot error `ErrPatternConflict` / `ErrDuplicateRoute` / `ErrPrefixCollision` | closed | silent shadowing is worse than not starting; a resolvable same-owner set registers as one generalised pattern (A-1) |
| Native JSON property not snake_case | boot error `ErrJSONName` | closed | one wire convention (FE-12) |
| Legacy route missing from the manifest or without a permission | boot error `ErrLegacyManifest` | closed | R6 over the running table (SEC-13) |
| `LegacyAuthGin` mount not named by `app.Options.LegacyAuthGinUntil`; `OwnAuth` route naming no auth method its module registered | boot error `ErrLegacyAuthGin` / `ErrOwnAuthMethod` | closed | no module skips stages 8, 12, 13 by self-declaration (SEC2-17) |
| Shell route segment equals a non-SPA server segment | boot error `ProblemSPASegmentCollision` (06) | closed | FE2-7 |
| Legacy route outside its owner's claims; a cross-owner more specific pattern | boot error `ErrOutsideClaim` / `ErrShadow` | closed | silent capture of another owner's requests (SEC3-8) |
| Mux pattern and gin's own match disagree (escaped segment; a generalised pattern matched by no member) | 404 in the legacy shape, gin handler not run | closed | SEC2-22, A-1 |
| Non-Public `Infra` route without the scope (`/metrics`) | 401 / 403 like any route | closed | SEC2-14 |
| Request ID generation fails (crypto/rand) | fall back to monotonic counter id `local-<n>` | open | correlation only, not security |
| Tracing exporter down | request proceeds | open | observability must not take the API down |
| Panic in handler | problem 500, connection kept | closed for the request | contain blast radius |
| Peer not in allowlist | TCP-level 403 problem, no routing | closed | network perimeter |
| Cross-origin unsafe request | 403 `cross_origin_rejected`, audited | closed | CSRF (SEC-10) |
| Rate limit exceeded | 429 `rate_limited`, `Retry-After`, `RateLimit-Policy`/`RateLimit` headers | closed | abuse control |
| Rate limiter state store full (LRU at cap) | evict least-recently-used non-pinned bucket | open | memory bound beats perfect limiting |
| Body over limit | 413 `body_too_large` | closed | memory |
| Malformed JSON / unknown field / validation | 400 / 400 / 422 with `errors[]` | closed | |
| Authentication store read error | 503 `auth_unavailable` (not 401) | closed | never treat "can't check" as "anonymous ok"; today returns 500 (`middleware/auth.go:163-167`) |
| Presented credential refused | 401 with the failure code (Public route: cookie-only failures proceed anonymous, 05-authn §4.3) | closed | SEC-11, SEC-12 |
| No principal on non-Public route | 401 `authentication_required` | closed | |
| Missing scope / confined principal | 403 + audit denial (access log `security_event` until the identity flip F4) | closed | |
| Audit write for a denial fails | request still 403; `mediacore_audit_dropped_total` | open (audit) / closed (request) | the denial is enforced regardless |
| Cursor invalid or filter mismatch | 400 `invalid_cursor` | closed | |
| Response encode error after headers sent | log + abort connection | — | nothing else possible |

## 7. OpenAPI 3.1 and the TypeScript client

**Generation.** `Router.OpenAPI(owner)` produces one document per owner (core, each module,
facade excluded — the facade mirrors a foreign API) from `RouteInfo` plus reflected `Req`/`Resp`
types:

- `paths` from patterns; parameters from `path`/`query`/`header` tags; request body from the
  `body:"json"` field; responses: success schema, and `default` → `Problem`.
- Schemas: the shared reflector of `core/internal/jsonschemax` (§1.1) with the overrides each
  package contributes — `time.Time` comes from `jsonschemax.Base()` (03 R4, SIMP3-1), and httpapi's
  own entries are `rbac.Permission`, `json.RawMessage` → `{}` and `Page[T]`/`CursorPage[T]` → generic
  expansion `Page_<T>` (COR4-21); component
  names `<module>.<GoType>`; two Go types mapping to one name → `ErrSchema`; a non-snake_case
  property → `ErrJSONName`.
- Extensions: `x-mediacore-permission` (scope or `public`/`authenticated`) and `x-mediacore-guards`.
- Security schemes: `sessionCookie` (apiKey in cookie) and `bearer` (session or API key);
  `apiKeyHeader` (`X-Api-Key`) is added with the facade (deferred-build, §3.6).
- Served at `GET /api/core/v1/openapi/{owner}.json` (perm `rbac.AnyAuthenticated`;
  `core.http.openapi.public=true` makes it Public) and emitted at build time by the app binary's
  `openapi` subcommand (06-app-sdk) into `api/openapi/<owner>.json`, committed — **except `core`**:
  apps never emit the core document; `core.json` is emitted only by `core/examples/hello`'s `openapi`
  subcommand, from R1, into `core/openapi/core.json` (outside any npm package, which R1's front-end work, 07 §7.19 steps 1–2,
  creates later), and that work's api-client generator reads it from there (07 §7.8, 06 `Description.Core`;
  FE2-24, MOD3-4). A freshness gate
  (`make openapi-check`, modelled on `scripts/ci/check_generated_types.sh:1-22`) and the `oasdiff`
  breaking gate (§4.6) run in `make ci`.
- Legacy gin routes get **no** OpenAPI document (SIMP-27): they have no Go types to reflect, and
  their method, path and permission are already in the reviewed legacy manifest that
  `archtest.RoutesHavePermissions` checks.

**TypeScript.** Checked with `npm view` on 2026-11-04: `openapi-typescript` **7.13.0**,
`openapi-fetch` **0.17.0** (alternatives seen: `@hey-api/openapi-ts` 0.99.0, `orval` 8.46.0).
Chosen: `openapi-typescript` (types only, OpenAPI 3.1 support, zero runtime) +
`openapi-fetch` (typed `fetch` wrapper with middleware), because the app already uses native
`fetch` through `apiFetch` (`web/src/utils/apiFetch.ts:15-70`, `raw/13` §1, §8) and has no
react-query/axios runtime to integrate with. `openapi-fetch` is pre-1.0: pin exact versions (npm
exact `"0.17.0"`, no range), and the wrapper below isolates apps from its API so a swap is one
package. `openapi-typescript` under the repo's TypeScript pin is settled in 07 (O4 closed); there is
no in-house Go TypeScript emitter in this design.

- `@mediacore/api-client` = generated `paths` for core + `createCoreClient({ buildId, shell })` built on
  `openapi-fetch` with middleware ported from `apiFetch`: `credentials: 'include'`, timeout
  (`ApiTimeoutError`), SSO bounce detection (`ApiAuthRedirectError`), `X-Request-ID` surfacing,
  the stale-build rule of 07 §7.5.2. From release R1 (07 §7.19 steps 1–2) the runtime exports `checkBuild(response, opts):
  StaleReason | null`, `checkModules(body, response, opts): StaleReason | null`,
  `onStaleBuild(listener): Unsubscribe` and `getStaleReasons(): readonly StaleReason[]`, and ABO's
  `apiFetch` calls `checkBuild`, so legacy and native calls run one implementation (FE4-9, FE7-5).
  **The check is split by what it reads (FE8-2, option (b)).** `checkBuild` is synchronous and reads
  only headers, so it decides the header cases 1 and 3 and never consumes a body. Case 2 needs
  `app.ui_build_id` from the `/api/core/v1/modules` JSON body, so the shell's `/modules` fetch calls
  `checkModules` after it has parsed the body it reads anyway; `checkBuild` does not special-case
  `/modules`. Both notify `onStaleBuild`. No response header is added for case 2.
  **Both inputs are runtime values the caller passes, never defines baked into the library (FE6-1,
  FE6-8):** `@mediacore/api-client` is built to `dist/` by Vite library mode before, and separately
  from, the app, so an `import.meta.env.MEDIACORE_BUILD_ID` read inside it would carry the library
  build's value (or `undefined`), not the app's, and every Docker image would offer a reload on every
  response. `buildId` is read once at page load by the runtime's one meta reader,
  `readPageBuildId(): string` (`""` when absent), from the served document's
  `<meta name="mediacore-build">` — the value stage 5's `X-App-Build` comes from (§4.2) — and `shell`
  is the caller's own flag. **Both options are required (FE7-5)**, so an omitted `buildId` cannot
  disable the check silently: `createCoreClient(opts: { buildId: string; shell: boolean })` stores both
  for the runtime's middleware; ABO's `apiFetch` calls `checkBuild(response, { buildId:
  readPageBuildId(), shell: false })`; ABO's pre-shell client for generated operations (from the identity flip F4 until the shell flip F9) is one
  `createCoreClient({ buildId: readPageBuildId(), shell: false })` in `web/src/api/core.ts`, created in release R1;
  `createShell` passes `{ buildId: readPageBuildId(), shell: true }`; and module clients come from
  `createModuleClient<Paths>(coreClient)`, which inherits both values. `checkBuild` and `checkModules`
  return the stale reason or `null`. **Detections replay on subscribe (FE8-3).** The runtime keeps the
  reasons already detected on this page load (`getStaleReasons()`), and `onStaleBuild` calls a new
  listener at once for each of them; "once" is per listener per reason. A banner that mounts after the
  page's first stale response (the auth probe, `/auth/status`, `/me`) is still notified. So ABO's
  reload banner (from release R1) and the shell banner (after the shell flip F9) subscribe to one signal that native and legacy
  calls both raise. 07 §7.5.2 owns the banners and 09 assigns ABO's banner component (R1), which, until R1's
  api-client work lands, is fed by `apiFetch`'s own rule. `buildId.test.ts` asserts that a stale response on a native call
  and one on a legacy call notify the same listener, that a listener subscribed after a stale response
  is notified once, and that the case-2 path detects `legacy-spa` without consuming the body its caller
  reads. No `core/web` package reads `import.meta.env.MEDIACORE_*` except
  `MEDIACORE_CORE_VERSION`, the shell package's own version (07 §7.10). An e2e cell runs a production
  `dist/` build (not vitest) against a server whose `X-App-Build` equals the page's meta (no reload
  offered) and one where it differs (reload offered), in the PR image job (07 owns the test). The
  client also types `Problem` as the error type of every call and sets `skipAuthRedirect` from
  `x-mediacore-permission: public` (§4.3, FE-7).
- Each app generates its own client the same way for its native routes, as a directory, not a
  package (FE-28, 07 §7.8): ABO's is `web/src/modules/books/api/`, generated from
  `api/openapi/books.json` and constructed with `createModuleClient<Paths>(coreClient)`; generation is `make generate-api-client`, freshness-gated like
  `types-check`.
- tygo (`tygo.yaml`) stays for the types of routes that remain on the legacy mount, which may be
  indefinitely; a type moves to OpenAPI when its route goes native. `opsgen` (`tools/cmd/opsgen` →
  `web/src/generated/ops.ts`, `tools/cmd/opsgen/main.go:6,28` @748fd787a) keeps its HEAD input
  `internal/operations/state` and its freshness gate unchanged until R3.
  `ops.ts` is imported by `web/src/services/api.ts` and `web/src/utils/operationPolling.ts`
  (`operationPolling.ts` has 5 non-test importers @748fd787a). The tasks flip F5 moves the Activity
  page to the generated task client, so from F5 the page reads no `ops.ts`; the other importers keep
  it, and R3 deletes `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state`
  together, with those importers switched to the generated task client in the same release (03 §3.8 R3,
  07 §7.8, 05-tasks; XFILE-2, FE4-7, FE8-6). The golden contract test
  (`internal/wirecontract/contract_test.go`) is generalized for native routes:
  `coretest.ContractGoldens(t, router)` marshals each route's declared examples and **validates them
  against the generated schema** (jsonschema-go `Resolve`+`Validate`), and the TS side assigns the
  same goldens to generated types, as `web/src/types/contract/contract.test.ts` does now.

## 8. Migration from ABO (the permanent legacy mount)

### 8.1 Stages

Cross-cutting resolution 2: ABO's gin router is mounted permanently, behind core's perimeter, with
gin's own auth chain unchanged inside it; the auth engine behind that chain swaps once, in the R2
identity flip F4 (OPS-11). There is no gin-retirement stage and no alias stage: each R2 flip deletes the
gin routes its native routes replace, and the SPA, the first-party callers and the deploy files switch in
the same PR (§4.6, 09). The flip labels are 03 §3.8's: F2 the foundation PRs (landed dark), F3 config,
F4 identity, F5 tasks, F6 events, F7 integrations, F8 backup, the updater move, health and the kernel
`/metrics` mount, F9 the shell. No metric rename waits for F8: they land in R1 (HTTP), F5 (the ops
families) and F7 (AI), each with its dashboards and rules (05-metrics §13; XFILE-1).

| Release (flip) | What changes in HTTP | Who authenticates / authorizes the legacy mount | Rollback |
|---|---|---|---|
| R1 | `core/httpapi` exists; ABO's `main` builds `app.New(ctx, opts, books.Module())` (06 §6.11.1) with `app.Options.LegacyAuthGinUntil: "books"`; `books` calls `MountLegacy(Auth: LegacyAuthGin)` with the whole gin engine; **every manifest route is a mux pattern**, its own or, for a conflicting same-owner set, one generalised pattern (§4.8 collision rule, resolution A-1; `TestLegacyManifest_RegistersAsMuxPatterns` runs on the real engine, ABS on and off). The perimeter moves to core stages 0–11 and is removed from gin in the same PR (logger, recovery, security headers with `DefaultCSP` = today's per-response `contentSecurityPolicy()` and `Reporting-Endpoints` (FE2-1), CORS, gzip with HEAD's per-path exclusions, tracing and HTTP metrics (stage 2 records every request; `otelgin` leaves the engine, 13 §13.5.1), the `/api` rate limiter with per-route `RateClass`), **except the Basic gate (stage 8), which stays in gin until the config flip F3**, because core reads its keys only through core config; stage 5 emits `X-App-Build` on every response (FE2-5, OPS2-13). **Perimeter values are plain options in R1:** ABO's `main` reads its effective config at boot (UI-saved blob values included) and passes the listener and perimeter values (listen address and TLS files, `core.http.rate_limit.*` = `enable_rate_limit`/`api_rate_limit_per_minute`, the body limits, peer allow-list, trusted proxies; HEAD has no configurable CORS origins) through `app.Options.R1Values`, which the kernel calls once at 06 §6.5.2 start step 7, before the first listener opens (05-config §1.12.1), so a value saved only in the UI reaches stages 0, 10 and 11; an edit through `PUT /api/v1/config` takes effect at the next restart until F3 makes the keys hot-reloadable core keys, an R1 release-note line (`TestPerimeterHonoursBlobOnlyValues`). **The factory reset is offline only in R1** (`storage reset`, 05-storage §5S.3.15): HEAD's two gin reset routes are deleted, the SPA hides its reset control, and the core route arrives at F4 (§5), an R1 release-note line. Cross-origin protection (stage 7) is new, and its refusals go to the access log as `security_event` until `audit` exists (SEC2-20); the 36 permission-less routes are annotated `AnyAuthenticated` in the manifest (R6 must hold from R1), the playlist write routes among them, on which `books:playlists.create` is never enforced, so no principal loses its own-playlist writes (05-rbac §6.1, SEC-4), and **every `s.perm(atom)` route's `Perm` records the native requirement books' atom table gives the atom** (§8.3, 05-rbac §6; COR6-1): the reviewed manifest is final from R1, stage 13 starts enforcing it at F4, and `Build` checks its scopes against the catalog from then (`TestManifest_SPermRoutesCarryAtomRequirement`). **Client address (SEC2-19):** stage 0 resolves it and publishes it (`ClientAddrFrom`); ABO's `client_trust.go` stays in ABO until F4, and its `trustedClientIP` (used by the bootstrap, invite and ABS limiters, `bootstrap_rate_limit.go:207`, `invite_rate_limit.go:405`, `client_trust.go:600`) starts from that value and keeps only its "local peer + verified Access assertion" branch on top | gin only (CF binder, `RequireAuth`, `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner` unchanged); core stages 12–13 off for the mount | binary swap to R0; no user data changed |
| R2, F2 (foundation, dark) | no gin route is moved or deleted, and the SPA switches nothing. F2 lands the foundation packages dark (03 §3.8): among them the full rbac scope catalog, so every `core:*` scope a later flip's routes name (`core:metrics.read` on the legacy `/metrics` entry included) is in the catalog `Build` checks against before F4 starts enforcing it (IMPL-4), and the health registry with its route (05-health owns the path), so tokens raised from F3 surface before F8 (IMPL-12) | gin only, as R1 | R2 rollback (below) |
| R2, F3 (config) | The Basic gate (stage 8) moves from gin into the core perimeter with HEAD's exempt set (`OwnAuth`, `BasicExempt`, `BasicExemptAssets`; SEC-14, COR4-9) and refuses an enabled gate with no password (§4.2 stage 8, R5-1(c)); stage 8 reads `core.http.basic_gate.*` through core config (§10), whose migration M2 has moved HEAD's values, the password re-encrypted into core secrets (05-config §1.12.2), and `core.http.basic_gate.enabled` is `FailClosed` (§10); `basicauth.go` is deleted from gin in the same PR (04 §7), and the deploy files pass the canonical Basic-gate names only when set (05-config §1.12.4). The perimeter keys become hot-reloadable core keys | gin only, as R1 | R2 rollback (below) |
| R2, F4 (identity) | `LegacyAuth` switches to `LegacyAuthCore`, and the same PR removes `LegacyAuthGinUntil` from `app.Options` (an archtest fails if it is still present, SEC2-17): stage 12 binds the core principal and stage 13 enforces each manifest route's `Perm`/`Guards`, for an `s.perm(atom)` route the atom-table requirement recorded since R1, over the native scopes the identity migration stored on every role and key (05-authn §4.4, 05-rbac §6); the legacy `/metrics` entry carries `core:metrics.read`, which that migration stores on every principal that is not `Confined()` (05-rbac §6); inside gin the auth middleware becomes the bridge of §8.3 (it never authenticates; its authorization adapters still enforce through `rbac.Require`); core auth/users/roles/keys/sessions routes and the settings routes (`/api/core/v1/settings`, `/settings/schema`, 05-config §1.3.4; FE3-11) go native under `/api/core/v1`, and the gin identity routes they replace are deleted in the same PR, `PUT /api/v1/auth/me/password` with `user_id` included (SEC2-5), as are the gin settings handlers for every `k` and `s` key; `GET`/`PUT /api/v1/config` keeps serving the class-`i` fields until F7, whose instance routes replace them (05-config §1.12.1); `GET /auth/temp-login` and the `/invite/<token>` link form are replaced by 05-authn's link forms, and the one-time tokens minted before the flip are reset (D12 (b)), so no redirect route exists; HEAD's OAuth/OIDC callback path is served by core on `CoreFixedGroup` and its gin route is deleted (§3.5), **with HEAD's native-app hand-off kept (SEC-2):** core's start handler, at `GET /api/core/v1/auth/oidc/{provider}/start` and at HEAD's start path (below), takes HEAD's deep-link parameters (`redirect_uri`, or its alias `callback`, with `code_challenge`; only both present select the app branch, so a stray one is ignored and web login is never broken), answers an unregistered `redirect_uri` with an inline 400 against books' exact-match allowlist, never a redirect to it, refuses any `code_challenge_method` but `S256`, and carries the fields in the signed state; on that branch the callback has books' hook mint the app authorization code (HEAD's `abs.MintAppAuthorizationCode`, through the module callback 05-authn §4.7 defines), redirects to the app's scheme and issues no web session, and the code is redeemed at the fixed ABS route `/auth/openid/callback` (§4.6). HEAD's start path `/api/v1/auth/oauth/{provider}/start` is kept at its exact pattern, the second `CoreExactPatterns()` entry (§3.5, §4.9), served by the same core start handler with the same rules, because ABS clients such as AudioBooth start there and D12 (c) freezes the ABS API (SAFETY-11); books' gin start handler is deleted in the same PR, and no release-note line is needed because nothing changes for its callers. 05-authn §10 owns the test of each control (`TestOIDC_AppHandoffMintsABSCode` among them); the core factory-reset route lands in `internal/storagehttp` and the SPA's reset control returns on it (§5); the SPA's identity calls, the `server-bootstrap` skill and `scripts/` switch in the same PR; `client_trust.go`'s Access-assertion branch moves into ABO's `internal/cfaccess` authenticator; stage 7 and stage 13 refusals are audited from here | core; gin bridge | R2 rollback (below) |
| R2, F5 (tasks) | the native task routes (05-tasks §5T.3.10); the gin routes they replace are deleted in the same PR: the `/api/v1/operations*` task routes, `/api/v3/ops/*` with books' `/api/v3/` claim (§4.9) and the two operation streams (`/api/v1/operations/events`, `/api/v3/ops/events`); the Activity page moves to the native routes, stops reading `ops.ts` (§7) and polls `GET /api/core/v1/tasks/runs` until F6; HEAD's `operations_*`/`op_*` metric families go with HEAD's engine, with their dashboards, rules and rule tests (05-metrics §13); §4.8's books component re-folds | core | R2 rollback (below) |
| R2, F6 (events) | `/api/core/v1/events` (05-realtime); `/api/events` is deleted as the SPA's realtime client, the Activity page's included, moves to it: the PR rewrites ABO's `web/src/services/eventSourceManager.ts` onto `/api/core/v1/events`, and `@mediacore/shell`'s client replaces it at F9 or in R3 (07 §7.9.2; IMPL-6) | core | R2 rollback (below) |
| R2, F7 (integrations) | the unauthenticated `/api/v1/plugins` mount (G8) is deleted with `internal/plugin` (§8.4) | core | R2 rollback (below) |
| R2, F8 (backup, the updater move, health, the kernel `/metrics` mount) | the kernel's `Infra` mount replaces the legacy `/metrics` route (05-metrics §6), with no metric rename; `/api/health` and `/api/v1/health` are deleted (05-health §5H.3.3) and `BasicExempt` moves to `/health`, whose unauthenticated answer carries only 05-health §5H.3.3's listed token set (§4.2 stage 8, SEC-7); the Dockerfile `HEALTHCHECK`, `scripts/deploy-wait-ready.sh` and the other callers switch in the same PR | core | R2 rollback (below) |
| F9 (shell; R2 if ready, else R3) | the mount drops `ServesSPA`, gin's `NoRoute`/`setupStaticFiles` are deleted and the kernel mounts the shell with `Router.SPA` (§4.10) | core | per release |
| any later | a books route group moves to `/api/books/v1` **only when rewritten for its own reasons** (SIMP-2); its gin routes are deleted in the same PR | core | per release |

**R2 rollback** is the release's one-way rule (09): restore the pre-migration checkpoint or the off-host
archive offline, and reinstall the pre-freeze release, the last production release before F3 merged
(09 §9.2.1; CONFORM-1). Nothing in this file keeps a format an older build reads.

The R1 done-check in 09 covers the perimeter move: CSP header goldens per owner (the legacy SPA
response carries the inline-script hash and the configured Access team host, and
`Reporting-Endpoints`), `X-App-Build` on every response class in the perimeter goldens, the
rate-limit class golden, the cross-origin test, the bootstrap, invite and ABS limiter tests unchanged
(SEC2-19), the ABS conformance suite and `TestStage2_RecordsEveryRequest`. F3 covers the
Basic gate's move: its exempt set is the reviewed golden, HEAD's set until F8
(`TestBasicGate_ExemptSetGolden`), the gate enabled only in the stored settings turns stage 8 on also with
an operator `ABO_CORE_HTTP_BASIC_GATE_ENABLED=false`, and the shipped compose file, which passes the gate's
variables only when set, keeps a UI-enabled gate on (`TestBasicGate_MoreRestrictiveWins`, its compose cell;
XFILE-8, CONFORM-7), and an enabled gate with no password refuses (`TestBasicGate_EmptyPasswordRefuses`). F4 carries the reach oracle
`TestIdentityCutover_ReachOracle` (05-rbac §12 owns it; every manifest route, 572 at `748fd787a`, a count
the test generates at the flip's base commit, ADV-11, × every principal kind 05-rbac §12 lists, plus a
custom-role half over random native roles, SEC4-2): allow and deny on the migrated fixture corpus (written
by the R0 binary, plus its labelled hand-built overlay for the rows R0's API cannot write, custom roles
among them, 09 §9.3) equal the R0 binary's, R0 being HEAD plus the six early fixes (SEC-13), apart from
the reviewed differences 05-rbac §12 lists and asserts, an every-atom
key whose owner's roles hold `core:admin`, or an admin key, keeping its whole reach (an every-atom key of
a non-admin owner keeps only the intersection, SEC-12), task-run approval included (05-rbac §6.3), and the test
stays as the regression test of books' atom table.

### 8.2 `MountLegacy` contract

```go
type LegacyMount struct {
	Owner      string                // "books"
	Handler    http.Handler          // *gin.Engine
	Routes     iter.Seq[LegacyRoute] // manifest: every gin route; each becomes its own mux pattern or a member of one generalised pattern (§4.8 collision rule)
	Auth       LegacyAuth            // LegacyAuthGin only while app.Options.LegacyAuthGinUntil names Owner (R1) | LegacyAuthCore
	ServesSPA  bool                  // owns "/" and serves the SPA through gin NoRoute (§4.10)
	DefaultCSP func(*http.Request) string // stage 5 policy for this mount's responses, evaluated per response (ABO: today's contentSecurityPolicy(), FE2-1)
	CSPReport  string                // Reporting-Endpoints URL for DefaultCSP (ABO: "/api/v1/csp-report")
	Redirects  []LegacyRedirect      // the /api/* -> /api/v1/* rule and its exclusions (§4.9 item 3)
	TopSegments []string             // top-level server paths of the mount (ABO: ping, status, logout, socket.io, public): reserved IDs + NonSPA (03 §3.6)
	SPAShared  []string              // top-level segments shared with SPA pages, excluded from NonSPA (ABO: "login"; §3.5, SEC3-16)
	BuildID    string                // stage 5's X-App-Build until the shell flip: the <meta name="mediacore-build"> of the mount's embedded index.html, the only UI-build-ID source; never the binary's identity (FE3-2, FE4-1, R5-7)
	BasicExemptAssets func(path string) bool // stage 8 exempts these paths while the mount owns "/": HEAD's static-asset prefix/suffix rule (basicauth.go:40-46, COR4-9)
}

type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement // s.perm atom: the native requirement books' atom table gives it (§8.3, 05-rbac §6), recorded from R1 (COR6-1), enforced from F4. Admin kind: 05-rbac §6.2; Delegated when OwnAuth
	Guards          Guards           // from credRoute / RequireOwner recording
	Public          bool             // on the reviewed list of 15 (raw/09 §2.4)
	OwnAuth         bool             // the route's gin chain runs the ABS authenticator (ABSRequireAuth); stages 8 (Basic), 12, 13 skip it (SEC-14)
	BasicExempt     bool             // stage 8 skips it: HEAD's two health paths /api/health and /api/v1/health (basicauth.go:34, COR4-9), and /health from F8, which deletes those two; TestBasicGate_ExemptSetGolden
	AuthMethod      string           // required iff OwnAuth: the auth method ("books.abs") the owner registered (SEC2-17)
	RateClass       RateClass        // RateDefault iff the gin chain includes the API rate limiter today, else RateExempt
	NoCompress      bool             // stage 9 skips it: HEAD's compressionMiddleware exclusions (§4.2 stage 9, COR3-6); TestCompressionExclusionsEqualHEAD
}

```

**Admission (SEC2-17).** `MountLegacy` is a permanent public API, so its two ways of skipping core
stages are admitted by the kernel, not self-declared. `Auth: LegacyAuthGin` (stages 12–13 off for the
whole mount) is accepted only while the compile-time `app.Options.LegacyAuthGinUntil` names the
mount's owner (ABO sets `"books"` in R1 and removes it at F4; an archtest fails if the
field is set after it), else `ErrLegacyAuthGin`. `OwnAuth: true` (skips stages 8, 12, 13) is accepted
only when `AuthMethod` names an auth method the same module registered with `Registrar.AuthMethod`
(05-authn §3.3), else `ErrOwnAuthMethod`. `Registrar.AuthMethod` is an R1 member that only records the
`auth.MethodSpec` leaf value (06 §6.4.10), so admission holds from R1, when ABO's ABS routes are first
`OwnAuth` for stage 8's Basic exemption, and the method is validated by `auth` once it exists in R2
(SEC3-14, COR3-3);
`rbac.Delegated` is accepted only on such routes. The set of `OwnAuth` routes is a reviewed golden
(`TestOwnAuthRoutesGolden`).

- The manifest is produced inside ABO by extending the existing recording pattern
  (`recordCredRoute`, `internal/server/credential_routes.go:47-58`; `recordAdminRoute`,
  `internal/server/wire_admin_role.go:63-70`) to `s.perm`, the ABS auth middleware and the rate
  limiter, and by walking `(*gin.Engine).Routes()`. `Build` fails with `ErrLegacyManifest` when the
  walk and the manifest differ (a gin route without an entry, or an entry without a gin route).
- **Each manifest route is registered as its own `ServeMux` pattern** (gin path → mux pattern, §4.8),
  or, when it belongs to a conflicting same-owner set, as a member of that set's one generalised pattern
  (§4.8 collision rule, resolution A-1), with the gin engine as the handler (SEC-13), behind a wrapper
  that refuses (404, legacy shape) a request whose gin match `c.FullPath()` is not that pattern's manifest
  entry or one of its members, so the route stages 8, 10 and 13 judged is the route gin runs (SEC2-22,
  §4.8). The wrapper puts the matched pattern in the request context, where the post-match check reads
  it as `httpapi.MatchedPattern(ctx)` (§3.5), and writes it into stage 2's route carrier like every
  route wrapper (§4.2 stage 2). From F4, stage 13 enforces its `Perm` and `Guards`,
  so the manifest is the running chain, not documentation, and `archtest.RoutesHavePermissions`
  checks what is served. A request matching no manifest route never reaches gin's `NoRoute`, except
  through the `ServesSPA` `/` fallback; under a legacy prefix it gets 404 in the legacy error shape.
- `archtest.RoutesHavePermissions` (R6) accepts a legacy route only if it is in the manifest with a
  permission (or `Public` on the reviewed list of 15, `raw/09` §2.4, or admitted `OwnAuth` with
  `Delegated`),
  so the 36 permission-less authenticated routes (`raw/09` §4) each get an explicit requirement,
  `AnyAuthenticated` — written down and reviewed instead of implicit. The playlist write routes are
  among them and stay `AnyAuthenticated`: `books:playlists.create` is not enforced on them, so no HEAD
  principal loses its own-playlist writes and the reach oracle reports no loss (05-rbac §6.1, SEC-4).

### 8.3 Bridge contract (from the identity flip F4)

Inside gin, the auth chain keeps its positions; each element becomes an adapter over the core
decision already taken by stages 12–13. The bridge never **authenticates**, but it is not read-only
for authorization: its adapters still enforce, through `rbac.Require`, which audits (SEC-17), so a
gin route that ever disagreed with its mux pattern would still be checked (SEC2-22).

**Books' atom table.** HEAD's gin routes keep calling `s.perm(atom)`. One books-owned table, code and
not stored data, maps each HEAD atom to an `rbac.Requirement` over native scopes: `AllOf` the native
scope set equivalent to the atom's **full** HEAD reach, never the atom's expansion, so a custom role
holding part of `settings.manage`'s reach passes no `settings.manage` route (resolution R4-3, SEC4-2,
SEC4-4), plus a read-side entry for the in-handler sites HEAD judged by their read-side successor
(`core:users.view` for `users.manage`). 05-rbac §6.1 owns the table's rows, and its "Route requirement"
column is this entry: `AllOf` **all** of the row's native scopes, always (a single-scope row's
requirement is trivially its one scope), as 05-rbac §6.1's column states it (XFILE-5); the identity migration stores native scopes on
roles and keys by the same table. The manifest `Perm` of an `s.perm` route is
that table's entry, so stage 13, the adapter and the migrated grants all read one mapping.

| Legacy behaviour | Anchor | From F4 |
|---|---|---|
| `RequireAuth` (store nil → pass; session/key lookup) | `middleware/auth.go:152-262` | `legacyauth.FromCorePrincipal`: reads the `auth.Principal` bound by stage 12 and fills the legacy context values (`auth.WithUser`, `WithPermissions` with the atoms whose table requirement the principal's checker satisfies — none for the auth-disabled principal, as HEAD binds none with auth off, SEC5-9 — `WithMethod`); never authenticates; a missing principal on a non-Public route is a defensive 401 (stage 13 already answered) |
| zero users → pass everything | `middleware/auth.go:165-169` | gone: stage 13 allows only Public + setup/bootstrap (05-authn §4.2) |
| worker-only path confinement | `middleware/auth.go:265-298`, ABS half `middleware/absauth.go:629-632` | `Confining` scopes at stage 13 (05-rbac §4.10) for every route stage 13 runs on; for `OwnAuth` (ABS) routes, which stage 13 skips, the ABS adapter keeps the check (next rows) — `abortWorkerOnlyOutsideWorkerAPI` is **replaced**, not deleted (SEC2-4) |
| Cloudflare Access fail-open binder | `middleware/cfaccess.go:20-139` | ABO's `internal/cfaccess` authenticator (04 §2.4), registered app-level through `app.Options.Authenticators` (05-authn §3.3, SEC2-16), runs in stage 12; gin binder removed |
| `s.perm(atom)` | `server_lifecycle.go:1693-1698` | adapter over `rbac.Require(ctx, table[atom])`, the same requirement as the route's manifest `Perm`, so stage 13 and the adapter agree (COR5-4). Roles and keys hold native scopes after the identity migration, so a key is judged by core's ordinary role ∩ key rule (05-authn §4.4); the auth-disabled principal by core's auth-disabled checker, under which every `s.perm` route passes, as HEAD's `s.perm` did with auth off (05-rbac §4.11) |
| in-handler `auth.Can(ctx, atom)` | `internal/auth/context.go:171-178`; every raw `auth.Can` site in the mount, found by this row's ABO archtest (05-rbac §4.11 and §6.1 give the auth-off and read-side rules; XFILE-12) | each site is replaced by `legacyauth.Can(ctx, atom)`, which checks the table's entry for the atom (the read-side entry for a `read_side` site; an ABO archtest finds no raw `auth.Can` left in the mount), except that the auth-disabled principal is denied, as HEAD's `auth.Can` returns false with no permission set bound (sites with their own auth-off bypass keep it; 05-rbac §6.1; SEC5-9, SEC5-14) |
| `RequireAdmin` by role name; `adminRole` pass-through vs 401 with auth off | `middleware/auth.go:522-537`, `wire_admin_role.go:18-45` | the manifest carries 05-rbac §6.2's permission per admin kind; the gin gate becomes `rbac.Require(that permission)`; auth-off reach follows from the auth-disabled checker |
| `credGuard` | `credential_routes.go:79-87` (a pass-through with auth off) | `Guards.Interactive` at stage 13; gin adapter checks `Principal.Method.MayChangeCredentials()`. The auth-disabled principal is refused, an auth-off tightening with an R2 release-note line (05-authn §4.11, 05-rbac §4.11, SEC5-8) |
| `RequireOwner` | `owner_guard.go:51-89` | `Guards.Owner` at stage 13 |
| user/credential handlers acting on other users | `auth_reset_password.go:18-60` | call `auth.Service` methods that enforce dominance (05-rbac §4.7 rule 5) |
| ABS `ABSRequireAuth` | `middleware/absauth.go:621` | inside gin for `OwnAuth` routes; resolves through books' method-bound `auth.Compat` handle (05-authn §3.1, 06 §6.4.4; SEC-1), never `*auth.Service`: `VerifyPassword` for an ABS password login, `AuthenticateKey` for an `abk_` key (stage 12's status, expiry and purpose rules), `PrincipalFor` for an ABS token or a Cloudflare Access login, each refusing a sign-in-restricted or password-reset-required user and a `needs_confirmation` key, with the password and principal calls going through core's throttle (05-authn §3.1, §4.10). **Then** the adapter calls `Compat.CheckCredentialFor(p, route.Perm)`, which refuses (ABS JSON 403) a principal with `Perms.Confined()` (an `fp-worker` key or a worker user's ABS login) unless the route's manifest `Perm` names one of its scopes, so `TestIdentityCutover_ReachOracle`'s `fp-worker` cells pass on the ABS surface (SEC2-4). ABS's JWT secret value and its auth semantics are unchanged (D12 (c)) |

### 8.4 File mapping

| ABO today | Becomes |
|---|---|
| `internal/server/server.go:638-731` engine + global middleware | `core/internal/httpmw` chain stages 1–11; perimeter middleware and `otelgin` removed from gin in R1; the gin engine itself stays |
| `internal/server/server_lifecycle.go:1693-1698` `s.perm` | manifest `Perm` + `rbac.Require` adapter over books' atom table (§8.3) |
| `internal/server/server_lifecycle.go:1710-2135` `setupRoutes` | `books` module `Register` + `MountLegacy` (permanent) |
| `internal/server/peer_allowlist.go` | `core/internal/httpmw/peer.go` (stage 0) + `TrustedPeer` (§3.5), in R1 |
| `internal/server/client_trust.go` | stays in ABO until F4, reading `httpapi.ClientAddrFrom` from R1; at the flip its Access-assertion branch moves into ABO's `internal/cfaccess` and the rest is deleted (SEC2-19) |
| `internal/server/server_middleware.go:56-118` CORS + headers | stages 5–6 |
| `internal/server/csp.go` (`contentSecurityPolicy()`, team-host pointer) | stays in ABO until the shell flip F9 as `LegacyMount.DefaultCSP` (it hashes ABO's embedded `index.html` and reads the deployment's Access host, `csp.go:34-46,95-106,129-138`); at the shell flip the policy builder (`cspBase`) is `core/ui`'s and the Access host becomes ABO's `ConnectSrcFrom` (FE3-4, FE3-10) |
| `internal/server/csp_report.go` | `core/internal/uihttp` (`/api/core/v1/ui/csp-report`) at F4; gin's `/api/v1/csp-report` stays the legacy mount's `CSPReport` until the shell flip, then its handler is deleted |
| `internal/server/static_embed.go` | `core/ui` at the shell flip F9 |
| `internal/server/middleware/ratelimit.go`, `request_size.go` | stages 10–11 |
| `internal/server/middleware/basicauth.go` | stage 8 with per-route exemption, at F3 (it stays in gin in R1) |
| `internal/server/gone.go` | stays in gin for HEAD's existing stubs; core has no `Gone` (§4.6) |
| `internal/httputil/respond.go`, `types.go`, `parse.go` | `httpapi.Problem`, `Page[T]` for native routes; the legacy shape stays for gin and the router's legacy-prefix 404 |
| `internal/httputil/rangeserve.go` | stays app-side helper used via `HandleRaw` (already `net/http`) |
| `internal/plugin/router.go`, `internal/server/plugins_init.go:42` | deleted at F7 with `internal/plugin` (N4; 04 §8.3); replaced by the module `Group` |
| `tygo.yaml`, `tools/cmd/opsgen`, `internal/wirecontract` | kept for legacy-mount types; `opsgen` keeps its HEAD input until R3, which deletes `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state` together (§7); native routes use OpenAPI + `coretest.ContractGoldens` (§7) |
| `web/src/services/api.ts` (8,747 lines), `web/src/utils/apiFetch.ts` | calls to native routes use `@mediacore/api-client` / the generated `web/src/modules/books/api/` directory; calls to legacy routes keep `apiFetch`, which calls the runtime's `checkBuild(response, { buildId: readPageBuildId(), shell: false })` from release R1 (§7, FE4-9, FE6-1, FE7-5) |

## 9. Concurrency contract

- `Router` registration methods are called from the kernel's single registration goroutine; they
  are not goroutine-safe and panic-free (errors recorded). After `Build`, the returned handler and
  all `RouteInfo` are immutable and safe for concurrent use.
- Each request runs on its `net/http` goroutine; the chain starts **no** goroutines per request
  (today's per-request `go func()` for key touch was removed for the same reason,
  `internal/server/middleware/auth.go:366-382`).
- Rate-limiter buckets: sharded maps (`runtime.GOMAXPROCS(0)` shards) of `*rate.Limiter` with an LRU
  cap (`core.http.rate_limit.max_clients`, default 10,000) and one sweeper goroutine per `Router`,
  stopped by the app context (pattern of `IPRateLimiter.Start`, `ratelimit.go:196-230`).
- Hot-reloaded perimeter settings (peer allowlist, trusted proxies, CORS/trusted origins, Basic gate)
  are compiled into immutable values and swapped through an `atomic.Pointer` on config change; the
  `CrossOriginProtection` value is rebuilt, not mutated, so readers never see a half-updated set.
- Handler contexts are cancelled on client disconnect and on shutdown; handlers must pass `ctx`
  down. Long work belongs in `tasks` (start returns 202 with a run ID).

## 10. Config keys

`Protection` levels are defined by 05-config; `admin` = `ProtectAdmin` (requires `core:admin` and an
interactive method). Keys that decide who reaches the server or which client address is believed are
`admin` (SEC-6). Precedence is 05-config §1.3.5's: an operator value (file, env or flag) locks its key.
**`core.http.basic_gate.enabled` is `FailClosed`:** `httpapi`'s section declares
`config.FailClosed("core.http.basic_gate.enabled", json.RawMessage("true"), json.RawMessage("false"))`
(05-config §1.3.1), so enabled beats disabled across the operator and stored values, and a stored `true`
that beats an operator `false` is reported (`AttachReport.FailClosed`, one startup warning, health
`config_fail_closed_override`). It is this file's only `FailClosed` key: the username and the password are
locked by an operator value like any key (05-config refuses `FailClosed` on a string or secret key).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.http.listen` | string | `:8484` (ABO default port, `server_lifecycle.go:2187`) | no | admin |
| `core.http.peer_allowlist` | []CIDR | `[]` (warn: accept any peer) | yes | admin |
| `core.http.trusted_proxies` | [](CIDR or address with optional IPv6 zone) | `[]` | yes | admin |
| `core.http.client_ip_header` | string | `""` (ABO: `CF-Connecting-IP`) | yes | admin |
| `core.http.cors.allowed_origins` | []string | `[]` (same-origin only); also stage 7's trusted origins | yes | admin |
| `core.http.body.json_max_bytes` | int | 1 MiB | yes | none |
| `core.http.body.upload_max_bytes` | int | 10 MiB | yes | none |
| `core.http.rate_limit.enabled` | bool | true | yes | admin |
| `core.http.rate_limit.per_minute` | int | 100 | yes | none |
| `core.http.rate_limit.burst` | int | `max(per_minute/5, 10)` | yes | none |
| `core.http.rate_limit.max_clients` | int | 10000 | no | none |
| `core.http.basic_gate.enabled` | bool, `FailClosed` (above): a stale operator `false` cannot switch off a gate enabled in Settings, and compose passes the variable only when it is set (05-config §1.12.4; R5-1(a), OPS5-1) | false | yes | admin |
| `core.http.basic_gate.username` | string. Stage 8 compares it in constant time (SEC6-9). An enabled gate with an empty username keeps HEAD's behaviour (HEAD's default is `""`, `internal/config/config.go:2806`, and its gate then admits `:<password>`): the password is the secret, and refusing an empty username would lock out every deployment that set only a password, with no security gain; an empty **password** is what stage 8 refuses | `""` | yes | admin |
| `core.http.basic_gate.password` | secret (one canonical name, SEC4-10); core reads it from the config flip F3, when the gate moves (in R1 gin reads it itself). The config migration M2 re-encrypts HEAD's encrypted row into core secrets (05-config §1.12.2). Enabled with this unset, empty or unreadable → stage 8 refuses (R5-1(c)). An operator value locks it like any key, so a password set in the environment is used however the stored row reads, which is also early fix 5's recovery from a lock-out (05-authn §11.1 item 5, OPS9-7) | — | yes | admin |
| `core.http.openapi.public` | bool | false | yes | admin |
| `core.http.read_header_timeout` / `idle_timeout` | duration | 15s / 120s | no | none |

ABO's HEAD names (`json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`,
`api_rate_limit_per_minute`, `basic_auth_enabled`, `basic_auth_username`, `basic_auth_password`,
`allowed_peer_cidrs`; `internal/config/config.go:1554-1559,1578,1719`) and the env name
`TUNNEL_CONNECTOR_ADDRS` (→ `core.http.trusted_proxies`, zone semantics kept, `client_trust.go:133-201`)
are rows of 05-config's generated rename table (§1.12.3), which gives each its canonical key and env
name. M2 (F3) moves stored values to these keys and imports the operator file's
values once; there are no aliases, and boot refuses while a retired name of an `admin` or secret key
is set, naming the canonical one. HEAD keeps `allowed_peer_cidrs` and
`tunnel_connector_addrs` out of the settings blob and its API cannot set them; core stores them like
any `admin` key, editable only by an interactive `core:admin` principal.

## 11. Metrics

| Name | Type | Labels |
|---|---|---|
| `http_server_request_duration_seconds` | histogram (semconv, stage 2) | `http_request_method`, `http_response_status_code`, `http_route` (pattern, absent for unrouted requests, §4.2 stage 2), `server_address`, `server_port`, plus the semconv keys `httpconv` sets |
| `http_server_request_body_size_bytes` | histogram (semconv, stage 2) | as above |
| `http_server_response_body_size_bytes` | histogram (semconv, stage 2) | as above |
| `mediacore_http_inflight_requests` | gauge | `owner` |
| `mediacore_http_panics_total` | counter | `owner`, `route` |
| `mediacore_http_rate_limited_total` | counter | `class` |
| `mediacore_http_cross_origin_rejected_total` | counter | `owner` |
| `mediacore_http_peer_refused_total` | counter | — |

The three semconv families are the request count, latency and size of every route, legacy routes
included, so core keeps no second request counter or latency histogram beside them; the `owner`
dimension of a request is its `http_route`'s prefix. `route` labels carry the registered pattern;
unmatched paths use `route="unmatched"`, so cardinality stays bounded by the route table.

The `mediacore_http_*` names are shown under core's default metric namespace `mediacore`. An app passes
its own namespace as an `app.Options` value (ABO: `audiobook_organizer`, 05-metrics §4); the semconv
names carry no namespace. 05-metrics §13 says which HEAD families change and how the deploy files
follow: this file's change, the `http_route` values of the three `http_server_*` families (gin templates
→ mux patterns, §4.2 stage 2), lands in R1 with the perimeter move and is a line of R1's release note;
the ops families change at F5 and the AI families at F7, and F8 renames nothing (XFILE-1, CONFORM-11).

## 12. Tests

| Test | Asserts |
|---|---|
| `TestHandle_RejectsZeroPermission` | `Route{Perm: zero}` → `ErrNoPermission` from `Build` (R6) |
| `TestHandle_RejectsForeignScope` | module `tv` registering `books:edit` → `ErrForeignScope` |
| `TestBuild_PatternConflictIsError` | conflicting `ServeMux` patterns → `ErrPatternConflict`, no panic |
| `TestBuild_PrefixCollision_FacadeVsFacade` (deferred-build, §3.6) | two facades at `/api/v3` in one binary → error; with url_bases `/tv` and `/movies` → ok |
| `TestBuild_RejectsCamelCaseJSON` | native type with a `createdAt` tag → `ErrJSONName` (FE-12) |
| `TestModuleGroup_IsAuthenticated` | every route reachable from a module `Group` returns 401 without a credential unless `Public` (G8 regression) |
| `TestChain_OrderGolden` | recorded stage order equals §4.2 table |
| `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused` | cookie session, `Sec-Fetch-Site: same-site` POST → 403 `cross_origin_rejected`; same-origin and header-less clients pass (SEC-10, FE-5); a cross-site POST carrying Basic credentials is refused too (FE2-23) |
| `TestCrossOrigin_TrustedProxyForwardedHost` | plain-HTTP POST with `Origin` = public host, `Host` rewritten by a `TrustedProxies` peer with `X-Forwarded-Host` → allowed; the same `X-Forwarded-Host` from an untrusted peer → refused (FE2-23) |
| `TestBasicGate_ExemptSetGolden` | the reviewed golden of stage 8's exempt set, HEAD's (`basicauth.go:34,40-46,69`) until F8: the `OwnAuth` ABS routes, `/api/health` and `/api/v1/health` (`BasicExempt`), and the static-asset prefix/suffix rule on the `/` owner (`BasicExemptAssets`); from F8 `/health` is `BasicExempt` in place of the two deleted paths, and its unauthenticated answer behind an enabled gate carries no token outside 05-health §5H.3.3's listed set, whatever posture token is raised (SEC-7); `/ping`, `/status`, `/login` stay gated (SEC-14, COR4-9) |
| `TestBasicGate_EmptyPasswordRefuses` | from F3, early fix 5 ported as a **property test** (05-authn §10, CONFORM-14): over random states of the stored `enabled` and password (unset, cleared, empty, set, or an unreadable row) and the operator `enabled` and password (absent, empty, set), with a random presented username and password, a non-exempt request is admitted only when the gate is off, or when the effective password is non-empty and the presented credentials match it; `<username>:` is always refused; the exempt set always passes; an enabled gate with no usable password raises health `basic_gate_no_password` and names the key in the startup log; an operator password admits whatever the stored row holds (R5-1(c), SEC5-4, DATA5-12, OPS9-7) |
| `TestBasicGate_MoreRestrictiveWins` | from F3 (`FailClosed`, §10): operator `false` with stored `true`, and operator `true` with stored `false`, both turn stage 8 on; the first is reported in `AttachReport.FailClosed`, the startup log and health `config_fail_closed_override`. Compose cell: the shipped `docker-compose.yml` with no Basic-gate variable set on the host passes none (05-config §1.12.4), so a gate enabled in Settings stays on (OPS5-1; XFILE-8, CONFORM-7) |
| `TestBasicGate_UsernameCompared` | from F3: a wrong username with the right password → 401; with `core.http.basic_gate.username` empty and a password set, `:<password>` passes as at HEAD (SEC6-9) |
| `TestCSP_DefaultPerOwner` | SPA, legacy `DefaultCSP(r)`, native default; a handler-set CSP is kept (FE-4); the legacy SPA response carries the inline-script hash, the configured Access team host and `Reporting-Endpoints` (FE2-1) |
| `TestPerimeter_XAppBuildOnEveryResponse` | legacy, native, infra, 404, 405 and legacy 410 responses all carry `X-App-Build` (FE2-5, OPS2-13) |
| `TestInfra_MetricsRequiresScope` | (native role store) `/metrics` without a credential → 401, with a key lacking `core:metrics.read` → 403; `/livez`, `/readyz` stay Public (SEC2-14) |
| `TestMetricsScrape_MigratedReach` | (ABO, migrated fixture, from F4) on the legacy `/metrics` entry and, from F8, on the `Infra` mount: an `editor` session, a migrated full-scope key and a migrated key scoped `["library.view"]` → 200 (the identity migration stored `core:metrics.read` on them, 05-rbac §6), an `fp-worker` user's key and an admin-owned key scoped `["fingerprint.worker"]` → 403 (confined principals never get it, SEC10-1, R10-2), no credential → 401 |
| `TestClientIP_ZonedConnectorTrusted` | zoned link-local connector peer: header believed, peer admitted (SEC-18) |
| `TestTrace_NoApikeyInSpanAttributes` | every `log.DefaultRedactQuery()` value (`apikey`, `token`, `code`, `code_verifier`, `refresh_token`, …) absent from span attributes and the access log (SEC-22, SEC2-18; the list's superset-of-HEAD test is `TestRedactQuery_SupersetOfHEAD` in 05-logging) |
| `TestAccessLog_NoInviteToken` | no one-time token in the access log, journal or span attributes (SEC-21) |
| `TestStage12_InvalidKeyPlusValidSessionIs401` (05-authn §10 owns it: stage 12's stop rule, refused at stage 13) | stop rule (SEC-12) |
| `TestStage13_StaleCookieOnPublicRouteIsAnonymous` | cookie cleared, request proceeds (SEC-11) |
| `TestFacadeKey_RefusedOnCoreAndModuleGroups` (deferred-build, §3.6) | SEC-5 |
| `TestConfining_WorkerKeyRefusedOnABS` | an `fp-worker` key and a worker user's ABS login get ABS 403 on every ABS route whose `Perm` does not name `books:fingerprint.worker` (SEC2-4) |
| `TestProblem_ShapeGolden` / `TestLegacyError_ShapeGolden` | both envelopes byte-for-byte (the legacy one for gin and the router's legacy-prefix 404) |
| `TestUnmatched_APIPathIsProblemNotHTML` | `/api/nope` → problem 404; `/library` → SPA owner |
| `TestUnclean_PathNotRedirectedByMux` | `//ping` is not 301-redirected; the `/` owner decides |
| `TestSPA_NotRateLimitedAndPublic` | 200 cold-load requests to `/` owner are not 429; `RouteInfo{Kind: SPA}` present (FE-25) |
| `TestSPA_SingleOwnerOfRoot` | legacy `ServesSPA` + `Router.SPA` → `ErrPrefixCollision` (FE-1) |
| `TestBasicGate_SPAOptionsExemptAssets` | from the shell flip F9: `Router.SPA(h, SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})` exempts exactly HEAD's asset set on the shell; `TestBasicGate_ExemptSetGolden` repeated in the shell-flip PR passes (FE5-10) |
| `TestNonSPA_PredicateCoversClaims` | `/socket.io`, `/API/V1/x`, `POST /library`, `/health/`, `/HEALTH`, `/metrics/`, `/livez/` never reach the SPA handler; `/assets/x.js` and `GET /login` do (FE-3, FE2-6) |
| `TestNonSPA_LegacyFallbackConsultsPredicate` | with the legacy mount owning `/`, `/livez/` and `/READYZ` get problem 404, never gin's `NoRoute` page (FE3-13) |
| `TestMethodNotAllowed_HasAllow` | 405 + `Allow` |
| `TestBind_TagsCompiledAtRegistration` | bad tag → `ErrBadBinding` at boot |
| `TestDecode_StrictUnknownField` | 400 `unknown_field` on a route not marked `Lenient` |
| `TestPage_OffsetDefaultHasTotal` / `TestCursor_FilterMismatch` | SIMP-8 |
| `TestTaskStart_IdempotencyKeyReplaysRunView` | a repeated `Idempotency-Key` on task start → 200, the existing `RunView`, `Idempotent-Replayed: true` (SIMP2-8; store in 05-tasks) |
| `TestOutsideClaim_Refused` / `TestBuild_CrossOwnerShadowRefused` | a legacy route outside its mount's `LegacyPrefixes` (for example at `GET /api/core/v1/auth/oidc/google/callback`) → `ErrOutsideClaim`; one owner's pattern strictly more specific than another's → `ErrShadow` (SEC3-8) |
| `TestCoreFixedGroup_OIDCCallback` / `TestCoreExactPatterns_TwoEntries` | from F4: `GET /api/v1/auth/oauth/{provider}/callback` reaches core's callback through the whole core chain (`RouteInfo.Owner` `core`, Public, `RateAuth`) with no `ErrPrefixCollision` or `ErrShadow` against books' claim; a `CoreFixedGroup` route not in `CoreExactPatterns()` and a manifest route over the entry → `ErrPrefixCollision`; `CoreExactPatterns()` is exactly the callback and start patterns; HEAD's start path `/api/v1/auth/oauth/{provider}/start` reaches core's start handler through the whole core chain (`RouteInfo.Owner` `core`, Public, `RateAuth`) with no collision against books' claim and no gin handler, and an app-branch start there (both deep-link parameters, a listed `redirect_uri`, S256) leads to the same app-branch callback as the core start path, whose per-rule cells are 05-authn's `TestOAuthStart_HEADPathServedByCore`; an app-branch callback (both deep-link parameters at start) answers a redirect to the allow-listed app scheme with no `Set-Cookie` of a web session (§3.5, §4.9, §8.1 F4 row, 03 §3.8; SEC-2, whose per-control tests 05-authn §10 owns) |
| `TestPublicRoutesGolden` / `TestBuild_PublicNotInManifestRefused` | the reviewed set of every `Public` route, native and legacy; a module native route with `rbac.Public` absent from `Manifest.PublicRoutes` → `ErrPublicNotAllowed` (COR3-3) |
| `TestCompressionExclusionsEqualHEAD` | golden from HEAD's `compressionMiddleware` lists: the legacy exclusions that still exist carry `NoCompress`; `text/event-stream` responses are never gzipped; the log download is gzipped once (COR3-6) |
| `TestPerimeterHonoursBlobOnlyValues` | release R1: rate limit disabled only in ABO's settings blob → stage 10 off from boot; from F3: the migrated stored value keeps stage 10 off and a runtime edit applies without restart (COR3-1) |
| `TestBuildID_HeaderEqualsEmbeddedMeta` | for a `make build`, a Docker build and a goreleaser build, `X-App-Build` equals the embedded `index.html` meta value, which for every `embed_frontend` artefact is neither `""` nor `dev`; no Go build step is passed a UI build ID, and `X-App-Build` never equals `app.BinaryID` (FE3-2, FE4-1, FE5-1, FE5-3) |
| `TestOpenAPI_UsesConfigOverrides` | a `config.Duration` field renders as `string/duration`, the same schema as in the settings schema (SIMP3-1) |
| `TestRequestID_GeneratedAndPropagated` | header, problem, slog record, audit record carry the same id |
| `TestRateLimit_ClientBuckets` | `synctest` time; headers; legacy `RateClass` golden |
| `TestOpenAPI_Golden` / `TestOpenAPI_IsValid31` | generated doc equals committed file; validates against the 3.1 meta-schema |
| `TestOpenAPI_BreakingGate` | a fixture document with a removed field fails the `oasdiff breaking` step; the same change under a new major passes (§4.6) |
| `TestContractGoldens_ValidateAgainstSchema` | every example matches its schema |
| `archtest.RoutesHavePermissions` | over native + legacy manifest + SPA/infra set (R6) |
| `TestManifest_SPermRoutesCarryAtomRequirement` | from R1: every manifest entry recorded from an `s.perm(atom)` gin route has `Perm` equal to books' atom-table entry for the atom, built with no catalog; from F4 `Build` refuses an entry whose scopes are not in the catalog (COR6-1) |
| `TestLegacyManifest_MatchesGinRoutes` / `TestLegacyManifest_RegistersAsMuxPatterns` | `ErrLegacyManifest` on drift; from R1, over `(*gin.Engine).Routes()` of the real engine built with ABS on and with ABS off (never a fixture): every gin route is a mux pattern or a member of one generalised pattern, `Build` raises no conflict, the generalised patterns and kept routes of §4.8 that the build holds are present, and for each conflicting pair Go's overlap example is routed by the mux to a pattern holding gin's route (SEC-13, resolution A-1) |
| `TestCollisionRule_RefusesUnmergeableSets` | fixtures: a component spanning two owners (rule 1, A-1), members differing in `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` or `NoCompress`, members of different segment counts, a kept route not strictly more specific than the generalised pattern, and a generalised pattern that conflicts with another route → `ErrPatternConflict` naming the routes (A-1) |
| `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` | over books' gin engine, for every generalised pattern the build holds (in R1 HEAD's two, §4.8): each member reaches its gin handler; a path the pattern matches and no member does (`/api/v1/operations/x/y`) → 404 in the legacy shape with no handler run; the kept route wins its overlap (`/api/v1/operations/v2/activity` reaches `GetOperationV2` while that route exists) (A-1, SEC2-22) |
| `TestStage2_RecordsEveryRequest` | (real chain, ABO's gin engine mounted by `MountLegacy`, from R1) a native request, a legacy request on a sampled `{id}` route, a generalised-pattern member, a legacy-path 429 (stage 10), a stage 7 refusal, a gin `NoRoute` 404, a router 404 and an SPA load are each recorded exactly once in the three `http.server.*` instruments, with the route's mux pattern as `http_route` or, for the unrouted ones, none; `Gather` returns no error and no duplicate family; the duration carries the semconv boundaries; status code and port are integers |
| `TestStage2_RouteSurvivesRequestClone` | a stage between 2 and the mux that clones the request (`r.WithContext`) leaves a native route's and a legacy route's `http_route` and span name correct (MOD10-1) |
| `TestStage2_ServerAddressIgnoresHost` | two requests to one route with different `Host` headers produce one `server_address` value, `Options.ServerName` (MOD10-2) |
| `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash` | a request with `%2F` in a segment whose gin match differs from its mux pattern → 404, no gin handler runs (SEC2-22) |
| `TestOwnAuthRoutesGolden` / `TestLegacyAuthGin_RefusedWithoutOption` | the reviewed `OwnAuth` set; `LegacyAuthGin` without `LegacyAuthGinUntil`, and `OwnAuth` without a registered auth method → boot errors (SEC2-17) |
| `TestIdentityCutover_ReachOracle` (the reach oracle; 05-rbac §12 owns it) | on the F4 PR, over the fixture corpus written by the R0 binary plus its labelled hand-built overlay (custom roles and the other rows R0's API cannot write, 09 §9.3; IMPL-8), migrated: every manifest route (572 at `748fd787a`; the count is generated at the flip's base commit, ADV-11) × every principal kind of 05-rbac §12 (seed-role sessions, scoped API keys, auth-off mode: `settings.manage` routes 200, in-handler `deny` sites deny, the recorded tightenings of 05-rbac §4.11 asserted refused; COR5-4, SEC5-8, SEC5-9) allow and deny as the R0 binary does (HEAD plus the six early fixes, SEC-13), apart from the reviewed differences 05-rbac §12 lists, each asserted as such (the auth-off tightenings, the tightened `settings.manage` system routes, the `core:system.status` readiness reach, dominance and the one reviewed gain among them), an every-atom key whose owner's roles hold `core:admin`, or an admin key, keeping its whole reach, task-run approval included, and an editor-owned every-atom key keeping only the intersection (SEC-12); the playlist write routes admit every authenticated principal (SEC-4); custom-role half: for random native roles, stage 13 admits a legacy route only if the route's successor admits (SEC4-2). It stays as the regression test of books' atom table |
| `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` | every manifest route gated by `settings.manage` or `users.manage` carries the atom table's full-reach entry, 05-rbac §6.1's "Route requirement" (XFILE-5); a custom role holding the four `settings.manage` expansion scopes gets 403 on backup restore, backup create, logs, tool install and plugin settings (R4-3, SEC4-2) |
| `TestCoreRoutesReplaceGin` | after each R2 flip, on the real engine: no gin route remains at a path the flip's native routes replace (the flip's generated list), and after F5 no route lies under `/api/v3/` (§4.6, §4.9, D10) |

## 13. Open items

1. Whether `encoding/json/v2` replaces `encoding/json` in `Handle` (only once it is non-experimental in the pinned toolchain; 13-toolchain).
2. Whether `/metrics` moves under `/api/core/v1/metrics` or stays at `/metrics` (05-metrics decides; either way it carries `core:metrics.read`, which ABO's identity migration stores on every principal that is not `Confined()`, 05-rbac §6).
3. **Resolved** (coordination with 06-app-sdk, 06 O2, COR-16): 06 registers routes through
   `Registrar.HTTP() *httpapi.Group` with `httpapi.Handle` (or `Register(g, Get(...))` above) and legacy
   routers through `Registrar.MountLegacy(httpapi.LegacyMount)` (§8.2); there is no `Registrar.Route` or
   `Registrar.LegacyHandler`. This file owns the final signatures.
4. HTTP/3 `Alt-Svc`: becomes a listener option that sets the header in stage 5, fixing the
   registration-order miss noted in `raw/09` §2.2(c) — to be confirmed by a test once implemented.
5. A per-principal rate limiter (deferred, SIMP-8): add only if `mediacore_http_rate_limited_total`
   or the access log shows abuse from authenticated principals behind one address.

(v1.2.0's item 6, the stage that ships `@mediacore/api-client`, is closed: release R1's front-end work, 07 §7.19 steps 1–2, ships it, 09 §9.4.)

## Objections to 03

None open. v1.2.0's amendment ("no API returns a chain-less route group") is resolved: 03 v1.5.0 R6
states it, including `LegacyAuthGin` admission and `OwnAuth` auth methods (§8.2). (v1.0.0's
objection asking for reserved module IDs is resolved: 03 §3.6 owns the list and assigns each name to
one owner list; `httpapi.ReservedSegments()` supplies this file's part, §3.5, §4.1.)

## Out-of-scope defects found

1. HSTS is set when the client sends `X-Forwarded-Proto: https`, from any peer
   (`internal/server/server_middleware.go:113`); harmless in practice (browsers ignore HSTS over
   plain HTTP), fixed by stage 5's trusted-proxy rule.
2. `RequireAuth` answers a store read error with 500 rather than 503 (`internal/server/middleware/auth.go:163-167`), so clients and monitors cannot tell "auth backend down, retry" from a server bug; fixed by §6 (503 `auth_unavailable`).

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SIMP-2 / resolution 2 — `MountLegacy` permanent behind the core perimeter; no gin-retirement stage; books groups move only when rewritten (§1, §4.6, §8).
- OPS-11 — auth engine swap in exactly one stage (09 S4); S2 moves only the perimeter (§8.1).
- FE-1 — one owner of `/`: legacy `ServesSPA` until shell adoption, then `ui.Handler`; no `/` pattern reserved by httpapi (§4.1, §4.10).
- FE-3 — `Router.NonSPA` predicate for `ui.Options`; non-GET/HEAD never served the SPA (§4.10).
- FE-4 — CSP default on every response per owner; legacy `DefaultCSP` (§4.2 stage 5).
- FE-5 / SEC-10 — cross-origin protection stage 7 with `http.CrossOriginProtection` (§4.2, §6, §12).
- FE-7 — normative 401 codes; Public auth flows marked for `skipAuthRedirect` (§4.3, §7).
- FE-12 — snake_case JSON naming normative and enforced (`ErrJSONName`) (§3.4); UI-0 timing left to 09 (§13).
- FE-19 — no base path; served at the origin root (§1).
- FE-25 — SPA handler runs stages 0–9 only, Public, rate-exempt, in the archtest infra set (§4.10).
- SEC-5 — facade-key refusal on every non-facade route in stage 13 (§4.2) — also requested by R-F.
- SEC-6 — Protection column; perimeter keys `ProtectAdmin` (§10).
- SEC-11 / SEC-12 — stage 12 stop rule and stage 13 failure/cookie handling (§4.2).
- SEC-13 — each manifest route its own mux pattern enforced by stage 13 from S4; bridge contract table; manifest drift is a boot error (§8.2, §8.3).
- SEC-14 — Basic gate exempt per route (`OwnAuth`), never per mount (§4.2 stage 8, §8.2).
- SEC-17 — bridge denials audited through `rbac.Require` (§8.3).
- SEC-18 — `TrustedPeer` with zoned addresses; `TUNNEL_CONNECTOR_ADDRS` alias; trusted peers admitted in stage 0 (§3.5, §4.2, §10).
- SEC-21 / SEC-22 — URL redaction in span attributes and access log, including `token`/`apikey` (§4.2 stages 2 and 4).
- OPS-10 / SIMP-7 — aliases reference the single compat window; no wall-clock flip; compat counter (§3.1, §4.6, §11).
- SIMP-8 — offset default with `total`, cursor opt-in without HMAC, idempotency only on task-start/side-effect POSTs, ETag opt-in, per-principal limiter deferred (§3.1, §3.2, §4.4, §4.5).
- SIMP-14 — one JSON Schema reflector owned by `config`; jsonschema-go v0.4.3 verified (licence, version, maintenance) (§1.1, §7).
- SIMP-27 — `books-legacy.json` dropped; no Go TypeScript emitter (§7).
- COR-16 — `httpapi.Field` helper so 06's hello compiles against `Invalid(fields...)` (§3.3, §13).
- COR-19 — layer L6 per 03 v1.2.0 (§3).
- Cross-cluster (R-A) — `httpapi.ReservedSegments()`; core routes registered from `internal/<capability>http` (L6, kernel-only) (§3.5, §4.1, §5).
- Cross-cluster (R-E) — `*metrics.Scope`; Protection column; invite-token access-log test (`TestAccessLog_NoInviteToken`).
- COR-20 — `*metrics.Scope` in `Deps` (§3.5).
- SIMP-6 — idempotency prune on `tasks.Schedule` (§4.5).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: Global sweep (SIMP-22, as recorded in 06) — §4.9 narrowed to one subtree-claim rule: `Router.Claim(owner, prefix)`, no `PrefixClaim.Kind`, no `YieldsTo`; `/` and the legacy `/api/` redirect are fallbacks, not claims.
- integration pass: Verified already present (R-D request, SEC-5): stage 13 refuses a facade-purpose key on non-facade routes and a non-facade key on facade routes.
- integration pass: R-H request (G11) — §8.4: Cloudflare Access authenticator is ABO's `internal/cfaccess`, not `auth/cfaccess`.

Round 2 (2026-11-04), findings applied to this file:

- R2-1 — S4 rollback floor = the identity Teach build (05-authn §11.1); bridge adapters enforce (§8.1, §8.3). (superseded in round 3 by R3-1: no S4 floor)
- SIMP2-6 — facade members (`FacadeGroup`, `RouteKind Facade`, stage 13 facade clause, `apiKeyHeader`, facade tests) moved to a deferred-build section (§3.6, §4.2, §4.9, §7, §12).
- SIMP2-8 — httpapi idempotency store deleted; task start uses `tasks`' store via `Idempotency-Key` → `StartRequest.IdempotencyKey`; stage 14 removed (§3, §4.2, §4.5, §6, §9–§12).
- SIMP2-12 — `Deprecation{Shipped,Sunset}` deleted; aliases and deprecated routes name a ledger row, headers rendered from the embedded ledger (§3.1, §3.5, §4.6, §12). (ledger source refined in round 3 by SIMP3-3)
- SIMP2-3 — reflector in `core/internal/jsonschemax` with per-package overrides; `Deps.Config` dropped; no `config` import (§1.1, §3, §3.5, §7). ("no `config` import" superseded in round 3 by SIMP3-1)
- SIMP2-1 — the idempotency prune schedule is gone with SIMP2-8 (§4.5).
- MOD2-20 — "pinned exactly" made real: Dependabot ignore + `check_module_skew.py --pin` (§1.1).
- COR2-9 — minimum surface at S2 (rbac leaf types; auth/rbac/audit nil until S4; no storage) (§3, §3.5, §5). (refined in round 3 by COR3-3 / MOD3-4)
- COR2-19 — `ReservedSegments` static at P1; compat segments only from `app.Options`; `uihttp` added to the `CoreGroup` rule (§3.5, §5).
- COR2-1 — legacy event streams are gin manifest routes wrapping realtime projections (§4.1, §8.1).
- FE2-1 — `DefaultCSP func(*http.Request) string` + `CSPReport`; `Reporting-Endpoints` in stage 5; test extended (§4.2, §8.1, §8.2, §12).
- FE2-5 / OPS2-13 — stage 5 emits `X-App-Build` on every response from S2; one build-ID source; perimeter golden (§4.2, §8.1, §12). (ID source refined in round 3 by FE3-2)
- FE2-6 — `NonSPA` covers infra first segments, `health`, `metrics`, today's eight segments; `/livez` naming (§3.5, §4.1, §4.10, §12).
- FE2-7 — kernel check `ui.ReservedSegments ∩ NonSPASegments = ∅` (`ErrShellSegment`) (§3.3, §3.5, §4.10, §12). (error name superseded in round 3 by FE3-9: 06's `ProblemSPASegmentCollision`)
- FE2-11 — `ReservedSegments` per 03 v1.5.0 (`assets`, `static`, `ui`, `livez` added); compat inputs split into `app.Options.ReservedAPINamespaces` and `LegacyMount.TopSegments` (replacing `Segments`; P7 re-check of module IDs); `SPAShared` added (§3.5, §4.1, §8.2).
- FE2-23 — no credential type exempt from stage 7 (Basic included); `X-Forwarded-Host` from trusted proxies (§4.2, §12).
- FE2-24 — stale FE-20 and item-6 text closed; `core.json` only from hello; books client is a directory; `RouteInfo.RateClass` (§3.5, §7, §8.4, §13).
- SEC2-4 — ABS adapter keeps worker confinement via `CheckCredentialFor` (§8.3, §12).
- SEC2-5 — aliases dispatch by selector (`Targets`, `Request`, `Selector`) and are strict; `me/password` with `user_id` stays gin (§3.1, §3.4, §4.6, §8.1, §12). (`me/password` refined in round 3 by COR3-12: one gin route, no alias)
- SEC2-14 — non-Public `Infra` routes are "routes" (stages 10–13) (§3.5, §4.2, §6, §12).
- SEC2-16 — CF Access registered through `app.Options.Authenticators` (§8.3).
- SEC2-17 — `LegacyAuthGin`/`OwnAuth` admission by the kernel (§3.3, §8.1, §8.2, §12); objection 1 to 03 closed (03 v1.5.0 R6 states it).
- SEC2-18 — stages 2/4 use 05-logging's `DefaultRedactQuery` (⊇ HEAD) and `DefaultPathRules` (§4.2, §12).
- SEC2-19 — stage 0 publishes `ClientAddrFrom`; `client_trust.go` stays in ABO until S4 (§3.5, §4.2, §8.1, §8.4).
- SEC2-20 — stage 7/13 refusals go to the access log (`security_event`) until audit exists at S4 (§3.5, §4.2, §6, §8.1).
- SEC2-22 — legacy wrapper carries the matched pattern into gin and refuses a mismatched gin match (§4.8, §8.2, §8.3, §12; since round 11 through `httpapi.MatchedPattern`, not `r.Pattern`).
- SEC2-6 — legacy invite path redaction named as a 05-logging `DefaultPathRules` entry (§4.2).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Open item 3 resolved: 06 uses `Registrar.HTTP()` and `MountLegacy`; no `Registrar.Route`/`LegacyHandler`.

Round 3 (2026-11-04), findings applied to this file:

- R3-1 — §8.1 S4 rollback: binary swap over one stored identity format; no Teach build, no S4 floor (§8.1).
- COR3-1 — perimeter settings read through config's legacy adapter over `config_blob` from S2; `TestPerimeterHonoursBlobOnlyValues` (§8.1, §10, §12).
- COR3-3 / MOD3-4 — S2 surface: `auth` leaf types at S2, `Deps.Compat` at S2b, `Deps.Auth/RBAC/Audit` at S4; `httpapi` → `internal/openapi` only; `ErrPublicNotAllowed` deleted for `TestPublicRoutesGolden`; `core.json` at `core/openapi/core.json`; basic-gate password plaintext until S3 (§3, §3.3, §5, §7, §10, §12).
- COR3-6 — stage 9 skips `text/event-stream` and `LegacyRoute.NoCompress` routes (HEAD's exclusions); writer has `Unwrap`/`Flush`; operations-stream access-log skips (§4.2, §8.2, §12).
- COR3-12 — `PUT /api/v1/auth/me/password` is one gin route, no alias (§4.6).
- COR3-13 — `Route.Strict` inverted to `Lenient` (zero = strict) (§3.1, §3.4).
- SEC3-8 — aliases and legacy routes confined to their owner's claims; `ErrAliasOutsideClaim`, `ErrShadow`; tests (§3.1, §3.3, §4.9, §6, §12).
- SEC3-14 — `OwnAuth` admission from S2 through the recording-only S2 `Registrar.AuthMethod` (§3, §8.2).
- SEC3-16 / FE3-5 — ABO's `SPAShared = ["login", "invite"]`; the legacy invite and temp-login 302s are books manifest routes for the whole window, `ui.Handler` implements none; test (§3.5, §8.2, §12).
- FE3-1 — `NonSPA` is `TopSegments` only, never `SPAShared`; `LegacyPrefixes` first segments are reserved IDs only (§3.5).
- FE3-2 — build ID read from the embedded bundle's meta (`LegacyMount.BuildID`, later `ui`); no link-time value; missing-header rule (§4.2, §8.2, §12).
- FE3-6 — `LegacyPath.Shape` is untyped; aliases documented with the native schema; legacy-shape clients keep hand-written calls; no `WithLegacyShape` (§3.1).
- FE3-8 — `ui.ReservedSegments` lands at S2 so the reserved-ID equality test holds from S2 (§4.1).
- FE3-9 — one shell-segment error, 06's `ProblemSPASegmentCollision`; `ErrShellSegment` and its test deleted (§3.3, §3.5, §4.10, §6, §12).
- FE3-10 — `csp.go` stays in ABO until S9, `csp_report.go` → `internal/uihttp` at S4, `static_embed.go` → `ui` at S9 (§8.4).
- FE3-13 — the legacy `/` fallback consults `Router.NonSPA()` before gin from S2 (§4.10, §12).
- FE3-16 — `internal/modulehttp` named, fed a read-only `ModulesView` (§5).
- FE3-19 — dev proxies rewrite `Origin` with `Host` (§4.2 stage 7).
- SIMP3-1 — `httpapi` imports the static `config.Overrides`; `time.Time` is in `jsonschemax.Base`; `TestOpenAPI_UsesConfigOverrides` (§1.1, §3, §7, §12).
- SIMP3-2 — deferred facade members checked by `archtest.DeferredMembersAbsent` from S2 (§3.6).
- SIMP3-3 — ledger dates from `app.Options.CompatLedger` merged with core's fragment (§3.1, §3.5, §12).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): S2 r2 perimeter move excludes the Basic gate; new S3 row (gate moves with HEAD's exemption, `TestBasicGate_ExemptSetEqualsHEAD` and the gate half of `TestPerimeterHonoursBlobOnlyValues` at S3); `basic_gate.password` read by core from S3; settings routes go native at S4 (FE3-11); `ErrPublicNotAllowed` checks 06's `Manifest.PublicRoutes` (plus the golden); `jsonschemax.Base()`/`config.Overrides()` as functions; plugin router deleted at S7.

Round 4 (2026-11-04), findings applied to this file:

- R4-1 / SIMP4-2 / SIMP4-3 — S4 builds `auth` and `rbac` over the stores books supplies (`module.Manifest.LegacyStores`); no storage grant; native-only data in books' extension family; rollback text rewritten (§8.1).
- R4-3 / SEC4-2 — an `s.perm(atom)` route's `Perm` and the bridge adapter are `Catalog.LegacyReach(atom)` (`AllOf` the full legacy reach); auth-off judged against the expansion; custom-role half of `TestLegacyRoleReachUnchanged`; `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` (§8.1, §8.2, §8.3, §12).
- SEC4-4 — the reach comes from the frozen reach snapshot (05-rbac §6.3), not the running manifest (§8.3).
- COR4-4 / DATA4-2 — S4 r1 (dark: stores built, read-only census, `LegacyAuthGin`) and r2 (flip) split; r2 → r1 never meets a missing grant (§8.1).
- R4-2 / SEC4-1 / OPS4-1 / DATA4-1 — the Basic password's first core write leaves a never-matching sentinel in the legacy row and any `config.yaml` copy; the older gate fails closed; R-test cell `TestBasicGate_SentinelRefusesOnPrev` (§8.1, §10, §12).
- SEC4-10 — one canonical name `core.http.basic_gate.password`; "plaintext" corrected to the encrypted legacy row (§10).
- COR4-9 — stage 8's exempt set equals HEAD's: `LegacyRoute.BasicExempt` (two health paths) and `LegacyMount.BasicExemptAssets` (static assets); test golden extended (§4.2, §8.1, §8.2, §12).
- FE4-1 / COR4-18 / MOD4-9 — build ID stays meta-only (this file is the reference design): no Go build step receives an ID, no boot check; COR4-18's link-time alternative not adopted per the FE4-1 disposition (§4.2, §8.2, §12).
- FE4-3 — missing `X-App-Build` counts only on responses carrying a `Reporting-Endpoints` `csp` group (§4.2).
- FE4-2 — invite links keep the `/invite/<token>` path form until S9; the `GET /invite/{token}` 302 and `SPAShared` `invite` start at S9 (§3.5, §8.2, §12).
- FE4-7 — `opsgen` repointed at S5 and retired at S9, whose PR rewires `api.ts`' operation types and `operationPolling.ts` (direct importers listed) (§7, §8.4).
- Cross-cluster (R-F, R4-1) — §4.1 names the core confirmation routes for suspended native-only data, `POST /api/core/v1/integrations/{kind}/instances/{id}/confirm` among them.
- FE4-9 — the stale-build rule is in the api-client runtime's middleware list; `apiFetch` calls its `checkBuild` from UI-0 (§7, §8.4).
- COR4-11 / MOD4-11 — S2 leaf types: rbac adds `RoleName`, `Checker`; auth adds `Assurance`, `User`, `UserID`, `UserStatus`; `compat` not in the S2 surface (§3).
- COR4-19 / SIMP4-1 — heading renamed `Deferred-build members`; list generated from every such heading, package-qualified matching, per-member lifetime (§3.6).
- COR4-21 — `time.Time` removed from httpapi's reflector overrides (in `jsonschemax.Base()`) (§7).
- R4-4 — ABO's ledger embed is a generated copy under the books module (§3.1).

Integration pass (round 4):

- integration pass (round 4): §7 and §8.4: `opsgen` retires with the last native `ops.ts` importer (04's row, 07 §7.8), not at S9 (the S9 PR moves only the Activity page); checkBuild, Basic password and LegacyStores items checked, already applied.

Round 5 (2026-11-04), findings applied to this file:

- SEC5-4 / DATA5-12 (R5-1(c)) — stage 8 refuses an enabled gate whose password is unset, cleared, empty or unreadable (401, health `basic_gate_no_password`); `TestBasicGate_EmptyPasswordRefuses` is a core test here (§4.2, §8.1, §12).
- OPS5-2 (R5-1(b)) — the Basic password is not moved: books' store reads HEAD's encrypted row in place, no sentinel; S3 rollback text and §10 rewritten; `TestBasicGate_SentinelRefusesOnPrev` replaced by `TestBasicGate_PasswordReadInPlaceAcrossRollback` (§8.1, §10, §12).
- OPS5-1 (R5-1(a)) — perimeter legacy keys keep HEAD's precedence (UI value over compose's env default); compose cell in `TestPerimeterHonoursBlobOnlyValues` (§8.1, §12).
- COR5-4 / SEC5-7 / SEC5-9 / SEC5-14 — legacy routes carry `rbac.LegacyAtom(atom)` (roles: full reach; pre-core keys: role ∩ key atoms; auth-disabled: expansion); in-handler sites become `rbac.LegacyCheck` per the snapshot's in-handler list; the bridge binds no legacy set for the auth-disabled principal (§4.2, §8.1, §8.2, §8.3, §12).
- SEC5-8 — `credGuard` anchor corrected to `credential_routes.go:79-87` (pass-through with auth off); the `Guards.Interactive` refusal is a recorded tightening (§8.3, §12).
- OPS5-5 / SEC5-13 — S4 r2 re-annotates the legacy `/metrics` entry to `core:metrics.read`; `TestLegacyMetricsRoute_RequiresScopeFromS4` (§8.1, §12).
- FE5-1 / FE5-3 (R5-7) — the UI build ID is stamped by the npm build script itself on every build path, never by a side script, and is never the binary's identity (`app.BinaryID`); test extended (§4.2, §8.2, §12).
- FE5-7 — `ui_build_id` is the served shell bundle's ID, `""` while a legacy mount owns `/` (§5).
- FE5-10 — `Router.SPA(h, SPAOptions{BasicExemptAssets})`; the kernel passes `ui.BasicExemptAssets` at P8; test (§3.5, §4.2, §4.10, §12).
- COR5-16 / SIMP5-6 — deferred members as a table with an identifier-only first column; heading match after an optional section number (§3.6).
- MOD5-5 / SIMP5-14 — `log.DefaultRedactQuery()` / `log.DefaultPathRules()` cited as functions (§4.2, §12).

Round 6 (2026-11-04), findings applied to this file:

- COR6-1 — `LegacyAtom`/`LegacyCheck` join the S2 surface as record-only constructors; the S2 manifest records `rbac.LegacyAtom(atom)` for every `s.perm` route and stage 13 enforces it from S4 r2; `TestS2Manifest_SPermRoutesCarryLegacyAtom` (§3, §8.1, §8.2, §12).
- FE6-1 / FE6-8 — one signature `checkBuild(response, { buildId, shell })`; `buildId` from the served page's meta and `shell` from the caller, never a library-baked define; e2e cell on a production `dist/` build (§7, §8.4).
- SEC6-9 — `core.http.basic_gate.username` row (string, `""`, live, admin, `BelowStored` alias `BASIC_AUTH_USERNAME`); stage 8 compares it; an empty username kept at HEAD parity with the reason; `TestBasicGate_UsernameCompared` (§4.2, §10, §12).
- SEC6-2 — legacy-route key rules cite the one scope-source predicate (keys whose scopes come from the legacy row) (§8.1, §8.2, §8.3).
- MOD6-1 (R6-2) — `opsgen` keeps its HEAD input `internal/operations/state`, which stays in ABO; no S5 repointing (§7, §8.4).

Round 7 (2026-11-04), findings applied to this file:

- SEC7-1 (R7-1) — `core.http.basic_gate.enabled` row: an operator-enabled gate over a stored `basic_auth_enabled: false` is written back `true` at P4d step 1 from S3, only when a stored password exists (§10).
- SEC7-2 (R7-1) — a stored value equal to the default (`enable_rate_limit: true`) still beats its `BelowStored` legacy name (§10).
- COR7-1 / SEC7-4 (R7-2) — the factory-reset core route lands in `internal/storagehttp` at S4 r2 and the legacy path becomes its alias; before r2 books' gin handler stages through `legacy.Maintenance` behind HEAD's chain (§5, §8.1).
- FE7-5 — `checkBuild` returns `StaleReason | null` with `onStaleBuild`; `readPageBuildId()` is the one meta reader; `createCoreClient`'s options are required; ABO's pre-shell client and `createModuleClient` named; `apiFetch` wording corrected; listener test (§7, §8.4).
- FE7-13 — `/modules`' `ui_build_id` contrasted with `--version-json`'s (§5).
- integration pass (round 7): §10 `core.http.basic_gate.enabled` declares `config.NarrowStored` from S3 (R-E request, R7-1); §12 stop-rule test is 05-authn's `TestStage12_InvalidKeyPlusValidSessionIs401`.

Round 8 (2026-11-04), findings applied to this file:

- SEC8-1 (R8-4) — §10: `NarrowStored("core.http.basic_gate.enabled", narrow = value == true, when = nil)`; the false "keeps the gate closed" rationale replaced by the fail-closed sentence; release note about `BASIC_AUTH_PASSWORD` before rolling back below S3; R-test cells for canonical-only and env-only passwords.
- SEC8-6 (R8-4) — §10: `NarrowStored` on `core.http.rate_limit.enabled` (narrow = `true`); paragraph on canonical-only operator values (`peer_allowlist`), `operator-names.json` and the advisory token `canonical_only_operator_values`.
- SEC8-12 — §10: an absent `basic_auth_enabled` counts as HEAD's default `false` for the narrowing.
- SIMP8-6 — §10: the Basic gate keeps its explicit predicate, `core.auth.mode` uses its `SafeLegacyValue` one.
- COR8-1 / COR8-2 (R8-1) — §5: the storagehttp route's request (`{"confirm": "RESET"}`, 422 otherwise) and its `Stager`; both legacy resets are its aliases with HEAD's `{data: {message}}`; §3.1: a one-target alias may set `Request` only to fix a path value no native request carries (the store-reset alias's `reset_kind`).
- FE8-2 — §7: option (b): `checkBuild` stays synchronous and header-only (cases 1 and 3); `checkModules(body, response, opts)` decides case 2 after the shell's `/modules` fetch parses the body.
- FE8-3 — §7: `onStaleBuild` replays detected reasons to a new listener (`getStaleReasons()`); test cases added.
- FE8-6 — §7: "`ops.ts` is imported by `services/api.ts` and `utils/operationPolling.ts` (5 non-test importers)"; page list dropped.
- FE8-10 — §7: `opsgen` writes `web/src/generated/ops.ts`; `createCoreClient({ buildId, shell })`.
- integration pass (round 8): §10: both `NarrowStored` declarations drop `when = nil` (`NarrowStored` has no `when` parameter; R-E request); §4.10 names the P8 app-ID check `ProblemUIAppIDMismatch` over `ui.Bundle.AppID()` beside the segment check (R-G request); the `opsgen` output `web/src/generated/ops.ts` and the FE8-6 importer sentence in §7 were already applied.

Advisor (2026-11-04), resolutions and findings applied to this file:

- A-1 (ADV-1, BLOCKER) — §1.1, §4.8: the route collision rule (conflict graph per build variant; same-owner components with identical pre-gin fields register as one generalised pattern; gin-preferred routes such as `…/operations/v2/{id}` stay their own, strictly more specific pattern; the post-gin check accepts any member's `c.FullPath()`; S5's core alias registers beside books' pattern), with the advisor's appendix A conflict list re-anchored as evidence; §6 rows, §8.1 S2 row, §8.2 struct comment and rule; `TestLegacyManifest_RegistersAsMuxPatterns` on the real engine with ABS on and off from S2 r1, `TestCollisionRule_RefusesUnmergeableSets`, `TestLegacyMount_GeneralisedPatternAcceptsAnyMember`, `TestS5_CoreAliasesRegisterBesideLegacy` (§12).
- A-2 (ADV-3, ADV-S1) — §8.1 S4 row and §8.2/§8.3: books' identity stores keep HEAD's representation (raw sessions, `abk_` keys with legacy atoms, atom-only roles), so the rollback cell no longer mentions native-only records or suspension; §4.1: the confirmation routes for suspended native-only data removed; §10: both narrowing declarations and the canonical-name list file removed, replaced by the release-note rule for canonical-only values.
- A-5 (ADV-S4) — §4.1, §8.1, §12, §13: `/metrics` stays `AnyAuthenticated` for ABO (the legacy entry is never re-annotated; from S8 every `AnyAuthenticated` ABO principal holds `core:metrics.read`), `TestLegacyMetricsRoute_RequiresScopeFromS4` replaced by `TestLegacyMetrics_AnyAuthenticated`; §11: metric names under the app's namespace.
- A-14 (ADV-S8) — §4.6, §6, §11: no persisted usage and no zero-use gate; the `compat` counter is process-local and read by the owner before a contract PR (this file defined no `/compat/usage` route).
- A-17 — §5 item 2, §8.1 S4 row: the native factory-reset route deletes library files only with `delete_library_files: true`, confirmed by `RootDir` and the file count; the legacy alias passes it.
- A-4 (ADV-S3) — §3.6: the deferred-members archtest is gone; the table is planning information.
- ADV-11 — §8.1, §12: the 572-route count is marked informative and generated at the stage's base commit.
- integration pass (advisor): compat counter is `<namespace>_compat_legacy_use_total` (ABO `audiobook_organizer_compat_legacy_use_total`); 09 §9.2.1 row 3 cited; 08 cited as a design sketch.

Round 9 (2026-11-04), findings applied to this file:

- COR9-2 (R9-2) — §4.8 rule 1 holds at every stage; rule 5 gains a dispatcher for a core-owned component; rule 7 replaced: the T0+T1 PR moves all five operations routes to core aliases with HEAD's `library.view`; §8.1 S5 row; `TestS5_CoreAliasesRegisterBesideLegacy` deleted, `TestS5_OperationsComponentMovesWhole` added; `TestCollisionRule_RefusesUnmergeableSets` and `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` restated (§12).
- COR9-4 / SEC9-4 (R9-6) — §3.1, §5: the factory-reset contract is 05-storage's `storage.ResetRequest` (422 `library_files_confirm_required` against a fresh preflight count), flowing through `Stager.StageReset`, `kernel.stageReset` and `ResetArgs`; the alias sets the path values `delete_library_files=true` and `library_confirm=legacy`, never a body field.
- COR9-3 (R9-4) — §12 `TestLegacyMetrics_AnyAuthenticated`: a `LegacyFullScope` key and a scoped key reach `/metrics` because the ambient scopes are added after the key intersection.
- MOD9-2 — §4.2 stage 2, §8.1 S2 row: stage 2 records metrics only off the legacy mount; otelgin stays the emitter of HEAD's `http_server_*` series with gin's route label; `TestLegacySeriesLabelValuesMatchHEAD`.
- DATA9-6 (R9-5) — §10: `core.http.peer_allowlist` and `core.http.trusted_proxies` keep HEAD's storage on a legacy-backed instance (`LegacyYAML` only, file and env, never stored); the "save the setting through the settings page" advice deleted for them; `TestFileOnlyLegacyKeysNeverDBStored`.
- SEC9-1 (R9-5) — §10: the Basic gate keys keep HEAD's auth-off write rule; the rollback advice reads "keep the stored setting", with the auth-off sentence.
- SEC9-11 — §10: the dangling "(below)" replaced by 09 §9.2.1 row 3.
- OPS9-7 — §10, §12: an empty stored Basic password does not shadow `BASIC_AUTH_PASSWORD` (in memory, logged), early fix 5's recovery.
- integration pass (round 9): §4.2 stage 2 and §8.1 S2 row: stage 2 is the only `http_server_*` emitter, `otelgin` removed, and gets gin's `c.FullPath()` as `http_route` through the new §8.2 carrier `httpapi.ReportLegacyRoute`; §4.8: the former rule 7 is gone, its content is the unnumbered "The operations component at S5" paragraph (`TestS5_OperationsComponentMovesWhole`), references updated; §3.1 reset contract: `LegacyConfirm` and the comparison in `kernel.stageReset`.

Round 10:

- COR10-1 (R10-1) — §4.8 rule 1, rule 5 and "The operations component": books keeps all five operations routes as gin manifest routes at every stage (one owner, rule 1 unchanged); the R9-2 move and rule 5's core-owned dispatcher deleted; from T0+T1 the handlers read runs through `tasks.Client.Get`; §8.1 S5 row; `TestS5_OperationsComponentMovesWhole` replaced by `TestS5_OperationsComponentStaysBooks`; `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` runs at every stage.
- COR10-3 (R10-3) — §4.2 stage 2: an alias records its `LegacyPath.Pattern` in HEAD's gin form, a native or `Infra` route its pattern without method and host, `Router.SPA` no route; `TestLegacySeriesLabelValuesMatchHEAD` gains alias cells.
- MOD10-1 (R10-3) — §1.1, §4.2 stage 2: `otelhttp` spans only; stage 2 records the three instruments itself from a per-request carrier it owns; the Labeler and fallback clauses deleted; `TestStage2_RouteSurvivesRequestClone`.
- MOD10-2 (R10-3) — §3.5 `Options.ServerName`, §4.2 stage 2: `server.address` is a constant, never `Host`; `TestStage2_ServerAddressIgnoresHost`.
- MOD10-3 (R10-3) — §4.2 stage 2: HEAD routes that become aliases keep their gin-form `http_route`; the operations component never becomes a core dispatcher (R10-1).
- MOD10-4 (R10-3) — §4.2 stage 2 (c), §8.2: legacy-path requests a core stage answers before the mount are recorded under core's scope with the entry's `GinPath`; an observable change for 09 §9.2.1; `TestPreMountRejection_RecordedUnderCoreScope`.
- MOD10-5 (R10-3) — §4.2 stage 2, §8.1 S2 row: `otelgin` stays on gin with HEAD's metric configuration (tracer off), so no release drops the `http_server_*` families; stage 2 records only into the served provider; the contract test seeds through the real chain.
- MOD10-6 (R10-3) — §4.2 stage 2: stage 2's instruments take the Provider's views, which carry `app.Options.MetricViews`.
- R10-3 — §8.2: `httpapi.ReportLegacyRoute` deleted; the mount's wrapper writes `GinPath` into stage 2's carrier and marks gin-bound requests; `TestHTTPSeries_OneRecorderPerRequest` (each request and each series has one recorder).
- COR10-9 — §3.1 `LegacyPath.Selector`, §5: each reset alias drops `kind`, `delete_library_files`, `library_root` and `library_file_count` before the strict decode, so its path values alone set the request.
- SEC10-1 (R10-2) — §12 `TestLegacyMetrics_AnyAuthenticated`: an admin-owned `["fingerprint.worker"]` key → 403.
- SEC10-5 — §10 `core.http.basic_gate.enabled`: the three gate keys lose HEAD's auth-off write rule together, at the earlier of H2 and their settings cutover.
- integration pass (round 10): §4.2 stage 2 (LEAD correction to R10-3): (c) pre-mount refusals of legacy paths are recorded with **no** `http_route` (not the entry's `GinPath`); (a)/(b) no `otel_scope_name` label (HEAD's `WithoutScopeInfo`), the recorders disjoint by `http_route`; §8.2 wrapper writes no route into the carrier; `TestLegacySeriesLabelValuesMatchHEAD` and `TestPreMountRejection_RecordedUnderCoreScope` rows updated; §3.5 `Options.ServerName` filled from `app.Options.ServiceName`. Already as requested: aliases record `LegacyPath` in gin form; each reset alias's `Selector` lists `kind`, `delete_library_files`, `library_root`, `library_file_count`.

Round 11:

- VER11-1 (R11-1) — §1.1, §3.5 `Options.HTTPMetricsScope` and `InstrumentScope`, §4.2 stage 2 (a)–(c): stage 2 creates its three instruments once, with otelgin's `httpconv` constructors and scope, typed attributes, on the provider otelgin reads, so both recorders share one instrument set; the "disjoint by `http_route`" argument deleted; `WithoutScopeInfo` kept; otelgin's chain position stated once (after the Basic gate from S2 r2 until S3, then first).
- VER11-1 (R11-1) — §3.5 `MatchedPattern`, §4.8 escaped-segment row, §8.2: the wrapper clears `r.Pattern` before gin and the SEC2-22 post-match check reads `httpapi.MatchedPattern(ctx)`, so otelgin records no route for `NoRoute` and SPA loads, as at HEAD; §8.1 S2 row restated.
- VER11-1 (R11-1) — §12: `TestNoRouteSeriesSharedNoDuplicate` added; `TestHTTPSeries_OneRecorderPerRequest` gains the two 404 cases; `TestLegacySeriesLabelValuesMatchHEAD` gains an SPA load and asserts no `/` route; `TestPreMountRejection_RecordedUnderCoreScope` renamed `TestPreMountRejection_RecordedByStage2`.
- VER11-3 (R11-3) — §4.8 "The operations component", §8.1 S5 row, §12 `TestS5_OperationsComponentStaysBooks`: no `/api/v1/operations*` route is a core alias; the test asserts the generated route count under the prefix.
- VER11 note (R11-5) — §4.2 stage 2 (a): `http_server_response_body_size` counts uncompressed bytes from S2 r2 (gzip leaves gin), an observation change named for 09 §9.2.1 row 12.

Final verification:

- FIN12-7 — §3.5 `InstrumentScope` gains `SchemaURL`; §4.2 stage 2 (b): the exact identity the SDK shares on, explicit otelgin duration boundaries, `server.port` by otelgin's `requiredHTTPPort` rule, and the load-bearing scope version and typed values; §12 `TestNoRouteSeriesSharedNoDuplicate` asserts them.
- FIN12-8 — §12: `TestNoRouteSeriesSharedNoDuplicate` runs from S2 r1.

Plan-phase consistency:

- PC-15 — §4.6: the `all=true` admin list is `GET /api/core/v1/api-keys` (05-authn §4.13), selected only for `core:users.manage`; §12 `TestAlias_ListAllKeysNeedsUsersManage`.
- PC-16 — §8.1 and §12: the Basic-password rollback cell uses 05-config §1.12.2's names, `TestRollback_BasicGateNeverOpenOnPrev` and `TestRollback_BasicGateClearedStaysClosed`; `TestBasicGate_PasswordReadInPlaceAcrossRollback` retired.

- D10–D12 revision (2026-11-04): aliases and retirement deleted: `Alias`, `LegacyPath`, `Gone`, `Route.Deprecated`, `Deps.Compat`, the compat import, `ErrAliasTarget`, `Problem.Successor`, `legacy_field_unsupported`, deprecation headers and the compat counter (§1, §3, §3.1, §3.3, §3.4, §3.5, §6, §7, §11); §4.6 is versioning only (`oasdiff` gate kept, no aliases, ABS root routes a fixed contract mounted by books, D12 F3); otelgin co-recording, `r.Pattern` clearing, `HTTPMetricsScope`/`InstrumentScope` and `MetricViews` deleted: stage 2 records every request, gin's included, on core's `httpapi` meter (`Deps.MeterProvider`), and `MatchedPattern` stays for the post-match check only (§1.1, §3.5, §4.2 stage 2, §8.2); §4.8 collision rule kept, "The operations component" special case deleted; §4.9 is the `/api/v3` reservation (HEAD's `/api/v3/ops` deleted by the R2 tasks flip), `ErrAliasOutsideClaim` renamed `ErrOutsideClaim`; §8.1 re-keyed to R1 and the R2 flips (gin routes deleted as native routes replace them, no redirect routes, rollback = restore); §8.3 bridge over books' atom table (no `LegacyAtom`, `LegacyCheck`, `LegacyReach` or legacy stores); §10 without `BelowStored`, file-only legacy keys and rollback text (operator lock, more restrictive Basic-gate value wins, rename table); §11 semconv families for every request, duplicate and gone counters deleted; §12 alias, rollback, otelgin and HEAD-series tests deleted, `TestStage2_RecordsEveryRequest`, `TestBasicGate_MoreRestrictiveWins`, `TestMetricsScrape_MigratedReach`, `TestOpenAPI_BreakingGate`, `TestCoreRoutesReplaceGin` added; stage references S0–S10/UI-0 re-keyed to R0–R3. Consistency pass: flips labelled F3–F9 and D12's answers cited as D12 (a)–(d); the OIDC callback keeps HEAD's path on the new `Router.CoreFixedGroup` under the one-entry `CoreExactPatterns()` exception of §4.9; `core.http.basic_gate.enabled` declared `FailClosed`; the R1 factory reset is offline only; F5/F6 route deletions split as 05-tasks has them; `BasicExempt` moves to `/health` at F8 and the golden is renamed `TestBasicGate_ExemptSetGolden`; the reach oracle is `TestIdentityCutover_ReachOracle`; the rename table is 05-config §1.12.3. Final consistency sweep: §8.1's R1 row delivers the listener and perimeter values through `app.Options.R1Values` at start step 7 (no CORS origins, which HEAD cannot configure), the F4 row deletes the gin settings handlers for `k` and `s` keys while `/api/v1/config` keeps class-`i` fields until F7, and §3.5 names `auth.Options.OIDCCallbackBase` as the `CoreFixedGroup` base.

- Round 13 (2026-11-04), by round-13 ruling: R1 (F2 row: foundation PRs dark, the full scope catalog and the health registry's routing from F2; IMPL-4, IMPL-12), R2/CONFORM-1 (rollback reinstalls the pre-freeze release), R12/IMPL-6 (F6 rewrites `eventSourceManager`), R13/IMPL-8 (reach oracle over the R0-written corpus plus its labelled overlay), R14/XFILE-1 and CONFORM-11 (renames at R1, F5, F7; F8 relabelled; §11 R1 release-note line), R15/XFILE-2 (Activity page off `ops.ts` at F5; `opsgen`, `ops.ts` and `internal/operations/state` deleted together in R3; §7, §8.4), R16/XFILE-5 (§8.3 cites 05-rbac §6.1's "Route requirement" column), R17/XFILE-6 (07 §7.19 steps 1–2 in R1), XFILE-11 (`app.New(ctx, opts, books.Module())`), XFILE-12 (d) (in-handler sites found by the archtest, no snapshot list), R18/SEC-1 (ABS row uses books' `auth.Compat`), R19/SEC-2 (F4 row and §12: core's start keeps HEAD's deep-link hand-off; HEAD's start path deleted, release-note line; reversed in round 14), R20/SEC-4 (playlist writes stay `AnyAuthenticated`; §8.1, §8.2), R22/SEC-7 (`/health`'s unauthenticated token set bounded by 05-health §5H.3.3), SEC-12, SEC-13 (oracle baseline R0), CONFORM-7/XFILE-8 (compose cell of `TestBasicGate_MoreRestrictiveWins`), CONFORM-9 (§4.1, §4.6: no second major until a second app consumes the API), CONFORM-12 (the callback path is unchanged because cheaper, not a contract), CONFORM-14 (`TestBasicGate_EmptyPasswordRefuses` is a property test).
- Round 14 (2026-11-05): V22 (HEAD's OAuth start path `/api/v1/auth/oauth/{provider}/start` kept as the second `CoreExactPatterns()` entry, served by core's start handler with the `AppHandoff` rules; the "deleted with no alias" text and its release-note line removed; `TestCoreExactPatterns_OneEntry` renamed `TestCoreExactPatterns_TwoEntries`, with a cell for the kept start path; §3, §3.5, §4.6, §4.9, §8.1 F4 row, §12), V15 (§8.3: the route requirement is `AllOf` all of the row's native scopes, always), V20 (§8.1 and §12: the reach-oracle summary cites 05-rbac §12's reviewed differences), V21 (§8.3 ABS row: `PrincipalFor` refuses `PasswordResetRequired` and goes through the throttle), V23 (the round-13 history entry names its IDs as round-13 rulings).
