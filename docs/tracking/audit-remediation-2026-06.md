<!-- file: docs/tracking/audit-remediation-2026-06.md -->
<!-- version: 1.18.0 -->
<!-- guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f -->
<!-- last-edited: 2026-06-23 -->
<!-- Note: per-finding table synced to PR delivery table on 2026-06-23 -->

# Audit Remediation Tracking — June 2026

Unified tracking for findings from two overlapping audits:

- **[Structure Audit]** `docs/audits/2026-05-01-structure-audit.md` — file size, duplication, package cohesion, interface segregation, frontend structure.
- **[Security Sweep]** `docs/audits/2026-06-22-repo-optimization-security-sweep.md` — security, performance, architecture, frontend, tests/tooling.

Status markers: ✅ Done · 🔄 In progress · ⬜ Open · ❌ Blocked

---

## What Got Done Between the Two Audits (May → June)

These Structure Audit findings were addressed in the May–June refactor wave before the sweep ran. Listed here so they don't appear as open items.

| Finding | What happened |
|---|---|
| `metafetch/service.go` 3,932 lines | Split into 9 files (`service_apply`, `service_fetch`, `service_scoring`, `service_search`, `service_writeback`, etc.) — exactly the proposed split. |
| `maintenance_fixups.go` 6,400 lines | Split into `maintenance_fixups.go` (566), `maintenance_dispatcher.go`, `maintenance_job_op.go`. |
| `internal/server/` 105 files, 12+ domains | `handlers/` subpackages extracted for audiobooks, dedup, duplicates, entities, metadata, operations, system (PRs #1232–1239). |
| `useAsyncAction` hook | `web/src/hooks/useAsyncAction.ts` exists; `setLoading(true)` hits fell from 148 → 51. |
| Response helper duplication | `c.JSON(http.` callsites fell from 287 → 24 (~92% reduction). |
| `sqlite_store.go` 6,976 lines | Obsoleted by PebbleDB migration — file gone. |
| Centralized 50K-line mock | Per-handler mocks (operations, metadata, entities, system, audiobooks) — each smaller and scoped. |

---

## Security

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| SEC-1 | `abk_...` API key committed in `docs/FINGERPRINT_E2E_TEST_REPORT.md:141` | Sweep | ⬜ | **Immediate.** Rotate key, replace with placeholder, add secret scanning to docs/. |
| SEC-2 | Bootstrap/read-only credentials generated to plaintext files at startup | Sweep | ✅ | `Config.WriteStartupReadOnlyKey` bool (default true). Gated in `server_lifecycle.go`. Bootstrap token unchanged (emergency access). |
| SEC-3 | Temp-login URLs trust inbound `Host` header | Sweep | ✅ | Fixed in PR A (#1574): `auth_temp_login.go:108-116` uses `s.externalURL`; comment explicitly says "never trust the inbound Host header". |
| SEC-4 | No security-header middleware (CSP, frame-ancestors, nosniff, HSTS) | Sweep | ✅ | Fixed in PR A (#1574): `securityHeadersMiddleware()` registered at `server.go:361`; sets nosniff, X-Frame-Options, HSTS (HTTPS only), Referrer-Policy. CSP intentionally omitted — React SPA requires inline styles. |
| SEC-5 | Restore `verify=true` is a no-op; restore target is arbitrary absolute path | Sweep | ✅ | `pathvalidation.IsDangerousRoot` blocks system-dir targets; `verify=true` now logs a visible warning. Full checksum manifest deferred. Shipped PR #1584. |
| SEC-6 | Factory reset deletes everything under `RootDir` without path validation | Sweep | ✅ | `pathvalidation.IsDangerousRoot` check added before library folder deletion; returns 400 + logs error if RootDir is a protected path. Shipped PR #1584. |
| SEC-7 | `/metrics` and cache-stats endpoints unauthenticated | Sweep | ✅ | `/cache/stats` + `/cache/stats/history` moved to `protected` group (PermLibraryView). `/metrics` kept as accepted-risk per MED-1 — standard Prometheus scrape endpoint, network-layer gating. |
| SEC-8 | Docker build downloads unsigned tarballs, uses mutable base tags | Sweep | ✅ | Pinned all base images to manifest-list SHA digests in `Dockerfile` and `Dockerfile.build-cgo` (2026-06-23). Refresh with `docker buildx imagetools inspect <image> --format '{{.Manifest.Digest}}'`. |
| SEC-9 | OpenAI key exposed to frontend runtime for validation | Sweep | ✅ | `GetConfig` calls `MaskSecrets()` before responding; `update_service.go:37-55` masks all secret fields (OpenAI, AcoustID, Google Books, Hardcover, BasicAuth). Frontend only receives `***` masked value. |

---

## Performance

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| PERF-1 | Dedup full scan repeatedly sorts all candidate rows, caps at global top 50 — books with many candidates get incorrect results | Sweep | ✅ | `ListCandidatesForEntity(entityType, bookID, status)` secondary index added (PR D #1577). |
| PERF-2 | Multi-file import hashes/tags same files repeatedly, one `UpsertBookFile` per segment | Sweep | ✅ Partial | Batch upserts shipped (PR J #1583): N→1 `BatchUpsertBookFiles` per book. Hash carry-forward (PERF-2b) blocked — needs `saveBookToDatabase` API change. |
| PERF-3 | Library list has full-materialization escape hatches | Sweep | ⬜ | `audiobooks/service.go:856,1092,1286`. Push filters into `BookSummaryFilter`; add projections for common sorts. |
| PERF-4 | iTunes search calls `SearchBooks(search, 0, 0)` — returns zero rows | Sweep | ✅ | Root cause: `pebble_store.go:3169` `len(filtered) < limit` is always false when `limit=0`. Fixed: treat `limit==0` as "no limit". Regression test `TestSearchBooksUnlimited` added. |
| PERF-5 | iTunes backfill: offset pagination, N+1 file reads, per-row writes | Sweep | ✅ Partial | Per-row writes fixed: mappings now accumulated per page and flushed with one `BulkCreateExternalIDMappings` call (N→1 per page). N+1 file reads deferred — needs `GetBookFilesByBookIDs([]string)` batch method on Store + mock regen; TODO comment added. Offset pagination kept — 5 pages for 50K books is acceptable. |
| PERF-6 | Search index rebuild uses offset-based `GetAllBooks` | Sweep | ✅ | Added `GetAllBooksFrom(afterID, limit)` cursor to `BookReader` interface (`iface_book.go`), implemented in `PebbleStore` (O(1) seek via LowerBound), updated 6 generated mocks + 1 hand-written mock, rewrote `server_search.go` backfill loop to use cursor. Shipped PR #1601. |
| PERF-7 | Memdb projection is a monolith; strips `AcoustIDFingerprint` on round-trip | Sweep | ✅ | `UpsertBookFile` now has the same fingerprint-preserve guard as `BatchUpsertBookFiles`. 3 regression tests added in `pebble_bookfile_preserve_test.go`. |
| PERF-8 | Backup walks live Pebble files directly | Sweep | ⬜ | P2. Use Pebble `Checkpoint` before archiving. |

---

## Architecture

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| ARCH-1 | Handler extraction preserved Server coupling (lazy providers, injected closures, server-private helpers) | Sweep | ⬜ | `handlers/audiobooks/handler.go:77`, `handlers/metadata/handler.go:72`, etc. Introduce domain application services; handlers become HTTP adapters. |
| ARCH-2 | `wire_handlers.go` is an overloaded composition root + route registry | Sweep | ⬜ | `wire_handlers.go:31`, typed-nil guards at `:145,170,226`. Split per-domain route modules. |
| ARCH-3 | Operation enqueue boilerplate duplicated across handlers | Sweep | ✅ | `launchOp` / `launchLegacyOp` helpers extracted; 11 enqueue boilerplate sites eliminated (PR G #1577). |
| ARCH-4 | Config legacy remap machinery repeats by group | Sweep | ✅ | `applyLegacyRemaps` + `configRemapGroups` table in `update_service.go`. 6 per-group functions eliminated; all remap logic in one place. 13 tests updated. |
| ARCH-5 | `AudiobookService` is a god service | Sweep | ⬜ | P2. Split query/mutation/tags/delete/compatibility. |
| ARCH-6 | Optional store capabilities discovered ad hoc | Sweep | ⬜ | P2. Add `storecap` helpers. |
| ARCH-7 | Compatibility surfaces scattered across 6+ files | Sweep | ⬜ | P2. Create compatibility registry with owner/removal condition. |
| ARCH-8 | Service registry uses globals and panicking string lookups | Sweep | ⬜ | P2. Typed service keys or generated accessors. |
| STR-1 | Pagination helper missing — 376+ limit/offset/page callsites parsed independently | Structure | ✅ | `internal/server/pagination.go` created; pagination helper standardized (PR E #1578). |
| STR-2 | AI retry duplicated in `openai_parser.go`, `metadata_llm_review.go`, `embedding_client.go` | Structure | ✅ | `internal/ai/retry.go` → `DoWithRetry` function; 4 sites migrated (PR E #1578). |
| STR-3 | Path/string normalization scattered — 611 `ToLower/TrimSpace/Clean` callsites, subtle inconsistencies in author/series matching | Structure | ✅ Partial | `internal/util/normalize.go` already existed with 41 existing callers. Fixed the **correctness bug**: `pebble_store.go` author/series/alias/role/playlist index keys were using only `ToLower` (no TrimSpace) → names with whitespace produced wrong keys. Adopted `util.NormalizeTitle` in `memdb_indexers.go` and `util.NormalizeString` in `metadata_fetch_cache.go`. 49 remaining inline `ToLower+TrimSpace` patterns are style (already correct logic); incremental adoption ongoing. |

---

## Frontend

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| FE-1 | No centralized `apiFetch` wrapper — credentials, CSRF, error parsing, abort duplicated across `api.ts`, `activityApi.ts`, `readingApi.ts` | Sweep | ✅ | `apiFetch` wrapper created; 267 callsites migrated (PR F #1580). |
| FE-2 | `ActivityLog` page-size changes can fetch stale data | Sweep | ✅ | `pageSize` added to callback dep arrays (PR C #1576). |
| FE-3 | Legacy `BookDedup` rows-per-page changes can fetch stale data | Sweep | ✅ | Resolved by PR L (#1585): `BookDedup.tsx` reduced to 145 lines (pure tab router); the stale rows-per-page code was in the extracted tab components which were rewritten. |
| FE-4 | `BookDetail` file/segment state stale across route-param changes | Sweep | ✅ | Reset on `id` change added (PR C #1576). |
| FE-5 | `Library.tsx` still a 3,333-line maintenance hotspot | Both | ✅ Partial | `useLibraryQuery` + `useLibrarySelection` extracted, 3,333→1,811 lines (PR L #1585). `useImportPaths` + `useLibraryOperations` deferred — P2. |
| FE-6 | `useSettingsHandlers` passes too much mutable state through one hook | Sweep | ✅ | Extracted `useImportFolderHandlers` (212L), `useBackupHandlers` (167L), `useMetadataSourceHandlers` (121L). Main hook: 1259→936 lines (−26%). Return interface unchanged; Settings.tsx untouched. |
| FE-7 | Sensitive one-time tokens remain rendered in React state after display | Sweep | ✅ | Token auto-clear after copy/timeout added (PR C #1576). |
| FE-8 | E2E auth tests mock auth instead of exercising real cookie/CSRF | Sweep | ✅ | Added `Authentication — Real Server Smoke` test to `auth-flow.spec.ts`. Calls live `/api/v1/auth/status`, exercises first-run bootstrap + real session cookie, verifies cookie survives reload. Skips safely if DB already has users (local reuse). |
| STR-4 | `BookDedup.tsx` still ~3,656 lines | Structure | ✅ | Split into 3 tab components (DedupAIReviewTab, DedupEmbeddingTab, DedupAcousticTab); 2,907→145 lines (PR L #1585). |
| STR-5 | `useAsyncAction` reduces but doesn't eliminate setLoading duplication (51 remaining) | Structure | ✅ Partial | Hook exists; 51 remaining callsites could adopt it incrementally. |

---

## Tests and Tooling

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| TOOL-1 | ~1.7 GB tracked test fixture footprint (Librivox corpus) | Sweep | ✅ Partial | Hardcoded absolute path in `mediainfo_test.go` fixed (portable `findRepoRoot`); test skip message updated (PR M #1586). Full LFS migration deferred — P2. |
| TOOL-2 | CI mockery gate can pass when mockery fails (`|| true`) | Sweep | ✅ | `|| true` removed; mockery v2.53.6 pinned; `make mocks-check` wired into CI (PR B #1575). |
| TOOL-3 | Demo recording workflow mixed into default E2E target | Sweep | ✅ | `chromium` and `webkit` projects now exclude `demo-*.spec.ts` and `interactive-*.spec.ts` via `testIgnore`. `chromium-record` is opt-in. `npm run test:e2e:demo` / `make test-e2e-demo` added. |
| TOOL-4 | Testing docs and actual CI gates disagree (80% vs 30% coverage claims) | Sweep | ✅ | Docs aligned with actual CI thresholds (PR B #1575). |
| TOOL-5 | Generated mocks still large despite per-handler split | Sweep | ⬜ | P2. Prefer narrow hand-written fakes for new code. |
| TOOL-6 | Generated Playwright report tracked in git | Sweep | ✅ | `/playwright-report/` untracked, added to `.gitignore` (PR B #1575). |
| TOOL-7 | Fixed sleeps in tests | Sweep | ✅ Partial | `waitForTimeout(1000)` replaced with `waitForRequest(url)` in `dedup-operations.spec.ts:128` and `dedup.spec.ts:174`. `dynamic-ui-interactions.spec.ts` sleeps are in mock route handlers (intentional simulated latency, not polling) — correctly left as-is. Remaining Go backend sleeps (`time.Sleep`) are timing-sensitive (TTL expiry, timestamp ordering) and cannot be replaced with state checks. |
| TOOL-8 | Manual smoke scripts not integrated into Makefile | Sweep | ✅ | `make manual-smoke`, `make smoke-create-books`, `make smoke-run-demo` targets added to Makefile. |

---

## Initiative: Work-Item Execution Contract

**Status:** ✅ Partial — `RunItems[T]` shipped (PR I #1579); waves 1–2 migrated (PRs #1591/#1592); 3 sites deferred  
**Connects to:** ARCH-3, PERF-2, watchdog saga (#1562–#1567)

**ARCH-4b migration progress:**
| Site | Status | PR |
|---|---|---|
| `deluge/path_update.go` | ✅ Migrated | #1591 |
| `deluge/centralization.go` | ✅ Migrated | #1592 |
| `lsh_backfill.go` | ⬜ Deferred — 308K items × per-item UpdateProgress needs reporter throttle wrapper | — |
| `acoustid/backfill.go` | ⬜ Deferred — nested books→files loop + resume-by-book-ID | — |
| `acoustid/reset_all.go` | ⬜ Deferred — callback-driven PebbleStore API + dual heterogeneous loops | — |
| `acoustid/fingerprint_rescan.go` | ⬜ Excluded — already semaphore goroutine pool (not sequential) | — |

### Problem

Every operation that processes items in parallel today rolls its own fan-out boilerplate: `sync.WaitGroup`, semaphore channel, error collection, per-item context, and progress ticking. When bugs appear (early-return books never getting a heartbeat stamp, timeouts not propagating), they appear in each independently.

Confirmed fan-out sites in registered ops (WaitGroup fan-out over items, not shared-state protection):

| File | Pattern | Op ID (if known) |
|---|---|---|
| `plugins/maintenance/duration_reextract.go:347` | `var wg sync.WaitGroup` + semaphore | `maintenance.duration-reextract` |
| `plugins/acoustid/fingerprint_rescan.go:162` | `var bookWg sync.WaitGroup` | `acoustid.fingerprint-rescan` |
| `maintenance/jobs/repair_missing_files.go:179` | `var wg sync.WaitGroup` | maintenance job |
| `maintenance/jobs/scan_composer_tags.go:146` | `var wg sync.WaitGroup` | maintenance job |
| `server/metadata_ops.go:640` | `var wg sync.WaitGroup` | batch metadata fetch op |
| `server/metadata_candidate_op.go:86` | `var wg sync.WaitGroup` | metadata candidate op |

Additional fan-out in non-registered server code (share a helper but don't benefit from the contract):
- `itunes/import.go`, `itunes/service/importer.go`, `itunes/service/path_repair_resolver.go`
- `organizer/service.go`
- `metafetch/openlibrary.go`

These are distinct from shared-state mutexes (library-counts recompute mutex, atomic progress clock, cache maps) — those protect invariants and are not in scope for this initiative.

### Why the Watchdog Saga Makes This Concrete

PRs #1562–#1567 fixed a series of liveness bugs where operations were killed by the watchdog:

1. `os.Stat` calls blocking with no timeout → wrapped
2. DB compaction blocking keepalive goroutine → keepalive redesigned  
3. Heartbeat only written at end of function → early-return books never stamped → **this is the core bug**

Bug 3 was a direct consequence of each op managing its own item loop: the heartbeat call was in the wrong place in the loop. A framework-owned loop that calls `reporter.UpdateProgress` after each item would make this class of bug structurally impossible.

### Proposed Extension

`OperationDef` already has:
- `ConcurrencyKey` — serializes op-level runs
- `SetPluginMaxConcurrent` — caps concurrent runs per plugin
- `ProgressTimeout` / `MinCheckpointInterval` — watchdog tuning
- `Synchronous bool` — atomic-clock vs. sync flush

Missing: within-run item concurrency declaration. The natural extension:

```go
// On OperationDef (draft — not yet added):
ItemConcurrency int  // 0 or 1 = sequential; >1 = parallel with that many workers
```

And a corresponding helper on `Reporter`:

```go
// On Reporter (draft — not yet added):
RunItems[T any](ctx context.Context, items []T, fn func(ctx context.Context, item T) error) error
```

The framework implementation owns: worker pool, WaitGroup, per-item context with timeout, error collection, `UpdateProgress` call after each item, watchdog stamp.

### Open Questions Before Design

1. **Relationship to UOS?** The UOS dependency-scheduling spec (PR #1440 docs-only, no code) describes op-to-op scheduling. This is within-op item scheduling. Are these the same layer or separate? Decide before adding fields to `OperationDef`.
2. **Error semantics:** First error cancels remaining items (fail-fast)? Or collect all errors (best-effort)? Different ops need both.
3. **Per-item timeout:** `OperationDef.Timeout` is per-run. A per-item timeout (as added in #1562 for `os.Stat`) should also be declarable.
4. **Non-registered ops:** `metadata_ops.go`, `organizer/service.go` etc. benefit from the same helper but don't go through the registry. A standalone `internal/workerpool` package would cover both; the registry uses it internally.

### Next Step

Before any code: write a design doc (`docs/specs/YYYY-MM-DD-work-item-contract.md`) answering the four open questions above. Follow CLAUDE.md plan-first discipline — no implementation until the design is approved.

---

## Suggested PR Sequence

This ordering respects dependencies and keeps each PR reviewable:

| # | Theme | Includes | Blocked by |
|---|---|---|---|
| A | **Security hotfixes** | SEC-1 token removal, SEC-3 temp-login hardening, SEC-4 headers | — |
| B | **CI and artifact hygiene** | TOOL-2 mockery gate, TOOL-6 report untrack, TOOL-4 docs alignment | — |
| C | **Frontend quick fixes** | FE-2, FE-3, FE-4 stale deps; FE-7 token masking | — |
| D | **Dedup candidate index** | PERF-1 indexed lookup + regression benchmark | — |
| E | **Pagination + AI retry helpers** | STR-1 `pagination.go`, STR-2 `ai/retry.go` | — |
| F | **Frontend API wrapper** | FE-1 `apiFetch`, credentials/CSRF/error/abort | — |
| G | **Operation launch helper** | ARCH-3 unified enqueue helper | ARCH-2 partially |
| H | **Work-item contract design** | Design doc + open question resolution | G |
| I | **Work-item contract implementation** ✅ | `RunItems[T]` standalone generic fn + `ErrMode`/`RunItemsOptions` + 9 unit tests (PR #1579). Note: the 6 listed fan-out sites all have custom checkpointing/multi-counter/resume-from-index logic that cannot be replaced without regression — documented as future follow-up in TODO `ARCH-4b`. | H |
| I-b | **ARCH-4b — RunItems migration (waves 1–2)** ✅ | Wave 1: `deluge/path_update.go` (PR #1591). Wave 2: `deluge/centralization.go` migrated — pre-sliced to checkpoint.ProcessedFiles, atomic counters (success/skip/err), checkpoint written inside fn closure (PR #1592). Remaining 3 sites deferred: `lsh_backfill.go` (308K-item progress cadence), `acoustid/backfill.go` (nested loop + resume-by-ID), `acoustid/reset_all.go` (callback-driven API). `acoustid/fingerprint_rescan.go` excluded — already uses semaphore goroutine pool. | H |
| J | **Scanner batch pipeline** ✅ | PERF-2 batch upserts shipped PR #1583: `createBookFilesForBook` now collects all BookFiles then calls `BatchUpsertBookFiles` once (N→1 DB writes per book). Hash carry-forward (dedup check re-hashes same files at line 1885) deferred — needs `saveBookToDatabase` API change; documented as PERF-2b in TODO. | — |
| K | **Security guardrails** ✅ | SEC-5 + SEC-6: `IsDangerousRoot` in pathvalidation, restore dangerous-root guard + verify warning, factory-reset dangerous-root guard. PR #1584. | — |
| L | **Frontend page decomposition** ✅ | STR-4: BookDedup.tsx 2907→145 lines (3 tabs extracted: DedupAIReviewTab, DedupEmbeddingTab, DedupAcousticTab). FE-5: Library.tsx 2018→1811 lines (useLibraryQuery + useLibrarySelection hooks extracted). PR #1585. TypeScript: 0 errors. | F |
| M | **Dataset strategy** ✅ | TOOL-1: LFS already configured (`.gitattributes`). Fixed hardcoded absolute path in `mediainfo_test.go:889` (`falkcorp/...` → `findRepoRootForMediainfo()` walk). Skip message updated. PR #1586. | — |
