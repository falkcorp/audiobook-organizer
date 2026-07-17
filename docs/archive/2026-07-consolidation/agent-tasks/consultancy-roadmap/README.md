<!-- file: docs/agent-tasks/consultancy-roadmap/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: ac814b83-acb8-4935-b396-dacbfd889ef1 -->
<!-- last-edited: 2026-07-03 -->

# Workstream — Consultancy roadmap (2026-07-02 evaluation)

Implementation tasks for the consultancy evaluation
([`docs/consultancy/00-ROADMAP.md`](../../consultancy/00-ROADMAP.md), PR #1742,
101 findings). One brief per roadmap item, weak-model-proof, worktree-disciplined.
TODO.md tracks the Tier-0 subset as `CONSULT-1..8`.

**Model-tier policy (owner-directed):** Haiku for mechanical/small, Sonnet for
standard backend work, Opus ONLY for the genuinely complex items (concurrency,
calibration methodology, the big toggle, data ops at 384K scale, structural
splits).

## Task table

| Task | Source | Title | Pri | Effort | Tier | Wave | Depends on |
|------|--------|-------|-----|--------|------|------|------------|
| TASK-01 | CONSULT-1 (MATCH-1/BUG-1/QUAL-4) | EmbeddingScorer model/dim guard + F1 fallback on degenerate scores | P0 | M | Sonnet | 1 | — |
| TASK-02 | CONSULT-2 (STOR-1/QUAL-2) | memdb preserve guard on `UpdateBook` (mirror PERF-7 BookFile guard) | P0 | M | Sonnet | 1 | — |
| TASK-03 | CONSULT-2 (STOR-1) | BookSig/Description recovery audit from `book_ver:` snapshots (dry-run op) | P0 | M | **Opus** | 2 | TASK-02 |
| TASK-04 | CONSULT-3 (DEDUPC-1/TOGGLE-2) | Model-aware re-embed skip in `EmbedAuthor` + `EmbedBooksAsync` | P0 | S | Sonnet | 1 | — |
| TASK-05 | CONSULT-4 (OPS-3/NEWF-5/PROC-2) | Retire/gate nightly `dedup.embed-async` (quota-dead OpenAI Batch API) | P0 | S | Haiku | 1 | — |
| TASK-06 | CONSULT-5 (TOGGLE-1/MATCH-8) | Keyless local-backend registration (drop OpenAIAPIKey requirement) | P0 | S | Sonnet | 1 | — |
| TASK-07 | CONSULT-6 (SEC-2/SEC-5/PROC-7) | Fix pre-commit hook `.claude/.credentials/` block + SHA-pin `security.yml` | P0 | S | Haiku | 1 | — |
| TASK-08 | CONSULT-7 (OPS-1/OPS-6) | Commit deploy recipe templates + `scripts/manage-ollama-windows.py` + rollback target | P0 | M | Sonnet | 1 | — |
| TASK-09 | CONSULT-8 (SEC-1/PROC-6) | API-key rotation + expiry for bootstrap-issued keys | P1 | M | Sonnet | 1 | — |
| TASK-10 | NEWF-2 (TOGGLE-1..7) | Backend-mode toggle core (embeddings + LLM enums, per-config LLM base URL) | P1 | L | **Opus** | 3 | TASK-04, 06, 12 |
| TASK-11 | NEWF-2 | Backend-mode toggle frontend (settings selector + model-download prompt) | P1 | M | Sonnet | 4 | TASK-10 |
| TASK-12 | MATCH-7/TOGGLE-4/BUG-5 | Permanent-error classification in retry paths (stop retrying 429 quota) | P1 | S | Sonnet | 1 | — |
| TASK-13 | DEDUP-1 | Stale-candidate drain op (~384K importer-bug candidates, dry-run gated) | P1 | L | **Opus** | 2 | TASK-04 |
| TASK-14 | DEDUP-4 | Duration-coverage backfill (unblock not_dup catchers) | P1 | S | Sonnet | 3 | TASK-13 |
| TASK-15 | DEDUP-2/3 | bge-m3 embedding-threshold recalibration + candidate regeneration | P1 | M | **Opus** | 3 | TASK-13 |
| TASK-16 | NEWF-1 | Fingerprint-coverage campaign op + coverage KPI (~8,387 books) | P1 | M | Sonnet | 1 | — |
| TASK-17 | 02-dedup design | `dedup.auto-resolve` confidence-tiered op (dry-run, sampling audit, reversible) | P1 | L | **Opus** | 4 | TASK-13, 15, 16 |
| TASK-18 | QUAL-1/BUG-4 | slog-corruption sweep (duplicate keys, `value0`, dropped args, `!BADKEY`) | P1 | M | Haiku ×N | 5 | run alone |
| TASK-19 | SYS-1/BUG-2 | Shutdown escape-hatch + `Registry.Shutdown` goroutine-tracking fix | P1 | M | **Opus** | 1 | — |
| TASK-20 | ARCH-1/2/5 | HNSW snapshot staleness check, atomic export, wrap `Graph.Delete` | P1 | M | Sonnet | 2 | TASK-06 |
| TASK-21 | OPS-4/5 | Scrape `/metrics` + minimal alerting (op failures, backend availability) | P2 | M | Sonnet | 2 | TASK-08 |
| TASK-22 | STOR-3/ARCH-3/SYS-3 | NutsDB retirement (activity dual-write cutover + metrics to Pebble) | P2 | M | Sonnet | 2 | TASK-19 |
| TASK-23 | MATCH-6/BUG-3 | TOCTOU fix in `ApplyTranscriptionCandidate` (apply the gated candidate) | P2 | S | Sonnet | 1 | — |
| TASK-24 | MATCH-5/BUG-6 | Cover-filter ordering (stop dropping top-scored candidates) | P2 | S | Haiku | 2 | TASK-01 |
| TASK-25 | MATCH-3 | `IsGarbageValue` "error" substring false positives | P2 | S | Haiku | 2 | TASK-01 |
| TASK-26 | MATCH-2/CTR-3 | LLM-rerank scale normalization (clamped vs unclamped score mixing) | P2 | M | Sonnet | 3 | TASK-25 |
| TASK-27 | MATCH-9 | Rune-based Levenshtein (non-ASCII titles) | P3 | S | Haiku | 1 | — |
| TASK-28 | PROC-3/5, ARCH-4, SYS-6 | Doc-drift cleanup (AI-REFERENCE, pebble-schema duplicate, mockery pins) | P2 | S | Haiku | 1 | — |
| TASK-29 | PROC-4 | Strengthen the 30% coverage gate (dedupe run, surface output, ratchet) | P3 | S | Haiku | 2 | TASK-08 |
| TASK-30 | SYS-5 | Split `pebble_store.go` (11,398 lines) into per-domain files | P3 | L | **Opus** | 6 | TASK-02, 03 |
| TASK-31 | SYS-2 | Decompose `Server.Start` (670 lines, dual lifecycle authorities) | P3 | L | **Opus** | 6 | TASK-19, 22 |

## Needs-brainstorm bucket (NO briefs — spec first, per BREAKDOWN convention)

- **NEWF-3** bulk metadata-review queue — feature design needed before tasking.
- **NEWF-4** library-health / data-quality dashboard.
- **CTR-1/CTR-2** structural redesign: field-masked write primitive + separate
  memdb projection type (would permanently eliminate the STOR-1 bug class; big).
- **NEWF-7** progress-sync/player API · **NEWF-8** community fingerprint index.

## Ground rules

- Go tasks: `go build ./... && go test ./internal/<changed-pkg>/... -count=1 && go vet ./...`
  minimum; run `make ci` before PR.
- **Verify every file:line anchor with `grep` before editing** — the consultancy
  citations are from 2026-07-02 and drift.
- File headers bumped on every change; conventional commits; PR + rebase merge
  (never squash, never direct-to-main).
- Prod-data ops (TASK-03, 13, 15, 17) are **dry-run-first, owner-greenlight
  gated** — the brief's acceptance criteria stop at "dry-run report produced".

## Collision / wave notes

Same-file groups that force serialization (see [orchestration.md](orchestration.md)):

- `internal/dedup/engine.go`: T04 → T13 → T15 → T17 (one per wave).
- `internal/metafetch/service_scoring.go` / `service_search.go`: T01 → {T24, T25} → T26.
- `internal/ai/*`: T06/T12 (disjoint files, wave 1) → T10 (touches both areas, wave 3).
- `internal/server/server_lifecycle.go`: T19 → T22 → T31.
- `internal/database/pebble_store.go`: T02 → T03 → T30.
- `Makefile` / `deploy/`: T08 → {T21, T29}.
- T18 (slog sweep) touches dozens of files across packages — it runs **alone** in
  wave 5, after waves 1–4 merge, to avoid rebasing against everything.
