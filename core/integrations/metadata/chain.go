// file: core/integrations/metadata/chain.go
// version: 1.0.0
// guid: 31d931b3-d892-48d9-a8cb-e32e85abbb79
// last-edited: 2026-11-11

package metadata

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/tasks"
)

// ChainMode says what Search does with the providers after one has answered.
type ChainMode uint8

const (
	// FirstHit asks the providers one at a time in priority order and stops at
	// the first that returns results. It never calls a provider whose answer
	// would be discarded: every call spends that provider's request budget.
	FirstHit ChainMode = iota
	// All asks every provider, up to Parallelism at a time.
	All
)

// defaultPerProvider is ChainOptions.PerProvider's default
// (core.metadata.per_provider_timeout).
const defaultPerProvider = 20 * time.Second

// ChainOptions configures NewChain.
type ChainOptions[Q any] struct {
	// Instances lists the providers to ask, in order. Default: the enabled
	// metadata instances that are built as the chain's Provider[Q, R], by
	// Priority (lower first, then instance ID), read on every Search so an
	// edit, a reorder or an enable takes effect on the next search.
	Instances func() []integrations.Ref
	// Parallelism bounds the fan-out of All and Stream. Required (it has no
	// usable zero value, 03 R7): network-bound work declares tasks.Fixed with
	// the reason, e.g. tasks.Fixed(4, "provider rate limits").
	Parallelism tasks.Parallelism
	// PerProvider is each provider's deadline inside one call (default 20s).
	PerProvider time.Duration
	// Mode is FirstHit (default) or All.
	Mode ChainMode
	// Cache, when set, answers each provider's question read-through.
	Cache *Cache[Q]
}

// Chain asks a module's metadata providers in priority order, skipping the
// ones the gate holds. It is goroutine-safe. The breaker, hold and bucket
// state it relies on lives in providerhttp per Ref, so editing or reordering
// an instance never resets it (M3.4).
type Chain[Q, R any] struct {
	reg     *integrations.Registry
	o       ChainOptions[Q]
	workers int
}

// NewChain returns a Chain over reg's metadata instances.
func NewChain[Q, R any](reg *integrations.Registry, opts ChainOptions[Q]) (*Chain[Q, R], error) {
	if reg == nil {
		return nil, errors.New("metadata: a Chain needs the integrations Registry")
	}
	if err := opts.Parallelism.Validate(); err != nil {
		return nil, fmt.Errorf("metadata: a Chain needs a declared Parallelism: %w", err)
	}
	if opts.Mode != FirstHit && opts.Mode != All {
		return nil, fmt.Errorf("metadata: unknown ChainMode %d", opts.Mode)
	}
	if opts.PerProvider <= 0 {
		opts.PerProvider = defaultPerProvider
	}
	return &Chain[Q, R]{reg: reg, o: opts, workers: max(opts.Parallelism.Workers(), 1)}, nil
}

// slot is one provider's part of a search.
type slot[R any] struct {
	considered bool // false: not this chain's instance (another query type)
	out        Outcome
	rs         []R
	at         time.Time
	fromCache  bool
}

// refs is the ordered provider list of this search. explicit says the caller
// named them: an instance of another query type is then an error, not a skip.
func (c *Chain[Q, R]) refs() (refs []integrations.Ref, explicit bool) {
	if c.o.Instances != nil {
		return slices.Clone(c.o.Instances()), true
	}
	var cfgs []integrations.InstanceConfig
	for cfg := range c.reg.Instances(integrations.KindMetadata) {
		if cfg.Enabled {
			cfgs = append(cfgs, cfg)
		}
	}
	slices.SortStableFunc(cfgs, func(a, b integrations.InstanceConfig) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(string(a.Ref.Instance), string(b.Ref.Instance))
	})
	for _, cfg := range cfgs {
		refs = append(refs, cfg.Ref)
	}
	return refs, false
}

// Search asks the providers and returns their results in priority order, plus
// one Outcome for every instance considered (answered, empty, skipped or
// failed). err is non-nil only when NO provider answered: ErrNoProviderAnswered
// joined with each provider's error (mirroring ABO's ErrNoSourceAnswered), so
// "no answer" never looks like "no results". An empty answer is an answer.
// When ctx ends err also carries ctx's error, whatever the providers said, so
// a partial result is never mistaken for a complete one.
//
// In FirstHit mode the providers after the first one with results are not
// asked and have no Outcome.
func (c *Chain[Q, R]) Search(ctx context.Context, q Q) ([]Result[R], []Outcome, error) {
	refs, explicit := c.refs()
	var slots []slot[R]
	if c.o.Mode == FirstHit {
		slots = c.runFirstHit(ctx, q, refs, explicit)
	} else {
		slots = c.runAll(ctx, q, refs, explicit, nil)
	}
	var (
		results  []Result[R]
		outcomes []Outcome
		causes   []error
		answered bool
	)
	for _, s := range slots {
		if !s.considered {
			continue
		}
		outcomes = append(outcomes, s.out)
		switch {
		case s.out.answered():
			answered = true
			results = append(results, s.results(s.out.Provider)...)
		case s.out.Err != nil && s.out.Status != Canceled:
			causes = append(causes, s.out.Err)
		}
	}
	var errs []error
	if cerr := ctx.Err(); cerr != nil {
		errs = append(errs, fmt.Errorf("metadata: search canceled: %w", cerr))
	}
	if !answered {
		errs = append(errs, ErrNoProviderAnswered)
	}
	if len(errs) == 0 {
		return results, outcomes, nil
	}
	return results, outcomes, errors.Join(append(errs, causes...)...)
}

// Stream asks every provider, up to Parallelism at a time, and yields each
// provider's results as its answer arrives, in completion order (FirstHit is
// ignored). A provider that failed yields one (Result{Provider}, err); held,
// skipped and empty providers yield nothing. Stopping the iteration cancels
// the providers still running and waits for them. If ctx ends before the
// providers finish, the last pair is (Result{}, the cancellation).
func (c *Chain[Q, R]) Stream(ctx context.Context, q Q) iter.Seq2[Result[R], error] {
	return func(yield func(Result[R], error) bool) {
		refs, explicit := c.refs()
		runCtx, cancel := context.WithCancel(ctx)
		ch := make(chan slot[R], len(refs)) // never blocks a worker
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.runAll(runCtx, q, refs, explicit, func(s slot[R]) { ch <- s })
			close(ch)
		}()
		defer func() {
			cancel()
			<-done
		}()
		for s := range ch {
			if !s.considered {
				continue
			}
			switch {
			case s.out.answered():
				for _, r := range s.results(s.out.Provider) {
					if !yield(r, nil) {
						return
					}
				}
			case s.out.Status == Failed:
				if !yield(Result[R]{Provider: s.out.Provider}, s.out.Err) {
					return
				}
			}
		}
		if err := ctx.Err(); err != nil {
			yield(Result[R]{}, fmt.Errorf("metadata: search canceled: %w", err))
		}
	}
}

// results is the slot's answer as Results of ref.
func (s slot[R]) results(ref integrations.Ref) []Result[R] {
	out := make([]Result[R], 0, len(s.rs))
	for _, v := range s.rs {
		out = append(out, Result[R]{Provider: ref, Value: v, AskedAt: s.at, FromCache: s.fromCache})
	}
	return out
}

// runFirstHit asks the providers in order until one returns results.
func (c *Chain[Q, R]) runFirstHit(ctx context.Context, q Q, refs []integrations.Ref, explicit bool) []slot[R] {
	slots := make([]slot[R], 0, len(refs))
	for _, ref := range refs {
		if ctx.Err() != nil {
			slots = append(slots, canceledSlot[R](ref, ctx.Err()))
			break
		}
		s := c.ask(ctx, ref, q, explicit)
		slots = append(slots, s)
		if s.considered && s.out.Status == Answered {
			break
		}
	}
	return slots
}

// runAll asks every provider with at most c.workers calls in flight (a
// semaphore, not a goroutine per provider: the chain is reachable from a
// library-scale loop). emit, when set, is called from the workers as each
// answer lands.
func (c *Chain[Q, R]) runAll(ctx context.Context, q Q, refs []integrations.Ref, explicit bool, emit func(slot[R])) []slot[R] {
	slots := make([]slot[R], len(refs))
	sem := make(chan struct{}, c.workers)
	var wg sync.WaitGroup
	for i, ref := range refs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for j := i; j < len(refs); j++ {
				slots[j] = canceledSlot[R](refs[j], ctx.Err())
				if emit != nil {
					emit(slots[j])
				}
			}
			wg.Wait()
			return slots
		}
		wg.Go(func() {
			defer func() { <-sem }()
			slots[i] = c.ask(ctx, ref, q, explicit)
			if emit != nil {
				emit(slots[i])
			}
		})
	}
	wg.Wait()
	return slots
}

func canceledSlot[R any](ref integrations.Ref, err error) slot[R] {
	return slot[R]{considered: true, out: Outcome{Provider: ref, Status: Canceled, Class: integrations.ClassCanceled, Err: err}}
}

// ask runs one provider's part of a search: lease, capability check, the call
// (through the cache when there is one) under the per-provider deadline, and
// the classification of what came back.
func (c *Chain[Q, R]) ask(ctx context.Context, ref integrations.Ref, q Q, explicit bool) slot[R] {
	start := time.Now()
	done := func(s slot[R]) slot[R] {
		s.considered = true
		s.out.Provider = ref
		s.out.Elapsed = time.Since(start)
		return s
	}
	inst, release, err := integrations.Lease[integrations.Instance](ctx, c.reg, ref)
	if err != nil {
		return done(slot[R]{out: classify(ctx, err)})
	}
	defer release()
	p, ok := inst.(Provider[Q, R])
	if !ok {
		if !explicit {
			return slot[R]{} // an instance of another query type: not this chain's
		}
		return done(slot[R]{out: Outcome{Status: Failed, Class: integrations.ClassUnsupported,
			Err: fmt.Errorf("metadata: %s is built as %T, not the chain's provider type", ref, inst)}})
	}
	if cp, ok := inst.(Caps[Q]); ok && !cp.Accepts(q) {
		return done(slot[R]{out: Outcome{Status: SkippedCaps}})
	}
	pctx, cancel := context.WithTimeout(ctx, c.o.PerProvider)
	defer cancel()
	rs, at, fromCache, err := cached(pctx, c.o.Cache, p, q)
	if err != nil {
		return done(slot[R]{out: classify(ctx, err)})
	}
	st := Answered
	if len(rs) == 0 {
		st = Empty
	}
	return done(slot[R]{out: Outcome{Status: st}, rs: rs, at: at, fromCache: fromCache})
}

// classify turns a provider's error into its Outcome. parent is the CALLER's
// context: a done parent always means Canceled (our budget, not the
// provider's), whatever error the provider returned. The gate's own refusals
// are skips (M3.5): a hold or a spent quota is SkippedHeld, an open breaker
// SkippedBreaker; every other error is Failed, whatever it is.
func classify(parent context.Context, err error) Outcome {
	if parent.Err() != nil {
		return Outcome{Status: Canceled, Class: integrations.ClassCanceled, Err: err}
	}
	out := Outcome{Status: Failed, Class: integrations.ClassOf(parent, err), Err: err}
	if reason, ok := providerhttp.ReasonOf(err); ok {
		switch reason {
		case providerhttp.ReasonHold, providerhttp.ReasonQuota:
			out.Status = SkippedHeld
		case providerhttp.ReasonBreaker:
			out.Status = SkippedBreaker
		}
	}
	return out
}
