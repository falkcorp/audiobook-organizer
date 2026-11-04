<!-- file: docs/plans/platform-core/raw/00-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a74e877-60c2-4a55-9ee3-79e53d373c0c -->
<!-- last-edited: 2026-11-03 -->

# Platform-core raw data — index

**Evidence commit:** `748fd787a6d922c63dfb3bbb09da578a1418632e` (main on 2026-11-03).
Some files cite `57fd77ffc`; that commit is `748fd787a` plus documentation-only commits on
the spec branch (it adds `docs/plans/platform-core/STATE.md` only), so every code anchor is identical
at both SHAs.

All files were produced by read-only scouts, normalized to repo-relative paths, and passed the
per-commit confidentiality scrub before commit. Classifications marked "judgment" are the scout's,
and the spec re-checks any that a decision depends on.

| File | Lines | Covers (prompt §5 Phase 1 item) |
|---|---|---|
| [00-baseline-build.md](00-baseline-build.md) | — | §2.1: control build/vet and module-layout experiments |
| [01-package-inventory.md](01-package-inventory.md) | 292 | 1: every package, LOC, layer, degree, class |
| [02-import-graph-hotspots.md](02-import-graph-hotspots.md) | 589 | 2: coupling hotspots, `models`, `pkg/` importers |
| [03-layering-guard.md](03-layering-guard.md) | 102 | §2.3: `internal/arch/layering_test.go` assumptions |
| [04-database-store.md](04-database-store.md) | 656 | 3: `database.Store` (445 methods) by domain, 591 narrowed interfaces |
| [05-storage.md](05-storage.md) | 381 | 4: engines, 258 Pebble key families, migrations, backup |
| [06-tasks-ops.md](06-tasks-ops.md) | 550 | 5: ops v1/v2/v3, scheduler, `RunItems`, every registered op |
| [07-plugin-registry-sdk.md](07-plugin-registry-sdk.md) | 246 | 13 + §2.4: serviceregistry, plugin, sdk, every `init()` |
| [08-prior-art.md](08-prior-art.md) | 141 | 14: 2026-09-18 platform docs, layering, `docs/decisions/` |
| [09-http-api.md](09-http-api.md) | 920 | 6: 497 + 75 routes, middleware, wire contract, SSE |
| [10-auth-rbac.md](10-auth-rbac.md) | 202 | 7: authN/authZ, 13 permissions, secrets, gaps G1–G15 |
| [11-integrations.md](11-integrations.md) | 95 | 8: AI, download, metadata, media servers, notifications |
| [12-cross-cutting.md](12-cross-cutting.md) | 104 | 9: config, logging, metrics, events, backup, lifecycle, globals |
| [13-frontend.md](13-frontend.md) | 255 | 10: routes, nav, contexts, API client, embedding |
| [14-build-ci.md](14-build-ci.md) | 249 | 11: Makefile, `make ci`, tags, Docker, workflows, Go pins |
| [15-dependencies.md](15-dependencies.md) | 135 | 12: current vs latest Go/npm versions (checked 2026-11-03) |
| [16-arr-domain-research.md](16-arr-domain-research.md) | 572 | 15: Sonarr/Radarr domain, quality, acquisition, *arr v3 API callers |
