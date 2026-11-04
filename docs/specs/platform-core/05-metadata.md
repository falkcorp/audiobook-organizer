<!-- file: docs/specs/platform-core/05-metadata.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d8ae2ec-73b0-4d41-b6e5-1531122e991c -->
<!-- last-edited: 2026-11-04 -->

# 05 — Metadata providers, `providerhttp`, and the integration-instance base

Evidence commit `748fd787a` (the checked-out branch adds docs only; code is identical).
Raw inputs: [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §3, §6;
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §9;
[`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md) §5.1, §7.1 row 1.

This file holds **three things**, because the other four integration specs
([05-ai](05-ai.md), [05-download](05-download.md), [05-indexer](05-indexer.md), [05-notify](05-notify.md))
stand on the first two:

| § | Contract | Package |
|---|---|---|
| M1 | Integration-instance base: adapter descriptors, per-instance configuration, host-owned secrets, health, test, error taxonomy | `integrations` (new root package, see "Objections to 03") |
| M2 | Outbound HTTP for every adapter: throttle, daily quota, circuit breaker, persisted holds, retry, egress policy, redaction — **per `App`, not package globals** | `integrations/providerhttp` |
| M3 | Generic metadata provider `Provider[Q, R]`, provider chain, answer cache | `integrations/metadata` |

Terms (03 §3.1): an **adapter** is an implementation (Deluge, Audible, SMTP); an **instance** is one
configured use of an adapter (a user may run two qBittorrent instances, or two Audible regions). Every
integration in core is addressed by `integrations.Ref{Kind, Instance}`, never by a provider-name string.

---

## M1. Integration-instance base (`integrations`)

### M1.1 Purpose & scope

One mechanism, used by all five integration kinds, for: declaring an adapter (settings schema,
egress policy, default limits); creating, updating and deleting **instances** as data; resolving
their secrets through core `secrets`; building, health-checking, testing and hot-swapping them; and
classifying their errors into one taxonomy. Out of scope: what an instance *does* (each kind's file).

### M1.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| No instance concept: every integration is a singleton built from flat config. Deluge is one cached package global built on first use and never rebuilt when its URL/password change. | `internal/deluge/integration.go:27-67` |
| Metadata providers are built by a hard-coded `switch src.ID`; adding one means editing that switch, `providerhttp.defaultLimits` and `provider_keys.go`. | `internal/metafetch/service_search.go:301-339`; `internal/metadata/providerhttp/providerhttp.go:60-75`; `internal/metadata/provider_keys.go:21-27`; raw/11 §3 |
| Secrets come from three places (flat fields, per-source `Credentials` map, env-bound keys); only 4 config secrets are encrypted at rest; Deluge password, metadata `Credentials`, webhook HMAC secret are plaintext. | `internal/metafetch/service_search.go:310-333`; raw/10 §7 rows "Which secrets use it"/"NOT encrypted"; `internal/plugins/webhook/plugin.go:58` |
| Test-connection is per-integration ad-hoc code reading the live global config (cannot test an unsaved form). | `internal/server/deluge_integration.go:57-80`; `internal/ai/openai_parser.go:877-889` |
| Two plugin frameworks plus `serviceregistry`; vestigial `plugin.DownloadClient`, `plugin.Notifier`, `plugin.MediaPlayer` have zero implementors. | `internal/plugin/plugin.go:51-102`; raw/11 §5 |
| Error classification exists four times with different answers to different questions: `aidispatch.Classify` (failover?), `ai.isPermanentAIError` (retry same endpoint?), `metadata.ClassifyProviderError` (how long to hold?), `providerhttp.shouldRetry` (retry in transport?). | `internal/aidispatch/classify.go:93-154`; `internal/ai/retry.go:44-57`; `internal/metadata/throttle.go:139-213`; `internal/metadata/providerhttp/providerhttp.go:351-361` |
| *arr ecosystems treat every provider (indexer, download client, notification) as `{Id, Name, Implementation, ConfigContract, Tags, Fields[]}` with a `/schema` endpoint. | raw/16 §5.1, §7.1 row 1 |

### M1.3 Go API

```go
// Package integrations is the kind-neutral base of every integration contract.
package integrations // github.com/falkcorp/mediacore/integrations

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
)

// Kind is an integration family. Closed set; a new kind is a core change.
type Kind string

const (
	KindAI       Kind = "ai"
	KindDownload Kind = "download"
	KindIndexer  Kind = "indexer"
	KindMetadata Kind = "metadata"
	KindNotify   Kind = "notify"
)

// InstanceID is operator-chosen, [a-z0-9][a-z0-9-]{0,31}, unique within a Kind.
type InstanceID string

// AdapterID is "<moduleID>.<name>": "core.qbittorrent", "books.audible".
type AdapterID string

// Ref addresses one instance. It is the ONLY key used for limits, holds,
// breakers, health, metrics and storage. String() is "<kind>/<instance>".
type Ref struct {
	Kind     Kind
	Instance InstanceID
}

func (r Ref) String() string { return string(r.Kind) + "/" + string(r.Instance) }

// Meta describes an adapter type. One per adapter, registered once.
type Meta struct {
	ID           AdapterID
	Kind         Kind
	DisplayName  string
	Version      string
	InfoURL      string           // public docs link, shown in the UI
	Settings     config.Schema    // field list; fields tagged Secret are stored via secrets (05-config)
	Egress       EgressPolicy     // enforced by providerhttp (M2)
	Limits       Limits           // providerhttp defaults for every instance of this adapter
	MaxInstances int              // 0 = unlimited
	Origin       string           // "core" or the registering module ID; set by the kernel
}

// Descriptor is what module.Registrar.Adapter accepts. Built only through a
// kind package's typed constructor (download.Adapter, ai.Adapter, ...), which
// calls NewDescriptor; the build func is unexported so a kind cannot be
// registered with an instance type its kind package does not accept.
type Descriptor struct {
	meta  Meta
	build func(ctx context.Context, env Env, inst InstanceConfig) (Instance, error)
}

// NewDescriptor is called by kind packages only (archtest.IntegrationsDescriptorCallers).
func NewDescriptor[T Instance](m Meta, build func(ctx context.Context, env Env, inst InstanceConfig) (T, error)) Descriptor

func (d Descriptor) Meta() Meta

// InstanceConfig is one stored instance row.
type InstanceConfig struct {
	Ref      Ref
	Adapter  AdapterID
	Name     string
	Enabled  bool
	Priority int                 // lower is preferred; meaning is per kind
	Tags     []string            // scoping (raw/16 §7.1 row 15)
	Owner    *auth.UserID        // nil: global (admin-managed); set: personal (notify only, 05-notify)
	Origin   Origin              // UI, File (read-only, lock reason shown), ArrFacade (08)
	Settings map[string]any      // validated against Meta.Settings; secret fields hold secrets.Ref only
	Limits   *Limits             // optional per-instance override of Meta.Limits (zero fields inherit)
	Version  uint64              // optimistic concurrency for PUT
	Updated  time.Time
}

type Origin string // "ui" | "file" | "arrfacade"

// Env is everything an instance may use. It is constructed per instance by
// the Registry and is the ONLY way an adapter reaches the outside world.
type Env struct {
	Ref     Ref
	Log     *slog.Logger                 // child logger: integration.kind, integration.instance, adapter
	HTTP    HTTP                         // instance-scoped; implemented by *providerhttp.Client (M2)
	Gate    Gate                         // same gate without HTTP, for SMTP/subprocess adapters
	Secrets SecretReader                 // reads only the refs declared in this instance's Settings
	Paths   library.PathMapper           // 05-library; adapters pass source "<kind>:<instance>" (e.g. "download:qbt-main")
	Now     func() time.Time             // clock; fake in coretest
}

// SecretReader resolves a declared secret at use time (not at build time), so
// rotation takes effect on the next request without a rebuild.
type SecretReader interface {
	Secret(ctx context.Context, field string) (secrets.Value, error)
}

// HTTP is the outbound client an adapter gets. Declared here (not in
// providerhttp) so that integrations does not import providerhttp, which
// imports integrations for Ref and Class.
type HTTP interface {
	Do(req *http.Request) (*http.Response, error)
	Client() *http.Client // for SDKs that take an *http.Client (e.g. openai-go option.WithHTTPClient)
	Gate() Gate
}

// Gate is the transport-independent admission control (M2.4 order).
type Gate interface {
	Acquire(ctx context.Context, counter string) (Ticket, error) // *Error{Class: ClassRefusedLocal} when refused
	State() GateState
}

// Ticket records one attempt's outcome; started time is captured at Acquire.
type Ticket interface{ Done(err error) }

// ---- policy data consumed by providerhttp ---------------------------------

// Limits is one instance's outbound budget. Zero fields inherit (instance →
// adapter Meta.Limits → core fallback); nonsense is clamped, never allowed to
// disable pacing (normalize, providerhttp.go:180-194, kept).
type Limits struct {
	RPS           float64
	Burst         int
	MaxRetries    int
	Timeout       time.Duration // per attempt, starting after the bucket wait (M2.6)
	RetryAfterMax time.Duration // default 5m
	Daily         *DailyLimits  // nil: no daily quota
	Breaker       BreakerPolicy
	Holds         HoldPolicy
}

type DailyLimits struct {
	Total, Background int            // Background ≤ Total; Interactive may use the rest
	Zone              *time.Location // quota-day boundary; default UTC; Google Books preset America/Los_Angeles
	Counters          []string       // named counters, e.g. indexer "query" and "grab"; default ["requests"]
}

type BreakerPolicy struct {
	Threshold    int           // consecutive CountsAgainstRemote failures; default 5
	Cooldown     time.Duration // default 30s; SHORT on purpose (circuitbreaker.go:151-166 rationale kept)
	ProbeTimeout time.Duration // a half-open probe lease expires after this; default = Limits.Timeout
}

type HoldPolicy struct {
	Durations map[Class]time.Duration // defaults: Quota 4h, RateLimited 15m, Auth 6h, 5xx 30m, transport 5m (throttle.go:59-65)
	Max       time.Duration           // default 24h (maxThrottleHold, throttle.go:102)
	Persist   bool                    // default true
}

type EgressPolicy uint8

const (
	EgressAny              EgressPolicy = iota // operator-configured targets incl. LAN: download clients, local AI, self-hosted ntfy
	EgressPublicOnly                           // data-derived URLs: cover art, links found in responses (safehttp rules)
	EgressSameHostOrPublic                     // indexer download links: the instance's own host, else public only
)

// Hold is a persisted stand-off for one Scope.
type Hold struct {
	Scope  string
	Class  Class
	Detail string
	Until  time.Time
	SetAt  time.Time
}

type BreakerState uint8 // BreakerClosed, BreakerHalfOpen, BreakerOpen

type GateState struct {
	Hold         *Hold
	Breaker      BreakerState
	QuotaUsed    map[string]int // by counter
	QuotaLimit   map[string]int
	BucketTokens float64
}

// Instance is the common surface of every built instance.
type Instance interface {
	// Health actively probes the remote. It must be cheap (one request) and
	// must not mutate remote state.
	Health(ctx context.Context) Health
	// Close releases connections. Called after the instance is swapped out
	// and its in-flight calls have drained, or at shutdown.
	Close() error
}

// HealthStatus is ordered: a higher value is worse.
type HealthStatus uint8

const (
	HealthOK HealthStatus = iota
	HealthDegraded             // reachable, but held, slow, or missing an optional capability
	HealthDown                 // unreachable or 5xx
	HealthAuthFailed           // credentials rejected; needs a human
	HealthMisconfigured        // settings invalid, secret missing
	HealthDisabled             // Enabled=false
)

type Health struct {
	Status        HealthStatus
	Message       string        // human text, already redacted
	RemoteVersion string        // e.g. qBittorrent "v5.1.2", Newznab server version
	Latency       time.Duration
	CheckedAt     time.Time
	Hold          *Hold         // active providerhttp hold, if any (M2)
	Breaker       BreakerState
}

// Registry is per-App. It owns descriptors, instance rows, built instances
// and their swap/drain lifecycle.
type Registry struct{ /* unexported */ }

func (r *Registry) Adapters(kind Kind) iter.Seq[Meta]
func (r *Registry) Instances(kind Kind) iter.Seq[InstanceConfig]
func (r *Registry) Config(ref Ref) (InstanceConfig, bool)
func (r *Registry) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error)
func (r *Registry) Delete(ctx context.Context, ref Ref) error
// Test builds a throwaway instance from an UNSAVED config, runs Health and
// the kind's extra test (caps fetch, login), and discards it. Nothing is persisted.
func (r *Registry) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error)
func (r *Registry) Health(ctx context.Context, ref Ref) (Health, error)

// Lease returns the current built instance as T plus a release func; the
// instance cannot be Closed while a lease is held. Kind packages wrap this.
func Lease[T Instance](ctx context.Context, r *Registry, ref Ref) (T, func(), error)

// ---- error taxonomy -------------------------------------------------------

// Class answers ONE question per value; callers decide retry/failover/hold
// from the class, never from strings.
type Class uint8

const (
	ClassUnknown      Class = iota // unrecognised: treated as Permanent for retry, Transient for health
	ClassCanceled                  // our ctx was canceled or its deadline passed; says nothing about the remote
	ClassRefusedLocal              // we refused before sending (hold, open breaker, spent daily budget, egress guard)
	ClassTransient                 // transport failure, 5xx, 408, per-attempt timeout on a live parent ctx
	ClassRateLimited               // burst overrun (429 or rate-limit vocabulary in a 403); RetryAfter may be set
	ClassQuota                     // per-day/credit allowance spent; retry after the quota window
	ClassAuth                      // credentials rejected/expired; permanent until a human acts
	ClassNotFound                  // a definitive "no such item" answer; NOT a remote failure
	ClassInvalid                   // the request is wrong (400/404-without-JSON excluded, 422, protocol error code)
	ClassUnsupported               // the instance lacks the capability (caps say no)
	ClassBadOutput                 // the remote answered 2xx but the answer is unusable (bad JSON, refusal, short array)
)

func (c Class) Retryable() bool // Transient, RateLimited (same target, after backoff)
func (c Class) CountsAgainstRemote() bool // Transient, RateLimited, Quota, Auth (breaker/hold/health input)

// Error is the one error type adapters return for remote failures.
type Error struct {
	Ref        Ref
	Op         string        // "add", "search", "generate", "send", ...
	Class      Class
	Status     int           // HTTP status or protocol code; 0 if none
	RetryAfter time.Duration // from Retry-After or the protocol; 0 if none
	Detail     string        // bounded (512 B), redacted body excerpt
	Err        error
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

// ClassOf classifies any error. parent is the CALLER's ctx: a done parent
// always yields ClassCanceled (same rule as aidispatch.Classify, classify.go:97).
func ClassOf(parent context.Context, err error) Class

var (
	ErrNotConfigured = errors.New("integrations: instance not configured")
	ErrDisabled      = errors.New("integrations: instance disabled")
	ErrUnknownAdapter = errors.New("integrations: unknown adapter")
)
```

Registration (the integration-facing slice of `module.Registrar`, which [06-app-sdk](06-app-sdk.md) owns):

```go
// in package module (06-app-sdk §6.4.3 owns the type; problems accumulate in r.Err()).
// Adapter IDs must carry the module's prefix; "core.*" only via app.Options.Adapters.
func (r *Registrar) Adapter(d ...integrations.Descriptor)

// in package app: core adapters are opt-in per binary (03 §3.7 "the adapters it enables").
app.New(ctx, app.Options{
	Adapters: []integrations.Descriptor{deluge.Descriptor(), notifymail.Descriptor(), webhook.Descriptor()},
}, books.Module())
```

HTTP surface (mounted by core `httpapi`; schemas in [05-http-api](05-http-api.md)):

| Route | Permission |
|---|---|
| `GET /api/core/v1/integrations/adapters?kind=` (Meta + settings schema) | `core:integrations.read` |
| `GET, POST /api/core/v1/integrations/{kind}/instances` | read / `core:integrations.manage` |
| `GET, PUT, DELETE /api/core/v1/integrations/{kind}/instances/{id}` | read / manage |
| `POST /api/core/v1/integrations/{kind}/test` (unsaved body) and `.../instances/{id}/test` | manage |
| `GET .../instances/{id}/health`; `DELETE .../instances/{id}/hold` (release a hold, as `ThrottleRegistry.ReleaseHold`, `internal/metadata/throttle_registry.go:338`) | read / manage |

### M1.4 Lifecycle

1. **Register** (kernel, before listeners): descriptors from `app.Options.Adapters` and each module's
   `Registrar.Adapter`. Duplicate `AdapterID`, a module registering outside its prefix, or a `Settings`
   schema with a secret field lacking `Secret: true` fails boot.
2. **Load**: rows from `c/integrations/instances/<kind>/<id>` plus file-defined instances
   (`core.integrations.<kind>.instances.<id>`); a file instance shadows a DB row of the same Ref and is
   read-only (`Origin=file`, lock reason surfaced like `config.SettingLockedBy`, `internal/config/config.go:2391`).
   A row whose adapter is not registered (module disabled, binary without the adapter) is **kept** and
   reported `HealthMisconfigured` with reason `adapter not in this binary` (restart-bound enablement, 03 §3.7).
3. **Build lazily** on first `Lease`; construction must not do network I/O (so boot never waits on a
   remote). Validation errors make the instance `HealthMisconfigured`, never fail boot.
4. **Hot swap** on `Put`: validate → persist row and secrets in one storage batch → build new →
   atomically replace → old instance drains its leases → `Close`. providerhttp state (bucket, holds,
   breaker, quota) is keyed by `Ref` and **survives** the swap.
5. **Health task** `core.integrations.health` (05-tasks) probes every enabled instance on an interval
   with bounded parallelism and publishes `core.integration.health.changed.v1` on status change
   (consumed by [05-health](05-health.md) and [05-notify](05-notify.md)).
6. **Shutdown**: stop issuing leases, wait for drain up to `core.integrations.drain_timeout`, `Close` all.

### M1.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Unknown adapter for a stored row | closed for that instance (unusable), open for boot | Data is retained for a binary that has the adapter; boot must not depend on optional integrations. |
| Settings fail schema validation on `Put` | closed (422, nothing written) | Never persist an instance that cannot build. |
| Secret field referenced but missing at request time | closed (`ClassAuth`, `HealthMisconfigured`) | Sending an unauthenticated request is wrong (same rule as `apiKeyFor`, `internal/ai/pool_routing.go:93-103`). |
| Build fails (bad URL) | closed for that instance | Reported via health; other instances unaffected. |
| Health probe fails | open (instance stays usable; status Down) | A probe is advisory; calls carry their own classification. |
| `Put` version conflict | closed (409) | Two admins editing one instance must not silently overwrite. |
| Secrets backend unavailable on `Put` | closed (503, row not written) | Row and secret are one batch; never a row pointing at a secret that was not stored. |
| `Test` with remote failure | n/a (returns `TestResult{OK:false, Class, Message}` with 200) | A failed test is a successful API call. |

### M1.6 Concurrency contract

`Registry` is goroutine-safe; the instance map is an `atomic.Pointer` to an immutable snapshot, swapped
under a writer mutex. `Lease` is lock-free on the read path and uses a per-instance reference count;
`Close` runs on a dedicated goroutine (`wg.Go`) after the count reaches zero or the drain timeout fires.
Adapters must be goroutine-safe (asserted by the conformance suites with `-race`). The health task
probes at most `core.integrations.health_parallelism` instances at once; each probe has its own timeout.

### M1.7 Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.integrations.<kind>.instances.<id>.*` | instance row | none | yes (file reload via 05-config notification) |
| `core.integrations.health_interval` | duration | `5m` | yes |
| `core.integrations.health_parallelism` | int ≥1 | `4` | yes |
| `core.integrations.probe_timeout` | duration | `10s` | yes |
| `core.integrations.drain_timeout` | duration | `30s` | yes |
| `core.integrations.max_instances_per_kind` | int | `32` | yes (bounds metric label cardinality) |

### M1.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_integrations_instances` | gauge | `kind`, `adapter`, `status` |
| `mediacore_integrations_health_status` | gauge (HealthStatus value) | `kind`, `instance` |
| `mediacore_integrations_health_probe_seconds` | histogram | `kind`, `adapter` (no `instance`: bucket × instance multiplication, ABO rule C5, `internal/ai/ai_metrics.go:36-39`) |
| `mediacore_integrations_swaps_total` | counter | `kind`, `instance` |

### M1.9 Tests

`TestRegistry_RejectsForeignAdapterPrefix`, `TestRegistry_UnknownAdapterRowRetained`,
`TestRegistry_HotSwapDrainsLeases` (synctest), `TestRegistry_FileInstanceReadOnly`,
`TestRegistry_PutWritesRowAndSecretsAtomically`, `TestRegistry_TestDoesNotPersist`,
`TestRegistry_BuildDoesNoIO` (transport that fails the test on any request),
`TestClassOf_Table` (one row per class, incl. the done-parent rule), `TestError_RedactsDetail`,
archtest `IntegrationsDescriptorCallers` (only kind packages call `NewDescriptor`).

### M1.10 Migration from ABO

| ABO today | Becomes |
|---|---|
| `config.MetadataSources[]` (`internal/config/config.go:147-163`) | `metadata/<id>` instance rows, adapters `books.<id>`; `Credentials` values moved into `secrets` |
| `DelugeWebURL/Password`, `DownloadClient.Torrent.Deluge` (`config.go:225-240,1776-1780`) | one `download/deluge` instance (05-download §Migration) |
| `ai_endpoints` rows (`internal/config/ai_endpoints.go:36-68`) | `ai/<id>` instances (05-ai) |
| `config.Plugins["webhook"].Settings` | `notify/<id>` webhook instances (05-notify) |
| `handleDelugeTestConnection`, `/ai/test-connection` | `POST /api/core/v1/integrations/{kind}/test` |
| `internal/plugin` (`Plugin`, `Registry`, `DownloadClient`, `Notifier`, `MediaPlayer`) | deleted (N4); `MediaPlayer` has no successor until a media-server kind is specified |

### M1.11 Open items

1. `config.Schema`, `secrets.Ref`/`secrets.Value` and `UserID` are owned by 05-config / 05-authn; this file
   uses those names and changes import paths, not shapes, if they land elsewhere.
2. **Shape of `module.Adapter` (06-app-sdk §6.4.3).** The 06 draft has kind packages return
   `module.Adapter{Capability, Kind, Factory any}` (e.g. `metadata.AdapterOf[Q, R]`). That makes
   `integrations/*` import `module`, which is above them in R4, and moves the type check to a runtime
   assertion. This file's shape avoids both: kind packages return `integrations.Descriptor` (type safety via
   the unexported build func) and `Registrar.Adapter` accepts it (or `module.Adapter` wraps it). To be
   reconciled with 06.
3. `events.EntityRef{Module, Kind, ID}` (02 decision 6) is used by 05-download and 05-notify; its home
   package is not fixed yet (05-rbac has `audit.Target` with the same shape). Same shape wherever it lands.
4. A sixth kind (media-server sync: Plex/Jellyfin/ABS outbound) is not specified; `plugin.MediaPlayer`
   is deleted rather than carried forward because it has zero implementors (`internal/plugin/plugin.go:76-81`).

---

## M2. Outbound HTTP (`integrations/providerhttp`)

### M2.1 Purpose & scope

Every adapter's outbound HTTP passes through one per-`App` `providerhttp.Registry`, which composes —
in the transport, so no call site can bypass it (the design rationale of
`internal/metadata/providerhttp/providerhttp.go:17-20`, kept) — a reason-aware persisted **hold**, a
**circuit breaker**, a **token bucket**, a **daily quota**, an **egress guard**, bounded **retry**, a
user agent, and **secret redaction**. Non-HTTP adapters (SMTP, local subprocess) use the same `Gate`
without the transport.

### M2.2 Current state at HEAD

| Piece | Where | Global state |
|---|---|---|
| Token bucket + retry (429/5xx, Retry-After ≤5m, exp. backoff + jitter) in a `RoundTripper` | `internal/metadata/providerhttp/providerhttp.go:283-347,369-405` | `limiters`, `clients`, `overrides` maps (`:81-86`), keyed by provider string |
| Built-in limits per provider, fallback 2 RPS | `providerhttp.go:60-79` | package var |
| Limit change = `SetLimits` + `ResetProvider` (drops limiter) | `providerhttp.go:92-114`; caller `internal/metafetch/service_search.go:162-180` | — |
| Persisted, reason-aware hold (daily-quota 4h, rate-limit 15m, auth 6h, 5xx 30m, transport 5m; Retry-After clamped to [15m, 24h]) | `internal/metadata/throttle.go:59-65,102,139-213` | `defaultThrottles` (`throttle_registry.go:69`); store attached in `cmd/root.go:416-418` |
| Daily quota counter, two tiers on one counter (background stops at 800 of 1000 for Google Books), tier on ctx, persisted per reservation, quota day in Pacific time | `internal/metadata/dailyquota/dailyquota.go:41-71,159-188`; `internal/metadata/google_quota.go:30-34` | `registry` map (`dailyquota.go:308-312`) |
| Circuit breaker (5 failures, 30s cooldown), wrapped around `MetadataSource` only | `internal/metadata/circuitbreaker.go:32-104,165-174` | per `ProtectedSource` instance — and a new one per chain build (`service_search.go:342`), plus separate ones at `internal/metafetch/chain_throttle.go:32`, `internal/server/metadata_ops.go:304` |
| SSRF guard transport (refuses private/reserved IPs), used for cover downloads | `internal/security/safehttp/safehttp.go:107-261`; `internal/metadata/cover.go:55` | — |

Defects at HEAD in this exact area, which the design below removes (in scope: they are the behaviour being
specified):

1. **Bucket split on limit change.** `ResetProvider` deletes the limiter (`providerhttp.go:109-114`) while
   clients from `ClientWithTransport` (uncached, `:239-262`) and any in-flight chain keep the old one, so
   after a settings change two buckets serve one provider until those clients die — up to 2× the budget.
2. **Breaker stuck half-open forever.** `AllowRequest` moves Open→HalfOpen and then refuses every call
   while HalfOpen (`circuitbreaker.go:58-75`); `recordOutcome` returns **without recording** on a daily-
   budget refusal (`circuitbreaker.go:211-219`). A probe that hits the spent budget therefore leaves the
   breaker HalfOpen with no path back to Closed for the life of the process.
3. **Breaker counts our own cancellation.** `recordOutcome` calls `RecordFailure` for any non-nil error
   (`circuitbreaker.go:220-224`), including `context.Canceled`, which the hold classifier correctly ignores
   (`throttle.go:155-157`). Cancelling a bulk run can open every provider's breaker.
4. **Breaker state is per chain build**, so a config edit resets it and three call sites hold three
   unrelated breakers for Audible (anchors above).

### M2.3 Go API

```go
package providerhttp // github.com/falkcorp/mediacore/integrations/providerhttp

// Limits, DailyLimits, BreakerPolicy, HoldPolicy, EgressPolicy, Hold and
// GateState are declared in package integrations (M1.3) and used here.

// Registry is per-App. Never a package global (R5).
type Registry struct{ /* unexported */ }

type Options struct {
	Store     storage.Keyspace       // c/providerhttp/ ; nil = in-memory (tests)
	Base      http.RoundTripper      // default: a cloned http.DefaultTransport, never the global itself
	UserAgent string                 // "<app>/<version> (+mediacore/<v>)"
	Meter     metrics.Registry       // 05-metrics
	Log       *slog.Logger
	Now       func() time.Time
}

func New(opts Options) (*Registry, error)

// Client returns the instance-scoped client. Every Client for the same Scope
// shares ONE bucket, ONE breaker, ONE hold and ONE quota counter set.
func (r *Registry) Client(ref integrations.Ref, p Policy) *Client

type Policy struct {
	Limits integrations.Limits
	Egress integrations.EgressPolicy
	// Scope groups instances that share an upstream budget. Default ref.String().
	// Two instances using the same API key against one provider set the same Scope
	// (e.g. "google-books:" + hex(sha256(key))[:12]).
	Scope   string
	Redact  []string // extra query/header names to redact; apikey, api_key, token, password, Authorization, X-Api-Key, Cookie are always redacted
}

// Client implements integrations.HTTP: an *http.Client whose transport runs
// the gate, plus helpers.
type Client struct{ /* unexported */ }

var _ integrations.HTTP = (*Client)(nil)

func (c *Client) Client() *http.Client
func (c *Client) Do(req *http.Request) (*http.Response, error)
func (c *Client) GetJSON(ctx context.Context, url string, out any) error // classifies non-2xx into *integrations.Error
func (c *Client) Gate() integrations.Gate
// SetLimits mutates the existing bucket in place (rate.Limiter.SetLimit/SetBurst):
// no rebuild, no second bucket (fixes defect 1).
func (c *Client) SetLimits(l integrations.Limits)

// GateFor returns the gate alone, for SMTP and subprocess adapters.
func (r *Registry) GateFor(ref integrations.Ref, p Policy) integrations.Gate

// Acquire (integrations.Gate) runs hold → breaker → bucket → quota, in that
// order; nothing is sent if it errors. Ticket.Done records the outcome; the
// start time is the Acquire time, so a success releases only a hold that
// already existed when the attempt started (RecordSuccess rule,
// throttle_registry.go:303-318, kept).

func (r *Registry) ReleaseHold(ctx context.Context, scope string) error
func (r *Registry) Holds() iter.Seq[integrations.Hold]

// Priority tier travels on ctx (dailyquota.WithInteractive, kept). Unmarked = Background.
func WithInteractive(ctx context.Context) context.Context
// WithBypassHold lets an operator-initiated single lookup through an active hold
// (metadata.WithThrottleBypass, throttle_registry.go:390, kept). It never bypasses
// the breaker-open state's probe lease nor the daily quota.
func WithBypassHold(ctx context.Context) context.Context

// Classify maps a response or transport error to the taxonomy, reading the
// body before the status (Google's 403/429 ambiguity, throttle.go:168-195).
func Classify(ctx context.Context, resp *http.Response, body []byte, err error) integrations.Class
```

### M2.4 Lifecycle

`app.New` constructs one `Registry` with the `c/providerhttp/` keyspace, loads persisted holds (dropping
expired ones; clamping any `Until` beyond `HoldPolicy.Max` — the `maxThrottleHold` rule,
`internal/metadata/throttle.go:102`) and today's quota counters **before** any instance is built, which
replaces the ordering-by-comment in `cmd/root.go:405-418`. Gate state per `Scope` is created on first
`Client` call and lives for the App. Shutdown flushes nothing extra: holds and quota are written on change.

Per attempt (transport order, outer → inner):

```
ctx done? → ClassCanceled (nothing recorded)
hold active and !WithBypassHold → ClassRefusedLocal
breaker Open → ClassRefusedLocal; Open & cooldown elapsed → take the single probe lease (others refused)
bucket.Wait(ctx)
quota.Reserve(tier, counter) → ClassRefusedLocal when spent (counted per request SENT; never refunded)
egress dial guard (EgressPolicy)
base.RoundTrip
classify → breaker/hold/metrics update → retry if Class.Retryable() && attempt < MaxRetries && replayable
```

### M2.5 Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| Hold active | closed (`ClassRefusedLocal`, nothing sent) | A held provider is removed from the chain, not called and failed (`throttle.go:32-35`). |
| Breaker open | closed | Ride out a blip without traffic. |
| Half-open probe lease expires without an outcome | **reopens** (counts as one failure) | Fixes defect 2: a probe can never strand the breaker. |
| Outcome is `ClassCanceled`, `ClassRefusedLocal`, `ClassNotFound`, `ClassInvalid`, `ClassBadOutput` | not counted by breaker or hold | Fixes defect 3; these say nothing about the remote's health. |
| Daily quota spent | closed for Background at `Background`, for Interactive at `Total` | Reserve quota for people (owner decision recorded at `dailyquota.go:6-11`). |
| Quota store read fails | **closed** (`ErrBudgetUnavailable`) | Over-counting is the safe direction (`dailyquota.go:100-106` rationale); a quota we cannot read could be spent. |
| Hold store write fails | open (hold stays in memory; one warning per Scope) | Losing persistence must not disable the hold for this process. |
| Egress guard refuses the address | closed (`ClassRefusedLocal`) | Security control; data-derived URLs must not reach the LAN. |
| Retry-After beyond `RetryAfterMax` | clamp | Never park a worker indefinitely (`providerhttp.go:389-401`). |
| Non-replayable body on a retryable status | return response as-is | Never resend an empty body (`providerhttp.go:311-326`). |
| Error strings containing the request URL | redacted before return and log | Newznab/Torznab put `apikey` in the query (raw/16 §4.2); `*url.Error` embeds the URL. |

### M2.6 Concurrency contract

All types goroutine-safe. One `rate.Limiter`, one breaker and one quota counter **per Scope**, shared by
every Client and every worker (`providerhttp.go:198-200` rationale). `Timeout` is per attempt and starts
after the bucket wait, so waiting in line is never reported as provider slowness (the
`WithAttemptTimeout` rule of `internal/aidispatch/dispatch.go:143-148`). Waits honour ctx. Retry sleeps
use the registry clock (`synctest`-testable). Quota reservations serialize per counter under a mutex
and persist synchronously (one storage write per outbound request — acceptable at provider rates, and
it is what makes restarts safe, `dailyquota.go:151-153`).

### M2.7 Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.providerhttp.fallback.rps` | float | `2.0` | yes |
| `core.providerhttp.fallback.burst` | int | `2` | yes |
| `core.providerhttp.fallback.max_retries` | int | `3` | yes |
| `core.providerhttp.fallback.timeout` | duration | `30s` | yes |
| `core.providerhttp.retry_after_max` | duration | `5m` | yes |
| `core.providerhttp.hold.max` | duration | `24h` | yes |
| `core.providerhttp.user_agent` | string | `<app>/<version> (+mediacore/<v>)` | no |
| per instance: `limits.rps`, `limits.burst`, `limits.max_retries`, `limits.timeout`, `limits.tier` (`low`=0.5×, `medium`=1×, `high`=2× the adapter default, `internal/config/config.go:165-190`), `limits.daily.total`, `limits.daily.background`, `limits.daily.zone` | per field | adapter default | yes (in place, M2.3 `SetLimits`) |

### M2.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_providerhttp_requests_total` | counter | `kind`, `instance`, `class` |
| `mediacore_providerhttp_request_seconds` | histogram | `kind`, `adapter` |
| `mediacore_providerhttp_bucket_wait_seconds` | histogram | `kind`, `adapter` |
| `mediacore_providerhttp_refused_total` | counter | `kind`, `instance`, `reason` (`hold`, `breaker`, `quota`, `egress`) |
| `mediacore_providerhttp_retries_total` | counter | `kind`, `instance` |
| `mediacore_providerhttp_hold_active` | gauge | `kind`, `instance`, `class` |
| `mediacore_providerhttp_breaker_state` | gauge (0 closed, 1 half-open, 2 open) | `kind`, `instance` |
| `mediacore_providerhttp_quota_used` / `_quota_limit` | gauge | `kind`, `instance`, `counter`, `tier` |

### M2.9 Tests

`TestBucketSharedAcrossClientsOfScope`, `TestSetLimitsMutatesInPlace` (two clients, one bucket, after
change), `TestHoldPersistsAcrossRegistryRestart`, `TestHoldClampedToMaxOnLoad`,
`TestHold_EarlySuccessKeepsHold` (synctest), `TestBreaker_LeaseExpiryReopens`
(synctest; regression for defect 2), `TestBreaker_IgnoresCanceled` (defect 3),
`TestQuotaTiers` (background stops at Background, interactive continues to Total),
`TestQuotaDayRollsInZone` (DST boundary, embedded tzdata), `TestQuotaStoreReadFailureFailsClosed`,
`TestClassify_Google403RateLimitIsRateLimited`, `TestClassify_DailyQuotaBodyBeatsStatus`,
`TestRetryHonoursRetryAfterAndClamp`, `TestNoRetryWithoutGetBody`, `TestEgressPublicOnlyRefusesPrivate`,
`TestEgressSameHostOrPublic`, `TestRedactsApikeyInURLError`, `TestNoGlobals` (R5 archtest covers the package).

### M2.10 Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/metadata/providerhttp` | `core/integrations/providerhttp` (Client/limits/retry code reused; globals removed) |
| `internal/metadata/dailyquota` | `providerhttp` daily quota (same counter semantics; zone configurable) |
| `internal/metadata/throttle.go`, `throttle_registry.go` (`ThrottleRegistry`, `ThrottleStore`) | `providerhttp` holds; `ClassifyProviderError` → `providerhttp.Classify` |
| `internal/metadata/circuitbreaker.go` `CircuitBreaker` | `providerhttp` breaker (with probe lease); `ProtectedSource`/`NewChainSource` deleted — the gate is in the transport |
| `internal/security/safehttp` | dial guard behind `EgressPublicOnly`; package stays for non-integration callers (`internal/covers`) until 04 moves it |
| Pebble keys `provider_throttle:<id>` (`internal/database/provider_throttle.go:29`), `provider_daily_budget:<id>` (`dailyquota.go:85`) | one-time import into `c/providerhttp/holds/<scope>` and `c/providerhttp/quota/<scope>/<counter>/<day>` at the books module's first core boot; old keys deleted in the same batch; scope for each = `metadata/<id>` |
| `cmd/root.go:405-418` attach ordering | constructor ordering inside `app.New` |
| `/api/v1/metadata/providers/throttles` (`internal/server/handlers/metadata/provider_throttles.go:43`) | `GET .../instances/{id}/health` (`Hold`) and `DELETE .../hold`; legacy path aliased (09) |

### M2.11 Open items

1. In a multi-app binary (lowest priority), two apps calling the same upstream with the same key should
   share one budget. The `Registry` is an explicit object, so the multi-app kernel can pass one Registry
   to both apps; nothing in this design prevents it, and nothing is built for it now.
2. Whether `Limits.Timeout` should also offer a whole-call deadline (attempts + waits) is left to the first
   caller that needs it; today every caller passes a ctx deadline instead.

---

## M3. Metadata providers (`integrations/metadata`)

### M3.1 Purpose & scope

A generic provider contract whose **query and result types are defined by the app** (`BookMetadata`
stays in `books`; TV uses a series/episode result; Movies a movie result), plus the shared machinery
every app needs around it: an ordered, hold-aware **chain** with bounded fan-out, a **read-through
answer cache** with positive/negative TTLs and single-flight, and result provenance. Ranking, merging and
field-level application stay app-side.

### M3.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| `MetadataSource` = `Name`, `SearchByTitle`, `SearchByTitleAndAuthor`, all returning `[]BookMetadata`; optional `ContextualSearch.SearchByContext(*SearchContext)`; optional `ProviderIdentified.ProviderID()` | `internal/metadata/source.go:52-56,68-76,92-94`; `internal/metadata/provider_keys.go:37-39` |
| `BookMetadata` is audiobook-shaped (narrator, ASIN, duration, abridged) | `internal/metadata/openlibrary.go:148`; raw/11 §3 |
| 6 implementations: Audible, Audnexus, Google Books, Hardcover, Open Library, Wikipedia; constructed by a switch | `internal/metafetch/service_search.go:301-339` |
| `ProviderIdentified` kept optional "so mocks need not implement"; the result is that a decorator that forgets to forward it silently disables budgets | `internal/metadata/provider_keys.go:29-36`; `internal/metadata/circuitbreaker.go:231-234` |
| Shared provider-answer cache, generic `cachedCall[T]`: replay positive answers for the fetch-cache TTL, empty answers for a per-provider negative TTL, products 30 days; errors/throttles/429s never written; bypass and read-only modes | `internal/metafetch/pcache.go:37-90,111-175` |
| No single-flight: concurrent identical questions each go live (`cachedCall` reads then calls, `pcache.go:162-175`) | same |
| Held providers are removed from the chain before the call | `internal/metadata/throttle_registry.go:408` (`UnthrottledSources`) |

### M3.3 Go API

```go
package metadata // github.com/falkcorp/mediacore/integrations/metadata

// Provider is one metadata instance. Q and R are app types; core never
// inspects them except through the functions the app passes to Chain/Cache.
// ID is mandatory (no optional identity interface): a provider without an
// identity cannot be budgeted, cached or held.
type Provider[Q, R any] interface {
	integrations.Instance
	Ref() integrations.Ref
	Search(ctx context.Context, q Q) ([]R, error)
}

// Lookup is optional: fetch by a provider-native key (ASIN, TMDB id, TVDB id).
type Lookup[R any] interface {
	Lookup(ctx context.Context, key string) (R, error) // ClassNotFound when absent
}

// Caps is optional: which query fields this provider can use. The chain
// skips a provider whose Accepts(q) is false instead of calling it.
type Caps[Q any] interface {
	Accepts(q Q) bool
}

// Adapter returns the descriptor for an app-defined provider adapter.
func Adapter[Q, R any](m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Provider[Q, R], error),
) integrations.Descriptor

// Result is one answer with provenance.
type Result[R any] struct {
	Provider  integrations.Ref
	Value     R
	AskedAt   time.Time // when the provider gave this answer (cache age preserved)
	FromCache bool
}

type ChainOptions[Q any] struct {
	Instances   func() []integrations.Ref // ordered; default: enabled metadata instances whose adapter is Provider[Q,R], by Priority
	Parallelism tasks.Parallelism         // required (R7 type); network-bound: e.g. tasks.Fixed(4)
	PerProvider time.Duration             // per-provider deadline inside the call; default 20s
	Mode        ChainMode                 // FirstHit (stop at first provider with results, in priority order) | All
	Cache       *Cache[Q]                 // optional
}

type ChainMode uint8

const (
	FirstHit ChainMode = iota
	All
)

type Chain[Q, R any] struct{ /* unexported */ }

func NewChain[Q, R any](reg *integrations.Registry, opts ChainOptions[Q]) (*Chain[Q, R], error)

// Search returns results plus a per-provider outcome for every instance
// considered (skipped-held, skipped-caps, answered, failed). err is non-nil
// only when NO provider answered (ErrNoProviderAnswered, joined with each
// provider's error via errors.Join), mirroring ErrNoSourceAnswered
// (internal/metafetch/cache.go:455).
func (c *Chain[Q, R]) Search(ctx context.Context, q Q) ([]Result[R], []Outcome, error)

// All iterates per provider as answers arrive (FirstHit ignored).
func (c *Chain[Q, R]) Stream(ctx context.Context, q Q) iter.Seq2[Result[R], error]

type Outcome struct {
	Provider integrations.Ref
	Status   OutcomeStatus // Answered, Empty, SkippedHeld, SkippedCaps, SkippedBreaker, Failed, Canceled
	Class    integrations.Class
	Err      error
	Elapsed  time.Duration
}

var ErrNoProviderAnswered = errors.New("metadata: no provider answered")

// Cache is a read-through answer cache stored in the CALLING MODULE's
// keyspace (the values are app types, so they are module data, R8).
type Cache[Q any] struct{ /* unexported */ }

type CacheOptions[Q any] struct {
	Keyspace    storage.Keyspace                     // module keyspace, e.g. m/books/pcache/
	QuestionKey func(Q) (key string, ok bool)        // normalized; ok=false: uncacheable question
	Version     string                               // bump to invalidate all questions (searchIdentityVersion analogue)
	TTL         func(provider integrations.Ref) TTL  // positive/negative per provider
	Now         func() time.Time
}

type TTL struct{ Positive, Negative time.Duration }

func NewCache[Q any](opts CacheOptions[Q]) (*Cache[Q], error)

// Use flags per call (cacheUse, pcache.go:146-153, kept).
func WithCacheBypass(ctx context.Context) context.Context   // skip read, still write
func WithCacheReadOnly(ctx context.Context) context.Context // read, never write (dry runs)

// Cached answers one provider question through the cache with single-flight
// per (provider, key). Only err==nil answers are written; empty answers are
// written with the negative TTL.
func Cached[Q, R any](ctx context.Context, c *Cache[Q], p Provider[Q, R], q Q) (rs []R, askedAt time.Time, err error)

func (c *Cache[Q]) Invalidate(ctx context.Context, provider integrations.Ref, key string) error
func (c *Cache[Q]) Sweep(ctx context.Context) (removed int, err error) // run by a module task, R7 parallelism declared
```

Provider adapters build their HTTP client from `env.HTTP` (M2); they never construct an `http.Client`.
archtest `MetadataAdaptersUseEnvHTTP` fails any adapter package that references `http.Client{` or
`http.DefaultClient`.

### M3.4 Lifecycle

Instances are created/updated through M1. A module builds its `Chain`(s) once in its constructor; the
chain reads the instance list on each `Search` (an atomic snapshot), so enabling, reordering or editing a
provider takes effect on the next search without rebuilding — and without resetting breaker/hold/bucket
state, which lives in providerhttp per `Ref`. Cache sweep is a module-owned scheduled task (e.g.
`books.metadata.cache-sweep`).

### M3.5 Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| Provider held / breaker open / quota spent | skip that provider (Outcome `Skipped*`), continue chain | Removing a held provider is the point of holds (`throttle.go:32-35`). |
| Provider `ClassNotFound` | Outcome `Empty`; negative-cached | An answer about the query, not a failure (`internal/metadata/provider_error.go:22-38`). |
| Provider `ClassBadOutput`/`ClassInvalid` | Outcome `Failed`; not cached; chain continues | Another provider may answer. |
| Every provider failed or was skipped | closed: `ErrNoProviderAnswered` (joined causes) | "No answer" must never look like "no results" — the bug class `cache.go:449-455` documents. |
| Cache read error / undecodable entry | open: treated as a miss | A cache must never block a live answer (`decodeEntry`, `pcache.go:133-144`). |
| Cache write error | open: logged once per key family | Same. |
| Caller ctx canceled | closed: return `ClassCanceled`; no cache write; no hold | Our budget, not theirs. |

### M3.6 Concurrency contract

`Chain` and `Cache` are goroutine-safe. Fan-out uses `errgroup` with `SetLimit(opts.Parallelism.N())`
(CLAUDE.md "Concurrency" rule; the `Parallelism` type has no usable zero value, 03 R7). Single-flight is
keyed by `(provider Ref, Version, QuestionKey)`; a canceled leader does not cancel followers (they retry
the call themselves). Results from `Stream` are delivered in completion order; `Search` returns them in
instance-priority order. Library-scale callers (bulk fetch) must run through `tasks.RunItems` with their
own declared parallelism; the provider bucket, not the worker count, bounds the request rate.

### M3.7 Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.integrations.metadata.instances.<id>.*` | instance row (M1) | none | yes |
| per instance `cache.positive_ttl` | duration | module default (books: `metadata_fetch_cache_ttl_days`) | yes |
| per instance `cache.negative_ttl` | duration | module default (books: `metadata_fetch.negative_ttl_days`, `internal/metafetch/pcache.go:52-53`) | yes |
| `core.metadata.per_provider_timeout` | duration | `20s` | yes |

### M3.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_metadata_searches_total` | counter | `module`, `outcome` (`answered`, `no_answer`) |
| `mediacore_metadata_provider_outcomes_total` | counter | `instance`, `status` |
| `mediacore_metadata_cache_requests_total` | counter | `module`, `result` (`hit`, `miss`, `bypass`, `negative_hit`) |
| `mediacore_metadata_singleflight_shared_total` | counter | `module` |
| `mediacore_metadata_provider_seconds` | histogram | `adapter` |

### M3.9 Tests

`TestChain_SkipsHeldProvider`, `TestChain_FirstHitStopsInPriorityOrder`,
`TestChain_NoAnswerIsError`, `TestChain_NotFoundIsEmpty`,
`TestChain_FanOutBounded` (counts concurrent calls), `TestChain_EditKeepsBreaker`,
`TestCache_NegativeTTL`, `TestCache_ErrorsNeverWritten`, `TestCache_ReadOnlyWritesNothing`,
`TestCache_SingleFlight` (N goroutines, 1 live call), `TestCache_VersionBumpInvalidates`,
`TestCache_UndecodableIsMiss`. Fakes: `coretest.FakeProvider[Q, R]` (scripted answers, latency, classes)
and `coretest.ProviderHTTP(t)` (Registry over `httptest.Server`, fake clock, in-memory store).

### M3.10 Migration from ABO

| ABO | Becomes |
|---|---|
| `metadata.MetadataSource` + `ContextualSearch` + `ProviderIdentified` | `metadata.Provider[books.Query, books.Metadata]`: the three search methods collapse into `Search(ctx, books.Query)`, where `books.Query` is today's `SearchContext` fields (`source.go:68-76`); `ID` is mandatory |
| 6 clients in `internal/metadata/{audible,audnexus,googlebooks,hardcover,openlibrary,wikipedia}.go` | stay in ABO as adapters `books.<id>` registered via `Registrar.Adapter`; HTTP from `env.HTTP`; credentials via `env.Secrets` |
| `switch src.ID` in `buildSourceChainFromConfig` | deleted; instances from M1 |
| `BuildSourceChain` memoization + `ProtectedSource` | `metadata.Chain` (no memo needed; no per-build breaker) |
| `pcache` (`internal/metafetch/pcache.go`, `internal/database/metadata_fetch_cache.go`) | `metadata.Cache` in `m/books/` keyspace; existing `pcache:` rows are claimed as a legacy prefix by `books` (02 #13) and read in place behind the cache's key function until expiry |
| `AuthorLister` (`internal/metadata/audible_author_list.go:32`) | an app-side optional interface on the books adapter (type-assert on the leased provider) |
| `MetadataCandidateScorer` (`internal/ai/metadata_scorer.go:29`) | stays in ABO; may call `ai.Router` (05-ai) |

### M3.11 Open items

1. TV/Movies metadata (TVDB/TMDB) adapters are app-side; whether any of them is "shared" (≥2 consumers,
   03 §3.4) is decided when the second app exists.
2. Cover/artwork download is a metadata-adjacent concern that today uses `providerhttp` with key `cover`
   (`internal/metadata/cover.go:55`); it becomes a module-owned client with `EgressPublicOnly` and Scope
   `metadata/cover` until a core "artwork" helper has two consumers.

---

## Objections to 03

1. **Add a root package `integrations`** (path `core/integrations`, layer = the `integrations/*` layer of
   R4). 03 §3.3 lists only the five kind packages and `providerhttp`. The instance descriptor, `Ref`,
   health type, registry and error taxonomy are shared by all six and cannot live in `providerhttp`
   (SMTP and subprocess adapters have no HTTP) or in any one kind without the others importing a sibling.
   R4's "own or lower layer" already allows `integrations/download` → `integrations`; R1/R5 are unaffected.
2. **`module.Registrar` needs `Adapter(integrations.Descriptor)`** and the kind-specific registrations in
   05-ai/05-notify (`AICapability`, `AIBatchHandler`, `NotificationTopic`). 03 §3.3 says the Registrar
   covers "adapters"; this is the concrete shape, for 06 to adopt.

## Out-of-scope defects found

None outside this file's area. (The four providerhttp/breaker defects in M2.2 are in scope and are
removed by the design.)
