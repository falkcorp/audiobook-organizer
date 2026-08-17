### Store-interface audit — defects found incidentally (each independent of the refactor)

Surfaced while producing `docs/audits/2026-08-16-store-interface-decomposition.md` (§11).
Measured at `8011a755`. **None is caused by that proposal**; they are filed separately so the
proposal's scope stays reviewable. Items marked ⚠ are agent-reported and not hand-verified.

**Concurrency / correctness**

- [ ] `internal/database/store.go` — `globalStore` is guarded by `globalStoreMu sync.RWMutex`
      (`:1217`) but three of five accesses bypass it: `InitializeStore` writes bare (`:1261`),
      `CloseStore` reads bare (`:1275`) and writes bare (`:1276`). `:1280` is a
      `time.Sleep(100 * time.Millisecond)` commented "brief pause to let in-flight goroutines
      notice the nil" — a race workaround, not a fix. Blast radius is test-only today (zero
      production readers of the global), which is exactly why it becomes production-critical
      the moment a `GetGlobalStore()` call is reintroduced.
- [ ] `internal/server/wire_abs_routes.go:494` — bare `s.Store().(*database.PebbleStore)`
      assertion inside a goroutine, the literal form `internal/database/store_capability.go:44`
      forbids. `Server.Store()` (`server.go:331-333`) reads `s.store` with no lock while
      `server_lifecycle.go:362` writes `s.store = wrapped`; the goroutine is launched from
      `setupRoutes()` inside `NewServer`, i.e. before `Start()`. The data race is certain;
      **which side wins is not** — this is not a claim that warmup is skipped in prod.

**Capability pattern — the historically-realized defect class**

- [ ] `internal/database/iface_assert.go` — its comment claims compile-time proof that
      `PebbleStore` satisfies *every* sub-interface. It asserts **36 of 40**. Missing:
      `OAuthIdentityStore`, `MetadataCacheStore`, `RejectedMetadataStore`, `ReviewStore`.
      One line each.
- [ ] `internal/merge/service.go:34-42` — `AsExternalIDReassigner` uses a bare
      `s.(ExternalIDReassigner)` instead of `database.AsCapability`. Called on `ms.db` at
      `:236` and `:377`. Latent today (registry-built `merge.Service` holds the bare store),
      but one wiring change turns it into silent skipping of iTunes-PID/ASIN reassignment on
      merge. Same shape at `internal/plugins/acoustid/reset_all.go:69` and `lsh_backfill.go:86`.
- [ ] `internal/operations/registry/register.go:40-42` — `prodSchedulerStore` embeds
      `database.Store` and adds `BookFiles`, but does not implement `StoreUnwrapper`.
      Defect-*shaped*, not live: no capability lookup currently runs through it.

> Context for why this class is not hypothetical: `internal/server/server_lifecycle.go:1737-1766`
> documents the **third** capability lost to the same decorator, measured in production
> 2026-08-10 23:07:40 — the version-group index backfill "had NEVER ONCE RUN, silently" since
> the decorator was installed, and is the likely origin of the under-reporting in #2277.

**Comments that are false at HEAD**

- [ ] `internal/importer/service.go:27-31` — `type Store = database.Store` justified by
      "`versions.CreateIngestVersion` requires the full Store interface." It uses **4 methods**.
- [ ] `internal/server/handlers/organize.go:57-62` — `type OrganizeStore = database.Store`
      justified by `organizer.SetStore` and `deluge.NotifyDelugeAfterOrganize`. At HEAD those
      take a 4-method `OrganizerStore` and an anonymous `interface{ database.BookVersionStore }`.
- [ ] `internal/dedup/collectors_metadata.go:51` — "`database.EnsureSingletonBookTag` (which
      requires the full Store interface)." It uses **3**.
- [ ] `internal/database/store.go:17` cites
      `docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md`, which is not on
      main. Recoverable via `git show 29e256ac:<path>`. Either restore the doc or repoint the
      reference to `docs/archive/superpowers/plans/`.

**Test quality**

- [ ] ⚠ `internal/database/mock_store.go` — ~88 of `MockStore`'s 399 methods have no `Func`
      override field and are hardwired to a zero return no test can change.
      `GetAllAuthorBookCounts` (`:863`) returns `map[int]int{}, nil` unconditionally, so
      `TestListAuthors_Success` asserts against a response where every author has `BookCount: 0`.
- [ ] ⚠ `internal/server/organize_service_test.go:34` — vacuous test. It sets `GetAllBooksFunc`;
      the code under test calls `GetAllBooksCore`, whose func field is unset → `nil, nil`.
      `TestOrganizeService_PerformOrganize_NoBooksToOrganize` asserts only `err == nil` and
      passes against a mock wired to nothing.

**Dead generated code (part of the audit's step 1, listed here for tracking)**

- [ ] `internal/scanner/mocks` — 442 generated lines, **zero** importers, while
      `internal/scanner`'s own tests hand-roll `fullMockScanner`
      (`scanner_coverage_test.go:655`) because importing the mocks package would cycle.
      Delete the `Scanner:` entry from `.mockery.yaml`; keep the hand-written double.
- [ ] `internal/operations/mocks` — 206 generated lines, effectively unreferenced.
- [ ] `Makefile` `check-mock-fresh` — runs `go generate` where the repo has **zero**
      `//go:generate` directives, so the diff is always clean and the gate can never fail.
      It runs in `make ci`. Delete it or give it a real directive.
