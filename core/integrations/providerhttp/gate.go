// file: core/integrations/providerhttp/gate.go
// version: 1.2.0
// guid: ec0c1bf4-fb0b-44a4-ac17-f2ca02f02f83
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// gate is one instance's admission control over its scope's shared state. It
// implements integrations.Gate and is what the transport admits through, so a
// non-HTTP adapter and an HTTP one are held by the same hold, breaker, bucket
// and quota.
type gate struct {
	r   *Registry
	ref integrations.Ref
	pol Policy
	sc  *scopeState
	// scr removes this instance's credentials from errors and excerpts: the
	// built-in rules plus Policy.Redact and the query credential's name.
	scr scrubber
	// private reports whether an endpoint ("host:port") is one the instance may
	// reach on a private network. Derived from Policy.Trust by the Registry and
	// settable by nothing else.
	private func(hostport string) bool
	// qc is Policy.QueryCredential; qcOrigins are its Origins as
	// "scheme://host:port" keys (endpoint form).
	qc        *integrations.QueryCredential
	qcOrigins map[string]struct{}
}

// qcApplies reports whether the query credential may be sent to u: it is set
// and u's origin is one of the credential's Origins.
func (g *gate) qcApplies(u *url.URL) bool {
	if g.qc == nil || u == nil {
		return false
	}
	_, ok := g.qcOrigins[strings.ToLower(u.Scheme)+"://"+endpoint(u)]
	return ok
}

// originKeys parses "scheme://host[:port]" origins into the keys qcApplies
// looks up; an entry that is not an http(s) origin with a host is dropped (it
// can then match nothing).
func originKeys(origins []string) map[string]struct{} {
	out := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		u, err := url.Parse(strings.TrimSpace(o))
		if err != nil || ValidateParsedURL(u) != nil {
			continue
		}
		out[strings.ToLower(u.Scheme)+"://"+endpoint(u)] = struct{}{}
	}
	return out
}

var _ integrations.Gate = (*gate)(nil)

// ticket is one admitted attempt: its start time and, when it holds the
// breaker's half-open lease, the lease.
type ticket struct {
	g     *gate
	ctx   context.Context
	start time.Time
	probe bool
	epoch uint64
	once  sync.Once
}

// outcome is what one finished attempt says.
type outcome struct {
	class      integrations.Class
	status     int
	retryAfter time.Duration
	success    bool
}

// admit runs the first two stages of M2.4's order for one request: the
// disabled switch, ctx, the hold, the breaker. Nothing is sent, dialled or
// written when it refuses.
func (g *gate) admit(ctx context.Context, op string) (*ticket, error) {
	r := g.r
	if r.isDisabled() {
		r.met.refused(g.ref, ReasonDisabled)
		return nil, refuse(g.ref, op, ReasonDisabled, time.Time{}, integrations.ErrDisabled)
	}
	if err := ctx.Err(); err != nil {
		return nil, canceled(g.ref, op, err)
	}
	if !bypassOf(ctx) {
		if h, ok := r.activeHold(g.sc.name); ok {
			r.met.refused(g.ref, ReasonHold)
			return nil, refuse(g.ref, op, ReasonHold, h.Until, nil)
		}
	}
	now := r.now()
	g.sc.mu.Lock()
	lim := g.sc.lim
	probe, epoch, until, ok := g.sc.br.admit(now, lim.Breaker)
	g.sc.mu.Unlock()
	if !ok {
		r.met.refused(g.ref, ReasonBreaker)
		return nil, refuse(g.ref, op, ReasonBreaker, until, nil)
	}
	return &ticket{g: g, ctx: ctx, start: now, probe: probe, epoch: epoch}, nil
}

// pace runs the last two stages for one attempt: the shared bucket (ctx ends
// the wait) and the daily quota, counted per request SENT, retries included.
// counter is "" for ctx's counter, else the default.
//
// It returns the counter the attempt was counted against ("" when the limits
// declare no daily quota), so a refusal made after the count can refund it.
func (g *gate) pace(ctx context.Context, counter, op string) (string, error) {
	r := g.r
	if err := g.sc.bk.wait(ctx, r.sleep); err != nil {
		return "", canceled(g.ref, op, err)
	}
	if counter == "" {
		counter = counterOf(ctx)
	}
	used, err := g.sc.reserve(ctx, counter, interactiveOf(ctx))
	if err != nil {
		if ctx.Err() != nil && !errors.Is(err, ErrQuotaSpent) {
			return "", canceled(g.ref, op, ctx.Err())
		}
		r.met.refused(g.ref, ReasonQuota)
		return "", refuse(g.ref, op, ReasonQuota, time.Time{}, err)
	}
	return used, nil
}

// finish records the attempt's outcome once: the breaker, then the hold. A
// success releases only a hold that existed when the attempt started.
func (t *ticket) finish(o outcome) {
	t.once.Do(func() {
		g, r := t.g, t.g.r
		v := verdictOf(o.class, o.success)
		g.sc.mu.Lock()
		lim := g.sc.lim
		g.sc.br.record(r.now(), lim.Breaker, t.probe, t.epoch, v)
		g.sc.mu.Unlock()
		switch {
		case o.success:
			r.releaseIfStarted(t.ctx, g.sc.name, t.start)
		case o.class.CountsAgainstRemote():
			d := holdDuration(lim.Holds, o)
			r.placeHold(t.ctx, g.ref, g.sc.name, o.class, "class="+o.class.String(), d, lim.Holds.Max)
		}
	})
}

// abort ends the ticket without an outcome: a probe's lease is released and
// nothing is counted.
func (t *ticket) abort() { t.finish(outcome{class: integrations.ClassUnknown}) }

// holdDuration is how long to stand off after o: HEAD's table
// (throttle.go:59-65), overridable per class by HoldPolicy.Durations. A rate
// limit honours Retry-After, never shorter than the class default and never
// longer than the maximum (the caller clamps).
func holdDuration(p integrations.HoldPolicy, o outcome) time.Duration {
	if d, ok := p.Durations[o.class]; ok && d > 0 {
		if o.class == integrations.ClassRateLimited {
			return max(o.retryAfter, d)
		}
		return d
	}
	switch o.class {
	case integrations.ClassQuota:
		return 4 * time.Hour
	case integrations.ClassRateLimited:
		return max(o.retryAfter, 15*time.Minute)
	case integrations.ClassAuth:
		return 6 * time.Hour
	case integrations.ClassTransient:
		if o.status >= 500 {
			return 30 * time.Minute
		}
		return 5 * time.Minute
	}
	return 0
}

// Acquire runs hold, breaker, bucket, quota in that order for a non-HTTP
// attempt; nothing may be sent if it errors. The returned Ticket's Done records
// the outcome (integrations.Gate).
func (g *gate) Acquire(ctx context.Context, counter string) (integrations.Ticket, error) {
	tk, err := g.admit(ctx, "acquire")
	if err != nil {
		return nil, err
	}
	if _, err := g.pace(ctx, counter, "acquire"); err != nil {
		tk.abort()
		return nil, err
	}
	return gateTicket{tk}, nil
}

type gateTicket struct{ t *ticket }

// Done records err (nil: success), classified with integrations.ClassOf
// against the Acquire context.
func (gt gateTicket) Done(err error) {
	if err == nil {
		gt.t.finish(outcome{class: integrations.ClassUnknown, success: true})
		return
	}
	o := outcome{class: integrations.ClassOf(gt.t.ctx, err)}
	if e, ok := errors.AsType[*integrations.Error](err); ok {
		o.status, o.retryAfter = e.Status, e.RetryAfter
	}
	gt.t.finish(o)
}

// State is the gate's current admission state.
func (g *gate) State() integrations.GateState {
	now := g.r.now()
	st := integrations.GateState{BucketTokens: g.sc.bk.available()}
	if h, ok := g.r.activeHold(g.sc.name); ok {
		st.Hold = &h
	}
	g.sc.mu.Lock()
	st.Breaker = g.sc.br.view(now)
	g.sc.mu.Unlock()
	st.QuotaUsed, st.QuotaLimit = g.sc.quotaView()
	return st
}
