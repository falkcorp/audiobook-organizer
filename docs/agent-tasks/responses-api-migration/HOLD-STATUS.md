<!-- file: docs/agent-tasks/responses-api-migration/HOLD-STATUS.md -->
<!-- version: 1.0.0 -->
<!-- guid: 57553b85-8c60-4c39-b42e-5696271e2e61 -->
<!-- last-edited: 2026-07-10 -->

# HOLD-STATUS — OpenAI Responses API migration (AI-RESP-A..F)

**Status:** BLOCKED — awaiting hold-lift on #1260-#1265

**Gate:** CONFIRM-HOLD — blocked-on-hold-lift. Issues #1260-#1265 are all marked [hold]. NOTHING
executes until the user explicitly lifts the hold. When lifted: sequenced lowest-risk-first
(A -> B -> D -> E -> F), each phase SOAKS in prod before the next; embeddings (/v1/embeddings) are
NEVER migrated (AI-RESP-C guard).

**File-ownership:** none known; all targets in `internal/ai/`

This package deliberately contains NO new TASK-NN briefs. The execution briefs ALREADY EXIST at
`docs/agent-tasks/ai-responses-migration/` (README.md, orchestration.md, run.sh, TASK-01..05,
written 2026-07-01) — this file is the freshness audit of that package as of 2026-07-10 at HEAD
`fce58498`, plus the gap work the old briefs don't cover. Sequencing/soak/rollback live in
`docs/plans/2026-07-10-responses-api-migration.md`; locked design decisions in
`docs/specs/2026-07-10-responses-api-migration-design.md`.

## Why this audit exists (what changed after 2026-07-01)

1. **Ollama local backend (2026-07-02).** `internal/ai/register.go` now constructs the SAME
   `OpenAIParser` for local mode via `NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, ...)`
   (re-verify: `grep -n 'NewOpenAIParserWithBaseURL' internal/ai/register.go` — ≥1 hit in the
   `AIBackendModeLocal` arm). Every phase A/B call site serves BOTH backends; Ollama is not assumed
   to support `/v1/responses`. Locked consequence (spec Decision 3): migrated sites become a
   two-arm dispatch on a new `OpenAIParser.useResponsesAPI` flag — NOT a replacement.
2. **SDK is now `openai-go/v3 v3.41.0`** (re-verify: `grep -n 'openai-go' go.mod`). The briefs
   reference the `v2` module path. v3 ships a `responses` package AND
   `BatchNewParamsEndpointV1Responses` — TASK-03's "possibly unsupported" blocker framing is stale.
3. **The strictly-serial soak sequence** in the INIT-7 gate supersedes the old parallel wave plan
   in `orchestration.md`.
4. The spec this workstream originally pointed at
   (`docs/superpowers/specs/2026-04-29-responses-api-migration-design.md`) never existed in this
   repo — the only reference is a dangling TODO.md link (re-verify:
   `grep -rn '2026-04-29-responses-api-migration-design' docs/` — 0 hits under docs/). The
   replacement spec is `docs/specs/2026-07-10-responses-api-migration-design.md`.

## Per-brief verdicts (docs/agent-tasks/ai-responses-migration/)

### README.md — MOSTLY FRESH, wave table stale

- Fresh: DEFERRED/optional framing, AI-RESP-C guard text, aijobs path correction
  (`internal/ai/aijobs/aijobs.go`, NOT `internal/aijobs/`), ground rules.
- Stale: the Wave column (TASK-02/03/04 all "Wave 2" parallel) — superseded by the serial soak
  sequence; and "Go only, in `internal/ai/...` and `internal/aijobs/...`" still names the wrong
  aijobs dir in one spot.
- At execution: treat the plan's skeleton table as authoritative for waves; do not dispatch from
  README's wave column.

### orchestration.md — STALE (superseded)

- Its Wave 2 runs TASK-02/03/04 in parallel after TASK-01. The INIT-7 gate mandates
  A → B → D → E → F strictly serial with prod soak between phases. Do NOT use `run.sh 02 03 04`.
- Still valid: the TASK-05-runs-last constraint and the embedding_client.go out-of-scope note.
- At execution: version-bump this file with a "superseded by docs/plans/2026-07-10-responses-api-migration.md"
  banner as part of M5 close-out (not before — it stays as historical record during the hold).

### TASK-01-metadata-llm-review.md (AI-RESP-A) — STALE in two load-bearing ways

- Fresh: anchor still exact — 1 call site (re-verify VERBATIM, provenance anchor:
  `grep -n 'Chat.Completions.New' internal/ai/metadata_llm_review.go` — expect 1 hit, ~line 145);
  worktree block; idempotency direction; test guidance.
- **Stale 1 (breaking):** the brief says REPLACE `Chat.Completions.New` with `Responses.New`. The
  call is a method on `*OpenAIParser` (`grep -n 'func (p \*OpenAIParser) scoreMetadataBatch' internal/ai/metadata_llm_review.go`),
  which local/Ollama mode also uses. Must be the two-arm dispatch of spec Decision 3, plus GAP-1
  below in the same PR.
- **Stale 2:** SDK verification snippet uses `github.com/openai/openai-go/v2/responses` — the
  module is `/v3`. Substitute v3 paths.
- Stale (minor): its idempotency grep ("if Responses.New present, done") is presence-of-new — still
  correct for the dispatch shape, but "Chat.Completions.New absent" must NOT be expected (the Chat
  arm survives).
- At execution re-verify: call-site count; SDK version; that no `useResponsesAPI` field exists yet
  (`grep -n 'useResponsesAPI' internal/ai/openai_parser.go` — expect 0 hits before, ≥2 after).

### TASK-02-openai-parser.md (AI-RESP-B) — STALE line numbers + same dispatch correction

- Fresh: depends-on-TASK-01 check, per-site-individually instruction, incremental-build step.
- Stale: brief cites sites at lines 167/272/356/416/562/684; verified 2026-07-10 they are at
  207/312/396/456/602/724 (drift already; re-verify VERBATIM, provenance anchor:
  `grep -n 'Chat.Completions.New' internal/ai/openai_parser.go` — expect exactly 6 hits). The brief
  already says to trust the grep, so this is informational.
- Stale (breaking, same as TASK-01): acceptance criterion "`grep -n "Chat.Completions.New"
  internal/ai/openai_parser.go` returns zero matches" is WRONG under the dispatch design — expect
  6 surviving hits, each inside the local-backend arm. Rewrite that criterion at dispatch time to:
  every remaining hit is in the `else` arm of a `useResponsesAPI` dispatch.
- At execution re-verify: site count still 6; TASK-01's dispatch pattern merged (its brief's
  `git log --grep="AI-RESP-A"` check).

### TASK-03-batches-api.md (AI-RESP-D) — FRESH structure, stale expectation

- Fresh: verify-before-migrate discipline, blocked-path fallback, JSONL `url` swap steps,
  OpenAI-only surface (no Ollama arm needed — Ollama has no Batches API).
- Stale: written expecting the SDK probably lacks Responses batch support. As of
  `openai-go/v3 v3.41.0` the constant `BatchNewParamsEndpointV1Responses` exists. Run the brief's
  verification step anyway (against the v3 modcache path, not v2) and take the SUPPORTED branch.
- At execution re-verify: `grep -n '/v1/chat/completions' internal/ai/openai_batch.go` (expect ≥2
  hits pre-edit — verified 2026-07-10 in the JSONL `URL:` fields) and the SDK constant in the
  pinned version's `batch.go`.

### TASK-04-aijobs-multiturn.md (AI-RESP-E) — FRESH path, framing needs re-validation

- Fresh: the path-correction note is correct and self-documented — file lives at
  `internal/ai/aijobs/aijobs.go` (re-verify VERBATIM, provenance anchor:
  `grep -n 'package aijobs' internal/ai/aijobs/aijobs.go` — expect 1 hit); `LastResponseID`
  design; the turn-2-doesn't-resend-history test; idempotency direction.
- **Needs re-validation at execution:** the file today is a BATCH-submission helper (builds
  `/v1/chat/completions` JSONL bodies — re-verify: `grep -n '/v1/chat/completions' internal/ai/aijobs/aijobs.go`,
  expect a doc-comment hit and a `"url"` hit), not a live interactive multi-turn loop. The brief's
  multi-turn/`PreviousResponseID` framing presumes a conversational flow; before editing, map where
  multi-turn jobs actually iterate (check `internal/ai/aijobs_adapter.go` and
  `grep -rn 'aijobs\.' internal/ai/ --include=*.go` for callers). If turns are batch rounds, thread
  `LastResponseID` through the round-trip state instead; if no true multi-turn exists, the token
  win shrinks — record that finding and descope with the coordinator rather than inventing a loop.
- Stale (minor): SDK grep paths reference v2 modcache; use v3.

### TASK-05-cleanup-chatcompletions.md (AI-RESP-F) — STALE GOAL (re-scoped by spec Decision 4)

- Fresh: runs-last discipline, four merged-prereq checks, embedding_client.go guard, dead-code
  confirmation loop (`grep -rn` for callers before deleting).
- **Stale (breaking):** the goal "delete remaining Chat Completions call sites" and the acceptance
  "sweep grep returns nothing" are IMPOSSIBLE under the dispatch design — the Chat arms are
  load-bearing for the local backend. Re-scope at dispatch time to: delete only OpenAI-only dead
  helpers/types with zero references; annotate every surviving Chat arm with
  `// AI-RESP-F: retained for local (Ollama) backend`; final sweep grep expects hits, each manually
  confirmed to be a local arm or the annotation comment.
- Its idempotency check (absence-of-old) must likewise flip to: annotations present AND no
  unreferenced Chat-only helpers remain.

### run.sh — STALE usage

- `./run.sh 02 03 04` (parallel) contradicts the serial soak sequence. Use single-task invocations
  only, one per soaked phase, coordinator-gated.

## AI-RESP-C — permanent guard (no brief, by design)

`internal/ai/embedding_client.go` stays on the SDK embeddings call forever (re-verify VERBATIM,
provenance anchor: `grep -n 'Embeddings.New' internal/ai/embedding_client.go` — expect 1 hit,
~lines 360-364). Note the literal string `/v1/embeddings` does NOT appear in the Go source — the
SDK call maps to that endpoint — so a naive `/v1/embeddings` grep proving "nothing to migrate" is
vacuous; the guard is enforced per-PR by `git diff --stat` never containing `embedding_client.go`.
Both backend modes (OpenAI + local bge-m3) run through this client; migrating it would break both.

## Gap work (sections, not new TASK files)

### GAP-1 — backend-dispatch flag (fold into TASK-01's PR; prerequisite for every dispatch arm)

- What: add `useResponsesAPI bool` to `OpenAIParser`
  (`grep -n 'type OpenAIParser struct' internal/ai/openai_parser.go`); the struct currently records
  NO backend identity (`grep -n 'useResponsesAPI\|isLocal' internal/ai/openai_parser.go` — expect
  0 hits at HOLD time). Set `true` only in `register.go`'s `AIBackendModeOpenAI` constructor arm
  (`grep -n 'EffectiveLLMMode' internal/ai/register.go`); `NewOpenAIParserWithBaseURL` calls with a
  local base URL leave it `false`. Fail-safe: zero-value routes to Chat Completions.
- Test: constructor-level — local-mode parser never calls the Responses arm (anti-over-suppression
  twin: OpenAI-mode parser DOES take the Responses arm).
- Polarity: additive. Idempotency: presence of `useResponsesAPI` in both files.

### GAP-2 — dual-backend soak evidence (extends every phase's acceptance, owned by the coordinator)

- What: each soak (plan §Soak protocol) must log evidence for BOTH modes where the surface has a
  local arm (M1, M2): one real op in OpenAI mode and one against local qwen2.5:7b-instruct
  (<gpu-host>). D/E are OpenAI-only. The old briefs' acceptance criteria test only code shape;
  soak-pass is a prod-runtime criterion the briefs cannot self-certify.

### GAP-3 — close-out doc sweep (rides M5)

- Fix the TODO.md dangling link to the nonexistent 2026-04-29 spec → point at
  `docs/specs/2026-07-10-responses-api-migration-design.md`; banner-supersede `orchestration.md` +
  this file; update CHANGELOG.md; close #1260-#1265 with PR links.

## Dispatch checklist (post-hold-lift, per phase — coordinator runs this before handing over a brief)

1. Hold explicitly lifted by the user (plan §M0) — never inferred.
2. Re-run this file's re-verify greps for the phase; any count drift → re-audit before dispatch.
3. Paste the phase's staleness corrections (above) into the dispatch prompt ALONGSIDE the brief.
4. Gate command for every phase: `make ci` — staticcheck is red on main (pre-existing backlog
   #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. Then the
   full `go test ./... -short`.
5. Previous phase merged + deployed + soak-passed (plan §Soak protocol). No exceptions.
