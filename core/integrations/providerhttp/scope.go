// file: core/integrations/providerhttp/scope.go
// version: 1.1.0
// guid: e8205a94-c082-4fb1-8b84-cf178b452d05
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"
	_ "time/tzdata" // quota zones are configuration: a container image may carry no zone database

	"github.com/falkcorp/mediacore/integrations"
)

// scopeState is the gate state one Scope shares among all its Clients
// (05-metadata M2.3): ONE bucket, ONE breaker, ONE set of quota counters. Its
// hold is in the Registry's hold table, keyed by the same scope name.
type scopeState struct {
	r    *Registry
	name string
	bk   *bucket

	mu  sync.Mutex // lim and br
	lim integrations.Limits
	br  breaker

	cmu      sync.Mutex // counters (the map, not the counters)
	counters map[string]*counter
}

// counter is one daily counter. Its mutex is held across the store's commit,
// so concurrent reservations lose no count (M2.6).
type counter struct {
	mu     sync.Mutex
	loaded bool
	day    string
	used   int
}

func (r *Registry) newScope(name string, lim integrations.Limits) *scopeState {
	return &scopeState{r: r, name: name, lim: lim, bk: newBucket(r.now, lim.RPS, lim.Burst),
		counters: map[string]*counter{}}
}

// limits is the scope's effective limits.
func (s *scopeState) limits() integrations.Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lim
}

// setLimits applies l in place: the bucket is resized, never rebuilt.
func (s *scopeState) setLimits(l integrations.Limits) {
	s.mu.Lock()
	s.lim = l
	s.mu.Unlock()
	s.bk.set(l.RPS, l.Burst)
}

// minRPS is the slowest rate a limit may name: one request a day. Below it a
// rate is configuration noise (1e-300), and the wait it implies is not a
// duration.
const minRPS = 1.0 / 86400

// normalizeLimits fills every zero field of l from the core fallback and
// repairs nonsense: a nonsensical limit is clamped, never allowed to disable
// pacing (HEAD's normalize, kept).
func normalizeLimits(l integrations.Limits, set Settings) integrations.Limits {
	f := set.Fallback
	if !(l.RPS > 0) || math.IsInf(l.RPS, 0) || math.IsNaN(l.RPS) {
		l.RPS = f.RPS
	}
	l.RPS = max(l.RPS, minRPS)
	switch {
	case l.Burst == 0:
		l.Burst = f.Burst
	case l.Burst < 0:
		l.Burst = 1
	}
	switch {
	case l.MaxRetries == 0:
		l.MaxRetries = f.MaxRetries
	case l.MaxRetries < 0:
		l.MaxRetries = 0
	}
	if l.Timeout <= 0 {
		l.Timeout = f.Timeout
	}
	if l.RetryAfterMax <= 0 {
		l.RetryAfterMax = set.RetryAfterMax
	}
	if l.Breaker.Threshold <= 0 {
		l.Breaker.Threshold = 5
	}
	if l.Breaker.Cooldown <= 0 {
		l.Breaker.Cooldown = 30 * time.Second
	}
	if l.Breaker.ProbeTimeout <= 0 {
		l.Breaker.ProbeTimeout = l.Timeout
	}
	if l.Holds.Max <= 0 {
		l.Holds.Max = set.HoldMax
	}
	if l.Daily != nil {
		d := *l.Daily
		if d.Zone == nil {
			d.Zone = time.UTC
		}
		if len(d.Counters) == 0 {
			d.Counters = []string{defaultCounter}
		}
		d.Background = min(d.Background, d.Total)
		l.Daily = &d
	}
	return l
}

// MergeLimits returns base with every non-zero field of over applied on top:
// the instance's override over the adapter's defaults (M1.3 Limits; "zero
// fields inherit"). Daily replaces whole when over sets it.
func MergeLimits(base integrations.Limits, over *integrations.Limits) integrations.Limits {
	if over == nil {
		return base
	}
	o := *over
	if o.RPS != 0 {
		base.RPS = o.RPS
	}
	if o.Burst != 0 {
		base.Burst = o.Burst
	}
	if o.MaxRetries != 0 {
		base.MaxRetries = o.MaxRetries
	}
	if o.Timeout != 0 {
		base.Timeout = o.Timeout
	}
	if o.RetryAfterMax != 0 {
		base.RetryAfterMax = o.RetryAfterMax
	}
	if o.Daily != nil {
		base.Daily = o.Daily
	}
	if o.Breaker.Threshold != 0 {
		base.Breaker.Threshold = o.Breaker.Threshold
	}
	if o.Breaker.Cooldown != 0 {
		base.Breaker.Cooldown = o.Breaker.Cooldown
	}
	if o.Breaker.ProbeTimeout != 0 {
		base.Breaker.ProbeTimeout = o.Breaker.ProbeTimeout
	}
	if o.Holds.Max != 0 {
		base.Holds.Max = o.Holds.Max
	}
	if len(o.Holds.Durations) > 0 {
		merged := map[integrations.Class]time.Duration{}
		maps.Copy(merged, base.Holds.Durations)
		maps.Copy(merged, o.Holds.Durations)
		base.Holds.Durations = merged
	}
	base.Holds.Persist = base.Holds.Persist || o.Holds.Persist
	return base
}

// counterFor is the counter name's state, nil when the limits do not declare
// the name.
func (s *scopeState) counterFor(name string, d *integrations.DailyLimits) *counter {
	declared := slices.Contains(d.Counters, name)
	if !declared {
		return nil
	}
	s.cmu.Lock()
	defer s.cmu.Unlock()
	c := s.counters[name]
	if c == nil {
		c = &counter{}
		s.counters[name] = c
	}
	return c
}

// reserve takes one unit of the named counter (empty: the first declared) at
// the tier the caller runs at. The reservation is taken BEFORE the request and
// never refunded: over-counting a quota is the safe direction (M2.5). A
// counter that cannot be read or written refuses (ErrBudgetUnavailable): a
// quota the gate cannot read could be spent.
func (s *scopeState) reserve(ctx context.Context, name string, interactive bool) (counted string, err error) {
	lim := s.limits()
	d := lim.Daily
	if d == nil {
		return "", nil
	}
	if name == "" {
		name = d.Counters[0]
	}
	c := s.counterFor(name, d)
	if c == nil {
		return "", ErrUnknownCounter
	}
	limit := d.Background
	if interactive {
		limit = d.Total
	}
	limit = max(limit, 0)
	today := s.r.now().In(d.Zone).Format(dayForm)

	c.mu.Lock()
	defer c.mu.Unlock()
	sctx, cancel := s.r.storeCtx(ctx)
	defer cancel()
	if !c.loaded {
		day, used, err := s.r.store.Quota(sctx, s.name, name)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrBudgetUnavailable, err)
		}
		c.day, c.used, c.loaded = day, used, true
	}
	// A new day starts at zero. A counter whose stored day is LATER than today
	// (the clock moved back) keeps counting on that day: over-counting is the
	// safe direction.
	if c.day < today {
		c.day, c.used = today, 0
	}
	if c.used >= limit {
		return "", fmt.Errorf("%w: %d of %d used", ErrQuotaSpent, c.used, limit)
	}
	c.used++
	if err := s.r.store.PutQuota(sctx, s.name, name, c.day, c.used); err != nil {
		c.used-- // nothing was counted, nothing is sent
		return "", fmt.Errorf("%w: %w", ErrBudgetUnavailable, err)
	}
	return name, nil
}

// refund gives back one unit of the named counter for an attempt the egress
// guard refused AFTER it was counted: nothing was sent, so nothing is spent
// (a data-derived URL that names a private host must not be a way to burn a
// provider's daily allowance). It is the one refund: a request that was sent
// counts even if it failed (M2.4). A store that cannot take the lower count
// leaves the higher one in place, which over-counts: the safe direction.
func (s *scopeState) refund(ctx context.Context, name string) {
	if name == "" {
		return
	}
	d := s.limits().Daily
	if d == nil {
		return
	}
	c := s.counterFor(name, d)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || c.used <= 0 {
		return
	}
	sctx, cancel := s.r.storeCtx(ctx)
	defer cancel()
	if err := s.r.store.PutQuota(sctx, s.name, name, c.day, c.used-1); err == nil {
		c.used--
	}
}

// quotaView is the in-memory counts per counter and their Total limit.
func (s *scopeState) quotaView() (used, limit map[string]int) {
	d := s.limits().Daily
	if d == nil {
		return nil, nil
	}
	used, limit = map[string]int{}, map[string]int{}
	today := s.r.now().In(d.Zone).Format(dayForm)
	for _, name := range d.Counters {
		limit[name] = d.Total
		s.cmu.Lock()
		c := s.counters[name]
		s.cmu.Unlock()
		if c == nil {
			continue
		}
		c.mu.Lock()
		if c.loaded && c.day >= today {
			used[name] = c.used
		}
		c.mu.Unlock()
	}
	return used, limit
}
