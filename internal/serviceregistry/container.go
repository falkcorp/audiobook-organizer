// file: internal/serviceregistry/container.go
// version: 1.2.2
// last-edited: 2026-09-02

package serviceregistry

import (
	"context"
	"fmt"
	"slices"
	"sort"
)

type containerPhase int

const (
	phaseUnresolved containerPhase = iota
	phaseResolved
	phaseBuilt
	phasePostInit
	phaseStarted
	phaseStopped
)

// Container holds per-instance registry state. Created fresh per
// NewServer() and per test. The factory list is global; each Container
// builds its own service instances from it.
type Container struct {
	include       map[string]bool
	overrides     map[string]any
	built         map[string]any
	order         []string
	phase         containerPhase
	activeBuilder string // name of service whose Build is currently running
}

// NewContainer creates a new empty Container.
func NewContainer() *Container {
	return &Container{
		include:   map[string]bool{},
		overrides: map[string]any{},
		built:     map[string]any{},
	}
}

// Include adds service names to the build set. Transitive deps are pulled
// in at Resolve time. Chainable.
func (c *Container) Include(names ...string) *Container {
	for _, n := range names {
		c.include[n] = true
	}
	return c
}

// IncludeAll adds every registered ServiceDef. Useful for tests that want
// to verify the global dep graph builds; production code should prefer
// IncludeGroup or explicit Include to keep the included set auditable.
func (c *Container) IncludeAll() *Container {
	for name := range registered {
		c.include[name] = true
	}
	return c
}

// IncludeGroup adds every registered ServiceDef whose Groups field
// contains any of the given group names. Transitive deps are pulled in
// at Resolve time (same semantics as Include). Chainable.
//
// Group membership is declared on ServiceDef.Groups. Audit a group with
//
//	grep -rn 'Groups.*"<groupname>"' internal/ --include="*.go"
//
// Unknown group names are silently no-ops — they include zero services.
// Callers that want to assert a group exists can check via Groups().
func (c *Container) IncludeGroup(names ...string) *Container {
	want := make(map[string]bool, len(names))
	for _, g := range names {
		want[g] = true
	}
	for name, def := range registered {
		for _, g := range def.Groups {
			if want[g] {
				c.include[name] = true
				break
			}
		}
	}
	return c
}

// Groups returns a sorted, deduplicated list of every group name declared
// on any registered ServiceDef. Useful for diagnostics, audit tooling,
// and tests that want to assert a group exists.
func Groups() []string {
	seen := map[string]struct{}{}
	for _, def := range registered {
		for _, g := range def.Groups {
			seen[g] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Override substitutes an instance for the named service. Factory Build
// is not called. The name is implicitly Included. Treated as a leaf in
// the dep graph (its declared Needs are ignored). Test-only.
func (c *Container) Override(name string, instance any) *Container {
	c.overrides[name] = instance
	c.include[name] = true
	return c
}

// Names returns the resolved build order. Only valid after Resolve.
func (c *Container) Names() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

func (c *Container) requirePhase(want containerPhase, op string) error {
	if c.phase < want {
		return fmt.Errorf("%w: %s requires phase >= %d, have %d", ErrWrongPhase, op, want, c.phase)
	}
	return nil
}

// Build runs all ServiceDef.Build funcs in resolved order. Resolve is
// called implicitly if needed. Each Build runs under activeBuilder
// tracking so Get[T] can enforce Needs membership.
func (c *Container) Build(ctx context.Context) error {
	if c.phase >= phaseBuilt {
		return nil
	}
	if err := c.Resolve(); err != nil {
		return err
	}
	for _, name := range c.order {
		if instance, isOverride := c.overrides[name]; isOverride {
			c.built[name] = instance
			continue
		}
		def := registered[name]
		c.activeBuilder = name
		instance, err := def.Build(c)
		c.activeBuilder = ""
		if err != nil {
			return fmt.Errorf("serviceregistry: build %q: %w", name, err)
		}
		c.built[name] = instance
	}
	c.phase = phaseBuilt
	return nil
}

// PostInit invokes PostInit() on services that implement PostIniter,
// in resolved dep order. Get[T] is unrestricted here.
func (c *Container) PostInit(ctx context.Context) error {
	if err := c.requirePhase(phaseBuilt, "PostInit"); err != nil {
		return err
	}
	if c.phase >= phasePostInit {
		return nil
	}
	for _, name := range c.order {
		instance := c.built[name]
		if pi, ok := instance.(PostIniter); ok {
			if err := pi.PostInit(ctx, c); err != nil {
				return fmt.Errorf("serviceregistry: postinit %q: %w", name, err)
			}
		}
	}
	c.phase = phasePostInit
	return nil
}

// Start invokes Start() on services that implement Starter, in resolved
// order. On error, aborts and calls Stop on already-started services
// in reverse.
func (c *Container) Start(ctx context.Context) error {
	if err := c.requirePhase(phasePostInit, "Start"); err != nil {
		return err
	}
	if c.phase >= phaseStarted {
		return nil
	}
	started := []string{}
	for _, name := range c.order {
		instance := c.built[name]
		s, ok := instance.(Starter)
		if !ok {
			continue
		}
		if err := s.Start(ctx); err != nil {
			// Stop already-started services in reverse.
			for _, s := range slices.Backward(started) {
				if stopper, ok := c.built[s].(Stopper); ok {
					_ = stopper.Stop(ctx)
				}
			}
			return fmt.Errorf("serviceregistry: start %q: %w", name, err)
		}
		started = append(started, name)
	}
	c.phase = phaseStarted
	return nil
}

// Stop invokes Stop() on services that implement Stopper, in REVERSE
// resolved order. Best-effort — errors are logged but do not abort.
func (c *Container) Stop(ctx context.Context) error {
	if c.phase >= phaseStopped {
		return nil
	}
	var firstErr error
	for _, name := range slices.Backward(c.order) {

		instance, ok := c.built[name]
		if !ok {
			continue
		}
		s, ok := instance.(Stopper)
		if !ok {
			continue
		}
		if err := s.Stop(ctx); err != nil {
			err = fmt.Errorf("serviceregistry: stop %q: %w", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	c.phase = phaseStopped
	return firstErr
}

// Get returns the instance registered under name, type-asserted to T.
// Panics on missing service or type mismatch (programmer error — fail
// fast at startup). During Build, also panics if name is not in the
// active builder's Needs.
//
// For a service consumed with the SAME type across many builders (ARCH-8),
// prefer a typed accessor over a bare Get[T](c, name) call if one exists —
// e.g. config.GetConfig(c) instead of Get[*config.Config](c, KeyConfig), or
// plugin.GetEventBus(c) instead of Get[*plugin.EventBus](c, KeyEventBus).
// Those wrap this exact function (same panic behavior, unchanged) but fix
// BOTH the key and T together in the function signature, so a call site can
// no longer pair a valid key with the wrong type (or vice versa) — e.g.
// Get[*plugin.EventBus](c, KeyConfig) type-checks today and panics on the
// type assertion below; the typed accessor makes that pairing impossible
// to express. Misspelling the key itself was ALREADY a compile error
// before this change, via the Key* constants in keys.go — every existing
// call site already passed one of those, not a bare string literal, so
// that part of the safety story is unchanged. They live in the owning
// package (internal/config, internal/plugin), not here — this package is
// a dependency of theirs, so importing them back here would be a cycle.
// Get[T] stays the mechanism for every service without one, including
// "store" (KeyStore), which is deliberately consumed through many
// different narrowed interface types and so has no single accessor to
// offer.
func Get[T any](c *Container, name string) T {
	if c.activeBuilder != "" {
		def := registered[c.activeBuilder]
		if !slices.Contains(def.Needs, name) {
			panic(fmt.Sprintf(
				"serviceregistry: service %q called Get[%T](%q) but %q is not in its Needs",
				c.activeBuilder, *new(T), name, name))
		}
	}
	instance, ok := c.built[name]
	if !ok {
		panic(fmt.Sprintf("serviceregistry: service %q not built (called from %q)",
			name, c.activeBuilder))
	}
	typed, ok := instance.(T)
	if !ok {
		panic(fmt.Sprintf("serviceregistry: service %q is %T, not %T",
			name, instance, *new(T)))
	}
	return typed
}

// TryGet is the non-panicking variant for optional deps. Returns
// (zero, false) on missing service or type mismatch. Bypasses the
// Needs check (callers know they may not have declared the dep).
func TryGet[T any](c *Container, name string) (T, bool) {
	var zero T
	instance, ok := c.built[name]
	if !ok {
		return zero, false
	}
	typed, ok := instance.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}
