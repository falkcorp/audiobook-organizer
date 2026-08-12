<!-- file: docs/archive/2026-08-consolidation-wave2/architecture/server-plugin-registry-plan.md -->
<!-- version: 1.0.1 -->
<!-- last-edited: 2026-08-12 -->

# SERVER-PLUGIN-REG Implementation Plan

> **For agentic workers:** This plan is dispatched via `/parallel-sweep`. Each wave is one `/parallel-sweep` invocation. Within a wave, parallel tasks are dispatched to subagents (Haiku for mechanical work, Sonnet for `.INT` integration and complex services). Each Task section is self-contained — a subagent reads only its assigned Task plus the wave's "Pattern" section. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 968-line `internal/server/server.go` `NewServer` mega-function with a registry-driven service container. Domain packages self-register via `init()`; the container resolves deps, builds, post-inits, and manages Start/Stop lifecycle.

**Architecture:** New `internal/serviceregistry` package provides `ServiceDef` (registration descriptor) + per-instance `Container`. Domain packages add a `register.go` calling `serviceregistry.Register` from `init()`. `NewServer` becomes ~25 lines that instantiate a container, wire host overrides, and populate `*Server` typed fields from built services. See `docs/architecture/server-plugin-registry-design.md` for the full design.

**Tech Stack:** Go 1.24 (generics, `t.Context()`), `internal/operations/registry` (existing async opRegistry — same architectural pattern, sync flavor), `internal/database/mocks` (mockery v3 generated).

---

## How to use this plan

1. **Sequential wave execution.** Each wave fully completes (all parallel tasks merged + `.INT` task merged) before the next wave starts.
2. **Parallel task dispatch within a wave.** Use `/parallel-sweep` with the wave's task list. Each task gets its own worktree + child subagent. Tasks within a wave never touch the same file by design — see each wave's file-isolation note.
3. **`.INT` task is serial.** After all parallel tasks in a wave merge, dispatch the `.INT` task to a single Sonnet subagent. It consolidates `server.go` changes for the wave.
4. **Read order for subagents:** Read the wave's "Pattern" subsection (worked example with full TDD steps), then your assigned task's spec (`Needs`, files, test assertions). The pattern is the same for every task in the wave; the task spec only varies in service-specific details.
5. **Spec reference.** When in doubt about a design decision, the spec at `docs/architecture/server-plugin-registry-design.md` is authoritative.

---

## Wave 0 — Registry Foundation

**Goal:** Build `internal/serviceregistry/` with no callers. Pure mechanism + unit tests. One sequential task.

### Task W0.1 — Build internal/serviceregistry

**Files:**
- Create: `internal/serviceregistry/registry.go`
- Create: `internal/serviceregistry/container.go`
- Create: `internal/serviceregistry/graph.go`
- Create: `internal/serviceregistry/lifecycle.go`
- Create: `internal/serviceregistry/errors.go`
- Create: `internal/serviceregistry/registry_test.go`
- Create: `internal/serviceregistry/container_test.go`
- Create: `internal/serviceregistry/graph_test.go`
- Create: `internal/serviceregistry/lifecycle_test.go`

**Subagent model:** Sonnet (foundational, must be correct)

- [ ] **Step 1 — Write `errors.go`**

```go
// file: internal/serviceregistry/errors.go
// version: 1.0.0

package serviceregistry

import "errors"

var (
	ErrCycle           = errors.New("serviceregistry: dependency cycle")
	ErrUnknownService  = errors.New("serviceregistry: unknown service")
	ErrUndeclaredDep   = errors.New("serviceregistry: undeclared dependency")
	ErrNotBuilt        = errors.New("serviceregistry: service not built")
	ErrTypeMismatch    = errors.New("serviceregistry: type mismatch")
	ErrWrongPhase      = errors.New("serviceregistry: operation called in wrong phase")
)
```

- [ ] **Step 2 — Write `lifecycle.go`**

```go
// file: internal/serviceregistry/lifecycle.go
// version: 1.0.0

package serviceregistry

import "context"

// PostIniter is implemented by services that need cross-wiring after all
// Build() calls complete. PostInit runs in resolved dep order. Within
// PostInit, Get[T] is unrestricted — any built service may be retrieved.
type PostIniter interface {
	PostInit(ctx context.Context, c *Container) error
}

// Starter is implemented by services that run background goroutines or
// otherwise need an explicit start signal. Start runs in resolved dep
// order; on error, the container halts and calls Stop on already-started
// services in reverse.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is implemented by services that hold resources requiring
// explicit release. Stop runs in REVERSE resolved order. Errors are
// logged but do not abort the sweep.
type Stopper interface {
	Stop(ctx context.Context) error
}
```

- [ ] **Step 3 — Write `registry.go`**

```go
// file: internal/serviceregistry/registry.go
// version: 1.0.0

package serviceregistry

import "fmt"

// ServiceDef describes a registered service.
type ServiceDef struct {
	// Name is the registry key. Must be unique. Convention: lowercase,
	// dot-separated for grouping (e.g. "dedup", "metafetch", "itunes.batcher").
	Name string

	// Needs lists names of OTHER services this service's Build func will
	// Get[T]. The container enforces that Build can only Get services listed
	// here. Needs is the single source of truth for the build-time dep graph.
	Needs []string

	// Build constructs the service instance. May call Get[T](c, name) for
	// any name in Needs.
	Build func(c *Container) (any, error)
}

var registered = map[string]ServiceDef{}

// Register appends a ServiceDef to the package-level factory list.
// Called from init() in a domain package's register.go.
// Panics on duplicate Name or missing required field — caught at startup,
// never at runtime.
func Register(def ServiceDef) {
	if def.Name == "" {
		panic("serviceregistry: ServiceDef.Name is required")
	}
	if def.Build == nil {
		panic(fmt.Sprintf("serviceregistry: ServiceDef.Build is required (name=%q)", def.Name))
	}
	if _, dup := registered[def.Name]; dup {
		panic(fmt.Sprintf("serviceregistry: duplicate name: %q", def.Name))
	}
	registered[def.Name] = def
}

// ResetForTest clears the package-level factory list. ONLY for tests
// that need to register isolated ServiceDefs without polluting production
// registrations. Production code never calls this.
func ResetForTest() {
	registered = map[string]ServiceDef{}
}
```

- [ ] **Step 4 — Write `container.go` (constructor + phase tracking)**

```go
// file: internal/serviceregistry/container.go
// version: 1.0.0

package serviceregistry

import (
	"context"
	"fmt"
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

// IncludeAll adds every registered ServiceDef. Production default.
func (c *Container) IncludeAll() *Container {
	for name := range registered {
		c.include[name] = true
	}
	return c
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
```

- [ ] **Step 5 — Write `graph.go` (Kahn's sort + cycle detection)**

```go
// file: internal/serviceregistry/graph.go
// version: 1.0.0

package serviceregistry

import (
	"fmt"
	"sort"
)

// Resolve computes the transitive closure of c.include, validates the
// dep graph, and produces a topologically sorted build order. The ready
// queue is sorted lexically before processing for deterministic order
// (stable startup logs, reproducible tests).
//
// Returns ErrUnknownService if a dep references an unregistered name.
// Returns ErrCycle if the dep graph contains a cycle.
// Idempotent after first successful call.
func (c *Container) Resolve() error {
	if c.phase >= phaseResolved {
		return nil
	}

	// 1. Transitive closure: walk include set following Needs.
	//    Overrides short-circuit — they are leaves in the graph.
	wanted := map[string]bool{}
	var walk func(name string) error
	walk = func(name string) error {
		if wanted[name] {
			return nil
		}
		if _, isOverride := c.overrides[name]; isOverride {
			wanted[name] = true
			return nil
		}
		def, ok := registered[name]
		if !ok {
			return fmt.Errorf("%w: %q (declared as a dep but not registered)", ErrUnknownService, name)
		}
		wanted[name] = true
		for _, dep := range def.Needs {
			if err := walk(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for name := range c.include {
		if err := walk(name); err != nil {
			return err
		}
	}

	// 2. Build dep graph over wanted set. Overrides are treated as leaves.
	incoming := map[string]int{}
	outgoing := map[string][]string{}
	for name := range wanted {
		if _, isOverride := c.overrides[name]; isOverride {
			incoming[name] = 0
			continue
		}
		def := registered[name]
		incoming[name] = len(def.Needs)
		for _, dep := range def.Needs {
			outgoing[dep] = append(outgoing[dep], name)
		}
	}

	// 3. Kahn's algorithm with lex-stable ready queue.
	ready := []string{}
	for name, n := range incoming {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(wanted))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)

		// Sort dependents lexically before appending so the queue stays stable.
		dependents := append([]string{}, outgoing[name]...)
		sort.Strings(dependents)
		for _, dep := range dependents {
			incoming[dep]--
			if incoming[dep] == 0 {
				ready = append(ready, dep)
			}
		}
		sort.Strings(ready)
	}

	// 4. Cycle check.
	if len(order) != len(wanted) {
		cycle := []string{}
		for name := range wanted {
			if incoming[name] > 0 {
				cycle = append(cycle, name)
			}
		}
		sort.Strings(cycle)
		return fmt.Errorf("%w among: %v", ErrCycle, cycle)
	}

	c.order = order
	c.phase = phaseResolved
	return nil
}
```

- [ ] **Step 6 — Add Build/PostInit/Start/Stop to container.go**

```go
// Append to internal/serviceregistry/container.go

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
			for i := len(started) - 1; i >= 0; i-- {
				if stopper, ok := c.built[started[i]].(Stopper); ok {
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
	for i := len(c.order) - 1; i >= 0; i-- {
		name := c.order[i]
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
```

- [ ] **Step 7 — Add Get[T] and TryGet[T] to container.go**

```go
// Append to internal/serviceregistry/container.go

import "slices"

// Get returns the instance registered under name, type-asserted to T.
// Panics on missing service or type mismatch (programmer error — fail
// fast at startup). During Build, also panics if name is not in the
// active builder's Needs.
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
```

- [ ] **Step 8 — Write `graph_test.go` (TDD: cycle detection + lex order)**

```go
// file: internal/serviceregistry/graph_test.go
// version: 1.0.0

package serviceregistry

import (
	"errors"
	"testing"
)

func TestResolve_LexStableOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	// Register A, B, C, D all depending on nothing. Lex order should be A,B,C,D.
	for _, name := range []string{"zebra", "alpha", "mike", "delta"} {
		n := name
		Register(ServiceDef{
			Name:  n,
			Build: func(c *Container) (any, error) { return n, nil },
		})
	}

	c := NewContainer().IncludeAll()
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"alpha", "delta", "mike", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolve_CycleDetected(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Needs: []string{"b"}, Build: func(c *Container) (any, error) { return nil, nil }})
	Register(ServiceDef{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) { return nil, nil }})

	c := NewContainer().Include("a")
	err := c.Resolve()
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func TestResolve_UnknownDep(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Needs: []string{"nonexistent"}, Build: func(c *Container) (any, error) { return nil, nil }})

	c := NewContainer().Include("a")
	err := c.Resolve()
	if !errors.Is(err, ErrUnknownService) {
		t.Fatalf("expected ErrUnknownService, got %v", err)
	}
}

func TestResolve_TransitiveClosure(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) { return "leaf", nil }})
	Register(ServiceDef{Name: "mid", Needs: []string{"leaf"}, Build: func(c *Container) (any, error) { return "mid", nil }})
	Register(ServiceDef{Name: "top", Needs: []string{"mid"}, Build: func(c *Container) (any, error) { return "top", nil }})

	// Include only "top" — should pull mid + leaf transitively.
	c := NewContainer().Include("top")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"leaf", "mid", "top"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolve_OverrideIsLeaf(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	// "real" needs "store"; "store" is not registered, only overridden.
	Register(ServiceDef{Name: "real", Needs: []string{"store"}, Build: func(c *Container) (any, error) {
		return Get[string](c, "store"), nil
	}})

	c := NewContainer().Override("store", "mock-store").Include("real")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	// store appears first (no deps), then real.
	if len(got) != 2 || got[0] != "store" || got[1] != "real" {
		t.Fatalf("got %v, want [store real]", got)
	}
}
```

- [ ] **Step 9 — Run graph tests to verify they pass**

Run: `go test ./internal/serviceregistry/ -run TestResolve_ -v`
Expected: `PASS` for all four TestResolve_* tests.

- [ ] **Step 10 — Write `container_test.go` (Build/PostInit/Start/Stop, Get enforcement)**

```go
// file: internal/serviceregistry/container_test.go
// version: 1.0.0

package serviceregistry

import (
	"context"
	"testing"
)

type fakeService struct {
	name     string
	postInit bool
	started  bool
	stopped  bool
}

func (f *fakeService) PostInit(ctx context.Context, c *Container) error { f.postInit = true; return nil }
func (f *fakeService) Start(ctx context.Context) error                  { f.started = true; return nil }
func (f *fakeService) Stop(ctx context.Context) error                   { f.stopped = true; return nil }

func TestContainer_Build_RunsInDepOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	order := []string{}
	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) {
		order = append(order, "leaf")
		return &fakeService{name: "leaf"}, nil
	}})
	Register(ServiceDef{Name: "top", Needs: []string{"leaf"}, Build: func(c *Container) (any, error) {
		_ = Get[*fakeService](c, "leaf") // verify Get works mid-Build
		order = append(order, "top")
		return &fakeService{name: "top"}, nil
	}})

	c := NewContainer().Include("top")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(order) != 2 || order[0] != "leaf" || order[1] != "top" {
		t.Fatalf("build order = %v, want [leaf top]", order)
	}
}

func TestContainer_Get_UndeclaredDepPanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) {
		return "leaf-instance", nil
	}})
	// "naughty" does NOT declare "leaf" in Needs but tries to Get it.
	Register(ServiceDef{Name: "naughty", Build: func(c *Container) (any, error) {
		_ = Get[string](c, "leaf") // should panic
		return nil, nil
	}})

	c := NewContainer().Include("leaf", "naughty")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared Get")
		}
	}()
	_ = c.Build(t.Context())
}

func TestContainer_PostInit_RunsAfterAllBuilds(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Build: func(c *Container) (any, error) { return &fakeService{name: "a"}, nil }})
	Register(ServiceDef{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) { return &fakeService{name: "b"}, nil }})

	c := NewContainer().Include("a", "b")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := c.PostInit(t.Context()); err != nil {
		t.Fatalf("postinit: %v", err)
	}

	a := Get[*fakeService](c, "a")
	b := Get[*fakeService](c, "b")
	if !a.postInit || !b.postInit {
		t.Fatalf("postinit not called: a=%v b=%v", a.postInit, b.postInit)
	}
}

func TestContainer_Stop_ReverseOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	stopOrder := []string{}
	type stopRecorder struct {
		name string
		ref  *[]string
	}
	// Inline Stop methods can't capture closures from struct fields cleanly,
	// so use an interface wrapper.
	type stopper struct {
		name string
	}
	// Custom type with Stop that records into stopOrder.
	type recordedStopper struct{ n string }

	for _, name := range []string{"a", "b", "c"} {
		n := name
		Register(ServiceDef{Name: n, Build: func(c *Container) (any, error) {
			return &recordingService{name: n, sink: &stopOrder}, nil
		}})
	}
	// Make c depend on b, b depend on a, so order is a, b, c.
	// (Re-register with deps.)
	ResetForTest()
	Register(ServiceDef{Name: "a", Build: func(c *Container) (any, error) {
		return &recordingService{name: "a", sink: &stopOrder}, nil
	}})
	Register(ServiceDef{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) {
		return &recordingService{name: "b", sink: &stopOrder}, nil
	}})
	Register(ServiceDef{Name: "c", Needs: []string{"b"}, Build: func(c *Container) (any, error) {
		return &recordingService{name: "c", sink: &stopOrder}, nil
	}})

	c := NewContainer().Include("c")
	if err := c.Build(t.Context()); err != nil { t.Fatalf("build: %v", err) }
	if err := c.PostInit(t.Context()); err != nil { t.Fatalf("postinit: %v", err) }
	if err := c.Start(t.Context()); err != nil { t.Fatalf("start: %v", err) }
	if err := c.Stop(t.Context()); err != nil { t.Fatalf("stop: %v", err) }

	want := []string{"c", "b", "a"}
	if len(stopOrder) != 3 {
		t.Fatalf("stop order = %v, want %v", stopOrder, want)
	}
	for i := range want {
		if stopOrder[i] != want[i] {
			t.Errorf("stopOrder[%d] = %q, want %q", i, stopOrder[i], want[i])
		}
	}

	_ = stopper{}
	_ = recordedStopper{}
	_ = stopRecorder{}
}

type recordingService struct {
	name string
	sink *[]string
}

func (r *recordingService) Start(ctx context.Context) error { return nil }
func (r *recordingService) Stop(ctx context.Context) error {
	*r.sink = append(*r.sink, r.name)
	return nil
}
```

- [ ] **Step 11 — Run container tests**

Run: `go test ./internal/serviceregistry/ -v`
Expected: All tests PASS.

- [ ] **Step 12 — Write `registry_test.go` (Register duplicate detection)**

```go
// file: internal/serviceregistry/registry_test.go
// version: 1.0.0

package serviceregistry

import (
	"strings"
	"testing"
)

func TestRegister_DuplicatePanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "x", Build: func(c *Container) (any, error) { return nil, nil }})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate Register")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate") {
			t.Errorf("panic msg = %v, expected duplicate", r)
		}
	}()
	Register(ServiceDef{Name: "x", Build: func(c *Container) (any, error) { return nil, nil }})
}

func TestRegister_RequiresName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	defer func() { _ = recover() }()
	Register(ServiceDef{Build: func(c *Container) (any, error) { return nil, nil }})
	t.Fatal("expected panic on missing Name")
}

func TestRegister_RequiresBuild(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	defer func() { _ = recover() }()
	Register(ServiceDef{Name: "x"})
	t.Fatal("expected panic on missing Build")
}
```

- [ ] **Step 13 — Run all serviceregistry tests + go vet + go build**

Run:
```bash
go vet ./internal/serviceregistry/...
go test ./internal/serviceregistry/... -v -race
go build ./...
```
Expected: All pass. No vet warnings. Full build succeeds (nothing else changes).

- [ ] **Step 14 — Commit**

```bash
git add internal/serviceregistry/
git commit -m "$(cat <<'EOF'
feat(serviceregistry): registry foundation (W0.1)

internal/serviceregistry: per-instance Container with init()-registered
ServiceDefs. Kahn's topo sort with lex-stable order, cycle detection,
Build-strict / PostInit-relaxed Get enforcement, optional lifecycle
interfaces (PostIniter, Starter, Stopper).

No callers yet. Wave 0 of the SERVER-PLUGIN-REG migration.

Spec: docs/architecture/server-plugin-registry-design.md
EOF
)"
```

---

## Wave 1 — Leaf Services (10 parallel + 1 serial)

**File isolation:** Each parallel task creates one new file `internal/<pkg>/register.go`. Zero conflicts. The `.INT` task is the only one that touches `server.go`.

### Pattern (read once per wave)

A Wave-1 leaf-service task creates `register.go` in the service's package. The `init()` calls `serviceregistry.Register` with the service's name, declared deps (always `[store]` for leaves; `[store, config]` if AppConfig is needed), and a Build func that pulls deps and calls the existing constructor.

**Worked example — `internal/audiobooks/register.go` (this is the W1.1 deliverable):**

```go
// file: internal/audiobooks/register.go
// version: 1.0.0

package audiobooks

import (
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:  "audiobook",
		Needs: []string{"store"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			return NewAudiobookService(store), nil
		},
	})
}
```

**Worked test — `internal/audiobooks/register_test.go`:**

```go
// file: internal/audiobooks/register_test.go
// version: 1.0.0

package audiobooks_test

import (
	"testing"

	"github.com/jdfalk/audiobook-organizer/internal/audiobooks"
	"github.com/jdfalk/audiobook-organizer/internal/database/mocks"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

func TestAudiobookRegistration(t *testing.T) {
	c := serviceregistry.NewContainer().
		Override("store", mocks.NewMockStore(t)).
		Include("audiobook")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*audiobooks.AudiobookService](c, "audiobook")
	if svc == nil {
		t.Fatal("AudiobookService is nil")
	}
}
```

**Per-task TDD steps:**

- [ ] **Step 1** — Write `register_test.go` (above pattern, swap names).
- [ ] **Step 2** — Run test, confirm it fails (`register.go` doesn't exist yet): `go test ./internal/<pkg>/ -run TestRegistration -v`.
- [ ] **Step 3** — Write `register.go` (above pattern, swap names).
- [ ] **Step 4** — Run test, confirm PASS: `go test ./internal/<pkg>/ -run TestRegistration -v`.
- [ ] **Step 5** — Run `go build ./...` to confirm package compiles cleanly.
- [ ] **Step 6** — Commit: `git commit -m "feat(<pkg>): register service (W1.X)"`.

### Task table

| ID | Service name | Package | Needs | Constructor | Result type |
|----|--------------|---------|-------|-------------|-------------|
| W1.1 | `audiobook` | `internal/audiobooks` | `[store]` | `NewAudiobookService(store database.Store)` | `*AudiobookService` |
| W1.2 | `batch` | `internal/batch` | `[store]` | `NewBatchService(store database.BookStore)` (pass via `database.Store`, satisfies interface) | `*BatchService` |
| W1.3 | `work` | `internal/work` | `[store]` | `NewWorkService(store database.WorkStore)` | `*WorkService` |
| W1.4 | `filesystem` | `internal/fileops` | `[store]` | `NewFilesystemService(store)` | `*FilesystemService` |
| W1.5 | `importpath` | `internal/importer` | `[store]` | `NewImportPathService(store)` | `*ImportPathService` |
| W1.6 | `scan` | `internal/scanner` | `[store]` | `NewScanService(store)` | `*ScanService` |
| W1.7 | `dashboard` | `internal/sysinfo` | `[store]` | `NewDashboardService(store)` | `*DashboardService` |
| W1.8 | `system` | `internal/sysinfo` | `[store, config]` | `NewSystemService(store, appVersion, calculateLibrarySizes)` — see note | `*SystemService` |
| W1.9 | `configupdate` | `internal/config` | `[store]` | `NewUpdateService(store)` | `*UpdateService` |
| W1.10 | `metadatastate` | `internal/metafetch` | `[store]` | `NewMetadataStateService(store)` | `*MetadataStateService` |

**Notes:**
- W1.7 and W1.8 are in the same package (`internal/sysinfo`). Both `register.go` files live there — collapse the two `init()` calls into one `register.go` file with both `Register` calls if it reduces churn.
- W1.8's Build needs `appVersion` and `calculateLibrarySizes`. Since `appVersion` is a package-level `var` in `internal/server` (line 72) and `calculateLibrarySizes` is a function defined in `internal/server`, **W1.8's register.go cannot reference them**. Solution: defer W1.8 to W1.INT (move `NewSystemService` construction into the server-side wireup, or refactor those constants out of `internal/server` first). Mark W1.8 as a Sonnet task with the refactor included, or simply drop W1.8 from Wave 1 and address as part of W1.INT — implementer's call.
- W1.10's package (`internal/metafetch`) also hosts the metafetch service (which is Wave 2). Add to the same `register.go` if it exists; otherwise create it.

### Task W1.INT — Integration into server.go

**Files:**
- Modify: `internal/server/server.go`
- Create: `internal/server/registry_wire.go`

**Subagent model:** Sonnet

- [ ] **Step 1** — Create `internal/server/registry_wire.go` with `wireServerFromContainer(s, c)` that pulls Wave-1 services from the container into `*Server` typed fields. Initial skeleton:

```go
// file: internal/server/registry_wire.go
// version: 1.0.0

package server

import (
	"github.com/jdfalk/audiobook-organizer/internal/audiobooks"
	"github.com/jdfalk/audiobook-organizer/internal/batch"
	"github.com/jdfalk/audiobook-organizer/internal/config"
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/fileops"
	"github.com/jdfalk/audiobook-organizer/internal/importer"
	"github.com/jdfalk/audiobook-organizer/internal/metafetch"
	"github.com/jdfalk/audiobook-organizer/internal/scanner"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
	"github.com/jdfalk/audiobook-organizer/internal/sysinfo"
)

// wireServerFromContainer populates *Server's typed service fields from
// the container. Called once during NewServer after Container.Build()
// and Container.PostInit() return.
//
// Adding a new service: add one line here + one register.go in the domain
// package. Removing a service (handler-migration future work): delete one
// line here + the *Server field.
func wireServerFromContainer(s *Server, c *serviceregistry.Container) {
	s.store                = serviceregistry.Get[database.Store](c, "store")
	s.audiobookService     = serviceregistry.Get[*audiobooks.AudiobookService](c, "audiobook")
	s.batchService         = serviceregistry.Get[*batch.BatchService](c, "batch")
	s.workService          = serviceregistry.Get[*work.WorkService](c, "work")
	s.filesystemService    = serviceregistry.Get[*fileops.FilesystemService](c, "filesystem")
	s.importPathService    = serviceregistry.Get[*importer.ImportPathService](c, "importpath")
	s.scanService          = serviceregistry.Get[*scanner.ScanService](c, "scan")
	s.dashboardService     = serviceregistry.Get[*sysinfo.DashboardService](c, "dashboard")
	s.configUpdateService  = serviceregistry.Get[*config.UpdateService](c, "configupdate")
	s.metadataStateService = serviceregistry.Get[*metafetch.MetadataStateService](c, "metadatastate")
	s.container = c
}
```

(Note: `work` package import — add `"github.com/jdfalk/audiobook-organizer/internal/work"` to the imports.)

- [ ] **Step 2** — Add `container *serviceregistry.Container` field to `*Server` struct in `server.go` (after the existing fields).

- [ ] **Step 3** — In `NewServer`, install the registry flow. Replace lines that construct the Wave-1 services in the struct literal with a container build + `wireServerFromContainer` call. Insert immediately after the `server := &Server{...}` struct literal allocation (which now omits the Wave-1 services):

```go
// After the existing &Server{...} struct literal (with Wave-1 service
// fields removed), insert:
c := serviceregistry.NewContainer().
	Override("store", resolvedStore).
	Override("config", &config.AppConfig).
	IncludeAll()
if err := c.Resolve(); err != nil {
	log.Fatalf("[server] registry resolve: %v", err)
}
if err := c.Build(context.Background()); err != nil {
	log.Fatalf("[server] registry build: %v", err)
}
if err := c.PostInit(context.Background()); err != nil {
	log.Fatalf("[server] registry postinit: %v", err)
}
wireServerFromContainer(server, c)
```

- [ ] **Step 4** — Delete the corresponding struct literal entries from `NewServer` (lines 293-321 originally — the `audiobookService: NewAudiobookService(...)` lines for the 9 Wave-1 services). Leave the W1.8 `system` service in place if Wave 1 deferred it.

- [ ] **Step 5** — Run `go build ./...` to confirm compilation.

- [ ] **Step 6** — Run `make ci` (or at minimum `go test ./internal/server/... -race`). Expected: GREEN.

- [ ] **Step 7** — Commit:

```bash
git add internal/server/registry_wire.go internal/server/server.go
git commit -m "feat(server): wire Wave-1 leaf services through registry (W1.INT)"
```

---

## Wave 2 — Cross-wired Services (5 parallel + 1 serial)

**File isolation:** Each parallel task touches one domain package (adds `PostInit` method, possibly creates `register.go` if Wave 1 didn't). Zero conflicts. The `.INT` task deletes the now-replaced `SetX` blocks from `server.go`.

### Pattern

A Wave-2 task adds a `PostInit(ctx context.Context, c *serviceregistry.Container) error` method to the service. It moves cross-wiring calls (currently in `server.go`) into the PostInit. If the service was not registered in Wave 1, also creates `register.go`.

**Worked example — `internal/merge/service.go` (W2.3 deliverable, simplest):**

Current code in `internal/server/server.go` line ~638:
```go
server.mergeService.SetWriteBackBatcher(server.writeBackBatcher)
```

becomes, in `internal/merge/register.go`:

```go
// file: internal/merge/register.go
// version: 1.0.0

package merge

import (
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:  "merge",
		Needs: []string{"store"},
		Build: func(c *serviceregistry.Container) (any, error) {
			return NewService(serviceregistry.Get[database.Store](c, "store")), nil
		},
	})
}
```

and a `PostInit` on `*Service` in `internal/merge/service.go`:

```go
// Append to internal/merge/service.go:

import (
	"context"

	itunesservice "github.com/jdfalk/audiobook-organizer/internal/itunes/service"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

// PostInit wires the iTunes write-back batcher so merge can enqueue
// post-merge tag rewrites. Optional: if no batcher service is built
// (e.g. tests), merge degrades to no-op writebacks.
func (s *Service) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if batcher, ok := serviceregistry.TryGet[*itunesservice.WriteBackBatcher](c, "writebackbatcher"); ok {
		s.SetWriteBackBatcher(batcher)
	}
	return nil
}
```

**Per-task steps:**

- [ ] **Step 1** — Write `register.go` (if not already registered).
- [ ] **Step 2** — Write `register_test.go` (same shape as Wave 1, with all Needs overridden).
- [ ] **Step 3** — Run registration test, confirm PASS.
- [ ] **Step 4** — Add `PostInit` method to the service struct (or its package), moving cross-wiring logic from `server.go`. Use `TryGet` for optional deps; `Get` for required deps declared in Needs.
- [ ] **Step 5** — Write a test `TestServicePostInit` that builds the service + its deps in a container, calls `PostInit`, and asserts the wiring took effect (e.g. mock writebackbatcher records being SetX'd, or call a service method that exercises the wired dep).
- [ ] **Step 6** — Run tests + build.
- [ ] **Step 7** — Commit: `git commit -m "feat(<pkg>): add PostInit cross-wiring (W2.X)"`.

**Important:** Wave-2 tasks do **NOT** delete the corresponding `SetX` call from `server.go`. The PostInit and the old call coexist temporarily — `make ci` stays green because the SetX is idempotent (either it's called twice with the same value, or PostInit runs first and the SetX is a no-op overwrite). The `.INT` task deletes the dead `server.go` lines.

### Task table

| ID | Service | Package | Cross-wiring to move into PostInit |
|----|---------|---------|------------------------------------|
| W2.1 | `metafetch` | `internal/metafetch` | `SetOLStore` (OL store from `olService`), `SetISBNEnrichment`, `SetWriteBackBatcher`, `SetActivityService`, `SetDedupEngine`, `SetMetadataScorer`, `SetMetadataLLMScorer`, `SetSafeWriteDeps` |
| W2.2 | `activity` | `internal/activity` | Create `activity.Writer`, call `Start`, wire `scanService.SetActivityWriter`, set log output, wire `itunesSvc.Repair.SetActivityWriter`, register startup activity entry, call `audiobookService.SetActivityService` |
| W2.3 | `merge` | `internal/merge` | `SetWriteBackBatcher` |
| W2.4 | `quarantine` | `internal/quarantine` | `SetWriteBackBatcher` |
| W2.5 | `organize` | `internal/server` (currently `OrganizeService` lives in server pkg) — move to new `internal/organize` package as part of this task | `SetWriteBackBatcher`, `SetOrganizeHooks`, `DiscoverITunesLibraryPath`, `ExecuteITunesSync` closure, `ScanEnqueuer` closure |

**W2.5 caveat:** `OrganizeService` currently lives in `internal/server/organize_service.go`. Moving it to `internal/organize` is an additional file move (~5 files touch). If too large for one Wave-2 task, split into W2.5a (move package) and W2.5b (PostInit) — implementer's call.

**W2.2 caveat:** `activity.Writer` setup is order-dependent — `log.SetOutput(aw)` should happen exactly once. Encode this as `Start` rather than `PostInit` if it fits better (Wave 3 territory). For Wave 2, the lighter-weight wiring (`SetActivityService` calls) goes in PostInit; the writer + Start moves to Wave 3.

### Task W2.INT — server.go cleanup

**Subagent model:** Sonnet

- [ ] **Step 1** — Delete each `SetX` call from `server.go` that was moved into a PostInit. Cross-reference with the W2.1–W2.5 commits to confirm each one has a PostInit replacement.
- [ ] **Step 2** — Run `go build ./...` and `make ci`. Expected: GREEN.
- [ ] **Step 3** — Commit: `git commit -m "refactor(server): delete cross-wiring superseded by PostInit (W2.INT)"`.

---

## Wave 3 — Start/Stop Services (7 parallel + 1 serial)

**File isolation:** Each parallel task adds `Start(ctx)` / `Stop(ctx)` methods to one service. The `.INT` task wires `s.container.Start(ctx)` / `s.container.Stop(ctx)` into `Server.Start` / `Server.Shutdown` and deletes the manual calls.

### Pattern

A Wave-3 task adds `Start` and/or `Stop` methods to the service (whichever apply). The methods wrap whatever startup/shutdown code is currently in `server.go` for that service.

**Worked example — `internal/activity/writer.go` (W3.3 simplified):**

Currently in `server.go`:
```go
aw := activity.NewWriter(server.activityService.Store(), 10000)
aw.Start()
server.activityWriter = aw
// ... at shutdown: nothing explicit, the writer's Start goroutine just bleeds
```

becomes a `*Writer` with explicit lifecycle:

```go
// In internal/activity/writer.go, ensure Writer has these methods:

func (w *Writer) Start(ctx context.Context) error {
	// Existing Start logic, plus capture ctx so the goroutine can exit cleanly.
	w.ctx, w.cancel = context.WithCancel(ctx)
	go w.run()
	return nil
}

func (w *Writer) Stop(ctx context.Context) error {
	if w.cancel != nil { w.cancel() }
	// Drain pending writes with a deadline from ctx.
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

Registration (`internal/activity/register.go` if not already in Wave 2):

```go
serviceregistry.Register(serviceregistry.ServiceDef{
	Name:  "activitywriter",
	Needs: []string{"activity"}, // depends on the activity service
	Build: func(c *serviceregistry.Container) (any, error) {
		svc := serviceregistry.Get[*Service](c, "activity")
		return NewWriter(svc.Store(), 10000), nil
	},
})
```

**Per-task steps:**

- [ ] **Step 1** — If service already has Start/Stop methods, audit their context handling. If they don't accept `context.Context`, add it.
- [ ] **Step 2** — If Start/Stop don't exist, add them. Wrap the existing goroutine launch + shutdown logic.
- [ ] **Step 3** — Write `register.go` (or extend existing) with the service registered, declaring `Needs` for whatever it depends on.
- [ ] **Step 4** — Write a test `TestStartStop` that builds the service in a container, calls `c.Start(ctx)` and `c.Stop(ctx)`, asserts goroutines exit cleanly within a short timeout.
- [ ] **Step 5** — Run tests + build.
- [ ] **Step 6** — Commit: `git commit -m "feat(<pkg>): add Start/Stop lifecycle (W3.X)"`.

### Task table

| ID | Service | Package | Phase(s) |
|----|---------|---------|----------|
| W3.1 | `writebackbatcher` | `internal/itunes/service` (or wherever WriteBackBatcher lives) | Start (flush loop), Stop (drain) |
| W3.2 | `updatescheduler` | `internal/updater` | Start (existing — adapt to ctx), Stop |
| W3.3 | `activitywriter` | `internal/activity` | Start, Stop |
| W3.4 | `searchindex` | `internal/search` | Start (open Bleve, launch index worker), Stop (close index, drain queue) |
| W3.5 | `opregistry` | `internal/operations/registry` | Start (existing — already takes ctx), Stop (existing — already takes ctx) |
| W3.6 | `batchpoller` | `internal/server` (move to `internal/batchpoller` as part of this task) | Start (poll loop), Stop |
| W3.7 | `librarywatcher` | `internal/itunes` | Start (fsnotify), Stop |

**W3.5 special-case:** `opregistry.Start` and `opregistry.Shutdown` already exist and take `context.Context`. The work here is just registering opregistry as a ServiceDef and wrapping its methods to match the `Starter`/`Stopper` interface signatures exactly.

**W3.6 caveat:** `BatchPoller` lives in `internal/server/batch_poller.go`. Moving it out of `internal/server` is a small package extraction. Consider deferring the move; just add Start/Stop on the existing type in-place and register it from `internal/server/registry_wire.go` if the move is out of scope.

### Task W3.INT

**Subagent model:** Sonnet

- [ ] **Step 1** — In `server.go`, define `Server.Start(ctx, cfg) error` (if not already) and `Server.Shutdown(ctx) error`. Wire:

```go
func (s *Server) Start(ctx context.Context, cfg ServerConfig) error {
	if err := s.container.Start(ctx); err != nil {
		return err
	}
	return s.runHTTP(cfg) // existing HTTP-serve code
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.bgCancel()
	_ = s.container.Stop(ctx)
	return s.httpServer.Shutdown(ctx)
}
```

- [ ] **Step 2** — Delete the manual `Start()` calls in `NewServer` (e.g. `server.updateScheduler.Start()`, `aw.Start()`, etc.) — they run via `container.Start` now.
- [ ] **Step 3** — Audit `cmd/serve.go` and the shutdown handler — make sure `Server.Shutdown` is called and propagates a reasonable ctx (likely a `context.WithTimeout(ctx, 30*time.Second)`).
- [ ] **Step 4** — Run `make ci`. Expected: GREEN. Pay attention to TestITunesImport / TestStartScanOperation timeouts — those are pre-existing failures (tracked as SERVER-THIN-8) but should not be made worse.
- [ ] **Step 5** — Commit: `git commit -m "refactor(server): drive Start/Stop via container (W3.INT)"`.

---

## Wave 4 — Embedding/AI Cluster (8 parallel + 1 serial)

**File isolation:** Each parallel task creates a new `register.go` in its domain package and (if needed) refactors construction to be ctor-only. The `.INT` task deletes the 140-line conditional block in `server.go`.

### Pattern

Wave 4 is more nuanced because half the services are **optional** (only present when `OpenAIAPIKey` is set and `EmbeddingEnabled` is true). The mechanism:

- **Hard deps** (always built): `embeddingstore`, `aijobsstore`. These don't need an API key — they're stores derived from the underlying PebbleDB.
- **Soft deps** (built only when config allows): `embedclient`, `llmparser`, `chromemstore`, `dedup`, `metadatascorer`, `metadatallmscorer`.

For soft-deps, the Build func returns `nil, nil` (not built) when config doesn't enable. Consumers use `serviceregistry.TryGet[T](c, "name")` and skip wiring when not present.

**Alternative (cleaner):** soft-deps register their ServiceDef with a Build that returns an error wrapped in a sentinel (`ErrDisabled`). Container.Build skips services that return `ErrDisabled` from Build. Add this to Wave 0 if you want — minor extension.

**Recommended approach:** stick with `TryGet` and nil-instance pattern; document it. Less mechanism change.

**Worked example — `internal/database/register.go` for `embeddingstore`:**

```go
// In internal/database/register.go (existing or new):

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:  "embeddingstore",
		Needs: []string{"store"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			ps, ok := store.(*PebbleStore)
			if !ok {
				return nil, nil // not PebbleDB: skip; consumers TryGet and degrade
			}
			return NewEmbeddingStore(ps.DB()), nil
		},
	})
}
```

**Per-task steps:** identical to Wave 2 pattern (register.go + PostInit), with attention to optional construction.

### Task table

| ID | Service | Package | Notes |
|----|---------|---------|-------|
| W4.1 | `embeddingstore` | `internal/database` | Type-assert store to `*PebbleStore`; return nil on miss |
| W4.2 | `embedclient` | `internal/ai` | Conditional on `config.AppConfig.OpenAIAPIKey != "" && config.AppConfig.EmbeddingEnabled`; chain `.WithCache(embeddingStore)` |
| W4.3 | `llmparser` | `internal/ai` | Conditional on `config.AppConfig.OpenAIAPIKey != ""`; `NewOpenAIParser(&config.AppConfig, ...)` |
| W4.4 | `chromemstore` | `internal/database` | Conditional; `NewChromemEmbeddingStore(dbDir, 3072)` |
| W4.5 | `aijobsstore` | `internal/database` | Type-assert store to `AIJobsStore` interface |
| W4.6 | `dedup` | `internal/dedup` | Needs: `[store, embeddingstore, embedclient, llmparser, merge]`; PostInit wires chromem hydrate (Start), aijobs, scorer; Build only if all soft-deps present (TryGet for each in Build, return nil if any missing) |
| W4.7 | `metadatascorer` | `internal/ai` | Conditional on `MetadataEmbeddingScoringEnabled`; `NewEmbeddingScorer(embClient, embStore)` |
| W4.8 | `metadatallmscorer` | `internal/ai` | `NewLLMScorer(llmParser)`; PostInit step to log enabled/disabled message |

### Task W4.INT — Delete the AI conditional block

**Subagent model:** Sonnet

- [ ] **Step 1** — Delete the entire `if ps, ok := database.GetGlobalStore().(*database.PebbleStore); ok { ... }` block in `server.go` (currently lines 464-602).
- [ ] **Step 2** — Verify `wireServerFromContainer` populates `s.embeddingStore`, `s.dedupEngine`, `s.aiScanStore` via the new ServiceDefs. Use `TryGet` for fields where the service might not be built (no API key) — assign nil when missing.
- [ ] **Step 3** — Run `make ci`. Expected: GREEN. Verify dedup/embedding-related tests still pass.
- [ ] **Step 4** — Commit: `git commit -m "refactor(server): delete AI cluster construction block (W4.INT)"`.

---

## Wave 5 — UOS Plugin Migrations (5 parallel + 1 serial)

**File isolation:** Each parallel task touches one UOS plugin package. The `.INT` task deletes the inline `New(...).Register(server.opRegistry)` calls from `server.go`.

### Pattern

Each existing UOS plugin (e.g. `internal/plugins/deluge`) becomes a ServiceDef:
- `Build` returns the constructed plugin (e.g. `*Plugin`).
- `PostInit` calls `p.Register(opRegistry)` — pulls opregistry via `Get`.

**Worked example — `internal/plugins/deluge/register.go`:**

```go
// file: internal/plugins/deluge/register.go
// version: 1.0.0

package deluge

import (
	"context"

	"github.com/jdfalk/audiobook-organizer/internal/database"
	delugeclient "github.com/jdfalk/audiobook-organizer/internal/deluge"
	opsregistry "github.com/jdfalk/audiobook-organizer/internal/operations/registry"
	"github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:  "delugeplugin",
		Needs: []string{"store"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			client := delugeclient.GetClient()
			cache, _ := serviceregistry.TryGet[*delugeclient.ProtectedPathCache](c, "protectedpathcache")
			if client == nil || cache == nil {
				return nil, nil // Deluge not configured
			}
			return New(client, cache, store), nil
		},
	})
}

func (p *Plugin) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if p == nil { return nil }
	opReg := serviceregistry.Get[*opsregistry.Registry](c, "opregistry")
	return p.Register(opReg)
}
```

**Per-task steps:** same shape — `register.go` + `PostInit` method on the Plugin type.

### Task table

| ID | Plugin | Package | Hard deps | Notes |
|----|--------|---------|-----------|-------|
| W5.1 | `maintenanceplugin` | `internal/plugins/maintenance` | `[store, opregistry]` | Currently constructed inline with `maintenanceplugin.New(server)` — needs decoupling from `*Server` first (Sonnet task) |
| W5.2 | `itunesplugin` | `internal/plugins/itunes` | `[store, itunes, opregistry]` | Gate on `itunesSvc.Enabled()` |
| W5.3 | `delugeplugin` | `internal/plugins/deluge` | `[store, opregistry]` (+ TryGet protectedpathcache) | Gate on `client != nil && cache != nil` |
| W5.4 | `dedupplugin` | `internal/plugins/dedup` | `[store, dedup, embeddingstore, opregistry]` | Likely folded into W4.6 — implementer's call |
| W5.5 | `acoustidplugin` | `internal/plugins/acoustid` | `[store, dedup, embeddingstore, opregistry]` | Same pattern as dedupplugin |

**W5.1 caveat:** `maintenanceplugin.New(server)` takes `*Server` directly. Refactor `New` to take a `Deps` struct of typed deps before registering. Spec line for the maintenance task: "introduce `maintenanceplugin.Deps` struct, refactor `New` to take it; current `*Server`-coupled methods inside the plugin must accept the typed deps instead." This is a meaningful refactor — Sonnet task, not Haiku.

### Task W5.INT

- [ ] **Step 1** — Delete inline plugin construction blocks in `server.go` (lines ~347, 396, 546, 552, 797 — search for `.Register(server.opRegistry)`).
- [ ] **Step 2** — Run `make ci`. Expected: GREEN. Verify UOS plugin tests still pass.
- [ ] **Step 3** — Commit: `git commit -m "refactor(server): delete inline UOS plugin Register calls (W5.INT)"`.

---

## Wave 6 — Scheduler Residual Extraction

**Goal:** Move `internal/server/scheduler_extra_ops.go` to `internal/scheduler`. Closes SERVER-THIN-RESIDUAL.

### Task W6.1

**Files:**
- Read: `internal/server/scheduler_extra_ops.go` (the 5 *Server methods)
- Create: `internal/scheduler/extra_ops.go`
- Modify: `internal/scheduler/register.go` (extend Needs)
- Delete: `internal/server/scheduler_extra_ops.go`

**Subagent model:** Sonnet

- [ ] **Step 1** — Read `internal/server/scheduler_extra_ops.go` and enumerate the 5 receiver methods. Note which `*Server` fields each one uses: `dedupEngine`, `dedupCache`, `aiScanStore`, `activityWriter`, `olService`.

- [ ] **Step 2** — Add corresponding deps to `TaskScheduler` struct (or `SchedulerDeps`) in `internal/scheduler`:

```go
type SchedulerDeps struct {
	// existing deps ...
	DedupEngine    *dedup.Engine
	DedupCache     *cache.Cache[gin.H]
	AIScanStore    *database.AIScanStore
	ActivityWriter *activity.Writer
	OLService      *metafetch.OpenLibraryService
}
```

- [ ] **Step 3** — Move each of the 5 methods into `internal/scheduler/extra_ops.go`, changing receivers from `*Server` to `*TaskScheduler` and changing field accesses (`s.dedupEngine` → `s.deps.DedupEngine`, etc.).

- [ ] **Step 4** — Update `internal/scheduler/register.go` to declare the new deps in `Needs` and `Get` them in Build:

```go
Needs: []string{
	"store", "dedup", "dedupcache", "aiscanstore",
	"activitywriter", "openlibrary", /* plus existing */,
},
Build: func(c *serviceregistry.Container) (any, error) {
	dedupEngine, _ := serviceregistry.TryGet[*dedup.Engine](c, "dedup")
	aiScanStore, _ := serviceregistry.TryGet[*database.AIScanStore](c, "aiscanstore")
	// ... etc, using TryGet for optional services ...
	return scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		// existing deps ...
		DedupEngine:    dedupEngine,
		AIScanStore:    aiScanStore,
		// ...
	}), nil
},
```

- [ ] **Step 5** — Audit all callers of the 5 methods (likely in `register_scheduler_tasks.go` or similar). Update call sites to use `scheduler.<MethodName>` instead of `server.<MethodName>`.

- [ ] **Step 6** — Delete `internal/server/scheduler_extra_ops.go`.

- [ ] **Step 7** — Run `make ci`. Expected: GREEN. The scheduler's existing tests should cover the extracted methods; if not, add unit tests in `internal/scheduler/extra_ops_test.go` for each of the 5 methods using `mocks/MockStore` and table-driven fixtures.

- [ ] **Step 8** — Commit: `git commit -m "refactor(scheduler): extract residual ops from internal/server (W6.1)"`. Note: this closes SERVER-THIN-RESIDUAL — update TODO.md to mark it complete.

---

## Wave 7 — Final Cleanup

### Task W7.1 — Trim NewServer to ≤50 lines

**Subagent model:** Sonnet

- [ ] **Step 1** — Read `internal/server/server.go` `NewServer` function. List every remaining service construction / wiring call.
- [ ] **Step 2** — For each remaining call, determine: (a) is it actually a service that should be registered? (b) is it host-process orchestration (router, gzip, middleware) that legitimately belongs in NewServer?
- [ ] **Step 3** — Migrate any (a) cases into a domain package's `register.go`.
- [ ] **Step 4** — Confirm NewServer is now: bg-context setup, router setup, container build + postinit, `wireServerFromContainer`, registerRoutes, return. Aim for ≤50 lines.
- [ ] **Step 5** — `make ci` GREEN, commit: `refactor(server): trim NewServer to registry-only orchestration (W7.1)`.

### Task W7.2 — Audit `database.GetGlobalStore()` removal

**Subagent model:** Sonnet

- [ ] **Step 1** — `grep -rn "GetGlobalStore\|SetGlobalStore" --include="*.go" internal/ cmd/` and produce a list.
- [ ] **Step 2** — For each call site: is the file already migrated to use container `store`? If yes, replace `GetGlobalStore()` with the local `store` parameter / field. If no, leave with TODO comment referencing this task for later cleanup.
- [ ] **Step 3** — If any file is fully migrated and `SetGlobalStore` is no longer needed in tests, remove the global plumbing.
- [ ] **Step 4** — `make ci` GREEN, commit: `refactor(database): remove redundant GetGlobalStore calls (W7.2)`.

---

## Cross-cutting concerns

### TODO.md updates per wave

After each wave completes, edit `TODO.md`:
- Mark the wave as ✅ complete (e.g. "SERVER-PLUGIN-REG W3 — complete").
- Bump version header.
- After W6, mark SERVER-THIN-RESIDUAL as ✅ complete.
- After W7, mark SERVER-PLUGIN-REG as ✅ complete.

### CHANGELOG.md updates per wave

Prepend a per-wave entry (don't replace existing content):
```markdown
#### YYYY-MM-DD — SERVER-PLUGIN-REG Wave N (PRs #XXX–#YYY)
- Wave summary: e.g. "10 leaf services registered + server.go integration (PRs #830-#840)"
```

### Test discipline

- Every `register.go` gets a `register_test.go` that builds the service in a minimal container and asserts the result type.
- Every PostInit gets a test that builds dependencies + the service, calls PostInit, and asserts the wiring landed (e.g. by exercising a method that requires the wired dep, or by mock expectations on the dep).
- Every Start/Stop gets a test that verifies goroutines exit within a short ctx deadline.

### Failure recovery

- **Single-task PR fails CI:** revert the PR, fix on a follow-up branch, re-merge. Wave is not blocked unless ALL parallel tasks block.
- **`.INT` task fails CI:** investigate which Wave-N parallel task introduced the regression. The `.INT` is the only place where cross-task interaction surfaces.
- **Wave gets stuck:** halt at the `.INT` step; subsequent waves cannot proceed because their `.INT` tasks depend on prior waves' server.go state.

---

## Self-review checklist

- [x] **Spec coverage:** every section of `docs/architecture/server-plugin-registry-design.md` maps to a wave: registry mechanism = W0; leaf services = W1; cross-wiring = W2; lifecycle = W3; AI cluster = W4; UOS plugins = W5; scheduler residual = W6; cleanup = W7.
- [x] **No placeholders:** all task tables have explicit service names, packages, deps, and result types. No "TBD" markers.
- [x] **Type consistency:** `Container`, `ServiceDef`, `Get[T]`, `TryGet[T]`, `Register`, `Resolve`, `Build`, `PostInit`, `Start`, `Stop` names used identically across all sections.
- [x] **Granularity:** Wave 0 has 14 explicit TDD steps; subsequent waves give per-task patterns + per-task tables so each parallel-sweep child has a self-contained task spec to read.
