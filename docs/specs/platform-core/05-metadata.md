<!-- file: docs/specs/platform-core/05-metadata.md -->
<!-- version: 1.7.1 -->
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
| M1 | Integration-instance base: adapter descriptors, the **one** integration-instance registry and its store interface (settings + `secrets.Ref`s), health, test, egress trust, error taxonomy | `integrations` (03 §3.3 root package, R4 L4) |
| M2 | Outbound HTTP for every adapter: throttle, daily quota, circuit breaker, persisted holds (behind a store interface), retry, egress policy, redaction — **per `App`, not package globals** | `integrations/providerhttp` |
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

**One owner, one stored format per instance (SIMP-4, R4-1).** `integrations` owns every integration-instance
record — kind, instance ID, adapter, settings, and the `secrets.Ref`s of its secret fields. `config` holds **no**
integration instances: there is no `core.integrations.<kind>.instances.<id>` config key, no
`core.download.clients`, `core.notify.mail.*` or `core.ai.*` instance row, and no module-scoped `"<ID>/"` secret
for an instance. Records persist through one SDK store interface, `integrations.InstanceStore` (M1.3), and every
instance has exactly one stored form:

- **Native** instances live in core's own implementation, `integrations.NativeInstanceStore`, over
  `c/integrations/instances/<kind>/<id>` (M1.4 "Native store"). It is the only store of an app with no pre-core
  instances (new apps, `hello`, `coretest`, TV/Movies, [10](10-tv-movies-proof.md)).
- **Legacy-backed** instances are the ones an app already stored before core. The app supplies a **legacy
  implementation** of the same interface through `Manifest.LegacyStores` ([06](06-app-sdk.md)); for ABO it is
  `books` code over ABO's existing keys and row formats (the M1.10 table). Core holds none of those key names or
  row formats (03 R1) and no storage grant over them: grants for these families belong to the deferred cutover
  (M1.10, "Deferred: cutover"). Nothing is copied into `c/integrations/instances/`, so this build and an older
  one read the same values.

**Store model: routed per record (SIMP5-1).** Two of the store interfaces in this file serve an app through **both**
implementations at once, and choose per record: `integrations.InstanceStore` (M1.3) and `providerhttp.GateStore`
(M2.3). The native store always serves; when the app supplies a legacy store, it additionally serves exactly the
records it covers, decided by the legacy store's `Covers(id)` (the optional `legacy.Covering` capability, 05-storage
§5S.3.11 "Legacy capabilities") over a record ID this file defines (`legacy.IntegrationsCoverID`,
`legacy.ProviderHTTPCoverID`, M1.3 "Legacy members"). Only the legacy half implements `Covers`; a legacy half
without it covers every record, the native half is never asked, and the `backends` ledger then records `legacy`
(SIMP6-5). `notify.RuleStore` ([05-notify](05-notify.md) §3) is the third routed store; library's pre-core roots
(`legacy.RootSource`, [05-library](05-library.md)) are legacy-only beside its config entries, not routed (03 §3.3,
SIMP6-3); every other store interface of 03 §3.3 (`auth.Store`, `rbac.Store`, `tasks.Stores`,
`config.OverrideStore`, `log.JournalStore`, `ai.JobStore`, the full-text dirty set) is **exclusive**: the legacy
implementation, when supplied, replaces the native one. For ABO the native `InstanceStore` and `GateStore`
therefore run in production during the extraction (a stored `notify/email`, a second download client, every
non-metadata gate scope), and the claim ledger's `backends` value for both is `legacy+native` (05-storage §5S.3.11).

**Legacy-only identifiers live in package `legacy` (R6-3).** The capabilities this file's routers consume
(`legacy.Covering`, `legacy.ExtConfirmer`), the record-ID helpers, `legacy.LocalCandidate` and the legacy
conformance suites (`legacy/legacytest`) are members of core's `legacy` package, which sits at L5, imports
`integrations` and `providerhttp`, and is excluded from the API-stability gate (03 §3.3 "Legacy-only identifiers"),
so deleting them at the integrations cutover is not a breaking change. `integrations` and `providerhttp` never
import `legacy`: the Registry and the gate router name each capability through an **unexported** interface of their
own with the identical method set, and an `export_test.go` assertion in each package checks, in both directions,
that its interface and the `legacy` one are mutually assignable (and that its unexported record-ID helper returns
what the exported one does), so a renamed or retyped member fails that package's tests instead of silently
disabling routing. What cannot move stays in v1, inert for native apps (03 §3.3): the legacy-state pointer fields
on the record types (`InstanceConfig.Legacy`, `StoredInstance.Legacy`, SIMP6-7), the `Registry.Confirm` service
method (it answers 409 for a native instance), and the legacy half's constructor input `providerhttp.Options.Legacy`.

Secret names follow 05-config §2.3's grammar:
`integrations.<kind>.<instance>.<field>` (e.g. `integrations.download.deluge.password`). Operator-layer
values (file, env, flag) of ABO's legacy integration-bound keys reach this package only as 05-config
`Passthrough` values at those same paths, and are applied as an **in-memory overlay** that is never
persisted (M1.4 step 2, SIMP2-5). Every kind file (05-ai, 05-download, 05-indexer, 05-notify) and the ABO
migration (09 S7, which builds this package and notify v1 together, SIMP2-4) point here.

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

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
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

// InstanceConfig is one instance as the Registry serves it: the record an
// InstanceStore loaded (native or legacy-backed, M1.4 step 2a) plus the
// effective-view fields (Locked, Stored).
// All instances are global (admin-managed); per-user instances are deferred with
// notify's personal channels (05-notify §11).
type InstanceConfig struct {
	Ref      Ref
	Adapter  AdapterID
	Name     string
	Enabled  bool
	Priority int                 // lower is preferred; meaning is per kind
	Origin   Origin              // set by the Registry from the writing principal, never by the caller
	// Local allows the hosts of the instance's configured URL-typed fields (and
	// no other host) to be loopback or private addresses (M2 egress guard,
	// M2.4; SEC4-15). Setting it, or keeping it across a URL change, needs the
	// ProtectAdmin check: core:admin AND an interactive principal (05-config
	// §1.3.7; SEC-15, SEC2-7). Any other writer's instance reaches public
	// addresses plus the hosts in core.integrations.egress.lan_allow only. For a
	// legacy-backed instance it is a native-only field, kept by the legacy store
	// in its extension record with the URL origin it was granted for, and it
	// reads false while that record is suspended or holds only a pending
	// candidate (M1.4 step 2a, M1.10; R4-1, DATA5-9).
	Local    bool
	Settings map[string]any      // validated against Meta.Settings; secret fields hold secrets.Ref only
	Limits   *Limits             // optional per-instance override of Meta.Limits (zero fields inherit)
	// Version is the optimistic-concurrency token for PUT, reported by the
	// instance's store. A legacy store derives it from the legacy fields plus
	// its extension record (64-bit truncated hash), so a write by any build
	// changes it.
	Version  uint64
	Updated  time.Time
	// Legacy is non-nil exactly for an instance served by the app's legacy
	// store until its family's cutover (M1.4 step 2a, M1.10); set by the
	// store, nil from NativeInstanceStore and always nil after the cutover.
	// The one legacy-state pointer of this type (03 §3.3, SIMP6-7).
	Legacy   *InstanceConfigLegacyState
	// Locked lists the fields a canonical operator-layer value currently
	// overlays and locks (field -> OperatorValue.By, which names the layer
	// and spelling). A value under a BelowStored legacy name never appears
	// here: it only fills a field with no stored value (M1.4 step 2, SEC6-5).
	// Set by the Registry on the effective view only; never stored (SIMP2-5).
	Locked   map[string]string
	// Stored is false for an effective instance that exists only because an
	// operator value names it (no row in c/integrations/instances/).
	Stored   bool
}

// InstanceConfigLegacyState is what the effective view of a legacy-backed
// instance adds (M1.4 step 2a).
type InstanceConfigLegacyState struct {
	// Shared lists the fields the legacy format stores once for the whole
	// family (ABO's legacy webhooks share their enabled flag and secret,
	// M1.10); a Put changing one changes it for every legacy-backed instance
	// of that family.
	Shared []string
}

// Origin records who wrote the stored row last.
//   "ui"        interactive session holding core:integrations.manage
//   "api"       API-key principal holding core:integrations.manage (cannot set Local)
//   "legacy"    a legacy-backed instance (M1.4 step 2a): older builds and the
//               legacy config alias write the same stored form, so its Origin
//               is always reported as "legacy"
//   "arrfacade" written through FacadeWriter (deferred-build member, see below)
// There is no "pinned" origin: operator values are an overlay, not a writer.
type Origin string

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

// EgressPolicy says which URLs an adapter dials. Whatever the policy, the
// providerhttp dial guard (M2.4) checks the RESOLVED address at dial time and
// refuses loopback, RFC 1918 / RFC 4193 private, CGNAT and unspecified
// addresses unless the instance is trusted for private networks, i.e.
// InstanceConfig.Local is true or the host is listed in
// core.integrations.egress.lan_allow. Link-local addresses (IPv4 169.254/16,
// fe80::/10, which include cloud metadata endpoints) are refused even then (SEC-15).
type EgressPolicy uint8

const (
	EgressConfigured       EgressPolicy = iota // the instance's own configured URLs: download clients, AI endpoints, webhooks, indexer API
	EgressPublicOnly                           // data-derived URLs (cover art, links found in responses): never private, even for a Local instance
	EgressSameHostOrPublic                     // indexer download links: the instance's configured host under that instance's own guard, else public only
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

// ---- persistence (R4-1) --------------------------------------------------

// InstanceStore persists instance records. It is SDK surface, frozen by the
// API-stability gate, and holds only the native contract (SIMP5-4). Core ships
// NativeInstanceStore (M1.4 "Native store"); an app whose instances predate
// core passes its own implementation in the Integrations field of
// legacy.Stores, set on Manifest.LegacyStores (06; R6-3). Store model: ROUTED
// (M1.1, SIMP5-1): the Registry sends each instance to the legacy store when
// that store covers its record ID (legacy.IntegrationsCoverID), else to the
// native store. Every write stages into the caller's batch: the Registry stages the
// instance's secrets (core secrets) and its audit record in that same batch.
type InstanceStore interface {
	Load(ctx context.Context) ([]StoredInstance, error)
	// Put writes inst in the store's own format and returns the new Version.
	Put(ctx context.Context, b *storage.Batch, inst StoredInstance) (uint64, error)
	Delete(ctx context.Context, b *storage.Batch, ref Ref) error
}

// Legacy-only capabilities of an InstanceStore are OPTIONAL interfaces in
// package legacy (R6-3, SIMP5-4; 05-storage §5S.3.11 "Legacy capabilities").
// The kernel detects them by type assertion when it builds the Registry at
// P4c; the Registry names them only through unexported interfaces with the
// identical method sets (M1.1; checked by export_test.go). The native store
// implements neither. A legacy store asserts them at compile time (books:
// var _ legacy.Covering, var _ legacy.ExtConfirmer), because a failed
// assertion is silent; legacytest.IntegrationsInstanceStoreSuite runs a case
// for each (M1.9).
//   - legacy.Covering: Covers(id) with id = legacy.IntegrationsCoverID(ref,
//     adapter) reports whether the store holds ref, or would hold a NEW
//     instance ref of that adapter (a legacy store covers its list families
//     and only the fixed IDs of its singleton families, M1.4 step 2a). A
//     legacy store without it covers every record and the backends ledger
//     records legacy (SIMP6-5); the native store is never asked.
//   - legacy.ExtConfirmer: Confirm(ctx, b, id) with id = ref.String() is the
//     admin re-confirmation of a suspended extension record, or of a pending
//     Local candidate (legacy.Extension.Confirm, 05-storage §5S.3.11;
//     Registry.Confirm). An instance whose store lacks it has nothing to
//     confirm.

// StoredInstance is one persisted record: InstanceConfig without the
// effective-view fields, plus the native-only trust fields.
type StoredInstance struct {
	Config        InstanceConfig // Locked and Stored are ignored
	LocalOrigin   string         // scheme://host:port Local was granted for; "" = none
	SecretsOrigin string         // URL origin the core secrets copies were written for
	// Legacy is the legacy store's view of the record (M1.4 step 2a); nil for
	// every native record and always nil after the integrations cutover. The
	// one legacy-state pointer of this type; it stays in v1 because
	// integrations cannot name a type of package legacy (03 §3.3, SIMP6-7).
	Legacy *StoredInstanceLegacyState
	// Malformed is non-empty when the stored value cannot be decoded; it names
	// the field. The instance is HealthMisconfigured and nothing is rewritten.
	Malformed string
}

// StoredInstanceLegacyState holds what only a legacy-backed instance has.
// It carries no secret state: the Registry reads and writes every secret
// field of every instance through secrets (Get, Stage), and secrets alone
// reaches the app's legacy copies, through the one legacy-secret mechanism,
// 05-config's legacy.SecretsStore (kept names written in place in the app's
// cipher, moved names sentinelled, re-entered values re-imported; 05-config
// §2.2-§2.4, M1.4 step 2a; lead decision, round 6). The instance store never
// reads or writes a secret field.
type StoredInstanceLegacyState struct {
	// Ext is the state of the extension record (05-storage §5S.3.11):
	// ExtCurrent, ExtStale (an older build changed the instance's legacy
	// fields, R4-1) or ExtOrphan. Suspended means the Widening field
	// Config.Local must not be applied until legacy.ExtConfirmer.Confirm (M1.4 step
	// 2a classifies every field).
	Ext       storage.ExtState
	Suspended bool
	// PendingLocal is the URL origin the S7 books migration offered as a Local
	// candidate (M1.10, DATA5-9): it grants nothing until an admin confirms it
	// with Registry.Confirm, and only while it equals the instance's current
	// URL origin. "" = none.
	PendingLocal string
}

// NativeInstanceStore is core's implementation over its own keyspace
// c/integrations/ (M1.4 "Native store"). It implements no legacy capability:
// the Registry never asks it Covers, and an instance it holds has nothing to
// confirm.
func NativeInstanceStore(ks storage.Keyspace) InstanceStore

// Registry is per-App. It owns descriptors, the stores, built instances and
// their swap/drain lifecycle.
type Registry struct{ /* unexported */ }

func (r *Registry) Adapters(kind Kind) iter.Seq[Meta]
func (r *Registry) Instances(kind Kind) iter.Seq[InstanceConfig] // effective views (stored row + overlay)
func (r *Registry) Config(ref Ref) (InstanceConfig, bool)        // effective view
// Put validates and persists one instance with its secrets (one batch, M1.4).
// The principal in ctx sets Origin. URL-typed fields are written by any holder
// of core:integrations.manage (interactive or API key; SEC2-7). Put refuses
// (403) a principal without the ProtectAdmin check (core:admin + interactive)
// that sets Local, or keeps Local=true across a URL change. A Put that changes
// a field in Locked is refused whole (409, ErrFieldLocked); one that omits the
// locked fields or repeats their effective value stores the other fields and
// keeps each locked field's stored value as it was (HEAD semantics). When
// the scheme, host or port of any URL-typed settings field changes, every
// secret field is cleared unless secretsIn re-supplies it, Local is reset to
// false unless the writer passes the ProtectAdmin check and sets it again, and
// the write is audited as core.integration.change_url with secrets_dropped=true
// (05-rbac §7.1; SEC-15). A secret counts as re-supplied only when the raw
// request carries a value that is neither empty nor an echoed or mask-shaped
// value; the check runs before any round-trip restoration of masked values
// (the legacy config alias's restoreRoundTripSecrets step,
// internal/config/update_service.go:333-356 @748fd787a), so a URL change made
// through the legacy UI cannot carry the stored secret to the new host (SEC3-4).
// A Put on a legacy-backed instance goes to the legacy store, which writes the
// legacy fields in their legacy format and its extension record into the batch
// that also holds the secrets (M1.4 step 2a). A Put that changes a secret
// field releases the instance's ClassAuth hold (M2.4): new credentials are the
// human action that hold waits for.
func (r *Registry) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error)
func (r *Registry) Delete(ctx context.Context, ref Ref) error
// Test builds a throwaway instance from an UNSAVED config, runs Health and
// the kind's extra test (caps fetch, login), and discards it. Nothing is
// persisted. The same egress guard as a saved instance applies; a config with
// Local=true needs the same ProtectAdmin check as Put (SEC2-7). TestResult
// carries only OK, the Class and the HTTP/protocol status: never a response
// body or remote error text (a test must not be a read oracle, SEC-15).
func (r *Registry) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error)
func (r *Registry) Health(ctx context.Context, ref Ref) (Health, error)
// Confirm re-confirms a legacy-backed instance whose extension record is
// suspended, or accepts its pending Local candidate (M1.4 step 2a, M1.10):
// ProtectAdmin check (core:admin + interactive), audited as
// core.integration.confirm with the instance's current URL origins;
// afterwards Local applies to the instance's current configured hosts. A
// pending candidate whose origin no longer equals the current URL origin is
// dropped instead of granted. An instance with nothing to confirm (native, or
// neither suspended nor pending) gets ErrNothingToConfirm (409). A routine
// Put never clears a suspension and never accepts a candidate.
func (r *Registry) Confirm(ctx context.Context, ref Ref) error

type TestResult struct {
	OK     bool
	Class  Class
	Status int
}

// FacadeWriter is the only write path of the *arr facade (08, SEC-5). It is a
// deferred-build member of this package (lands with arrfacade and
// integrations/indexer; not in the ABO extraction plan, SIMP2-6). It accepts a
// principal holding "<module>:arr.indexers"
// (not core:integrations.manage), writes and deletes only KindIndexer
// instances whose Origin is arrfacade, forces Origin=arrfacade and
// Local=false, and validates every URL-typed field before Put: scheme http or
// https, and a host that resolves to public addresses or is listed in
// core.integrations.egress.lan_allow. Anything else is refused (422).
type FacadeWriter struct{ /* unexported */ }

func (r *Registry) FacadeWriter(module string) *FacadeWriter
func (w *FacadeWriter) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error)
func (w *FacadeWriter) Delete(ctx context.Context, ref Ref) error
func (w *FacadeWriter) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error)

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
	ErrFieldLocked   = errors.New("integrations: field is set by an operator value")
	ErrNothingToConfirm = errors.New("integrations: the instance is neither suspended nor pending a Local confirmation")
)
```

#### Legacy members (package `legacy`, R6-3)

These members exist only for an app whose instances and gate rows predate core. They are specified here, owned by
this file, and live in core's `legacy` package (03 §3.3 "Legacy-only identifiers"; 05-storage §5S.3.11 lists them),
outside the API-stability gate; they are deleted at the integrations cutover (M1.10), which is not a breaking change.
`legacy.Covering` and `legacy.ExtConfirmer` themselves are defined by 05-storage §5S.3.11.

```go
package legacy // github.com/falkcorp/mediacore/legacy (L5; imports integrations and providerhttp)

// IntegrationsCoverID is the record ID a routed instance store's Covers
// receives: "<adapter>|<kind>/<instance>" (adapter IDs and the Ref grammar
// contain no "|", so it splits unambiguously). The Registry builds the same
// string with an unexported helper; integrations' export_test.go asserts the
// two agree (M1.1).
func IntegrationsCoverID(ref integrations.Ref, a integrations.AdapterID) string

// ProviderHTTPCoverID is the record ID a routed gate store's Covers receives:
// "<counter>|<scope>", with counter "" for the scope's hold (counter names
// are [a-z][a-z0-9_]* and contain no "|"). Mirrored and asserted the same way
// in providerhttp (M2.3).
func ProviderHTTPCoverID(scope, counter string) string

// LocalCandidate is the egress-trust predicate for instances that predate
// core (M1.10, SEC3-5, DATA5-9): ok only when rawURL's host is an IP literal
// in the loopback or private ranges; origin is the URL's scheme://host:port.
// It decides only whether an admin is OFFERED the origin to confirm; it
// grants nothing. A legacy store's one-time migration calls it; core never
// calls it at runtime.
func LocalCandidate(rawURL string) (origin string, ok bool)
```

The legacy conformance suites are `legacytest.IntegrationsInstanceStoreSuite` and
`legacytest.ProviderHTTPGateStoreSuite` (test-support package `legacy/legacytest`, one suite per store interface
with a case per capability the implementation has, 03 §3.3, SIMP6-6; M1.9, M2.9).

#### Deferred-build members

Deferral applies to members of this built package (SIMP2-6, 03 §3.4). The contract text above is kept, but the
09 S7 build of `integrations` contains none of these. The first column holds only package-qualified Go identifiers,
each absent from non-test files under `core/` per `archtest.DeferredMembersAbsent` (03 §3.4), whose list is
generated from every `Deferred-build members` heading; values, rows and tests are in the second column and are not
checked (03 §3.4 grammar, SIMP5-6, COR5-16). The PR that builds a member deletes that member's row (SIMP4-1):

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `integrations.FacadeWriter`, `integrations.Registry.FacadeWriter` | the `"arrfacade"` value of `integrations.Origin`; the M1.5 facade row; `TestFacadeIndexer_LANRefused` | `arrfacade` ([08](08-arr-facade.md)) and `integrations/indexer` | M1.3 `FacadeWriter` comment (SEC-5, SEC-15) |
| `integrations.InstanceConfig.Tags` | instance scoping (raw/16 §7.1 row 15) | phase-2 pool selection (05-download `download.Selection.Tags`, `download.GrabRequest.Tags`) and `acquire`'s tag scoping (10 G7) | a `[]string` appended to the struct, an additive change (SIMP3-11) |

The facade-confined key, query-key extraction and the facade route group that the writer depends on are
deferred members of `auth` and `httpapi` ([05-authn](05-authn.md), [05-http-api](05-http-api.md)).

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
| `POST .../instances/{id}/confirm` (`Registry.Confirm`, M1.4 step 2a) | `core:admin`, interactive (ProtectAdmin) |

### M1.4 Lifecycle

1. **Register** (kernel, before listeners): descriptors from `app.Options.Adapters` and each module's
   `Registrar.Adapter`. Duplicate `AdapterID`, a module registering outside its prefix, or a `Settings`
   schema with a secret field lacking `Secret: true` fails boot.
2. **Load**: `Load` of the native store and, when the app supplies one, of its legacy store (step 2a); one owner,
   one stored form per instance (SIMP-4, R4-1).
   There is no `core.integrations.<kind>.instances.<id>` config form of an instance. Operator-set values of the legacy
   integration-bound keys (the env-bound Deluge, OpenAI and SMTP settings ABO reads today, 09 S7) arrive from
   `config.Store.PassthroughValues()` (05-config §1.4 step 8) at the paths of the M1.10 legacy-backed instance table.
   **Operator overlay (SIMP2-5).** They are an in-memory overlay, applied at `Load` and at every config hot
   swap, and **never persisted**: no instance row, no `c/secrets/` value and no audit record is written
   from them. **One precedence rule (SEC6-5, shared with 05-config C11 and §1.12.4).** Each value arrives with
   `OperatorValue.BelowStored` (05-config §1.3.3):
   - a value under a **`BelowStored` legacy name** — every integration-bound name HEAD lays the stored blob over,
     `OPENAI_BASE_URL` among them (05-config §1.12.4) — keeps HEAD's precedence: it fills the field only while the
     instance has **no stored value** for it, locks nothing, never appears in `Locked`, and a `Put` of that field
     stores normally (after which the stored value wins);
   - a value under a **canonical spelling** overlays the stored field: the API returns the effective instance with
     `Locked` naming each such field and its `OperatorValue.By`, and those fields are read-only (`ErrFieldLocked`).

   An operator value for an instance with no stored row yields an effective instance with the stable ID of the M1.10
   table and `Stored=false`. Removing the operator value reverts to the stored row — the operator's own stored
   password comes back, as HEAD does (`internal/config/persistence.go:1959-1971` @748fd787a) — or removes the
   effective instance when no row exists. **Private-network trust follows the effective value's source, never the
   mere presence of an operator value.** A URL field is trusted for private networks without `Local` only when its
   **effective** value came from an operator layer (a canonical overlay, or a `BelowStored` value filling an
   unstored field): the operator layer then carries the same authority as `lan_allow`. When the stored value wins —
   a `BelowStored` name beside a stored URL that a `core:integrations.manage` holder wrote — the effective URL is the
   stored one and needs `Local` or `lan_allow` like any other (SEC-15, SEC2-7); `PassthroughValues()` returning the
   raw operator value confers nothing by itself (`TestPassthroughBelowStored_StoredURLNotLanTrusted`). The Registry
   records the effective source per URL field when it builds the effective view; it is not a stored field. The env
   names ABO reads today are **permanent** aliases onto these paths (05-config C7/C11). This is config's
   operator-layer lock (05-config §1.3.5) applied to instance fields; there is no second lock model.

   **2a. Legacy-backed instances (R4-1).** A legacy-backed instance is one the app's legacy `InstanceStore` covers
   (`Covers(legacy.IntegrationsCoverID(ref, adapter))`, M1.3; per-record routing, M1.1).
   Its stored form is the app's pre-core format and there is no other copy: no import, no import hash, no
   re-import, no mirror. Core calls only the interface; the store's code, key names and row formats are the
   app's (for ABO, `books` code, M1.10), and no storage grant to `integrations` or `config` is involved.
   - **Read.** At `Load` the Registry takes what the legacy store returns. An edit an older build made after a
     rollback is simply what this build reads on its next boot, subject to the suspension rule below.
   - **Write.** Every write of a legacy-backed field in this build goes through `Put`: the native routes and
     05-config's legacy `PUT /api/v1/config` alias alike, which hands its integration-bound fields to `Put`
     (05-config §1.12.2), so the trust rules of M1.3 apply once. The legacy store writes the fields in the shape
     the app's older builds read, in the caller's batch together with the instance's secrets and its extension
     record. ABO's implementation writes the settings blob through books' legacy config store (05-config §1.3.3,
     R4-6), so the blob keeps one writer: it lives in the same books package as that store, joins the caller's
     `*storage.Batch` and shares that store's pending blob render (05-config §1.6, C11), so a config write and an
     instance write in one batch render and commit the blob once.
   - **Native-only fields** — `Local` with the URL origin it was granted for, the URL origin the `secrets` copies
     were written for, `Limits`, and any field the legacy format has no place for — are kept by the legacy store
     in an **app-owned extension family that older builds never read**, never additively in the legacy rows
     (R4-1; an extra member in a legacy row is dropped or, worse, interpreted by an older build, SEC4-3). The
     records follow 05-storage §5S.3.11's one extension-record rule (SIMP4-9): keyed by the legacy instance key;
     never holding a field the legacy format holds; ignored while the legacy instance is absent and deleted by the
     store's next write of that key; and carrying a hash of the legacy fields they extend (the guarded projection
     below), so a rewrite that changes nothing does not count as a change.
   - **Field classes and suspension (R4-1, DATA4-8, SEC3-5; replaces the round-3 URL-origin reset).** The records
     use `legacy.Extension` (05-storage §5S.3.11). Guarded projection: the instance's own legacy fields, decoded
     and canonically encoded (for a blob-embedded family, that instance's entry, not the whole blob); no field is
     volatile. When an older build changed them, the record reads `ExtStale` and its fields are treated by class:

     | Field | Class | On `ExtStale` |
     |---|---|---|
     | `Local` | Widening | suspended: reads false, so the instance reaches only public hosts and `lan_allow`, until an admin calls `Registry.Confirm` (audited `core.integration.confirm`); a routine `Put` never clears it |
     | `LocalOrigin` | Independent | kept; shown by the confirm form and the health detail |
     | `SecretsOrigin` | Independent | kept; a `secrets` copy is used only while it equals the current legacy URL origin, so a secret never follows a URL an older build changed (SEC-15 across builds), and is deleted on the next write otherwise |
     | `Limits` | Independent | kept |
     | `PendingLocal` (M1.10) | Independent (grants nothing) | kept; accepted by `Registry.Confirm` only while it equals the current legacy URL origin, dropped otherwise |

     A suspended instance reports `HealthDegraded` with reason `integration_local_suspended` naming it (and counts
     in 05-storage's `storage_extension_suspended`); one whose `secrets` copy is withheld reports
     `integration_secret_origin_changed`. An `ExtOrphan` record is ignored and deleted by the store's next write.
     Nothing widens silently on roll-forward.
   - **Secrets: one mechanism, `secrets` over 05-config's `legacy.SecretsStore` (lead decision, round 6).** The
     Registry stages and reads **every** secret field of a legacy-backed instance through `secrets` under its M1.10
     name, exactly as for a native instance (`secrets.Stage` in the `Put`'s batch, `Get` through `Env.Secrets`); the
     legacy instance store never reads or writes a secret field. What happens to the app's legacy copy is
     05-config's single mechanism (§2.2–§2.4), which `secrets` drives through the books store's
     `legacy.SecretsStore`:
     - **kept names (R5-1(b)).** A secret whose legacy copy the app keeps encrypted in its own secret row (for ABO
       the OpenAI key, Google Books key and Hardcover token, HEAD's encrypted setting rows,
       `internal/config/persistence.go:1955-1957`, `:1989-2003` @748fd787a) is **not moved** during the extraction:
       `secrets.Get` returns the row's value (`SourceLegacy`, `LegacyEncrypted`), and `secrets.Stage` calls
       `SecretsStore.Write`, which writes it back into the same row in the app's cipher, in the caller's batch; no
       `c/secrets/` record and no sentinel exist. An older build therefore reads the current value after a
       rollback, and roll-forward has nothing to reconcile. A row the app's cipher cannot decrypt reads as "must be
       re-entered" (`Meta.Err` `ErrKeyUnavailable`; instance `HealthMisconfigured`, reason
       `integration_secret_unreadable`) and is kept (DATA3-12). The value moves into `secrets` at the family's
       cutover.
     - **moved names (R4-2).** Every other secret (the Deluge password, metadata credentials, the webhook secret) is
       read in place until this build first writes it (`SourceLegacy`, `LegacyPlain`); that write stages a
       `c/secrets/` record and, in the same batch, `SecretsStore.Supersede` replaces the legacy field — and every
       other plaintext copy its format keeps — with the unguessable, never-matching sentinel, never an empty value.
       A legacy value other than the sentinel beside a `secrets` record means an operator re-entered it on an
       older build after a rollback: `Get` serves it at once and `secrets.Open`'s roll-forward reconcile at 06 P4d
       step 1(ii) re-imports and re-sentinels it (05-config §2.4), so that value wins; the Registry has no
       re-import of its own. After a rollback the older build therefore sends the sentinel and fails
       authentication against the remote until the operator re-enters the secret there (fail-closed; for Deluge,
       a blank password would instead fall back to the factory-default password,
       `internal/deluge/integration.go:27-67` @748fd787a). An auth hold the older build sets that way lives in
       the shared hold rows (M2.10); it expires after `HoldPolicy` (6h for `ClassAuth`) or is released with
       `DELETE .../hold` or by a `Put` that re-supplies the secret. The release note states both.
   - **Family-level fields.** Where the legacy format stores one value for a whole family (ABO's legacy webhooks:
     enabled flag, secret and event list, 05-notify §10), the store lists the field in `InstanceConfig.Legacy.Shared`, and a `Put` that
     changes it changes it for every legacy-backed instance of that family.
   - **New instances.** A new instance whose adapter has a legacy representation is created in the legacy store
     when that family is a list (`Covers` is true for the new instance's `legacy.IntegrationsCoverID`), so older builds see it too; a singleton
     family accepts only its fixed ID (M1.10). Every other instance is native.

   A row whose adapter is not registered (module disabled, binary without the adapter) is **kept** and
   reported `HealthMisconfigured` with reason `adapter not in this binary` (restart-bound enablement, 03 §3.7).
3. **Build lazily** on first `Lease`; construction must not do network I/O (so boot never waits on a
   remote). Validation errors make the instance `HealthMisconfigured`, never fail boot.
4. **Hot swap** on `Put`, and on a config hot swap that changes an overlaid value: validate → apply the trust
   rules (Origin from the principal; `Local` only with the ProtectAdmin check; URL change drops secrets and
   `Local`, SEC-15) → persist through the instance's store (native, or the legacy store with its extension
   record, step 2a), with the secrets in one storage batch (an overlay change persists nothing) → build new → atomically
   replace → old instance drains its leases → `Close`. providerhttp state (bucket, holds, breaker, quota) is
   keyed by `Ref` and **survives** the swap; the egress trust (`Local`, `lan_allow`) is re-read per dial.
5. **Health task** `core.integrations.health` (05-tasks), scheduled by
   `tasks.ScheduleFromConfig("core.integrations.schedules.health")` (the one schedule-key rule, R2-7),
   probes every enabled instance with parallelism `tasks.Fixed(n, "network-bound probes")` where n is
   `core.integrations.health_parallelism`, and publishes `core.integration.health.changed.v1` on status
   change (consumed by [05-health](05-health.md) and [05-notify](05-notify.md) global rules).
6. **Shutdown**: stop issuing leases, wait for drain up to `core.integrations.drain_timeout`, `Close` all.

**Outbound integrations off (R6-7, OPS6-4).** `core.integrations.disabled` (M1.7) is the one switch that makes a copy
of a store inert towards the outside world, whatever its instances say: native rows, legacy-backed instances,
operator overlays and instances added later alike, so it cannot drift with the list of kinds or legacy fields. While
it is true the Registry still loads and serves every record (the Integrations page lists them, `Put` stores
normally), but it builds no instance: `Lease` returns `ErrDisabled`, every instance reports `HealthDisabled` with
reason `integrations_disabled`, the health task probes nothing, `Test` is refused (409, `ErrDisabled`), and
providerhttp refuses every `Acquire` before dialling (`ClassRefusedLocal`), so no hold, breaker or quota state is
written. Each kind file states what its callers do with `ErrDisabled` (05-ai, 05-download, 05-indexer, 05-notify).
09 §9.3's staging procedure sets it in the staging copy's operator layer before that copy's first boot from S7 on (an
operator-set value locks it, so no stored override can turn integrations back on there); a pre-S7 copy has no
integrations package, and the procedure clears its legacy integration URLs instead (09).

**Native store (apps without a legacy store; SIMP4-3).** `NativeInstanceStore` lands with `integrations` at
09 S7. Every app uses it for every instance its legacy store does not cover (routed model, M1.1); an app that passes
no `LegacyStores.Integrations` (TV/Movies, `hello`, `coretest`) uses it for all of them. A native instance does not
exist for an older build: after a rollback from S7 or later, a native instance (for ABO: a stored `notify/email`, a second download client, any
instance the legacy store does not cover) does nothing, and events during the
rollback are never delivered to it; only deliveries already pending in `c/outbox/` are sent on roll-forward
(05-notify §4 step 3). This is one row of 09 §9.2.1's table of rollback consequences (OPS5-12).

| Key | Value | Notes |
|---|---|---|
| `c/integrations/instances/<kind>/<id>` | `codec.JSON[integrations.StoredInstance](1)` | secret fields hold `secrets.Ref` only; `Local`, `LocalOrigin`, `SecretsOrigin` and `Limits` are ordinary fields of the row (no extension record, no suspension); `Version` is the row's storage version; no index rows (a kind is one prefix scan) |

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
| `Test` with remote failure | n/a (returns `TestResult{OK:false, Class, Status}` with 200; no body, no remote text) | A failed test is a successful API call; echoing remote text would make `Test` an SSRF read oracle (SEC-15). |
| URL field host/scheme/port changed without re-supplied secrets | open for the write; secrets cleared, `Local` reset, audited (`core.integration.change_url`) | A stored password or API key must never follow a URL to a new host (SEC-15). |
| A principal without the ProtectAdmin check (core:admin + interactive) sets `Local`, keeps it across a URL change, or calls `Test` with `Local=true` | closed (403) | Private-network trust is admin-level network trust, like `lan_allow` (SEC-15, SEC2-7). |
| Write to a field in `Locked` | closed (409 `ErrFieldLocked`); other fields of the same `Put` are not written either | The operator's environment owns that value; the API must not appear to change it. |
| Store write fails (native or legacy store) | closed for that `Put` (nothing written: the record, its extension record and the secrets are one batch) | The store's form is the only stored copy (R4-1). |
| Stored value undecodable at `Load` (`StoredInstance.Malformed`) | open for boot, closed for that instance: `HealthMisconfigured` naming the field; nothing is rewritten | It is the only copy; the operator repairs it through `Put`, as at HEAD. |
| App supplies no legacy store, or its `Load` fails | no legacy store: every instance is native (open). `Load` error: closed for the legacy-backed instances (none served; health `integration_store_unavailable`), open for boot and for native instances | A store that cannot be read must not be mistaken for "no instances" and overwritten. |
| Extension record stale (an older build changed the legacy fields) | open for boot; closed for trust: `Local` suspended until `Registry.Confirm`; a `secrets` copy used only while its origin equals the current URL origin; health `integration_local_suspended` / `integration_secret_origin_changed` | No silent widening on roll-forward; SEC-15 holds across builds (R4-1, SEC3-5). |
| `Confirm` by a principal without the ProtectAdmin check | closed (403) | Re-granting private-network trust is the same act as setting `Local` (SEC2-7). |
| `Confirm` of an instance that is neither suspended nor pending (native, or nothing changed) | closed (409 `ErrNothingToConfirm`) | Nothing to grant; a 200 would suggest trust changed. |
| Pending `Local` candidate whose origin no longer equals the current URL origin | closed: `Confirm` drops the candidate and grants nothing | A candidate is bound to the URL the upgrade saw (DATA5-9). |
| In-place encrypted legacy secret undecryptable (the app's cipher key missing) | open for boot, closed for that secret: `HealthMisconfigured` (`integration_secret_unreadable`); the row is kept | It is the only copy; the operator re-enters it (R5-1(b), DATA3-12). |
| `core.integrations.disabled` true (a staging copy, R6-7) | closed for every outbound call (`ErrDisabled`, `HealthDisabled` reason `integrations_disabled`); open for boot, reads and `Put` | A copy of a production store must never act on production systems (OPS6-4). |
| Facade-pushed URL not http(s), or private and not in `lan_allow` (deferred-build member) | closed (422, nothing written) | One leaked facade key must not aim scheduled fetches at the LAN (SEC-15, SEC-5). |

### M1.6 Concurrency contract

`Registry` is goroutine-safe; the instance map is an `atomic.Pointer` to an immutable snapshot, swapped
under a writer mutex. `Lease` is lock-free on the read path and uses a per-instance reference count;
`Close` runs on a dedicated goroutine (`wg.Go`) after the count reaches zero or the drain timeout fires.
Adapters must be goroutine-safe (asserted by the conformance suites with `-race`). The health task
probes at most `core.integrations.health_parallelism` instances at once; each probe has its own timeout.

### M1.7 Config keys

Protection classes per 05-config §1.3.7 (SEC2-8).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| (instance records) | data — native rows in `c/integrations/instances/`, legacy-backed instances in the app's legacy store (M1.4 step 2a) — **not** config keys (SIMP-4, R4-1) | — | yes (hot swap, M1.4) | `core:integrations.manage`; `Local` needs the ProtectAdmin check (M1.3) |
| `core.integrations.egress.lan_allow` | list of host, `host:port` or CIDR (SEC-15); applies to every instance without `Local` | `[]` | yes (read per dial) | admin |
| `core.integrations.schedules.health` | `tasks.ScheduleConfig`, declared by `integrations`, registered through `tasks.ScheduleFromConfig` (R2-7) | `{enabled: true, every: 5m}` | yes (re-read every scheduler poll) | none |
| `core.integrations.health_parallelism` | int ≥1 | `4` | yes | none |
| `core.integrations.probe_timeout` | duration | `10s` | yes | none |
| `core.integrations.drain_timeout` | duration | `30s` | yes | none |
| `core.integrations.max_instances_per_kind` | int | `32` | yes (bounds metric label cardinality) | none |
| `core.integrations.disabled` | bool: build no instance and make no outbound call (M1.4 "Outbound integrations off"; R6-7) | `false` | yes (read per `Lease` and per `Acquire`) | admin; set by 09 §9.3's staging procedure in the operator layer, which locks it |

The schedule key and `core.integrations.disabled` are new with core (HEAD has no integration health task and no
such switch), so neither has a legacy alias.

### M1.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_integrations_instances` | gauge | `kind`, `adapter`, `status` |
| `mediacore_integrations_health_status` | gauge (HealthStatus value) | `kind`, `instance` |
| `mediacore_integrations_health_probe_seconds` | histogram | `kind`, `adapter` (no `instance`: bucket × instance multiplication, ABO rule C5, `internal/ai/ai_metrics.go:36-39`) |
| `mediacore_integrations_swaps_total` | counter | `kind`, `instance` |

### M1.9 Tests

`TestRegistry_RejectsForeignAdapterPrefix`, `TestRegistry_UnknownAdapterRowRetained`,
`TestRegistry_HotSwapDrainsLeases` (synctest), `TestRegistry_OverlayNeverPersisted` (env value present: no row,
no `c/secrets/` write; SIMP2-5), `TestRegistry_OverlayRemovedRevertsToStoredRow` (the stored password returns),
`TestRegistry_LockedFieldWriteRefused`, `TestRegistry_EnvOnlyInstanceHasStableID`,
`TestRegistry_PutWritesRowAndSecretsAtomically`, `TestRegistry_TestDoesNotPersist`,
`TestRegistry_URLChangeDropsSecrets`, `TestRegistry_APIKeyCannotSetLocal`, `TestRegistry_LocalNeedsAdmin`
(interactive non-admin holding `core:integrations.manage`: `Put` and `Test` with `Local=true` refused; SEC2-7),
`TestRegistry_TestReturnsNoBody`, `TestPut_EchoedMaskIsNotResupply` (a URL change whose secret field carries an
echoed mask clears the secret; SEC3-4), `TestNativeInstanceStore_RoundTrip` (SIMP4-3),
`TestLegacyBacked_NoNativeCopy` (with a fake legacy store: no `c/integrations/instances/` row for a covered
instance; a value the store returns is what the next boot reads; R4-1), `TestLegacyBacked_ConfigAliasGoesThroughPut`,
`TestLegacyBacked_SecretMovedAndSentinelled` (a moved name: the `Put` stages it through `secrets.Stage`, whose
`SecretsStore.Supersede` sentinels every legacy copy in the same batch; never an empty value; the instance store
writes no secret field; R4-2), `TestLegacyBacked_EncryptedSecretKeptInPlace` (a kept name: the `Put`'s
`secrets.Stage` calls `SecretsStore.Write`, which writes the new value into the legacy encrypted row; nothing under
`c/secrets/`, no sentinel, and an older build's reader decrypts the new value; R5-1(b)),
`TestLegacyBacked_ReenteredLegacySecretReimported` (a non-sentinel legacy value beside a `secrets` record is served
by `Get` and re-imported by `secrets.Open`'s reconcile, never by the Registry; R4-2), `TestLegacyBacked_StaleExtSuspendsLocal` (`Local` false, health
`integration_local_suspended`; R4-1), `TestLegacyBacked_RoutinePutKeepsSuspension`, `TestLegacyBacked_ConfirmNeedsAdmin`
(ProtectAdmin, audited), `TestLegacyBacked_StaleSecretUsedOnlyForSameOrigin` (SEC3-5),
`TestLegacyBacked_StoreLoadFailureServesNone`, `TestPut_SecretChangeReleasesAuthHold`,
`TestLocalCandidate_IPLiteralOnly` (package `legacy`; SEC3-5), `TestLocalStepGrantsNothing` (the S7 migration records
`PendingLocal` only; `Local` reads false until `Registry.Confirm`; DATA5-9), `TestLocalStepSkippedStoreGrantsNothing`
(a HEAD store booted directly on S7+), `TestLocalStepAfterOldArchiveRestore` (a pre-S7 archive restored on S7+:
the migration re-runs and again only records candidates), `TestConfirmPendingLocal_OriginChangedDropsCandidate`,
`TestRegistry_LegacyWithoutCoveringCoversAll` (a legacy store without `legacy.Covering` is routed every record and
the native store is never asked; SIMP5-1, SIMP6-5), `TestLegacyMirrorsMatch` (`export_test.go`: the Registry's
unexported capability interfaces and `legacy.Covering`/`legacy.ExtConfirmer` are mutually assignable, and its
unexported record-ID helper equals `legacy.IntegrationsCoverID` on a table of refs; R6-3),
`TestRegistry_BelowStoredFillsOnlyUnstoredField` (a `BelowStored` passthrough value fills an empty field, is not in
`Locked`, and a stored value beside it wins; SEC6-5), `TestPassthroughBelowStored_StoredURLNotLanTrusted` (env
`OPENAI_BASE_URL` set to a private address, the stored URL a second private address written by a non-admin holder of
`core:integrations.manage`: the effective URL is the stored one and its dial is refused without `Local`; SEC6-5),
`TestRegistry_CanonicalOverlayLocksAndTrusts` (a canonical operator value overlays a stored URL, is `Locked`, and is
trusted for private networks; SEC6-5), `TestRegistry_DisabledMakesNoOutboundCall` (`core.integrations.disabled`:
records listed, `Lease` and `Test` give `ErrDisabled`, the health task and a transport that fails the test on any
request see nothing, no hold or quota row is written; R6-7),
`TestLegacyWebhook_SharedFieldsFamilyWide`,
`TestFacadeIndexer_LANRefused` (FacadeWriter; with the deferred facade build),
`TestRegistry_BuildDoesNoIO` (transport that fails the test on any request),
`TestClassOf_Table` (one row per class, incl. the done-parent rule), `TestError_RedactsDetail`,
archtest `IntegrationsDescriptorCallers` (only kind packages call `NewDescriptor`). Store conformance:
`integrationstest.InstanceStoreSuite(t, factory)` (public test-support package `integrations/integrationstest`,
03 §3.3, MOD5-6) runs against `NativeInstanceStore`, and an app runs it against its legacy store with its own
fixtures (ABO: books' store with HEAD fixtures, which replaces the round-3 core test of the legacy write format) —
round trip; and the app runs the one legacy suite of this interface, `legacytest.IntegrationsInstanceStoreSuite`
(`legacy/legacytest`, outside the API-stability gate with `legacy`; 03 §3.3, SIMP6-6, R6-3), against its legacy
store, with a case for each capability the store has and for its record state: `Covers` (`legacy.Covering`),
no secret field read or written by the instance store (secrets are 05-config's `legacytest.ConfigOverrideStoreSuite`
cases), `ExtStale` after an out-of-band change
of the legacy fields and not after a no-op rewrite, `Confirm` (`legacy.ExtConfirmer`), orphan extension deleted on
the next write.

### M1.10 Migration from ABO

ABO's existing instances are served by **books' legacy `InstanceStore`** (ABO code in the `books` module, over
ABO's keys and row formats, passed in the `Integrations` field of `legacy.Stores` on `Manifest.LegacyStores`; R4-1,
R6-3). Everything in this section
that names an ABO key describes that books code, not core.

| ABO today | Becomes |
|---|---|
| `config.MetadataSources[]` (`internal/config/config.go:147-163`) | legacy-backed `metadata/<id>` instances, adapters `books.<id>`, served in place from `metadata_sources` by books' store (M1.4 step 2a); `Credentials` values (plaintext in the blob) move into `secrets` under `integrations.metadata.<id>.credentials.<k>` on this build's first write of them, the legacy values sentinelled (R4-2); the Google Books key and the Hardcover token, which HEAD keeps encrypted in their own rows, stay there (kept names of 05-config §2.2, R5-1(b); table below) |
| `DelugeWebURL/Password`, `DownloadClient.Torrent.Deluge` (`config.go:225-240,1776-1780`) | the legacy-backed `download/deluge` instance (05-download §10) |
| (egress trust) | `Local` is native-only (books' extension family, M1.4 step 2a). Today these instances have no egress guard at all; from S7 a private host needs `Local` or `lan_allow`. **The upgrade grants no `Local` (DATA5-9).** At S7 books ships one versioned `Migration` (05-storage §5S.3.5; `Additive`, P4b, raises no level; never a `LegacyMigration`, which `TestNoNewLegacyMigrations` forbids after S2b; SIMP5-11, DATA5-17) that writes only books' extension family `m/books/ext/integrations/` and nothing older builds read: for each legacy-backed instance whose URL core's `legacy.LocalCandidate` accepts — the host is an **IP literal** in the loopback or private ranges — it records that URL origin as `PendingLocal`, with the legacy-field hash of M1.4 step 2a. A candidate grants nothing: the instance reads `Local=false`, reports `HealthDegraded` with reason `integration_local_pending`, and is listed on the Integrations page until an admin accepts it with `Registry.Confirm` (ProtectAdmin, audited `core.integration.confirm`), which grants `Local` bound to that origin only while it still equals the instance's URL origin. This holds whatever wrote the URL: values HEAD's `PUT /api/v1/config` wrote before S3 (a HEAD custom role holding `settings.manage` could write them), a HEAD store that skips S3–S6 and boots S7+ directly, and a pre-S7 archive restored on S7+ — whose store lacks the migration's applied record, so the migration runs again on the archive's URLs and again only records candidates. A private host given by **name** is never a candidate (a name can resolve anywhere at dial time): it is `HealthMisconfigured` with reason "private host needs Local or lan_allow" until an admin sets `Local` or lists it in `lan_allow` (SEC3-5). A host change by any writer resets `Local` and drops a candidate (M1.3 `Put`; for an older build's change, the suspension of M1.4 step 2a). From S3 every integration-bound blob field (URL, host, credentials, headers) is also `ProtectAdmin` on the config façade until S7 hands it to `Registry.Put`, and a URL-origin change through the façade before S7 clears that instance's stored secret unless the same request re-supplies it (05-config §1.3.7, §1.12.2; `TestPreS7Facade_IntegrationURLIsProtectAdmin`; SEC4-7). Release note (S7): "Integrations that reach a LAN address by IP need one confirmation by an admin on the Integrations page after this upgrade; until then they cannot connect." |
| `ai_endpoints` rows (`internal/config/ai_endpoints.go:36-68`) | legacy-backed `ai/<id>` instances, served in place from `ai_endpoints` by books' store (05-ai §10) |
| `config.Plugins["webhook"].Settings` | legacy-backed `notify/webhook-<n>` instances, served in place from `plugins.webhook` by books' store (05-notify §10) |
| `handleDelugeTestConnection`, `/ai/test-connection` | `POST /api/core/v1/integrations/{kind}/test` |
| `internal/plugin` (`Plugin`, `Registry`, `DownloadClient`, `Notifier`, `MediaPlayer`) | deleted at 09 S7 (N4; 04 §8.3); `MediaPlayer` has no successor until a media-server kind is specified |

**Legacy-backed instance table** (normative for books' store). It fixes the instance ID of every legacy singleton before the
09 S3 build ships, because S3 is the first build that can write one of the plaintext secrets and it stages each
once under its final name (05-config §2.2). The secrets HEAD already keeps encrypted (05-config §2.2's **kept** names) are not moved
during the extraction (R5-1(b)); their final name is the one the family's cutover uses. Instance IDs follow `InstanceID`'s grammar; IDs that exist in ABO today
are kept verbatim. The *Passthrough path* column is the operator-layer path 05-config hands over (M1.4 step 2, an
in-memory overlay); env aliases onto it are permanent.

| ABO source = stored form until cutover | Instance | Secret names (moved: in `secrets` from this build's first write; kept: in the legacy encrypted row until cutover, 05-config §2.2) | Passthrough paths (operator layers) |
|---|---|---|---|
| `deluge_web_url`, `deluge_web_password`, `download_client.torrent.deluge.*` (`config.go:225-240,1776-1780`) | `download/deluge` | `integrations.download.deluge.password` | `integrations.download.deluge.{url, password}` |
| `openai_api_key`, `openai_base_url` (the row ABO itself synthesises with ID `openai`, `internal/config/ai_endpoints.go:74,403-440`) | `ai/openai` | `integrations.ai.openai.api_key` (kept: HEAD's encrypted row `persistence.go:1955`) | `integrations.ai.openai.{api_key, base_url}` (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) |
| other `ai_endpoints[]` rows (`local-llm`, `local-uv`, user rows; `ai_endpoints.go:73-75`) | `ai/<row id>` | `integrations.ai.<row id>.api_key` where `auth_ref` is set | — (DB only today) |
| `google_books_api_key` (`persistence.go:1956`) | `metadata/google-books` (the source ID, `config.go:3769`) | `integrations.metadata.google-books.api_key` (kept) | `integrations.metadata.google-books.api_key` |
| `hardcover_api_token` (`persistence.go:1957`) | `metadata/hardcover` (`config.go:3779`) | `integrations.metadata.hardcover.api_token` (kept) | `integrations.metadata.hardcover.api_token` |
| other `metadata_sources[]` entries | `metadata/<source id>` | `integrations.metadata.<source id>.credentials.<k>` | — |
| `config.Plugins["webhook"]` (`urls`, `secret`, `events`; `internal/plugins/webhook/plugin.go:43-71`) | `notify/webhook-<n>` (n = 1… = position in `urls`, empty entries counted, 05-notify §10) | `integrations.notify.webhook-<n>.secret` (one family-level legacy `secret`, staged under every instance's name; `Shared`) | — |

`notify/email` is **not** legacy-backed: HEAD stores no SMTP setting (`smtp_*` are environment-only and "the config
blob never stores one", `internal/config/invite_config.go:28-29` @748fd787a). Its operator values are the overlay
of M1.4 step 2 at `integrations.notify.email.{host, port, username, password, from}`, and a `notify/email` stored
through the API is a native row that older builds never read (they keep reading the environment).

A legacy ID that does not match `InstanceID` (none at HEAD) is lower-cased with `_` → `-` by books' store on read
and mapped back on write; the mapping is logged once. No instance is imported or copied: the legacy keys stay the
stored form until cutover (M1.4 step 2a), and the Passthrough paths feed the overlay of M1.4 step 2, never the
stored form.

**Deferred: cutover.** Moving a legacy-backed family to `NativeInstanceStore` and deleting its legacy keys is not
part of the extraction (09 S0–S10). It is a later step per family — one-way, checkpointed, behind the single
per-owner level guard (05-storage), decided on measured data (02 decision 13) — and its rollback floor is the
cutover release itself. It is the first step in which core reads books' rows, so the read-delete storage grant it
needs belongs to it (05-storage §5S.3.13; R4-1). It is also the step that moves the kept secrets into
`secrets` (R5-1(b); 05-config §1.12.2). After it this file's members of package `legacy` (M1.3 "Legacy members", and the
capabilities' use here) have no implementation left and are deleted, which is not a breaking change (R6-3);
`InstanceConfig.Legacy` and `StoredInstance.Legacy` stay, always nil, and `Registry.Confirm` answers 409, until a
major (03 §3.3, 14 K23). Until then every build reads the same keys and no integration step
raises a level; the import, re-import, legacy mirror, adopt step with a Teach `Floor`, contract C7's mirror stop and
the core-side legacy-format adapter of the earlier designs do not exist (R3-1, R4-1).

### M1.11 Open items

1. `config.Schema`, `secrets.Ref`/`secrets.Value` and `UserID` are owned by 05-config / 05-authn; this file
   uses those names and changes import paths, not shapes, if they land elsewhere.
2. A sixth kind (media-server sync: Plex/Jellyfin/ABS outbound) is not specified; `plugin.MediaPlayer`
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
	Store     GateStore              // NativeGateStore over c/providerhttp/; nil = in-memory (tests)
	// Legacy is the app's legacy implementation (the Gates field of
	// legacy.Stores on Manifest.LegacyStores, 06; R4-1, R6-3) for the scopes
	// whose holds and counters predate core; nil for an app without them, and
	// for every app after the integrations cutover (it stays in v1 as the
	// routed store's legacy-half input, 03 §3.3). Routed model (M1.1,
	// SIMP5-1): a hold or counter goes to Legacy when Legacy covers its
	// legacy.ProviderHTTPCoverID(scope, counter) (legacy.Covering, named here
	// through an unexported mirror interface checked by export_test.go; a
	// legacy store without it covers everything, SIMP6-5), else to Store.
	Legacy    GateStore
	Base      http.RoundTripper      // default: a cloned http.DefaultTransport, never the global itself
	UserAgent string                 // "<app>/<version> (+mediacore/<v>)"
	Metrics   *metrics.Scope         // core scope "mediacore.providerhttp" (05-metrics)
	Log       *slog.Logger
	Now       func() time.Time
}

// GateStore persists holds and daily quota counters (R4-1). SDK surface,
// frozen by the API-stability gate, holding only the native contract
// (SIMP5-4). Core ships NativeGateStore (M2.4 "Native store"); an app passes a
// legacy implementation for the scopes it already stores (ABO: books code over
// its existing hold and budget rows, M2.10), which implements the optional
// legacy.Covering (05-storage §5S.3.11 "Legacy capabilities"; asserted at
// compile time by the implementation; R6-3). A store keeps no field its format cannot
// hold in a side record: a hold field the legacy row has no place for reads as
// its zero value.
//
// Writes take no caller batch and carry no commit-time precondition: each
// commits its own batch. They are one of the three stated exceptions to the
// store-interface batch rule, listed once in 05-storage §5S.3.11 "Store
// interfaces" (COR5-12, COR6-7, SIMP6-10), because no gate write ever belongs to a
// multi-owner commit: holds and counts are written on change, from the
// transport. The Registry is the only caller, and it serialises every
// read-modify-write of one scope's hold and of one counter under its own
// in-process mutex across the store's commit (M2.6), as HEAD's DailyBudget.mu
// does (internal/metadata/dailyquota/dailyquota.go:160 @748fd787a); the store
// is the families' only writer (05-storage §5S.9.1 row 19). Because no gate
// store uses a commit-time check, its families need no Serialized declaration
// under R5-4.
type GateStore interface {
	Holds(ctx context.Context) ([]integrations.Hold, error)
	PutHold(ctx context.Context, h integrations.Hold) error // zero Until deletes the record
	Quota(ctx context.Context, scope, counter string) (day string, used int, err error)
	PutQuota(ctx context.Context, scope, counter, day string, used int) error
}

// The record ID a routed gate store's Covers receives is
// legacy.ProviderHTTPCoverID(scope, counter) (M1.3 "Legacy members", R6-3).

// NativeGateStore is core's implementation over its own keyspace
// c/providerhttp/. It implements no legacy capability and is never asked
// Covers: it serves every scope and counter the legacy store does not cover.
func NativeGateStore(ks storage.Keyspace) GateStore

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
	// Private reports whether this instance may reach loopback/private
	// addresses on host: InstanceConfig.Local AND host is the host of one of
	// the instance's configured URL-typed fields (the bound origin; SEC4-15),
	// or host is in core.integrations.egress.lan_allow. Read on every dial, so
	// a trust change applies without a rebuild. Never consulted for
	// EgressPublicOnly, and never for link-local addresses (M1.3 EgressPolicy).
	Private func(host string) bool
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

`app.New` constructs one `Registry` with `NativeGateStore` and, when the app supplies one, its legacy gate store
(M2.10), loads persisted holds from both (dropping
expired ones; clamping any `Until` beyond `HoldPolicy.Max` — the `maxThrottleHold` rule,
`internal/metadata/throttle.go:102`) and today's quota counters **before** any instance is built, which
replaces the ordering-by-comment in `cmd/root.go:405-418`. Gate state per `Scope` is created on first
`Client` call and lives for the App. Shutdown flushes nothing extra: holds and quota are written on change.

A `Put` that changes an instance's secret field releases that instance's `ClassAuth` hold (M1.3).

**Native store (SIMP4-3).** `NativeGateStore` lands with `providerhttp` at 09 S7 and holds every scope the app's
legacy store does not cover (all scopes for an app with none; routed model, M1.1):

| Key | Value | Notes |
|---|---|---|
| `c/providerhttp/holds/<scope>` | `codec.JSON[integrations.Hold](1)` | `<scope>` path-escaped to one key segment; deleted when the hold ends |
| `c/providerhttp/quota/<scope>/<counter>/<day>` | `codec.JSON[quotaCount](1)` (`Used int`) | `<day>` is `YYYY-MM-DD` in the limit's `Zone`; days before yesterday are deleted on the next write of that counter |

Per attempt (transport order, outer → inner):

```
ctx done? → ClassCanceled (nothing recorded)
hold active and !WithBypassHold → ClassRefusedLocal
breaker Open → ClassRefusedLocal; Open & cooldown elapsed → take the single probe lease (others refused)
bucket.Wait(ctx)
quota.Reserve(tier, counter) → ClassRefusedLocal when spent (counted per request SENT; never refunded)
egress dial guard (EgressPolicy + Policy.Private, on the resolved address: no DNS-rebinding bypass)
base.RoundTrip
redirect (EgressConfigured / EgressSameHostOrPublic) to a host other than the request's →
    re-checked under the EgressPublicOnly rule before it is followed (SEC4-15)
classify → breaker/hold/metrics update → retry if Class.Retryable() && attempt < MaxRetries && replayable
```

### M2.5 Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| Hold active | closed (`ClassRefusedLocal`, nothing sent) | A held provider is removed from the chain, not called and failed (`throttle.go:32-35`). |
| Breaker open | closed | Ride out a blip without traffic. |
| `core.integrations.disabled` true (R6-7) | closed (`ClassRefusedLocal`, nothing dialled, no hold, breaker or quota write) | A staging copy must never reach production systems (OPS6-4, M1.4). |
| Half-open probe lease expires without an outcome | **reopens** (counts as one failure) | Fixes defect 2: a probe can never strand the breaker. |
| Outcome is `ClassCanceled`, `ClassRefusedLocal`, `ClassNotFound`, `ClassInvalid`, `ClassBadOutput` | not counted by breaker or hold | Fixes defect 3; these say nothing about the remote's health. |
| Daily quota spent | closed for Background at `Background`, for Interactive at `Total` | Reserve quota for people (owner decision recorded at `dailyquota.go:6-11`). |
| Quota store read fails | **closed** (`ErrBudgetUnavailable`) | Over-counting is the safe direction (`dailyquota.go:100-106` rationale); a quota we cannot read could be spent. |
| Hold store write fails | open (hold stays in memory; one warning per Scope) | Losing persistence must not disable the hold for this process. |
| Egress guard refuses the address | closed (`ClassRefusedLocal`) | Security control: by default nothing reaches loopback, private or link-local networks; only an instance an interactive admin marked `Local` (for the hosts of its own configured URLs) or a `lan_allow` host may, and data-derived URLs never may (SEC-15). |
| Redirect to another host that is private | closed (`ClassRefusedLocal`, redirect not followed) | `Local` is bound to the instance's configured origins; a redirect must not carry private trust to another LAN host (SEC4-15). |
| Gate store read fails at construction (native or legacy) | quota: closed for that scope (`ErrBudgetUnavailable`); holds: open (in memory only, one warning per store) | Same directions as the quota-read and hold-write rows. |
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
it is what makes restarts safe, `dailyquota.go:151-153`). That mutex is held across the gate store's own commit,
and hold changes of one scope are serialised the same way, so the store needs no commit-time check (M2.3): it is
the only writer of its families, and the Registry is its only caller.

### M2.7 Config keys

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.providerhttp.fallback.rps` | float | `2.0` | yes | none |
| `core.providerhttp.fallback.burst` | int | `2` | yes | none |
| `core.providerhttp.fallback.max_retries` | int | `3` | yes | none |
| `core.providerhttp.fallback.timeout` | duration | `30s` | yes | none |
| `core.providerhttp.retry_after_max` | duration | `5m` | yes | none |
| `core.providerhttp.hold.max` | duration | `24h` | yes | none |
| `core.providerhttp.user_agent` | string | `<app>/<version> (+mediacore/<v>)` | no | none |
| per instance: `limits.rps`, `limits.burst`, `limits.max_retries`, `limits.timeout`, `limits.tier` (`low`=0.5×, `medium`=1×, `high`=2× the adapter default, `internal/config/config.go:165-190`), `limits.daily.total`, `limits.daily.background`, `limits.daily.zone` | per field | adapter default | yes (in place, M2.3 `SetLimits`) | instance rules (M1.3) |

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
`TestEgress_PrivateRefusedUnlessLocal`, `TestEgress_LANAllowListAdmits`, `TestEgress_LinkLocalAlwaysRefused`,
`TestEgress_RebindingCheckedAtDial`, `TestEgressSameHostOrPublic` (same host still under the instance's guard),
`TestLocalInstance_OtherPrivateHostRefused` (a `Local` instance dialling a private host that is not one of its
configured hosts is refused; SEC4-15), `TestLocalInstance_RedirectToOtherPrivateHostRefused` (SEC4-15),
`TestRedactsApikeyInURLError`, `TestNoGlobals` (R5 archtest covers the package), `TestNativeGateStore_RoundTrip`
(SIMP4-3), `TestGate_LegacyCoveredScopesNeverNative` (with a fake legacy store: a covered scope's hold and count go
only to it, others only to `c/providerhttp/`; R4-1), `TestGate_LegacyWithoutCoveringCoversAll` (a legacy store
that does not implement `legacy.Covering` is routed every scope and counter; SIMP5-4, SIMP6-5),
`TestGateLegacyMirrorsMatch` (`export_test.go`: the unexported mirror of `legacy.Covering` and the record-ID helper
agree with `legacy`; R6-3), `TestGate_DisabledRefusesBeforeDial` (`core.integrations.disabled`: `Acquire` refuses
with `ClassRefusedLocal`, no hold, breaker or quota write; R6-7), `TestQuota_ConcurrentReservationsNoLostCount` (`-race`: N goroutines reserving on one counter persist
exactly N; R5-4). Store conformance: `providerhttptest.GateStoreSuite(t, factory)` (public test-support package
`integrations/providerhttp/providerhttptest`, 03 §3.3, MOD5-6) runs against `NativeGateStore`, and an app runs it
against its legacy store (ABO: books' store with HEAD fixtures — a hold and count it writes are read by HEAD's
loader and one written by HEAD is honoured), together with `legacytest.ProviderHTTPGateStoreSuite` (its one legacy
suite: the `Covers` case; 03 §3.3, SIMP6-6, R6-3).

### M2.10 Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/metadata/providerhttp` | `core/integrations/providerhttp` (Client/limits/retry code reused; globals removed) |
| `internal/metadata/dailyquota` | `providerhttp` daily quota (same counter semantics; zone configurable) |
| `internal/metadata/throttle.go`, `throttle_registry.go` (`ThrottleRegistry`, `ThrottleStore`) | `providerhttp` holds; `ClassifyProviderError` → `providerhttp.Classify` |
| `internal/metadata/circuitbreaker.go` `CircuitBreaker` | `providerhttp` breaker (with probe lease); `ProtectedSource`/`NewChainSource` deleted — the gate is in the transport |
| `internal/security/safehttp` | the dial guard for every `EgressPolicy` (private ranges refused unless `Policy.Private`); package stays for non-integration callers (`internal/covers`) until 04 moves it |
| Pebble keys `provider_throttle:<id>` (`internal/database/provider_throttle.go:29`), `provider_daily_budget:<id>` (`dailyquota.go:85`) | served **in place**, in HEAD's shapes, by **books' legacy `GateStore`** (ABO code over ABO's rows, passed in the `Gates` field of `legacy.Stores` on `Manifest.LegacyStores`; R4-1, SIMP4-2, R6-3) for the scopes `metadata/<id>` (hold, and the default `requests` counter); no core grant, nothing copied or deleted, so a rollback keeps every hold and the day's count, and holds an older build sets are honoured on roll-forward. Holds and counters of every other scope or counter (non-metadata instances; named counters such as the indexer's `query`/`grab`) are in `NativeGateStore` (`c/providerhttp/`, M2.4), which older builds never read. Moving the metadata scopes there belongs to the integrations cutover (M1.10) |
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
	Parallelism tasks.Parallelism         // required (R7 type); network-bound: e.g. tasks.Fixed(4, "provider rate limits")
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

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `metadata/<id>` instance records | M1 store, not config | none | yes | instance rules (M1.3) |
| instance setting `cache.positive_ttl` | duration | module default (books: `metadata_fetch_cache_ttl_days`) | yes | instance rules (M1.3) |
| instance setting `cache.negative_ttl` | duration | module default (books: `metadata_fetch.negative_ttl_days`, `internal/metafetch/pcache.go:52-53`) | yes | instance rules (M1.3) |
| `core.metadata.per_provider_timeout` | duration | `20s` | yes | none |

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

None open. The round-4 objection on 03 §3.3's `integrations` row (SIMP4-12) is resolved: 03 calls the
package "the one registry for integration instances", native vs legacy-backed, and its store-interface table
lists `integrations.InstanceStore`. The two objections of v1.0.0 are resolved in 03 v1.2.0: the root package `integrations` is in §3.3 and R4 (L4),
and `Registrar.Adapter(integrations.Descriptor)` is the shape 06 adopted. Notification topics are event topics
(05-notify §3), so no `NotificationTopic` registrar method is needed.

## Out-of-scope defects found

None outside this file's area. (The four providerhttp/breaker defects in M2.2 are in scope and are
removed by the design.)

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SIMP-4: `integrations` is the one integration-instance store (settings + `secrets.Ref`s, secret names
  `integrations/<kind>/<id>/<field>` — superseded by the dotted form `integrations.<kind>.<id>.<field>` in the
  integration pass); file-defined config instances removed; legacy env sources applied as read-only
  `Origin=pinned` rows for the compat window (superseded in round 2 by the in-memory overlay, SIMP2-5).
- SEC-15: private, loopback and link-local egress refused by default in the providerhttp dial guard
  (`Policy.Private`, `InstanceConfig.Local`, `core.integrations.egress.lan_allow`); URL change clears secrets
  and `Local`; API keys cannot set `Local`; `TestResult` carries no body; facade-pushed URLs validated.
- SEC-5: `integrations.FacadeWriter` (deferred-build) accepts `<m>:arr.indexers`, forces `Origin=arrfacade`
  and the SEC-15 egress restrictions.
- SIMP-6: health task scheduled by `tasks.ScheduleFromConfig` (key superseded in round 2 by
  `core.integrations.schedules.health`, R2-7).
- SIMP-10: `InstanceConfig.Owner` (personal instances) removed with notify's personal channels.
- SIMP-17: `tasks.Fixed(n, reason)` with a reason.
- COR-17: `EntityRef` open item removed; `entity.Ref` (03 v1.2.0) is the one reference type.
- COR-19: stale objections (root `integrations` package, `Registrar.Adapter`) removed as resolved by 03 v1.2.0.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-E request (SIMP-4, SIMP-15) — M1.10 integrations import table fixes every legacy singleton's instance ID (`download/deluge`, `ai/openai`, `metadata/google-books`, `metadata/hardcover`, `notify/email`, `notify/webhook-<n>`) before the S3 build; secret names use 05-config's `integrations.<kind>.<instance>.<field>`; operator values arrive through `Passthrough`; `config.Schema` already used.
- integration pass: R-H request — one `OPENAI_*` rule: permanent env aliases; `Origin=pinned` rows exist while an operator value is set (M1.4 step 2), matching 05-config C7. (Superseded in round 2: pinned rows replaced by the in-memory overlay, SIMP2-5.)
- integration pass: R-D request (SEC-15) — SSRF and URL-change rules verified present; `lan_allow` corrected to `ProtectAdmin` (was `ProtectInteractive`, contradicting SEC-15 and 05-config §1.3.7).
- integration pass: Global sweep — providerhttp `Options.Meter metrics.Registry` → `Metrics *metrics.Scope` (05-metrics has no `Registry`).

Round 2 (`docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-7 / COR2-6 / SIMP2-1 — `core.integrations.health_interval` (duration) replaced by the declared schedule key
  `core.integrations.schedules.health` (`tasks.ScheduleConfig`) in M1.4 step 5 and M1.7.
- SEC2-7 — `Local` (set, or kept across a URL change) and `Test` with `Local=true` need the ProtectAdmin check
  (`core:admin` + interactive); URL fields are written by any `core:integrations.manage` holder; other writers
  get public + `lan_allow`, which applies to every instance without `Local`; `TestRegistry_LocalNeedsAdmin`.
- SIMP2-5 — operator values are an in-memory overlay (never persisted; `Locked` field map, `Stored`,
  `ErrFieldLocked`); `Origin` loses `"pinned"` and gains `"import"` (superseded in round 3 by `"legacy"`, R3-1); removal reverts to the stored row (HEAD
  semantics); overlay tests replace `TestRegistry_PinnedInstanceReadOnly`; history lines marked superseded.
- DATA2-10 — M1.4 step 2a: hash-keyed re-import of every M1.10 instance (URL and secret together) every boot
  and a same-batch mirror of instance edits back to the legacy keys until 09 C7; two tests; M1.5 row.
  (Superseded in round 3 by R3-1: legacy-backed instances are read and written in place; no import, no mirror.)
- SIMP2-6 — FacadeWriter, the `arrfacade` origin, its error row and test listed as deferred-build members with
  an S7 grep done-check.
- SIMP2-4 — the ABO migration of this package is 09 S7 only (with notify v1).
- SIMP2-16 — the slash-form secret-name history line marked superseded by the dotted form.
- SEC2-8 — M1.7 gains a Protection column (M2.7 and M3.7 too, for the same rule).
- M1.10's two migration rows that had been displaced below the import table moved back into their table.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): M1.10: the S7 import is adopt step `core/integrations#1` stamping `Floor` 5 (09 v1.2.0); URL changes audited as `core.integration.change_url` (05-rbac §7.1). (The import and its Teach `Floor` superseded in round 3 by R3-1: `core/integrations#1` only writes the initial `Local` grants, without `Floor`.)

Round 3 (`docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — M1.1: native rows vs **legacy-backed** instances, one stored form each; M1.4 step 2a rewritten: read and
  write in place through 05-config's legacy-format adapter (logged grant), every legacy-backed write through `Put`
  (legacy config alias included), core-only extension record `c/integrations/ext/` for native-only fields, secrets
  moved to `secrets` on this build's first write and blanked (rollback = re-enter), family-level `Shared` fields,
  new instances in list families; M1.3 `Legacy`, `Shared`, `Version` rule, `Origin` `"legacy"` replaces `"import"`;
  M1.4 step 4, M1.5 rows, M1.7 row, M1.9 tests; M1.10 import table → legacy-backed instance table (`notify/email`
  native), adopt step/`Floor` and C7 text removed, "Deferred: cutover" paragraph; M2.3 `LegacyGateStore`, M2.4, M2.9
  and M2.10: `metadata/<id>` holds and quota read and written in place, no one-time import and delete.
  (Superseded in round 4 by R4-1: the legacy formats are served by app-supplied stores, not core adapters under a
  grant; the core-only `c/integrations/ext/` became an app-owned extension family; secrets are sentinelled, R4-2.)
- DATA3-3 — resolved by R3-1: no contract deletes the blob; integration-bound settings are read in place.
- OPS3-8 — resolved by R3-1: nothing is mirrored, so no C7 mirror stop exists to leave older builds stale.
- SEC3-5 — `Local` granted once by `core/integrations#1`, IP-literal private hosts only, bound to the URL origin;
  an origin change by any writer resets it (`integration_local_reset`); two tests. (Superseded in round 4: the
  grant is a books legacy step using `InitialLocalGrant`, and an older build's change suspends `Local` until
  `Registry.Confirm`, health `integration_local_suspended`, R4-1.)
- SEC3-4 — `Put`'s "re-supplied secret" excludes empty, echoed and mask-shaped values, judged before round-trip
  restoration, since the legacy config alias now writes through `Put`; `TestPut_EchoedMaskIsNotResupply`.
- SIMP3-11 — `InstanceConfig.Tags` moved to the deferred-build members.
- SIMP3-12 — the round-2 `Origin`/DATA2-10 lines and the round-2 integration-pass `Floor` line marked superseded.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): M1.10 `core/integrations#1` "raises no level" (no `Floor` wording); `internal/plugin` deleted at 09 S7.

Round 4 (`docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 — `integrations.InstanceStore` (SDK) with `NativeInstanceStore` over `c/integrations/instances/` and the
  app's legacy store via `Manifest.LegacyStores.Integrations` (books code for ABO); M1.1, M1.3, M1.4 step 2/2a/4,
  M1.5, M1.7, M1.9, M1.10 rewritten: no core-side legacy-format adapter, no grant, no ABO key name in core text;
  native-only fields in an app-owned extension family (`storage.Extension`) with a legacy-field hash and per-field
  classes (`Local` Widening, suspended until the audited ProtectAdmin `Registry.Confirm`, new route; the rest
  Independent); the initial `Local` grant is a books legacy step using core's `InitialLocalGrant`; `providerhttp.GateStore`
  with `NativeGateStore` and the app's legacy store (M2.3, M2.4, M2.9, M2.10).
- R4-2 — legacy-backed secrets: the legacy field and every plaintext copy get the never-matching sentinel
  (`StoredInstance.Moved`); a re-entered legacy value is re-imported; a secret change releases the `ClassAuth` hold.
- SIMP4-1 / COR4-19 — `Deferred-build members` is a heading with package-qualified identifiers; the per-file S7
  grep replaced by `archtest.DeferredMembersAbsent`.
- SIMP4-2 — `providerhttp.LegacyGateStore` (kernel-built under a grant) replaced by the app-supplied `GateStore`.
- SIMP4-3 — "Native store" tables for instances (M1.4) and holds/quotas (M2.4); round-trip tests and public store suites.
- SIMP4-9 / DATA4-8 — the extension records cite 05-storage §5S.3.11's one rule; the URL-origin staleness rule is
  generalised to the legacy-field hash.
- SIMP4-12 — intro table says "registry"; 03 wording raised as objection 1.
- SEC4-7 — M1.10: the initial `Local` grant sees only admin-written URLs (pre-S7 façade `ProtectAdmin` and
  secret-clearing rule, 05-config).
- SEC4-15 — `Policy.Private` bound to the instance's configured hosts; cross-host redirects re-checked public-only
  (M1.3 `Local`, M2.3, M2.4, M2.5); two tests.

Integration pass (round 4):

- integration pass (round 4): M1.4 step 2a: books' legacy instance store shares the books config store's pending blob render (05-config §1.6, C11); objection 1 (SIMP4-12) removed, settled by 03.

Round 5 (`docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- R5-1(b) — M1.4 step 2a, M1.10: secrets HEAD already keeps encrypted (OpenAI key, Google Books key, Hardcover
  token) are `LegacyRecord.InPlace`: read and written in place in HEAD's encrypted rows by books' store, never
  moved or sentinelled during the extraction, moved at the cutover; only plaintext secrets move;
  `TestLegacyBacked_EncryptedSecretKeptInPlace`; M1.5 unreadable-row row.
- R5-4 / DATA5-1 — M2.3, M2.6: `GateStore` writes commit their own batch under the Registry's per-scope and
  per-counter mutex (HEAD's `DailyBudget.mu`), with no commit-time check, so its families need no `Serialized`
  declaration; stated as an exception to the batch rule (COR5-12); `TestQuota_ConcurrentReservationsNoLostCount`.
- SIMP5-1 — M1.1 "Store model": `InstanceStore` and `GateStore` (with 05-notify's `RuleStore` and 05-library's
  `RootSource`) route per record; the other store interfaces are exclusive; `backends` = `legacy+native`; one
  `Covers(id string)` shape over `integrations.CoverID` / `providerhttp.CoverID`.
- SIMP5-4 — `Covers` and `Confirm` moved off `InstanceStore`/`GateStore` into the optional `storage.Covering` /
  `storage.ExtConfirmer`; `StoredInstance`'s legacy-only fields grouped in `LegacyRecord` (nil for native);
  `ErrNothingToConfirm`; legacy suite split; `TestRegistry_NativeStoreCoversAll`, `TestGate_StoreWithoutCoveringCoversAll`.
- SIMP5-6 / COR5-16 — M1.3 `Deferred-build members` table: identifier column and an unchecked column.
- SIMP5-11 / DATA5-17 — M1.10: the S7 step is a books versioned `Migration` (`Additive`, writes only
  `m/books/ext/integrations/`), not a legacy step.
- DATA5-9 — M1.10, M1.4 step 2a, M1.3: the S7 migration grants no `Local`; it records `PendingLocal` candidates
  (`integrations.LocalCandidate`, renamed from `InitialLocalGrant`) that an admin accepts with `Registry.Confirm`;
  re-runs on a skipped-stage store and after a pre-S7 archive restore; release note; four tests.
- OPS5-12 — M1.4 native store: a native instance does nothing after a rollback and misses events meanwhile.
- MOD5-6 — M1.9, M2.9: store suites are public test-support packages (`integrationstest`, `providerhttptest`).

Round 6 (`docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-3 / SIMP6-2 / SIMP6-3 — M1.1 "Legacy-only identifiers live in package `legacy`"; M1.3 "Legacy members":
  `integrations.CoverID` → `legacy.IntegrationsCoverID`, `providerhttp.CoverID` → `legacy.ProviderHTTPCoverID`,
  `integrations.LocalCandidate` → `legacy.LocalCandidate`; `storage.Covering`/`storage.ExtConfirmer` →
  `legacy.Covering`/`legacy.ExtConfirmer`, named by the Registry and the gate router through unexported mirror
  interfaces checked by `export_test.go`; `legacy.Stores` fields `Integrations`/`Gates`; what stays in v1 (record
  legacy-state pointers, `Registry.Confirm`, `providerhttp.Options.Legacy`) stated; M1.10 cutover deletes the
  `legacy` members (not a break); library roots are legacy-only, not routed; `TestLegacyMirrorsMatch`,
  `TestGateLegacyMirrorsMatch`.
- R6-7 / OPS6-4 — M1.4 "Outbound integrations off", M1.5 and M2.5 rows, M1.7 key `core.integrations.disabled`
  (set by the staging procedure in the operator layer); `TestRegistry_DisabledMakesNoOutboundCall`,
  `TestGate_DisabledRefusesBeforeDial`.
- SEC6-5 — M1.4 step 2: one precedence rule with 05-config C11/§1.12.4: a `BelowStored` passthrough value fills
  only an unstored field and locks nothing; a canonical value overlays and locks; private-network trust follows the
  effective value's source only; `InstanceConfig.Locked` comment; three tests incl.
  `TestPassthroughBelowStored_StoredURLNotLanTrusted`.
- SIMP6-5 — M1.1, M1.3, M2.3: the default stated once (only the legacy half implements `Covers`; without it, it
  covers every record and the ledger records `legacy`); tests renamed `TestRegistry_LegacyWithoutCoveringCoversAll`,
  `TestGate_LegacyWithoutCoveringCoversAll`; one helper name, `<Pkg>CoverID`, in `legacy`.
- SIMP6-6 — M1.9, M2.9: one legacy suite per store interface, `legacytest.IntegrationsInstanceStoreSuite` (replaces
  `integrationstest.LegacyInstanceStoreSuite`) and `legacytest.ProviderHTTPGateStoreSuite`, a case per capability.
- SIMP6-7 — M1.3: `InstanceConfig.Legacy bool` + `Shared` → one pointer `Legacy *InstanceConfigLegacyState`;
  `LegacyRecord` → `StoredInstanceLegacyState` (the `Legacy *<Type>LegacyState` rule of 03 §3.3).
- COR6-7 / SIMP6-10 — M2.3: the gate store's own-batch writes cite 05-storage's one list of three exceptions.
- integration pass (round 6, lead decision: one legacy-secret mechanism): `StoredInstanceLegacyState` drops `InPlace`, `Moved` and `Secrets`; M1.4 step 2a reads and stages every secret field through `secrets`, which alone reaches 05-config's `legacy.SecretsStore` (kept names written in place, moved names sentinelled, re-entry re-imported by `secrets.Open` at 06 P4d step 1(ii)); tests, the legacy suite cases and the legacy-backed table use 05-config's kept/moved terms; the out-of-scope "spec defect" note is removed; `legacy.Extension` replaces `storage.Extension`. Already applied: M1.4 step 2's `BelowStored` passthrough names (SEC6-5).
