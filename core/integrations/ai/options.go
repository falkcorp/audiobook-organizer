// file: core/integrations/ai/options.go
// version: 1.0.0
// guid: 7a2c5e18-6f3d-4d90-8b14-c0e9f1a3d527
// last-edited: 2026-11-12

package ai

import (
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// ---- call options ------------------------------------------------------------

// CallOption narrows or tunes one call.
type CallOption func(*callOptions)

type callOptions struct {
	localOnly      bool
	cloudOnly      bool
	labels         []string
	attemptTimeout time.Duration
	pinnedModel    string
	avoid          []integrations.Ref
	// requireFeatures are features the operation itself adds to the
	// capability's own (SubmitBatch asks for FeatureBatch).
	requireFeatures []Feature
}

func newCallOptions(opts []CallOption) callOptions {
	var o callOptions
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return o
}

// WithLocalOnly refuses every endpoint whose effective locality is not local:
// a call may narrow the capability's floor, never widen it.
func WithLocalOnly() CallOption { return func(o *callOptions) { o.localOnly = true } }

// WithCloudOnly refuses every endpoint whose effective locality is local: the
// second half of a local-first call, so a fallback pass does not try the same
// local endpoints again.
func WithCloudOnly() CallOption { return func(o *callOptions) { o.cloudOnly = true } }

// WithRequiredLabels requires every endpoint serving the call to carry ALL of
// labels (an endpoint with RequireGPU needs "gpu" on top).
func WithRequiredLabels(labels ...string) CallOption {
	return func(o *callOptions) { o.labels = append(o.labels, labels...) }
}

// WithAttemptTimeout sets the per-attempt deadline of this call. The clock
// starts only AFTER the slot is acquired, so a timeout means "the model is
// slow", never "I waited in line".
func WithAttemptTimeout(d time.Duration) CallOption {
	return func(o *callOptions) { o.attemptTimeout = d }
}

// WithPinnedModel makes the call servable ONLY by endpoints whose effective
// model for it equals model. It is mandatory for KindEmbed
// (ErrEmbedModelNotPinned): a caller whose stored vectors are keyed by one
// model must pin it, or a benched preferred endpoint would silently move it
// into another vector space.
func WithPinnedModel(model string) CallOption { return func(o *callOptions) { o.pinnedModel = model } }

// WithAvoidEndpoints is a preference, not a refusal: the listed endpoints are
// tried last (transcribe AvoidWorkers).
func WithAvoidEndpoints(refs ...integrations.Ref) CallOption {
	return func(o *callOptions) { o.avoid = append(o.avoid, refs...) }
}

// ---- router options ------------------------------------------------------------

// Config carries the core.ai.* values of 05-ai section 7 that the router
// reads. A zero field takes the key's default; the kernel passes the live
// values through RouterOptions.Config, so every hot key is re-read per call.
// The keys are not declared by this package (the kernel owns core's
// sections); F7-09 wires them.
type Config struct {
	CooldownBase  time.Duration // core.ai.cooldown_base (30s)
	CooldownMax   time.Duration // core.ai.cooldown_max (5m)
	QuotaCooldown time.Duration // core.ai.quota_cooldown (30m)
	// DeadlineFailovers is core.ai.deadline_failovers (default 1): how many
	// per-attempt deadlines fail over; the next one ends the call. Zero takes
	// the default; a negative value is none.
	DeadlineFailovers int
	// KindLimits is core.ai.kind_limits.<kind>: the pool-wide in-flight cap of
	// a kind, 0 or absent = none. It restores the production setter HEAD lacks
	// for whisper_max_in_flight.
	KindLimits map[Kind]int
	// RepairAttempts is core.ai.structured.repair_attempts (default 1): the
	// repair turns a structured answer gets from the SAME endpoint. Zero takes
	// the default; a negative value is none.
	RepairAttempts        int
	CapsTTL               time.Duration // core.ai.caps_ttl (1h)
	BatchParallelism      int           // core.ai.batch_parallelism (2), at least 1
	BatchMaxApplyAttempts int           // core.ai.batch_max_apply_attempts (5)
	LogNoCapableEvery     time.Duration // core.ai.log_no_capable_every (1m)
}

// The defaults of section 7.
const (
	defaultCooldownBase      = 30 * time.Second
	defaultCooldownMax       = 5 * time.Minute
	defaultQuotaCooldown     = 30 * time.Minute
	defaultDeadlineFailovers = 1
	defaultRepairAttempts    = 1
	defaultCapsTTL           = time.Hour
	defaultBatchParallelism  = 2
	defaultBatchMaxApply     = 5
	defaultLogNoCapableEvery = time.Minute
)

func (c Config) withDefaults() Config {
	if c.CooldownBase <= 0 {
		c.CooldownBase = defaultCooldownBase
	}
	if c.CooldownMax <= 0 {
		c.CooldownMax = defaultCooldownMax
	}
	if c.QuotaCooldown <= 0 {
		c.QuotaCooldown = defaultQuotaCooldown
	}
	switch {
	case c.DeadlineFailovers == 0:
		c.DeadlineFailovers = defaultDeadlineFailovers
	case c.DeadlineFailovers < 0:
		c.DeadlineFailovers = 0
	}
	switch {
	case c.RepairAttempts == 0:
		c.RepairAttempts = defaultRepairAttempts
	case c.RepairAttempts < 0:
		c.RepairAttempts = 0
	}
	if c.CapsTTL <= 0 {
		c.CapsTTL = defaultCapsTTL
	}
	if c.BatchParallelism < 1 {
		c.BatchParallelism = defaultBatchParallelism
	}
	if c.BatchMaxApplyAttempts < 1 {
		c.BatchMaxApplyAttempts = defaultBatchMaxApply
	}
	if c.LogNoCapableEvery <= 0 {
		c.LogNoCapableEvery = defaultLogNoCapableEvery
	}
	if c.KindLimits != nil {
		kl := make(map[Kind]int, len(c.KindLimits))
		maps.Copy(kl, c.KindLimits)
		c.KindLimits = kl
	}
	return c
}

// RouterOptions configures NewRouter (a name coined by F7-03: 05-ai section 3
// shows Router as "per-App" and leaves its construction to the kernel, which
// wires module.Deps.AI at F7-09).
type RouterOptions struct {
	// Registry is the integration registry whose ai instances the router
	// routes over. Required.
	Registry *integrations.Registry
	// Disabled reports core.integrations.disabled, read per call: while it is
	// true every endpoint is refused, SubmitBatch stores nothing and the poll
	// changes no job (a restored copy must never spend a production
	// provider's budget). nil = never disabled.
	Disabled func() bool
	// Config reads the live core.ai section; nil = every default.
	Config func() Config
	// Jobs is the batch job store (NativeJobStore). nil: the batch operations
	// fail with ErrNoJobStore.
	Jobs JobStore
	// NewBatch opens an indexed batch over the store's own keyspace, which
	// Jobs stages into (ks.NewIndexedBatch for NativeJobStore). Required when
	// Jobs is set.
	NewBatch func() *storage.Batch
	// Stager is the kernel's stage-only starter for module runs (the poll
	// stages each ready job's apply run through it; core services never hold
	// the Executor). nil: the poll cannot stage an apply run and fails closed.
	Stager tasks.Stager
	// Metrics is the core "ai" scope, created with the labels capability,
	// instance, model, class, kind, direction, mode, state and result; nil
	// records nothing.
	Metrics *metrics.Scope
	// Now is the clock (default time.Now); the router's cooldowns and job
	// timestamps read it.
	Now func() time.Time
	// Logger receives the router's diagnostics (default: discarded).
	Logger *slog.Logger
}

// kindSpecDefaults are the default attempt timeouts per kind (CapabilitySpec).
func defaultAttemptTimeout(k Kind) time.Duration {
	switch k {
	case KindEmbed:
		return 60 * time.Second
	case KindTranscribe:
		return 30 * time.Minute
	}
	return 120 * time.Second
}

// containsAll reports whether have contains EVERY element of want. An empty
// want is satisfied by anything.
func containsAll[T comparable](have, want []T) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
