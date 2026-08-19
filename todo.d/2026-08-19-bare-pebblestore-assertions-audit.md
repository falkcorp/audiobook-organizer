- [x] **Bare `store.(*PebbleStore)` assertions swept — all 10 production sites
      converted (2026-08-19).** Zero remain outside `_test.go`, where they are
      correct: those build a bare `*PebbleStore` locally and never see the
      decorator. Found while removing the interface-shaped twin of this bug
      (#2580, 11 sites) — the concrete shape had never been swept.
      `internal/database/store_capability.go` documents the failure mode and
      records two prod jobs silently degraded for weeks by it.

- [x] **Provenance traced for every other site (2026-08-19).** The question that
      decides severity is **does this value come from `Server.Store()`?**
      `serviceregistry.Container.Build` runs **eagerly inside `NewServer`**, and
      `Override("store", resolvedStore)` seeds `KeyStore` with the **bare** store
      and is never replaced — so "built by a service-registry factory" means bare,
      lazily-built or not. `Start` installs the `indexedStore` wrapper afterwards
      onto `s.store` only, which is what `Server.Store()` returns.

      - `handlers/diagnostics.go` (`GetDBHealth`) — bare: the handler captures
        `s.Store()` in `wireHandlers` → `setupRoutes` → `NewServer`. Its methods
        running at request time does not change what it captured. Converted.
      - `wire_abs_routes.go:494` — **racy**, and the one real defect. Wiring runs
        inside `NewServer` like every other handler, but this site reads
        `s.Store()` *inside a goroutine*, so the read happens whenever that
        goroutine is scheduled — possibly after `Start` has written the wrapper.
        Construction time vs. read time is the whole distinction.
      - `scanner/process_file.go` — bare (`scanner.SetStore(resolvedStore)` in
        `NewServer`, never re-set). Converted defensively.
      - `dedup/lifecycle.go` — bare (`Get[dedup.Store](c, KeyStore)`). Defensive.
      - `dedup/engine.go` Tier-0 LSH lookup — bare, same reason. Defensive (#2598).
      - `database/migrations.go` — bare by construction. Defensive.
      - `plugins/dedup/*`, `plugins/acoustid/lsh_backfill.go` — all assert narrow
        interfaces that `database.Store` does not carry, but all hold the bare
        store via the container. Left alone.
      - `server/search_coverage.go`, `server/middleware/absauth.go`,
        `operations/registry/legacy_op_status.go`,
        `handlers/audiobooks/handler.go`, `handlers/audiobooks/handler_files.go` —
        compile-probed: every asserted method **is** in `database.Store`, so these
        resolve through the decorator regardless. Safe as written.

      Test-file assertions are out of scope: those build a bare `*PebbleStore`
      locally and never see the decorator.
