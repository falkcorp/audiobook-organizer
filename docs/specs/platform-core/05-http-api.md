<!-- file: docs/specs/platform-core/05-http-api.md -->
<!-- version: 1.0.0 -->
<!-- guid: fcd387ab-7a8e-49e2-a4b4-a6901aaee1e3 -->
<!-- last-edited: 2026-11-04 -->

# 05 — HTTP API framework (`core/httpapi`)

Evidence commit `748fd787a`. Raw inputs: [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §8,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Names, layers and rules R1–R8 are from [03-architecture](03-architecture.md); identity is in
[05-authn](05-authn.md), permissions and audit in [05-rbac](05-rbac.md), streams in
[05-realtime](05-realtime.md).

## 1. Purpose & scope

`core/httpapi` is the only way any module, the kernel, or the optional *arr facade puts a route on
the wire. It owns:

- the router (standard-library `net/http.ServeMux` with Go 1.22+ method/wildcard patterns — decision N7, confirmed in §1.1);
- typed route registration with generics and a **mandatory permission** (R6, fail-closed at registration);
- the **core-owned middleware chain**, handed to each module as an already-authenticated `Group` (fixes G8);
- the error envelope, pagination, idempotency, request IDs, deprecation/versioning headers;
- OpenAPI 3.1 generation from registrations and the TypeScript client pipeline;
- the **legacy mount** that lets ABO's gin engine (497 routes, +75 ABS) run as an `http.Handler`
  under the `books` module while route groups move one at a time, and the prefix-claim table that
  resolves path collisions (`/api/v3/ops` vs an *arr facade's `/api/v3`, §4.9).

Out of scope: listener/TLS/HTTP-3 configuration beyond the handler (kept in `app` options,
[06-app-sdk](06-app-sdk.md)); SPA serving (`core/ui`, [07-ui-shell](07-ui-shell.md)); the *arr
resource mapping ([08-arr-facade](08-arr-facade.md)); health/metrics endpoints' payloads
([05-health](05-health.md), [05-metrics](05-metrics.md)).

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
| `NoRoute` | 16 | `ServeMux` fallback pattern `/` and `/api/` (§4.8) |
| route groups + per-route middleware chains | everywhere | `Group` + `Route` metadata (§3.1) |

The only gin-specific *behaviour* with no stdlib default is struct-tag validation, which the
design replaces deliberately (explicit `Validate`, visible in OpenAPI). One gin behaviour the
repo documents as a hazard disappears: gin binds a route's chain at registration, so a later
`router.Use` silently misses earlier routes (`internal/server/server_lifecycle.go:1712-1717`;
the `Alt-Svc` middleware added at `:957-960` after `setupRoutes` is the live instance,
`raw/09` §2.2 note c). In `httpapi` the chain is assembled once, after all registrations, by
the kernel (§5), so ordering bugs of that kind cannot exist. **No objection to N7.**

Third-party libraries considered and rejected for core: `huma/v2` v2.40.0 (typed handlers + OpenAPI
3.1 on top of `ServeMux`): its `huma.API`/`huma.Context` types would become part of every module's
signatures and therefore of the SDK stability gate (§12), and its security model is advisory
metadata, not a fail-closed permission requirement. `kin-openapi` v0.152.0, `swaggest/openapi-go`
v0.2.61: document models only; we generate a small 3.1 document ourselves. Accepted:
`github.com/google/jsonschema-go` v0.4.3 (`jsonschema.For[T]` emits JSON Schema 2020-12, the
dialect OpenAPI 3.1 uses) for request/response schemas, and
`go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` v0.72.0 replacing `otelgin`
(same contrib release train as the current `otelgin v0.72.0`, `go.mod:31`). Versions checked against
the Go module proxy on 2026-11-04.

## 2. Current state at HEAD

| Topic | Today | Anchor |
|---|---|---|
| Router | `gin.New()`, trusted proxies disabled | `internal/server/server.go:638,689` |
| Registration | imperative, `setupRoutes` (`:1710`) + `wireHandlers` + 16 `wire_*_routes.go` + ~100 leftover lines; no route metadata; permissions exist only as closures in chains | `internal/server/server_lifecycle.go:1710-2135`, `internal/server/wire_handlers.go:42`, `raw/09` §2.1, §8.1 |
| Totals | 497 routes default build, +75 ABS; 15 public; 36 authenticated with no route permission; 45 `gone()` stubs | `raw/09` §1 |
| Permission helper | `s.perm(p)` returns a pass-through when `EnableAuth=false`, else `RequirePermission` | `internal/server/server_lifecycle.go:1693-1698` |
| Chain | peer allowlist (outside gin) → logger → recovery → security headers → CORS → basic auth → raw-response capture → gzip → tracing → [7 early routes] → `/api/*` 301 redirect → group: rate limit → body limit → CF Access → `RequireAuth` → per-route perm/admin/credGuard/owner | `raw/09` §2.2; `internal/server/server.go:695-731`; `internal/server/server_lifecycle.go:1746-1875` |
| Early routes outside the group chain | `/metrics`, `/health`, `/api/health`, `/api/v1/health`, `POST /api/v1/csp-report`, `/api/events`, `/auth/temp-login`, each with a hand-built auth chain | `internal/server/server_lifecycle.go:1731-1788` |
| Unauthenticated module mount (G8) | `s.router.Group("/api/v1/plugins")` — not under `api`/`protected`, so no rate limit, body limit, CF Access or `RequireAuth` | `internal/server/plugins_init.go:42`, `internal/plugin/router.go:11-26` |
| Versioning | `/api/v1/*` app API; `/api/v3/ops/*` (12 routes, own prefix); `/api/v1/operations/v2/*`; unversioned `/api/*` 301 → `/api/v1/*` with exclusions for v3, health, events, metrics and ABS-reserved paths | `internal/server/server_lifecycle.go:1791-1822,2127-2135`, `raw/09` §2.3 |
| Retirement | `gone()` 410 stubs keep auth+perm ordering, body `{"error":"gone","message":…}`, kept "one release" | `internal/server/gone.go:15-40` |
| Error shape | `httputil.ErrorResponse{error, code, status}` — a second shape for 410 | `internal/httputil/types.go:9-13`, `internal/httputil/respond.go:21-44` |
| Pagination | offset/limit, default 50, cap 1000; `ListResponse{items,count,limit,offset,total}` | `internal/httputil/parse.go:74-90`, `internal/httputil/types.go:23-29` |
| Request IDs, idempotency keys, Deprecation/Sunset headers | none (0 non-test files mention `X-Request-ID`, `Idempotency-Key`, `Deprecation`, `Sunset`) | grep at HEAD |
| CORS | same-origin or dev Vite origin only, credentials allowed | `internal/server/server_middleware.go:56-97` |
| Security headers | nosniff, SAMEORIGIN, referrer policy, CSP + reporting, HSTS on TLS | `internal/server/server_middleware.go:102-118` |
| Rate limit | per-IP token bucket, on by default (100 rpm, burst `max(rpm/5,10)`) | `internal/server/server_lifecycle.go:1829-1838`, `internal/config/config.go:2797,2802` |
| Body limit | JSON 1 MiB / upload 10 MiB via `http.MaxBytesReader` | `internal/server/middleware/request_size.go:77`, `internal/config/config.go:2798-2799` |
| Listener | `http.Server{Handler: peerGatedHandler, ReadHeaderTimeout, WriteTimeout 0 for SSE, IdleTimeout, MaxHeaderBytes 1 MiB}` | `internal/server/server_lifecycle.go:903-920,1078` |
| Wire contract | tygo for 2 packages (~860 TS lines), opsgen enums, golden JSON contract test; ~260 hand-written TS interfaces in an 8,747-line `api.ts`; **no OpenAPI** | `tygo.yaml:12-40`, `internal/wirecontract/contract_test.go:1-20`, `web/src/services/api.ts:24`, `raw/13` §8 |
| Route-recording guards (prior art) | `credRoute`/`adminRoute` record each guarded route; tests walk the full route list and fail on unclassified routes | `internal/server/credential_routes.go:23-75`, `internal/server/wire_admin_role.go:18-104` |

## 3. Go API

Package `github.com/falkcorp/mediacore/httpapi` (layer: `httpapi`, may import `auth`, `rbac`,
`audit`, `config`, `log`, `metrics`, `storage`). Implementation details in `core/internal/openapi`
and `core/internal/httpmw`.

### 3.1 Routes and groups

```go
package httpapi

import (
	"context"
	"iter"
	"net/http"
	"time"

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
	Idempotency Idempotency  // IdempotencyNone (default), IdempotencyKeyed, IdempotencyRequired
	Success     int          // success status; 0 = 200 (201 if Resp is Created[T], 204 if NoContent)
	Deprecated  *Deprecation // set on a route that is itself being retired
	Strict      bool         // reject unknown JSON fields (default true for native routes; see §3.4)
}

// Guards are identity requirements beyond the permission.
type Guards struct {
	Interactive bool // refuse API keys and compat tokens (today's credGuard, auth.Method.MayChangeCredentials)
	Owner       bool // require an owner-assurance principal (today's RequireOwner; 05-authn §4.8)
}

type RateClass uint8

const (
	RateDefault RateClass = iota
	RateAuth              // login/setup/bootstrap/invite: tighter per-source buckets (05-authn §4.9)
	RateExpensive         // search, exports
	RateExempt            // health probes only; rejected for non-Public routes
)

type Idempotency uint8

const (
	IdempotencyNone Idempotency = iota
	IdempotencyKeyed    // honour Idempotency-Key when sent
	IdempotencyRequired // 428 problem "idempotency_key_required" when absent
)

// Deprecation drives the Deprecation (RFC 9745), Sunset (RFC 8594) and
// Link rel="successor-version" headers.
type Deprecation struct {
	Since     time.Time // when it was deprecated (Deprecation: @<unix>)
	Sunset    time.Time // when it starts answering 410 gone
	Successor string    // "GET /api/books/v1/books/{id}"
}

// Group is a mount point whose whole core chain is already applied. A module
// receives exactly one, rooted at /api/<moduleID>/v1, from module.Registrar.HTTP().
// There is no API that yields an unauthenticated or chain-less group (G8).
type Group struct{ /* unexported: owner module, base, version, registry */ }

func (g *Group) Base() string                // "/api/books/v1"
func (g *Group) Module() string              // "books"
func (g *Group) Sub(prefix string) *Group    // "/api/books/v1/series"; same chain
func (g *Group) Version(major int) *Group    // "/api/books/v2"; major >= 2

// Handle registers a typed JSON operation. It never panics: errors are collected
// and returned by the kernel from app.New (fail-closed boot, §5).
func Handle[Req, Resp any](g *Group, rt Route, fn func(ctx context.Context, in *In[Req]) (Resp, error))

// HandleRaw registers an operation that writes its own response (file download,
// range serving, SSE). Perm, guards, rate and body limits still apply. The
// OpenAPI entry is described by the provided RawDoc.
func HandleRaw(g *Group, rt Route, doc RawDoc, h http.Handler)

// Alias registers a legacy path for an already-registered native route
// (same handler, same permission) and stamps deprecation headers (§4.6).
func Alias(g *Group, name string, legacy LegacyPath)

type LegacyPath struct {
	Pattern     string    // full ServeMux pattern, e.g. "GET /api/v1/books/{id}"
	Since       time.Time
	Sunset      time.Time         // release date of (alias release + 2 minor releases), §4.6
	LegacyError bool              // render httputil-style {error,code,status} instead of problem+json
	Shape       func(v any) any   // optional: adapt a native response body to the legacy JSON shape
}

// Gone registers a retired route: auth and permission still run first, then
// 410 with problem code "gone" (and legacy {"error":"gone","message":…} on legacy paths).
func Gone(g *Group, rt Route, successor string)

// Sugar used by 06-app-sdk's examples and module.Registrar.Route: a deferred
// registration built from method helpers. Exactly equivalent to Handle.
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

### 3.2 Typed input

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

// Page is the cursor page every list operation returns (§4.4).
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      *int64 `json:"total,omitempty"` // only when Route declares it cheap
}

// PageParams is embedded in list request types.
type PageParams struct {
	Limit  int    `query:"limit"`  // default 50, max 1000 (today's cap, internal/httputil/parse.go:74-90)
	Cursor string `query:"cursor"` // opaque
}

// Cursor encodes/decodes the opaque cursor; the payload is HMAC-signed with a
// per-App key so clients cannot forge storage positions.
func EncodeCursor(c Cursorer, pos []byte, filterHash uint64) string
func DecodeCursor(c Cursorer, s string, filterHash uint64) (pos []byte, err error)

// Paginate drives an iter.Seq2 from storage into a Page, stopping at limit+1.
func Paginate[T any](ctx context.Context, p PageParams, seq iter.Seq2[Positioned[T], error], enc Cursorer, filterHash uint64) (Page[T], error)

type Positioned[T any] struct {
	Pos  []byte // storage key or sort key
	Item T
}
```

### 3.3 Errors (problem+json)

```go
// Problem is the native error body, RFC 9457 application/problem+json with
// extension members. Every native route and every core middleware emits it.
type Problem struct {
	Type      string       `json:"type"`             // "https://mediacore.dev/problems/<code>" stable URI, or "about:blank"
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"` // request path
	Code      string       `json:"code"`               // machine code, snake_case, stable: "not_found", "permission_denied", "gone", ...
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`   // validation details
	Required  string       `json:"required_permission,omitempty"` // on 403, the missing scope
	Successor string       `json:"successor,omitempty"`           // on 410
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
func Forbidden(scope rbac.Permission) *Error
func Unavailable(detail string, retryAfter time.Duration) *Error

// Sentinel causes that modules may wrap; core maps them without a constructor.
var (
	ErrNotFound      = errors.New("httpapi: not found")       // 404 not_found
	ErrConflict      = errors.New("httpapi: conflict")        // 409 conflict
	ErrPrecondition  = errors.New("httpapi: precondition")    // 412 precondition_failed (If-Match)
	ErrUnavailable   = errors.New("httpapi: unavailable")     // 503 unavailable
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
	ErrPublicNotAllowed   = errors.New("httpapi: Public route not on the module's declared public list")
)
```

### 3.4 Validation

```go
// Validator is implemented by request types that need more than JSON decoding.
// Return Invalid(...) or errors.Join of *FieldError-carrying errors.
type Validator interface{ Validate() error }
```

Decoding rules: `Content-Type` must be `application/json` (415 otherwise) for `body:"json"`; body is
read through `http.MaxBytesReader(MaxBody)` (413 `body_too_large`); `Strict` routes use
`Decoder.DisallowUnknownFields` (400 `unknown_field`) — default true for native routes, false for
aliases so legacy clients that send extra fields keep working.

### 3.5 Kernel-facing router

```go
// Router is built by the kernel only (kernel/ imports it; modules never see it).
type Router struct{ /* mux, registry, chain, claims */ }

func NewRouter(opts Options, deps Deps) *Router

type Options struct {
	CoreGroupBase string // "/api/core/v1"
	TrustedProxies []netip.Prefix
	ClientIPHeader string // "" = RemoteAddr only; e.g. "X-Forwarded-For" or a CDN header, believed only from TrustedProxies
	CORS           CORSOptions
	Limits         LimitOptions
	OpenAPI        OpenAPIOptions
}

type Deps struct {
	Auth    *auth.Service
	RBAC    *rbac.Service
	Audit   audit.Logger
	Store   storage.Keyspace // c/http/ : idempotency records, cursor key
	Log     *slog.Logger
	Metrics *metrics.Registry
	Tracer  trace.TracerProvider
}

func (r *Router) ModuleGroup(moduleID string) *Group        // /api/<id>/v1
func (r *Router) CoreGroup() *Group                         // /api/core/v1
func (r *Router) FacadeGroup(owner, base string) *Group     // *arr facade, §4.9
func (r *Router) Infra(pattern string, perm rbac.Requirement, h http.Handler) // /healthz, /readyz, /metrics
func (r *Router) MountLegacy(m LegacyMount) error           // §8
func (r *Router) Claim(c PrefixClaim) error                 // §4.9
func (r *Router) Routes() iter.Seq[RouteInfo]               // archtest, OpenAPI, docs
func (r *Router) Build() (http.Handler, error)              // registers into a fresh ServeMux; errors.Join of all registration errors
func (r *Router) OpenAPI(module string) (*openapi.Document, error)

type RouteInfo struct {
	Owner   string // module ID or "core"
	Method  string
	Pattern string
	Name    string
	Perm    rbac.Requirement
	Guards  Guards
	Kind    RouteKind // Native, Alias, Gone, Raw, Legacy, Facade, Infra
	Sunset  time.Time
}
```

## 4. Behaviour

### 4.1 Path layout

| Prefix | Owner | Notes |
|---|---|---|
| `/api/core/v1/…` | core | auth, users, roles, api-keys, sessions, audit, tasks, settings, system, events (SSE), openapi |
| `/api/<moduleID>/v1/…` | module | e.g. `/api/books/v1/books/{id}` (03 §3.6) |
| `/api/<moduleID>/v{N}/…` | module | a new major only for breaking changes; old major kept ≥ 2 minor releases with deprecation headers |
| `/healthz`, `/readyz`, `/metrics` | core infra | payloads in 05-health / 05-metrics |
| `/api/v1/…`, `/api/v3/ops/…`, `/api/events`, `/health`, `/api/health`, `/auth/temp-login`, ABS unversioned paths | `books` (legacy/compat) | §8; aliases with Deprecation/Sunset, then 410 |
| `/api/v3/…` (or `/<moduleID>/api/v3/…`) | *arr facade owner (`tv`/`movies`) | §4.9 |
| `/` | `core/ui` SPA fallback | least-specific pattern |

Reserved module IDs (cannot be used because they would collide with this table): `core`, `api`,
`events`, `health`, `metrics`, `auth`, `ui`, and any `v[0-9]+`. Proposed as an amendment to 03 §3.6
(see "Objections to 03").

### 4.2 Middleware chain (core-owned, fixed order)

Applied by `Router.Build` around every route; a module cannot reorder, skip or add to it (a
module needing per-route behaviour uses `Route` fields). Outermost first:

| # | Stage | Scope | Replaces |
|---|---|---|---|
| 0 | Peer allowlist (`net/netip` prefixes; loopback always allowed) | listener `http.Handler`, before routing | `internal/server/peer_allowlist.go:237` |
| 1 | Request ID: generate ULID, keep a well-formed inbound `X-Request-ID` (`^[A-Za-z0-9._-]{1,64}$`) as `client_request_id`; echo `X-Request-ID` | all | new |
| 2 | Tracing (`otelhttp`, span name = `r.Pattern`) | all | `tracingMiddleware` incl. invite-path redaction, `internal/server/server.go:1672`, `internal/server/middleware/trace_redact.go` |
| 3 | Recovery: panic → problem 500 `internal`, stack logged redacted, span marked error, `mediacore_http_panics_total` | all | `RedactingRecovery`, `internal/server/server.go:704` |
| 4 | Access log (`slog`, query-secret redaction, skip streams) | all | `gin.LoggerWithConfig`, `internal/server/server.go:695` |
| 5 | Security headers (nosniff, frame, referrer, permissions policy; CSP owned by `core/ui`; HSTS only when `r.TLS != nil` or the request came through a trusted proxy that says https) | all | `internal/server/server_middleware.go:102-118` |
| 6 | CORS (explicit allowlist, same-origin always) | all | `internal/server/server_middleware.go:56-97` |
| 7 | Optional perimeter gate (HTTP Basic, off by default; exempts infra probes and compat mounts that declare own auth) | all | `internal/server/middleware/basicauth.go:24` |
| 8 | Compression (gzip; skipped for `HandleRaw` streams and `/metrics`) | all | `compressionMiddleware`, `internal/server/server.go:725` |
| 9 | Per-client rate limit (token bucket per client address; class from `Route.RateClass`) | routes | `internal/server/middleware/ratelimit.go:96` |
| 10 | Body limit (`MaxBody`) | routes | `internal/server/middleware/request_size.go:77` |
| 11 | Authenticate: run `auth.Service` authenticators, bind `*auth.Principal` or anonymous; **does not reject** | routes (not compat mounts that declare own auth) | `RequireAuth` step 3–6 + `cfMW` (`raw/10` §2.2) |
| 12 | Authorize: anonymous on non-Public → 401; `Guards`; `Perm` → 403 problem with `required_permission`; denial → `audit` (coalesced) | routes | `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner` |
| 13 | Per-principal rate limit (bucket per user, separate bucket for API keys) | authenticated routes | new |
| 14 | Idempotency (§4.5) | routes declaring it | new |
| 15 | Handler (`Handle` decode → validate → fn → encode) | route | — |

Stages 11–12 being **inside** the module's `Group` is what fixes G8: there is no group without
them. Public routes (`rbac.Public`) still pass 11 (so a credential, if sent, is bound and audited)
but skip the 401 in 12.

### 4.3 Response and error rules

- Success: `Content-Type: application/json`; bodies are the `Resp` value (no `{data:…}` wrapper).
  `Created[T]` → 201 + `Location`; `NoContent` → 204.
- Errors: always `application/problem+json` with the `Problem` shape (§3.3) on native routes;
  legacy aliases with `LegacyError` render `{error, code, status}` exactly like
  `internal/httputil/types.go:9-13`.
- 401 vs 403 split kept from `RespondPermissionDenied` (`internal/server/middleware/auth.go:510-519`):
  no principal → 401 `authentication_required`; principal without scope → 403 `permission_denied`
  naming the scope.
- 404/405 for unmatched paths: the router wraps `ServeMux` and, when `mux.Handler(r)` returns an
  empty pattern, answers problem 404 `not_found` or 405 `method_not_allowed` with the `Allow`
  header the mux computed (captured from the mux's own response, body discarded) — never the mux's
  plain-text body, and never the SPA HTML for `/api/` paths (today's `spa_fallback.go:100` rule kept).
- ETags: `GET` of a versioned entity returns `ETag: "<version>"`; `PATCH`/`PUT`/`DELETE` honour
  `If-Match` (412 `precondition_failed`). Storage rows already carry `Version` (`internal/database/store.go:744,976`).

### 4.4 Pagination

Cursor pagination is the default for every list: `?limit=` (default 50, max 1000 — the current cap,
`internal/httputil/parse.go:74-90`) and `?cursor=`. The cursor is
`base64url(pos || filterHash || HMAC-SHA256(key, …)[:16])`; a cursor replayed with different filters
or forged → 400 `invalid_cursor`. Pebble scans are ordered (`storage` `iter.Seq2`, 05-storage), so
`Paginate` reads `limit+1` items and needs no `COUNT`. `total` is returned only by routes that keep
a counter (declared in `RawDoc`/route docs). Offset pagination exists **only** on legacy aliases.

### 4.5 Idempotency

`Idempotency-Key` (IETF httpapi draft) on routes with `IdempotencyKeyed|Required`:

1. Key grammar `^[A-Za-z0-9_-]{8,128}$`; record key `c/http/idem/<principalID>/<sha256(key)>`.
2. First request: write `{state: in_flight, reqHash: sha256(method, pattern, canonical body), expires}`
   in a storage batch, run the handler, then store `{state: done, status, headers subset, body ≤ 64 KiB}`.
3. Same key + same `reqHash` + `done` → replay stored response with `Idempotent-Replayed: true`.
4. Same key + different `reqHash` → 422 `idempotency_key_reused`.
5. Same key while `in_flight` → 409 `idempotency_in_progress` + `Retry-After: 1`.
6. A response body > 64 KiB is not stored; the replay answers 409 `idempotency_result_unavailable`.
7. Expiry 24 h; pruned by core task `core.http.idempotency.prune` (hourly, `tasks.Parallelism` = sequential with reason "single keyspace range delete").

Task-start routes (`POST /api/core/v1/tasks`) declare `IdempotencyKeyed` so a retried click does not
start a run twice (today's guard is per-definition "already running" checks in `operations_v2.go`).

### 4.6 Versioning, aliases and retirement

- Within a major: additive only (new optional fields, new routes, new enum values only where the
  schema marks the enum `x-extensible-enum`). CI gate: `oasdiff` v1.35.0 `breaking` against the
  OpenAPI document of the last release tag (committed as `api/openapi/<module>.json`, §7); a breaking
  diff fails `make ci` unless the route is in a new major.
- Legacy aliases (`Alias`) send on every response:
  `Deprecation: @<since unix>`, `Sunset: <HTTP-date>`, `Link: <successor>; rel="successor-version"`,
  increment `mediacore_http_deprecated_requests_total{module,alias}`, and log once per
  (alias, principal) per hour at `INFO`.
- **Window: two minor releases** after the release that ships the replacement (02 §2.5 Q5). The
  sunset date is computed from the release plan and checked into the alias registration; at
  `Sunset` the alias turns into a `Gone` stub automatically (the router compares `time.Now()` at
  boot), and the stub is deleted one minor release later — the existing `gone()` convention
  (`internal/server/gone.go:20-32`) kept, with its auth-first ordering.
- 410 body on native paths: problem `code:"gone"`, `successor`; on legacy paths the exact legacy body
  `{"error":"gone","message":"this endpoint has been removed; use … instead"}` so existing tests
  (`internal/server/wire_entities_gone_test.go:19-20`) and clients stay valid.

### 4.7 Request IDs and correlation

Request ID (stage 1) is put in the context (`httpapi.RequestID(ctx)`), as `slog` attribute
`request_id` by the core log handler (05-logging), as span attribute `http.request_id`, in every
`Problem`, and in every audit record (05-rbac §7). Background tasks started by a request record
the request ID as `origin_request_id` (05-tasks).

### 4.8 `ServeMux` specifics the router must handle

| Behaviour of `net/http.ServeMux` (Go 1.22+) | Handling |
|---|---|
| Registering two conflicting patterns panics | `Build` registers inside a function with `recover()`, converts the panic text (which names both patterns) into `ErrPatternConflict`; duplicates are detected earlier by our own map for a clearer message |
| Subtree pattern `/x/` redirects `/x` → `/x/` | native routes use exact patterns (`{$}` where needed); subtree patterns are allowed only for legacy mounts and the SPA |
| More specific pattern wins across methods/paths | the basis of route-by-route migration (§8.1) and of prefix claims (§4.9) |
| `{name...}` wildcard | translation of gin `*name`; gin `:name` → `{name}` |
| 405 + `Allow` generated automatically | wrapped into problem+json (§4.3) |

### 4.9 Prefix claims and the `/api/v3` collision

`Router.Claim(PrefixClaim{Prefix, Owner, Kind})` records every subtree an owner occupies. `Build`
fails with `ErrPrefixCollision` when two owners claim overlapping prefixes, unless the more-specific
claim is a `Legacy` claim and the less-specific one declares `YieldsTo` it.

The concrete collision: ABO's legacy `/api/v3/ops/*` (12 routes, `internal/server/server_lifecycle.go:2127-2135`)
and an *arr-v3 facade, whose clients call `<base>/api/v3/…` (`raw/16` §5.1). Resolution:

1. In **separate binaries** (the priority case) there is no collision: the legacy `/api/v3/ops`
   alias belongs to the `books` module of ABO, and ABO ships no *arr-v3 facade (the Readarr API
   was `/api/v1`, and Readarr is retired, `raw/16` §6).
2. The facade is mounted through `Router.FacadeGroup(owner, base)` with `base` from config
   `modules.<id>.arrfacade.url_base` (default `""` → `/api/v3`). *arr clients support a URL base
   (Prowlarr, Seerr and Bazarr all store a base URL, `raw/16` §5.1), so in a **multi-app binary**
   the kernel **requires** a non-empty URL base for every facade (`/tv`, `/movies` → `/tv/api/v3`),
   and boot fails if two facades or a facade and a legacy claim would share `/api/v3/`.
3. After the `/api/v3/ops` alias reaches its sunset (two minor releases after `core/tasks` routes
   ship), the legacy claim is removed and the constraint in (2) relaxes to "two facades need distinct
   bases".
4. The legacy unversioned redirect (`/api/*` → `/api/v1/*`, `server_lifecycle.go:1791-1822`) is
   re-implemented as the least-specific `/api/` handler of the legacy mount, so every claimed prefix
   (`/api/core/`, `/api/books/`, a facade) is matched before it by `ServeMux` specificity and can
   never be redirected into `/api/v1`.

## 5. Lifecycle

1. `app.New` builds `httpapi.Router` after `auth`, `rbac`, `audit` exist (R4 order).
2. Core registers its own routes on `CoreGroup()`; each module's `Register(r module.Registrar)`
   receives `r.HTTP() *httpapi.Group`. Registration only **records**; nothing touches the mux.
3. Kernel calls `Router.Build()`: validates every route (permission present and in catalog, scope
   namespace rule from 05-rbac §4.9, Public allowlist, binding tags, schema generation), validates
   prefix claims, registers into a fresh `ServeMux`, assembles the chain, and returns
   `errors.Join` of every problem. Any error aborts boot **before listeners open** (03 §3.7).
4. `App.Run` serves the handler on configured listeners. Shutdown: `http.Server.Shutdown(ctx)` with
   the app's drain deadline; SSE handlers end on `Server.RegisterOnShutdown` + context cancel
   (05-realtime); idempotency records left `in_flight` expire normally.
5. Routes are immutable after `Build` (restart-bound enablement, 03 §3.7).

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Route without `Perm` | boot error `ErrNoPermission` | closed | R6 |
| Route `Perm` scope unknown / foreign namespace | boot error | closed | prevents typos and cross-module escalation (05-rbac §4.9) |
| Pattern conflict / duplicate / prefix collision | boot error | closed | silent shadowing is worse than not starting |
| Request ID generation fails (crypto/rand) | fall back to monotonic counter id `local-<n>` | open | correlation only, not security |
| Tracing exporter down | request proceeds | open | observability must not take the API down |
| Panic in handler | problem 500, connection kept | closed for the request | contain blast radius |
| Peer not in allowlist | TCP-level 403 problem, no routing | closed | network perimeter |
| Rate limit exceeded | 429 `rate_limited`, `Retry-After`, `RateLimit-Policy`/`RateLimit` headers | closed | abuse control |
| Rate limiter state store full (LRU at cap) | evict least-recently-used non-pinned bucket | open | memory bound beats perfect limiting |
| Body over limit | 413 `body_too_large` | closed | memory |
| Malformed JSON / unknown field / validation | 400 / 400 / 422 with `errors[]` | closed | |
| Authentication store read error | 503 `auth_unavailable` (not 401) | closed | never treat "can't check" as "anonymous ok"; today returns 500 (`middleware/auth.go:163-167`) |
| No principal on non-Public route | 401 | closed | |
| Missing scope | 403 + audit denial | closed | |
| Audit write for a denial fails | request still 403; `mediacore_audit_dropped_total` | open (audit) / closed (request) | the denial is enforced regardless |
| Idempotency store error | 503 `idempotency_unavailable` | closed | replaying or double-running a non-idempotent op is the thing being prevented |
| Cursor invalid | 400 `invalid_cursor` | closed | |
| Alias after sunset | 410 `gone` | closed | contract |
| Response encode error after headers sent | log + abort connection | — | nothing else possible |

## 7. OpenAPI 3.1 and the TypeScript client

**Generation.** `Router.OpenAPI(module)` produces one document per owner (core, each module,
facade excluded — the facade mirrors a foreign API) from `RouteInfo` plus reflected `Req`/`Resp`
types:

- `paths` from patterns; parameters from `path`/`query`/`header` tags; request body from the
  `body:"json"` field; responses: success schema, and `default` → `Problem`.
- Schemas: `jsonschema.For[T]` with `TypeSchemas` overrides (`time.Time` → `string/date-time`,
  `json.RawMessage` → `{}`, `rbac.Permission` → pattern string, `Page[T]` → generic expansion
  `Page_<T>`); component names `<module>.<GoType>`; two Go types mapping to one name → `ErrSchema`.
- Extensions: `x-mediacore-permission` (scope or `public`/`authenticated`), `x-mediacore-guards`,
  `x-mediacore-sunset`; `deprecated: true` for aliases and deprecated routes.
- Security schemes: `sessionCookie` (apiKey in cookie), `bearer` (session or API key), and for the
  facade only `apiKeyHeader` (`X-Api-Key`).
- Served at `GET /api/core/v1/openapi/{owner}.json` (perm `rbac.AnyAuthenticated`;
  `core.http.openapi.public=true` makes it Public) and emitted at build time by the app binary's
  `openapi` subcommand (06-app-sdk) into `api/openapi/<owner>.json`, committed. A freshness gate
  (`make openapi-check`, modelled on `scripts/ci/check_generated_types.sh:1-22`) and the `oasdiff`
  breaking gate (§4.6) run in `make ci`.
- Legacy gin routes appear in a separate `api/openapi/books-legacy.json` with method, path,
  permission and `deprecated: true` but untyped bodies (they have no Go types to reflect), generated
  from the legacy route manifest (§8.2).

**TypeScript.** Checked with `npm view` on 2026-11-04: `openapi-typescript` **7.13.0**,
`openapi-fetch` **0.17.0** (alternatives seen: `@hey-api/openapi-ts` 0.99.0, `orval` 8.46.0).
Chosen: `openapi-typescript` (types only, OpenAPI 3.1 support, zero runtime) +
`openapi-fetch` (typed `fetch` wrapper with middleware), because the app already uses native
`fetch` through `apiFetch` (`web/src/utils/apiFetch.ts:15-70`, `raw/13` §1, §8) and has no
react-query/axios runtime to integrate with. `openapi-fetch` is pre-1.0: pin exact versions, and the
wrapper below isolates apps from its API so a swap is one package.

- `@mediacore/api-client` = generated `paths` for core + `createCoreClient()` built on
  `openapi-fetch` with middleware ported from `apiFetch`: `credentials: 'include'`, timeout
  (`ApiTimeoutError`), SSO bounce detection (`ApiAuthRedirectError`), `X-Request-ID` surfacing,
  `Problem` typed as the error type of every call.
- Each app generates its own package the same way (`@abo/books-api-client` from
  `api/openapi/books.json`); generation is `make generate-api-client`, freshness-gated like
  `types-check`.
- tygo (`tygo.yaml`) and opsgen stay until the routes whose types they cover are native; then they are
  deleted. The golden contract test (`internal/wirecontract/contract_test.go`) is generalized:
  `httpapi`'s `coretest.ContractGoldens(t, router)` marshals each route's declared examples and
  **validates them against the generated schema** (jsonschema-go `Resolve`+`Validate`), and the TS
  side assigns the same goldens to generated types, as `web/src/types/contract/contract.test.ts` does now.

## 8. Migration from ABO (mounting the gin router under `books`)

### 8.1 Stages

| Stage | What changes | Rollback |
|---|---|---|
| H0 | `core/httpapi` exists; ABO's `main` builds `app.New(opts, books.Module())`; `books` calls `MountLegacy` with the **whole** gin engine; core chain stages 0–10 run outside gin; inside gin, `RequireAuth` is replaced by a bridge (`legacyauth.FromCorePrincipal`) and duplicate global middleware (logger, recovery, headers, CORS, gzip, tracing) is removed from the gin engine | revert to the old `main`; storage untouched |
| H1 | Core routes (auth, users, roles, keys, sessions, tasks, events, settings, system) go native under `/api/core/v1`; legacy paths become `Alias`es of them (more specific than the gin subtree, so the mux sends them to native code); the corresponding gin routes are deleted in the same PR | alias removal re-exposes gin route |
| H2..Hn | `books` route groups move one at a time to `/api/books/v1` with aliases, largest groups last (`/api/v1/audiobooks` 106 routes, `raw/09` §1.1) | per group |
| Hend | gin engine empty → `MountLegacy` removed; gin, `gin-contrib/gzip`, `otelgin`, go-playground validator leave `go.mod` (`go.mod:12-15,31`) | — |

### 8.2 `MountLegacy` contract

```go
type LegacyMount struct {
	Owner    string        // "books"
	Patterns []string      // subtree/exact patterns: "/api/v1/", "/api/v3/ops/", "GET /auth/temp-login", ABS paths
	Handler  http.Handler  // *gin.Engine
	Auth     LegacyAuth    // LegacyAuthCore (stage 11 binds a Principal; gin bridge enforces) | LegacyAuthOwn (compat surface, e.g. ABS, authenticates itself and resolves to core User, 05-authn §4.10)
	Routes   iter.Seq[LegacyRoute] // manifest: every gin route with its permission
	Redirects []LegacyRedirect     // the /api/* -> /api/v1/* rule and its exclusions
}

type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement // mapped from the s.perm atom via the 13-atom table (05-rbac §6); Delegated for compat
	Guards          Guards
	Public          bool
}
```

The manifest is produced inside ABO by extending the existing recording pattern
(`recordCredRoute`, `internal/server/credential_routes.go:47-58`; `recordAdminRoute`,
`internal/server/wire_admin_role.go:63-70`) to `s.perm`, and by walking `(*gin.Engine).Routes()`.
`archtest.RoutesHavePermissions` (R6) accepts a legacy route only if it is in the manifest with a
permission (or `Public` on the reviewed list of 15, `raw/09` §2.4), so the 36 permission-less
authenticated routes (`raw/09` §4) must each get `rbac.AnyAuthenticated` explicitly — written down,
reviewed, and visible in `books-legacy.json` — instead of being implicit.

### 8.3 File mapping

| ABO today | Becomes |
|---|---|
| `internal/server/server.go:638-731` engine + global middleware | `core/internal/httpmw` chain stages 1–8; removed from gin at H0 |
| `internal/server/server_lifecycle.go:1693-1698` `s.perm` | `Route.Perm`; during migration the gin bridge maps atoms |
| `internal/server/server_lifecycle.go:1710-2135` `setupRoutes` | `books` module `Register` + `MountLegacy` (shrinking) |
| `internal/server/peer_allowlist.go` | `core/internal/httpmw/peer.go` (stage 0) |
| `internal/server/server_middleware.go:56-118` CORS + headers | stages 5–6 |
| `internal/server/middleware/ratelimit.go`, `request_size.go` | stages 9–10, 13 |
| `internal/server/gone.go` | `httpapi.Gone` |
| `internal/httputil/respond.go`, `types.go`, `parse.go` | `httpapi.Problem`, `Page[T]`; legacy shape kept for aliases |
| `internal/httputil/rangeserve.go` | stays app-side helper used via `HandleRaw` (already `net/http`) |
| `internal/plugin/router.go`, `internal/server/plugins_init.go:42` | deleted (N4); replaced by the module `Group` |
| `tygo.yaml`, `tools/cmd/opsgen`, `internal/wirecontract` | superseded by OpenAPI + `coretest.ContractGoldens` (§7) |
| `web/src/services/api.ts` (8,747 lines), `web/src/utils/apiFetch.ts` | `@mediacore/api-client` + `@abo/books-api-client`; `apiFetch` behaviour ported into client middleware |

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
- Idempotency: the `in_flight` record is written with a storage batch conditional on absence
  (05-storage `Batch.SetIfAbsent`), so two concurrent requests with one key cannot both run.
- Handler contexts are cancelled on client disconnect and on shutdown; handlers must pass `ctx`
  down. Long work belongs in `tasks` (start returns 202 with a run ID).

## 10. Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.http.listen` | string | `:8484` (ABO default port, `server_lifecycle.go:2187`) | no |
| `core.http.peer_allowlist` | []CIDR | `[]` (warn: accept any peer) | yes |
| `core.http.trusted_proxies` | []CIDR | `[]` | yes |
| `core.http.client_ip_header` | string | `""` | yes |
| `core.http.cors.allowed_origins` | []string | `[]` (same-origin only) | yes |
| `core.http.body.json_max_bytes` | int | 1 MiB | yes |
| `core.http.body.upload_max_bytes` | int | 10 MiB | yes |
| `core.http.rate_limit.enabled` | bool | true | yes |
| `core.http.rate_limit.per_minute` | int | 100 | yes |
| `core.http.rate_limit.burst` | int | `max(per_minute/5, 10)` | yes |
| `core.http.rate_limit.max_clients` | int | 10000 | no |
| `core.http.basic_gate.enabled` | bool | false | yes |
| `core.http.basic_gate.password` | secret ref | — | yes |
| `core.http.openapi.public` | bool | false | yes |
| `core.http.idempotency.ttl` | duration | 24h | yes |
| `core.http.read_header_timeout` / `idle_timeout` | duration | 15s / 120s | no |

ABO's flat keys (`json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`,
`api_rate_limit_per_minute`, `basic_auth_enabled`, `allowed_peer_cidrs`;
`internal/config/config.go:1554-1559,1578,1719`) are aliased by 05-config's key-alias mechanism for
two minor releases.

## 11. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_http_requests_total` | counter | `owner`, `route` (pattern), `method`, `code` |
| `mediacore_http_request_duration_seconds` | histogram | `owner`, `route`, `method` |
| `mediacore_http_inflight_requests` | gauge | `owner` |
| `mediacore_http_response_bytes` | histogram | `owner`, `route` |
| `mediacore_http_panics_total` | counter | `owner`, `route` |
| `mediacore_http_rate_limited_total` | counter | `scope` (`client`,`principal`), `class` |
| `mediacore_http_deprecated_requests_total` | counter | `owner`, `alias` |
| `mediacore_http_gone_requests_total` | counter | `owner`, `route` |
| `mediacore_http_idempotency_total` | counter | `outcome` (`stored`,`replayed`,`conflict`,`reused`) |
| `mediacore_http_peer_refused_total` | counter | — |

Unmatched paths use `route="unmatched"` so cardinality stays bounded by the route table.

## 12. Tests

| Test | Asserts |
|---|---|
| `TestHandle_RejectsZeroPermission` | `Route{Perm: zero}` → `ErrNoPermission` from `Build` (R6) |
| `TestHandle_RejectsForeignScope` | module `tv` registering `books:edit` → `ErrForeignScope` |
| `TestBuild_PatternConflictIsError` | conflicting `ServeMux` patterns → `ErrPatternConflict`, no panic |
| `TestBuild_PrefixCollision_FacadeVsLegacyV3Ops` | facade at `/api/v3` + books legacy `/api/v3/ops/` → error; with url_base `/tv` → ok |
| `TestModuleGroup_IsAuthenticated` | every route reachable from a module `Group` returns 401 without a credential unless `Public` (G8 regression) |
| `TestChain_OrderGolden` | recorded stage order equals §4.2 table |
| `TestProblem_ShapeGolden` / `TestLegacyError_ShapeGolden` | both envelopes byte-for-byte |
| `TestUnmatched_APIPathIsProblemNotHTML` | `/api/nope` → problem 404; `/library` → SPA |
| `TestMethodNotAllowed_HasAllow` | 405 + `Allow` |
| `TestBind_TagsCompiledAtRegistration` | bad tag → `ErrBadBinding` at boot |
| `TestDecode_StrictUnknownField` / `TestAlias_NonStrict` | 400 native, accepted on alias |
| `TestPaginate_CursorRoundTripAndForgery` | `limit+1`, `next_cursor`, tampered cursor 400 |
| `TestIdempotency_ReplayConflictInFlight` | uses `testing/synctest` for the concurrent in-flight case |
| `TestAlias_DeprecationHeaders` / `TestAlias_TurnsGoneAtSunset` | headers; fake clock past sunset → 410 legacy body |
| `TestRequestID_GeneratedAndPropagated` | header, problem, slog record, audit record carry the same id |
| `TestRateLimit_ClientAndPrincipalBuckets` | `synctest` time; headers |
| `TestOpenAPI_Golden` / `TestOpenAPI_IsValid31` | generated doc equals committed file; validates against the 3.1 meta-schema |
| `TestContractGoldens_ValidateAgainstSchema` | every example matches its schema |
| `archtest.RoutesHavePermissions` | over native + legacy manifest (R6) |
| `TestLegacyMount_AliasShadowsGinRoute` | after `Alias`, the gin handler for that method+path is never invoked |

## 13. Open items

1. Whether `encoding/json/v2` replaces `encoding/json` in `Handle` (only once it is non-experimental in the pinned toolchain; 13-toolchain).
2. Exact sunset dates per alias depend on the release plan (09-abo-on-core).
3. Whether `/metrics` moves under `/api/core/v1/metrics` or stays at `/metrics` (05-metrics decides; either way it carries `core:metrics.read`, 05-rbac §6).
4. Coordination with 06-app-sdk: its `Registrar.Route(routes ...httpapi.Route)` and
   `httpapi.Get[Q,R](path, perm, h, opts...)` correspond to `Register(g, Get(...))` above, and its
   `Registrar.LegacyHandler(prefix, policy, h)` to `Router.MountLegacy` (§8.2); this file owns the
   final signatures (06 O2).
5. HTTP/3 `Alt-Svc`: becomes a listener option that sets the header in stage 5, fixing the
   registration-order miss noted in `raw/09` §2.2(c) — to be confirmed by a test once implemented.

## Objections to 03

1. **Amendment, not divergence:** 03 §3.6 allows any `[a-z][a-z0-9]{1,15}` module ID. IDs `v1`…`v9…`,
   `api`, `events`, `health`, `metrics`, `auth`, `ui` would collide with core paths (§4.1); add a
   reserved list to §3.6 and to the kernel's manifest validation.
2. **Amendment:** R6 should also say "no API returns a chain-less route group" — the G8 failure mode
   was not a missing permission but a missing chain.

## Out-of-scope defects found

1. HSTS is set when the client sends `X-Forwarded-Proto: https`, from any peer
   (`internal/server/server_middleware.go:113`); harmless in practice (browsers ignore HSTS over
   plain HTTP), fixed by stage 5's trusted-proxy rule.
2. `RequireAuth` answers a store read error with 500 rather than 503 (`internal/server/middleware/auth.go:163-167`), so clients and monitors cannot tell "auth backend down, retry" from a server bug; fixed by §6 (503 `auth_unavailable`).
