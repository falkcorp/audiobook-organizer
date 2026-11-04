<!-- file: docs/specs/platform-core/05-http-api.md -->
<!-- version: 1.8.1 -->
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
`compat`, `config` (for the static `config.Overrides()` value only; never a `*config.Store`, SIMP2-3,
SIMP3-1), `log`, `metrics`, `core/internal/jsonschemax`; not `storage`: with the idempotency store
deleted (§4.5) `httpapi` keeps no records). Implementation details in `core/internal/openapi` and
`core/internal/httpmw`. The import direction is `httpapi` → `internal/openapi`, never the reverse:
`internal/openapi` defines its own operation-description type, which `Router.OpenAPI` fills from
`RouteInfo` and the reflected schemas, so the two cannot form a cycle (MOD3-4).

**Minimum surface per stage (COR2-9, COR3-3, MOD3-4).** `httpapi` lands at 09 S2 with
`internal/openapi`, `internal/httpmw` and `internal/jsonschemax`, before `compat` (S2b) and before
`auth`, `audit` and the rest of `rbac` (S4). At S2 it needs only:

- the `rbac` leaf types `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`, `Public`,
  `AnyAuthenticated`, `Delegated`, `LegacyAtom`, `LegacyCheck`, `RoleName` and the `Checker` interface
  (05-rbac §3, which decides this list; COR4-11, MOD4-11, COR6-1). `LegacyAtom` and `LegacyCheck` are
  record-only constructors at S2: `httpapi` accepts them only in a `MountLegacy` manifest whose mount is
  `LegacyAuthGin` (stages 12–13 off, so nothing evaluates them) or, from S4 r2, `LegacyAuthCore`, and
  `Handle` refuses them; evaluating them needs the S4 checker and catalog;
- the `auth` leaf types `Principal`, `Method`, `MethodSpec`, `Assurance`, `User`, `UserID` and
  `UserStatus` (05-authn §3), which land at S2 next to rbac's so `In.Principal` (whose `User` and
  `Perms` fields name them) and `RouteInfo.AuthMethod` compile and `Registrar.AuthMethod` can record
  compat methods (06 §6.4.10; SEC3-14, COR4-11);
- `config.Overrides()`, `log` and `metrics`.

`Deps.Compat` is an S2b addition, and `compat` itself is not part of the S2 surface (no alias exists
before S4, so nothing at S2 renders a ledger row or counts a legacy use; MOD4-11), and `Deps.Auth`, `Deps.RBAC` and `Deps.Audit` are S4 additions; struct fields
added later are additive under the API-stability gate. While the S4 fields are absent, `Build`
refuses a native route other than `Public`/infra (`ErrNoAuthService`); that holds at S2–S3 (09 S3 adds
no native endpoint: only the legacy mount with `LegacyAuthGin`, infra and the legacy `/` owner exist),
and stages 12–13 are skipped for the mount (§8.1).

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
	Lenient     bool         // accept unknown JSON fields; zero value = strict, for native routes and aliases (§3.4, COR3-13)
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
// Deprecation (RFC 9745) and Sunset (RFC 8594) headers from the ledger the
// app supplies (app.Options.CompatLedger; ABO: //go:embed of a generated copy
// of docs/compat-surfaces.md under the books module, kept fresh by a CI check,
// resolution R4-4) merged with core's own fragment
// (core/compat/core-ledger.md) by compat (SIMP3-3), and
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
// deprecation headers from the ledger, and counts its use (§4.6). The
// pattern must lie inside a prefix g's owner claims or, for core, inside a
// prefix whose ledger row names core as the alias owner (the deliberate
// core-over-books aliases of 09 S4); otherwise Build fails with
// ErrAliasOutsideClaim (§4.9, SEC3-8).
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
	// Shape optionally adapts a native response body to the legacy JSON shape.
	// It is untyped, so OpenAPI documents an alias only as a deprecated entry
	// with the target's native schema; a client that needs the legacy wire shape
	// keeps its hand-written calls on the legacy path (07 §7.11; FE3-6). There is
	// no WithLegacyShape API.
	Shape       func(v any) any
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
	ErrLegacyManifest     = errors.New("httpapi: legacy route missing from or inconsistent with the manifest")
	ErrAliasTarget        = errors.New("httpapi: alias names an unknown target, or several targets without Request")
	ErrLegacyAuthGin      = errors.New("httpapi: LegacyAuthGin mount not named by app.Options.LegacyAuthGinUntil")
	ErrOwnAuthMethod      = errors.New("httpapi: OwnAuth route names no compat method registered by its module")
	ErrNoAuthService      = errors.New("httpapi: non-Public native route registered before auth/rbac exist")
	ErrPublicNotAllowed   = errors.New("httpapi: module native route uses rbac.Public but is not in its Manifest.PublicRoutes")
	ErrAliasOutsideClaim  = errors.New("httpapi: alias or legacy route pattern outside its owner's claims")
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
uses `Decoder.DisallowUnknownFields` (400 `unknown_field`) — the zero value is strict, for native
routes **and for aliases** (SEC2-5; a Go `bool` cannot default to true, so the field is the opt-out,
COR3-13). An alias removes its `Selector` fields, then decodes the rest strictly into the
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
	Auth    *auth.Service  // S4 addition (§3, COR2-9, COR3-3)
	RBAC    *rbac.Service  // S4 addition
	Audit   audit.Logger   // S4 addition; before it, stage 7/13 refusals go to the access log (§4.2)
	Compat  *compat.Ledger // S2b addition: alias registration, ledger dates from app.Options.CompatLedger, mediacore_compat_legacy_use_total (09 §9.2.9, SIMP3-3)
	Log     *slog.Logger
	Metrics *metrics.Scope // "mediacore.http" (05-metrics)
}

func (r *Router) ModuleGroup(moduleID string) *Group    // /api/<id>/v1
func (r *Router) CoreGroup() *Group                     // /api/core/v1; used only by core/internal/<capability>http, uihttp included (03 §3.3, COR2-19)
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
SPA page and which are therefore **not** in `NonSPA`: ABO's is `["login"]` until S9 and
`["login", "invite"]` from S9 — `login`, whose ABS route is `POST /login` while `GET /login` is the SPA
page, as today; `invite`, whose legacy `GET /invite/{token}` 302 route (05-authn §4.13) joins the
manifest at S9, under the shell's `/invite` page (SEC3-16, FE4-2).
`Build` fails with `ErrPrefixCollision` when a legacy manifest route outside `/api/` has a first
segment that is in neither `ReservedSegments()`, `TopSegments` nor `SPAShared`, so a new top-level
legacy path cannot silently fall through to `index.html`.

**`NonSPA` comes from `TopSegments` only (FE3-1, resolution R3-5).** The first segment of a
`module.Manifest.LegacyPrefixes` entry is a reserved module ID, nothing more: `NonSPA` is the union of
§4.10's core inputs and every mount's `TopSegments`, and **never** includes a `SPAShared` segment. A
legacy prefix is non-SPA only for the paths it actually serves, so ABS `POST /login` never makes the
`/login` page non-SPA, and the P8 shell-segment check passes for a `SPAShared` segment that is also a
shell route (`login`, `invite`) while it fails for a `TopSegments` one (06 §6.10). 06 and 07 use this
model.

### 3.6 Deferred-build members

These land with `arrfacade` (08). Cross-cutting resolution 1 defers `arrfacade`, and deferral applies to features inside built packages
as well as to packages (SIMP2-6, 03 §3.4). These members of `httpapi` are specified here so that 08
can rely on them, and are **not built** in 09 S2/S4; they land in the PR that adds `arrfacade`. The
first column holds only package-qualified identifiers, which the archtest reads; everything else is
in "Also deferred (not checked)" (COR5-16, SIMP5-6):

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `httpapi.Router.FacadeGroup`, `httpapi.RouteKindFacade` | stage 13's facade clause; the OpenAPI `apiKeyHeader` (`X-Api-Key`) security scheme; tests `TestFacadeKey_RefusedOnCoreAndModuleGroups`, `TestFacadeKey_RefusedOnABS`, `TestBuild_PrefixCollision_FacadeVsLegacyV3Ops` | `arrfacade` (08) | `func (r *Router) FacadeGroup(owner, base string) *Group` mounts an *arr facade (§4.9) with route kind `Facade`; stage 13 refuses a facade-purpose key on every non-facade route and a non-facade key on facade routes (SEC-5, 05-authn §3.5) |

Until then SEC-5 holds trivially: no facade key can be minted (05-authn §3.5). Their absence is
checked by the deferred-members archtest, `archtest.DeferredMembersAbsent` (03 §3.4, 09 S2), from S2 on
rather than first at S7 (SIMP3-2). Its list is generated from every heading whose text, after an
optional section number, is `Deferred-build members` under `docs/specs/platform-core/` (03 §3.4 is the
one definition), reads only the table's first column and matches package-qualified identifiers
(`httpapi.Router.FacadeGroup`, `httpapi.RouteKindFacade`), never bare names; the PR that
builds a deferred member deletes **that member's** entry, and the test is deleted when the list is
empty (COR4-19, SIMP4-1). When the facade lands, the additions are additive and pass the
API-stability gate.

## 4. Behaviour

### 4.1 Path layout

| Prefix | Owner | Notes |
|---|---|---|
| `/api/core/v1/…` | core | auth, users, roles, api-keys, sessions, audit, tasks, settings, system, events (SSE), openapi; and the confirmation routes that clear native-only data an app's legacy store suspended because an older build changed its legacy row (R4-1): `POST /roles/{name}/reconfirm` (05-rbac §6.3), API-key re-enable (05-authn §4.4), and `POST /integrations/{kind}/instances/{id}/confirm`, which re-confirms a suspended widening field such as `Local` (`core:admin`, `Guards.Interactive`, audited; 05-metadata) |
| `/api/<moduleID>/v1/…` | module | e.g. `/api/books/v1/books/{id}` (03 §3.6) |
| `/api/<moduleID>/v{N}/…` | module | a new major only for breaking changes; the old major follows the compat window (09 §9.2.1) |
| `/livez`, `/readyz` (Public), `/metrics` (`core:metrics.read`) | core infra | payloads in 05-health / 05-metrics; probe names as 05-health defines them (FE2-6) |
| `/api/v1/…`, `/api/v3/ops/…`, `/api/events`, `/health`, `/api/health`, `/auth/temp-login`, ABS unversioned paths | `books` legacy mount (**permanent**, §8) | gin routes from the manifest; the paths of core subsystems that moved to native (auth, users, tasks, …) are `Alias`es of the native routes for the compat window; the three legacy event streams are manifest routes wrapping realtime projections (05-realtime §4.9) |
| `/api/v3/…` (or `/<moduleID>/api/v3/…`) | *arr facade owner (`tv`/`movies`) | §4.9; deferred-build (§3.6) |
| `/` | the `books` legacy mount (`ServesSPA`) **until ABO adopts the shell build**; then `core/ui` | §4.10; never both (FE-1) |

Reserved module IDs are owned by 03 §3.6. The kernel validates against `module.ReservedIDs()`, the
union of `httpapi.ReservedSegments()` (`api`, `v<digits>`, `livez`, `readyz`, `metrics`, `health`,
`auth`, `events`, `assets`, `static`, `ui`, §3.5), `ui.ReservedSegments` (shell routes, 07; a static
function that lands at S2 with `module`, ahead of the rest of `ui` at S9, so the equality test below
holds from S2, FE3-8),
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
| 2 | Tracing (`otelhttp`, span name = `r.Pattern`). **Span attributes carry the redacted URL** (SEC-22): `url.path`, `url.query` and `url.full` are written after `log.Redactor` (05-logging) applies its query list `log.DefaultRedactQuery()` (which is ⊇ HEAD's `credentialQueryKeys`, including `code`, `code_verifier`, `refresh_token` and `secret`, `internal/server/middleware/log_redact.go:26-37`; this file does not restate the list, SEC2-18) and its `log.DefaultPathRules()` (the books legacy invite path `/invite/{token}`, SEC2-6; both are functions returning fresh copies, MOD5-5). If `otelhttp` cannot be configured to emit only redacted values, stage 2 starts the server span itself with semconv attributes; `TestTrace_NoApikeyInSpanAttributes` decides | all | `tracingMiddleware` incl. invite-path redaction, `internal/server/server.go:1672`, `internal/server/middleware/trace_redact.go` |
| 3 | Recovery: panic → problem 500 `internal`, stack logged redacted, span marked error, `mediacore_http_panics_total` | all | `RedactingRecovery`, `internal/server/server.go:704` |
| 4 | Access log (`slog`, the same 05-logging redaction as stage 2, skip streams — including HEAD's `SkipPaths` for the two operations streams, `server.go:696`, COR3-6). Before `audit` exists (09 S2–S3) the stage 7 and stage 13 refusals are written here with the structured attribute `security_event` (SEC2-20) | all | `gin.LoggerWithConfig`, `internal/server/server.go:695` |
| 5 | Security headers (nosniff, frame, referrer, permissions policy; HSTS only when `r.TLS != nil` or a trusted proxy says https). **CSP on every response (FE-4, FE2-1):** when the handler has not set `Content-Security-Policy` by the time headers are written, stage 5 writes the owner's policy — the SPA handler's from `core/ui`; for the legacy mount, `LegacyMount.DefaultCSP(r)`, a function evaluated **per response** (ABO passes today's `contentSecurityPolicy()`, `csp.go:95-106,129-138`: `script-src 'self'` plus the SHA-256 of `index.html`'s inline theme script, and `connect-src 'self' https://<Access team host>` so `apiFetch` still recognises a Cloudflare Access expiry redirect, `csp.go:34-46`); otherwise `default-src 'none'; frame-ancestors 'self'`. **`Reporting-Endpoints: csp="<owner's report URL>"`** with every CSP (legacy mount: `/api/v1/csp-report`, as `server_middleware.go:109` sets it today; `ui`: `/api/core/v1/ui/csp-report`), so Chromium, which prefers `report-to`, keeps reporting. **`X-App-Build: <UI build id>`** on every response from 09 S2 — legacy mount, native, infra, 404, 405 and 410 included (FE2-5, OPS2-13). **One source, the embedded bundle (FE3-2, FE4-1, R5-7):** the app's npm build script itself (`npm run build`, through its Vite config; from S9 the `@mediacore/shell/vite` preset) generates the UI build ID and embeds it as `<meta name="mediacore-build">` in `index.html`, so every build path — `make build`, the Docker stages, goreleaser, a bare `npm run build` — stamps it with no variable to carry and no side script (07 §7.6.3, FE5-1). The UI build ID is a reload discriminator, **never the binary's identity**: updater, storage and backup identify a binary by `app.BinaryID` (06 §6.4.5: the app version plus the executable's SHA-256), which stage 5 never writes (FE5-3). No Go build step receives a UI build ID: stage 5 writes the ID of the bundle the `/` owner embeds — from S9 `ui.Bundle.BuildID()` (07), before S9 the mount's `LegacyMount.BuildID` (ABO fills it from its embedded `web/dist/index.html`) — otherwise `dev`. Every build path (make, Docker, goreleaser) therefore serves the value its own bundle carries, a server whose header disagrees with its bundle cannot be built, and there is no boot-time build-ID check. `""` or `dev` (API-only and dev builds) means "unknown", which clients never treat as a mismatch. A missing `X-App-Build` counts as a mismatch only on a response that carries a `Reporting-Endpoints` header naming a `csp` group (FE4-3): HEAD sets that header on every response (`internal/server/server_middleware.go:102-109`) and stage 5 writes it with every CSP from S2, while a proxy's 502 or an Access interstitial carries neither (07 §7.5.2). `X-Request-ID` is no discriminator: HEAD never writes it, and every build that writes it also writes `X-App-Build` | all | `internal/server/server_middleware.go:102-118` |
| 6 | CORS (explicit allowlist, same-origin always) | all | `internal/server/server_middleware.go:56-97` |
| 7 | **Cross-origin protection (SEC-10, FE-5):** `net/http.CrossOriginProtection` (stdlib since Go 1.25; the toolchain pin is 1.27) on unsafe methods: refuse when `Sec-Fetch-Site` is present and not `same-origin`/`none`, or, without it, when `Origin` is present and does not match the host; trusted origins = `core.http.cors.allowed_origins`; refusal 403 `cross_origin_rejected`, audited from 09 S4 (before that, access log `security_event`, SEC2-20). **No credential type is exempt** (FE2-23): non-browser clients pass because they send neither `Sec-Fetch-Site` nor `Origin`, not because of the credential they carry; a browser attaches cached HTTP Basic credentials cross-site exactly like cookies, so Basic is never a bypass, and a browser cannot attach `Authorization`/`X-Api-Key` cross-origin without a CORS preflight that stage 6 refuses for unlisted origins. **Host-rewriting proxies:** over plain HTTP browsers send no `Sec-Fetch-Site`, so the `Origin` comparison decides; when the TCP peer is a `TrustedProxies` member, its `X-Forwarded-Host` replaces `r.Host` for that comparison (a proxy that rewrites `Host`, nginx's default, otherwise refuses every UI POST); from any other peer `X-Forwarded-Host` is ignored. Operators behind a proxy that sends neither may list the public origin in `core.http.cors.allowed_origins`; the first refusal raises a health warning naming that key. Applies to the legacy mount too (today's only defence there is `SameSite=Strict`, which is site-scoped and does not stop a sibling app). A development proxy that rewrites `Host` (Vite `changeOrigin: true`) must rewrite `Origin` to the target origin as well, or every dev POST over a LAN address is refused (07 §7.10, FE3-19) | all | new (supersedes per-handler checks such as `repairs/handler.go:27-28`) |
| 8 | Optional perimeter gate (HTTP Basic, off by default). Exempt **per route, never per mount** (SEC-14), with HEAD's exempt set exactly (`basicauth.go:34,40-46,69`; COR4-9): Public infra probes; legacy routes with `OwnAuth` (§8.2; accepted only for registered compat methods, SEC2-17); legacy routes with `LegacyRoute.BasicExempt` (generated from HEAD's two health paths, `/api/health` and `/api/v1/health`, which monitors call); and, on the `/` owner, the paths its `LegacyMount.BasicExemptAssets` predicate matches (HEAD's static-asset rule: the `/assets/` prefix, `/favicon.ico` and the `.js`, `.css`, `.png`, `.svg`, `.woff2` suffixes; from S9 the same predicate arrives as `SPAOptions.BasicExemptAssets` when the kernel mounts the shell with `Router.SPA`, §3.5, 07 §7.6.1, FE5-10). Everything else, including the ABS routes that run no compat authenticator, stays behind it, as `basicauth.go:51-70` does. The gate compares the presented username and password with `core.http.basic_gate.username` and `.password`, each in constant time, as HEAD does (`basicauth.go:80-84`; §10, SEC6-9). **No password, no entry (resolution R5-1(c), SEC5-4, DATA5-12):** when `core.http.basic_gate.enabled` is true and `core.http.basic_gate.password` is unset (`ErrNotSet`), cleared, empty or unreadable (`ErrKeyUnavailable`), every non-exempt request gets 401, health reports `degraded` (`basic_gate_no_password`) and the startup log names the key; a presented empty password never matches. HEAD's comparison ported as it is would admit `<username>:` (`basicauth.go:81-87`) — an operator clearing the password on an older build, HEAD's key-regeneration delete path and a partial reset all produce that state (`TestBasicGate_EmptyPasswordRefuses`) | all | `internal/server/middleware/basicauth.go:24` |
| 9 | Compression (gzip; skipped for `HandleRaw` streams, `/metrics`, every response whose `Content-Type` is `text/event-stream`, and every legacy route with `LegacyRoute.NoCompress` — HEAD's exclusions `/api/events`, `/metrics`, `/api/v1/auth/accept-invite` and `^/api/v1/operations/v2/[^/]+/logs/download$`, `internal/server/server.go:1590-1606`, whose last entry prevents double-gzipped log files, COR3-6). The compressing writer implements `Unwrap` and `Flush`, so `http.ResponseController` reaches the connection | all | `compressionMiddleware`, `internal/server/server.go:725` |
| 10 | Per-client rate limit (token bucket per client address from stage 0, `ClientAddrFrom`; class from `Route.RateClass` or `LegacyRoute.RateClass`) | routes | `internal/server/middleware/ratelimit.go:96` |
| 11 | Body limit (`MaxBody`; for legacy routes an outer cap of `core.http.body.upload_max_bytes`, gin keeps its per-group limits) | routes | `internal/server/middleware/request_size.go:77` |
| 12 | Authenticate: `auth.Service.Middleware` (05-authn §4.2): binds a principal or records an `auth.Failure`; **stops at the first authenticator that returns anything other than `ErrNoCredential`**; writes no response except 503 `auth_unavailable` (SEC-12) | routes, except `OwnAuth` legacy routes and, before 09 S4, the whole legacy mount (§8.1) | `RequireAuth` step 3–6 + `cfMW` (`raw/10` §2.2) |
| 13 | Authorize: recorded failure → on non-Public routes 401 with the failure code; on Public routes the cookie rule of 05-authn §4.3 (anonymous + cookie cleared; an invalid Bearer/API key is still 401). No principal on non-Public → 401 `authentication_required`. `Guards`. `Perm` via the principal's checker (confinement and `Sensitive` included, 05-rbac §4.4, §4.10; a legacy route's `rbac.LegacyAtom(atom)` with its auth-disabled and legacy-key rules, 05-rbac §3.1, COR5-4, SEC5-7) → 403 problem with `required_permission`. (The facade-key clause is a deferred-build member, §3.6.) Denial → `audit` (coalesced) from 09 S4 | routes, same exceptions as 12 | `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner`, `abortWorkerOnlyOutsideWorkerAPI` (whose ABS half is kept in the bridge, §8.3) |
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
  not aliased at all: `PUT /api/v1/auth/me/password` — with **and** without `user_id` (with it, it
  sets another user's password, `handlers/auth.go:581-616`) — stays one gin route that calls
  `auth.Service` for both cases, the dominance-checked path for `user_id` (05-authn §4.13), and its
  gin pattern is not deleted at S4. One ServeMux pattern cannot be both an alias and a gin route
  (`ErrDuplicateRoute`), and an alias's `Targets` name only native routes (COR3-12).
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
yielding (SIMP-22, as 06 §6.4.2 states it).

**Patterns stay inside their owner's claims (SEC3-8).** ServeMux panics only on *conflicting*
patterns; a strictly more specific one wins silently, so a claim alone does not stop one owner from
capturing another's requests (an `Alias` at `GET /api/core/v1/auth/oidc/google/callback` would receive
the OIDC `code` and `state`). `Build` therefore fails with `ErrAliasOutsideClaim` when an `Alias`
pattern is outside every prefix its registering owner claims — for core: outside every prefix whose
ledger row (09 §9.2.9) names core as the alias owner — or a legacy manifest route is outside its mount
owner's `LegacyPrefixes`; and with `ErrShadow` when any route of one owner is strictly more specific
than a route of another owner over the same path set. The deliberate core-over-books aliases of 09 S4
(core's auth, users and task routes at their `/api/v1/…` paths) are exactly the ledger rows that name
core as alias owner, and are therefore exempt. The two fallbacks are not subtree claims: `/` (the SPA
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
  that case and the kernel does not mount `ui.Handler`. From S2 the legacy `/` fallback consults
  `Router.NonSPA()` **before** it calls gin, so a path core added to the non-SPA set (S8's `/livez`,
  `/readyz` and their case or slash variants) gets the router's problem 404 instead of gin's
  `NoRoute` page while gin's static `nonSPASegments` list still lacks it — one predicate serves both
  owners (FE3-13). When ABO adopts the shell build (07, 09 S9),
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
  refresh with a JSON 404. `SPAShared` segments are not in `NonSPASegments()` (§3.5).

## 5. Lifecycle

1. `app.New` builds `httpapi.Router` after `auth`, `rbac`, `audit`, `compat` exist (R4 order; at
   09 S2 none of them, at S2b–S3 only `compat`, §3).
2. Core's capability routes are registered on `CoreGroup()` by one package per capability,
   `core/internal/<capability>http` (`authhttp`, `rbachttp`, `audithttp`, `taskhttp`, `eventhttp`,
   `confighttp`, `backuphttp`, `updaterhttp`, `healthhttp`, `libraryhttp`, `storagehttp` (from S4 r2:
   `POST /api/core/v1/system/factory-reset`, whose legacy path becomes its alias; 05-rbac §6.2, resolution
   R7-2), `modulehttp` for
   `GET /api/core/v1/modules` (the kernel passes it a read-only `ModulesView` — catalog snapshot,
   `restart_required`, the `ui_build_id` source: the served **shell** bundle's ID,
   `ui.Bundle.BuildID()`, and `""` while a legacy mount owns `/`, whose ID only `X-App-Build` carries
   (07 §7.5.1, FE5-7); it is not `--version-json`'s `ui_build_id`, which is the value `X-App-Build`
   reports, the embedded bundle's meta, legacy or shell, or `dev` without one (06, FE7-13) — so it
   imports nothing above L6, FE3-16), and
   `uihttp` for `ui/diagnostics` and `ui/csp-report`, so `ui` registers nothing on `CoreGroup` itself; layer L6,
   imported only by `kernel`, 03 §3.3, COR-18, COR2-19); each module's `Register(r module.Registrar)`
   receives `r.HTTP() *httpapi.Group` and, for ABO, calls `MountLegacy`. Registration only
   **records**; nothing touches the mux.
3. Kernel calls `Router.Build()`: validates every route (permission present and in catalog, scope
   namespace rule from 05-rbac §4.9, binding tags, schema generation, snake_case names, the legacy
   manifest against the gin engine, `LegacyAuthGin` and `OwnAuth` admission of §8.2, alias targets and
   claims, cross-owner shadowing), validates prefix claims and the `/` owner,
   registers into a fresh `ServeMux`, assembles the chain, and returns `errors.Join` of every
   problem. Any error aborts boot **before listeners open** (03 §3.7). A module's native
   route may use `rbac.Public` only when the module's `Manifest.PublicRoutes` names it (06 §6.4.2;
   hello's S2 `getGreeting`), else `ErrPublicNotAllowed`; the set of every `Public` route (core and
   module native, alias, legacy) is also a reviewed golden, `TestPublicRoutesGolden`, like
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
| Pattern conflict / duplicate / prefix collision / two owners of `/` | boot error | closed | silent shadowing is worse than not starting |
| Native JSON property not snake_case | boot error `ErrJSONName` | closed | one wire convention (FE-12) |
| Legacy route missing from the manifest or without a permission | boot error `ErrLegacyManifest` | closed | R6 over the running table (SEC-13) |
| `LegacyAuthGin` mount not named by `app.Options.LegacyAuthGinUntil`; `OwnAuth` route naming no compat method its module registered | boot error `ErrLegacyAuthGin` / `ErrOwnAuthMethod` | closed | no module skips stages 8, 12, 13 by self-declaration (SEC2-17) |
| Alias with an unknown target, or several targets and no `Request` | boot error `ErrAliasTarget` | closed | SEC2-5 |
| Shell route segment equals a non-SPA server segment | boot error `ProblemSPASegmentCollision` (06) | closed | FE2-7 |
| Alias or legacy route outside its owner's claims; a cross-owner more specific pattern | boot error `ErrAliasOutsideClaim` / `ErrShadow` | closed | silent capture of another owner's requests (SEC3-8) |
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
  package contributes — `time.Time` comes from `jsonschemax.Base()` (03 R4, SIMP3-1), and httpapi's
  own entries are `rbac.Permission`, `json.RawMessage` → `{}` and `Page[T]`/`CursorPage[T]` → generic
  expansion `Page_<T>` (COR4-21); component
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
  subcommand, from S2, into `core/openapi/core.json` (outside any npm package, which UI-0 creates
  later), and UI-0's api-client generator reads it from there (07 §7.8, 06 `Description.Core`;
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

- `@mediacore/api-client` = generated `paths` for core + `createCoreClient()` built on
  `openapi-fetch` with middleware ported from `apiFetch`: `credentials: 'include'`, timeout
  (`ApiTimeoutError`), SSO bounce detection (`ApiAuthRedirectError`), `X-Request-ID` surfacing,
  the stale-build rule of 07 §7.5.2 (the runtime exports `checkBuild(response, opts): StaleReason |
  null` and `onStaleBuild(listener): Unsubscribe` from UI-0, and ABO's `apiFetch` calls `checkBuild`, so
  legacy and native calls run one implementation; FE4-9, FE7-5).
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
  readPageBuildId(), shell: false })`; ABO's pre-shell client for generated operations (S4–S8) is one
  `createCoreClient({ buildId: readPageBuildId(), shell: false })` in `web/src/api/core.ts` from UI-0;
  `createShell` passes `{ buildId: readPageBuildId(), shell: true }`; and module clients come from
  `createModuleClient<Paths>(coreClient)`, which inherits both values. `checkBuild` returns the stale
  reason or `null`, and `onStaleBuild` fires once per page load per reason, so ABO's pre-shell banner
  (S0–S8) and the shell banner (S9+) subscribe to one signal that native and legacy calls both raise
  (07 §7.5.2 owns the banners; `buildId.test.ts` asserts that a stale response on a native call and one
  on a legacy call notify the same listener). No `core/web` package reads `import.meta.env.MEDIACORE_*` except
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
  `web/src/services/ops.ts`) follows one rule (FE4-7, shared with 07 §7.8 and 04): its input
  `internal/operations/state` **stays in ABO** as books' legacy run-state package (resolution R6-2,
  MOD6-1: the v2 table and HEAD's V3 vocabulary that `ops.ts` emits exist in no core package, and core's
  `tasks.RunState` maps to them in books' taskstore), so `opsgen` keeps its HEAD input and its
  freshness gate stays byte-identical with no repointing at S5; it retires with the PR that makes the
  **last** `ops.ts` importer native (end of 07 §7.19 step 6; 04 maps it at S10), not at a fixed stage.
  `ops.ts` is imported by `web/src/services/api.ts` and by `web/src/utils/operationPolling.ts`, whose
  importers include books pages that keep legacy calls after S9 (`Library.tsx`, the review lanes,
  `ITunesImport.tsx`, `stagedMetadataApply.ts`, `MaintenanceTab.tsx`, `ThrottlePanel.tsx`); the S9 PR
  moves only the Activity page and `useOperationsStore` off it (07 §7.8). The golden contract test
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
| S2 (H0) | `core/httpapi` exists; ABO's `main` builds `app.New(opts, books.Module())` with `app.Options.LegacyAuthGinUntil: "books"`; `books` calls `MountLegacy(Auth: LegacyAuthGin)` with the whole gin engine; **every manifest route is its own mux pattern**; at **S2 r2** the perimeter moves to core stages 0–11 and is removed from gin in the same PR (logger, recovery, security headers with `DefaultCSP` = today's per-response `contentSecurityPolicy()` and `Reporting-Endpoints` (FE2-1), CORS, gzip with HEAD's per-path exclusions, tracing, the `/api` rate limiter with per-route `RateClass`), **except the Basic gate (stage 8), which stays in gin until S3** because core reads its password, a legacy encrypted secret row, only through the config and secrets stores that land at S3 (09 S2); stage 5 emits `X-App-Build` on every response (FE2-5, OPS2-13). **Perimeter settings keep their UI-saved values (COR3-1, R3-1):** the perimeter keys (`core.http.rate_limit.*` = `enable_rate_limit`/`api_rate_limit_per_minute`, CORS origins, peer allow-list; from S3 also `core.http.basic_gate.*` = `basic_auth_enabled`) are read through config's override store from S2 — for ABO the books-supplied legacy config store over ABO's settings blob (05-config; resolutions R4-1, R4-6) — so a value saved only in the UI and a live edit through `PUT /api/v1/config` reach stages 0, 6 and 10 as they do today (and stage 8 from S3; until then gin's `basicauth.go:26` reads it live). For these legacy keys books' store keeps HEAD's precedence until cutover: a value persisted through the UI wins over an environment default exactly as at HEAD, so compose's always-set `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}` (`docker-compose.yml:20`) cannot switch off a gate enabled in the UI (resolution R5-1(a), OPS5-1; 05-config); done-check `TestPerimeterHonoursBlobOnlyValues`; cross-origin protection (stage 7) is new, and its refusals go to the access log as `security_event` until `audit` exists at S4 (SEC2-20); the 36 permission-less routes are annotated `AnyAuthenticated` in the manifest (R6 must hold at S2), and **every `s.perm(atom)` route's `Perm` records `rbac.LegacyAtom(atom)`** from the S2 leaf constructor, which needs no catalog (05-rbac §3, COR6-1): the reviewed manifest is final from S2, nothing re-annotates those entries later (only `/metrics` changes, at S4 r2), and stage 13 starts enforcing the recorded values at S4 r2 (`TestS2Manifest_SPermRoutesCarryLegacyAtom`). **Client address (SEC2-19):** stage 0 resolves it and publishes it (`ClientAddrFrom`); ABO's `client_trust.go` stays in ABO until S4, and from S2 its `trustedClientIP` (used by the bootstrap, invite and ABS limiters, `bootstrap_rate_limit.go:207`, `invite_rate_limit.go:405`, `client_trust.go:600`) starts from that value and keeps only its "local peer + verified Access assertion" branch on top; at S4 that branch moves into ABO's `internal/cfaccess` authenticator | gin only (CF binder, `RequireAuth`, `s.perm`, `RequireAdmin`, `credGuard`, `RequireOwner` unchanged); core stages 12–13 off for the mount | revert to the old `main`; storage untouched |
| S3 | The Basic gate (stage 8) moves from gin into the core perimeter with HEAD's exempt set (`OwnAuth`, `BasicExempt`, `BasicExemptAssets`; SEC-14, COR4-9) and refuses an enabled gate with no password (§4.2 stage 8, R5-1(c)); stage 8 reads `core.http.basic_gate.*` through config (§10), and for ABO books' legacy config store serves the password from HEAD's own encrypted row and `basic_auth_enabled` with HEAD's precedence (R5-1(a),(b)); `basicauth.go` is deleted from gin in the same PR (04 §7) | gin only, as S2 | binary swap. The Basic password is **not moved** during the extraction: HEAD already stores it encrypted (`internal/config/persistence.go:1955-1958`), books' store reads and writes that row in place, and no sentinel is written (resolution R5-1(b), OPS5-2). After a rollback the older build's gate accepts the same password, a password re-entered there is the one the next roll-forward reads, and a password cleared there leaves core's gate closed (stage 8) and the hotfixed older gate closed (05-authn §11.1 item 5) |
| S4 | **r1 (dark):** `auth` and `rbac` land and are built over the stores the books module supplies (`module.Manifest.LegacyStores`, 05-authn §3.4, 05-rbac §3.4: ABO code over ABO's own rows, so core holds no audiobook key name and no storage grant is involved, resolution R4-1); the kernel runs their read-only decode census at P4d; the mount stays `LegacyAuthGin`, no native route exists, and nothing is written through the stores (COR4-4). **r2 (flip):** `LegacyAuth` switches to `LegacyAuthCore`, and the same PR removes `LegacyAuthGinUntil` from `app.Options` (an archtest fails if it is still present after S4, SEC2-17): stage 12 binds the core principal and stage 13 enforces each manifest route's `Perm`/`Guards` — for an `s.perm(atom)` route, the `rbac.LegacyAtom(atom)` its entry has recorded since S2: for a role principal `AllOf` the native scope set equivalent to the atom's **full** legacy reach (resolution R4-3), for a key whose scopes come from its legacy row (no extension record holding canonical scopes, 05-authn §4.4) HEAD's role ∩ key reach (SEC5-7, SEC6-2), for the auth-disabled principal the atom's expansion (COR5-4) (§8.2, 05-rbac §3.1, §6.3); the same PR re-annotates the legacy `/metrics` entry from `AnyAuthenticated` to `core:metrics.read`, so scoped legacy scrape keys change at S4, as 09 §9.2.6 records (OPS5-5, SEC5-13; `TestLegacyMetricsRoute_RequiresScopeFromS4`); inside gin the auth middleware becomes the bridge of §8.3 (it never authenticates; its authorization adapters still enforce through `rbac.Require`); core auth/users/roles/keys/sessions routes and the settings routes (`/api/core/v1/settings`, `/settings/schema`, 05-config §1.3.4; FE3-11; until S4 settings go through ABO's `/api/v1/config`) go native under `/api/core/v1` and the legacy identity paths become `Alias`es with targets and selectors (§4.6; more specific than the gin patterns they replace, which are deleted in the same PR), except paths that cannot be mapped (`PUT /api/v1/auth/me/password` with `user_id` stays a gin route calling `auth.Service`, SEC2-5); the core factory-reset route lands in `internal/storagehttp` and `POST /api/v1/system/factory-reset` becomes its `Alias` (from S2b until this PR books' gin handler, behind HEAD's chain, only stages the reset through `legacy.Maintenance.StageFactoryReset`; no core route exists before r2; 05-rbac §6.2, resolution R7-2); stage 7 and stage 13 refusals are audited from here | r1: gin only, as S2–S3; r2: core; gin bridge | binary swap (r2 → r1 and S4 → S3). Identity rows keep their one stored format and are read and written by books' own legacy stores in both r1 and r2, so a rollback between the S4 releases never meets a missing grant (DATA4-2). Native-only data (canonical scopes, hashed sessions, native one-time tokens) lives in a books-owned extension family that older builds never read, never as extra strings in legacy rows (SEC4-3): an older build refuses every key and user restricted here, grants a role only the legacy atoms the row rule admitted, and its browser users sign in once; an extension whose legacy row an older build changed is suspended on roll-forward (05-authn §11, 05-rbac §6.3). There is no rollback floor for S4 and no identity level step |
| S5, S6, … | each core subsystem's routes go native in the stage that moves it (tasks S5, events S6, …), legacy paths aliased; at S6 the three legacy event streams stay gin manifest routes (`Perm` `AnyAuthenticated`, `OwnAuth` false) whose handler is the realtime projection obtained through `Registrar.Realtime().Projection` (05-realtime §4.9, COR2-1) | core | per stage |
| any later | a books route group moves to `/api/books/v1` **only when rewritten for its own reasons** (SIMP-2); its old paths become aliases | core | per group |

The S2 done-check in 09 covers the perimeter move: CSP header goldens per owner
(the legacy SPA response carries the inline-script hash and the configured Access team host, and
`Reporting-Endpoints`), `X-App-Build` on every response class in the perimeter goldens, the
rate-limit class golden, the cross-origin test, the bootstrap, invite and ABS limiter tests unchanged
(SEC2-19), ABS conformance fixtures and the live smoke. The S3 done-check covers the Basic gate's move:
its exempt set equals HEAD's (`TestBasicGate_ExemptSetEqualsHEAD`), the gate enabled only in the
blob turns stage 8 on also with compose's `BASIC_AUTH_ENABLED=false` in the environment (the
Basic-gate half of `TestPerimeterHonoursBlobOnlyValues`, OPS5-1), an enabled gate with no password
refuses (`TestBasicGate_EmptyPasswordRefuses`), and the R-test row keeps the password across a
rollback (`TestBasicGate_PasswordReadInPlaceAcrossRollback`, OPS5-2).
`TestLegacyRoleReachUnchanged` (572 routes × 4 roles, plus scoped keys, auth-off and a custom-role half
over random native roles, SEC4-2) belongs to S4 r2, where the engine moves, and stays a permanent CI
test because the mount is permanent.

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
	SPAShared  []string              // top-level segments shared with SPA pages, excluded from NonSPA (ABO: "login"; "invite" joins at S9 with the invite 302 route; §3.5, SEC3-16, FE4-2)
	BuildID    string                // stage 5's X-App-Build until S9: the <meta name="mediacore-build"> of the mount's embedded index.html, the only UI-build-ID source; never the binary's identity (FE3-2, FE4-1, R5-7)
	BasicExemptAssets func(path string) bool // stage 8 exempts these paths while the mount owns "/": HEAD's static-asset prefix/suffix rule (basicauth.go:40-46, COR4-9)
}

type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement // s.perm atom: rbac.LegacyAtom(atom), recorded from S2 (COR6-1), enforced from S4 r2 — roles: Catalog.LegacyReach(atom), AllOf the atom's full legacy reach (R4-3); keys whose scopes come from the legacy row: role ∩ key atoms (SEC5-7, SEC6-2); auth-disabled: the expansion (COR5-4); 05-rbac §3.1, §6.3. Admin kind: 05-rbac §6.2; Delegated when OwnAuth
	Guards          Guards           // from credRoute / RequireOwner recording
	Public          bool             // on the reviewed list of 15 (raw/09 §2.4)
	OwnAuth         bool             // the route's gin chain runs a compat authenticator (ABSRequireAuth); stages 8 (Basic), 12, 13 skip it (SEC-14)
	BasicExempt     bool             // stage 8 skips it: HEAD's two health paths, /api/health and /api/v1/health (basicauth.go:34, COR4-9); TestBasicGate_ExemptSetEqualsHEAD
	AuthMethod      string           // required iff OwnAuth: the compat method ("books.abs") the owner registered (SEC2-17)
	RateClass       RateClass        // RateDefault iff the gin chain includes the API rate limiter today, else RateExempt
	NoCompress      bool             // stage 9 skips it: HEAD's compressionMiddleware exclusions (§4.2 stage 9, COR3-6); TestCompressionExclusionsEqualHEAD
}
```

**Admission (SEC2-17).** `MountLegacy` is a permanent public API, so its two ways of skipping core
stages are admitted by the kernel, not self-declared. `Auth: LegacyAuthGin` (stages 12–13 off for the
whole mount) is accepted only while the compile-time `app.Options.LegacyAuthGinUntil` names the
mount's owner (ABO sets `"books"` at S2 and removes it in the S4 PR; an archtest fails if the field is
set after S4), else `ErrLegacyAuthGin`. `OwnAuth: true` (skips stages 8, 12, 13) is accepted only when
`AuthMethod` names a compat method the same module registered with `Registrar.AuthMethod` (05-authn
§3.3), else `ErrOwnAuthMethod`. `Registrar.AuthMethod` is an S2 member that only records the
`auth.MethodSpec` leaf value (06 §6.4.10), so admission holds from S2, when ABO's ABS routes are first
`OwnAuth` for stage 8's Basic exemption, and the method is validated by `auth` once it exists at S4
(SEC3-14, COR3-3);
`rbac.Delegated` is accepted only on such routes. The set of `OwnAuth` routes is a reviewed golden
(`TestOwnAuthRoutesGolden`).

- **Legacy redirect routes (FE3-5, FE4-2).** From S4 the books manifest rewrites the existing
  `GET /auth/temp-login` to answer the 302 of 05-authn §4.13 with `Referrer-Policy: no-referrer`.
  Invite links keep the `/invite/<token>` path form that the legacy SPA opens
  (`web/src/App.tsx:201-206`) until the shell stage: from S4 to S9 `GET /invite/<token>` is served by
  the legacy `/` fallback as at HEAD, and the page posts the token in a body. From S9, when core mints
  `/invite#token=` links, the manifest adds `GET /invite/{token}` (Public, `RateAuth`, under
  `SPAShared` segment `invite`) answering the 302 for path-form links issued before the shell, for its
  window (09 §9.6). Each is the only implementation for its window: `ui.Handler` serves `GET /invite`
  (the page) and the more specific manifest pattern answers `GET /invite/<token>`. Test
  `TestLegacyInviteRedirect_SPASharedSegment` (`GET /invite` serves the page; `GET /invite/<t>` 302s
  with `no-referrer`; from S9).
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
| `RequireAuth` (store nil → pass; session/key lookup) | `middleware/auth.go:152-262` | `legacyauth.FromCorePrincipal`: reads the `auth.Principal` bound by stage 12 and fills the legacy context values (`auth.WithUser`, `WithPermissions` with the legacy atoms whose `rbac.LegacyAtom` the principal's checker satisfies, 05-rbac §6.3 — none for the auth-disabled principal, as HEAD binds none with auth off, SEC5-9 — `WithMethod`); never authenticates; a missing principal on a non-Public route is a defensive 401 (stage 13 already answered) |
| zero users → pass everything | `middleware/auth.go:165-169` | gone: stage 13 allows only Public + setup/bootstrap (05-authn §4.2) |
| worker-only path confinement | `middleware/auth.go:265-298`, ABS half `middleware/absauth.go:629-632` | `Confining` scopes at stage 13 (05-rbac §4.10) for every route stage 13 runs on; for `OwnAuth` (ABS) routes, which stage 13 skips, the ABS adapter keeps the check (next rows) — `abortWorkerOnlyOutsideWorkerAPI` is **replaced**, not deleted (SEC2-4) |
| Cloudflare Access fail-open binder | `middleware/cfaccess.go:20-139` | ABO's `internal/cfaccess` authenticator (04 §2.4), registered app-level through `app.Options.Authenticators` (05-authn §3.3, SEC2-16), runs in stage 12; gin binder removed |
| `s.perm(atom)` | `server_lifecycle.go:1693-1698` | adapter over `rbac.Require(ctx, rbac.LegacyAtom(atom))`, the same requirement as the route's manifest `Perm`, so stage 13 and the adapter agree (COR5-4). A role principal needs `Catalog.LegacyReach(atom)`: `AllOf` the native scope set equivalent to the atom's **full** legacy reach, taken from the frozen reach snapshot, not the atom's expansion — so a custom role holding part of `settings.manage`'s reach passes no `settings.manage` route (resolution R4-3, SEC4-2, SEC4-4). A key whose scopes come from its legacy row (a pre-core `abk_` key with no extension record holding canonical scopes, 05-authn §4.4) passes iff the atom is in its stored atoms (or the list is empty) and its owner's roles satisfy that reach, HEAD's role ∩ key semantics (SEC5-7, SEC6-2). The auth-disabled principal is judged against the atom's expansion, as HEAD's `s.perm` passed every request with auth off (05-rbac §3.1, §4.11) |
| in-handler `auth.Can(ctx, atom)` | `internal/auth/context.go:171-178`; sites in the reach snapshot's in-handler list | each site is replaced by `legacyauth.Can(ctx, rbac.LegacyCheck(atom, readSide))` with the site's entry from the snapshot's in-handler list (an ABO archtest finds no raw `auth.Can` left in the mount): `LegacyAtom`'s role and key rules, except that the auth-disabled principal is denied, as HEAD's `auth.Can` returns false with no permission set bound (sites with their own auth-off bypass keep it), and a `read_side` site is judged against its read-side successor (`core:users.view` for `users.manage`; 05-rbac §6.1, §6.3; SEC5-9, SEC5-14) |
| `RequireAdmin` by role name; `adminRole` pass-through vs 401 with auth off | `middleware/auth.go:522-537`, `wire_admin_role.go:18-45` | the manifest carries 05-rbac §6.2's permission per admin kind; the gin gate becomes `rbac.Require(that permission)`; auth-off reach follows from the auth-disabled checker |
| `credGuard` | `credential_routes.go:79-87` (a pass-through with auth off) | `Guards.Interactive` at stage 13; gin adapter checks `Principal.Method.MayChangeCredentials()`. The auth-disabled principal is refused, a recorded auth-off tightening from S4 (05-authn §4.11, 05-rbac §4.11, SEC5-8) |
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
| `internal/server/csp.go` (`contentSecurityPolicy()`, team-host pointer) | stays in ABO until S9 as `LegacyMount.DefaultCSP` (it hashes ABO's embedded `index.html` and reads the deployment's Access host, `csp.go:34-46,95-106,129-138`); at S9 the policy builder (`cspBase`) is `core/ui`'s and the Access host becomes ABO's `ConnectSrcFrom` (FE3-4, FE3-10) |
| `internal/server/csp_report.go` | `core/internal/uihttp` (`/api/core/v1/ui/csp-report`) at S4; gin's `/api/v1/csp-report` stays the legacy mount's `CSPReport` until S9, then its handler is deleted |
| `internal/server/static_embed.go` | `core/ui` at S9 |
| `internal/server/middleware/ratelimit.go`, `request_size.go` | stages 10–11 |
| `internal/server/middleware/basicauth.go` | stage 8 with per-route exemption, at 09 S3 (it stays in gin at S2) |
| `internal/server/gone.go` | `httpapi.Gone` (native) — legacy stubs stay in gin |
| `internal/httputil/respond.go`, `types.go`, `parse.go` | `httpapi.Problem`, `Page[T]` for native routes; legacy shape kept for aliases and gin |
| `internal/httputil/rangeserve.go` | stays app-side helper used via `HandleRaw` (already `net/http`) |
| `internal/plugin/router.go`, `internal/server/plugins_init.go:42` | deleted at 09 S7 with `internal/plugin` (N4; 04 §8.3); replaced by the module `Group` |
| `tygo.yaml`, `tools/cmd/opsgen`, `internal/wirecontract` | kept for legacy-mount types (`opsgen` keeps its HEAD input `internal/operations/state`, which stays in ABO, and retires with the last native `ops.ts` importer, §7; R6-2); native routes use OpenAPI + `coretest.ContractGoldens` (§7) |
| `web/src/services/api.ts` (8,747 lines), `web/src/utils/apiFetch.ts` | calls to native routes use `@mediacore/api-client` / the generated `web/src/modules/books/api/` directory; calls to legacy routes keep `apiFetch`, which calls the runtime's `checkBuild(response, { buildId: readPageBuildId(), shell: false })` from UI-0 (§7, FE4-9, FE6-1, FE7-5) |

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
| `core.http.basic_gate.enabled` | bool; 05-config's `BelowStored` alias `basic_auth_enabled` / `BASIC_AUTH_ENABLED` maps to it. **An operator's tightening survives a rollback (resolution R7-1, SEC7-1):** the http section declares, **from S3**, `config.NarrowStored("core.http.basic_gate.enabled", narrow = value == true, when = core.http.basic_gate.password has a stored value (`Secret.Set` with `SourceStored` or `SourceLegacy`))` (05-config §1.3.1); from S3, when stage 8 reads the key through config, an effective `true` from an operator layer (`ABO_CORE_HTTP_BASIC_GATE_ENABLED`, the operator file) over a stored `basic_auth_enabled: false` makes the legacy config store write `basic_auth_enabled: true` at P4d step 1 (audited `core.config.legacy_narrowed`, 05-config §1.3.5; 05-authn §4.11 applies the same rule to `enable_auth`), **only when a stored password exists**, so an older build after a rollback keeps the gate on with the same password; with no stored password nothing is written, and stage 8 here and early fix 5 on a hotfixed older build keep the gate closed (`TestRollback_OperatorEnabledAuthStaysOn`'s Basic-gate cell) | false | yes | admin |
| `core.http.basic_gate.username` | string; 05-config's `BelowStored` alias `basic_auth_username` / `BASIC_AUTH_USERNAME` maps to it (SEC6-9). Stage 8 compares it in constant time. An enabled gate with an empty username keeps HEAD's behaviour (HEAD's default is `""`, `internal/config/config.go:2806`, and its gate then admits `:<password>`): the password is the secret, and refusing an empty username would lock out every deployment that set only a password, with no security gain; an empty **password** is what stage 8 refuses | `""` | yes | admin |
| `core.http.basic_gate.password` | secret (one canonical name; 05-config's alias `basic_auth_password` maps to it, SEC4-10); core reads it from S3, when the gate moves (until then gin reads it itself). It is **not moved** during the extraction: for ABO, books' legacy config store reads and writes HEAD's own **encrypted** row in place, with no sentinel, so a rollback leaves the older gate as configured (resolution R5-1(b); 05-config). Enabled with this unset, empty or unreadable → stage 8 refuses (R5-1(c)) | — | yes | admin |
| `core.http.openapi.public` | bool | false | yes | admin |
| `core.http.read_header_timeout` / `idle_timeout` | duration | 15s / 120s | no | none |

ABO's flat keys (`json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`,
`api_rate_limit_per_minute`, `basic_auth_enabled`, `basic_auth_username`, `allowed_peer_cidrs`;
`internal/config/config.go:1554-1559,1578,1719`) and the env name `TUNNEL_CONNECTOR_ADDRS`
(→ `core.http.trusted_proxies`, zone semantics kept, `client_trust.go:133-201`) are aliased by
05-config's key-alias mechanism for the compat window of 09 §9.2.1. A stored value equal to the default
still beats a `BelowStored` legacy name (05-config §1.3.5, SEC7-2): a blob's `enable_rate_limit: true`
(HEAD's default, `internal/config/config.go:2802`, saved by HEAD's whole-config save) keeps stage 10 on
when the legacy env name is set to false, as at HEAD.

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
| `TestBasicGate_ExemptSetEqualsHEAD` | golden of HEAD's exempt set (`basicauth.go:34,40-46,69`): the `OwnAuth` ABS routes, `/api/health` and `/api/v1/health` (`BasicExempt`), and the static-asset prefix/suffix rule on the `/` owner (`BasicExemptAssets`); `/ping`, `/status`, `/login` stay gated (SEC-14, COR4-9) |
| `TestBasicGate_EmptyPasswordRefuses` | S3 on: with `core.http.basic_gate.enabled` true and the password unset, cleared, empty or unreadable, every non-exempt request → 401 (also `<username>:`), health `basic_gate_no_password`, the startup log names the key; the exempt set still passes (R5-1(c), SEC5-4, DATA5-12) |
| `TestBasicGate_UsernameCompared` | S3 on: a wrong username with the right password → 401; with `core.http.basic_gate.username` empty and a password set, `:<password>` passes as at HEAD; `BASIC_AUTH_USERNAME` is a `BelowStored` alias (SEC6-9) |
| `TestBasicGate_PasswordReadInPlaceAcrossRollback` | (R-test cells) Basic password saved on `cur` is HEAD's encrypted row: roll back, `prev`'s gate accepts it and refuses `<username>:`; re-entered on `prev`, roll forward: `cur` accepts the re-entered one; cleared on `prev`, roll forward: 401 for `<username>:` (R5-1(b),(c), OPS5-2, SEC5-4) |
| `TestCSP_DefaultPerOwner` | SPA, legacy `DefaultCSP(r)`, native default; a handler-set CSP is kept (FE-4); the legacy SPA response carries the inline-script hash, the configured Access team host and `Reporting-Endpoints` (FE2-1) |
| `TestPerimeter_XAppBuildOnEveryResponse` | legacy, native, infra, 404, 405 and 410 responses all carry `X-App-Build` (FE2-5, OPS2-13) |
| `TestInfra_MetricsRequiresScope` | `/metrics` without a credential → 401, with a key lacking `core:metrics.read` → 403; `/livez`, `/readyz` stay Public (SEC2-14) |
| `TestLegacyMetricsRoute_RequiresScopeFromS4` | S4 r2 to S7: the legacy `/metrics` manifest entry is `core:metrics.read`; a scoped legacy key without it → 403, an `editor` session → 200 (OPS5-5, SEC5-13) |
| `TestClientIP_ZonedConnectorTrusted` | zoned link-local connector peer: header believed, peer admitted (SEC-18) |
| `TestTrace_NoApikeyInSpanAttributes` | every `log.DefaultRedactQuery()` value (`apikey`, `token`, `code`, `code_verifier`, `refresh_token`, …) absent from span attributes and the access log (SEC-22, SEC2-18; the list's superset-of-HEAD test is `TestRedactQuery_SupersetOfHEAD` in 05-logging) |
| `TestAccessLog_NoInviteToken` | no one-time token in the access log, journal or span attributes (SEC-21) |
| `TestStage12_InvalidKeyPlusValidSessionIs401` (05-authn §10 owns it: stage 12's stop rule, refused at stage 13) | stop rule (SEC-12) |
| `TestStage13_StaleCookieOnPublicRouteIsAnonymous` | cookie cleared, request proceeds (SEC-11) |
| `TestFacadeKey_RefusedOnCoreAndModuleGroups` (deferred-build, §3.6) | SEC-5 |
| `TestConfining_WorkerKeyRefusedOnABS` | an `fp-worker` key and a worker user's ABS login get ABS 403 on every ABS route whose `Perm` does not name `books:fingerprint.worker` (SEC2-4) |
| `TestProblem_ShapeGolden` / `TestLegacyError_ShapeGolden` | both envelopes byte-for-byte |
| `TestUnmatched_APIPathIsProblemNotHTML` | `/api/nope` → problem 404; `/library` → SPA owner |
| `TestUnclean_PathNotRedirectedByMux` | `//ping` is not 301-redirected; the `/` owner decides |
| `TestSPA_NotRateLimitedAndPublic` | 200 cold-load requests to `/` owner are not 429; `RouteInfo{Kind: SPA}` present (FE-25) |
| `TestSPA_SingleOwnerOfRoot` | legacy `ServesSPA` + `Router.SPA` → `ErrPrefixCollision` (FE-1) |
| `TestBasicGate_SPAOptionsExemptAssets` | from S9: `Router.SPA(h, SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})` exempts exactly HEAD's asset set on the shell; `TestBasicGate_ExemptSetEqualsHEAD` repeated in the S9 PR passes (FE5-10) |
| `TestNonSPA_PredicateCoversClaims` | `/socket.io`, `/API/V1/x`, `POST /library`, `/health/`, `/HEALTH`, `/metrics/`, `/livez/` never reach the SPA handler; `/assets/x.js` and `GET /login` do (FE-3, FE2-6) |
| `TestNonSPA_LegacyFallbackConsultsPredicate` | with the legacy mount owning `/`, `/livez/` and `/READYZ` get problem 404, never gin's `NoRoute` page (FE3-13) |
| `TestMethodNotAllowed_HasAllow` | 405 + `Allow` |
| `TestBind_TagsCompiledAtRegistration` | bad tag → `ErrBadBinding` at boot |
| `TestDecode_StrictUnknownField` / `TestAlias_UnknownLegacyFieldRefused` | 400 `unknown_field` native; 400 `legacy_field_unsupported` on an alias for a field neither the selector nor the target knows (SEC2-5) |
| `TestAlias_CreateKeyForOtherUserKeepsTarget` | legacy `POST /api/v1/auth/api-keys {user_id}` reaches `createUserAPIKey` for that user, bounded by the target's roles and dominance; without `user_id`, `createOwnAPIKey` (SEC2-5) |
| `TestPage_OffsetDefaultHasTotal` / `TestCursor_FilterMismatch` | SIMP-8 |
| `TestTaskStart_IdempotencyKeyReplaysRunView` | a repeated `Idempotency-Key` on task start → 200, the existing `RunView`, `Idempotent-Replayed: true` (SIMP2-8; store in 05-tasks) |
| `TestAlias_DeprecationHeadersAndCompatCounter` | headers rendered from the ledger row of `app.Options.CompatLedger` (or core's fragment); `mediacore_compat_legacy_use_total{surface="http"}` increments (SIMP3-3) |
| `TestAlias_OutsideClaimRefused` / `TestBuild_CrossOwnerShadowRefused` | a module alias at `GET /api/core/v1/auth/oidc/google/callback`, and a legacy route outside its mount's `LegacyPrefixes`, → `ErrAliasOutsideClaim`; one owner's pattern strictly more specific than another's → `ErrShadow`; 09 S4's ledger-listed core aliases pass (SEC3-8) |
| `TestPublicRoutesGolden` / `TestBuild_PublicNotInManifestRefused` | the reviewed set of every `Public` route, native, alias and legacy; a module native route with `rbac.Public` absent from `Manifest.PublicRoutes` → `ErrPublicNotAllowed` (COR3-3) |
| `TestCompressionExclusionsEqualHEAD` | golden from HEAD's `compressionMiddleware` lists: the four legacy exclusions carry `NoCompress`; `text/event-stream` responses are never gzipped; the log download is gzipped once (COR3-6) |
| `TestPerimeterHonoursBlobOnlyValues` | S2: rate limit disabled only in ABO's settings blob → stage 10 off; a runtime edit through `PUT /api/v1/config` applies without restart (COR3-1); S3: Basic gate enabled only in the settings blob → stage 8 on, also with compose's `BASIC_AUTH_ENABLED=false` in the environment (R5-1(a), OPS5-1) |
| `TestBuildID_HeaderEqualsEmbeddedMeta` | for a `make build`, a Docker build and a goreleaser build, `X-App-Build` equals the embedded `index.html` meta value, which for every `embed_frontend` artefact is neither `""` nor `dev`; no Go build step is passed a UI build ID, and `X-App-Build` never equals `app.BinaryID` (FE3-2, FE4-1, FE5-1, FE5-3) |
| `TestOpenAPI_UsesConfigOverrides` | a `config.Duration` field renders as `string/duration`, the same schema as in the settings schema (SIMP3-1) |
| `TestLegacyInviteRedirect_SPASharedSegment` | from S9: `GET /invite` serves the page; `GET /invite/<t>` → 302 `/invite#token=<t>` with `Referrer-Policy: no-referrer` (SEC3-16, FE3-5, FE4-2) |
| `TestAlias_ServedPastAdvisorySunset` | fake clock past the ledger row's earliest-removal date → still served (OPS-10) |
| `TestRequestID_GeneratedAndPropagated` | header, problem, slog record, audit record carry the same id |
| `TestRateLimit_ClientBuckets` | `synctest` time; headers; legacy `RateClass` golden |
| `TestOpenAPI_Golden` / `TestOpenAPI_IsValid31` | generated doc equals committed file; validates against the 3.1 meta-schema |
| `TestContractGoldens_ValidateAgainstSchema` | every example matches its schema |
| `archtest.RoutesHavePermissions` | over native + legacy manifest + SPA/infra set (R6) |
| `TestS2Manifest_SPermRoutesCarryLegacyAtom` | S2 on: every manifest entry recorded from an `s.perm(atom)` gin route has `Perm == rbac.LegacyAtom(atom)`, built with no catalog; `Handle` refuses a `LegacyAtom`/`LegacyCheck` requirement; a `LegacyAtom` manifest is accepted under `LegacyAuthGin` at S2 and under `LegacyAuthCore` from S4 r2, and S4's `Freeze` refuses an atom no legacy alias names (COR6-1) |
| `TestLegacyManifest_MatchesGinRoutes` / `TestLegacyManifest_RegistersAsMuxPatterns` | `ErrLegacyManifest` on drift; every gin route is a mux pattern, no conflict (SEC-13) |
| `TestLegacyMount_AliasShadowsGinRoute` | after `Alias`, the gin handler for that method+path is never invoked |
| `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash` | a request with `%2F` in a segment whose gin match differs from its mux pattern → 404, no gin handler runs (SEC2-22) |
| `TestOwnAuthRoutesGolden` / `TestLegacyAuthGin_RefusedWithoutOption` | the reviewed `OwnAuth` set; `LegacyAuthGin` without `LegacyAuthGinUntil`, and `OwnAuth` without a registered method → boot errors (SEC2-17) |
| `TestLegacyRoleReachUnchanged` (09 S4 r2, permanent) | 572 routes × 4 seed roles, scoped API keys (a pre-core key keeps HEAD's role ∩ key reach, SEC5-7), auth-off mode (`settings.manage` routes 200; in-handler `deny` sites deny; the recorded tightenings of 05-rbac §4.11 asserted refused; COR5-4, SEC5-8, SEC5-9); custom-role half: for random native roles, stage 13 admits a legacy route only if the route's successor admits (SEC4-2) |
| `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` | every manifest route gated by `settings.manage` or `users.manage` carries `Catalog.LegacyReach` of its atom; a custom role holding the four `settings.manage` expansion scopes gets 403 on backup restore, backup create, logs, tool install and plugin settings (R4-3, SEC4-2) |

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
- SEC2-22 — legacy wrapper passes `r.Pattern` to gin and refuses a mismatched gin match (§4.8, §8.2, §8.3, §12).
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
