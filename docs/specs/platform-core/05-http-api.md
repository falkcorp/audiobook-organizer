<!-- file: docs/specs/platform-core/05-http-api.md -->
<!-- version: 1.3.1 -->
<!-- guid: fcd387ab-7a8e-49e2-a4b4-a6901aaee1e3 -->
<!-- last-edited: 2026-11-04 -->

# 05 — HTTP API framework (`core/httpapi`)

Evidence commit `748fd787a`. Raw inputs: [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md),
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md),
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §8,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md).
Names, layers (L0–L9) and rules R1–R8 are from [03-architecture](03-architecture.md) v1.2.0; identity is
in [05-authn](05-authn.md), permissions and audit in [05-rbac](05-rbac.md), streams in
[05-realtime](05-realtime.md), the compatibility window and stages in [09-abo-on-core](09-abo-on-core.md)
§9.2.1 and §9.4.

## 1. Purpose & scope

`core/httpapi` is the only way any module, the kernel, or the optional *arr facade puts a route on
the wire. It owns:

- the router (standard-library `net/http.ServeMux` with Go 1.22+ method/wildcard patterns — decision N7, confirmed in §1.1);
- typed route registration with generics and a **mandatory permission** (R6, fail-closed at registration);
- the **core-owned middleware chain**, handed to each module as an already-authenticated `Group` (fixes G8),
  including the perimeter (peer allowlist, recovery, security headers, CORS, cross-origin protection,
  Basic gate, rate limit);
- the error envelope for native routes, offset pagination with opt-in cursors, request IDs,
  deprecation headers rendered from the compat ledger;
- OpenAPI 3.1 generation from registrations and the TypeScript client pipeline;
- the **legacy mount** (`MountLegacy`): ABO's gin engine (497 routes, +75 ABS) runs **permanently** as
  an `http.Handler` under the `books` module, behind core's perimeter, with each gin route registered
  as its own pattern from a reviewed manifest (cross-cutting resolution 2, SIMP-2). A books route
  group moves to native `httpapi` only when it is rewritten for its own reasons; there is no
  gin-retirement stage;
- the prefix-claim table that resolves path collisions (`/api/v3/ops` vs an *arr facade's `/api/v3`, §4.9,
  a deferred-build member, §3.6), and the place of the SPA handler in the route table (§4.10).

Out of scope: listener/TLS/HTTP-3 configuration beyond the handler (kept in `app` options,
[06-app-sdk](06-app-sdk.md)); what the SPA handler serves (`core/ui`, [07-ui-shell](07-ui-shell.md));
the *arr resource mapping ([08-arr-facade](08-arr-facade.md)); health/metrics endpoints' payloads
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
`Validate`, visible in OpenAPI). One gin behaviour the repo documents as a hazard disappears for
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
own layer: `config` adds `config.Duration` and `secrets.Ref`; `httpapi` adds `rbac.Permission` (which
it already imports), `time.Time` and its page types. `httpapi` therefore needs no `config` import and
no live config store to reflect a type, so a `coretest` router or the `examples/hello` route test
builds without one; there is still exactly one reflector. Verified on 2026-11-04 against the Go
module proxy and the module zip: v0.4.3 is the latest tag (published 2026-04-17, older than the
14-day release-age rule), MIT licence, one dependency (`github.com/google/go-cmp`), four releases in
the last 11 months; `TypeSchemas` exists in `jsonschema/infer.go`. **Pinning (MOD2-20).** A Go
`require` is a minimum, so "pinned exactly" needs a mechanism: Dependabot `ignore` entries (all
update types) for `github.com/google/jsonschema-go`, so it is bumped only by a deliberate PR, and
`check_module_skew.py --pin github.com/google/jsonschema-go=v0.4.3`, which asserts the resolved
version in both the root and `core/` `go list -m all` outputs. `TestSchema_Golden` remains the
after-the-fact drift check.
`go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` v0.72.0 replaces `otelgin` for core
stages (same contrib release train as the current `otelgin v0.72.0`, `go.mod:31`).

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
`compat`, `log`, `metrics`, `core/internal/jsonschemax`; neither `config` (SIMP2-3) nor `storage`:
with the idempotency store deleted (§4.5) `httpapi` keeps no records). Implementation details in
`core/internal/openapi` and `core/internal/httpmw`.

**Minimum surface per stage (COR2-9).** `httpapi` lands at 09 S2, before `auth`, `audit` and the
rest of `rbac` (S4). At S2 it needs only the `rbac` leaf types `Permission`, `Requirement`, `Need`,
`AllOf`, `AnyOf`, `Public`, `AnyAuthenticated` and `Delegated` (which land with it at S2, 05-rbac
§3.1), `compat`, `log` and `metrics`. `Deps.Auth`, `Deps.RBAC` and `Deps.Audit` are nil until S4;
`Build` refuses a native route other than `Public`/infra while they are nil (`ErrNoAuthService`); that holds at S2–S3
(09 S3 adds no native endpoint: only the legacy mount with `LegacyAuthGin`, infra and the legacy `/`
owner exist), and stages 12–13 are skipped for the mount (§8.1).

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
	Deprecated  string       // compat ledger row ID when this native route is itself being retired (§4.6); "" = not deprecated
	Strict      bool         // reject unknown JSON fields (default true, for native routes and aliases; see §3.4)
}

// Guards are identity requirements beyond the permission.
type Guards struct {
	Interactive bool // refuse API keys, compat tokens and auth-disabled (today's credGuard, auth.Method.MayChangeCredentials)
	Owner       bool // require an owner-assurance principal (today's RequireOwner; 05-authn §4.8)
}

type RateClass uint8

const (
	RateDefault RateClass = iota
	RateAuth              // login/setup/bootstrap/invite: tighter per-source buckets (05-authn §4.9)
	RateExpensive         // search, exports
	RateExempt            // infra probes and the SPA handler only; rejected for non-Public routes
)

// Lifetimes are not Go data (SIMP2-12). An alias or a deprecated route names
// its compat ledger row (09 §9.2.9); the row holds the class, gate, shipped
// release and date and the earliest removal date. The router renders the
// Deprecation (RFC 9745) and Sunset (RFC 8594) headers from the ledger table
// embedded in the binary (one //go:embed of the generated ledger table), and
// Link rel="successor-version" from the target route's pattern. Sunset is
// ADVISORY: nothing in the router compares it with the clock (OPS-10, SIMP-7).
// TestCompatLedgerMatchesCode joins every Ledger ID with the ledger.

// Group is a mount point whose whole core chain is already applied. A module
// receives exactly one, rooted at /api/<moduleID>/v1, from module.Registrar.HTTP().
// There is no API that yields an unauthenticated or chain-less group (G8).
type Group struct{ /* unexported: owner module, base, version, registry */ }

func (g *Group) Base() string             // "/api/books/v1"
func (g *Group) Module() string           // "books"
func (g *Group) Sub(prefix string) *Group // "/api/books/v1/series"; same chain
func (g *Group) Version(major int) *Group // "/api/books/v2"; major >= 2

// Handle registers a typed JSON operation. It never panics: errors are collected
// and returned by the kernel from app.New (fail-closed boot, §5).
func Handle[Req, Resp any](g *Group, rt Route, fn func(ctx context.Context, in *In[Req]) (Resp, error))

// HandleRaw registers an operation that writes its own response (file download,
// range serving, SSE). Perm, guards, rate and body limits still apply. The
// OpenAPI entry is described by the provided RawDoc.
func HandleRaw(g *Group, rt Route, doc RawDoc, h http.Handler)

// Alias registers a legacy path for one or more already-registered native
// routes of g (each target keeps its own handler and permission), stamps
// deprecation headers from the ledger, and counts its use (§4.6).
func Alias(g *Group, legacy LegacyPath)

type LegacyPath struct {
	Pattern     string   // full ServeMux pattern, e.g. "POST /api/v1/auth/api-keys"
	Ledger      string   // compat ledger row ID (09 §9.2.9); required
	Targets     []string // Route.Name of each native route this path may reach; one entry = fixed target
	// Request picks the target for one request from a body or query selector
	// field (SEC2-5), e.g. user_id present -> "createUserAPIKey", absent ->
	// "createOwnAPIKey". Required iff len(Targets) > 1. It reads a buffered copy
	// of the body, may set path values for the chosen target (r.SetPathValue),
	// and returns an *Error (400) for a selector it cannot map.
	Request     func(r *http.Request) (target string, err error)
	Selector    []string        // body/query fields Request consumes; removed before the target decodes strictly
	LegacyError bool            // render httputil-style {error,code,status} instead of problem+json
	Shape       func(v any) any // optional: adapt a native response body to the legacy JSON shape
}

// Gone registers a retired route: auth and permission still run first, then
// 410 with problem code "gone" (and legacy {"error":"gone","message":…} on legacy paths).
// Only a contract PR replaces an Alias with Gone (§4.6).
func Gone(g *Group, rt Route, successor string)

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
	Code      string       `json:"code"`               // machine code, snake_case, stable: "not_found", "permission_denied", "gone", ...
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`              // validation details
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
	ErrPublicNotAllowed   = errors.New("httpapi: Public route not on the module's declared public list")
	ErrLegacyManifest     = errors.New("httpapi: legacy route missing from or inconsistent with the manifest")
	ErrAliasTarget        = errors.New("httpapi: alias names an unknown target, or several targets without Request")
	ErrLegacyAuthGin      = errors.New("httpapi: LegacyAuthGin mount not named by app.Options.LegacyAuthGinUntil")
	ErrOwnAuthMethod      = errors.New("httpapi: OwnAuth route names no compat method registered by its module")
	ErrShellSegment       = errors.New("httpapi: a shell route segment is also a non-SPA server segment")
	ErrNoAuthService      = errors.New("httpapi: non-Public native route registered before auth/rbac exist")
)
```

### 3.4 Validation and JSON naming

```go
// Validator is implemented by request types that need more than JSON decoding.
// Return Invalid(...) or errors.Join of *FieldError-carrying errors.
type Validator interface{ Validate() error }
```

Decoding rules: `Content-Type` must be `application/json` (415 otherwise) for `body:"json"`; body is
read through `http.MaxBytesReader(MaxBody)` (413 `body_too_large`); `Strict` routes use
`Decoder.DisallowUnknownFields` (400 `unknown_field`) — default true for native routes **and for
aliases** (SEC2-5). An alias removes its `Selector` fields, then decodes the rest strictly into the
target's request type; any other legacy field is refused with 400 `legacy_field_unsupported`, never
silently dropped, because a dropped selector (`user_id`, `all`) retargets the action to the caller.
A legacy path whose fields cannot all be mapped stays a gin route (or becomes `Gone`), it is never
aliased (§4.6).

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
	Auth    *auth.Service  // nil at S2–S3 (§3, COR2-9)
	RBAC    *rbac.Service  // nil at S2–S3
	Audit   audit.Logger   // nil at S2–S3: stage 7/13 refusals go to the access log (§4.2)
	Compat  *compat.Ledger // alias registration, embedded ledger dates, mediacore_compat_legacy_use_total (09 §9.2.9)
	Log     *slog.Logger
	Metrics *metrics.Scope // "mediacore.http" (05-metrics)
}

func (r *Router) ModuleGroup(moduleID string) *Group    // /api/<id>/v1
func (r *Router) CoreGroup() *Group                     // /api/core/v1; used only by core/internal/<capability>http, uihttp included (03 §3.3, COR2-19)
func (r *Router) Infra(pattern string, perm rbac.Requirement, h http.Handler) // /livez, /readyz, /metrics; non-Public perms are enforced (§4.2, SEC2-14)
func (r *Router) MountLegacy(m LegacyMount) error       // §8.2
func (r *Router) SPA(h http.Handler) error              // §4.10; ErrPrefixCollision while a legacy mount ServesSPA
func (r *Router) NonSPA() func(*http.Request) bool      // §4.10, for ui.Options (FE-3, FE2-6)
func (r *Router) NonSPASegments() []string              // the first segments behind NonSPA, for the kernel's shell-route check (§4.10, FE2-7)
func (r *Router) Claim(owner, prefix string) error      // subtree claim, §4.9
func (r *Router) Routes() iter.Seq[RouteInfo]           // archtest, OpenAPI, docs
func (r *Router) Build() (http.Handler, error)          // registers into a fresh ServeMux; errors.Join of all registration errors
func (r *Router) OpenAPI(module string) (*openapi.Document, error)

// ClientAddrFrom returns the client address stage 0 resolved for this request
// (peer, or the trusted proxy's client-IP header) and whether it was
// attributed through a trusted proxy. From 09 S2 the legacy gin limiters read
// it instead of re-deriving it (§8.1, SEC2-19).
func ClientAddrFrom(ctx context.Context) (addr netip.Addr, attributed bool)

// ReservedSegments returns the first path segments core itself serves: "api",
// "v<digits>", "livez", "readyz", "metrics", "health", "auth", "events",
// "assets", "static", "ui" (the SPA's hashed-asset and core UI paths, FE2-11).
// It is static: the kernel evaluates it at P1, before any MountLegacy (P6), so
// it carries no app compat segment (COR2-19). One input of module.ReservedIDs()
// (03 §3.6). A function, not an exported slice (R5).
func ReservedSegments() []string

type RouteInfo struct {
	Owner      string // module ID or "core"
	Method     string
	Pattern    string
	Name       string
	Perm       rbac.Requirement
	Guards     Guards
	RateClass  RateClass
	Kind       RouteKind // Native, Alias, Gone, Raw, Legacy, Infra, SPA (Facade: deferred-build, §3.6)
	OwnAuth    bool      // legacy route authenticated by its compat surface (§8.2)
	AuthMethod string    // the compat method an OwnAuth route names (§8.2)
	Ledger     string    // compat ledger row, aliases and deprecated routes only
}
```

**Compat segments: two inputs with different meanings (FE2-11, 03 §3.6).** An app's compatibility
surfaces occupy two different kinds of segment. `app.Options.ReservedAPINamespaces` holds second
segments under `/api` (ABO's ABS-compatible `/api/libraries`, …): reserved module IDs only, known at
P1. `LegacyMount.TopSegments` holds the mount's top-level server paths (ABO's `ping`, `status`,
`logout`, `socket.io`, `public`): reserved module IDs **and** `NonSPA`. Because `ReservedSegments()`
is static and evaluated at P1, before any `MountLegacy` (P6), it carries neither input (COR2-19); the
kernel validates module IDs against the static owner lists and `ReservedAPINamespaces` at P1 and
re-checks them against every recorded mount's `TopSegments` at P7, still before listeners open.
`LegacyMount.SPAShared` lists the top-level segments where the mount's routes share a path with an
SPA page and which are therefore **not** in `NonSPA` (ABO: `login`, whose ABS route is `POST /login`
while `GET /login` is the SPA page, as today). `Build` fails with `ErrPrefixCollision` when a legacy
manifest route outside `/api/` has a first segment that is in neither `ReservedSegments()`,
`TopSegments` nor `SPAShared`, so a new top-level legacy path cannot silently fall through to
`index.html`.

### 3.6 Deferred-build members (land with `arrfacade`, 08)

Cross-cutting resolution 1 defers `arrfacade`, and deferral applies to features inside built packages
as well as to packages (SIMP2-6, 03 §3.4). These members of `httpapi` are specified here so that 08
can rely on them, and are **not built** in 09 S2/S4; they land in the PR that adds `arrfacade`:

- `func (r *Router) FacadeGroup(owner, base string) *Group` (an *arr facade mount, §4.9) and
  `RouteKind` value `Facade`;
- stage 13's facade clause: a facade-purpose key is refused on every non-facade route and a
  non-facade key on facade routes (SEC-5, 05-authn §3.5);
- the OpenAPI `apiKeyHeader` (`X-Api-Key`) security scheme;
- tests `TestFacadeKey_RefusedOnCoreAndModuleGroups`, `TestFacadeKey_RefusedOnABS` and
  `TestBuild_PrefixCollision_FacadeVsLegacyV3Ops`.

Until then SEC-5 holds trivially: no facade key can be minted (05-authn §3.5). The S7 done-check
(09) includes `! grep -rn 'FacadeGroup\|PurposeFacade' core --include=*.go`. When the facade lands,
the additions are additive and pass the API-stability gate.

## 4. Behaviour

### 4.1 Path layout

| Prefix | Owner | Notes |
|---|---|---|
| `/api/core/v1/…` | core | auth, users, roles, api-keys, sessions, audit, tasks, settings, system, events (SSE), openapi |
| `/api/<moduleID>/v1/…` | module | e.g. `/api/books/v1/books/{id}` (03 §3.6) |
| `/api/<moduleID>/v{N}/…` | module | a new major only for breaking changes; the old major follows the compat window (09 §9.2.1) |
| `/livez`, `/readyz` (Public), `/metrics` (`core:metrics.read`) | core infra | payloads in 05-health / 05-metrics; probe names as 05-health defines them (FE2-6) |
| `/api/v1/…`, `/api/v3/ops/…`, `/api/events`, `/health`, `/api/health`, `/auth/temp-login`, ABS unversioned paths | `books` legacy mount (**permanent**, §8) | gin routes from the manifest; the paths of core subsystems that moved to native (auth, users, tasks, …) are `Alias`es of the native routes for the compat window; the three legacy event streams are manifest routes wrapping realtime projections (05-realtime §4.9) |
| `/api/v3/…` (or `/<moduleID>/api/v3/…`) | *arr facade owner (`tv`/`movies`) | §4.9; deferred-build (§3.6) |
| `/` | the `books` legacy mount (`ServesSPA`) **until ABO adopts the shell build**; then `core/ui` | §4.10; never both (FE-1) |

Reserved module IDs are owned by 03 §3.6. The kernel validates against `module.ReservedIDs()`, the
union of `httpapi.ReservedSegments()` (`api`, `v<digits>`, `livez`, `readyz`, `metrics`, `health`,
`auth`, `events`, `assets`, `static`, `ui`, §3.5), `ui.ReservedSegments` (shell routes, 07),
`rbac.ReservedNamespaces()` (`core`, `custom`) and the app's two compat inputs
(`app.Options.ReservedAPINamespaces`, `LegacyMount.TopSegments`, §3.5); a test asserts that the three
owner lists together equal 03's row exactly.

### 4.2 Middleware chain (core-owned, fixed order)

Applied by `Router.Build` around every route; a module cannot reorder, skip or add to it (a
module needing per-route behaviour uses `Route` fields). Outermost first. "All" = every request,
including infra, the SPA handler and the legacy mount; "routes" = registered routes (native, alias,
legacy manifest routes, and **`Infra` routes whose `perm` is not `Public`**, e.g. `/metrics`; SEC2-14).
Public infra probes (`/livez`, `/readyz`) stay Public and `RateExempt`:

| # | Stage | Scope | Replaces |
|---|---|---|---|
| 0 | Peer allowlist (`net/netip` prefixes; loopback always allowed; `TrustedProxies` always allowed — today's connector rule, `peer_allowlist.go:28-34,223`); resolves the client address (the client-IP header believed only from `TrustedProxies`) once and publishes it in the request context (`ClientAddrFrom`) for every later stage and for the legacy gin limiters (SEC2-19) | listener `http.Handler`, before routing | `internal/server/peer_allowlist.go:237` |
| 1 | Request ID: generate ULID, keep a well-formed inbound `X-Request-ID` (`^[A-Za-z0-9._-]{1,64}$`) as `client_request_id`; echo `X-Request-ID` | all | new |
| 2 | Tracing (`otelhttp`, span name = `r.Pattern`). **Span attributes carry the redacted URL** (SEC-22): `url.path`, `url.query` and `url.full` are written after `log.Redactor` (05-logging) applies its query list `DefaultRedactQuery` (which is ⊇ HEAD's `credentialQueryKeys`, including `code`, `code_verifier`, `refresh_token` and `secret`, `internal/server/middleware/log_redact.go:26-37`; this file does not restate the list, SEC2-18) and its `DefaultPathRules` (the books legacy invite path `/invite/{token}`, SEC2-6). If `otelhttp` cannot be configured to emit only redacted values, stage 2 starts the server span itself with semconv attributes; `TestTrace_NoApikeyInSpanAttributes` decides | all | `tracingMiddleware` incl. invite-path redaction, `internal/server/server.go:1672`, `internal/server/middleware/trace_redact.go` |
| 3 | Recovery: panic → problem 500 `internal`, stack logged redacted, span marked error, `mediacore_http_panics_total` | all | `RedactingRecovery`, `internal/server/server.go:704` |
| 4 | Access log (`slog`, the same 05-logging redaction as stage 2, skip streams). Before `audit` exists (09 S2–S3) the stage 7 and stage 13 refusals are written here with the structured attribute `security_event` (SEC2-20) | all | `gin.LoggerWithConfig`, `internal/server/server.go:695` |
| 5 | Security headers (nosniff, frame, referrer, permissions policy; HSTS only when `r.TLS != nil` or a trusted proxy says https). **CSP on every response (FE-4, FE2-1):** when the handler has not set `Content-Security-Policy` by the time headers are written, stage 5 writes the owner's policy — the SPA handler's from `core/ui`; for the legacy mount, `LegacyMount.DefaultCSP(r)`, a function evaluated **per response** (ABO passes today's `contentSecurityPolicy()`, `csp.go:95-106,129-138`: `script-src 'self'` plus the SHA-256 of `index.html`'s inline theme script, and `connect-src 'self' https://<Access team host>` so `apiFetch` still recognises a Cloudflare Access expiry redirect, `csp.go:34-46`); otherwise `default-src 'none'; frame-ancestors 'self'`. **`Reporting-Endpoints: csp="<owner's report URL>"`** with every CSP (legacy mount: `/api/v1/csp-report`, as `server_middleware.go:109` sets it today; `ui`: `/api/core/v1/ui/csp-report`), so Chromium, which prefers `report-to`, keeps reporting. **`X-App-Build: <build id>`** on every response from 09 S2 — legacy mount, native, infra, 404, 405 and 410 included (FE2-5, OPS2-13); the build ID is the one value `make build` generates (git commit plus build timestamp) and passes to both `go build -ldflags` and the Vite `define`/`<meta name="mediacore-build">`; `""` or `dev` (API-only and dev builds) means "unknown", which clients never treat as a mismatch (07 §7.5.2) | all | `internal/server/server_middleware.go:102-118` |
| 6 | CORS (explicit allowlist, same-origin always) | all | `internal/server/server_middleware.go:56-97` |
| 7 | **Cross-origin protection (SEC-10, FE-5):** `net/http.CrossOriginProtection` (stdlib since Go 1.25; the toolchain pin is 1.27) on unsafe methods: refuse when `Sec-Fetch-Site` is present and not `same-origin`/`none`, or, without it, when `Origin` is present and does not match the host; trusted origins = `core.http.cors.allowed_origins`; refusal 403 `cross_origin_rejected`, audited from 09 S4 (before that, access log `security_event`, SEC2-20). **No credential type is exempt** (FE2-23): non-browser clients pass because they send neither `Sec-Fetch-Site` nor `Origin`, not because of the credential they carry; a browser attaches cached HTTP Basic credentials cross-site exactly like cookies, so Basic is never a bypass, and a browser cannot attach `Authorization`/`X-Api-Key` cross-origin without a CORS preflight that stage 6 refuses for unlisted origins. **Host-rewriting proxies:** over plain HTTP browsers send no `Sec-Fetch-Site`, so the `Origin` comparison decides; when the TCP peer is a `TrustedProxies` member, its `X-Forwarded-Host` replaces `r.Host` for that comparison (a proxy that rewrites `Host`, nginx's default, otherwise refuses every UI POST); from any other peer `X-Forwarded-Host` is ignored. Operators behind a proxy that sends neither may list the public origin in `core.http.cors.allowed_origins`; the first refusal raises a health warning naming that key. Applies to the legacy mount too (today's only defence there is `SameSite=Strict`, which is site-scoped and does not stop a sibling app) | all | new (supersedes per-handler checks such as `repairs/handler.go:27-28`) |
| 8 | Optional perimeter gate (HTTP Basic, off by default). Exempt **per route, never per mount** (SEC-14): Public infra probes and legacy routes with `OwnAuth` (§8.2; accepted only for registered compat methods, SEC2-17); everything else, including the ABS routes that run no compat authenticator, stays behind it, as `basicauth.go:51-70` does | all | `internal/server/middleware/basicauth.go:24` |
| 9 | Compression (gzip; skipped for `HandleRaw` streams and `/metrics`) | all | `compressionMiddleware`, `internal/server/server.go:725` |
| 10 | Per-client rate limit (token bucket per client address from stage 0, `ClientAddrFrom`; class from `Route.RateClass` or `LegacyRoute.RateClass`) | routes | `internal/server/middleware/ratelimit.go:96` |
| 11 | Body limit (`MaxBody`; for legacy routes an outer cap of `core.http.body.upload_max_bytes`, gin keeps its per-group limits) | routes | `internal/server/middleware/request_size.go:77` |
| 12 | Authenticate: `auth.Service.Middleware` (05-authn §4.2): binds a principal or records an `auth.Failure`; **stops at the first authenticator that returns anything other than `ErrNoCredential`**; writes no response except 503 `auth_unavailable` (SEC-12) | routes, except `OwnAuth` legacy routes and, before 09 S4, the whole legacy mount (§8.1) | `RequireAuth` step 3–6 + `cfMW` (`raw/10` §2.2) |
| 13 | Authorize: recorded failure → on non-Public routes 401 with the failure code; on Public routes the cookie rule of 05-authn §4.3 (anonymous + cookie cleared; an invalid Bearer/API key is still 401). No principal on non-Public → 401 `authentication_required`. `Guards`. `Perm` via the principal's checker (confinement and `Sensitive` included, 05-rbac §4.4, §4.10) → 403 problem with `required_permission`. (The facade-key clause is a deferred-build member, §3.6.) Denial → `audit` (coalesced) from 09 S4 | routes, same exceptions as 12 | `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner`, `abortWorkerOnlyOutsideWorkerAPI` (whose ABS half is kept in the bridge, §8.3) |
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
  shape (§3.3). Legacy aliases with `LegacyError` render `{error, code, status}` exactly like
  `internal/httputil/types.go:9-13`. Legacy-mount handlers (gin) keep their own bodies; RFC 9457 is
  for native routes only (SIMP-8).
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

### 4.6 Versioning, aliases and retirement

- Within a major: additive only (new optional fields, new routes, new enum values only where the
  schema marks the enum `x-extensible-enum`). CI gate: `oasdiff` v1.35.0 `breaking` against the
  OpenAPI document of the last release tag (committed as `api/openapi/<owner>.json`, §7); a breaking
  diff fails `make ci` unless the route is in a new major.
- **One compatibility window (SIMP-7, OPS-10).** The window is defined once, in 09 §9.2.1, and
  implemented once, in `compat`. Each `Alias` names its ledger row (`LegacyPath.Ledger`, 09 §9.2.9);
  lifetimes are not typed into Go code (SIMP2-12). On every response the alias sends
  `Deprecation: @<unix time of the row's shipped date>`, `Sunset: <HTTP-date of the row's earliest
  removal>` (advisory), `Link: <target pattern>; rel="successor-version"`, counts
  `mediacore_compat_legacy_use_total{surface="http",name=<legacy pattern>,entry="request"}` (09 §9.2.4;
  the counter the removal gate P8 reads), and logs once per (alias, principal) per hour at `INFO`.
- **Aliases never retarget an action (SEC2-5).** One legacy path may front several native routes when
  the legacy handler multiplexed on a body or query field: `POST /api/v1/auth/api-keys` creates a key
  for the caller, or with `user_id` for another user (`credential_routes.go:93`), so its alias has
  `Targets: [createOwnAPIKey, createUserAPIKey]`, `Selector: [user_id]` and a `Request` that sets the
  `{id}` path value; `GET /api/v1/auth/api-keys?all=true` selects the admin list the same way. Every
  remaining legacy field must decode strictly into the chosen target (§3.4). 09 S4 lists, per aliased
  path, its targets and selector field. A legacy path that cannot be mapped without losing meaning is
  not aliased: `PUT /api/v1/auth/me/password` with `user_id` (sets another user's password,
  `handlers/auth.go:581-616`) stays a gin route that calls the dominance-checked `auth.Service`
  (05-authn §4.13), and its gin pattern is not deleted at S4.
- **No clock flips anything.** An alias is replaced by a `Gone` stub only in a contract PR, which
  `TestNoContractBeforeSunset` and the zero-use gate of 09 §9.2.1 allow; the stub is deleted one minor
  release later — the existing `gone()` convention (`internal/server/gone.go:20-32`) kept, with its
  auth-first ordering. A pinned or old build restarted after its advisory sunset keeps serving its
  aliases.
- A native route that is itself being retired names its ledger row in `Route.Deprecated` and gets the
  same headers.
- 410 body on native paths: problem `code:"gone"`, `successor`; on legacy paths the exact legacy body
  `{"error":"gone","message":"this endpoint has been removed; use … instead"}` so existing tests
  (`internal/server/wire_entities_gone_test.go:19-20`) and clients stay valid.
- **Books legacy routes are not aliases.** They stay gin routes under the permanent legacy mount
  (§8); `/api/v1/…` is a permanent `books` prefix unless a group is rewritten natively, in which case
  that group's old paths become aliases under this section. ABS-compatible unversioned paths are a
  protocol owned by `books` permanently (09 §9.2.4).

### 4.7 Request IDs and correlation

Request ID (stage 1) is put in the context (`httpapi.RequestID(ctx)`), as `slog` attribute
`request_id` by the core log handler (05-logging), as span attribute `http.request_id`, in every
`Problem`, and in every audit record (05-rbac §7). Background tasks started by a request record
the request ID as `origin_request_id` (05-tasks).

### 4.8 `ServeMux` specifics the router must handle

| Behaviour of `net/http.ServeMux` (Go 1.22+) | Handling |
|---|---|
| Registering two conflicting patterns panics | `Build` registers inside a function with `recover()`, converts the panic text (which names both patterns) into `ErrPatternConflict`; duplicates are detected earlier by our own map for a clearer message |
| Subtree pattern `/x/` redirects `/x` → `/x/` | native and legacy manifest routes use exact patterns (`{$}` where needed; gin runs with `RedirectTrailingSlash=false`, `server.go:639`); subtree patterns are allowed only for the legacy `/api/` and `/` fallbacks and the SPA |
| Unclean paths (`//x`, `/./x`, `/a/../b`) are 301-redirected to the clean path | gin does not do this (`RedirectFixedPath=false`, `server.go:640`), and the SPA fallback answers such variants itself (`spa_fallback.go:90`). The router checks `path.Clean` before the mux: an unclean `/api/` path → problem 404; any other unclean path goes to the `/` owner (legacy mount or SPA handler) unchanged, whose own normalization decides |
| More specific pattern wins across methods/paths | the basis of native routes shadowing legacy ones (§8.1) and of prefix claims (§4.9) |
| `{name...}` wildcard | translation of gin `*name`; gin `:name` → `{name}` |
| 405 + `Allow` generated automatically | wrapped into problem+json (§4.3) |
| Wildcards match **escaped** segments (`/a%2Fb` is one segment), while gin with `UseRawPath=false` routes on the decoded `r.URL.Path` (`server.go:637-641`), so the two can pick different routes for one request | the legacy handler wrapper passes the matched `r.Pattern` to gin, and after gin routed, a request whose `c.FullPath()` is not the manifest entry of that pattern gets 404 in the legacy error shape before any gin handler runs (SEC2-22, §8.2) |

### 4.9 Prefix claims and the `/api/v3` collision

(The facade half of this section — `FacadeGroup` and its URL-base rule — is a deferred-build member,
§3.6; the single claim rule applies from S2.)

`Router.Claim(owner, prefix)` records every subtree an owner occupies (core groups, module groups,
facade groups, and each `module.Manifest.LegacyPrefixes` entry). `Build` fails with
`ErrPrefixCollision` when two owners claim overlapping prefixes — one rule, no claim kinds and no
yielding (SIMP-22, as 06 §6.4.2 states it). The two fallbacks are not subtree claims: `/` (the SPA
fallback, §4.10) and the legacy `/api/` redirect handler (item 4), both matched last by `ServeMux`
specificity.

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
3. When a contract PR removes the `/api/v3/ops` aliases (compat window, §4.6), the legacy claim goes
   with them and the constraint in (2) relaxes to "two facades need distinct bases".
4. The legacy unversioned redirect (`/api/*` → `/api/v1/*`, `server_lifecycle.go:1791-1822`) is
   re-implemented as the least-specific `/api/` handler of the legacy mount, so every claimed prefix
   (`/api/core/`, `/api/books/`, a facade) is matched before it by `ServeMux` specificity and can
   never be redirected into `/api/v1`. A request the redirect rule excludes is answered 404 in the
   legacy error shape.

### 4.10 The SPA handler and `/` (FE-1, FE-3, FE-25)

- **One owner of `/`.** While the `books` legacy mount declares `ServesSPA: true`, it owns `/` and
  gin's `NoRoute` keeps serving the SPA with every protection it has today (non-SPA segments,
  non-GET/HEAD, case and slash variants, CF service-token recovery on page navigations,
  `spa_fallback.go:58-125`, `cf_recovery.go:36-60`). `Router.SPA` returns `ErrPrefixCollision` in
  that case and the kernel does not mount `ui.Handler`. When ABO adopts the shell build (07, 09 S9),
  the same PR drops `ServesSPA`, deletes gin's `NoRoute`/`setupStaticFiles`, and the kernel calls
  `Router.SPA(ui.Handler(...))`. No `/` pattern is reserved by `httpapi` itself.
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
- **Shell routes and server segments are disjoint (FE2-7).** At P8 the kernel checks
  `ui.ReservedSegments() ∩ Router.NonSPASegments() = ∅` and fails boot with `ErrShellSegment`
  otherwise: a shell page whose first segment is a server segment (the shell's former `/logout`
  against ABO's ABS `logout`) would be reachable only by client-side navigation and answer a typed
  URL or a refresh with a JSON 404.

## 5. Lifecycle

1. `app.New` builds `httpapi.Router` after `auth`, `rbac`, `audit`, `compat` exist (R4 order; at
   09 S2–S3 only `compat` exists, §3).
2. Core's capability routes are registered on `CoreGroup()` by one package per capability,
   `core/internal/<capability>http` (`authhttp`, `rbachttp`, `audithttp`, `taskhttp`, `eventhttp`,
   `confighttp`, `backuphttp`, `updaterhttp`, `healthhttp`, `libraryhttp`, and `uihttp` for
   `ui/diagnostics` and `ui/csp-report`, so `ui` registers nothing on `CoreGroup` itself; layer L6,
   imported only by `kernel`, 03 §3.3, COR-18, COR2-19); each module's `Register(r module.Registrar)`
   receives `r.HTTP() *httpapi.Group` and, for ABO, calls `MountLegacy`. Registration only
   **records**; nothing touches the mux.
3. Kernel calls `Router.Build()`: validates every route (permission present and in catalog, scope
   namespace rule from 05-rbac §4.9, Public allowlist, binding tags, schema generation, snake_case
   names, the legacy manifest against the gin engine, `LegacyAuthGin` and `OwnAuth` admission of
   §8.2, alias targets), validates prefix claims, the `/` owner and the shell-segment check (§4.10),
   registers into a fresh `ServeMux`, assembles the chain, and returns `errors.Join` of every
   problem. Any error aborts boot **before listeners open** (03 §3.7).
4. `App.Run` serves the handler on configured listeners. Shutdown: `http.Server.Shutdown(ctx)` with
   the app's drain deadline; SSE handlers end on `Server.RegisterOnShutdown` + `Hub.Close`
   (05-realtime).
5. Routes are immutable after `Build` (restart-bound enablement, 03 §3.7).

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Route without `Perm` | boot error `ErrNoPermission` | closed | R6 |
| Route `Perm` scope unknown / foreign namespace | boot error | closed | prevents typos and cross-module escalation (05-rbac §4.9) |
| Pattern conflict / duplicate / prefix collision / two owners of `/` | boot error | closed | silent shadowing is worse than not starting |
| Native JSON property not snake_case | boot error `ErrJSONName` | closed | one wire convention (FE-12) |
| Legacy route missing from the manifest or without a permission | boot error `ErrLegacyManifest` | closed | R6 over the running table (SEC-13) |
| `LegacyAuthGin` mount not named by `app.Options.LegacyAuthGinUntil`; `OwnAuth` route naming no compat method its module registered | boot error `ErrLegacyAuthGin` / `ErrOwnAuthMethod` | closed | no module skips stages 8, 12, 13 by self-declaration (SEC2-17) |
| Alias with an unknown target, or several targets and no `Request` | boot error `ErrAliasTarget` | closed | SEC2-5 |
| Shell route segment equals a non-SPA server segment | boot error `ErrShellSegment` | closed | FE2-7 |
| Alias request carries a legacy field the target does not know | 400 `legacy_field_unsupported` | closed | a dropped selector retargets the action (SEC2-5) |
| Mux pattern and gin's own match disagree (escaped segment) | 404 in the legacy shape, gin handler not run | closed | SEC2-22 |
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
| Missing scope / confined principal | 403 + audit denial (access log `security_event` before 09 S4) | closed | |
| Audit write for a denial fails | request still 403; `mediacore_audit_dropped_total` | open (audit) / closed (request) | the denial is enforced regardless |
| Cursor invalid or filter mismatch | 400 `invalid_cursor` | closed | |
| Alias past its advisory sunset | still served, still counted | open | removal is a contract PR (OPS-10) |
| Gone stub | 410 `gone` | closed | contract |
| Response encode error after headers sent | log + abort connection | — | nothing else possible |

## 7. OpenAPI 3.1 and the TypeScript client

**Generation.** `Router.OpenAPI(owner)` produces one document per owner (core, each module,
facade excluded — the facade mirrors a foreign API) from `RouteInfo` plus reflected `Req`/`Resp`
types:

- `paths` from patterns; parameters from `path`/`query`/`header` tags; request body from the
  `body:"json"` field; responses: success schema, and `default` → `Problem`.
- Schemas: the shared reflector of `core/internal/jsonschemax` (§1.1) with the overrides each
  package contributes, httpapi's included (`rbac.Permission`, `time.Time`, `json.RawMessage` → `{}`,
  `Page[T]`/`CursorPage[T]` → generic expansion `Page_<T>`); component
  names `<module>.<GoType>`; two Go types mapping to one name → `ErrSchema`; a non-snake_case
  property → `ErrJSONName`.
- Extensions: `x-mediacore-permission` (scope or `public`/`authenticated`), `x-mediacore-guards`,
  `x-mediacore-sunset` (advisory, from the ledger row); `deprecated: true` for aliases and deprecated
  routes.
- Security schemes: `sessionCookie` (apiKey in cookie) and `bearer` (session or API key);
  `apiKeyHeader` (`X-Api-Key`) is added with the facade (deferred-build, §3.6).
- Served at `GET /api/core/v1/openapi/{owner}.json` (perm `rbac.AnyAuthenticated`;
  `core.http.openapi.public=true` makes it Public) and emitted at build time by the app binary's
  `openapi` subcommand (06-app-sdk) into `api/openapi/<owner>.json`, committed — **except `core`**:
  apps never emit the core document; `core.json` is emitted only by `core/examples/hello`'s `openapi`
  subcommand into `core/web/packages/api-client/openapi/core.json` (07 §7.8, 06 `Description.Core`;
  FE2-24). A freshness gate
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

- `@mediacore/api-client` = generated `paths` for core + `createCoreClient()` built on
  `openapi-fetch` with middleware ported from `apiFetch`: `credentials: 'include'`, timeout
  (`ApiTimeoutError`), SSO bounce detection (`ApiAuthRedirectError`), `X-Request-ID` surfacing,
  `Problem` typed as the error type of every call, and `skipAuthRedirect` set from
  `x-mediacore-permission: public` (§4.3, FE-7).
- Each app generates its own client the same way for its native routes, as a directory, not a
  package (FE-28, 07 §7.8): ABO's is `web/src/modules/books/api/`, generated from
  `api/openapi/books.json`; generation is `make generate-api-client`, freshness-gated like
  `types-check`.
- tygo (`tygo.yaml`) and opsgen stay for the types of routes that remain on the legacy mount, which
  may be indefinitely; a type moves to OpenAPI when its route goes native. The golden contract test
  (`internal/wirecontract/contract_test.go`) is generalized for native routes:
  `coretest.ContractGoldens(t, router)` marshals each route's declared examples and **validates them
  against the generated schema** (jsonschema-go `Resolve`+`Validate`), and the TS side assigns the
  same goldens to generated types, as `web/src/types/contract/contract.test.ts` does now.

## 8. Migration from ABO (the permanent legacy mount)

### 8.1 Stages

Cross-cutting resolution 2: ABO's gin router is mounted permanently, behind core's perimeter, with
gin's own auth chain unchanged inside it; the auth engine behind that chain swaps in exactly one
stage, 09 S4 (OPS-11). There is no gin-retirement stage.

| 09 stage | What changes in HTTP | Who authenticates / authorizes the legacy mount | Rollback |
|---|---|---|---|
| S2 (H0) | `core/httpapi` exists; ABO's `main` builds `app.New(opts, books.Module())` with `app.Options.LegacyAuthGinUntil: "books"`; `books` calls `MountLegacy(Auth: LegacyAuthGin)` with the whole gin engine; **every manifest route is its own mux pattern**; the perimeter moves to core stages 0–11 and is removed from gin in the same PR (logger, recovery, security headers with `DefaultCSP` = today's per-response `contentSecurityPolicy()` and `Reporting-Endpoints` (FE2-1), CORS, Basic auth with per-route exemption, gzip, tracing, the `/api` rate limiter with per-route `RateClass`); stage 5 emits `X-App-Build` on every response (FE2-5, OPS2-13); cross-origin protection (stage 7) is new, and its refusals go to the access log as `security_event` until `audit` exists at S4 (SEC2-20); the 36 permission-less routes are annotated `AnyAuthenticated` in the manifest (R6 must hold at S2). **Client address (SEC2-19):** stage 0 resolves it and publishes it (`ClientAddrFrom`); ABO's `client_trust.go` stays in ABO until S4, and from S2 its `trustedClientIP` (used by the bootstrap, invite and ABS limiters, `bootstrap_rate_limit.go:207`, `invite_rate_limit.go:405`, `client_trust.go:600`) starts from that value and keeps only its "local peer + verified Access assertion" branch on top; at S4 that branch moves into ABO's `internal/cfaccess` authenticator | gin only (CF binder, `RequireAuth`, `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner` unchanged); core stages 12–13 off for the mount | revert to the old `main`; storage untouched |
| S4 | `LegacyAuth` switches to `LegacyAuthCore`, and the same PR removes `LegacyAuthGinUntil` from `app.Options` (an archtest fails if it is still present after S4, SEC2-17): stage 12 binds the core principal and stage 13 enforces each manifest route's `Perm`/`Guards`; inside gin the auth middleware becomes the bridge of §8.3 (it never authenticates; its authorization adapters still enforce through `rbac.Require`); core auth/users/roles/keys/sessions routes go native under `/api/core/v1` and their legacy paths become `Alias`es with targets and selectors (§4.6; more specific than the gin patterns they replace, which are deleted in the same PR), except paths that cannot be mapped (`PUT /api/v1/auth/me/password` with `user_id` stays a gin route calling `auth.Service`, SEC2-5); stage 7 and stage 13 refusals are audited from here | core; gin bridge | 09 S4 rollback (mirrored identity data, 05-rbac §6.3); **the rollback floor is the identity Teach build** (05-authn §11.1, resolution R2-1): once S4 has run, the per-owner level guard refuses any older build |
| S5, S6, … | each core subsystem's routes go native in the stage that moves it (tasks S5, events S6, …), legacy paths aliased; at S6 the three legacy event streams stay gin manifest routes (`Perm` `AnyAuthenticated`, `OwnAuth` false) whose handler is the realtime projection obtained through `Registrar.Realtime().Projection` (05-realtime §4.9, COR2-1) | core | per stage |
| any later | a books route group moves to `/api/books/v1` **only when rewritten for its own reasons** (SIMP-2); its old paths become aliases | core | per group |

The S2 done-check in 09 must therefore cover the perimeter move (cross-cluster request): the Basic
gate's exempt set equals HEAD's (`TestBasicGate_ExemptSetEqualsHEAD`), CSP header goldens per owner
(the legacy SPA response carries the inline-script hash and the configured Access team host, and
`Reporting-Endpoints`), `X-App-Build` on every response class in the perimeter goldens, the
rate-limit class golden, the cross-origin test, the bootstrap, invite and ABS limiter tests unchanged
(SEC2-19), ABS conformance fixtures and the live smoke.
`TestLegacyRoleReachUnchanged` (572 routes × 4 roles, plus scoped keys and auth-off) belongs to S4,
where the engine moves, and stays a permanent CI test because the mount is permanent.

### 8.2 `MountLegacy` contract

```go
type LegacyMount struct {
	Owner      string                // "books"
	Handler    http.Handler          // *gin.Engine
	Routes     iter.Seq[LegacyRoute] // manifest: every gin route; each becomes its own mux pattern
	Auth       LegacyAuth            // LegacyAuthGin only while app.Options.LegacyAuthGinUntil names Owner (09 S2–S3) | LegacyAuthCore
	ServesSPA  bool                  // owns "/" and serves the SPA through gin NoRoute (§4.10)
	DefaultCSP func(*http.Request) string // stage 5 policy for this mount's responses, evaluated per response (ABO: today's contentSecurityPolicy(), FE2-1)
	CSPReport  string                // Reporting-Endpoints URL for DefaultCSP (ABO: "/api/v1/csp-report")
	Redirects  []LegacyRedirect      // the /api/* -> /api/v1/* rule and its exclusions (§4.9 item 4)
	TopSegments []string             // top-level server paths of the mount (ABO: ping, status, logout, socket.io, public): reserved IDs + NonSPA (03 §3.6)
	SPAShared  []string              // top-level segments shared with SPA pages, excluded from NonSPA (ABO: "login"; §3.5)
}

type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement // mapped from the s.perm atom / admin kind via 05-rbac §6; Delegated when OwnAuth
	Guards          Guards           // from credRoute / RequireOwner recording
	Public          bool             // on the reviewed list of 15 (raw/09 §2.4)
	OwnAuth         bool             // the route's gin chain runs a compat authenticator (ABSRequireAuth); stages 8 (Basic), 12, 13 skip it (SEC-14)
	AuthMethod      string           // required iff OwnAuth: the compat method ("books.abs") the owner registered (SEC2-17)
	RateClass       RateClass        // RateDefault iff the gin chain includes the API rate limiter today, else RateExempt
}
```

**Admission (SEC2-17).** `MountLegacy` is a permanent public API, so its two ways of skipping core
stages are admitted by the kernel, not self-declared. `Auth: LegacyAuthGin` (stages 12–13 off for the
whole mount) is accepted only while the compile-time `app.Options.LegacyAuthGinUntil` names the
mount's owner (ABO sets `"books"` at S2 and removes it in the S4 PR; an archtest fails if the field is
set after S4), else `ErrLegacyAuthGin`. `OwnAuth: true` (skips stages 8, 12, 13) is accepted only when
`AuthMethod` names a compat method the same module registered with `Registrar.AuthMethod` (05-authn
§3.3; the kernel records method declarations from S2, before `auth` exists), else `ErrOwnAuthMethod`;
`rbac.Delegated` is accepted only on such routes. The set of `OwnAuth` routes is a reviewed golden
(`TestOwnAuthRoutesGolden`).

- The manifest is produced inside ABO by extending the existing recording pattern
  (`recordCredRoute`, `internal/server/credential_routes.go:47-58`; `recordAdminRoute`,
  `internal/server/wire_admin_role.go:63-70`) to `s.perm`, the ABS auth middleware and the rate
  limiter, and by walking `(*gin.Engine).Routes()`. `Build` fails with `ErrLegacyManifest` when the
  walk and the manifest differ (a gin route without an entry, or an entry without a gin route).
- **Each manifest route is registered as its own `ServeMux` pattern** (gin path → mux pattern, §4.8)
  with the gin engine as the handler (SEC-13), behind a wrapper that passes the matched `r.Pattern`
  to gin and refuses (404, legacy shape) a request whose gin match `c.FullPath()` is not that
  pattern's manifest entry, so the route stages 8, 10 and 13 judged is the route gin runs (SEC2-22,
  §4.8). From S4, stage 13 enforces its `Perm` and `Guards`,
  so the manifest is the running chain, not documentation, and `archtest.RoutesHavePermissions`
  checks what is served. A request matching no manifest route never reaches gin's `NoRoute`, except
  through the `ServesSPA` `/` fallback; under a legacy prefix it gets 404 in the legacy error shape.
- `archtest.RoutesHavePermissions` (R6) accepts a legacy route only if it is in the manifest with a
  permission (or `Public` on the reviewed list of 15, `raw/09` §2.4, or admitted `OwnAuth` with
  `Delegated`),
  so the 36 permission-less authenticated routes (`raw/09` §4) each get an explicit requirement
  (`AnyAuthenticated` or the narrower scope 05-rbac §6.1 assigns) — written down and reviewed
  instead of implicit.

### 8.3 Bridge contract (09 S4 onward)

Inside gin, the auth chain keeps its positions; each element becomes an adapter over the core
decision already taken by stages 12–13. The bridge never **authenticates**, but it is not read-only
for authorization: its adapters still enforce, through `rbac.Require`, which audits (SEC-17), so a
gin route that ever disagreed with its mux pattern would still be checked (SEC2-22).

| Legacy behaviour | Anchor | From 09 S4 |
|---|---|---|
| `RequireAuth` (store nil → pass; session/key lookup) | `middleware/auth.go:152-262` | `legacyauth.FromCorePrincipal`: reads the `auth.Principal` bound by stage 12 and fills the legacy context values (`auth.WithUser`, `WithPermissions` with reverse-mapped atoms, `WithMethod`); never authenticates; a missing principal on a non-Public route is a defensive 401 (stage 13 already answered) |
| zero users → pass everything | `middleware/auth.go:165-169` | gone: stage 13 allows only Public + setup/bootstrap (05-authn §4.2) |
| worker-only path confinement | `middleware/auth.go:265-298`, ABS half `middleware/absauth.go:629-632` | `Confining` scopes at stage 13 (05-rbac §4.10) for every route stage 13 runs on; for `OwnAuth` (ABS) routes, which stage 13 skips, the ABS adapter keeps the check (next rows) — `abortWorkerOnlyOutsideWorkerAPI` is **replaced**, not deleted (SEC2-4) |
| Cloudflare Access fail-open binder | `middleware/cfaccess.go:20-139` | ABO's `internal/cfaccess` authenticator (04 §2.4), registered app-level through `app.Options.Authenticators` (05-authn §3.3, SEC2-16), runs in stage 12; gin binder removed |
| `s.perm(atom)`, in-handler `auth.Can(ctx, atom)` | `server_lifecycle.go:1693-1698` | adapter over `rbac.Require` with the full `Catalog.Legacy` expansion (05-rbac §6.3) |
| `RequireAdmin` by role name; `adminRole` pass-through vs 401 with auth off | `middleware/auth.go:522-537`, `wire_admin_role.go:18-45` | the manifest carries 05-rbac §6.2's permission per admin kind; the gin gate becomes `rbac.Require(that permission)`; auth-off reach follows from the auth-disabled checker |
| `credGuard` | `credential_guard.go:28` | `Guards.Interactive` at stage 13; gin adapter checks `Principal.Method.MayChangeCredentials()` |
| `RequireOwner` | `owner_guard.go:51-89` | `Guards.Owner` at stage 13 |
| user/credential handlers acting on other users | `auth_reset_password.go:18-60`, `auth_temp_login.go:225-227` | call `auth.Service` methods that enforce dominance (05-rbac §4.7 rule 5) |
| ABS `ABSRequireAuth` | `middleware/absauth.go:621` | inside gin for `OwnAuth` routes; resolves through `auth.Service.PrincipalFor`, whose direct API-key path applies stage 12's status, expiry and purpose rules (05-authn §4.10). **Then** the adapter calls `auth.Service.CheckCredentialFor(p, route.Perm)`, which refuses (ABS JSON 403) a principal with `Perms.Confined()` (an `fp-worker` key or a worker user's ABS login) unless the route's manifest `Perm` names one of its scopes, so `TestLegacyRoleReachUnchanged`'s `fp-worker` cells pass on the ABS surface (SEC2-4) |

### 8.4 File mapping

| ABO today | Becomes |
|---|---|
| `internal/server/server.go:638-731` engine + global middleware | `core/internal/httpmw` chain stages 1–11; perimeter middleware removed from gin at 09 S2; the gin engine itself stays |
| `internal/server/server_lifecycle.go:1693-1698` `s.perm` | manifest `Perm` + `rbac.Require` adapter (§8.3) |
| `internal/server/server_lifecycle.go:1710-2135` `setupRoutes` | `books` module `Register` + `MountLegacy` (permanent) |
| `internal/server/peer_allowlist.go` | `core/internal/httpmw/peer.go` (stage 0) + `TrustedPeer` (§3.5), at 09 S2 |
| `internal/server/client_trust.go` | stays in ABO until 09 S4, reading `httpapi.ClientAddrFrom` from S2; at S4 its Access-assertion branch moves into ABO's `internal/cfaccess` and the rest is deleted (SEC2-19) |
| `internal/server/server_middleware.go:56-118` CORS + headers | stages 5–6 |
| `internal/server/middleware/ratelimit.go`, `request_size.go` | stages 10–11 |
| `internal/server/middleware/basicauth.go` | stage 8 with per-route exemption |
| `internal/server/gone.go` | `httpapi.Gone` (native) — legacy stubs stay in gin |
| `internal/httputil/respond.go`, `types.go`, `parse.go` | `httpapi.Problem`, `Page[T]` for native routes; legacy shape kept for aliases and gin |
| `internal/httputil/rangeserve.go` | stays app-side helper used via `HandleRaw` (already `net/http`) |
| `internal/plugin/router.go`, `internal/server/plugins_init.go:42` | deleted (N4); replaced by the module `Group` |
| `tygo.yaml`, `tools/cmd/opsgen`, `internal/wirecontract` | kept for legacy-mount types; native routes use OpenAPI + `coretest.ContractGoldens` (§7) |
| `web/src/services/api.ts` (8,747 lines), `web/src/utils/apiFetch.ts` | calls to native routes use `@mediacore/api-client` / the generated `web/src/modules/books/api/` directory; calls to legacy routes keep `apiFetch` |

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
`admin` (SEC-6).

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
| `core.http.basic_gate.enabled` | bool | false | yes | admin |
| `core.http.basic_gate.password` | secret ref | — | yes | admin |
| `core.http.openapi.public` | bool | false | yes | admin |
| `core.http.read_header_timeout` / `idle_timeout` | duration | 15s / 120s | no | none |

ABO's flat keys (`json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`,
`api_rate_limit_per_minute`, `basic_auth_enabled`, `allowed_peer_cidrs`;
`internal/config/config.go:1554-1559,1578,1719`) and the env name `TUNNEL_CONNECTOR_ADDRS`
(→ `core.http.trusted_proxies`, zone semantics kept, `client_trust.go:133-201`) are aliased by
05-config's key-alias mechanism for the compat window of 09 §9.2.1.

## 11. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_http_requests_total` | counter | `owner`, `route` (pattern), `method`, `code` |
| `mediacore_http_request_duration_seconds` | histogram | `owner`, `route`, `method` |
| `mediacore_http_inflight_requests` | gauge | `owner` |
| `mediacore_http_response_bytes` | histogram | `owner`, `route` |
| `mediacore_http_panics_total` | counter | `owner`, `route` |
| `mediacore_http_rate_limited_total` | counter | `class` |
| `mediacore_http_cross_origin_rejected_total` | counter | `owner` |
| `mediacore_http_gone_requests_total` | counter | `owner`, `route` |
| `mediacore_http_peer_refused_total` | counter | — |

Alias use is counted by `compat`'s `mediacore_compat_legacy_use_total{surface="http",…}` (09
§9.2.4), not by an httpapi-specific counter, so the removal gate reads the series that is actually
incremented (OPS-10). Unmatched paths use `route="unmatched"` so cardinality stays bounded by the
route table.

## 12. Tests

| Test | Asserts |
|---|---|
| `TestHandle_RejectsZeroPermission` | `Route{Perm: zero}` → `ErrNoPermission` from `Build` (R6) |
| `TestHandle_RejectsForeignScope` | module `tv` registering `books:edit` → `ErrForeignScope` |
| `TestBuild_PatternConflictIsError` | conflicting `ServeMux` patterns → `ErrPatternConflict`, no panic |
| `TestBuild_PrefixCollision_FacadeVsLegacyV3Ops` (deferred-build, §3.6) | facade at `/api/v3` + books legacy `/api/v3/ops/` → error; with url_base `/tv` → ok |
| `TestBuild_RejectsCamelCaseJSON` | native type with a `createdAt` tag → `ErrJSONName` (FE-12) |
| `TestModuleGroup_IsAuthenticated` | every route reachable from a module `Group` returns 401 without a credential unless `Public` (G8 regression) |
| `TestChain_OrderGolden` | recorded stage order equals §4.2 table |
| `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused` | cookie session, `Sec-Fetch-Site: same-site` POST → 403 `cross_origin_rejected`; same-origin and header-less clients pass (SEC-10, FE-5); a cross-site POST carrying Basic credentials is refused too (FE2-23) |
| `TestCrossOrigin_TrustedProxyForwardedHost` | plain-HTTP POST with `Origin` = public host, `Host` rewritten by a `TrustedProxies` peer with `X-Forwarded-Host` → allowed; the same `X-Forwarded-Host` from an untrusted peer → refused (FE2-23) |
| `TestBasicGate_ExemptSetEqualsHEAD` | golden of today's per-route exempt set; `/ping`, `/status`, `/login` stay gated (SEC-14) |
| `TestCSP_DefaultPerOwner` | SPA, legacy `DefaultCSP(r)`, native default; a handler-set CSP is kept (FE-4); the legacy SPA response carries the inline-script hash, the configured Access team host and `Reporting-Endpoints` (FE2-1) |
| `TestPerimeter_XAppBuildOnEveryResponse` | legacy, native, infra, 404, 405 and 410 responses all carry `X-App-Build` (FE2-5, OPS2-13) |
| `TestInfra_MetricsRequiresScope` | `/metrics` without a credential → 401, with a key lacking `core:metrics.read` → 403; `/livez`, `/readyz` stay Public (SEC2-14) |
| `TestClientIP_ZonedConnectorTrusted` | zoned link-local connector peer: header believed, peer admitted (SEC-18) |
| `TestTrace_NoApikeyInSpanAttributes` | every `DefaultRedactQuery` value (`apikey`, `token`, `code`, `code_verifier`, `refresh_token`, …) absent from span attributes and the access log (SEC-22, SEC2-18; the list's superset-of-HEAD test is `TestRedactQuery_SupersetOfHEAD` in 05-logging) |
| `TestAccessLog_NoInviteToken` | no one-time token in the access log, journal or span attributes (SEC-21) |
| `TestStage13_InvalidKeyPlusValidSessionIs401` | stop rule (SEC-12) |
| `TestStage13_StaleCookieOnPublicRouteIsAnonymous` | cookie cleared, request proceeds (SEC-11) |
| `TestFacadeKey_RefusedOnCoreAndModuleGroups` (deferred-build, §3.6) | SEC-5 |
| `TestConfining_WorkerKeyRefusedOnABS` | an `fp-worker` key and a worker user's ABS login get ABS 403 on every ABS route whose `Perm` does not name `books:fingerprint.worker` (SEC2-4) |
| `TestProblem_ShapeGolden` / `TestLegacyError_ShapeGolden` | both envelopes byte-for-byte |
| `TestUnmatched_APIPathIsProblemNotHTML` | `/api/nope` → problem 404; `/library` → SPA owner |
| `TestUnclean_PathNotRedirectedByMux` | `//ping` is not 301-redirected; the `/` owner decides |
| `TestSPA_NotRateLimitedAndPublic` | 200 cold-load requests to `/` owner are not 429; `RouteInfo{Kind: SPA}` present (FE-25) |
| `TestSPA_SingleOwnerOfRoot` | legacy `ServesSPA` + `Router.SPA` → `ErrPrefixCollision` (FE-1) |
| `TestNonSPA_PredicateCoversClaims` | `/socket.io`, `/API/V1/x`, `POST /library`, `/health/`, `/HEALTH`, `/metrics/`, `/livez/` never reach the SPA handler; `/assets/x.js` and `GET /login` do (FE-3, FE2-6) |
| `TestKernel_ShellSegmentsDisjointFromNonSPA` | a shell route whose segment is in `NonSPASegments()` → `ErrShellSegment` at boot (FE2-7) |
| `TestMethodNotAllowed_HasAllow` | 405 + `Allow` |
| `TestBind_TagsCompiledAtRegistration` | bad tag → `ErrBadBinding` at boot |
| `TestDecode_StrictUnknownField` / `TestAlias_UnknownLegacyFieldRefused` | 400 `unknown_field` native; 400 `legacy_field_unsupported` on an alias for a field neither the selector nor the target knows (SEC2-5) |
| `TestAlias_CreateKeyForOtherUserKeepsTarget` | legacy `POST /api/v1/auth/api-keys {user_id}` reaches `createUserAPIKey` for that user, bounded by the target's roles and dominance; without `user_id`, `createOwnAPIKey` (SEC2-5) |
| `TestPage_OffsetDefaultHasTotal` / `TestCursor_FilterMismatch` | SIMP-8 |
| `TestTaskStart_IdempotencyKeyReplaysRunView` | a repeated `Idempotency-Key` on task start → 200, the existing `RunView`, `Idempotent-Replayed: true` (SIMP2-8; store in 05-tasks) |
| `TestAlias_DeprecationHeadersAndCompatCounter` | headers rendered from the embedded ledger row; `mediacore_compat_legacy_use_total{surface="http"}` increments |
| `TestAlias_ServedPastAdvisorySunset` | fake clock past the ledger row's earliest-removal date → still served (OPS-10) |
| `TestRequestID_GeneratedAndPropagated` | header, problem, slog record, audit record carry the same id |
| `TestRateLimit_ClientBuckets` | `synctest` time; headers; legacy `RateClass` golden |
| `TestOpenAPI_Golden` / `TestOpenAPI_IsValid31` | generated doc equals committed file; validates against the 3.1 meta-schema |
| `TestContractGoldens_ValidateAgainstSchema` | every example matches its schema |
| `archtest.RoutesHavePermissions` | over native + legacy manifest + SPA/infra set (R6) |
| `TestLegacyManifest_MatchesGinRoutes` / `TestLegacyManifest_RegistersAsMuxPatterns` | `ErrLegacyManifest` on drift; every gin route is a mux pattern, no conflict (SEC-13) |
| `TestLegacyMount_AliasShadowsGinRoute` | after `Alias`, the gin handler for that method+path is never invoked |
| `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash` | a request with `%2F` in a segment whose gin match differs from its mux pattern → 404, no gin handler runs (SEC2-22) |
| `TestOwnAuthRoutesGolden` / `TestLegacyAuthGin_RefusedWithoutOption` | the reviewed `OwnAuth` set; `LegacyAuthGin` without `LegacyAuthGinUntil`, and `OwnAuth` without a registered method → boot errors (SEC2-17) |
| `TestLegacyRoleReachUnchanged` (09 S4, permanent) | 572 routes × 4 seed roles, scoped API keys, auth-off mode |

## 13. Open items

1. Whether `encoding/json/v2` replaces `encoding/json` in `Handle` (only once it is non-experimental in the pinned toolchain; 13-toolchain).
2. Whether `/metrics` moves under `/api/core/v1/metrics` or stays at `/metrics` (05-metrics decides; either way it carries `core:metrics.read`, 05-rbac §6).
3. **Resolved** (coordination with 06-app-sdk, 06 O2, COR-16): 06 registers routes through
   `Registrar.HTTP() *httpapi.Group` with `httpapi.Handle` (or `Register(g, Get(...))` above) and legacy
   routers through `Registrar.MountLegacy(httpapi.LegacyMount)` (§8.2); there is no `Registrar.Route` or
   `Registrar.LegacyHandler`. This file owns the final signatures.
4. HTTP/3 `Alt-Svc`: becomes a listener option that sets the header in stage 5, fixing the
   registration-order miss noted in `raw/09` §2.2(c) — to be confirmed by a test once implemented.
5. A per-principal rate limiter (deferred, SIMP-8): add only if `mediacore_http_rate_limited_total`
   or the access log shows abuse from authenticated principals behind one address.

(v1.2.0's item 6, the stage that ships `@mediacore/api-client`, is closed: 09 UI-0 ships it.)

## Objections to 03

None open. v1.2.0's amendment ("no API returns a chain-less route group") is resolved: 03 v1.5.0 R6
states it, including `LegacyAuthGin` admission and `OwnAuth` compat methods (§8.2). (v1.0.0's
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

- R2-1 — S4 rollback floor = the identity Teach build (05-authn §11.1); bridge adapters enforce (§8.1, §8.3).
- SIMP2-6 — facade members (`FacadeGroup`, `RouteKind Facade`, stage 13 facade clause, `apiKeyHeader`, facade tests) moved to a deferred-build section (§3.6, §4.2, §4.9, §7, §12).
- SIMP2-8 — httpapi idempotency store deleted; task start uses `tasks`' store via `Idempotency-Key` → `StartRequest.IdempotencyKey`; stage 14 removed (§3, §4.2, §4.5, §6, §9–§12).
- SIMP2-12 — `Deprecation{Shipped,Sunset}` deleted; aliases and deprecated routes name a ledger row, headers rendered from the embedded ledger (§3.1, §3.5, §4.6, §12).
- SIMP2-3 — reflector in `core/internal/jsonschemax` with per-package overrides; `Deps.Config` dropped; no `config` import (§1.1, §3, §3.5, §7).
- SIMP2-1 — the idempotency prune schedule is gone with SIMP2-8 (§4.5).
- MOD2-20 — "pinned exactly" made real: Dependabot ignore + `check_module_skew.py --pin` (§1.1).
- COR2-9 — minimum surface at S2 (rbac leaf types; auth/rbac/audit nil until S4; no storage) (§3, §3.5, §5).
- COR2-19 — `ReservedSegments` static at P1; compat segments only from `app.Options`; `uihttp` added to the `CoreGroup` rule (§3.5, §5).
- COR2-1 — legacy event streams are gin manifest routes wrapping realtime projections (§4.1, §8.1).
- FE2-1 — `DefaultCSP func(*http.Request) string` + `CSPReport`; `Reporting-Endpoints` in stage 5; test extended (§4.2, §8.1, §8.2, §12).
- FE2-5 / OPS2-13 — stage 5 emits `X-App-Build` on every response from S2; one build-ID source; perimeter golden (§4.2, §8.1, §12).
- FE2-6 — `NonSPA` covers infra first segments, `health`, `metrics`, today's eight segments; `/livez` naming (§3.5, §4.1, §4.10, §12).
- FE2-7 — kernel check `ui.ReservedSegments ∩ NonSPASegments = ∅` (`ErrShellSegment`) (§3.3, §3.5, §4.10, §12).
- FE2-11 — `ReservedSegments` per 03 v1.5.0 (`assets`, `static`, `ui`, `livez` added); compat inputs split into `app.Options.ReservedAPINamespaces` and `LegacyMount.TopSegments` (replacing `Segments`; P7 re-check of module IDs); `SPAShared` added (§3.5, §4.1, §8.2).
- FE2-23 — no credential type exempt from stage 7 (Basic included); `X-Forwarded-Host` from trusted proxies (§4.2, §12).
- FE2-24 — stale FE-20 and item-6 text closed; `core.json` only from hello; books client is a directory; `RouteInfo.RateClass` (§3.5, §7, §8.4, §13).
- SEC2-4 — ABS adapter keeps worker confinement via `CheckCredentialFor` (§8.3, §12).
- SEC2-5 — aliases dispatch by selector (`Targets`, `Request`, `Selector`) and are strict; `me/password` with `user_id` stays gin (§3.1, §3.4, §4.6, §8.1, §12).
- SEC2-14 — non-Public `Infra` routes are "routes" (stages 10–13) (§3.5, §4.2, §6, §12).
- SEC2-16 — CF Access registered through `app.Options.Authenticators` (§8.3).
- SEC2-17 — `LegacyAuthGin`/`OwnAuth` admission by the kernel (§3.3, §8.1, §8.2, §12); objection 1 to 03 closed (03 v1.5.0 R6 states it).
- SEC2-18 — stages 2/4 use 05-logging's `DefaultRedactQuery` (⊇ HEAD) and `DefaultPathRules` (§4.2, §12).
- SEC2-19 — stage 0 publishes `ClientAddrFrom`; `client_trust.go` stays in ABO until S4 (§3.5, §4.2, §8.1, §8.4).
- SEC2-20 — stage 7/13 refusals go to the access log (`security_event`) until audit exists at S4 (§3.5, §4.2, §6, §8.1).
- SEC2-22 — legacy wrapper passes `r.Pattern` to gin and refuses a mismatched gin match (§4.8, §8.2, §8.3, §12).
- SEC2-6 — legacy invite path redaction named as a 05-logging `DefaultPathRules` entry (§4.2).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Open item 3 resolved: 06 uses `Registrar.HTTP()` and `MountLegacy`; no `Registrar.Route`/`LegacyHandler`.
