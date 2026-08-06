<!-- file: docs/consultancy/05-features.md -->
<!-- version: 1.1.0 -->
<!-- guid: 40d501df-868a-4c5f-9ed4-cb64c1c6ff1c -->
<!-- last-edited: 2026-07-17 -->

# Consultancy Evaluation — Feature Portfolio (2026-07-02)

Evaluation run by a read-only multi-agent workflow; findings cited as `file:line` against the repository at the evaluation date. This dimension covers two reports: **do/defer/kill verdicts on deferred work** (FEAT-) and **missing-feature proposals** (NEWF-), followed by portfolio-optimization guidance.

## Executive Summary

Two specialist reports converge on the same portfolio thesis: for a personal ~50K-book organizer that just survived an OpenAI quota-out and cut over to local Ollama, **the highest-leverage work is not new surfaces but closing data-quality loops the docs already identify as blockers** — and several deferred items on the board are actively hazardous to run now.

On the deferred-work side: the **ai-responses-migration ×5** workstream should be killed/indefinitely deferred — it migrates toward OpenAI's `/v1/responses` endpoint shape while the only working backend (Ollama, reached via the OpenAI-compatible `base_url` override at `internal/ai/openai_parser.go:85-87`) serves only `/v1/chat/completions`; migrating would break the sole functioning backend. **Dedup C8** auto-bug-filing stays correctly deferred behind the CONS-10 backfill gate and should be downgraded from GitHub-issue filing to an in-app dry-run report. **CONS-13** flat-key shim retirement is the one clear "do": small, briefed, cheap permanent-debt removal once its 1-week stability gate is verified. The **pluggable-workflow subsystem** stays deferred except WF-2 (capability declarations), which just acquired a concrete driver in the backend-mode toggle. The **Plex-style API** is deferred (no spec, no consumer, book identity mid-repair), and the **Postgres research tracks** (4.1/4.7) should be collapsed into a one-paragraph decision record — they contradict the PebbleDB-primary mandate with no driving pain.

On the missing-feature side, the top three proposals all close loops: (1) a **fingerprint-coverage campaign + KPI** — ~8,387 unfingerprinted books block the AcoustID dedup veto, the status doc calls coverage "the real lever," yet TODO.md tracks nothing; (2) the **LLM/embedding backend-mode toggle** — the only remaining single point of failure from the OpenAI outage; (3) a **bulk metadata-review queue** — ~40% of transcribed books have low-quality metadata and review today is strictly one-by-one. A unified **library-health dashboard** would tie the quality signals together. On the over-built side: the deprecated `dedup.embed-async` op is still nightly-scheduled at 03:00 against the quota-dead OpenAI Batch API (recurring 429 noise every night), `GeneratePlaylistsForSeries` is a dead stub, and the backlog itself needs reconciliation — "duration/filesize aggregation" is already shipped, while the P4 bugs (BUG-1/QUAL-2) that should outrank every feature here are untracked in TODO.md.

Advisor verification (below) spot-checked the key citations and confirmed the verdicts as well-calibrated, with two minor adjustments reflected inline: FEAT-6's "kill" overstates the change (4.1/4.7 are already `[hold]`-tagged), and NEWF-5 slightly undersells urgency (the nightly cron 429s every night).

## Advisor Verification

The engagement advisor's boundary assessment for this phase, reproduced and reflected in the findings below:

- **Spot-checks pass:** `openai_parser.go:85-87` base_url override, `embed_async.go:23-37` nightly 03:00 deprecated op, and `embed_scan.go:73/90` confirming it submits to the OpenAI Batch API (Ollama serves neither `/v1/batches` nor `/v1/responses`) — so the FEAT-1 kill verdict and NEWF-5 are both solid.
- **Citations verified verbatim:** TODO.md:173/1722/1728/1734 match. BUG-1/QUAL-2 and "fingerprint coverage" are genuinely absent from TODO.md, confirming NEWF-1 and NEWF-9.
- **Minor quibble 1 (FEAT-6):** "kill" overstates the change — TODO items 4.1/4.7 are already `[hold]`-tagged; the recommendation is really a consolidation of already-parked items into a decision record, not a status change.
- **Minor quibble 2 (NEWF-5):** urgency is slightly undersold — the nightly cron will 429 every night against the quota-dead OpenAI account, adding recurring error noise, so retirement is an ops-hygiene item, not merely cleanup.
- **No overreach found; verdicts are well-calibrated.**

## Verdicts on Deferred Work

| Item | Verdict | Rationale (one line) |
|---|---|---|
| ai-responses-migration ×5 (AI-RESP-A/B/D/E/F) | **KILL / indefinite defer** | Targets `/v1/responses`, which Ollama (the only working backend) does not serve; Batch API path is quota-dead |
| Dedup C8 auto-bug-filing | **DEFER** (correctly gated) | Hard-blocked on CONS-10 backfill; re-scope to dry-run report, drop GitHub-filing credential surface |
| CONS-13 flat-key shim retirement | **DO** (after gate) | Effort S, fully briefed, clean rollback; verify 1-week stability window + frontend grep first |
| Pluggable-workflow subsystem (WF-2..WF-6) | **DEFER subsystem; pull WF-2 forward** | WF-2 capability declarations have a live consumer (backend-mode toggle); WF-5 UI builder must not start |
| Plex-style HTTP media API (3.8) | **DEFER** | No spec, no named consumer, L-size external surface while book identity is mid-repair |
| Postgres eval (4.1) + per-workload store eval (4.7) | **KILL as standing items** (already `[hold]`; consolidate) | Contradicts PebbleDB-primary mandate; no driving pain at 50K books; fold into one decision record |

## Findings Table

| ID | Severity | Impact | Effort | Title |
|---|---|---|---|---|
| NEWF-1 | high | high | medium | Missing: fingerprint-coverage campaign op + coverage KPI (unblocks dedup auto-resolution) |
| NEWF-2 | high | high | medium | Missing: LLM/embedding backend-mode toggle — single remaining SPOF from the OpenAI outage |
| NEWF-3 | medium | high | high | Missing: bulk metadata-review queue (middle ground between auto-fetch and one-by-one dialog) |
| NEWF-4 | medium | medium | medium | Missing: unified library-health / data-quality dashboard |
| NEWF-5 | medium | medium | low | Over-built/stale: deprecated dedup.embed-async still nightly-scheduled against quota-dead OpenAI Batch API |
| NEWF-6 | medium | medium | low | Portfolio optimization: park ai-responses-migration ×5; trim workflow subsystem to WF-2 only |
| NEWF-7 | low | medium | high | Missing: consumption/player integration (progress-sync API) — foundations built, no consumer |
| NEWF-9 | low | low | low | Backlog hygiene: duration/filesize aggregation already shipped; memory/TODO reconciliation needed (BUG-1/QUAL-2 untracked) |
| NEWF-8 | info | medium | high | Community acoustic-fingerprint index: high-value differentiator, hard-gate on NEWF-1 coverage |
| FEAT-1 | info | low | high | ai-responses-migration ×5 (AI-RESP-A/B/D/E/F): kill / indefinite defer |
| FEAT-2 | info | low | medium | dedup C8 auto-bug-filing: defer (correctly gated); consider downgrading to a report op |
| FEAT-3 | info | low | low | CONS-13 flat-key config shim retirement: do, once the 1-week gate is verified |
| FEAT-4 | info | medium | medium | Pluggable-workflow subsystem: defer the subsystem; pull WF-2 (capability declarations) forward |
| FEAT-5 | info | medium | high | Plex-style HTTP media server API (3.8): defer — no spec, no consumer, wrong time |
| FEAT-6 | info | low | low | PostgreSQL research track (4.1) + per-workload store eval (4.7): consolidate already-held items into a decision record |

---

## Deferred-Work Verdicts (FEAT-)

### FEAT-1 — ai-responses-migration ×5 (AI-RESP-A/B/D/E/F): kill / indefinite defer

**Verdict: KILL / indefinite defer.**

The migration's rationale (TODO.md:1538-1546: new models ship `/v1/responses`-first, `PreviousResponseID` token savings) assumed OpenAI as the live backend. OpenAI is now 429 `insufficient_quota` and the primary is local Ollama at <gpu-host>, reached via the OpenAI-compatible `base_url` override in `openai_parser.go:85-87` (`OPENAI_BASE_URL`) — Ollama serves `/v1/chat/completions`, not `/v1/responses`, so migrating A/B/E/F would break the only working backend. AI-RESP-D (Batches) is doubly dead: `openai_batch.go` targets the OpenAI Batch API, which is unusable at zero quota (the status doc explicitly routes re-embed via `dedup.embed-scan`, NOT `embed-async`, for this reason). The pending LLM backend-mode toggle (status doc Pending #1) also needs the Chat Completions shape for its local-only mode.

Advisor verification confirmed the load-bearing facts directly: `embed_scan.go:73/90` shows the async path submits to the OpenAI Batch API, and Ollama serves neither `/v1/batches` nor `/v1/responses`.

**Recommendation:** Do not greenlight. Keep the briefs archived and AI-RESP-C (never migrate `embedding_client.go`) as a marker. Re-open only if (1) OpenAI quota is restored AND (2) the backend-mode toggle exists so `/v1/responses` can be an OpenAI-mode-only code path behind a provider abstraction. Sequence the backend-mode toggle first; it subsumes most of the value.

**Citations:**
- TODO.md:1538-1556
- docs/agent-tasks/ai-responses-migration/README.md:1
- internal/ai/openai_parser.go:85-87
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:10-14
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:70-77

### FEAT-2 — dedup C8 auto-bug-filing: defer (correctly gated); consider downgrading to a report op

**Verdict: DEFER (correctly gated); re-scope when unblocked.**

C8 clusters `not_dup` labeled examples and files one GitHub issue per systematic false-positive cluster. It is hard-blocked on the prod labeled-dataset backfill, which is itself behind the CONS-10 drain sequence (duration-backfill + re-scan, a DATA-LOSS-gated prod operation per TODO CONS-10). The brief is well-designed (dry-run default, confirm-backfill-done flag) but the deliverable is questionable for a single-operator project: no GitHub-issue machinery exists in `internal/` (TASK-05:42-44), so it adds a new dependency and a token-bearing prod-to-GitHub write path purely to replace what the existing C6 label-review UI (`DedupLabels.tsx`, shipped) already surfaces interactively.

**Recommendation:** Keep deferred; do not greenlight until CONS-10 backfill completes. When unblocked, re-scope: ship only the clustering + dry-run report (log/endpoint output), and add real GitHub filing only if triage volume proves the UI insufficient. That halves effort and removes the new credential surface.

**Citations:**
- TODO.md:432-434
- docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md:8-15
- docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md:42-44

### FEAT-3 — CONS-13 flat-key config shim retirement: do, once the 1-week gate is verified

**Verdict: DO (after two pre-flight checks).**

Removes `legacyRemapGroup`/`configRemapGroups`/`applyLegacyRemaps` in the config update service so only nested config keys are accepted. Effort S, single-file plus tests, fully briefed with a clean rollback (revert restores compat). The only real risk is a client still sending flat keys — fields would silently stop applying (TASK-05:14-18) — and the brief already mandates the frontend grep (TASK-05:89-98) and a human-confirmed 1-week prod-stability window. Note the brief's target path (`internal/config/update_service.go`) and TODO.md:173 (`internal/server/update_service.go`) disagree — the executing agent must resolve which file actually holds the shim before editing.

**Recommendation:** Greenlight after two checks: (1) grep prod logs / frontend for any flat-key payload in the last week; (2) reconcile the file-path discrepancy between TODO.md:173 and the brief. Then dispatch as-is. This is the cheapest permanent-debt removal in the portfolio; still ranks below BUG-1/QUAL-2.

**Citations:**
- TODO.md:173
- docs/agent-tasks/perf-cleanup/TASK-05-retire-flatkey-shim.md:10-21
- docs/agent-tasks/perf-cleanup/TASK-05-retire-flatkey-shim.md:89-98

### FEAT-4 — Pluggable-workflow subsystem: defer the subsystem; pull WF-2 (capability declarations) forward

**Verdict: DEFER subsystem; carve out WF-2.**

TODO.md:283-285 already carries the correct stance: evolve UOS (op registry + plugins + the now-landed PR #1440 dependency scheduling, flag-off, commit 8282f818 per WF-1), resist Temporal/Conductor (breaks single-binary deploy), no code before a WF-0 brainstorm→spec session. WF-3 (persisted Workflow objects) and especially WF-5 (UI workflow builder, "biggest single cost") have no forcing function today. But WF-2 — action-level `requires: [ollama, openai, fpcalc]` declarations — just acquired a concrete driver: the OpenAI→Ollama cutover and the pending LLM backend-mode toggle (status doc Pending #1) both need per-op backend gating, which is currently ad-hoc (`SetOllamaAvailable`, `toolRegistry.Available` checks per TOOL-6).

**Recommendation:** Keep WF-0 as the gate for WF-3/4/5/6 (defer). Carve WF-2 out and design it together with the LLM backend-mode toggle — one small mechanism serving an immediate need, de-risking the larger subsystem later. Do not start WF-5 under any priority scheme this quarter.

**Citations:**
- TODO.md:281-296
- docs/archive/2026-07-consolidation/agent-tasks/BREAKDOWN-2026-07-01.md:129
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:70-72

### FEAT-5 — Plex-style HTTP media server API (3.8): defer — no spec, no consumer, wrong time

**Verdict: DEFER.**

A one-line backlog entry (TODO.md:1722, size L) with no spec, no named client (no Plexamp/Prologue/Audiobookshelf-app target), and no design doc anywhere in `docs/specs/`. BREAKDOWN-2026-07-01.md:130 correctly classifies it as needs-brainstorm ("new external-facing subsystem"). It would add streaming/range-request, external auth, and library-browse API surface to a server whose core identity data is mid-repair: 380K mislabeled dedup candidates awaiting the CONS-10 drain, 49K iTunes-fragmented books awaiting re-group, ~8,387 books unfingerprinted. Serving a media API over unstable book identity bakes churn into external clients.

**Recommendation:** Defer until the data-quality tracks (CONS-10 drain, iTunes re-group, fingerprint coverage) complete. Before any build: a brainstorm session that first evaluates exposing the existing REST API to one concrete client app vs. implementing a Plex/Audiobookshelf-compatible protocol — the latter only if a real playback client is named as the consumer.

**Citations:**
- TODO.md:1722
- docs/archive/2026-07-consolidation/agent-tasks/BREAKDOWN-2026-07-01.md:130

### FEAT-6 — PostgreSQL research track (4.1) + per-workload store eval (4.7): consolidate already-held items into a decision record

**Verdict: retire as standing backlog items.** (Per advisor verification: both items are already `[hold]`-tagged, so "kill" overstates the change — this is a consolidation of parked items, not a status reversal.)

4.1 (XL, `[hold]`) and 4.7 (L, `[hold]`) are overlapping research tracks with no spec and no driving pain point. The architecture has since committed hard the other way: PebbleDB is the mandated sole production store (project memory: "always implement PebbleStore fully before shipping"), the memdb-optimization spec (`docs/archive/2026-07-consolidation/specs/fable5-spec-memory-db-optimization.md`) invested in Pebble-side performance, vector data lives in Pebble's EmbeddingStore with chromem/HNSW as derived indexes (status doc:49-51), and SQLite is already legacy/stale. A Postgres migration would be the largest project in the backlog while every observed bottleneck (aggregates, cache warm-up, pagination caps) was fixed within the Pebble architecture. Keeping an XL "research" item alive invites periodic re-litigation cost.

**Recommendation:** Remove 4.1 and fold 4.7 into a single one-paragraph decision record: "Pebble is primary; revisit only if a concrete relational/query requirement (multi-node, ad-hoc reporting, >500K books) emerges." That preserves the option without carrying an XL item on the board.

**Citations:**
- TODO.md:1728
- TODO.md:1734
- docs/archive/2026-07-consolidation/agent-tasks/BREAKDOWN-2026-07-01.md:131
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:49-51

---

## Proposed Features, Ranked (NEWF-)

Ranked by leverage. Note the standing constraint from the P4 phase: bugs BUG-1/QUAL-2 outrank every item below (and are themselves untracked — see NEWF-9).

### NEWF-1 — Missing: fingerprint-coverage campaign op + coverage KPI (unblocks dedup auto-resolution) — severity: high

The status doc concludes the AcoustID dedup veto is "NOT viable at current coverage" (~65% of candidates have an unfingerprinted book; ~8,387 books unfingerprinted) and calls fingerprint coverage "the real lever." The mechanics exist — `fingerprint-rescan-missing` op (optimize.go:66), acoustid backfill plugin, per-book coverage fields (`ComputeFingerprintFields`, calculator.go:31) — but there is no library-wide coverage KPI, no campaign orchestration (rate-limited long-run over 8,387 books with perm-fail tombstone handling), and grep finds zero TODO.md items tracking it (advisor-confirmed). The single feature that converts 380K noisy dedup candidates into auto-resolvable pairs is untracked.

**Recommendation:** Add a tracked workstream: (a) library-wide fingerprint-coverage stat in the system stats endpoint + Dashboard tile, (b) a resumable campaign op (reuse `fingerprint-rescan-missing` + acoustid backfill, honoring the 4,882 perm-fail tombstones), (c) re-run `ReevaluateAcoustIDConflicts` (#1736) when coverage crosses a threshold. Sequence before any new dedup feature and before the community index (NEWF-8).

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:64-66
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:73-74
- internal/plugins/maintenance/optimize.go:66
- internal/fingerprint/calculator.go:31
- internal/plugins/acoustid/backfill.go:247

### NEWF-2 — Missing: LLM/embedding backend-mode toggle — single remaining SPOF from the OpenAI outage — severity: high

The cutover to Ollama was done by hand-editing prod config (`embedding.base_url=http://<gpu-host>:11434/v1`, `metadata_scoring.llm_enabled=false`). Status doc pending item 1 specs the fix: a config enum + FE selector (disable-all / OpenAI-only / local-only / OpenAI+local-fallback) plus model-download prompt. Without it, any backend flap requires manual config surgery, and the LLM scoring feature stays globally disabled rather than falling back to qwen2.5:7b-instruct. This is the only planned feature that directly de-risks the incident that just happened.

**Recommendation:** Build as specced in the status doc. Implement the fallback mode as a runtime health-check chain (reuse `ToolRegistry.Available` / `SetOllamaAvailable` gates from TOOL-6) rather than a static choice, so a dead backend degrades instead of erroring. This is also the concrete first consumer for WF-2 capability declarations (see NEWF-6 / FEAT-4).

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:70-72
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:42-47

### NEWF-3 — Missing: bulk metadata-review queue (middle ground between auto-fetch and one-by-one dialog) — severity: medium

All metadata acceptance flows route through the per-book MetadataReviewDialog (e.g. the duration-mismatch chip at MetadataReviewDialog.tsx:604, cited from TODO.md:1469). With ~40% of transcribed books carrying low-quality/unparsed metadata (per the 2026-06-30 transcription status memory) and a pending scoped metadata refetch, reviewing tens of thousands of candidates one dialog at a time is the dominant remaining manual workflow. There is no triage queue: no "accept all candidates above confidence X", no batch-diff view, no keyboard-driven review stream. The differentiated residual-triage op (PH-2, TODO.md:103) generates populations to review but has no matching UI to consume them at scale.

**Recommendation:** Build a review-queue page: server-side queue of (book, top-candidate, confidence, diff summary) sortable by confidence; bulk-accept above threshold with the existing persistent undo (AP-2); keyboard j/k/a/r flow for the gray zone. Feed it from PH-2 triage populations and the transcription quality gate. This multiplies the value of every upstream matching fix (#1734).

**Citations:**
- TODO.md:1469
- TODO.md:103

### NEWF-4 — Missing: unified library-health / data-quality dashboard — severity: medium

Every data-quality signal lives in its own diagnostic endpoint: duration-mismatch scan (`GET /maintenance/scan-duration-mismatch`, TODO.md:1468), acoustid-conflict purge (`/dedup/purge-acoustid-conflicts`, #1736), per-book fingerprint coverage, transcription quality, embedding model-match (#1738). System stats expose only volume aggregates (totalDuration etc., system/handler.go:708). There is no single view answering "how healthy is my library and what should run next" — which is why gaps like the 8,387 unfingerprinted books and the stale 3072-dim vectors were discovered reactively during incidents rather than observed on a dashboard.

**Recommendation:** Add a `GET /api/v1/library/health` endpoint aggregating counts: unfingerprinted, untranscribed/low-quality-transcript, embedding-model-stale, duration-mismatch, unresolved relinks, open dedup candidates by layer — each with a deep link to the fixing op. Render as a Dashboard section. Cheap to build (all counts derivable from existing stores; use the cached-aggregates + dirty-flag pattern already established).

**Citations:**
- internal/server/handlers/system/handler.go:708
- TODO.md:1468
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:23
- internal/fingerprint/calculator.go:31

### NEWF-5 — Over-built/stale: deprecated dedup.embed-async still nightly-scheduled against quota-dead OpenAI Batch API — severity: medium

`embedAsyncDef` registers op `dedup.embed-async` ("Deprecated — use embed-scan with async:true") with `Schedule: "0 3 * * *"` — nightly submission of un-embedded books to the OpenAI Batch API. The status doc explicitly notes the cutover re-embed deliberately avoids this path because it "uses the OpenAI Batch API", and OpenAI is 429 `insufficient_quota`. The batch poller subsystem (batch_poller.go, batch_poller_register.go) exists to ingest results that will never arrive. Nightly failures at minimum generate noise; at worst they interact with the model-aware skip logic (#1738). Advisor verification adds urgency: the 03:00 cron will 429 **every night** against the quota-dead account — this is a recurring ops-hygiene problem, not passive cleanup.

**Recommendation:** Retire the deprecated op ID now (its one-release grace window dates to 2026-06-10) and gate the async path + batch poller behind an "openai backend enabled" capability check — which falls out of NEWF-2's backend-mode enum. Verify on prod (server-logs) whether the 03:00 run is currently erroring nightly.

**Citations:**
- internal/plugins/dedup/embed_async.go:26-38
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:55-57
- internal/server/batch_poller_register.go:1
- internal/plugins/maintenance/batch_poller.go:1

### NEWF-6 — Portfolio optimization: park ai-responses-migration ×5; trim workflow subsystem to WF-2 only — severity: medium

ai-responses-migration (Chat Completions → OpenAI `/v1/responses`, 5 tasks, already marked DEFERRED/optional at agent-tasks README:35) targets an API shape whose provider is quota-dead, while the now-primary Ollama backend is used precisely via OpenAI-compatible Chat Completions `/v1` (status doc prod config). Migrating to `/v1/responses` risks breaking local-backend compatibility for zero current benefit. Separately, the pluggable-workflow subsystem (TODO.md:281-292, EXPLORATORY, no code) spans WF-2..WF-6 including a UI workflow builder flagged as "biggest single cost"; only WF-2 (capability/requirement declarations `requires: [ollama, openai, fpcalc]`) has an immediate consumer — backend gating for NEWF-2/NEWF-5.

**Recommendation:** Park ai-responses-migration until OpenAI is re-funded AND Ollama `/v1/responses` support is confirmed; record the decision in the workstream README. For workflows: build WF-2 only, defer WF-3..WF-5 indefinitely, and skip the WF-0 brainstorming session until a second concrete consumer exists. (Note: this differs slightly from FEAT-4, which keeps WF-0 as the gate for the rest of the subsystem; both agree WF-2 goes first and WF-5 does not start.)

**Citations:**
- docs/agent-tasks/README.md:35
- TODO.md:281-292
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:76-77

### NEWF-7 — Missing: consumption/player integration (progress-sync API) — foundations already built, no consumer — severity: low

A full listening-state model exists: `UserPosition` rows per file segment, `UserBookState` with auto-derived unstarted/in_progress/finished (≥95%) statuses, manual-override flags, and iTunes position sync (readstatus.go header). But the only writer is iTunes sync; there is no API for a modern player (Audiobookshelf/Plexamp-style, or the greenfield "Plex-style API" idea) to read the library or write positions, and no streaming/download endpoint pairing. Meanwhile the playlist package's series-playlist generator is a dead stub returning an error (playlist.go:34-37) since the fable5 SQLite removal. The organize/dedup half of the product is mature; the consume half is an unexposed data model plus dead code.

**Recommendation:** Short term: delete `GeneratePlaylistsForSeries` (dead since T022). Medium term: greenfield-assess a minimal read/stream + position-write API (or an Audiobookshelf export/sync bridge) as the consumer for readstatus — spec first, no existing doc. Rank below NEWF-1..3.

**Citations:**
- internal/readstatus/readstatus.go:5-27
- internal/playlist/playlist.go:28-37

### NEWF-8 — Community acoustic-fingerprint index: high-value differentiator, hard-gate on NEWF-1 coverage — severity: info

TODO.md:296-313 sketches a git-repo-backed community index ("AcoustID for audiobooks") with PR-bot loop, keyed on `internal/fingerprint/book_signature.go` whole-book signatures. It doubles as disaster recovery for the identity layer. But its value is proportional to local fingerprint coverage: with ~8,387 books unfingerprinted and 4,882 perm-fail tombstones, the exported index would be materially incomplete, and the open design questions (format, identity unit, governance, license) are all unresolved.

**Recommendation:** Keep as needs-planning; schedule the brainstorming→spec session only after the NEWF-1 coverage campaign completes, so real coverage numbers inform the identity-unit and format decisions. Fold "disaster-recovery export of the identity layer" (fingerprints + verified metadata as a repo snapshot) out as a smaller standalone feature that does not need community governance.

**Citations:**
- TODO.md:296-313
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:64-66

### NEWF-9 — Backlog hygiene: "duration/filesize aggregation" is already shipped; memory/TODO reconciliation needed (and BUG-1/QUAL-2 untracked) — severity: low

Two portfolio-tracking inaccuracies found. (1) The memory-listed missing feature "duration/filesize aggregation (Book shows snapshots not sums)" is implemented: `recompute-book-aggregates` job (MED-2) backfills sums and "PebbleStore BookFile create/update/delete hooks call RecomputeBookAggregates automatically" (recompute_book_aggregates.go:13-15; pebble_store_book_aggregates.go:138). Only residual: verify the `system:backfill:book_aggregates_v1_done` flag is set on prod. Similarly, the advisor-listed "unbuilt" tool-lifecycle/startup-wizard is fully shipped (TOOL-1..6, WIZ-1..3 all checked, PR #1465, TODO.md:263-279). (2) The P4 bugs BUG-1/QUAL-2 that should outrank all new features appear nowhere in TODO.md or docs/agent-tasks/ (grep: zero hits, advisor-confirmed) — they are untracked, violating the fix-or-document rule.

**Recommendation:** One-time reconciliation pass: close the stale memory items, confirm the aggregates backfill flag on prod, and add BUG-1/QUAL-2 to TODO.md with root-cause hints so the prioritization rule (bugs before features) is enforceable.

**Citations:**
- internal/maintenance/jobs/recompute_book_aggregates.go:6-19
- internal/database/pebble_store_book_aggregates.go:138
- TODO.md:263-279

---

## Portfolio Optimization (What to Trim)

Consolidated trim list, in order of ops urgency:

1. **Retire `dedup.embed-async` now** (NEWF-5) — it 429s against OpenAI nightly at 03:00; gate the batch poller behind the backend-mode capability check. Ops-hygiene, effort low.
2. **Park ai-responses-migration ×5** (FEAT-1 / NEWF-6) — record the decision in the workstream README; keep AI-RESP-C as a "never migrate embedding_client.go" marker.
3. **Trim the workflow subsystem to WF-2 only** (FEAT-4 / NEWF-6) — WF-2 rides along with the backend-mode toggle; WF-3/4/5/6 stay behind WF-0; WF-5 (UI builder) does not start this quarter under any priority scheme.
4. **Consolidate Postgres items 4.1 + 4.7** (FEAT-6) — both already `[hold]`; replace with a one-paragraph decision record naming the concrete re-open triggers (multi-node, ad-hoc reporting, >500K books).
5. **Defer the Plex-style API** (FEAT-5) — and when revisited, evaluate exposing the existing REST API to one named client before building any Plex/Audiobookshelf-compatible protocol. NEWF-7's minimal position-write API is the smaller, better-grounded first step on the consumption side.
6. **Delete dead code**: `GeneratePlaylistsForSeries` stub (NEWF-7).
7. **Backlog reconciliation pass** (NEWF-9) — close stale memory items (duration/filesize aggregation, tool-lifecycle wizard), confirm the aggregates backfill flag on prod, and track BUG-1/QUAL-2 in TODO.md.

Recommended build order for what remains: **BUG-1/QUAL-2 fixes → NEWF-2 backend-mode toggle (+WF-2) → NEWF-5 retirement (falls out of the toggle) → NEWF-1 fingerprint-coverage campaign → FEAT-3 CONS-13 shim retirement → NEWF-4 health dashboard → NEWF-3 bulk review queue**. Everything else waits on data-quality completion or a named consumer.
