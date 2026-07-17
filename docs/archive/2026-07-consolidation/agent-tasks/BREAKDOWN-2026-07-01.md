<!-- file: docs/agent-tasks/BREAKDOWN-2026-07-01.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f8b1a20-6c4d-4e19-9a7b-1d2e3f405162 -->
<!-- last-edited: 2026-07-01 -->

# Agent-Task Breakdown & Fan-Out Plan — 2026-07-01

This document is the planning output for turning the remaining open `TODO.md`
work into **weak-model-proof agent briefs** (`docs/agent-tasks/`), plus a
cost/efficiency strategy for fanning them out to Sonnet/Haiku workers.

It follows the same conventions as the existing packages: see
[`README.md`](README.md) (universal protocol) and [`ORCHESTRATION.md`](ORCHESTRATION.md)
(coordinator + workers, dependency waves).

## Method

Every remaining open TODO item was verified against the current codebase, then
sorted into three buckets. **Only Bucket 1 becomes agent briefs** — forcing
design-heavy or prod-verification items into weak-model briefs produces the
opposite of "excellent results."

---

## Bucket 1 — Authored as agent briefs (localized / mechanical / well-specced)

Grouped into 8 workstreams. Each task names a **model tier** (Haiku for truly
mechanical edits; Sonnet where logic, schema, or risk judgment is required) with
a one-line justification, and a **wave** so same-file tasks never run concurrently.

### ⚠️ Same-file collision rule (drives wave ordering)

These files are touched by multiple tasks — the tasks that share a file are put
in **different waves** so parallel workers never rebase-conflict:

| Shared file | Tasks that touch it | Resolution |
|-------------|---------------------|------------|
| `internal/dedup/engine.go` | WS1/T01 (guard), WS1/T02 (part-vs-whole), WS2/T03 (C5 live-capture) | serialize: wave1=T01, wave2=T02, wave3=WS2/T03 |
| `internal/dedup/dataset/builder.go` | WS2/T01 (C5-sig), WS2/T02 (C5-folder) | serialize: wave1=T01, wave2=T02 |
| `web/src/pages/Library.tsx` | WS4/T02 (quick-filters), WS4/T03 (tag-search), WS4/T04 (cache-bug) | serialize: wave1=T04 (bug), wave2=T02, wave3=T03 |
| `internal/ai/openai_parser.go` | WS7/T01→T02 chain | serialize per AI-RESP dependency chain |

### WS-1 — dedup-hardening (backend) · maps to DEDUP-INTRO-1 residual, CONS-15, CONS-FRAG-2

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | (dedup residual) | Add boilerplate-title + min-duration guard to `upsertExactCandidate` chokepoint (engine.go:1264) — closes the intro/outro exact-layer leak in one place | **Sonnet** | central chokepoint, all 5 emitters route through it; wrong guard suppresses real dups | 1 |
| T02 | CONS-15 | Part-vs-whole defense-in-depth guard in the exact emitter | **Sonnet** | dedup correctness logic + tests | 2 |
| T03 | CONS-FRAG-2 | Route `BookFiles>1` books to `OrganizeBookDirectory` in `organizeOneBook` (importer.go:1105) | **Sonnet** | file-move path, medium risk | 1 (diff file from T01/T02) |

### WS-2 — dedup-dataset (backend) · maps to C5, C5-sig, C5-folder, C7, C8

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | C5-sig | Offset/subsequence containment in `signatureRelation` (builder.go) | **Sonnet** | new relation logic | 1 |
| T02 | C5-folder | `sibling_parts` folder relation in `folderRelation` (builder.go) | **Haiku** | mechanical, mirrors existing branches | 2 |
| T03 | C5 | Wire `BuildExample`+`Classify` into the candidate-upsert path (embedding_store.go / engine.go) | **Sonnet** | touches hot path; ordering vs WS1 | 3 |
| T04 | C7 | JSONL export endpoint/CLI for `dedup:label:` examples | **Haiku** | read + serialize, no mutation | 1 |
| T05 | C8 | Auto-bug-filing GitHub issue per `not_dup` cluster | **Sonnet** | external side-effect; **depends on backfill (see Bucket 3)** | deferred |

### WS-3 — provenance-hash-chain (backend) · maps to HASH-CHAIN-1, HASH-CHAIN-3

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | HASH-CHAIN-1 | Add `DownloadHash` field to `book_files` (PebbleDB), populate from Deluge import, manual-set API | **Sonnet** | schema/field addition + migration semantics | 1 |
| T02 | HASH-CHAIN-3 | Integrity alert: flag `file_hash != original_file_hash` with no AO write on record | **Sonnet** | uses existing `OriginalFileHash` + `book_file_orig_hash` index | 1 |

### WS-4 — library-ui (frontend) · maps to EMB-UI-1, USER-QUICK-FILTERS, TAG-SEARCH, Library cache bug

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | EMB-UI-1 | "Download latest Ollama" deep-link on Settings embeddings section | **Haiku** | one static link, isolated file | 1 |
| T02 | USER-QUICK-FILTERS | Save current filter set as a named preset (persist per-user, kebab menu, Manage submenu) | **Sonnet** | new feature spanning FE + settings persistence | 2 |
| T03 | TAG-SEARCH | "has tag X" filter + browsable tag cloud on Library page | **Sonnet** | new filter UI + query wiring | 3 |
| T04 | (Library cache bug) | Clear `useLibraryCache` on all ~14 mutation handlers in Library.tsx (or thread `bypassCache`) | **Sonnet** | stale-data correctness across many handlers | 1 |

### WS-5 — perf-cleanup (backend, low priority) · maps to ARCH-4b, MAYDEPLOY-H5/H7, NUTSDB leak, CONS-13/CFG-2-D

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | ARCH-4b | Migrate `acoustid/reset_all.go` to `registry.RunItems` (last of 3 sites) | **Sonnet** | callback API + dual heterogeneous loops | 1 |
| T02 | MAYDEPLOY-H5 | `metadata-fetch-ids` per-book `GetAuthorByID` fast path when `len(bookIDs)<100` | **Sonnet** | perf branch, correctness parity | 1 |
| T03 | MAYDEPLOY-H7 | TTL-cache `isProtectedPath` / `GetAllImportPaths` at the two hot sites | **Sonnet** | cache invalidation correctness | 1 |
| T04 | NUTSDB-CLOSE | Fix `NutsActivityStore.Close()` goroutine lifecycle | **Haiku** | localized; **benign/optional** (process-lifetime singleton) | 1 |
| T05 | CONS-13 / CFG-2-D | Retire flat→nested compat shim in `internal/config/update_service.go` | **Sonnet** | migration removal; **gated on 1wk prod stability** | 1 |

### WS-6 — logging-slog (backend) · maps to SLOG-W13 residual

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | SLOG-W13a | Wire `logging.Info(ctx)` into `runBulkWriteBack` + ISBN enrichment goroutine | **Haiku** | mechanical replace of raw slog | 1 |
| T02 | SLOG-W13b | Same for iTunes sync ops + batch poller | **Haiku** | mechanical | 1 |
| T03 | SLOG-W13c | Same for scanner deep paths | **Haiku** | mechanical | 1 |

> Split into 3 small tasks because W13 was previously re-held for **context
> overflow** — keep each worker's file set small.

### WS-7 — ai-responses-migration (backend) · maps to AI-RESP-A/B/D/E/F · **⚠️ was [hold]**

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | AI-RESP-A | Migrate `metadata_llm_review.go` (single call) to `/v1/responses` | **Sonnet** | API-shape migration + response parsing | 1 |
| T02 | AI-RESP-B | Migrate `openai_parser.go` 6 single-shot sites | **Sonnet** | depends on A clean | 2 |
| T03 | AI-RESP-D | Migrate Batches API (`openai_batch.go`) — verify endpoint allowlist first | **Sonnet** | conditional on OpenAI batch `/v1/responses` support | 2 |
| T04 | AI-RESP-E | Migrate `aijobs.go` multi-turn (add `last_response_id`) | **Sonnet** | stateful multi-turn; biggest token win | 2 |
| T05 | AI-RESP-F | Delete remaining Chat Completions call sites in `internal/ai/` | **Haiku** | cleanup after A–E | 3 |

> **Do not touch `embedding_client.go`** (AI-RESP-C = explicit do-not-migrate marker).
> This whole workstream is **optional/deferred** until the team decides to pick up
> the Responses migration; the briefs exist so it's ready when greenlit.

### WS-8 — ci-flaky-fixes (tooling/test) · maps to the "Known flaky CI" TODO block

| Task | TODO id | Title | Tier | Why tier | Wave |
|------|---------|-------|------|----------|------|
| T01 | mock-freshness | Resolve mockery v2/v3 pin drift; regenerate + commit mocks scoped correctly | **Sonnet** | version-pin + scoped regen (repo-wide regen is a known footgun) | 1 |
| T02 | flaky-backup | Root-cause + fix `TestBackupEndpointsErrors` | **Sonnet** | diagnose, don't rerun-and-ignore | 1 |
| T03 | flaky-scan | Root-cause + fix `TestScanService_MultiChapterAudiobook` | **Sonnet** | diagnose flake | 1 |

---

## Bucket 2 — NOT briefs: needs brainstorm/design first

These are flagged for a human/strong-model design session before any code. Weak
models cannot produce good results here.

| TODO id | Why it needs design first |
|---------|---------------------------|
| WF-0…WF-6 | Whole pluggable-workflow subsystem; WF-0 *is* "run a brainstorm→spec session first." |
| 3.8 | Plex-style media-server API — new external-facing subsystem. |
| 4.1 / 4.7 | PostgreSQL research + per-workload store evaluation — research tracks. |
| 3.9 / 3.10 | LLM series detection / AI cover generation — model + UX design. |
| 1.17 | Product rename + logo — blocked on a name decision. |
| REPO-SIZE-1 | Git-history rewrite — needs a careful plan (see LFS note: 743MB is a safe local `git lfs prune`, but history externalization is not). |
| CONS-17b | Filesystem `resolveTitle` "all-chapters-agree" discriminator — album-preference already rejected; needs a small design. |
| 4.10 | Full four-service mock-store unit suite (`MergeService` residual) — mid-size test design; could become a brief later. |

---

## Bucket 3 — NOT tasks: operational / prod-verification (no code deliverable)

Require prod access, a running op, or a GitHub-console action — not agent work.

CONS-10 · PH-2 · PD-3 · I1 · I6 · MAYDEPLOY-I1 · MAYDEPLOY-I6 · verify-14k ·
verify-booksig · SEC-AUDIT-11 · SLOG-PROD-VERIFY · DEDUP-CANDIDATE-EXPLOSION.

---

## Archived (already shipped — moved to `docs/archive/agent-tasks/`)

| Workstream | Status | Note |
|------------|--------|------|
| `dedup-ui/` | ✅ all 5 shipped (CONS-4/6/11, C6, DEDUP-KB-1) | verified in UnifiedDedupTab/CandidateCompareDrawer/Library |
| `system-docs/` | ✅ output present in `docs/system/` (8 files) | DOCS-1 deliverable exists |
| `dedup-intro-falsepositive/` | ✅ all 4 shipped (investigate, short-clip skip, title blocklist, ISBN gate) | **residual** (`upsertExactCandidate` chokepoint lacks the boilerplate/min-duration guard — confirmed at engine.go:1264) carried into WS-1/T01 |
| `transcription-matching/` | ✅ all 5 shipped (search-hints, auto-confirm, upgrade-gate, batch auto-match, dedup tiebreaker) | verified in metafetch/metabatch/dedup |

---

## Cost / efficiency strategy (fan-out)

- **Tier split:** Haiku for mechanical edits (static links, slog swaps, JSONL
  export, folder-relation mirroring); Sonnet for logic/schema/risk/design-adjacent
  work. ~1/3 Haiku, ~2/3 Sonnet across Bucket 1.
- **Coordinator owns git/gh** (per ORCHESTRATION.md): workers stay read/write in
  their worktree and report done; only the coordinator merges + rebases siblings.
- **Waves respect the collision table** above — never co-schedule two tasks that
  touch the same file.
- **Cheapest-first ordering within a workstream:** run the independent, different-file
  tasks as wave 1 to maximize parallelism, serialize the same-file ones after.
- **Known CI noise** (mock-freshness, 2 flaky tests) is fixed by WS-8; until then
  each worker's gate is *its own changed packages passing locally*, not full CI.
