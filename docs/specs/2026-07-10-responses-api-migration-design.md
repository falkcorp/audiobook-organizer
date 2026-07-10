<!-- file: docs/specs/2026-07-10-responses-api-migration-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5914851b-b1ae-436a-99d6-7f84d9ee4286 -->
<!-- last-edited: 2026-07-10 -->

# OpenAI Responses API Migration (AI-RESP-A..F) — Design Spec

**Status:** BLOCKED — awaiting hold-lift on #1260-#1265
**Scope:** Go only, `internal/ai/` + `internal/ai/aijobs/`. THIN spec: this document replaces the
dangling reference `docs/superpowers/specs/2026-04-29-responses-api-migration-design.md` (which does
not exist anywhere in the repo — the only mention is a dangling markdown link in TODO.md; verify:
`grep -rn '2026-04-29-responses-api-migration-design' docs/` returns nothing under docs/). It records
the locked phase sequence, the AI-RESP-C embeddings guard, the prod soak protocol, and the NEW
Ollama-backend-compatibility constraint discovered after the original briefs were written.
**Parent task:** INIT-7 (master plan `.claude/notes/2026-07-10-remaining-work-master-plan.md`), GitHub #1260-#1265

---

## Motivation

`internal/ai` talks to OpenAI through the Chat Completions API. OpenAI's `/v1/responses` API is the
successor: it supports server-side conversation state (`previous_response_id`), which CAN eliminate
resending full message history on every turn of a **turn-sequenced** job — potentially the biggest
token-cost lever in this codebase (AI-RESP-E). That benefit is **contingent, not established**:
`internal/ai/aijobs/aijobs.go` is today a Batch-submission helper (independent JSONL requests
processed as a set — no sequential turns), so the token win is realized ONLY if aijobs is, or is
refactored into, a turn-sequenced loop; if the surface stays batch-only, E reduces to an endpoint
swap with no `previous_response_id` at all (see C5). The migration was specced in April 2026
(#1260-#1265), briefed
in July (`docs/agent-tasks/ai-responses-migration/`), and put **[hold]** pending a team decision.

Grounded call-site inventory (verified 2026-07-10 at HEAD `fce58498`; re-verify greps inline —
line numbers drift):

| Phase | ID | Target | Call sites | Re-verify |
|---|---|---|---|---|
| A | AI-RESP-A | `internal/ai/metadata_llm_review.go` | 1 (`~line 145`) | `grep -n 'Chat.Completions.New' internal/ai/metadata_llm_review.go` |
| B | AI-RESP-B | `internal/ai/openai_parser.go` | 6 (`~lines 207, 312, 396, 456, 602, 724`) | `grep -n 'Chat.Completions.New' internal/ai/openai_parser.go` |
| C | AI-RESP-C | `internal/ai/embedding_client.go` | **GUARD — never migrated** | `grep -n 'Embeddings.New' internal/ai/embedding_client.go` |
| D | AI-RESP-D | `internal/ai/openai_batch.go` | batch endpoint + JSONL `url` fields | `grep -n '/v1/chat/completions' internal/ai/openai_batch.go` |
| E | AI-RESP-E | `internal/ai/aijobs/aijobs.go` | batch-body builder (`package aijobs`) | `grep -n 'package aijobs' internal/ai/aijobs/aijobs.go` and `grep -n '/v1/chat/completions' internal/ai/aijobs/aijobs.go` |
| F | AI-RESP-F | sweep `internal/ai/` | whatever remains after A/B/D/E | `grep -rn 'Chat.Completions\|chat/completions' internal/ai/ internal/ai/aijobs/` |

Path correction (carried from the existing briefs): the aijobs file lives at
`internal/ai/aijobs/aijobs.go`, **not** `internal/aijobs/aijobs.go` — the latter does not exist.

**Goal:** migrate every OpenAI-backend Chat Completions call in `internal/ai` to `/v1/responses`,
lowest-risk-first with prod soak between phases, without touching embeddings and without breaking
the local Ollama backend.

## Goals

- Migrate phases A, B, D, E, F in that order, one PR per phase, each soaking in prod before the next.
- Preserve exact downstream behavior at every call site (same parsed structs, same error semantics).
- AI-RESP-E: unconditionally swap the aijobs batch-body transport to `/v1/responses` (E1);
  additionally thread `previous_response_id` through job state for the token win (E2) ONLY IF
  execution-time re-validation proves the surface is (or is being refactored into) turn-sequenced
  — see C5. The token win is contingent, not established (Motivation, above).
- Keep the local (Ollama) LLM backend fully functional throughout and after the migration.

## Non-goals (v1)

- **Embeddings migration** — permanently out of scope (AI-RESP-C guard, below).
- Migrating the local/Ollama backend to `/v1/responses` — deferred until Ollama's OpenAI-compat
  layer demonstrably supports it; not assumed.
- Any prompt, model-selection, or caching redesign — the migration is transport-shape only.
- Frontend or config-schema changes beyond what backend dispatch requires.

## Decisions (locked during design)

1. **Phase order A → B → D → E → F, strictly serial with prod soak between phases** (losing
   alternative: the old `orchestration.md` wave plan that ran B/D/E in parallel after A — superseded
   by the INIT-7 gate, which requires each phase to soak before the next starts).
2. **AI-RESP-C is a permanent do-not-migrate guard**: `internal/ai/embedding_client.go` stays on
   the SDK Embeddings call (`/v1/embeddings`) forever. Verify the call site with
   `grep -n 'Embeddings.New' internal/ai/embedding_client.go`. Every phase's PR must show
   `git diff --stat` NOT containing `embedding_client.go`.
3. **NEW — Ollama-backend compatibility constraint (post-dates the existing briefs).** Since
   2026-07-02 the LLM path can run on local Ollama (Windows GPU box, `qwen2.5:7b-instruct`). The
   local backend REUSES the same `OpenAIParser` — `internal/ai/register.go` constructs it via
   `NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, ...)` when `cfg.EffectiveLLMMode()` is
   `config.AIBackendModeLocal` (verify: `grep -n 'NewOpenAIParserWithBaseURL' internal/ai/register.go`).
   Every Chat Completions call site in phases A and B is a method on `*OpenAIParser` and therefore
   serves BOTH backends. Ollama's OpenAI-compat layer supports `/v1/chat/completions` but is NOT
   assumed to support `/v1/responses`. Therefore: **`/v1/responses` is called ONLY when the parser's
   backend is OpenAI; the Chat Completions path is RETAINED as the local-backend implementation.**
   Mechanism: a new `useResponsesAPI bool` field on `OpenAIParser`, set at construction time from
   the **mode set in `register.go`'s `EffectiveLLMMode()` switch — the single authority and the
   ONLY predicate for this flag.** The mode enum has FOUR values (`internal/config/config.go`:
   `disabled`, `openai`, `local`, `openai-fallback-local`); the flag is `true` for `openai` AND
   `openai-fallback-local` (both land in the switch's `default:` arm, which constructs a real
   OpenAI client), `false` for `local` and `disabled`. Do NOT key on which constructor was called:
   `NewOpenAIParser` internally calls `NewOpenAIParserWithBaseURL`, so BOTH backends converge on
   the same constructor and function identity cannot discriminate. Caveat for
   `openai-fallback-local`: config.go documents its runtime local fallback as NOT yet implemented
   (intended to be wired in retry.go later; today the mode behaves as plain OpenAI at
   construction, so a construction-time flag is correct). If/when retry.go gains a runtime
   switch-to-Ollama fallback, a static construction-time flag becomes WRONG for that mode — the
   flag must then be re-evaluated per-request; record this as a hard precondition on any future
   retry.go fallback work. Each migrated call site becomes a two-arm dispatch on the field.
   Losing alternatives: (a) a separate `ResponsesParser` type behind an interface — rejected as a
   larger refactor with no behavioral gain for a transport migration; (b) deriving the arm at each
   call site from the cfg the parser already holds (`p.cfg.EffectiveLLMMode() == ...`), which
   needs no new field and no register.go change — rejected because `cfg` may be nil
   (`openai_parser.go` documents "cfg may be nil"), so a raw call-site read can panic, and the
   backend choice is a one-time construction decision, not per-call state (until the
   openai-fallback-local runtime fallback exists — see caveat above).
4. **Consequence for AI-RESP-F (re-scoped):** F can no longer "delete all remaining Chat
   Completions call sites" — the Chat arm is load-bearing for the local backend. F becomes: delete
   only OpenAI-only dead helpers/types made unreachable by A/B/D/E, verify every surviving
   `Chat.Completions` reference is reachable ONLY via the local-backend arm, and document each
   survivor with an `// AI-RESP-F: retained for local (Ollama) backend` comment.
5. **Batches and aijobs (D, E) are OpenAI-only surfaces** — Ollama has no Batches API, so D and E
   migrate unconditionally (no dispatch arm needed there), gated only on SDK/API support. At
   planning time the pinned SDK (`github.com/openai/openai-go/v3 v3.41.0`, verify:
   `grep -n 'openai-go' go.mod`) defines `BatchNewParamsEndpointV1Responses`, so TASK-03's
   "possibly blocked" framing is now "supported — re-verify at execution". **Caveat: that SDK
   claim (the `responses` package + `BatchNewParamsEndpointV1Responses` in v3.41.0) was not
   independently confirmed from the workspace modcache at planning time — only the go.mod pin
   was. BOTH D and E hinge on that single symbol, so M0's modcache re-verify is load-bearing,
   not cosmetic: if the symbol is absent at execution, D and E fall back to TASK-03's
   blocked-path handling instead of proceeding.**
6. **One PR per phase, rebase/FF merges, worktree per phase** — repo standard; each phase is
   independently revertable (single `git revert` + `make deploy`) — **but only during that
   phase's own soak window, before F ships.** F edits the same four files as A/B/D/E and deletes
   helpers those phases made unreachable (Decision 4), so once F has merged, a single-phase
   revert of A/B/D/E will conflict on those files and can reference deleted symbols, breaking the
   build. Any post-F incident must use the reverse-order full rollback (F→A) documented in
   §Rollback, not a single-phase revert.

## Data model

No persistent-store schema changes. One committed struct addition (the dispatch flag) plus one
CONDITIONAL addition (`LastResponseID`, phase E2 only — NOT committed until the E2 gate in C5
passes):

```go
// internal/ai/openai_parser.go — OpenAIParser gains a backend-dispatch flag (GAP-1, phase A).
// Existing struct verified via: grep -n "type OpenAIParser struct" internal/ai/openai_parser.go
type OpenAIParser struct {
	client        *openai.Client
	cfg           *config.Config
	maxRetries    int
	enabled       bool
	responseCache *cache.Cache[*ParsedMetadata]
	defaultModelOverride string

	// useResponsesAPI selects /v1/responses at call sites. Set true ONLY when
	// the parser targets the real OpenAI endpoint (EffectiveLLMMode == openai).
	// Local OpenAI-compatible backends (Ollama) keep Chat Completions.
	useResponsesAPI bool
}
```

```go
// internal/ai/aijobs/aijobs.go — CONDITIONAL (phase E2 ONLY): job state gains server-side
// conversation linkage. LastResponseID persists the previous turn's response ID; second and
// later turns send PreviousResponseID instead of the full message history.
// GATE: this field is added ONLY if the E2 re-validation (C5) proves aijobs is, or is being
// refactored into, a turn-sequenced loop. Today the file is a batch-submission helper with no
// sequential turns (verified anchor) — if that holds, this field is NOT added and phase E is
// the E1 endpoint swap alone. Do not ship unused state.
// Exact struct name/location re-verified at execution:
// grep -n "type.*Job.*struct" internal/ai/aijobs/aijobs.go
//   LastResponseID string `json:"last_response_id,omitempty"`
```

### Persistence

- (E2 only) aijobs job state persists wherever it does today (re-verify at execution how the job
  struct round-trips); `LastResponseID` must survive that round-trip. No new keyspaces. If E2
  does not dispatch, this section is moot — E1 touches no persisted state.
- (D/E operational, supports rollback) each SUBMITTED batch should record which endpoint its
  JSONL was built for, so result-parsing can dispatch or fail-closed across a revert boundary —
  see C4.

## Components

### C1. Backend dispatch flag (`internal/ai/openai_parser.go`, `internal/ai/register.go`) — GAP-1

`useResponsesAPI` set at construction; `register.go`'s LLM-mode switch
(`grep -n 'EffectiveLLMMode' internal/ai/register.go`) is the single authority. Fail-closed:
default `false` (Chat Completions) — an unset flag can never route Ollama traffic to
`/v1/responses`.

### C2. Phase A — `internal/ai/metadata_llm_review.go` (pattern-setter)

The single call site (`grep -n 'Chat.Completions.New' internal/ai/metadata_llm_review.go`) becomes
a two-arm dispatch on `p.useResponsesAPI`. The Responses arm uses the SDK `responses` package
(present in `openai-go/v3 v3.41.0`); prompt content, JSON response-format equivalent, parsing, and
error semantics preserved exactly. This PR establishes the arm shape B copies.

### C3. Phase B — `internal/ai/openai_parser.go` (6 sites)

Same dispatch, applied per-site; each site's request/response shape read individually (they differ
in JSON-schema usage). Incremental `go build ./internal/ai/...` after each site.

### C4. Phase D — `internal/ai/openai_batch.go`

Swap `BatchNewParamsEndpointV1ChatCompletions` → `BatchNewParamsEndpointV1Responses` at ALL
**three** occurrences (~lines 76, 216, 378 — verify:
`grep -n 'BatchNewParamsEndpointV1ChatCompletions' internal/ai/openai_batch.go`, post-edit 0) and
the JSONL per-line `"url": "/v1/chat/completions"` → `"/v1/responses"` at BOTH occurrences
(~lines 184, 348 — `grep -n '/v1/chat/completions' internal/ai/openai_batch.go`, post-edit 0);
a partial swap produces endpoint/url-mismatched batch bodies, so both greps gate the phase. Body
+ result parsing follow the phase-A shape. OpenAI-only surface — no dispatch arm.

**Revert-boundary hazard (batches are async):** batches submitted to `/v1/responses` during the
soak remain in flight on OpenAI's side; if D is reverted while they are pending, the restored
Chat-shape result parser will silently mis-parse their `/v1/responses` result JSONL when they
complete — a fail-open wrong-data path into metadata parsing. Mitigation (pick at
implementation): record the endpoint per submitted batch and have result-parsing dispatch on it
or refuse (fail-closed) when the recorded endpoint != the running code's endpoint; and/or the
rollback procedure must drain/await/cancel all in-flight new-endpoint batches before reverting
(see §Rollback).

### C5. Phase E — `internal/ai/aijobs/aijobs.go` — SPLIT E1/E2

The file today is a Batch-submission helper, not a live interactive loop (verified anchor + the
Motivation section), so E is split; only E1 is unconditional:

- **E1 (unconditional transport swap):** migrate the batch-body builder off
  `/v1/chat/completions` (`grep -n '/v1/chat/completions' internal/ai/aijobs/aijobs.go`) to
  `/v1/responses` — no `previous_response_id` involved. Same revert-boundary hazard and
  mitigation as C4. OpenAI-only surface.
- **E2 (gated — dispatch ONLY on proof of turn-sequencing):** add `LastResponseID` threading:
  first turn sends full input, later turns send `PreviousResponseID` + new-turn input only.
  Gate: the M0/execution re-validation (HOLD-STATUS.md, TASK-04 verdict — map callers via
  `internal/ai/aijobs_adapter.go` and `grep -rn 'aijobs\.' internal/ai/ --include=*.go`) must
  prove aijobs is, or is concurrently being refactored into, a turn-sequenced loop. If it stays
  batch-only, E2 does NOT dispatch: no `LastResponseID` field, no persistence work, no
  multi-turn test (it would have no subject) — record the descope with the coordinator instead
  of inventing a loop.

### C6. Phase F — sweep (re-scoped per Decision 4)

Delete OpenAI-only dead code; annotate retained local-backend Chat arms; never touch
`embedding_client.go`.

## Migration / integration

Per-call-site mechanical pattern (phase A defines the authoritative version). This snippet
SUPERSEDES the replace-style snippets in the 2026-07-01 briefs (TASK-01/02 say "replace
`Chat.Completions.New` with `Responses.New`", which would break the Ollama backend — Decision 3);
where a brief's snippet and this pattern disagree, this pattern wins:

```go
// Before (both backends):
completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...})

// After (dispatch — Chat arm retained verbatim for the local backend):
if p.useResponsesAPI {
	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{...})
	// extract output text via the SDK's Responses result accessor; same downstream parsing
} else {
	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...})
}
```

Exact SDK type/accessor names are pinned during phase A implementation from
`$(go env GOMODCACHE)/github.com/openai/openai-go/v3@v3.41.0/responses/` — not invented here.

## Milestones

- **M0 — hold-lift + re-verify.** No code. Confirm #1260-#1265 hold lifted (explicit user
  statement is the SOLE authorization; issue/label state is corroboration only), re-run every
  re-verify grep in this spec (including register.go, openai_batch.go, and the modcache
  `BatchNewParamsEndpointV1Responses` symbol — Decision 5 caveat), re-audit HOLD-STATUS.md
  verdicts, AND verify a working, funded OpenAI backend is reachable from prod (the
  `/v1/responses` arm is the only changed code in every phase; prod has been local-Ollama-only
  since the 2026-07-02 quota-out). If prod is local-only or the key is unfunded, the sequence
  does NOT start — hard blocker, not a skip.
- **M1 — Phase A** (AI-RESP-A + GAP-1 dispatch flag). Additive dispatch; Chat arm unchanged. SOAK.
- **M2 — Phase B** (AI-RESP-B, 6 sites). SOAK.
- **M3 — Phase D** (AI-RESP-D, batches). SOAK.
- **M4 — Phase E** (AI-RESP-E: E1 aijobs endpoint swap unconditional; E2 `last_response_id` only
  if the C5 turn-sequencing gate passes). SOAK.
- **M5 — Phase F** (AI-RESP-F, re-scoped sweep). Final soak + close #1260-#1265.

Each milestone is one PR, independently shippable and revertable within its own soak window
(Decision 6 — post-F, single-phase reverts of A/B/D/E are no longer clean). The behavior change in
every milestone is confined to the OpenAI backend arm; local mode is bit-identical until F's
comment pass.

## Soak protocol (applies between every pair of consecutive milestones)

0. Precondition (verified at M0, re-checked if time has passed): a working, funded OpenAI backend
   is reachable from prod. Without it the OpenAI arm — the only changed code — cannot be
   exercised, and no soak below can pass.
1. Merge phase PR (rebase/FF) → **snapshot the pre-deploy baseline** (journalctl `internal/ai`
   error-signature counts over a comparable prior window, saved to a recorded artifact — the plan's
   shared loop specifies the command and artifact path; a soak with no saved pre-deploy baseline
   is automatically a FAIL) → `make deploy` → service healthy (`systemctl is-active`, 0 restarts).
2. Soak window: **minimum 48 h** in prod, AND at least one real op exercising the migrated surface
   in EACH backend mode (OpenAI mode + local mode where the surface has a local arm; D/E are
   OpenAI-only). **The OpenAI-mode op is mandatory for every phase**: a soak that exercised only
   the local (Ollama) arm soaked unchanged code and is a FAIL regardless of log cleanliness.
3. Pass criteria: no new `internal/ai` error signatures in journalctl vs. the SAVED pre-deploy
   baseline artifact (diff against the artifact, not memory); no retry-rate increase in the parser
   telemetry; the exercised op completes with output equivalent to pre-migration.
4. Fail → revert the phase commit (for D/E: after draining in-flight new-endpoint batches — see
   §Rollback), `make deploy`, record the failure in HOLD-STATUS.md, STOP the sequence until
   re-planned.
5. Only after pass does the next phase's worktree get created.

## Files modified

| File | Phase | Change |
|---|---|---|
| `internal/ai/openai_parser.go` | A (struct+ctor), B (6 sites), F (sweep) | dispatch flag; per-site dispatch; dead-code sweep |
| `internal/ai/register.go` | A | set `useResponsesAPI` in the OpenAI-mode constructor path |
| `internal/ai/metadata_llm_review.go` | A, F | single-site dispatch; sweep |
| `internal/ai/openai_batch.go` | D, F | endpoint + JSONL url + body/result shapes; sweep |
| `internal/ai/aijobs/aijobs.go` | E, F | `/v1/responses` bodies (E1); `LastResponseID` only if E2 gate passes; sweep |
| `internal/ai/embedding_client.go` | — | **NEVER** (AI-RESP-C guard) |

## Testing

| Test | Asserts |
|---|---|
| `TestMetadataLLMReview*` (existing, extended) | Responses arm produces identical parsed struct; Chat arm untouched when `useResponsesAPI=false` (anti-over-suppression: local backend still works) |
| `TestOpenAIParser*` (existing, extended per site) | each of 6 sites: both arms parse to same downstream data |
| `TestOpenAIBatch*` | endpoint constant (all 3 call sites) + JSONL url (both lines) = `/v1/responses`; result parsing |
| aijobs E1 test | batch-body builder emits `/v1/responses` JSONL |
| aijobs multi-turn test (new, **E2 only** — dropped if the C5 gate fails: a batch-only helper has no turn 2 to assert on) | turn 2 sets `PreviousResponseID`, does NOT resend turn-1 history |
| Full gate | `make ci` — staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. Store-getter rule N/A here, but run the full `go test ./... -short` anyway after each phase. |

## Rollback

**Gate (verbatim):** CONFIRM-HOLD — blocked-on-hold-lift. Issues #1260-#1265 are all marked [hold].
NOTHING executes until the user explicitly lifts the hold. When lifted: sequenced lowest-risk-first
(A -> B -> D -> E -> F), each phase SOAKS in prod before the next; embeddings (/v1/embeddings) are
NEVER migrated (AI-RESP-C guard).

- Per phase: single PR → `git revert <phase commit>` + `make deploy` restores the previous
  transport exactly; no data or schema is touched by A-D/F. Phase E2 (if dispatched) writes
  `LastResponseID` into job state — the field is additive and ignored by reverted code, so revert
  is still clean (stale IDs are simply unused).
- **Revert window (Decision 6):** the single-PR revert above is clean ONLY during that phase's own
  soak window, before F ships. After F merges, F's edits to the same four files (and its deletion
  of helpers A/B/D/E made unreachable) make a lone revert of A/B/D/E conflict-prone and
  build-breaking — any post-F incident uses the reverse-order full rollback below.
- **D/E in-flight batch drain (async revert hazard, see C4):** before reverting D or E,
  drain/await/cancel every batch submitted to `/v1/responses` since that phase's deploy — or rely
  on the per-batch recorded-endpoint guard (result-parsing dispatches on, or fail-closed refuses,
  a batch whose recorded endpoint != the running code's endpoint). Never revert with new-endpoint
  batches pending and no endpoint guard: the restored Chat-shape parser would silently mis-parse
  their results when they land post-revert.
- The dispatch flag defaults `false`: if `register.go`'s flag-set line is reverted alone, the whole
  system falls back to Chat Completions everywhere — fail-safe direction.
- Full-initiative rollback: revert phases in reverse order (F→A); each is an independent commit.

## Open questions (resolved — recorded for the plan)

1. ~~Does the pinned SDK support Responses?~~ → Yes: `openai-go/v3 v3.41.0` ships the `responses`
   package and `BatchNewParamsEndpointV1Responses`. Re-verify at execution (SDK may bump).
2. ~~Does Ollama support `/v1/responses`?~~ → Not assumed. Locked: local backend stays on Chat
   Completions (Decision 3); revisit only with a demonstrated Ollama capability, as a new initiative.
3. ~~Can F delete Chat Completions entirely?~~ → No (Decision 4) — re-scoped to dead-code-only.
4. ~~Is the old parallel wave plan in `docs/agent-tasks/ai-responses-migration/orchestration.md`
   still valid?~~ → No — superseded by the strictly-serial soak sequence (Decision 1).
