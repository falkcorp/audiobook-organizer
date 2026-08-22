# Scope 09 — 19 items

## ITEM L4678 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] `internal/database/store.go` — `globalStore` is guarded by `globalStoreMu sync.RWMutex`
      (`:1217`) but three of five accesses bypass it: `InitializeStore` writes bare (`:1261`),
      `CloseStore` reads bare (`:1275`) and writes bare (`:1276`). `:1280` is a
      `time.Sleep(100 * time.Millisecond)` commented "brief pause to let in-flight goroutines
      notice the nil" — a race workaround, not a fix. Blast radius is test-only today (zero
      production readers of the global), which is exactly why it becomes production-critical
      the moment a `GetGlobalStore()` call is reintroduced.

## ITEM L4685 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/server/wire_abs_routes.go

- [ ] `internal/server/wire_abs_routes.go:494` — bare `s.Store().(*database.PebbleStore)`
      assertion inside a goroutine, the literal form `internal/database/store_capability.go:44`
      forbids. `Server.Store()` (`server.go:331-333`) reads `s.store` with no lock while
      `server_lifecycle.go:362` writes `s.store = wrapped`; the goroutine is launched from
      `setupRoutes()` inside `NewServer`, i.e. before `Start()`. The data race is certain;
      **which side wins is not** — this is not a claim that warmup is skipped in prod.

**Capability pattern — the historically-realized defect class**

## ITEM L4694 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] `internal/database/iface_assert.go` — its comment claims compile-time proof that
      `PebbleStore` satisfies *every* sub-interface. It asserts **36 of 40**. Missing:
      `OAuthIdentityStore`, `MetadataCacheStore`, `RejectedMetadataStore`, `ReviewStore`.
      One line each.

## ITEM L4698 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/merge | all_domains_guess: internal/merge;internal/plugins/acoustid

- [ ] `internal/merge/service.go:34-42` — `AsExternalIDReassigner` uses a bare
      `s.(ExternalIDReassigner)` instead of `database.AsCapability`. Called on `ms.db` at
      `:236` and `:377`. Latent today (registry-built `merge.Service` holds the bare store),
      but one wiring change turns it into silent skipping of iTunes-PID/ASIN reassignment on
      merge. Same shape at `internal/plugins/acoustid/reset_all.go:69` and `lsh_backfill.go:86`.

## ITEM L4703 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/operations | all_domains_guess: internal/operations

- [ ] `internal/operations/registry/register.go:40-42` — `prodSchedulerStore` embeds
      `database.Store` and adds `BookFiles`, but does not implement `StoreUnwrapper`.
      Defect-*shaped*, not live: no capability lookup currently runs through it.

> Context for why this class is not hypothetical: `internal/server/server_lifecycle.go:1737-1766`
> documents the **third** capability lost to the same decorator, measured in production
> 2026-08-10 23:07:40 — the version-group index backfill "had NEVER ONCE RUN, silently" since
> the decorator was installed, and is the likely origin of the under-reporting in #2277.

**Comments that are false at HEAD**

## ITEM L4714 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/importer | all_domains_guess: internal/importer

- [ ] `internal/importer/service.go:27-31` — `type Store = database.Store` justified by
      "`versions.CreateIngestVersion` requires the full Store interface." It uses **4 methods**.

## ITEM L4716 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers

- [ ] `internal/server/handlers/organize.go:57-62` — `type OrganizeStore = database.Store`
      justified by `organizer.SetStore` and `deluge.NotifyDelugeAfterOrganize`. At HEAD those
      take a 4-method `OrganizerStore` and an anonymous `interface{ database.BookVersionStore }`.

## ITEM L4719 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup

- [ ] `internal/dedup/collectors_metadata.go:51` — "`database.EnsureSingletonBookTag` (which
      requires the full Store interface)." It uses **3**.

## ITEM L4721 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/database | all_domains_guess: internal/database;docs

- [ ] `internal/database/store.go:17` cites
      `docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md`, which is not on
      main. Recoverable via `git show 29e256ac:<path>`. Either restore the doc or repoint the
      reference to `docs/archive/superpowers/plans/`.

**Test quality**

## ITEM L4728 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] ⚠ `internal/database/mock_store.go` — ~88 of `MockStore`'s 399 methods have no `Func`
      override field and are hardwired to a zero return no test can change.
      `GetAllAuthorBookCounts` (`:863`) returns `map[int]int{}, nil` unconditionally, so
      `TestListAuthors_Success` asserts against a response where every author has `BookCount: 0`.

## ITEM L4732 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/server/organize_service_test.go | all_domains_guess: internal/server/organize_service_test.go

- [ ] ⚠ `internal/server/organize_service_test.go:34` — vacuous test. It sets `GetAllBooksFunc`;
      the code under test calls `GetAllBooksCore`, whose func field is unset → `nil, nil`.
      `TestOrganizeService_PerformOrganize_NoBooksToOrganize` asserts only `err == nil` and
      passes against a mock wired to nothing.

**Dead generated code (part of the audit's step 1, listed here for tracking)**

## ITEM L4739 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/scanner | all_domains_guess: internal/scanner

- [ ] `internal/scanner/mocks` — 442 generated lines, **zero** importers, while
      `internal/scanner`'s own tests hand-roll `fullMockScanner`
      (`scanner_coverage_test.go:655`) because importing the mocks package would cycle.
      Delete the `Scanner:` entry from `.mockery.yaml`; keep the hand-written double.

## ITEM L4743 [tier C] section: Store-interface audit — defects found incidentally (each independent o
primary_domain_guess: internal/operations | all_domains_guess: internal/operations

- [ ] `internal/operations/mocks` — 206 generated lines, effectively unreferenced.

## ITEM L4832 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Recovery tool. Dry-run by default, full report before anything moves.
      **77 books have no other copy — a wrong move is unrecoverable.** Derive
      and validate the naming rule against the 5 books that also contain
      surviving audio (Project Hail Mary, Singularity Online 1, Welcome to the
      Multiverse 5, Dreamcatcher, Neuromancer) before pointing it at the other
      77. Reconstruct by rejoining directory tail + filename
      (`"Pink Bean Series - 1" + " " + "9.m4b"`), not by relocating the bare
      file, which discards the chapter's identity.

## ITEM L4840 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Compare with per-file SHA-256; where hashes differ because of embedded
      artwork, fall back to `ffmpeg -v error -i FILE -map 0:a -f md5 -`, which
      hashes decoded audio and ignores container metadata. Exact, unlike
      AcoustID — only exact should authorize a delete.

## ITEM L4844 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Investigate book rows affected as a side effect. `scrubVar`'s own comment
      records the scanner reacting by creating **85 separate Book records** for
      one book, so look for spurious rows (path segment matching ` - [0-9]+$`,
      or a purely numeric title) *before* doing soft-delete/purge archaeology.

## ITEM L4848 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Confirm no new bogus directories appear now that both the pattern and the
      builder guard are in place. The post-fix observation window is currently
      about zero.

## ITEM L4852 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: internal/scanner | all_domains_guess: internal/scanner

- [ ] **Give the AI parser typed provider errors.** `internal/scanner/ai_failure.go`
      decides whether an AI failure is permanent by substring-matching the error text
      (`insufficient_quota`, `invalid_api_key`, …) because `aiParser.ParseBatch` flattens
      the HTTP status and the provider's error code into a `fmt.Errorf` string several
      layers down. Return a typed error carrying status + provider code so the check can
      be `errors.As` instead of `strings.Contains`. The current matcher is safe to miss —
      the phase still stops after 3 consecutive failures — but a miss costs ~60s of
      guaranteed-failing calls per scan.

## ITEM L4861 [tier C] section: Stranded `.tmp-rename` recovery — bisect complete, recovery outstandin
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Maintenance window: watchdog cancels it, then the plugin reports success.** Prod
      cancelled the maintenance window at 331s idle, after which the plugin logged
      "completed successfully (100%)". Pre-existing disagreement, but newly consequential
      after #2483: the legacy operations row is now mirrored as `canceled` while the op's
      own log claims success, so the two records actively contradict each other.

