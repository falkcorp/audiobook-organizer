<!-- file: docs/tracking/audit-remediation-2026-06.md -->
<!-- version: 1.4.0 -->
<!-- guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f -->
<!-- last-edited: 2026-06-22 -->

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
| SEC-2 | Bootstrap/read-only credentials generated to plaintext files at startup | Sweep | ⬜ | `server_lifecycle.go:408`, `bootstrap.go:107,152,338`. Make recovery opt-in or local-only. |
| SEC-3 | Temp-login URLs trust inbound `Host` header | Sweep | ⬜ | `auth_temp_login.go:114`. Use configured canonical URL or Host allowlist. |
| SEC-4 | No security-header middleware (CSP, frame-ancestors, nosniff, HSTS) | Sweep | ⬜ | Gap around `server_middleware.go:21`. |
| SEC-5 | Restore `verify=true` is a no-op; restore target is arbitrary absolute path | Sweep | ✅ | `pathvalidation.IsDangerousRoot` blocks system-dir targets; `verify=true` now logs a visible warning. Full checksum manifest deferred. Shipped PR #1584. |
| SEC-6 | Factory reset deletes everything under `RootDir` without path validation | Sweep | ✅ | `pathvalidation.IsDangerousRoot` check added before library folder deletion; returns 400 + logs error if RootDir is a protected path. Shipped PR #1584. |
| SEC-7 | `/metrics` and cache-stats endpoints unauthenticated | Sweep | ⬜ | P2. Gate behind auth or internal bind. |
| SEC-8 | Docker build downloads unsigned tarballs, uses mutable base tags | Sweep | ⬜ | P2. Pin base digest, verify SHA256. |
| SEC-9 | OpenAI key exposed to frontend runtime for validation | Sweep | ⬜ | P2. Move validation server-side. |

---

## Performance

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| PERF-1 | Dedup full scan repeatedly sorts all candidate rows, caps at global top 50 — books with many candidates get incorrect results | Sweep | ⬜ | `dedup/engine.go:2032,404`, `embedding_store.go:608,663`. Add `ListCandidatesForEntity(entityType, bookID, status)` with index. |
| PERF-2 | Multi-file import hashes/tags same files repeatedly, one `UpsertBookFile` per segment | Sweep | ⬜ | `scanner.go:1885,1307,1322,1327`. Carry hash/tag results forward; batch upserts; recompute aggregates once per book. |
| PERF-3 | Library list has full-materialization escape hatches | Sweep | ⬜ | `audiobooks/service.go:856,1092,1286`. Push filters into `BookSummaryFilter`; add projections for common sorts. |
| PERF-4 | iTunes search calls `SearchBooks(search, 0, 0)` — returns zero rows | Sweep | ⬜ | `handlers/itunes.go:693`. Add bounded iTunes-filtered search. |
| PERF-5 | iTunes backfill: offset pagination, N+1 file reads, per-row writes | Sweep | ⬜ | `itunes/backfill.go:21,46,73`. Cursor iteration, batch file lookup, bulk writes. |
| PERF-6 | Search index rebuild uses offset-based `GetAllBooks` | Sweep | ⬜ | `server_search.go:63`. Add streaming/cursor book iteration. |
| PERF-7 | Memdb projection is a monolith; strips `AcoustIDFingerprint` on round-trip | Sweep | ⬜ | **P1, not P2.** `memdb_strip.go`. `UpsertBookFile` still has the latent data-loss bug. Split projections by query family. |
| PERF-8 | Backup walks live Pebble files directly | Sweep | ⬜ | P2. Use Pebble `Checkpoint` before archiving. |

---

## Architecture

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| ARCH-1 | Handler extraction preserved Server coupling (lazy providers, injected closures, server-private helpers) | Sweep | ⬜ | `handlers/audiobooks/handler.go:77`, `handlers/metadata/handler.go:72`, etc. Introduce domain application services; handlers become HTTP adapters. |
| ARCH-2 | `wire_handlers.go` is an overloaded composition root + route registry | Sweep | ⬜ | `wire_handlers.go:31`, typed-nil guards at `:145,170,226`. Split per-domain route modules. |
| ARCH-3 | Operation enqueue boilerplate duplicated across handlers | Sweep | ⬜ | `handlers/operations/handler.go:110`, `handlers/duplicates/handler.go:180,388,426,570`. Build one operation launch service. |
| ARCH-4 | Config legacy remap machinery repeats by group | Sweep | ⬜ | `config/update_service.go:70,102,138,170,206`. Centralize remap tables. |
| ARCH-5 | `AudiobookService` is a god service | Sweep | ⬜ | P2. Split query/mutation/tags/delete/compatibility. |
| ARCH-6 | Optional store capabilities discovered ad hoc | Sweep | ⬜ | P2. Add `storecap` helpers. |
| ARCH-7 | Compatibility surfaces scattered across 6+ files | Sweep | ⬜ | P2. Create compatibility registry with owner/removal condition. |
| ARCH-8 | Service registry uses globals and panicking string lookups | Sweep | ⬜ | P2. Typed service keys or generated accessors. |
| STR-1 | Pagination helper missing — 376+ limit/offset/page callsites parsed independently | Structure | ⬜ | `internal/server/pagination.go` (proposed). Estimated 300+ callsites still open (not verified post-refactor). |
| STR-2 | AI retry duplicated in `openai_parser.go`, `metadata_llm_review.go`, `embedding_client.go` | Structure | ⬜ | `internal/ai/retry.go` (proposed). 3 files, small fix. |
| STR-3 | Path/string normalization scattered — 611 `ToLower/TrimSpace/Clean` callsites, subtle inconsistencies in author/series matching | Structure | ⬜ | P2. `internal/util/normalize.go` (proposed). Important for author name matching correctness. |

---

## Frontend

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| FE-1 | No centralized `apiFetch` wrapper — credentials, CSRF, error parsing, abort duplicated across `api.ts`, `activityApi.ts`, `readingApi.ts` | Sweep | ⬜ | `api.ts:891`, `activityApi.ts:141`, `readingApi.ts:62`. |
| FE-2 | `ActivityLog` page-size changes can fetch stale data | Sweep | ⬜ | `ActivityLog.tsx:358,384,411`. Add `pageSize` to callback deps. |
| FE-3 | Legacy `BookDedup` rows-per-page changes can fetch stale data | Sweep | ⬜ | `BookDedup.tsx:2249,2273`. Add dep or retire legacy tab. |
| FE-4 | `BookDetail` file/segment state stale across route-param changes | Sweep | ⬜ | `BookDetail.tsx:80,223`. Reset on `id` change. |
| FE-5 | `Library.tsx` still a 3,333-line maintenance hotspot | Both | ⬜ | Extract `useLibraryQuery`, `useLibrarySelection`, `useImportPaths`, `useLibraryOperations`. |
| FE-6 | `useSettingsHandlers` passes too much mutable state through one hook | Sweep | ⬜ | `useSettingsHandlers.ts:40`. Split by domain. |
| FE-7 | Sensitive one-time tokens remain rendered in React state after display | Sweep | ⬜ | `TempLoginTab.tsx:32,139`, `APIKeysTab.tsx:109`. Auto-clear after copy/timeout. |
| FE-8 | E2E auth tests mock auth instead of exercising real cookie/CSRF | Sweep | ⬜ | Add one real-server smoke test. |
| STR-4 | `BookDedup.tsx` still ~3,656 lines | Structure | ⬜ | Split into feature sections. |
| STR-5 | `useAsyncAction` reduces but doesn't eliminate setLoading duplication (51 remaining) | Structure | ✅ Partial | Hook exists; 51 remaining callsites could adopt it incrementally. |

---

## Tests and Tooling

| ID | Finding | Source | Status | Notes |
|---|---|---|---|---|
| TOOL-1 | ~1.7 GB tracked test fixture footprint (Librivox corpus) | Sweep | ⬜ | Move large corpus to optional/LFS-on-demand; keep tiny fixtures in repo. |
| TOOL-2 | CI mockery gate can pass when mockery fails (`|| true`) | Sweep | ⬜ | `.github/workflows/ci.yml:79`. Remove `|| true`, pin mockery, call `make mocks-check`. |
| TOOL-3 | Demo recording workflow mixed into default E2E target | Sweep | ⬜ | `playwright.config.ts:37`. Split demo to opt-in `test:e2e:demo`. |
| TOOL-4 | Testing docs and actual CI gates disagree (80% vs 30% coverage claims) | Sweep | ⬜ | Consolidate `README.md`, `TESTING.md`, `CLAUDE.md` to derive claims from Makefile/CI. |
| TOOL-5 | Generated mocks still large despite per-handler split | Sweep | ⬜ | P2. Prefer narrow hand-written fakes for new code. |
| TOOL-6 | Generated Playwright report tracked in git | Sweep | ⬜ | Untrack `/playwright-report/`, ignore root report output. |
| TOOL-7 | Fixed sleeps in tests | Sweep | ⬜ | P2. Use locator/API waits and injected clocks. |
| TOOL-8 | Manual smoke scripts not integrated into Makefile | Sweep | ⬜ | P2. Wrap as explicit `make manual-smoke` targets. |

---

## Initiative: Work-Item Execution Contract

**Status:** ⬜ Design — open questions, no code yet  
**Connects to:** ARCH-3, PERF-2, watchdog saga (#1562–#1567)

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
| J | **Scanner batch pipeline** ✅ | PERF-2 batch upserts shipped PR #1583: `createBookFilesForBook` now collects all BookFiles then calls `BatchUpsertBookFiles` once (N→1 DB writes per book). Hash carry-forward (dedup check re-hashes same files at line 1885) deferred — needs `saveBookToDatabase` API change; documented as PERF-2b in TODO. | — |
| K | **Security guardrails** ✅ | SEC-5 + SEC-6: `IsDangerousRoot` in pathvalidation, restore dangerous-root guard + verify warning, factory-reset dangerous-root guard. PR #1584. | — |
| L | **Frontend page decomposition** ✅ | STR-4: BookDedup.tsx 2907→145 lines (3 tabs extracted: DedupAIReviewTab, DedupEmbeddingTab, DedupAcousticTab). FE-5: Library.tsx 2018→1811 lines (useLibraryQuery + useLibrarySelection hooks extracted). PR #1585. TypeScript: 0 errors. | F |
| M | **Dataset strategy** | TOOL-1 optional large corpus | — |
