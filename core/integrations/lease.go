// file: core/integrations/lease.go
// version: 1.3.1
// guid: 8db314c4-39fe-4242-974f-f95bb76ffdd4
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/secrets"
)

// Leases, builds, swaps and health (M1.4 steps 3 to 6, M1.6).

// acquire takes a lease on s unless it is retired.
func (s *slot) acquire() bool {
	for {
		v := s.state.Load()
		if v&retiredBit != 0 {
			return false
		}
		if s.state.CompareAndSwap(v, v+1) {
			return true
		}
	}
}

// release gives back one lease; the last lease of a retired slot signals
// its drain.
func (s *slot) release() {
	if s.state.Add(-1) == retiredBit {
		s.drainOnce.Do(func() { close(s.drained) })
	}
}

// retire marks s retired and starts its closer (wg.Go, M1.6): it waits for
// the leases to drain or core.integrations.drain_timeout, then Closes the
// built instance. A slot is retired once.
func (r *Registry) retire(s *slot) {
	old := s.state.Or(retiredBit)
	if old&retiredBit != 0 {
		return
	}
	if old == 0 {
		s.drainOnce.Do(func() { close(s.drained) })
	}
	timeout := r.settings().DrainTimeout
	r.closers.Go(func() {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-s.drained:
		case <-t.C:
			r.log.Warn("integrations: an instance was closed with leases still held (core.integrations.drain_timeout)", "instance", s.cfg.Ref.String())
		}
		s.buildMu.Lock()
		inst := s.inst
		s.inst = nil
		// A lease still held past the timeout builds nothing from now on:
		// build answers errSwappedOut, so an instance no closer will ever
		// Close is never made (a slot force-closed before its first build).
		s.built = true
		s.buildMu.Unlock()
		if inst != nil {
			if err := inst.Close(); err != nil {
				r.log.Warn("integrations: closing a swapped-out instance failed", "instance", s.cfg.Ref.String(), "error", err)
			}
		}
	})
}

// current returns ref's slot with a lease held, retrying across a swap. It
// is nil when ref has no slot or the Registry is closed.
func (r *Registry) current(ref Ref) (*slot, *snapshot) {
	for {
		snap := r.snap.Load()
		s := snap.slots[ref]
		if s == nil || r.closed.Load() {
			return nil, snap
		}
		if s.acquire() {
			return s, snap
		}
	}
}

// errSwappedOut is build's answer for a slot whose instance its closer
// already Closed: the slot was retired and its drain timeout passed.
var errSwappedOut = errors.New("swapped out")

// lease is Lease without the type assertion. It returns the instance build
// returned, never s.inst: a closer may clear s.inst under buildMu once the
// drain timeout passes, so reading the field here would race with it. A
// slot that is swapped out and force-closed between its lease and its build
// is let go and the lease retried on the slot that replaced it.
func (r *Registry) lease(ctx context.Context, ref Ref) (Instance, func(), error) {
	for {
		inst, release, err := r.leaseOnce(ctx, ref)
		if !errors.Is(err, errSwappedOut) {
			return inst, release, err
		}
	}
}

// leaseOnce is one attempt of lease.
func (r *Registry) leaseOnce(ctx context.Context, ref Ref) (Instance, func(), error) {
	if r.closed.Load() {
		return nil, nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if r.settings().Disabled {
		return nil, nil, refused(ref, "lease", ErrDisabled)
	}
	s, snap := r.current(ref)
	if s == nil {
		switch {
		case r.closed.Load():
			return nil, nil, ErrClosed
		case snap.storeErr != nil:
			return nil, nil, fmt.Errorf("%w: %s: %w", ErrNotConfigured, ref, snap.storeErr)
		}
		return nil, nil, fmt.Errorf("%w: %s", ErrNotConfigured, ref)
	}
	var once sync.Once
	release := func() { once.Do(s.release) }
	switch {
	case s.problem == ReasonAdapterNotInBinary:
		release()
		return nil, nil, fmt.Errorf("%w: %s: adapter %s is not in this binary", ErrUnknownAdapter, ref, s.cfg.Adapter)
	case s.problem != "":
		release()
		return nil, nil, fmt.Errorf("%w: %s: %s", ErrNotConfigured, ref, s.problem)
	case !s.cfg.Enabled:
		release()
		return nil, nil, refused(ref, "lease", fmt.Errorf("%w: %s is not enabled", ErrDisabled, ref))
	}
	inst, err := r.build(ctx, s)
	if err != nil {
		release()
		return nil, nil, err
	}
	return inst, release, nil
}

// build builds s's instance once (lazily, at its first lease or probe). A
// build does no I/O (M1.4), so its failure is kept until the slot is
// swapped.
func (r *Registry) build(ctx context.Context, s *slot) (Instance, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.built {
		if s.buildErr == nil && s.inst == nil {
			return nil, fmt.Errorf("%w: %s was %w", ErrNotConfigured, s.cfg.Ref, errSwappedOut)
		}
		return s.inst, s.buildErr
	}
	s.built = true
	env := r.env(s)
	inst, err := s.ad.desc.build(context.WithoutCancel(ctx), env, cloneConfig(s.cfg))
	switch {
	case err != nil:
		s.buildErr = fmt.Errorf("%w: %s: %s: %w", ErrNotConfigured, s.cfg.Ref, ReasonBuildFailed, err)
	case inst == nil:
		s.buildErr = fmt.Errorf("%w: %s: %s: the adapter built no instance", ErrNotConfigured, s.cfg.Ref, ReasonBuildFailed)
	default:
		s.inst = inst
	}
	return s.inst, s.buildErr
}

// gateOf is s's gate once built, nil before.
func (s *slot) gateOf() Gate {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	return s.gate
}

// env is s's Env (M1.3): the only way its instance reaches the outside.
func (r *Registry) env(s *slot) Env {
	ref := s.cfg.Ref
	var h HTTP
	var g Gate
	if r.o.Outbound != nil {
		h, g = r.o.Outbound(OutboundSpec{
			Ref: ref, Adapter: s.ad.desc.meta.ID, Egress: s.ad.desc.meta.Egress,
			Limits: s.ad.desc.meta.Limits, Override: s.cfg.Limits, Trust: s.trust, ConfiguredOrigins: s.origins,
		})
	}
	if g == nil {
		g = noGate{ref: ref}
	}
	g = guardedGate{inner: g, r: r, ref: ref}
	if h == nil {
		h = noHTTP{ref: ref, gate: g}
	}
	h = guardedHTTP{inner: h, r: r, ref: ref, gate: g}
	s.gate = g
	return Env{
		Ref:     ref,
		Log:     r.log.With("integration.kind", string(ref.Kind), "integration.instance", string(ref.Instance), "adapter", string(s.ad.desc.meta.ID)),
		HTTP:    h,
		Gate:    g,
		Secrets: secretReader{r: r, s: s},
		Now:     r.now,
	}
}

// errNoOutbound: the Registry was built without Options.Outbound.
var errNoOutbound = errors.New("integrations: no outbound client is configured")

// noGate refuses every attempt: the Registry has no outbound policy.
type noGate struct{ ref Ref }

func (g noGate) Acquire(context.Context, string) (Ticket, error) {
	return nil, refused(g.ref, "acquire", errNoOutbound)
}

func (noGate) State() GateState { return GateState{} }

// noHTTP refuses every request before sending.
type noHTTP struct {
	ref  Ref
	gate Gate
}

func (h noHTTP) Do(*http.Request) (*http.Response, error) {
	return nil, refused(h.ref, "http", errNoOutbound)
}

func (h noHTTP) Client() *http.Client {
	return &http.Client{Transport: refuseTransport{ref: h.ref, err: errNoOutbound}}
}

func (h noHTTP) Gate() Gate { return h.gate }

// refuseTransport fails every round trip before sending.
type refuseTransport struct {
	ref Ref
	err error
}

func (t refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, refused(t.ref, "http", t.err)
}

// disabledErr is the refusal while core.integrations.disabled is set,
// read per call (M1.7).
func (r *Registry) disabledErr(ref Ref, op string) error {
	if r.settings().Disabled {
		return refused(ref, op, ErrDisabled)
	}
	return nil
}

// guardedGate refuses while core.integrations.disabled is set, then defers
// to the instance's gate.
type guardedGate struct {
	inner Gate
	r     *Registry
	ref   Ref
}

func (g guardedGate) Acquire(ctx context.Context, counter string) (Ticket, error) {
	if err := g.r.disabledErr(g.ref, "acquire"); err != nil {
		return nil, err
	}
	return g.inner.Acquire(ctx, counter)
}

func (g guardedGate) State() GateState { return g.inner.State() }

// guardedHTTP refuses while core.integrations.disabled is set, on Do and
// on the *http.Client it hands to SDKs, then defers to the instance's
// client.
type guardedHTTP struct {
	inner HTTP
	r     *Registry
	ref   Ref
	gate  Gate
}

func (h guardedHTTP) Do(req *http.Request) (*http.Response, error) {
	if err := h.r.disabledErr(h.ref, "http"); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return h.inner.Do(req)
}

func (h guardedHTTP) Client() *http.Client {
	base := h.inner.Client()
	c := &http.Client{}
	if base != nil {
		*c = *base // the client's settings, with its transport wrapped below
	}
	c.Transport = guardTransport{base: c.Transport, h: h}
	return c
}

func (h guardedHTTP) Gate() Gate { return h.gate }

// guardTransport is guardedHTTP's round tripper.
type guardTransport struct {
	base http.RoundTripper
	h    guardedHTTP
}

func (t guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.h.r.disabledErr(t.h.ref, "http"); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if t.base == nil {
		return refuseTransport{ref: t.h.ref, err: errNoOutbound}.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}

// probe is s's health: the static findings first, then an active probe
// bounded by core.integrations.probe_timeout. The caller holds a lease on s.
func (r *Registry) probe(ctx context.Context, s *slot) (Health, bool) {
	h, probed, d, swapped := r.health(ctx, s)
	if swapped {
		return Health{}, false
	}
	r.observe(s, h, probed, d)
	return h, true
}

// health computes probe's answer; probed reports an active probe and d its
// duration. swapped reports a slot swapped out and force-closed before its
// build: it has no answer, and nothing is recorded for it.
func (r *Registry) health(ctx context.Context, s *slot) (h Health, probed bool, d time.Duration, swapped bool) {
	now := r.now()
	static := func(st HealthStatus, msg string) (Health, bool, time.Duration, bool) {
		return Health{Status: st, Message: msg, CheckedAt: now}, false, 0, false
	}
	switch {
	case r.settings().Disabled:
		return static(HealthDisabled, ReasonIntegrationsDisabled)
	case s.problem != "":
		return static(HealthMisconfigured, s.problem)
	case !s.cfg.Enabled:
		return static(HealthDisabled, ReasonInstanceDisabled)
	}
	if msg := r.secretFinding(ctx, s); msg != "" {
		return static(HealthMisconfigured, msg)
	}
	inst, err := r.build(ctx, s)
	switch {
	case errors.Is(err, errSwappedOut):
		return Health{}, false, 0, true
	case err != nil:
		return static(HealthMisconfigured, ReasonBuildFailed)
	}
	pctx, cancel := context.WithTimeout(ctx, r.settings().ProbeTimeout)
	defer cancel()
	start := r.now()
	h = inst.Health(pctx)
	d = sinceOrZero(r.now, start)
	if h.Latency == 0 {
		h.Latency = d
	}
	h.CheckedAt = now
	if g := s.gateOf(); g != nil {
		st := g.State()
		h.Hold, h.Breaker = st.Hold, st.Breaker
		if (st.Hold != nil || st.Breaker == BreakerOpen) && h.Status < HealthDegraded {
			h.Status = HealthDegraded
		}
	}
	// A withheld secret is reported as itself, also when the adapter
	// answered "auth failed": that refusal was ours, not the remote's. An
	// operator secret is operator_secret_withheld (M1.5); a stored secret of
	// an optional field is integration_secret_withheld_destination_moved (a
	// required one is a static finding above). Either is degraded; down and
	// misconfigured stay as they are.
	var why []string
	if len(s.withheld) > 0 {
		why = append(why, ReasonOperatorSecretWithheld+": "+strings.Join(s.withheld, ","))
	}
	if ws := s.withheldStored(); len(ws) > 0 {
		why = append(why, ReasonStoredSecretWithheld+": "+strings.Join(ws, ","))
	}
	if len(why) > 0 && h.Status != HealthDown && h.Status != HealthMisconfigured {
		h.Status = HealthDegraded
		h.Message = strings.Join(why, "; ")
	}
	return h, true, d, false
}

// secretFinding is the static secret finding of s (M1.5): a required secret
// field with nothing to serve is integration_secret_missing, or
// integration_secret_withheld_destination_moved when the field keeps a
// stored secret that the effective destinations withhold (the fix is to
// re-enter it or restore the destination, not to look for a lost secret); a
// stored secret that cannot be decrypted is integration_secret_unreadable.
// A withheld operator secret, or a withheld stored secret of an optional
// field, is not a finding here: it degrades the probe's answer.
func (r *Registry) secretFinding(ctx context.Context, s *slot) string {
	ref := s.cfg.Ref
	withheld := s.withheldStored()
	for _, f := range s.ad.fs.secrets {
		if _, ok := s.opSec[f]; ok {
			continue
		}
		if _, ok := s.given[f]; ok {
			continue
		}
		if slices.Contains(withheld, f) {
			if s.ad.fs.required[f] {
				return ReasonStoredSecretWithheld + ": " + strings.Join(withheld, ",")
			}
			continue
		}
		sr, isRef := s.cfg.Settings[f].(secrets.Ref)
		if !isRef || sr.Name() != secretName(ref, f) || !s.storedSecrets {
			if s.ad.fs.required[f] {
				return ReasonSecretMissing
			}
			continue
		}
		m, err := r.o.Secrets.Meta(ctx, sr.Name())
		switch {
		case err != nil || m.Err != nil:
			return ReasonSecretUnreadable
		case (!m.Set || m.Source != secrets.SourceStored) && s.ad.fs.required[f]:
			return ReasonSecretMissing
		}
	}
	return ""
}

// ProbeAll probes every instance, at most core.integrations.health_parallelism
// at once, each bounded by core.integrations.probe_timeout: the body of the
// core.integrations.health task (M1.4 step 5), which the tasks flip
// registers (name coined by F7-01). While core.integrations.disabled is set
// it probes nothing and reports every instance HealthDisabled.
func (r *Registry) ProbeAll(ctx context.Context) error {
	snap := r.snap.Load()
	refs := make([]Ref, 0, len(snap.slots))
	for ref := range snap.slots {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b Ref) int { return compareStrings(a.String(), b.String()) })
	sem := make(chan struct{}, r.settings().HealthParallelism)
	var wg sync.WaitGroup
	for _, ref := range refs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if _, err := r.Health(ctx, ref); err != nil {
				r.log.Debug("integrations: an instance went away during the health run", "instance", ref.String(), "error", err)
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// observe records h as s's last health, feeds the metrics and calls
// Options.HealthChanged when the status moved.
func (r *Registry) observe(s *slot, h Health, probed bool, d time.Duration) {
	ref := s.cfg.Ref
	r.hmu.Lock()
	prev, had := r.last[ref]
	r.last[ref] = h
	r.hmu.Unlock()
	if probed {
		r.met.probed(ref, s.cfg.Adapter, d)
	}
	if r.o.HealthChanged != nil && (!had || prev.Status != h.Status) {
		r.o.HealthChanged(ref, h)
	}
}

// lastHealth is ref's last recorded health.
func (r *Registry) lastHealth(ref Ref) (Health, bool) {
	r.hmu.Lock()
	defer r.hmu.Unlock()
	h, ok := r.last[ref]
	return h, ok
}

// forget drops the last health of every ref not in keep.
func (r *Registry) forget(keep map[Ref]*slot) {
	r.hmu.Lock()
	defer r.hmu.Unlock()
	for ref := range r.last {
		if keep[ref] == nil {
			delete(r.last, ref)
		}
	}
}
