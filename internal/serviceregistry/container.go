// file: internal/serviceregistry/container.go
// version: 1.4.0
// last-edited: 2026-11-08

package serviceregistry

import (
	"context"
	"fmt"
	"slices"
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
// NewServer() and per test. The factory list is the container's own: it is
// the defs NewContainerFrom was given, so two containers built from different
// lists never see each other's services and no package-level state exists.
type Container struct {
	defs          map[string]ServiceDef
	include       map[string]bool
	overrides     map[string]any
	built         map[string]any
	order         []string
	phase         containerPhase
	activeBuilder string // name of service whose Build is currently running
}

// NewContainerFrom creates a Container whose factory list is exactly defs
// (06 §6.11.1 step 3). Every def passed is in the build set, so there is
// nothing to include by name; Override keeps its meaning. With no defs it is the
// empty container.
//
// Panics on an empty Name, a nil Build or a duplicate Name among defs, so a
// malformed list is caught when the container is made, never at runtime.
func NewContainerFrom(defs ...ServiceDef) *Container {
	c := &Container{
		defs:      make(map[string]ServiceDef, len(defs)),
		include:   make(map[string]bool, len(defs)),
		overrides: map[string]any{},
		built:     map[string]any{},
	}
	for _, def := range defs {
		if def.Name == "" {
			panic("serviceregistry: ServiceDef.Name is required")
		}
		if def.Build == nil {
			panic(fmt.Sprintf("serviceregistry: ServiceDef.Build is required (name=%q)", def.Name))
		}
		if _, dup := c.defs[def.Name]; dup {
			panic(fmt.Sprintf("serviceregistry: duplicate name: %q", def.Name))
		}
		c.defs[def.Name] = def
		c.include[def.Name] = true
	}
	return c
}

// Override substitutes an instance for the named service. Factory Build
// is not called. The name is added to the build set. Treated as a leaf in
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
		def := c.defs[name]
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
		def := c.defs[c.activeBuilder]
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
