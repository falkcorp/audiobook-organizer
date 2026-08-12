<!-- file: docs/architecture/server-plugin-registry-deferred-work.md -->
<!-- version: 1.1.0 -->
<!-- last-edited: 2026-05-13 -->

# SERVER-PLUGIN-REG — Deferred Work Plan

The 7-wave SERVER-PLUGIN-REG migration (May 12–13, 2026) shipped the service
registry, registered ~30 services + 5 plugins, and wired W1+W2 services through
`wireServerFromContainer`. PR #882 finished the W4.INT/W5.INT cleanup that the
original sweep skipped.

**What's still on the wishlist** lives in this doc — five tickets totaling a
few weeks of focused engineering, each with concrete blockers, proposed
solutions, and risk profiles. They're sized to merge one-at-a-time so the
inline-and-container parallel-construction state can be unwound safely.

## Companion docs

- `server-plugin-registry-design.md` — design rationale, locked decisions
- [`../archive/2026-08-consolidation-wave2/architecture/server-plugin-registry-plan.md`](../archive/2026-08-consolidation-wave2/architecture/server-plugin-registry-plan.md)
  — original 7-wave plan (delivered; **archived 2026-08-12**, its residual scope lives in this document)

## TL;DR

| Ticket | Size | Risk | Blocks |
|--------|------|------|--------|
| **REGISTRY-NAMED-GROUPS** | S (½ day) | None | — (foundation for others) |
| **PLUGIN-DECOUPLE-SERVER-CLOSURES** | M (1–2 days) | Medium | LIFECYCLE-FLIP, W7.1 |
| **PROMOTE-STUB-REGISTRATIONS** | S (½ day) | Low | — |
| **SERVER-LIFECYCLE-FLIP** | L (3–5 days) | Medium–high | W7.1 |
| **W7.1 NEWSERVER-TRIM** | M (1 day) | Low | — |
| **SERVER-GLOBAL-STORE-AUDIT** | XL (1–2 weeks) | Low–medium | — |
| **TEST-GLOBAL-STORE-MIGRATION** | M (1–2 days) | Low | end-state cleanup |

Dependency order: `REGISTRY-NAMED-GROUPS` → `PLUGIN-DECOUPLE` → `PROMOTE-STUBS`
→ `LIFECYCLE-FLIP` → `W7.1`. `GLOBAL-STORE-AUDIT` is parallel-safe.
`TEST-GLOBAL-STORE-MIGRATION` follows after `GLOBAL-STORE-AUDIT` lands.

---

## 1. PLUGIN-DECOUPLE-SERVER-CLOSURES

**Problem.** Two services carry server-bound closures that prevent the registry
from constructing them: every other ticket waits on this.

### 1a. `itunesservice.Service.Deps`

Current inline construction (`internal/server/server.go:~427`):

```go
itunesSvc, err := itunesservice.New(itunesservice.Deps{
    Store:         resolvedStore,
    Config:        itunesCfg,
    AudiobookRoot: config.AppConfig.RootDir,
    ReportDir:     filepath.Join(config.AppConfig.RootDir, "reports"),
    OnBookCreated: func(bookID string) {
        server.fireDedupOnImport(bookID)   // ← captures *Server
    },
    Metafetch: server.metadataFetchService,
    OrganizerFactory: func() itunesservice.BookOrganizer {
        return organizer.NewOrganizer(&config.AppConfig)   // ← config closure
    },
})
```

**Closures that block container construction:**

- `OnBookCreated func(bookID string)` — calls `server.fireDedupOnImport(bookID)`
  which in turn uses `s.dedupEngine`, `s.bgWG`, `s.bgCtx` (server lifecycle state).
- `OrganizerFactory func() BookOrganizer` — uses `&config.AppConfig` (package
  global; not server-bound but coupled to construction site).

**Proposed solution — event-bus integration.**

1. Add a `BookCreated` event type to `internal/plugin`:
   ```go
   type BookCreatedEvent struct{ BookID string; Source string }
   ```
2. itunesservice publishes after `CreateBook` succeeds:
   ```go
   if s.deps.EventBus != nil {
       s.deps.EventBus.Publish(ctx, plugin.BookCreatedEvent{BookID: id, Source: "itunes"})
   }
   ```
3. Add a subscriber in `internal/dedup` that handles `BookCreatedEvent` and
   internally manages its own `bgCtx`/`bgWG`. Wired in `dedup.Engine.PostInit`:
   ```go
   func (e *Engine) PostInit(ctx context.Context, c *serviceregistry.Container) error {
       bus, _ := serviceregistry.TryGet[*plugin.EventBus](c, "eventbus")
       if bus == nil { return nil }
       bus.Subscribe(plugin.TopicBookCreated, e.onBookCreated)
       return nil
   }
   ```
4. itunesservice.Deps drops `OnBookCreated`, gains `EventBus *plugin.EventBus`.
5. `OrganizerFactory` moves out of Deps entirely — itunesservice constructs an
   organizer itself when needed: `organizer.NewOrganizer(s.deps.Config)`.
6. itunesservice gains `register.go`:
   ```go
   serviceregistry.Register(serviceregistry.ServiceDef{
       Name:  "itunes",
       Needs: []string{"store", "config", "eventbus", "metafetch"},
       Build: func(c) (any, error) {
           // ... build Deps from container, return itunesservice.New(...)
       },
   })
   ```
7. **Unstub** `internal/itunes/service/register.go` (W3.1) — its `writebackbatcher`
   stub becomes `Get[*itunesservice.Service](c, "itunes").Batcher`.
8. **Unstub** `internal/plugins/itunes/register.go` (W5.2) — builds real plugin
   when itunes service is available.

### 1b. `maintenance.ServerDeps`

Current `internal/plugins/maintenance/deps.go`:
```go
type ServerDeps struct {
    Store         database.Store
    Scheduler     *scheduler.TaskScheduler
    DedupEngine   *dedup.Engine
    AIScanStore   *database.AIScanStore
    ActivityWriter *activity.Writer
    OLService     *metafetch.OpenLibraryService
    // ... ~10 fields
}
```

Inline construction passes `*Server`:
```go
maintenanceplugin.New(server).Register(server.opRegistry)
```

**Proposed solution.** Replace `New(server *Server)` with `New(deps ServerDeps)`.
Each field gets populated from the container in the plugin's `register.go::Build`
via `TryGet`. Lifecycle methods that use the deps remain identical.

### Risk

- **Medium.** The event-bus refactor is the largest single change. The existing
  `OnBookCreated` is called synchronously from `CreateBook`; the event bus
  semantics (synchronous vs. async, ordering, error propagation) must match.
- Cover with a `TestBookCreatedEventDispatch` test that asserts dedup engine
  is invoked when itunes publishes.

### Tests

- Existing `TestITunesImport_*` and dedup tests should continue passing.
- New: integration test that the event bus dispatches `BookCreated` to dedup
  end-to-end through the container.

### Sequencing

Should be the **first** of the deferred items — every other lifecycle work
benefits from these two plugins being fully container-managed.

---

## 2. PROMOTE-STUB-REGISTRATIONS

**Problem.** Three registrations currently return typed nil ("stub"):

- `internal/itunes/service/register.go` — `writebackbatcher`
- `internal/plugins/maintenance/register.go` — `maintenanceplugin`
- `internal/plugins/itunes/register.go` — `itunesplugin`

All blocked by **PLUGIN-DECOUPLE-SERVER-CLOSURES**. Once that lands, promoting
these stubs to real registrations is mechanical.

### Procedure

For each stub register.go:

1. Replace the typed-nil Build with the real constructor call.
2. Add `PostInit` if the original inline path included extra wiring (e.g.,
   `Plugin.Register(opRegistry)` — analogous to dedup/acoustid/deluge plugins).
3. Add the service name to the `Include(...)` list in NewServer.
4. Delete the corresponding inline construction in NewServer.

### Risk

**Low.** All the deps are already in the container after PLUGIN-DECOUPLE lands.

### Sequencing

Immediately after PLUGIN-DECOUPLE.

---

## 3. SERVER-LIFECYCLE-FLIP

**Problem.** Several services have inline `.Start()` calls in NewServer that
duplicate work the container would do via `Container.Start(ctx)`. Calling
`Container.Start(ctx)` today would either double-launch goroutines or cause
hard conflicts (Bleve file lock).

### Per-service blockers + fixes

#### 3a. `updatescheduler`

**Blocker.** `internal/updater/register.go` hardcodes `NewUpdater("dev")`.
The inline `server.updater = updater.NewUpdater(appVersion)` uses the
real version (ldflags-injected via `main.version`).

**Fix.** Either:
- Add a host Override: `NewContainer().Override("appversion", appVersion)`,
  then `Needs: []string{"appversion"}` in updater register.go;
- Or expose `appVersion` from a different package (e.g.
  `internal/version`) and import in updater register.go.

Option A is cleaner (Override is explicit).

**Then.** Delete inline `server.updateScheduler = updater.NewScheduler(...)` +
inline `server.updateScheduler.Start()`. Container handles construction +
lifecycle.

#### 3b. `searchindex`

**Blocker.** Inline Bleve open in `server_lifecycle.go` (the Server.Start
phase) opens the same index path the container's `searchindex` service would.

**Fix.** Decide which path opens Bleve:

- **Option A — container only.** Delete the inline open in
  `server_lifecycle.go`; rely on `Container.Start` to open. Move the
  `indexQueue` worker setup into the searchindex service's Start. Risk: the
  indexQueue worker needs access to `database.Store` to do reindexing; that's
  resolvable via Needs in the service def.
- **Option B — defer searchindex.** Leave the inline open; remove `searchindex`
  from container Include so its Start isn't called. Container's `searchindex`
  becomes a phantom registration. Cleaner code but less progress.

Recommend **A**.

#### 3c. `activitywriter`

**Blocker.** Inline `aw := activity.NewWriter(...)` in NewServer (line ~709)
constructs a parallel writer. The container's "activitywriter" service builds
its own. After PR #882, `s.activityWriter` is **not** assigned by
`wireServerFromContainer` — it's only assigned from the inline `aw`.

**Fix.**

1. Add `s.activityWriter = serviceregistry.Get[*activity.Writer](c, "activitywriter")`
   to wireServerFromContainer (with TryGet — conditional on DatabasePath).
2. Delete inline `aw := ...` + `server.activityWriter = aw`.
3. Container.Start triggers `*activity.Writer.Start(ctx)`. The
   `log.SetOutput(server.activityWriter)` call stays — it operates on the
   container's instance via the field.

#### 3d. `aiScanStore` + `pipelineManager`

**Blocker.** Not yet in the container. The AI block at `server.go:~511`
constructs these inline AND uses local variables for the chromem hydrate +
SetX wiring against `server.dedupEngine`.

**Fix.**

1. Register two new services:
   - `aiscanstore` — Build type-asserts `*PebbleStore` and calls
     `NewAIScanStoreFromDB(ps.DB())`. Lives in
     `internal/server/registry_wire.go` (cycle with `internal/database`).
   - `pipelinemanager` — Needs `[aiscanstore, store]` + the AI parser via
     TryGet on llmparser. Build returns `aiscan.NewPipelineManager(...)`.
2. Move the **chromem hydrate goroutine** into `dedup.Engine.PostInit`:
   - `PostInit` calls `TryGet[*ChromemEmbeddingStore](c, "chromemstore")`,
     `engine.SetChromemStore(...)`, then launches the hydrate goroutine on
     the engine's own bg-context.
3. Move the **aijobs/scorer/llmscorer wiring** into the same PostInit.
4. Delete the inline AI block at server.go:~511 once all wiring has moved.
5. wireServerFromContainer pulls `s.aiScanStore` + `s.pipelineManager` from
   container.

This single fix lets NewServer drop ~140 lines.

### Final step: actually call Container.Start / Stop

```go
// In Server.Start (after the existing setup, before runHTTP):
if err := s.container.Start(s.bgCtx); err != nil {
    log.Fatalf("[server] container start: %v", err)
}

// In Server.Shutdown (before httpServer.Shutdown):
_ = s.container.Stop(shutdownCtx)
```

Each individual inline `.Start()` and `.Shutdown()` call gets deleted as its
service moves into the container's lifecycle.

### Risk

**Medium-high.** Lifecycle ordering matters: e.g. `activityWriter` must
start before `log.SetOutput` is invoked. `searchindex` open must finish
before any handler tries to query. Each per-service flip needs a smoke test.

### Tests

- Per-service: existing tests must continue to pass.
- Integration: a full `make ci` after each flip; SERVER-THIN-8 pre-existing
  timeouts are acceptable.

### Sequencing

After PLUGIN-DECOUPLE + PROMOTE-STUBS. Within this ticket: do 3a/3c first
(low-risk), then 3d (frees the biggest chunk of NewServer), then 3b last
(highest risk because it touches Server.Start).

---

## 4. SERVER-GLOBAL-STORE-AUDIT

**Problem.** ~120 production `database.GetGlobalStore()` callers remain.

```
35 internal/scanner/scanner.go
14 internal/audiobooks/helpers.go
13 internal/server/server_metadata.go
10 internal/server/server.go
 9 internal/metafetch/helpers.go
 8 internal/organizer/organizer.go
 3 internal/server/file_io_pool.go
 3 cmd/root.go
 3 cmd/diagnostics.go
 2 internal/server/server_title_helpers.go
 ... + ~20 long-tail callers
```

Plus ~289 test-only callers (lower priority — `database.SetGlobalStore` in
tests is the established pattern for mock injection).

### Why it matters

- `GetGlobalStore` makes the store an implicit global, which:
  - Hides dependencies (you can't see what a function needs from its signature).
  - Makes testing harder (must set/restore the global).
  - Conflicts with the registry's explicit-deps model.
- Currently many handlers do `s.Store()` (which falls back to GetGlobalStore)
  for the same reason.

### Proposed strategy — per-package incremental sweep

One PR per file (or small cluster of related files). Each PR:

1. Change the function signature to accept `store database.Store` (or a
   narrower interface) explicitly.
2. Update all callers in the same package.
3. For cross-package callers, propagate the store parameter back to the
   call site or fetch from a container/service field that already has it.
4. Mark the changed file with `// W7.2: removed GetGlobalStore — store
   now passed explicitly` comment.

Order (smallest-blast-radius first):
1. `cmd/seed.go`, `cmd/dedup_bench.go`, `cmd/diagnostics.go` (1, 1, 3 callers)
2. `internal/itunes/rebuild.go`, `internal/backup/backup.go`,
   `internal/database/store.go` (1 each)
3. `internal/server/server_title_helpers.go` (2 callers)
4. `internal/server/server_middleware.go` (2)
5. `internal/server/file_io_pool.go` (3)
6. `internal/metafetch/service_search.go` (2)
7. `internal/server/server.go` (10) — likely also touches W7.1
8. `internal/server/server_metadata.go` (13)
9. `internal/organizer/organizer.go` (8)
10. `internal/metafetch/helpers.go` (9)
11. `internal/audiobooks/helpers.go` (14)
12. `internal/scanner/scanner.go` (35) — biggest, save for last

After all production callers are migrated, consider:
- Renaming `GetGlobalStore`/`SetGlobalStore` to make their test-only
  intent explicit, e.g., `GetGlobalStoreForTest`/`SetGlobalStoreForTest`.
- Or extracting the global into `internal/database/testglobal` with a
  build tag so it can't leak into production code.

### Risk

**Low–medium per PR.** Each PR touches one file at a time. The pattern is
mechanical (add param, thread through callers). Risk is mostly in `scanner.go`
(35 sites) and the long-tail propagation.

### Sequencing

**Parallel-safe** with other tickets. Doesn't depend on PLUGIN-DECOUPLE or
LIFECYCLE-FLIP. Could be a long-running cleanup track.

---

## 5. W7.1 NEWSERVER-TRIM

**Problem.** NewServer is still ~577 lines. The original spec called for ≤50.

### What's left after the above tickets land

If PLUGIN-DECOUPLE + PROMOTE-STUBS + LIFECYCLE-FLIP all land, NewServer
naturally shrinks by ~250 lines (inline AI block goes; inline updater/activity/
itunes/maintenance plugin construction goes). That puts it around 320 lines.

To get to ≤50 lines, the remaining work:

1. Move route registration (`server.setupRoutes()`) call to Server.Start
   or a dedicated method.
2. Move plugin event-bus setup + pluginRegistry init into a domain package.
3. Move the iTunes service hooks wiring (`organizeService.DiscoverITunesLibraryPath`,
   etc.) into PostInit methods on the relevant services.
4. Move maintenance.InjectStore / InjectEnqueuer into PostInit on
   maintenance plugin.
5. Move scanner hooks (`scanner.SetScanHooks`, `PostScanFn`, `AutoOrganizeFn`)
   into PostInit on scanner or organizer.
6. Move the iTunes service construction itself into its register.go (post
   PLUGIN-DECOUPLE this is now possible).
7. Move the SafeWriteDeps wiring (deluge protected-path cache + importer)
   into a PostInit on metafetch.

After all of this, NewServer should be:

```go
func NewServer(store database.Store) *Server {
    bgCtx, bgCancel := context.WithCancel(context.Background())
    server := &Server{
        store:    store,
        bgCtx:    bgCtx,
        bgCancel: bgCancel,
        router:   newRouter(),
        // ... cache fields, primitives that don't fit the service model
    }
    c := serviceregistry.NewContainer().
        Override("store", store).
        Override("config", &config.AppConfig).
        IncludeAll()
    if err := c.Resolve(); err != nil { log.Fatalf("[server] resolve: %v", err) }
    if err := c.Build(context.Background()); err != nil { log.Fatalf("[server] build: %v", err) }
    if err := c.PostInit(context.Background()); err != nil { log.Fatalf("[server] postinit: %v", err) }
    wireServerFromContainer(server, c)
    server.container = c
    return server
}
```

That's ~25 lines. The rest of the setup (routes, HTTP server fields, bg
coordination) moves into a separate `(*Server).init()` or splits into
Server.Start.

### Risk

**Low.** This is the natural output of the prior tickets — mostly a
mechanical sweep + a final cleanup pass.

### Sequencing

Last. Depends on all of the above.

---

## Suggested execution plan

Two-track approach. Track A is structural; Track B is the long-tail cleanup.

```
Track A (structural)
═════════════════════
0. REGISTRY-NAMED-GROUPS               ┐  ½ day (foundation)
1. PLUGIN-DECOUPLE-SERVER-CLOSURES     │
2. PROMOTE-STUB-REGISTRATIONS          ├─ ~1 week
3. SERVER-LIFECYCLE-FLIP (3a, 3c)      │
4. SERVER-LIFECYCLE-FLIP (3d)          ├─ ~1 week
5. SERVER-LIFECYCLE-FLIP (3b)          │
6. W7.1 NEWSERVER-TRIM                 ┘  ~2-3 days

Track B (parallel)
═════════════════════
1-12. SERVER-GLOBAL-STORE-AUDIT (one PR per file/cluster)
                                       ~2 weeks (parallel)

Track C (after Track B)
═════════════════════
1. TEST-GLOBAL-STORE-MIGRATION         ~1-2 days
```

Total: 2-3 weeks of focused work for one engineer; could compress with parallel
agents on the Track B sweep.

## Resolved design decisions (May 13, 2026 brainstorm)

### Q1 — Event-bus semantics for `BookCreated`

**Decision: async dispatch, panic-isolated subscribers, decoupled contexts.**

- `EventBus.Publish(ctx, evt)` returns immediately. Publisher's ctx gates only
  the dispatch enqueue — used for bounded-channel back-pressure, e.g.
  `select { case b.dispatch <- evt: case <-ctx.Done(): return }`.
- Subscribers run in their own goroutines, each wrapped in `recover()` so a
  panicking subscriber can't crash the publisher.
- Subscribers receive the **bus's lifecycle context**, NOT the publisher's
  ctx. A canceled publisher ctx = "drop on the floor"; a canceled bus ctx =
  "shutdown signal to all subscribers."
- Errors from subscribers are logged but never propagated back to the publisher.

This matches the existing `OnBookCreated` callback semantics (notification-only,
no return value) and prevents accidental back-pressure on imports.

**New test:** `TestEventBus_SubscriberPanic_DoesNotAffectPublisher`.

### Q2 — NewServer lifecycle: full delegation vs. explicit phases

**Decision: explicit phases. `Build` + `PostInit` in NewServer; `Start` in
Server.Start; `Stop` in Server.Shutdown.**

Rationale:
- Construction (Build/PostInit) is "ready to serve a test request."
- Lifecycle (Start) is "ready to do real work" — when `cmd/serve.go` actually
  starts the HTTP listener. Background goroutines launch here.
- Tests that construct without Start stay clean; this is the same pattern as
  `sql.Open` vs `db.Ping`.
- Debugging benefit: explicit phase calls in NewServer give precise error
  attribution (`resolve failed` vs `build failed` vs `postinit failed`).

### Q3 — `database.GetGlobalStore` lifecycle

**Decision (bridge state): keep, but rename to `GetGlobalStoreForTest` /
`SetGlobalStoreForTest`.**

- Production callers go away in `SERVER-GLOBAL-STORE-AUDIT`.
- The ~289 test callers update mechanically to the new names; the global
  itself stays as a test affordance.
- Naming alone is the documentation — any production code that imports the
  `*ForTest` variant is an obvious code-review red flag.

**Decision (end state, separate ticket): delete the globals entirely once
production migration completes.**

- Open a new ticket: **`TEST-GLOBAL-STORE-MIGRATION`**.
- Migrate tests to per-test container construction (`serviceregistry.NewContainer().Override("store", mockStore)`).
- Once all 289 sites are migrated, delete `GetGlobalStoreForTest`/`SetGlobalStoreForTest`.
- Estimated scope: M (1–2 days, mostly mechanical).

The rename now is the cheap step; the test migration is the right end state
but not on the critical path.

### Q4 — `IncludeAll()` vs explicit `Include(...)`: NAMED GROUPS

**Decision: extend `ServiceDef` with a `Groups []string` field and add
`Container.IncludeGroup(names ...string)`. Production uses named groups;
explicit `Include(name)` remains for tests and ad-hoc additions.**

New ServiceDef shape:

```go
type ServiceDef struct {
    Name   string
    Needs  []string
    Groups []string        // NEW: zero or more group names
    Build  func(*Container) (any, error)
}
```

New Container API:

```go
func (c *Container) IncludeGroup(names ...string) *Container
```

Registration example:

```go
serviceregistry.Register(serviceregistry.ServiceDef{
    Name:   "audiobook",
    Needs:  []string{"store"},
    Groups: []string{"core", "library"},
    Build:  ...,
})
```

Initial group conventions:
- `core` — W1 leaf + W2 cross-wired (always-on infrastructure)
- `ai` — W4 embedding/AI cluster (config-gated internally)
- `activity` — `activity` + `activitystore` (DatabasePath-gated externally)
- `plugins` — all UOS plugin registrations
- `scheduler` — `opregistry`, `ophub`, `batchpoller`, `updatescheduler`,
  `librarywatcher`

NewServer post-migration:

```go
c := serviceregistry.NewContainer().
    Override("store", store).
    Override("config", &config.AppConfig).
    IncludeGroup("core", "ai", "plugins", "scheduler").
    Include("system")    // ad-hoc additions still work
if cfg.DatabasePath != "" {
    c.IncludeGroup("activity")
}
```

Auditability: `grep -rn 'Groups.*"core"' internal/ --include="*.go"` tells
you exactly what `IncludeGroup("core")` pulls in. No central groups file.

This is **the new top-of-list ticket: `REGISTRY-NAMED-GROUPS`** (S, ½ day,
no risk). All existing register.go files keep working unchanged — `Groups`
is opt-in. Add it before `PLUGIN-DECOUPLE` lands so new services land with
group membership baked in.
