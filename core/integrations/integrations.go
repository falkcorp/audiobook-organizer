// file: core/integrations/integrations.go
// version: 1.1.0
// guid: 1e407ce3-b439-4eda-a361-798c3530fdb0
// last-edited: 2026-11-11

// Package integrations is the kind-neutral base of every integration contract
// (05-metadata M1): adapter descriptors, the one integration-instance
// registry and its store interface (settings plus the secrets.Refs of an
// instance's secret fields), health, test, egress trust and the error
// taxonomy. It owns every integration-instance record; config holds none.
//
// The package is dark until the integrations flip (F7-09): nothing in an app
// registers an adapter or builds a Registry before it.
//
// Layer L4 (03 R4). It holds no package-level mutable state (R5): a Registry
// is per App.
package integrations

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/config"
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

// Kinds lists the closed set in order (name coined by F7-01).
func Kinds() []Kind { return []Kind{KindAI, KindDownload, KindIndexer, KindMetadata, KindNotify} }

// Valid reports whether k is one of Kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindAI, KindDownload, KindIndexer, KindMetadata, KindNotify:
		return true
	}
	return false
}

// InstanceID is operator-chosen, [a-z0-9][a-z0-9-]{0,31}, unique within a Kind.
//
// Validate is stricter than that grammar by one rule: the first character
// must be a letter. An instance's secrets are named
// "integrations.<kind>.<instance>.<field>" and a secrets.Name segment needs a
// leading letter (05-config §2.3), so an ID such as "0dark" could hold no
// secret. Refusing it fails closed; the conflict is a spec defect recorded by
// F7-01.
type InstanceID string

// maxInstanceID is the longest InstanceID: one leading character plus 31.
const maxInstanceID = 32

// Validate reports whether id follows the grammar above.
func (id InstanceID) Validate() error {
	s := string(id)
	if s == "" || len(s) > maxInstanceID {
		return fmt.Errorf("%w: instance ID %q must be 1 to %d characters", ErrInvalid, s, maxInstanceID)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return fmt.Errorf("%w: instance ID %q does not match [a-z][a-z0-9-]{0,31}", ErrInvalid, s)
		}
	}
	return nil
}

// AdapterID is "<moduleID>.<name>": "core.qbittorrent", "books.audible".
type AdapterID string

// maxAdapterName is the longest name part of an AdapterID.
const maxAdapterName = 64

// Prefix is the module part of the ID ("core" in "core.qbittorrent"), or ""
// when the ID has no '.'.
func (a AdapterID) Prefix() string {
	for i := 0; i < len(a); i++ {
		if a[i] == '.' {
			return string(a[:i])
		}
	}
	return ""
}

// Validate reports whether a is "<origin>.<name>": origin "core" or a module
// ID ([a-z][a-z0-9]{1,15}, 03 §3.6), name [a-z][a-z0-9_-]{0,63}.
func (a AdapterID) Validate() error {
	p := a.Prefix()
	if !validOrigin(p) {
		return fmt.Errorf("%w: adapter ID %q: want <module>.<name> with a module ID of [a-z][a-z0-9]{1,15}", ErrInvalid, string(a))
	}
	name := string(a[len(p)+1:])
	if name == "" || len(name) > maxAdapterName {
		return fmt.Errorf("%w: adapter ID %q: the name must be 1 to %d characters", ErrInvalid, string(a), maxAdapterName)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return fmt.Errorf("%w: adapter ID %q: the name does not match [a-z][a-z0-9_-]{0,63}", ErrInvalid, string(a))
		}
	}
	return nil
}

// validOrigin reports whether s is "core" or a module ID [a-z][a-z0-9]{1,15}.
func validOrigin(s string) bool {
	if s == "core" {
		return true
	}
	if len(s) < 2 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// Ref addresses one instance. It is the ONLY key used for limits, holds,
// breakers, health, metrics and storage. String() is "<kind>/<instance>".
type Ref struct {
	Kind     Kind
	Instance InstanceID
}

func (r Ref) String() string { return string(r.Kind) + "/" + string(r.Instance) }

// Validate reports whether r names a valid kind and instance ID.
func (r Ref) Validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("%w: kind %q is not one of ai, download, indexer, metadata, notify", ErrInvalid, string(r.Kind))
	}
	return r.Instance.Validate()
}

// Meta describes an adapter type. One per adapter, registered once.
type Meta struct {
	ID           AdapterID
	Kind         Kind
	DisplayName  string
	Version      string
	InfoURL      string        // public docs link, shown in the UI
	Settings     config.Schema // field list; fields tagged Secret are stored via secrets (05-config)
	Egress       EgressPolicy  // enforced by providerhttp (M2)
	Limits       Limits        // providerhttp defaults for every instance of this adapter
	MaxInstances int           // 0 = unlimited
	Origin       string        // "core" or the registering module ID; set by the kernel
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
func NewDescriptor[T Instance](m Meta, build func(ctx context.Context, env Env, inst InstanceConfig) (T, error)) Descriptor {
	d := Descriptor{meta: m}
	if build != nil {
		d.build = func(ctx context.Context, env Env, inst InstanceConfig) (Instance, error) {
			v, err := build(ctx, env, inst)
			if err != nil {
				return nil, err
			}
			return v, nil
		}
	}
	return d
}

// Meta returns the adapter's description.
func (d Descriptor) Meta() Meta { return d.meta }

// InstanceConfig is one instance as the Registry serves it: the record the
// InstanceStore loaded plus the effective-view fields (Locked, Stored).
// All instances are global (admin-managed); per-user instances are deferred with
// notify's personal channels (05-notify §11).
type InstanceConfig struct {
	Ref      Ref
	Adapter  AdapterID
	Name     string
	Enabled  bool
	Priority int    // lower is preferred; meaning is per kind
	Origin   Origin // set by the Registry from the writing principal, never by the caller
	// Local allows the hosts of the instance's configured URL-typed fields (and
	// no other host) to be loopback or private addresses (M2 egress guard,
	// M2.4; SEC4-15). Setting it, or keeping it across a URL change, needs the
	// ProtectAdmin check: core:admin AND an interactive principal (05-config
	// §1.3.7; SEC-15, SEC2-7). Any other writer's instance reaches public
	// addresses plus the hosts in core.integrations.egress.lan_allow only.
	Local    bool
	Settings map[string]any // validated against Meta.Settings; secret fields hold secrets.Ref only
	Limits   *Limits        // optional per-instance override of Meta.Limits (zero fields inherit)
	Version  uint64         // optimistic-concurrency token for PUT: the row's storage version
	Updated  time.Time
	// Locked lists the fields an operator-layer value currently overlays and
	// locks (field -> OperatorValue.By, which names the layer and spelling;
	// M1.4 step 2, SEC6-5). Set by the Registry on the effective view only;
	// never stored (SIMP2-5).
	Locked map[string]string
	// Stored is false for an effective instance that exists only because an
	// operator value names it (no row in c/integrations/instances/).
	Stored bool
}

// Origin records who wrote the stored row last.
//
//	"ui"        interactive session holding core:integrations.manage
//	"api"       API-key principal holding core:integrations.manage (cannot set Local)
//	"migration" written by an app's cutover step through Importer (M1.10);
//	            the next Put replaces it
//
// The facade origin of the arr facade is a deferred-build member and is not
// declared here. There is no "pinned" origin: operator values are an overlay,
// not a writer.
type Origin string

// The Origin values of the R2 build.
const (
	OriginUI        Origin = "ui"
	OriginAPI       Origin = "api"
	OriginMigration Origin = "migration"
)

// Env is everything an instance may use. It is constructed per instance by
// the Registry and is the ONLY way an adapter reaches the outside world.
//
// 05-metadata M1.3 also declares Paths library.PathMapper (05-library). That
// type lands with F7-06 (wave W31, after this row), so the field is added
// there, an additive change; until then no adapter maps a remote path.
type Env struct {
	Ref     Ref
	Log     *slog.Logger     // child logger: integration.kind, integration.instance, adapter
	HTTP    HTTP             // instance-scoped; implemented by *providerhttp.Client (M2)
	Gate    Gate             // same gate without HTTP, for SMTP/subprocess adapters
	Secrets SecretReader     // reads only the refs declared in this instance's Settings
	Now     func() time.Time // clock; fake in coretest
}

// SecretReader resolves a declared secret at use time (not at build time), so
// rotation takes effect on the next request without a rebuild. A withheld
// operator-sourced secret of a native instance returns
// ErrOperatorSecretWithheld (M1.4 step 2, SEC7-3, R8-5); the adapter's call
// then fails before sending (ClassRefusedLocal).
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

// DailyLimits is a per-day request allowance.
type DailyLimits struct {
	Total, Background int            // Background ≤ Total; Interactive may use the rest
	Zone              *time.Location // quota-day boundary; default UTC
	Counters          []string       // named counters, e.g. indexer "query" and "grab"; default ["requests"]
}

// BreakerPolicy configures the circuit breaker.
type BreakerPolicy struct {
	Threshold    int           // consecutive CountsAgainstRemote failures; default 5
	Cooldown     time.Duration // default 30s; SHORT on purpose (circuitbreaker.go:151-166 rationale kept)
	ProbeTimeout time.Duration // a half-open probe lease expires after this; default = Limits.Timeout
}

// HoldPolicy configures persisted holds.
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

// BreakerState is a circuit breaker's state.
type BreakerState uint8

// The breaker states.
const (
	BreakerClosed BreakerState = iota
	BreakerHalfOpen
	BreakerOpen
)

// GateState is a gate's current admission state.
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

// Tester is the kind's extra test (caps fetch, login) that Registry.Test runs
// after Health on an instance that implements it (name coined by F7-01;
// 05-metadata M1.3 "runs Health and the kind's extra test"). Its error is
// classified with ClassOf; an *Error's Status becomes TestResult.Status.
type Tester interface {
	Test(ctx context.Context) error
}

// HealthStatus is ordered: a higher value is worse.
type HealthStatus uint8

const (
	HealthOK            HealthStatus = iota
	HealthDegraded                   // reachable, but held, slow, or missing an optional capability
	HealthDown                       // unreachable or 5xx
	HealthAuthFailed                 // credentials rejected; needs a human
	HealthMisconfigured              // settings invalid, secret missing
	HealthDisabled                   // Enabled=false
)

// String is the status's metric label value.
func (s HealthStatus) String() string {
	switch s {
	case HealthOK:
		return "ok"
	case HealthDegraded:
		return "degraded"
	case HealthDown:
		return "down"
	case HealthAuthFailed:
		return "auth_failed"
	case HealthMisconfigured:
		return "misconfigured"
	case HealthDisabled:
		return "disabled"
	}
	return "unknown"
}

// Health is one instance's health.
type Health struct {
	Status        HealthStatus
	Message       string // human text, already redacted
	RemoteVersion string // e.g. qBittorrent "v5.1.2", Newznab server version
	Latency       time.Duration
	CheckedAt     time.Time
	Hold          *Hold // active providerhttp hold, if any (M2)
	Breaker       BreakerState
}

// The fixed health reasons the Registry reports in Health.Message (05-metadata
// M1.4, M1.5; 05-health §5H). Each is a token, never remote text.
const (
	ReasonIntegrationsDisabled   = "integrations_disabled"
	ReasonStoreUnavailable       = "integration_store_unavailable"
	ReasonAdapterNotInBinary     = "adapter not in this binary"
	ReasonSecretMissing          = "integration_secret_missing"
	ReasonSecretUnreadable       = "integration_secret_unreadable"
	ReasonOperatorSecretWithheld = "operator_secret_withheld"
	// ReasonStoredSecretWithheld: a stored secret is kept but not served,
	// because the effective destinations are not the ones it was written for
	// (an operator value, or a writer outside the Registry, moved them). The
	// fix is to re-enter the secret or to restore the destination (F7-01).
	ReasonStoredSecretWithheld = "integration_secret_withheld_destination_moved"
	ReasonInstanceDisabled     = "instance_disabled"
	ReasonMalformed            = "integration_row_malformed"
	ReasonInvalidSettings      = "integration_settings_invalid"
	ReasonBuildFailed          = "integration_build_failed"
)

// TestResult is Registry.Test's answer: never a response body or remote
// error text (a test must not be a read oracle, SEC-15).
type TestResult struct {
	OK     bool
	Class  Class
	Status int
}
