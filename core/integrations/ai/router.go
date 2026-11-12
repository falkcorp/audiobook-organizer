// file: core/integrations/ai/router.go
// version: 1.0.0
// guid: 1e5c8b73-0a29-4f64-9d3b-4c7a2e6f8d15
// last-edited: 2026-11-12

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Router is the per-App AI router (05-ai section 3): it owns the capability
// catalog (frozen before Run), the endpoints' health, in-flight slots and
// capability cache, and it routes every call over the registry's ai
// instances with HEAD's aidispatch algorithm. It holds no package-level state:
// two Routers share nothing, so two Apps in one process are isolated.
//
// It is safe for concurrent use. The endpoint snapshot is immutable per call;
// the catalog, the bench, the slots and the caps cache each sit behind their
// own mutex.
type Router struct {
	reg      *integrations.Registry
	disabled func() bool
	cfgFn    func() Config
	jobs     JobStore
	newBatch func() *storage.Batch
	stager   tasks.Stager
	now      func() time.Time
	log      *slog.Logger
	m        *instruments

	mu      sync.RWMutex
	catalog map[string]*catalogEntry
	frozen  bool

	bench *bench
	slots *slots

	capsMu sync.Mutex
	caps   map[integrations.Ref]capsEntry

	logMu     sync.Mutex
	lastNoCap map[string]time.Time

	pollMu   sync.Mutex
	openJobs map[JobState]int // the open states of the last poll's snapshot
}

// capsEntry is one endpoint's cached measured capabilities.
type capsEntry struct {
	caps    BackendCaps
	fetched time.Time
	ok      bool // the fetch that produced caps succeeded
}

// NewRouter builds a Router over opts. It fails on a missing Registry, a
// JobStore without the NewBatch that stages into its keyspace, or a metrics
// scope that refuses an instrument.
func NewRouter(opts RouterOptions) (*Router, error) {
	if opts.Registry == nil {
		return nil, errors.New("ai: NewRouter needs the integrations Registry")
	}
	if opts.Jobs != nil && opts.NewBatch == nil {
		return nil, errors.New("ai: NewRouter with a JobStore needs NewBatch, an indexed batch over the store's keyspace")
	}
	r := &Router{
		reg: opts.Registry, disabled: opts.Disabled, cfgFn: opts.Config,
		jobs: opts.Jobs, newBatch: opts.NewBatch, stager: opts.Stager,
		now: opts.Now, log: opts.Logger,
		catalog:   map[string]*catalogEntry{},
		slots:     newSlots(),
		caps:      map[integrations.Ref]capsEntry{},
		lastNoCap: map[string]time.Time{},
		openJobs:  map[JobState]int{},
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	r.bench = newBench(r.now)
	m, err := newInstruments(opts.Metrics, r)
	if err != nil {
		return nil, err
	}
	r.m = m
	return r, nil
}

// Close unregisters the router's gauge callback. Calls in flight finish.
func (r *Router) Close() { r.m.close() }

// config is the live core.ai section with its defaults applied.
func (r *Router) config() Config {
	var c Config
	if r.cfgFn != nil {
		c = r.cfgFn()
	}
	return c.withDefaults()
}

func (r *Router) isDisabled() bool { return r.disabled != nil && r.disabled() }

// oneLine writes a text's line breaks out as \r and \n, so a value an operator
// or a provider chose cannot end a log line and start a forged one
// (go/log-injection). Every refusal reason and every logged error text goes
// through it.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// ---- endpoint settings ----------------------------------------------------------

// decodeSettings reads the routing attributes of an instance's settings map.
// The adapter's own fields are ignored.
func decodeSettings(settings map[string]any) (EndpointSettings, error) {
	var set EndpointSettings
	if len(settings) == 0 {
		return set, nil
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return set, fmt.Errorf("ai: endpoint settings: %w", err)
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return set, fmt.Errorf("ai: endpoint settings: %w", err)
	}
	return set, nil
}

// ValidateEndpoint is the routing half of validating an ai instance's
// settings, for the kernel to call from the integrations write path: the
// concurrency is at least 1; a capability tick is never a wildcard; and a tick
// the catalog does not know is refused unless the stored row already carried it
// (05-ai section 4, step 2: such a tick is kept and ignored, but a PUT cannot
// add one). stored is the row being replaced, nil for a create. Every error
// wraps ErrInvalidEndpoint.
func (r *Router) ValidateEndpoint(settings, stored map[string]any) error {
	set, err := decodeSettings(settings)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEndpoint, err)
	}
	if set.Concurrency < 1 {
		return fmt.Errorf("%w: concurrency must be at least 1, got %d", ErrInvalidEndpoint, set.Concurrency)
	}
	var prev EndpointSettings
	if stored != nil {
		if prev, err = decodeSettings(stored); err != nil {
			prev = EndpointSettings{}
		}
	}
	for _, id := range set.Capabilities {
		switch {
		case strings.Contains(id, "*"):
			return fmt.Errorf("%w: capability tick %q is a wildcard; tick each capability explicitly", ErrInvalidEndpoint, oneLine(id))
		case r.HasCapability(id), slices.Contains(prev.Capabilities, id):
		default:
			return fmt.Errorf("%w: %w: %q", ErrInvalidEndpoint, ErrUnknownCapability, oneLine(id))
		}
	}
	return nil
}

// ---- capability cache -----------------------------------------------------------

// negativeCapsTTL is how long a failed caps fetch is remembered, so an endpoint
// that does not answer is not asked on every call.
func negativeCapsTTL() time.Duration { return 30 * time.Second }

// capsTimeout bounds one caps fetch.
func capsTimeout() time.Duration { return 5 * time.Second }

// capsFor returns ref's measured capabilities: the cached ones while they are
// fresh, else a new fetch through a lease. A failed fetch keeps the stale
// caps, if any, and is remembered briefly; ok is false when nothing is known.
func (r *Router) capsFor(ctx context.Context, ref integrations.Ref, ttl time.Duration) (BackendCaps, bool) {
	now := r.now()
	r.capsMu.Lock()
	e, have := r.caps[ref]
	r.capsMu.Unlock()
	if have {
		age := now.Sub(e.fetched)
		if (e.ok && age < ttl) || (!e.ok && age < negativeCapsTTL()) {
			return e.caps, e.ok
		}
	}
	fetched, err := r.fetchCaps(ctx, ref)
	r.capsMu.Lock()
	defer r.capsMu.Unlock()
	if err != nil {
		stale := r.caps[ref]
		stale.fetched = r.now()
		r.caps[ref] = stale // ok stays as it was: stale caps remain usable
		if !stale.ok {
			return BackendCaps{}, false
		}
		return stale.caps, true
	}
	r.caps[ref] = capsEntry{caps: fetched, fetched: r.now(), ok: true}
	return fetched, true
}

func (r *Router) fetchCaps(ctx context.Context, ref integrations.Ref) (BackendCaps, error) {
	b, release, err := integrations.Lease[Backend](ctx, r.reg, ref)
	if err != nil {
		return BackendCaps{}, err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, capsTimeout())
	defer cancel()
	return b.Caps(cctx)
}

// RefreshCaps fetches ref's capabilities now and replaces the cache (the
// health task and an operator's Test call it).
func (r *Router) RefreshCaps(ctx context.Context, ref integrations.Ref) error {
	c, err := r.fetchCaps(ctx, ref)
	if err != nil {
		return err
	}
	r.capsMu.Lock()
	r.caps[ref] = capsEntry{caps: c, fetched: r.now(), ok: true}
	r.capsMu.Unlock()
	return nil
}

// ---- selection ------------------------------------------------------------------

// endpoint is one ai instance as a call sees it: an immutable snapshot.
type endpoint struct {
	cfg      integrations.InstanceConfig
	set      EndpointSettings
	features []Feature
	labels   []string
	locality Locality // LocalityLocal or LocalityCloud
	model    string
}

func (e *endpoint) ref() integrations.Ref { return e.cfg.Ref }

// modelFor returns the model an endpoint uses for spec and whether it has one:
// the capability's override, else the endpoint's model of the capability's
// kind. A transcribe endpoint needs no model (a whisper server carries none).
func modelFor(set EndpointSettings, spec CapabilitySpec) (string, bool) {
	if m := set.CapabilityModels[spec.ID]; m != "" {
		return m, true
	}
	switch spec.Kind {
	case KindText:
		return set.TextModel, set.TextModel != ""
	case KindEmbed:
		return set.EmbedModel, set.EmbedModel != ""
	}
	return set.TranscribeModel, true
}

// mergeStrings is the union of declared and measured values, declared first.
func mergeStrings[T comparable](declared, measured []T) []T {
	out := slices.Clone(declared)
	for _, m := range measured {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// selectEndpoints applies selection steps 2 to 4 of section 4 to a registered
// spec and returns the candidates in the order a call would try them, plus a
// refusal for every endpoint left out. The refusals are one line each: a
// reason can carry an operator's model name, label or feature.
func (r *Router) selectEndpoints(ctx context.Context, spec CapabilitySpec, co callOptions, cfg Config) ([]*endpoint, []Refusal) {
	var out []*endpoint
	var refusals []Refusal
	disabled := r.isDisabled()
	for c := range r.reg.Instances(integrations.KindAI) {
		ref := c.Ref
		refuse := func(format string, args ...any) {
			refusals = append(refusals, Refusal{Endpoint: ref, Reason: oneLine(fmt.Sprintf(format, args...))})
		}
		if disabled {
			refuse("integrations disabled (core.integrations.disabled)")
			continue
		}
		if !c.Enabled {
			refuse("disabled")
			continue
		}
		set, err := decodeSettings(c.Settings)
		if err != nil {
			refuse("invalid endpoint settings: %v", err)
			continue
		}
		if set.Concurrency < 1 {
			refuse("invalid endpoint settings: concurrency must be at least 1")
			continue
		}
		if len(set.Capabilities) == 0 {
			refuse("no capabilities ticked (default-deny)")
			continue
		}
		if !slices.Contains(set.Capabilities, spec.ID) {
			if slices.ContainsFunc(set.Capabilities, func(s string) bool { return strings.Contains(s, "*") }) {
				refuse("capability not ticked (wildcard entries are not honoured)")
			} else {
				refuse("capability not ticked")
			}
			continue
		}
		model, ok := modelFor(set, spec)
		if !ok {
			refuse("no %s model configured", spec.Kind)
			continue
		}
		if co.pinnedModel != "" && model != co.pinnedModel {
			refuse("model %q is not the pinned model %q", model, co.pinnedModel)
			continue
		}
		// Only an endpoint that survived the cheap refusals is asked for its
		// measured capabilities: no request goes to a server that could not
		// serve the call anyway.
		measured, _ := r.capsFor(ctx, ref, cfg.CapsTTL)
		ep := &endpoint{
			cfg: c, set: set, model: model,
			features: mergeStrings(set.Features, measured.Features),
			labels:   mergeStrings(set.Labels, measured.Labels),
			locality: effectiveLocality(set, measured.Locality, c.Settings),
		}
		if (spec.Locality == LocalOnly || co.localOnly) && ep.locality != LocalityLocal {
			refuse("cloud endpoint excluded: this call is local-only")
			continue
		}
		if co.cloudOnly && ep.locality == LocalityLocal {
			refuse("local endpoint excluded: this call is cloud-only")
			continue
		}
		required := slices.Concat(spec.RequiredFeatures, co.requireFeatures)
		if !containsAll(ep.features, required) {
			refuse("missing required feature(s) %v (has %v)", required, ep.features)
			continue
		}
		labels := slices.Clone(co.labels)
		if set.RequireGPU && !slices.Contains(labels, "gpu") {
			labels = append(labels, "gpu")
		}
		if !containsAll(ep.labels, labels) {
			refuse("missing required label(s) %v (has %v)", labels, ep.labels)
			continue
		}
		if until := r.bench.benchedUntil(ref); !until.IsZero() {
			refuse("in failure cooldown until %s", until.Format(time.RFC3339))
			continue
		}
		out = append(out, ep)
	}
	// Depths are snapshotted once: reading them inside the comparator would let
	// the order change mid-sort as other goroutines acquire and release.
	depth := make(map[integrations.Ref]int, len(out))
	for _, ep := range out {
		depth[ep.ref()] = r.slots.depth(ep.ref())
	}
	slices.SortStableFunc(out, func(a, b *endpoint) int {
		aa, ab := slices.Contains(co.avoid, a.ref()), slices.Contains(co.avoid, b.ref())
		switch {
		case aa != ab && aa:
			return 1
		case aa != ab:
			return -1
		case a.cfg.Priority != b.cfg.Priority:
			return a.cfg.Priority - b.cfg.Priority
		}
		return depth[a.ref()] - depth[b.ref()]
	})
	return out, refusals
}

// EndpointStatus is one candidate endpoint as Candidates reports it.
type EndpointStatus struct {
	Ref           integrations.Ref
	Priority      int
	InFlight      int
	Concurrency   int
	CooldownUntil time.Time
	Locality      Locality // effective: LocalityLocal or LocalityCloud
}

// Candidates returns the endpoints that may serve c right now, in the order a
// call would try them, plus a refusal reason for every endpoint left out.
func (r *Router) Candidates(c Capability, opts ...CallOption) ([]EndpointStatus, []Refusal, error) {
	e, err := r.resolve(c)
	if err != nil {
		return nil, nil, err
	}
	eps, refusals := r.selectEndpoints(context.Background(), e.spec, newCallOptions(opts), r.config())
	out := make([]EndpointStatus, 0, len(eps))
	for _, ep := range eps {
		out = append(out, EndpointStatus{
			Ref: ep.ref(), Priority: ep.cfg.Priority, InFlight: r.slots.depth(ep.ref()),
			Concurrency: ep.set.Concurrency, CooldownUntil: r.bench.benchedUntil(ep.ref()), Locality: ep.locality,
		})
	}
	return out, refusals, nil
}

// Capacity is the number of requests for c that can be in flight at once across
// its current candidates, after the kind's pool-wide cap. Callers size their
// pools to min(configured, Capacity(c)); 0 means the work would fail closed.
func (r *Router) Capacity(c Capability, opts ...CallOption) int {
	e, err := r.resolve(c)
	if err != nil {
		return 0
	}
	cfg := r.config()
	eps, _ := r.selectEndpoints(context.Background(), e.spec, newCallOptions(opts), cfg)
	n := 0
	for _, ep := range eps {
		n += ep.set.Concurrency
	}
	if lim := cfg.KindLimits[e.spec.Kind]; lim >= 1 {
		n = min(n, lim)
	}
	return n
}

// logNoCapable writes at most one line per capability per
// core.ai.log_no_capable_every.
func (r *Router) logNoCapable(err *NoCapableEndpointError, every time.Duration) {
	r.logMu.Lock()
	last, seen := r.lastNoCap[err.Capability]
	now := r.now()
	emit := !seen || now.Sub(last) >= every
	if emit {
		r.lastNoCap[err.Capability] = now
	}
	r.logMu.Unlock()
	if emit {
		r.log.Warn("ai: no capable endpoint; failing closed", "capability", oneLine(err.Capability), "refusals", oneLine(err.Error()))
	}
}

// ---- the call loop ----------------------------------------------------------------

// Target is what a Call hands to the caller's function: the endpoint chosen
// for this attempt, its leased backend and the model to ask for.
type Target struct {
	Endpoint integrations.Ref
	Backend  Backend
	Model    string
	Features []Feature
	Labels   []string
}

// Call is the router's one routing loop and the escape hatch for app-defined
// protocols: the router selects and gates, fn talks to the backend it is
// given and reports what the call consumed.
//
//   - No capable endpoint: *NoCapableEndpointError (errors.Is
//     ErrNoCapableEndpoint). The caller must mark the item failed, never
//     skipped or complete.
//   - A class that does not fail over (BadOutput, Invalid, NotFound, Unknown)
//     is returned as is, with fn's result.
//   - Deadline: fail over once (core.ai.deadline_failovers); the next one ends
//     the call.
//   - Every candidate failed over: *ExhaustedError.
//
// Embedding work needs WithPinnedModel (ErrEmbedModelNotPinned) and fails over
// only to endpoints with the SAME model as the first one tried.
func Call[T any](ctx context.Context, r *Router, c Capability,
	fn func(ctx context.Context, t Target) (T, Usage, error), opts ...CallOption) (T, error) {
	var zero T
	e, err := r.resolve(c)
	if err != nil {
		return zero, err
	}
	spec := e.spec
	co := newCallOptions(opts)
	if spec.Kind == KindEmbed && co.pinnedModel == "" {
		return zero, fmt.Errorf("%w: capability %s", ErrEmbedModelNotPinned, spec.ID)
	}
	cfg := r.config()
	cands, refusals := r.selectEndpoints(ctx, spec, co, cfg)
	if len(cands) == 0 {
		r.m.refused(ctx, spec.ID)
		err := &NoCapableEndpointError{Capability: spec.ID, Refusals: refusals}
		r.logNoCapable(err, cfg.LogNoCapableEvery)
		return zero, err
	}
	timeout := co.attemptTimeout
	if timeout <= 0 {
		timeout = spec.AttemptTimeout
	}
	kindLimit := cfg.KindLimits[spec.Kind]

	var attempts []AttemptError
	deadlines := 0
	embedModel := ""
	remaining := cands
	for len(remaining) > 0 {
		// The embed and cooldown rules apply only AFTER the first attempt: the
		// first candidate set was filtered moments ago by selectEndpoints.
		if len(attempts) > 0 {
			kept := remaining[:0:0]
			for _, ep := range remaining {
				switch {
				case spec.Kind == KindEmbed && ep.model != embedModel:
					refusals = append(refusals, Refusal{Endpoint: ep.ref(),
						Reason: oneLine(fmt.Sprintf("failover skipped: embed model %q differs from %q", ep.model, embedModel))})
				case !r.bench.benchedUntil(ep.ref()).IsZero():
					// A sibling goroutine may have benched it since selection.
					refusals = append(refusals, Refusal{Endpoint: ep.ref(), Reason: "failover skipped: benched after selection"})
				default:
					kept = append(kept, ep)
				}
			}
			remaining = kept
			if len(remaining) == 0 {
				break
			}
		}

		// Free-slot assignment: take the most preferred candidate that has a
		// slot free RIGHT NOW, so a saturated preferred endpoint spills to an
		// idle peer instead of queueing. Only when every candidate is full does
		// the call wait, on the most preferred one, which keeps priority
		// meaningful under sustained backlog.
		pick := -1
		var release func()
		waitStart := r.now()
		for i, ep := range remaining {
			if rel, ok := r.slots.tryAcquire(ep.ref(), ep.set.Concurrency, spec.Kind, kindLimit); ok {
				pick, release = i, rel
				break
			}
		}
		if pick < 0 {
			pick = 0
			rel, err := r.slots.acquire(ctx, remaining[0].ref(), remaining[0].set.Concurrency, spec.Kind, kindLimit)
			r.m.waited(ctx, spec.Kind, r.now().Sub(waitStart))
			if err != nil {
				// acquire fails only when the CALLER's ctx is done, so trying the
				// next endpoint would fail the same way: stop.
				return zero, err
			}
			release = rel
		} else {
			r.m.waited(ctx, spec.Kind, r.now().Sub(waitStart))
		}
		ep := remaining[pick]
		remaining = append(remaining[:pick:pick], remaining[pick+1:]...)
		if spec.Kind == KindEmbed && embedModel == "" {
			embedModel = ep.model
		}

		start := r.now()
		var usage Usage
		var res T
		var attemptErr error
		deadline := false
		// One closure per attempt so the slot, the lease and the attempt context
		// are released by defer even if fn panics: sequential releases after fn
		// leaked the slot on a panic, and a concurrency-1 endpoint then refused
		// every later call for the life of the process.
		func() {
			defer release()
			attemptCtx, cancel := ctx, context.CancelFunc(func() {})
			if timeout > 0 {
				attemptCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			backend, releaseLease, lerr := integrations.Lease[Backend](ctx, r.reg, ep.ref())
			if lerr != nil {
				attemptErr = lerr
				return
			}
			defer releaseLease()
			res, usage, attemptErr = fn(attemptCtx, Target{
				Endpoint: ep.ref(), Backend: backend, Model: ep.model,
				Features: slices.Clone(ep.features), Labels: slices.Clone(ep.labels),
			})
			// The attempt's own deadline passed while the caller's context is
			// live: the model is slow, whatever shape the SDK gave the error.
			deadline = attemptErr != nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		}()

		out := classify(ctx, attemptErr, deadline)
		class := classOK
		if attemptErr != nil {
			class = out.class.String()
		}
		r.m.attempt(ctx, spec.ID, spec.Kind, ep.ref(), r.modelLabel(ep.model), class, r.now().Sub(start))
		if attemptErr != nil {
			r.log.Warn("ai: routed request failed", "capability", oneLine(spec.ID), "endpoint", oneLine(ep.ref().String()),
				"model", oneLine(ep.model), "class", out.class.String(), "elapsed", r.now().Sub(start).Round(time.Millisecond),
				"err", oneLine(attemptErr.Error()))
		}
		switch {
		case attemptErr == nil:
			r.bench.markSuccess(ep.ref())
			r.m.usage(ctx, spec.ID, ep.ref(), r.modelLabel(ep.model), modeSync, usage)
			return res, nil
		case out.class == integrations.ClassCanceled:
			return zero, attemptErr
		case !failsOver(out.class):
			return res, attemptErr
		}
		switch {
		case out.class == integrations.ClassQuota:
			r.bench.markQuota(ep.ref(), cfg.QuotaCooldown)
		case benches(out.class):
			r.bench.markFailure(ep.ref(), cfg.CooldownBase, cfg.CooldownMax)
		}
		attempts = append(attempts, AttemptError{Endpoint: ep.ref(), Class: out.class, Err: attemptErr})
		if out.deadline {
			deadlines++
			if deadlines > cfg.DeadlineFailovers {
				break
			}
		}
		r.m.failedOver(ctx, spec.ID, ep.ref(), out.class)
	}
	if len(attempts) == 0 {
		// Unreachable today: the first candidate is always attempted. Kept so a
		// future edit to the skip rules fails closed instead of returning a zero
		// value with a nil error.
		return zero, &NoCapableEndpointError{Capability: spec.ID, Refusals: refusals}
	}
	return zero, &ExhaustedError{Capability: spec.ID, Attempts: attempts}
}

// ---- the three kinds ---------------------------------------------------------------

// unsupported is the attempt error of an endpoint whose adapter does not
// implement the kind's interface: it fails over.
func unsupported(t Target, op string) error {
	return &integrations.Error{Ref: t.Endpoint, Op: op, Class: integrations.ClassUnsupported,
		Err: fmt.Errorf("ai: the adapter of %s does not implement %s", t.Endpoint, op)}
}

// attemptTrace remembers which endpoint and model the last attempt of a call
// used and how many attempts ran, for Served.
type attemptTrace struct {
	mu       sync.Mutex
	endpoint integrations.Ref
	model    string
	attempts int
}

func (a *attemptTrace) note(t Target) {
	a.mu.Lock()
	a.endpoint, a.model = t.Endpoint, t.Model
	a.attempts++
	a.mu.Unlock()
}

func (a *attemptTrace) served() Served {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Served{Endpoint: a.endpoint, Model: a.model, Attempts: a.attempts}
}

// Generate runs one text generation call.
func (r *Router) Generate(ctx context.Context, c Capability, req TextRequest, opts ...CallOption) (TextResponse, error) {
	var tr attemptTrace
	resp, err := Call(ctx, r, c, func(ctx context.Context, t Target) (TextResponse, Usage, error) {
		tr.note(t)
		g, ok := t.Backend.(TextGenerator)
		if !ok {
			return TextResponse{}, Usage{}, unsupported(t, "generate")
		}
		out, err := g.Generate(ctx, t.Model, req)
		if err != nil {
			return TextResponse{}, Usage{}, err
		}
		return out, out.Usage, nil
	}, opts...)
	if err != nil {
		return TextResponse{}, err
	}
	resp.Served = tr.served()
	return resp, nil
}

// Embed runs one embedding call; it needs WithPinnedModel.
func (r *Router) Embed(ctx context.Context, c Capability, req EmbedRequest, opts ...CallOption) (EmbedResponse, error) {
	var tr attemptTrace
	resp, err := Call(ctx, r, c, func(ctx context.Context, t Target) (EmbedResponse, Usage, error) {
		tr.note(t)
		e, ok := t.Backend.(Embedder)
		if !ok {
			return EmbedResponse{}, Usage{}, unsupported(t, "embed")
		}
		out, err := e.Embed(ctx, t.Model, req)
		if err != nil {
			return EmbedResponse{}, Usage{}, err
		}
		return out, out.Usage, nil
	}, opts...)
	if err != nil {
		return EmbedResponse{}, err
	}
	resp.Served = tr.served()
	return resp, nil
}

// Transcribe runs one transcription call.
func (r *Router) Transcribe(ctx context.Context, c Capability, req TranscribeRequest, opts ...CallOption) (TranscribeResponse, error) {
	var tr attemptTrace
	resp, err := Call(ctx, r, c, func(ctx context.Context, t Target) (TranscribeResponse, Usage, error) {
		tr.note(t)
		x, ok := t.Backend.(Transcriber)
		if !ok {
			return TranscribeResponse{}, Usage{}, unsupported(t, "transcribe")
		}
		out, err := x.Transcribe(ctx, t.Model, req)
		if err != nil {
			return TranscribeResponse{}, Usage{}, err
		}
		u := out.Usage
		if u.AudioSeconds == 0 {
			u.AudioSeconds = out.Duration.Seconds()
		}
		return out, u, nil
	}, opts...)
	if err != nil {
		return TranscribeResponse{}, err
	}
	resp.Served = tr.served()
	return resp, nil
}
