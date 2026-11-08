<!-- file: docs/architecture/server-plugin-registry-design.md -->
<!-- version: 1.1.0 -->
<!-- guid: ae3fdc85-331b-425f-9cd5-c587702714b7 -->
<!-- last-edited: 2026-11-08 -->

> **Superseded by 06 §6.11 (R1-34).** This document describes the removed `init()` /
> `serviceregistry.Register` / `IncludeGroup` API. Services are now listed explicitly: each registering
> package exports `ServiceDefs()`, `internal/server`'s `serviceDefs` is the one list, and the container is
> built with `serviceregistry.NewContainerFrom`. See
> `docs/agent-tasks/platform-core/R1-34-service-registry-explicit-list.md`. Kept for history only.

# SERVER-PLUGIN-REG — Service Registry Design

## Status

Brainstormed 2026-05-11. Spec written, awaiting user review before plan handoff.

## Goals (ranked)

1. **Unblock the scheduler residual.** `internal/server/scheduler_extra_ops.go` has 5 `*Server` receiver methods (using `dedupEngine`, `dedupCache`, `aiScanStore`, `activityWriter`, `olService`) that resisted extraction in wave 3 of the server-thinning sweep precisely because they need five sibling services with no clean dependency injection path. The registry is the means; extracting these methods to `internal/scheduler` is the proof.
2. **Decouple `*Server` for testing.** Tests today must stub many `*Server` fields or use the package-global store. A per-instance container lets a test build only the services it exercises, with typed overrides for the rest.
3. **Shrink `internal/server/server.go`.** Currently 968 lines; `NewServer()` itself is ~600 lines of construction + cross-wiring. Target: `NewServer` ≤ 50 lines, all service construction driven by the registry.
4. **Prepare for future plugin extensibility.** No runtime loading is needed (compile-time plugins are fine), but the architecture should support a future where a third-party Go package drops in, calls `serviceregistry.Register` in its `init()`, and is wired automatically. The mechanism that hits goals 1–3 (per-instance container, init() factory list) naturally hits this without additional cost.

**Non-goals:**
- Runtime plugin loading (Go `plugin` package, .so/.dll). Out of scope.
- Reflection-driven DI. Idiomatic Go solutions only.
- HTTP handler migration. Tracked separately as SERVER-HANDLER-MIGRATE (see Future Work). The registry enables it but does not deliver it.

## Architecture overview

Three concepts:

1. **`ServiceDef`** — value describing a service: `Name`, `Needs []string`, `Build func(*Container) (any, error)`. Registered at `init()` time into a **package-level factory list** (global, append-only). Domain packages call `serviceregistry.Register(ServiceDef{...})` from their `init()`.

2. **`Container`** — per-instance state: holds built services keyed by name; tracks lifecycle phase; owns the resolved build order. Created fresh per `NewServer()` and per test. The factory list is global; each `Container` builds its own service instances from it.

3. **Lifecycle phases** run in order across all included services:
   - **Resolve** — compute transitive closure of `Include` set, topological sort (Kahn's, lex-stable), fail on cycle.
   - **Build** — call each `Build` func in dep order. During Build, `Get[T]` for the active builder is restricted to names in its declared `Needs`.
   - **PostInit** — call `PostInit(ctx, c)` on services that implement `PostIniter`, in dep order. Cross-wiring lives here (`A.SetB(b)` / `B.SetA(a)`). Get is unrestricted.
   - **Start** — call `Start(ctx)` on services that implement `Starter`. Background goroutines, watchers, opRegistry dispatcher.
   - **Stop** — on shutdown, call `Stop(ctx)` on services that implement `Stopper` in **reverse** resolved order.

### Package layout

```
internal/serviceregistry/        ← the registry mechanism (new)
  registry.go    — ServiceDef, factory list, Register()
  container.go   — Container, Get[T], Include, Override, Resolve, Build, etc.
  graph.go       — topological sort, cycle detection
  lifecycle.go   — PostIniter, Starter, Stopper interfaces
  errors.go      — typed errors

internal/server/registry_wire.go ← server-specific: populates *Server typed fields after Build

internal/<domain>/register.go    ← each domain package's init() + ServiceDef
internal/<domain>/service.go     ← service struct (may implement lifecycle interfaces)
```

The design mirrors `internal/operations/registry` (the existing opRegistry for async ops): registration table + dispatcher + lifecycle, but for synchronous services instead of dispatchable op runs.

## ServiceDef and registration API

```go
// internal/serviceregistry/registry.go
package serviceregistry

type ServiceDef struct {
    // Name is the registry key. Must be unique. Convention: lowercase,
    // dot-separated for grouping (e.g. "dedup", "metafetch", "itunes.batcher").
    Name string

    // Needs lists names of OTHER services this service's Build func will
    // Get[T]. The container enforces that Build can only Get services listed
    // here — any other Get returns an error/panic. This makes Needs the
    // single source of truth for the build-time dep graph.
    Needs []string

    // Build constructs the service instance. May call Get[T](c, name) for
    // any name in Needs.
    Build func(c *Container) (any, error)
}

// Register appends a ServiceDef to the package-level factory list.
// Called from init() in a domain package's register.go.
// Panics on duplicate Name (caught at startup, never at runtime).
func Register(def ServiceDef) {
    if def.Name == "" {
        panic("serviceregistry: ServiceDef.Name is required")
    }
    if def.Build == nil {
        panic("serviceregistry: ServiceDef.Build is required (name=" + def.Name + ")")
    }
    if _, dup := registered[def.Name]; dup {
        panic("serviceregistry: duplicate name: " + def.Name)
    }
    registered[def.Name] = def
}

var registered = map[string]ServiceDef{}
```

### Example domain registration

`internal/dedup/register.go`:

```go
package dedup

import (
    "github.com/jdfalk/audiobook-organizer/internal/ai"
    "github.com/jdfalk/audiobook-organizer/internal/database"
    "github.com/jdfalk/audiobook-organizer/internal/merge"
    "github.com/jdfalk/audiobook-organizer/internal/serviceregistry"
)

func init() {
    serviceregistry.Register(serviceregistry.ServiceDef{
        Name:  "dedup",
        Needs: []string{"store", "embeddingstore", "merge", "embedclient", "llmparser"},
        Build: func(c *serviceregistry.Container) (any, error) {
            store     := serviceregistry.Get[database.Store](c, "store")
            embStore  := serviceregistry.Get[*database.EmbeddingStore](c, "embeddingstore")
            mergeSvc  := serviceregistry.Get[*merge.Service](c, "merge")
            embClient := serviceregistry.Get[*ai.EmbeddingClient](c, "embedclient")
            llmParser := serviceregistry.Get[*ai.OpenAIParser](c, "llmparser")
            engine := NewEngine(embStore, store, embClient, llmParser, mergeSvc)
            // threshold config applied from config.AppConfig here
            return engine, nil
        },
    })
}
```

One file per domain package, ~30 lines, uniform template. All deps are explicit and typed at the `Get[T]` call site. `Needs` is checked against actual `Get[T]` calls — drift is caught at startup.

## Container API

```go
// internal/serviceregistry/container.go
package serviceregistry

import "context"

type Container struct {
    include       map[string]bool      // whitelist (transitive closure computed at Resolve)
    overrides     map[string]any       // override-by-instance; treated as leaves
    built         map[string]any       // populated during Build phase
    order         []string             // resolved build order
    phase         containerPhase       // unresolved → resolved → built → started → stopped
    activeBuilder string               // name of service whose Build is currently running
}

func NewContainer() *Container { ... }

// Include adds service names to the build set. Transitive deps are pulled in
// at Resolve time. Chainable.
func (c *Container) Include(names ...string) *Container { ... }

// IncludeAll adds every registered ServiceDef. Production default.
func (c *Container) IncludeAll() *Container { ... }

// Override substitutes an instance for the named service. Factory Build is
// not called. Implicitly Included. Treated as a leaf in the dep graph
// (its declared Needs are ignored). Test-only.
func (c *Container) Override(name string, instance any) *Container { ... }

// Resolve computes transitive closure, runs topological sort, validates
// the dep graph. Idempotent.
//   Errors: ErrUnknownService, ErrCycle.
func (c *Container) Resolve() error { ... }

// Build runs all ServiceDef.Build funcs in resolved order. Resolve is
// called implicitly if needed. Each Build runs under activeBuilder tracking
// so Get[T] can enforce Needs membership.
func (c *Container) Build(ctx context.Context) error { ... }

// PostInit invokes PostInit() on services implementing PostIniter,
// in resolved order. Get[T] is unrestricted here.
func (c *Container) PostInit(ctx context.Context) error { ... }

// Start invokes Start() on services implementing Starter, in resolved order.
// On error, aborts and calls Stop on already-started services in reverse.
func (c *Container) Start(ctx context.Context) error { ... }

// Stop invokes Stop() on services implementing Stopper, in REVERSE resolved
// order. Best-effort — errors are logged but don't abort the sweep.
func (c *Container) Stop(ctx context.Context) error { ... }

// Get returns the instance registered under name, type-asserted to T.
// Panics on type mismatch (programmer error — fail fast at startup, never
// in production hot paths). During Build, also panics if name is not in
// the active builder's Needs.
func Get[T any](c *Container, name string) T { ... }

// TryGet is the non-panicking variant. Returns (zero, false) if missing.
// For optional deps (e.g. embedding store, present only when API key configured).
func TryGet[T any](c *Container, name string) (T, bool) { ... }
```

### Production flow

```go
c := serviceregistry.NewContainer().
    Override("store", store).        // host-provided
    Override("config", &cfg).         // host-provided
    IncludeAll()
if err := c.Resolve(); err != nil { return err }
if err := c.Build(ctx); err != nil { return err }
if err := c.PostInit(ctx); err != nil { return err }
// ... wireServerFromContainer(s, c) ...
if err := c.Start(ctx); err != nil { return err }
// ... serve ...
_ = c.Stop(shutdownCtx)
```

### Test flow

```go
c := serviceregistry.NewContainer().
    Override("store", mockStore).
    Override("activity", &activityNoop{}).
    Include("metafetch")  // pulls metafetch + its declared deps
require.NoError(t, c.Resolve())
require.NoError(t, c.Build(t.Context()))
```

## Lifecycle interfaces

Three optional interfaces. Services implement only what they need; the container picks them up by type-assertion in each phase.

```go
// internal/serviceregistry/lifecycle.go
package serviceregistry

import "context"

// PostIniter is implemented by services needing cross-wiring after all
// Build() calls complete. Called in resolved dep order. Get[T] is unrestricted.
type PostIniter interface {
    PostInit(ctx context.Context, c *Container) error
}

// Starter is implemented by services with background goroutines or other
// explicit-start needs (batchers, watchers, dispatchers).
type Starter interface {
    Start(ctx context.Context) error
}

// Stopper is implemented by services needing explicit resource release.
// Called in REVERSE resolved order during Container.Stop().
type Stopper interface {
    Stop(ctx context.Context) error
}
```

### Example using all three

`internal/itunes/service.go`:

```go
func init() {
    serviceregistry.Register(serviceregistry.ServiceDef{
        Name:  "itunes",
        Needs: []string{"store", "metafetch", "config"},
        Build: func(c *serviceregistry.Container) (any, error) {
            return itunesservice.New(itunesservice.Deps{
                Store:     serviceregistry.Get[database.Store](c, "store"),
                Metafetch: serviceregistry.Get[*metafetch.Service](c, "metafetch"),
                // ...
            })
        },
    })
}

func (s *Service) PostInit(ctx context.Context, c *serviceregistry.Container) error {
    org := serviceregistry.Get[*organize.Service](c, "organize")
    org.DiscoverITunesLibraryPath = func(_ database.Store) string {
        return s.Importer.DiscoverLibraryPath()
    }
    return nil
}

func (s *Service) Start(ctx context.Context) error { return s.Batcher.Start(ctx) }
func (s *Service) Stop(ctx context.Context) error  { return s.Batcher.Stop(ctx) }
```

### Why Build-strict, PostInit-relaxed?

- **Build** can only `Get[T]` services in declared `Needs`. This makes the dep graph the only source of build-time coupling — a cycle is a *real* cycle, not an artifact of who-knows-whom.
- **PostInit** can `Get[T]` anything because all services are built by then. Cross-wiring (`A.SetB(b)` / `B.SetA(a)`) is intentionally bidirectional and not a cycle in any meaningful sense.

## Dependency graph and cycle detection

Resolve runs Kahn's topological sort over the transitive closure of the `Include` set. Overrides are treated as leaves (their declared `Needs` are ignored — the override IS the leaf).

Algorithm:
1. Compute `wanted` set by walking `include` and following `Needs` recursively. Overrides short-circuit.
2. Build dep graph (`incoming` count + `outgoing` adjacency) over `wanted`.
3. Kahn's: pop nodes with zero in-degree, decrement dependents, repeat. **Sort the ready queue lexically before processing** for deterministic, reproducible build order.
4. If `len(order) != len(wanted)`, report cycle with the participating service names.

`Get[T]` during Build enforces Needs:

```go
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
```

Properties:
- Cycle errors report participating service names — actionable diagnostics.
- Undeclared Get panics — programmer bug, fails loudly at startup, never reaches production hot paths.
- Lex-stable order makes startup logs and tests reproducible.

## Integration with `*Server`

The only piece touching `internal/server` directly. Goal: keep typed fields on `*Server` so handler code is unchanged, but make their population a single deterministic step.

```go
// internal/server/registry_wire.go
package server

func wireServerFromContainer(s *Server, c *serviceregistry.Container) {
    s.store                  = serviceregistry.Get[database.Store](c, "store")
    s.audiobookService       = serviceregistry.Get[*audiobookspkg.AudiobookService](c, "audiobook")
    s.audiobookUpdateService = serviceregistry.Get[*AudiobookUpdateService](c, "audiobook.update")
    s.metadataFetchService   = serviceregistry.Get[*metafetch.Service](c, "metafetch")
    s.dedupEngine            = serviceregistry.Get[*dedup.Engine](c, "dedup")
    s.itunesSvc              = serviceregistry.Get[*itunesservice.Service](c, "itunes")
    s.scheduler              = serviceregistry.Get[*scheduler.TaskScheduler](c, "scheduler")
    // ... ~40 lines, one per existing field, shrinks as handlers migrate out
    s.container = c
}

func NewServer(store database.Store) *Server {
    bgCtx, bgCancel := context.WithCancel(context.Background())
    s := &Server{
        router:   newRouter(),
        bgCtx:    bgCtx,
        bgCancel: bgCancel,
    }
    // Host-provided overrides: services whose construction is owned by the
    // host process, not the registry. `store` is provided by main/cmd;
    // `config` is the package-level config.AppConfig (a *config.Config so
    // services see live updates).
    c := serviceregistry.NewContainer().
        Override("store", store).
        Override("config", &config.AppConfig).
        IncludeAll()
    if err := c.Resolve(); err != nil { log.Fatalf("%v", err) }
    if err := c.Build(context.Background()); err != nil { log.Fatalf("%v", err) }
    if err := c.PostInit(context.Background()); err != nil { log.Fatalf("%v", err) }
    wireServerFromContainer(s, c)
    s.registerRoutes()
    return s
}

func (s *Server) Start(ctx context.Context, cfg ServerConfig) error {
    if err := s.container.Start(ctx); err != nil { return err }
    return s.runHTTP(cfg)
}

func (s *Server) Shutdown(ctx context.Context) error {
    s.bgCancel()
    _ = s.container.Stop(ctx)
    return s.httpServer.Shutdown(ctx)
}
```

`NewServer` collapses from ~600 lines to ~25. Handler code (`s.metadataFetchService.Foo()`) is unchanged. `wireServerFromContainer` is interim glue that shrinks as handlers migrate into domain packages (future work).

## Test patterns

### Pattern A — single-service unit test

```go
func TestDedupEngineMergesPair(t *testing.T) {
    store := mocks.NewMockStore(t)
    emb   := newFakeEmbeddingStore(t)
    mrg   := mocks.NewMockMergeService(t)

    c := serviceregistry.NewContainer().
        Override("store", store).
        Override("embeddingstore", emb).
        Override("merge", mrg).
        Override("embedclient", &noopEmbedClient{}).
        Override("llmparser", &noopLLMParser{}).
        Include("dedup")
    require.NoError(t, c.Resolve())
    require.NoError(t, c.Build(t.Context()))

    engine := serviceregistry.Get[*dedup.Engine](c, "dedup")
    // exercise engine
}
```

No `*Server`, no extra services built. Overrides cover declared Needs as leaves.

### Pattern B — handler test with `*Server` subset

```go
func TestDedupHandler(t *testing.T) {
    store := mocks.NewMockStore(t)
    s, c := newTestServer(t,
        serviceregistry.Override("store", store),
        serviceregistry.Include("dedup", "merge", "embeddingstore"),
    )
    _ = c
    // drive s.router with httptest
}
```

`newTestServer` lives in `internal/server/testsupport.go` (test-only file or test build tag).

### Pattern C — legacy tests unchanged

Tests that currently stub `database.SetGlobalStore`, `initializeStore`, etc. via package-level vars (`cmd/commands_test.go` style) keep working. The registry is opt-in: tests migrate file-by-file at their own pace.

### Rules

- No `Container.Reset()` or global state. Each test gets a fresh container.
- `t.Context()` everywhere (Go 1.24+). Build/PostInit/Start get clean cancellation on test failure.
- Override the *instance*, not the factory function. Keeps tests typed.
- mockery-generated mocks plug straight into `Override(name, mockInstance)`.

## Migration plan — wave-based parallel-sweep execution

Rule: within a wave, parallel tasks **only** touch new files in a domain package — never `server.go`. Each wave ends with a serial `.INT` task that does the consolidated `server.go` change.

### Wave 0 — Registry foundation (1 task, sequential, blocks everything)

| ID | Description | Model |
|----|-------------|-------|
| **W0.1** | Build `internal/serviceregistry/` (registry.go, container.go, graph.go, lifecycle.go, errors.go) + unit tests for graph/cycle/lifecycle. No callers anywhere. ~600 LOC + ~400 LOC tests. | sonnet |

### Wave 1 — Leaf services (10 parallel + 1 serial)

Each parallel task: create `internal/<pkg>/register.go` with `init() { serviceregistry.Register(...) }`. No server.go edit. Compiles harmlessly.

| ID | Service | Needs | Model |
|----|---------|-------|-------|
| W1.1 | `audiobook` | `[store]` | haiku |
| W1.2 | `batch` | `[store]` | haiku |
| W1.3 | `work` | `[store]` | haiku |
| W1.4 | `filesystem` | `[store]` | haiku |
| W1.5 | `importpath` | `[store]` | haiku |
| W1.6 | `scan` | `[store]` | haiku |
| W1.7 | `dashboard` | `[store]` | haiku |
| W1.8 | `system` | `[store, config]` | haiku |
| W1.9 | `configupdate` | `[store]` | haiku |
| W1.10 | `metadatastate` | `[store]` | haiku |
| **W1.INT** | server.go: install full NewServer registry flow per section 6 — host overrides for `store` + `config`, `c.IncludeAll().Resolve().Build().PostInit()`. Add `wireServerFromContainer()` for these 10 fields, delete the corresponding struct literal lines. PostInit is a no-op pre-Wave-2 but lives in the flow from W1.INT onward so subsequent waves only edit registrations, not the flow. `make ci` green. | sonnet |

### Wave 2 — Cross-wired services (5 parallel + 1 serial)

Each parallel task: add `PostInit(ctx, c)` method moving the cross-wire setter calls in from server.go. Does not delete from server.go yet — that's W2.INT.

| ID | Service | Cross-wiring moved into PostInit | Model |
|----|---------|----------------------------------|-------|
| W2.1 | `metafetch` | `SetOLStore`, `SetISBNEnrichment`, `SetWriteBackBatcher`, `SetActivityService`, `SetDedupEngine`, `SetMetadataScorer`, `SetMetadataLLMScorer`, `SetSafeWriteDeps` | sonnet |
| W2.2 | `activity` | activityWriter / teeWriter setup, `SetScanHooks`, `audiobookService.SetActivityService` | haiku |
| W2.3 | `merge` | `SetWriteBackBatcher` | haiku |
| W2.4 | `quarantine` | `SetWriteBackBatcher` | haiku |
| W2.5 | `organize` | `SetWriteBackBatcher`, `SetOrganizeHooks`, `DiscoverITunesLibraryPath`, `ExecuteITunesSync`, `ScanEnqueuer` | sonnet |
| **W2.INT** | server.go: delete the manual `SetX` blocks; `c.PostInit(ctx)` runs them. Add ServiceDefs for any of these services not already registered in W1. | sonnet |

### Wave 3 — Start/Stop services (7 parallel + 1 serial)

| ID | Service | Phase wired | Model |
|----|---------|-------------|-------|
| W3.1 | `writebackbatcher` | Start (flush loop), Stop (drain) | haiku |
| W3.2 | `updatescheduler` | Start, Stop | haiku |
| W3.3 | `activitywriter` | Start, Stop | haiku |
| W3.4 | `searchindex` | Start (open Bleve), Stop (close + drain indexQueue) | sonnet |
| W3.5 | `opregistry` | Start (dispatcher), Stop (drain workers) | haiku |
| W3.6 | `batchpoller` | Start (poll loop), Stop | haiku |
| W3.7 | `librarywatcher` | Start (fsnotify), Stop | haiku |
| **W3.INT** | server.go: replace manual `Start()` / `Shutdown()` calls with `s.container.Start(ctx)` / `s.container.Stop(ctx)`. Verify shutdown ordering. | sonnet |

### Wave 4 — Embedding/AI cluster (8 parallel + 1 serial)

The big conditional block (server.go lines 464–602). Some services optional via `TryGet[T]`.

| ID | Service | Notes | Model |
|----|---------|-------|-------|
| W4.1 | `embeddingstore` | Needs: `[store]`; PebbleStore type-assert | haiku |
| W4.2 | `embedclient` | API-key-gated; with cache wiring | sonnet |
| W4.3 | `llmparser` | OpenAIParser | haiku |
| W4.4 | `chromemstore` | Optional via TryGet; hydrate goroutine moves to Start | haiku |
| W4.5 | `aijobsstore` | Type-asserted from store | haiku |
| W4.6 | `dedup` | Needs: `[store, embeddingstore, embedclient, llmparser, merge]`; PostInit wires chromem, aijobs, scorer; Start: embedding backfill goroutine | sonnet |
| W4.7 | `metadatascorer` | Embedding scorer | haiku |
| W4.8 | `metadatallmscorer` | LLM rerank scorer | haiku |
| **W4.INT** | server.go: delete the 140-line `if ps, ok := ...` block. | sonnet |

### Wave 5 — UOS plugin migrations (5 parallel + 1 serial)

Move each existing UOS plugin into a `register.go` whose `Build` returns the plugin and whose `PostInit` calls `Register(opRegistry)`.

| ID | Plugin | Model |
|----|--------|-------|
| W5.1 | `maintenanceplugin` | haiku |
| W5.2 | `itunesplugin` | haiku |
| W5.3 | `delugeplugin` | haiku |
| W5.4 | `dedupplugin` (may be folded into W4.6) | haiku |
| W5.5 | `acoustidplugin` | haiku |
| **W5.INT** | server.go: delete the inline plugin Register call sites. | sonnet |

### Wave 6 — Scheduler residual (1 task, sequential)

| ID | Description | Model |
|----|-------------|-------|
| **W6.1** | Extract `internal/server/scheduler_extra_ops.go` to `internal/scheduler` with `Needs: [dedup, aiscan, activitywriter, openlibrary, store]`. Delete file from `internal/server`. Closes SERVER-THIN-RESIDUAL. | sonnet |

### Wave 7 — Final cleanup (2 sequential)

| ID | Description | Model |
|----|-------------|-------|
| **W7.1** | Audit and delete remaining wiring code in NewServer. Target: ≤ 50 lines. | sonnet |
| **W7.2** | Audit and remove `database.GetGlobalStore()` fallback paths where no longer needed. | sonnet |

### Dependency graph between waves

```
W0.1 → W1.{1..10} (parallel) → W1.INT
                                  ↓
                W2.{1..5} (parallel) → W2.INT
                                          ↓
                       W3.{1..7} (parallel) → W3.INT
                                                  ↓
                            W4.{1..8} (parallel) → W4.INT
                                                      ↓
                                  W5.{1..5} (parallel) → W5.INT
                                                              ↓
                                                            W6.1
                                                              ↓
                                                            W7.{1,2}
```

Properties:
- Each wave is one `/parallel-sweep` invocation.
- Within a wave, parallel tasks never touch the same file.
- `.INT` tasks always touch `server.go` and run serially after their wave's parallel tasks merge.
- Failure in any task only rolls back that task's PR — earlier waves are already in main.
- ~50 total tasks, mostly haiku, sonnet for `.INT` and complex services.

## Future work (out of scope)

Items the registry **enables** but doesn't deliver in this spec. Tracked as separate backlog tickets.

| ID | Description |
|----|-------------|
| **SERVER-HANDLER-MIGRATE** | Move HTTP handlers out of `*Server` methods into domain packages. `internal/<pkg>/handlers.go` + `routes.go`; handler type takes its service via constructor; routes registered via a `RouteRegistrar` interface (likely another optional lifecycle method). Each migration deletes one typed field from `*Server` and one line from `wireServerFromContainer`. Order: smallest-blast-radius first (dedup, organize, quarantine), god-object handlers (audiobooks_handlers.go) last. |
| **SERVER-SDK-INTERFACES** | When external plugin authors arrive, publish `pkg/serverapi/` with interface contracts for public services (`DedupEngine`, `MetadataFetcher`, `OrganizeService`, etc.). Internal services keep concrete types in `Get[*Engine]`; external plugins use `Get[serverapi.DedupEngine]`. No registry change needed — just publishing interfaces and contract-stability discipline. |
| **REGISTRY-METRICS** | Expose registry state via `/api/v1/system/services` — names, phase, last error, uptime. |
| **REGISTRY-CONFIG-RELOAD** | A future `Reloader` interface (optional, like `Starter`) lets services react to live config updates via the registry rather than each service hooking config events independently. |
| **CLI-CONTAINER-INSPECT** | `audiobook-organizer registry list` / `registry graph --dot` to print resolved dep order and emit Graphviz. |

## Open questions

None remaining from brainstorm.

## Locked decisions

- Registration via `init()` auto-register into a global factory list (future-proofing for external plugins).
- Typed `Deps` per service via generic `Get[T](c, "name")`; container internally `map[string]any` keyed by string.
- HTTP handlers keep accessing services via typed `*Server` fields populated by `wireServerFromContainer`. Handler migration is future work.
- Lifecycle: optional `PostIniter` / `Starter` / `Stopper` interfaces, picked up by type-assertion. No mandatory lifecycle on every ServiceDef.
- Dependency declaration: `Needs []string` enforced at `Get[T]` time during Build. PostInit is unrestricted.
- Cycle detection: Kahn's topological sort with lex-stable ready queue for deterministic order.
- Test override pattern: `Container.Include(...)` (whitelist) + `Container.Override(name, instance)` (stub); overrides are leaves in the dep graph.
- Migration: 7 waves of parallel-sweep tasks, one wave at a time, each wave ending in a serial `server.go` integration task.
