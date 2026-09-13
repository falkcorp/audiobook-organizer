// file: internal/aidispatch/dispatch.go
// version: 1.1.0
// guid: 7e784d44-f9f1-4637-919b-c5cf3aa6ac53
// last-edited: 2026-09-13

package aidispatch

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Protocol is how the dispatcher talks to an endpoint.
type Protocol string

const (
	ProtocolOpenAICompat  Protocol = "openai_compat"
	ProtocolWhisperServer Protocol = "whisper_server"
	ProtocolLocalProcess  Protocol = "local_process"
)

// protocolsFor lists the protocols that can serve a kind of work.
var protocolsFor = map[Kind][]Protocol{
	KindChat:    {ProtocolOpenAICompat},
	KindEmbed:   {ProtocolOpenAICompat},
	KindWhisper: {ProtocolWhisperServer, ProtocolLocalProcess},
}

// Endpoint is one AI server as the dispatcher sees it (PLAN section 3). It is
// an input type local to this package until PR 2 adds the config struct that
// produces it.
type Endpoint struct {
	ID       string
	Label    string
	Protocol Protocol
	URL      string

	ChatModel  string
	EmbedModel string
	// CapabilityModels overrides the kind's model for one capability ID.
	CapabilityModels map[string]string

	// Priority orders candidates: lower is preferred.
	Priority int
	// Concurrency is this endpoint's in-flight cap. 0 means 1 (see
	// EffectiveConcurrency); it is never "unlimited".
	Concurrency int
	Enabled     bool

	// Capabilities are the work checkboxes, stored as an explicit list of
	// capability IDs. Default-deny: empty means nothing, and wildcard entries
	// are never honoured.
	Capabilities []string
	// Labels are declared (and, from PR 3, measured) properties such as
	// "gpu" or "local". whisper_requires is matched against these.
	Labels []string
	// Features are endpoint features: "batch_api", "vision", and the measured
	// "shared_fs:<root_id>" (section 3b), which is carried as data only here.
	Features []string

	RequireGPU bool
	// AuthRef names a secret; the secret itself is never stored on the row.
	AuthRef string
	// HostRoots optionally overrides the host's AO_ROOT per root_id (3b).
	HostRoots map[string]string
}

// Target is what a Call hands to the caller's function: the endpoint chosen
// for this attempt and the model to ask for.
type Target struct {
	Endpoint   Endpoint
	Capability Capability
	Model      string
}

// Dispatcher selects endpoints for capabilities. It is safe for concurrent use:
// its endpoint list is immutable after New, and all mutable state (slots,
// health, log throttling) sits behind a mutex.
type Dispatcher struct {
	endpoints      []Endpoint
	slots          *Slots
	health         *Health
	requiredLabels map[string][]string
	totals         map[Kind]TotalCap
	attemptTimeout map[string]time.Duration

	logMu      sync.Mutex
	lastNoCape map[string]time.Time
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithSlots uses s instead of the process-wide slot registry.
func WithSlots(s *Slots) Option { return func(d *Dispatcher) { d.slots = s } }

// WithHealth uses h instead of the process-wide health tracker.
func WithHealth(h *Health) Option { return func(d *Dispatcher) { d.health = h } }

// WithRequiredLabels requires every endpoint serving c to carry ALL of labels
// (whisper_requires). RequireGPU on an endpoint adds "gpu" on top.
func WithRequiredLabels(c Capability, labels ...string) Option {
	return func(d *Dispatcher) { d.requiredLabels[c.id] = slices.Clone(labels) }
}

// WithTotalCap caps simultaneous requests across all endpoints for one kind.
// A Limit < 1 means unlimited.
func WithTotalCap(k Kind, total TotalCap) Option {
	return func(d *Dispatcher) { d.totals[k] = total }
}

// WithAttemptTimeout sets a per-attempt deadline for c. The clock starts only
// AFTER the slot is acquired, so a timeout means "the model is slow", never
// "I waited in line".
func WithAttemptTimeout(c Capability, timeout time.Duration) Option {
	return func(d *Dispatcher) { d.attemptTimeout[c.id] = timeout }
}

// New returns a dispatcher over a copy of endpoints.
func New(endpoints []Endpoint, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		endpoints:      slices.Clone(endpoints),
		slots:          defaultSlots,
		health:         defaultHealth,
		requiredLabels: map[string][]string{},
		totals:         map[Kind]TotalCap{},
		attemptTimeout: map[string]time.Duration{},
		lastNoCape:     map[string]time.Time{},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// containsAll reports whether have contains EVERY element of want. An empty
// want is satisfied by anything. Features and labels are both contains-all:
// an endpoint with one of two required properties does not qualify.
func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// modelFor returns the model an endpoint would use for spec, and whether the
// endpoint has one. Whisper endpoints carry no model field.
func modelFor(ep Endpoint, spec Spec) (string, bool) {
	if m := ep.CapabilityModels[spec.Capability.id]; m != "" {
		return m, true
	}
	switch spec.Kind {
	case KindChat:
		return ep.ChatModel, ep.ChatModel != ""
	case KindEmbed:
		return ep.EmbedModel, ep.EmbedModel != ""
	}
	return "", true
}

// refuse returns why ep cannot serve spec, or "" if it can (ignoring
// cooldown, which the caller checks separately so Capacity can differ).
func (d *Dispatcher) refuse(ep Endpoint, spec Spec) string {
	id := spec.Capability.id
	if !ep.Enabled {
		return "disabled"
	}
	if len(ep.Capabilities) == 0 {
		return "no capabilities ticked (default-deny)"
	}
	if !slices.Contains(ep.Capabilities, id) {
		if slices.ContainsFunc(ep.Capabilities, func(s string) bool { return strings.Contains(s, "*") }) {
			return "capability not ticked (wildcard entries are not honoured)"
		}
		return "capability not ticked"
	}
	if !slices.Contains(protocolsFor[spec.Kind], ep.Protocol) {
		return fmt.Sprintf("protocol %q cannot serve %s work", ep.Protocol, spec.Kind)
	}
	if !containsAll(ep.Features, spec.RequiredFeatures) {
		return fmt.Sprintf("missing required feature(s) %v (has %v)", spec.RequiredFeatures, ep.Features)
	}
	req := d.requiredLabels[id]
	if ep.RequireGPU && !slices.Contains(req, "gpu") {
		req = append(slices.Clone(req), "gpu")
	}
	if !containsAll(ep.Labels, req) {
		return fmt.Sprintf("missing required label(s) %v (has %v)", req, ep.Labels)
	}
	if _, ok := modelFor(ep, spec); !ok {
		return fmt.Sprintf("no %s model configured", spec.Kind)
	}
	return ""
}

// candidates applies section 4 selection steps 2-4 to a registered spec.
func (d *Dispatcher) candidates(spec Spec) ([]Endpoint, []Refusal) {
	var out []Endpoint
	var refusals []Refusal
	for _, ep := range d.endpoints {
		if r := d.refuse(ep, spec); r != "" {
			refusals = append(refusals, Refusal{EndpointID: ep.ID, Reason: r})
			continue
		}
		if d.health.InCooldown(ep.ID) {
			refusals = append(refusals, Refusal{EndpointID: ep.ID,
				Reason: "in failure cooldown until " + d.health.CooldownUntil(ep.ID).Format(time.RFC3339)})
			continue
		}
		out = append(out, ep)
	}
	// Snapshot depths once: reading them inside the comparator would let the
	// order change mid-sort as other goroutines acquire and release.
	depth := make(map[string]int, len(out))
	for _, ep := range out {
		depth[ep.ID] = d.slots.Depth(ep.ID)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return depth[out[i].ID] < depth[out[j].ID]
	})
	return out, refusals
}

// Candidates returns the endpoints that may serve c right now, in the order a
// Call would try them, plus a refusal reason for every endpoint left out.
func (d *Dispatcher) Candidates(c Capability) ([]Endpoint, []Refusal, error) {
	spec, ok := Lookup(c)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownCapability, c.id)
	}
	eps, refusals := d.candidates(spec)
	return eps, refusals, nil
}

// Capacity is the number of requests for c that can be in flight at once
// across its current candidates. Callers size their own pools to
// min(configured, Capacity(c)); 0 means the work would fail closed.
func (d *Dispatcher) Capacity(c Capability) int {
	eps, _, err := d.Candidates(c)
	if err != nil {
		return 0
	}
	n := 0
	for _, ep := range eps {
		n += EffectiveConcurrency(ep.Concurrency)
	}
	if spec, ok := Lookup(c); ok {
		if t := d.totals[spec.Kind]; t.Limit > 0 {
			n = min(n, t.Limit)
		}
	}
	return n
}

// logNoCapable writes at most one line per capability per minute.
func (d *Dispatcher) logNoCapable(err *NoCapableEndpointError) {
	d.logMu.Lock()
	last := d.lastNoCape[err.Capability]
	now := time.Now()
	emit := now.Sub(last) >= time.Minute
	if emit {
		d.lastNoCape[err.Capability] = now
	}
	d.logMu.Unlock()
	if emit {
		slog.Warn("aidispatch: no capable endpoint; failing closed", "capability", err.Capability, "err", err.Error())
	}
}

// Call runs fn against the best capable endpoint for c, failing over per the
// section 4 table. It is a package-level function because Go methods cannot
// have type parameters.
//
//   - No capable endpoint: *NoCapableEndpointError (errors.Is ErrNoCapableEndpoint).
//     The caller must mark the item failed, never skipped or complete.
//   - A quality failure (ClassNoFailover) is returned as-is, with fn's result.
//   - Deadline: fail over once; a second deadline ends the call.
//   - Every candidate failed over: *ExhaustedError.
//
// Embedding work fails over only to endpoints with the SAME embedding model as
// the first candidate tried.
func Call[T any](ctx context.Context, d *Dispatcher, c Capability, fn func(context.Context, Target) (T, error)) (T, error) {
	var zero T
	ensureMetrics()
	spec, ok := Lookup(c)
	if !ok {
		return zero, fmt.Errorf("%w: %q", ErrUnknownCapability, c.id)
	}
	cands, refusals := d.candidates(spec)
	if len(cands) == 0 {
		noCapableTotal.WithLabelValues(c.id).Inc()
		err := &NoCapableEndpointError{Capability: c.id, Refusals: refusals}
		d.logNoCapable(err)
		return zero, err
	}

	var attempts []AttemptError
	deadlines := 0
	embedModel := ""
	for i, ep := range cands {
		model, _ := modelFor(ep, spec)
		if spec.Kind == KindEmbed {
			if embedModel == "" {
				embedModel = model
			} else if model != embedModel {
				refusals = append(refusals, Refusal{EndpointID: ep.ID,
					Reason: fmt.Sprintf("failover skipped: embed model %q differs from %q", model, embedModel)})
				continue
			}
		}
		// A sibling goroutine may have benched this endpoint since selection.
		if i > 0 && d.health.InCooldown(ep.ID) {
			refusals = append(refusals, Refusal{EndpointID: ep.ID,
				Reason: "failover skipped: benched after selection"})
			continue
		}

		waitStart := time.Now()
		release, err := d.slots.Acquire(ctx, ep.ID, ep.Concurrency, d.totals[spec.Kind])
		slotWaitSeconds.WithLabelValues(ep.ID).Observe(time.Since(waitStart).Seconds())
		if err != nil {
			// Acquire has no timeout of its own: it fails only when the
			// CALLER's ctx is done, so trying the next endpoint would fail the
			// same way. Stopping here is ClassStop, not a skip. If Acquire ever
			// gains its own bound, this must become a skip plus a refusal.
			return zero, err
		}

		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if t := d.attemptTimeout[c.id]; t > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, t)
		}
		inflightGauge.WithLabelValues(ep.ID).Inc()
		res, err := fn(attemptCtx, Target{Endpoint: ep, Capability: c, Model: model})
		inflightGauge.WithLabelValues(ep.ID).Dec()
		cancel()
		release()

		class := Classify(ctx, err)
		requestsTotal.WithLabelValues(c.id, ep.ID, class.String()).Inc()
		switch class {
		case ClassOK:
			d.health.MarkSuccess(ep.ID)
			return res, nil
		case ClassStop:
			return zero, err
		case ClassNoFailover:
			return res, err
		case ClassQuotaExhausted:
			d.health.MarkQuotaExhausted(ep.ID)
		case ClassDeadline:
			d.health.MarkFailure(ep.ID)
			deadlines++
		default:
			d.health.MarkFailure(ep.ID)
		}
		attempts = append(attempts, AttemptError{EndpointID: ep.ID, Class: class, Err: err})
		if class == ClassDeadline && deadlines > 1 {
			break
		}
		failoverTotal.WithLabelValues(c.id, ep.ID, class.String()).Inc()
	}
	if len(attempts) == 0 {
		// Unreachable today: the first candidate is always attempted (the
		// model and cooldown skips apply only after it). Kept so a future edit
		// to the skip rules fails closed instead of returning a zero value
		// with a nil error.
		return zero, &NoCapableEndpointError{Capability: c.id, Refusals: refusals}
	}
	return zero, &ExhaustedError{Capability: c.id, Attempts: attempts}
}
