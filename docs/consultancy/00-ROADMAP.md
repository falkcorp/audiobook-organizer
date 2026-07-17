<!-- file: docs/consultancy/00-ROADMAP.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5c960e64-01d1-498e-8316-6bf5193c3deb -->
<!-- last-edited: 2026-07-17 -->

# Consultancy Evaluation — Roadmap (2026-07-02)

Top-level synthesis of the six-dimension consultancy evaluation, ranked by
**impact × effort**. Produced by a read-only multi-agent workflow (25 agents:
schema-auditor, db-design, expert, go-specialist, code-reviewer, pii-scanner,
docs-agent, plus adversarial verifiers), with the repo expert consulted at every
phase boundary. All findings cite `file:line`; the five critical/high code
findings were independently adversarially verified and all confirmed real.

Dimension reports (full detail lives there, not here):

| Doc | Dimension | Findings |
|-----|-----------|----------|
| [01-storage-architecture.md](01-storage-architecture.md) | PebbleDB, derived vector indexes, NutsDB, lifecycle | 19 |
| [02-dedup.md](02-dedup.md) | Layered engine, backlog strategy, auto-resolution design | 13 |
| [03-matching-and-backends.md](03-matching-and-backends.md) | Metadata scoring, transcription match, backend-mode toggle design | 16 |
| [04-code-quality.md](04-code-quality.md) | Bug hunt, counter-arguments, adversarial verification | 17 |
| [05-features.md](05-features.md) | Deferred-work verdicts, missing features | 15 |
| [06-process-and-security.md](06-process-and-security.md) | CI/docs, security/PII, ops/cost | 21 |

## Headline conclusions

1. **Two verified high-severity defects are live right now, during the bge-m3
   re-embed.** (a) `EmbeddingScorer`'s cached-vector fast path ignores the
   stored vector's model, so mixed 3072/1024-dim comparisons cosine to 0 and
   metadata search silently returns zero candidates for not-yet-re-embedded
   books, with no F1 fallback (MATCH-1/BUG-1). (b) The memdb-stripped `Book`
   round-trip through full-replacement `UpdateBook` silently wipes
   `Description` and the `BookSigV1` dedup signatures — destroying the very
   fingerprint investment the dedup strategy depends on (STOR-1/QUAL-2;
   recoverable from `book_ver:` CoW snapshots, so recovery must precede any
   snapshot pruning).
2. **The dedup strategy needs a re-order.** Fingerprint coverage (~8,387
   books) is a real but *second-order* lever — it only powers the positive
   auto-resolve oracle. The higher-yield, zero-fingerprint lever is draining
   the ~384K stale candidates poisoned by the already-fixed importer bugs, plus
   cheap duration-coverage backfill. Meanwhile the embedding-layer thresholds
   are still calibrated for `text-embedding-3-large` and need recalibration
   for bge-m3 (DEDUP-1/2/4).
3. **The OpenAI→local cutover is ~80% done and the residue is dangerous:**
   author embeddings never re-embed (model-aware skip missing in
   `EmbedAuthor`/`EmbedBooksAsync`), a nightly `dedup.embed-async` op still
   targets the quota-dead OpenAI Batch API, local embedding still demands an
   `OpenAIAPIKey`, and the LLM path has no per-config base URL at all — local
   LLM mode is currently impossible. The backend-mode toggle design in doc 03
   resolves all of these.
4. **Ops is the weakest dimension:** deploy recipe + systemd drop-in exist only
   on one laptop (gitignored), no rollback target, the Windows GPU box is a
   triple SPOF held up by an interactive-session scheduled task whose setup
   scripts live in a scratchpad, `/metrics` is scraped by nothing, and quota
   exhaustion was discovered by production failure.
5. **Security posture is better than the stale memory suggests** — SEC-2,
   SEC-7, PERF-7 are verified done. Real gaps: API-key rotation has zero
   tracking and bootstrap keys never expire; the pre-commit hook does not
   actually block `.claude/.credentials/`; one workflow is pinned `@main`.

## Tier 0 — Do immediately (high impact, low effort)

| # | Item | Findings | Why now |
|---|------|----------|---------|
| 1 | Model/dimension check in `EmbeddingScorer` store fast-path + F1 fallback on degenerate zero-score results | MATCH-1, BUG-1, QUAL-4 | Verified critical; actively corrupting metadata search during the in-flight re-embed |
| 2 | memdb preserve guard on `UpdateBook` (mirror the PERF-7 BookFile guard); then audit/recover wiped `Description`/`BookSigV1` from `book_ver:` snapshots **before** any pruning | STOR-1, QUAL-2, DEDUP-6, DEDUPC-7 | Verified high; silently destroys dedup signatures on every memdb round-trip write |
| 3 | Apply model-aware re-embed skip to `EmbedAuthor` + `EmbedBooksAsync` | DEDUPC-1, TOGGLE-2 | Author embeddings stranded on OpenAI vectors post-cutover |
| 4 | Unschedule/gate nightly `dedup.embed-async` (OpenAI Batch API) | OPS-3, NEWF-5, PROC-2 | Fires nightly against a quota-dead API |
| 5 | Drop `OpenAIAPIKey` requirement for keyless local backends | TOGGLE-1, MATCH-8 | One-line-class fix; blocks clean local-only operation |
| 6 | Commit `docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md` | PROC-1 | Verdict: commit now — done in this PR |
| 7 | Fix pre-commit hook to actually block `.claude/.credentials/`; SHA-pin `security.yml` | SEC-2, SEC-5/PROC-7 | Cheap, closes claimed-but-absent protections |
| 8 | Commit deploy recipe (`Makefile.local` template + `deploy/local.conf` sample) and the Windows Ollama scripts as `scripts/manage-ollama-windows.py`; add a rollback target | OPS-1, OPS-6, status-doc TODO | The entire prod deploy story currently lives on one laptop |

## Tier 1 — Next (high impact, medium effort)

| # | Item | Findings | Notes |
|---|------|----------|-------|
| 9 | **Backend-mode toggle** (embeddings + LLM, independent enums: disabled / openai / local / fallback), incl. per-config LLM base URL, permanent-error classification in `retry.go` (stop retrying 429 `insufficient_quota`), runtime availability probing, FE selector + model-download prompt | NEWF-2, TOGGLE-1..7, MATCH-7, BUG-5/QUAL-6 | Full design in doc 03; kills the last SPOF class from the OpenAI outage |
| 10 | **Stale-candidate drain** (~384K candidates poisoned by fixed importer bugs) + duration-coverage backfill | DEDUP-1, DEDUP-4 | The first-order dedup lever; zero fingerprints required |
| 11 | **bge-m3 threshold recalibration** for the embedding dedup layer + candidate regeneration | DEDUP-2, DEDUP-3 | Current thresholds calibrated for text-embedding-3-large; Layer 2 silently degraded during re-embed window |
| 12 | **Fingerprint-coverage campaign** (~8,387 books) as a tracked op with a coverage KPI | NEWF-1, DEDUP-1 | Second-order but required for the auto-resolve positive oracle; do after items 2 & 10 |
| 13 | **Dedup auto-resolution op** (`dedup.auto-resolve`): confidence-tiered pipeline on existing ComposeScore bands with dry-run, sampling audit, reversible merges | DEDUPC design (doc 02) | The "perfect match after all filters" deliverable; gated on 10–12 |
| 14 | slog-corruption sweep (duplicate keys, `value0` literals, dropped args, `!BADKEY`) — mechanical, ideal `/parallel-sweep` | QUAL-1, BUG-4 | Observability is unreliable exactly where prod debugging happens |
| 15 | Shutdown correctness: 30s escape-hatch closes Pebble under live jobs; `Registry.Shutdown` can return with store-touching goroutines live | SYS-1, BUG-2 | Same class as the PEBBLE-CLOSED race already fixed once |
| 16 | HNSW snapshot staleness check vs Pebble source of truth; atomic Export; wrap `Graph.Delete` | ARCH-1, ARCH-2, ARCH-5 | Derived-index rebuild story is the main storage-architecture risk |
| 17 | API-key rotation + expiry for bootstrap-issued keys; add TODO tracking | SEC-1, PROC-6 | Only remaining substantive security gap |
| 18 | Minimal monitoring: scrape `/metrics`, alert on op failures + backend availability + (if restored) OpenAI spend | OPS-4, OPS-5 | Quota exhaustion was discovered by prod failure |

## Tier 2 — Later (medium impact or high effort)

- **NutsDB retirement** — finish the already-scaffolded Pebble cutover for activity (dual-write today) and metrics (still NutsDB-primary); removes a third storage engine and a goroutine leak (STOR-3/ARCH-3/SYS-3).
- **`pebble_store.go` split** (11,398 lines) and `Server.Start` decomposition (670 lines, dual lifecycle authorities) (SYS-2, SYS-5).
- **Bulk metadata-review queue** — highest-ranked new feature (NEWF-3); **library-health dashboard** (NEWF-4).
- **TOCTOU fix** in `ApplyTranscriptionCandidate` (applies cache slot 0, not the gated candidate) (MATCH-6/BUG-3/QUAL-3); cover-filter ordering (MATCH-5/BUG-6/QUAL-5); `IsGarbageValue` "error" substring (MATCH-3); LLM-rerank scale mismatch (MATCH-2); byte-wise Levenshtein (MATCH-9).
- **Doc-drift cleanup**: AI-REFERENCE route count + Ollama cutover, corrupted duplicate in `database-pebble-schema.md`, mockery v2 pin references (PROC-3, PROC-5, ARCH-4, SYS-6); strengthen the 30% coverage gate (PROC-4).
- **Structural counter-arguments** (strategic, high effort): replace full-replacement `UpdateBook` with field-masked writes at the store boundary (CTR-1) and split the memdb projection into its own type instead of reusing `*Book` (CTR-2) — these two would eliminate the entire STOR-1 bug class permanently.
- **PII scrub** (27+ files with internal hostnames/paths) — only if the repo is ever made public (SEC-3); `$RANDOM` credential entropy (SEC-4).

## Deferred-work verdicts (from doc 05)

| Item | Verdict |
|------|---------|
| ai-responses-migration ×5 | **Kill / indefinite defer** — Ollama (the only working backend) implements `/v1/chat/completions`, not `/v1/responses`; migrating would break prod |
| dedup C8 (auto-bug-filing) | **Defer** (correctly gated); downgrade to a report op when unblocked |
| CONS-13 (flat-key config shim retirement) | **Do**, once the 1-week gate is verified |
| Pluggable-workflow subsystem | **Defer** the subsystem; pull WF-2 (capability declarations) forward |
| Plex-style API | **Defer** — no spec, no consumer, wrong time |
| Postgres eval / per-workload store eval | **Kill as standing items** — PebbleDB-as-primary steelman held (doc 01) |

## What was explicitly validated (don't re-fix)

- SEC-2, SEC-7, PERF-7 (BookFile memdb guards): verified done.
- PRs #1738–#1741 (model-aware book re-embed, Ollama base_url trust, HNSW
  stale-dim discard + panic recovery): correctly implemented.
- Iterator hygiene (128/129 prefix-bounded), versioned backfill flags, cache
  layers, emission-time dedup gates: sound.
- PebbleDB-as-primary and derived-vector-index coupling: steelmanned and
  upheld (doc 01).
