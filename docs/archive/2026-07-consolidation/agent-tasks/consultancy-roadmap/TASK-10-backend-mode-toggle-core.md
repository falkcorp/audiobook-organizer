<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-10-backend-mode-toggle-core.md -->
<!-- version: 1.0.0 -->
<!-- guid: a43e97e3-2f83-4c92-bb60-3b0bdc738315 -->
<!-- last-edited: 2026-07-03 -->

# TASK-10 — Backend-mode toggle core (embedding + LLM mode enums, per-config LLM base URL)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus · go-backend subagent · **Depends on:** TASK-04, TASK-06, TASK-12 · **Wave:** 3

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-10-backend-mode-toggle-core" -b agent/cr-10-backend-mode-toggle-core origin/main
cd "$REPO/.worktrees/cr-10-backend-mode-toggle-core"
git rebase origin/main
```

## Read this FIRST, before touching any code

Read the full **"Design: LLM/Embedding Backend-Mode Toggle"** section of
`docs/consultancy/03-matching-and-backends.md` (both the "Primary design
(TOGGLE report)" and "Companion sketch (MATCH report)" subsections, plus
"Reconciling the two designs") in the roadmap-tasks checkout at
`docs/consultancy/03-matching-and-backends.md`. It is reproduced in essence
below, but the source doc has the full reasoning and is the tie-breaker if
this brief and the code disagree. Also skim findings TOGGLE-1 through
TOGGLE-7 (same file) — each documents one concrete gap this task closes.

This is the **Wave 3 capstone**: it assumes TASK-04 (embedding-model skip
fix), TASK-06 (keyless local registration), and TASK-12 (whatever P2 task
precedes it — verify it exists and is merged; if TASK-12 does not exist in
this repo yet, treat its intent as already covered by TASK-04/06 and proceed)
are already merged to `origin/main`. Re-run
`git log --oneline origin/main | grep -i "keyless\|embed-model-skip"` after
your `git fetch` above to confirm TASK-06 and TASK-04 landed before you start
— if they haven't, STOP and flag it; this task builds directly on their
registration-gate changes in `internal/ai/register.go`.

## Goal

Implement the backend-mode toggle **core**: a nested `AIBackendConfig` in the
config blob with independent `EmbeddingMode` / `LLMMode` enums
(`disabled | openai | local | openai-fallback-local`), a per-config LLM base
URL (closing TOGGLE-3 — today `NewOpenAIParser` only honors the process-wide
`OPENAI_BASE_URL` env), a startup blob migration mapping legacy fields onto
the new modes (following the existing `migrateEmbeddingBlob` /
`migrateDedupBlob` / `migrateMetadataScoringBlob` pattern in
`internal/config/persistence.go`), registration wired to honor the effective
mode, and replacement of the one-shot non-atomic `localOllamaOK` startup
flag (TOGGLE-5) with a runtime HTTP-probe-backed `atomic.Bool`. This task does
**not** implement the fallback *trigger* itself (that is TASK-12's error
classification in `retry.go`) — it wires the mode enum and availability plumbing
that the fallback trigger will consume, and gates `EmbedBooksAsync`/batch
paths hard to openai-only modes (TOGGLE-2).

Out of scope for this task (leave for follow-ups): the FE "AI Backends"
Settings card, the `GET /api/v1/ai/backends/status` endpoint, the
model-download-prompt / `ollama pull` op-registry integration, and
per-feature model-name mapping beyond `LocalEmbeddingModel`/`LocalLLMModel`
(TOGGLE-7 is a separate, smaller follow-up). Do not build FE or new HTTP
endpoints in this task — config + core Go wiring only.

## Background (verify before editing — line numbers drift, re-grep first)

```bash
grep -n "func (de \*Engine)\|EmbedBooksAsync\|embeddingModelMatches" internal/dedup/engine.go | head -20
grep -n "type Config struct\|OpenAIAPIKey\|EnableAIParsing\|Embedding EmbeddingConfig\|MetadataScoring MetadataScoringConfig\|ReviewModel" internal/config/config.go
grep -n "func init\|embedclient\|llmparser\|OpenAIAPIKey" internal/ai/register.go
grep -n "func NewOpenAIParser\|OPENAI_BASE_URL\|defaultModel" internal/ai/openai_parser.go
grep -n "func NewEmbeddingClient\|localOllamaOK\|SetOllamaAvailable\|defaultEmbeddingModel\|OPENAI_BASE_URL" internal/ai/embedding_client.go
grep -n "func DoWithRetry" internal/ai/retry.go
grep -n "localOllamaOK\|SetOllamaAvailable\|ollamaDaemon\|toolsCfg.Ollama" internal/server/server.go
grep -n "^func migrate.*Blob\|migrateEmbeddingBlob(blobStr)\|migrateDedupBlob(blobStr)\|migrateMetadataScoringBlob(blobStr)" internal/config/persistence.go
```

As of the roadmap-tasks snapshot (2026-07-02/03), verified in this worktree:

- **`internal/ai/register.go`**: `embedclient` `Build` returns nil when
  `cfg.OpenAIAPIKey == "" || !cfg.Embedding.Enabled` (line ~35), before
  resolving `baseURL := cfg.Embedding.BaseURL` a few lines later.
  `llmparser` `Build` returns nil when `cfg.OpenAIAPIKey == ""` (line ~65) and
  constructs via `NewOpenAIParser(cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing)`
  with no base-URL parameter at all. **If TASK-06 already merged, the key
  gates here should be relaxed to check `Embedding.BaseURL`/an equivalent
  local-base-URL field instead of blocking on key presence — build on top of
  that, don't re-add the key gate.**
- **`internal/ai/openai_parser.go`**: `NewOpenAIParser` (line ~79) reads
  `os.Getenv("OPENAI_BASE_URL")` (line ~85) to set `option.WithBaseURL(...)`.
  `defaultModel = "gpt-5-mini"` (line ~51). There is no
  `NewOpenAIParserWithBaseURL` and no per-client base URL parameter — this is
  the TOGGLE-3 gap, fully greenfield.
- **`internal/ai/embedding_client.go`**: `NewEmbeddingClientWithOptions(apiKey,
  model, baseURL string)` (line ~116) already takes an explicit `baseURL`
  parameter and deliberately avoids `OPENAI_BASE_URL` (see the comment at
  line ~96-112 explaining why — it would redirect every client built without
  an explicit base URL). `defaultEmbeddingModel = "text-embedding-3-large"`
  (line ~92). `localOllamaOK bool` (line ~82, plain bool, not atomic) is set
  once via `SetOllamaAvailable` (line ~141) and read in the batch path (near
  line ~193). This is the TOGGLE-5 data-race-prone flag — mirror the
  `NewEmbeddingClientWithOptions` base-URL pattern for the new
  `NewOpenAIParserWithBaseURL`, and replace `localOllamaOK bool` with
  `localOllamaOK atomic.Bool`.
- **`internal/server/server.go`**: around line ~590-625, wires
  `toolRegistry`/`ollamaDaemon`, and at ~614-624 does a one-shot
  `exec.LookPath`-or-trust-configured-base-URL check, then calls
  `server.embedClient.SetOllamaAvailable(ollamaOK)` exactly once at startup.
  This is the TOGGLE-5 chokepoint to replace with a TTL-cached HTTP probe
  (`GET {LocalBaseURL}/api/tags`, 2s timeout) re-probed on failure.
- **`internal/ai/retry.go`**: `DoWithRetry` (line ~26) treats every error
  identically — no permanent/transient classification exists yet. **Do not
  implement the classifier in this task** (that's TASK-12); this task only
  needs `DoWithRetry`'s signature to remain stable so TASK-12 can add
  classification later without touching this task's callers.
- **`internal/config/config.go`**: `ReviewModel` (Dedup config, line ~137,
  default `"gpt-5-mini"`), `EnableAIParsing`/`OpenAIAPIKey` (Config struct,
  line ~261-262), `Embedding EmbeddingConfig` (line ~308),
  `MetadataScoring MetadataScoringConfig` (line ~316). Defaults are set in a
  large struct literal further down (search for `EnableAIParsing:` and
  `ReviewModel:` — there are two occurrences each, one in the
  viper-load path ~line 782+, one in the hardcoded-defaults path ~line
  1286+). Both must be updated when adding the new nested config.
- **`internal/config/persistence.go`**: the established blob-migration
  pattern is a `migrateXBlob(blob string) (string, bool)` function
  (idempotent: returns `(blob, false)` if already in the new shape or no
  flat legacy keys present) called from the blob-load path (search
  `migrateEmbeddingBlob(blobStr)`, `migrateDedupBlob(blobStr)`,
  `migrateMetadataScoringBlob(blobStr)` for the call sites — all three are
  chained: migrate, then `saveRawBlob` if changed, then feed the mutated
  `blobStr` into the next migration). Your new `migrateAIBackendBlob` must
  follow this exact idempotent shape and be chained in the same place,
  after the existing three.
- **`internal/dedup/engine.go`**: `EmbedBooksAsync` (search
  `EmbedBooksAsync`) submits to the OpenAI Batch API with no backend check;
  its already-embedded skip check (near where `TextHash` is compared) does
  NOT call `embeddingModelMatches`, unlike `prepBookEmbed`'s skip check
  (search `embeddingModelMatches` for both call sites) — this is the
  TOGGLE-2 gap. `internal/plugins/dedup/embed_scan.go` (search `async`) is
  the caller that must downgrade to sync when effective embedding backend
  is not `openai`.

## Step-by-step

1. **Config shape** — in `internal/config/config.go`, add:
   ```go
   type AIBackendConfig struct {
       EmbeddingMode       string `json:"embedding_mode" mapstructure:"embedding_mode"`
       LLMMode             string `json:"llm_mode" mapstructure:"llm_mode"`
       LocalBaseURL        string `json:"local_base_url" mapstructure:"local_base_url"`
       LocalEmbeddingModel string `json:"local_embedding_model" mapstructure:"local_embedding_model"`
       LocalLLMModel       string `json:"local_llm_model" mapstructure:"local_llm_model"`
   }
   ```
   Modes are the string constants `"disabled"`, `"openai"`, `"local"`,
   `"openai-fallback-local"` — define them as exported consts
   (`AIBackendModeDisabled`, etc.) next to the struct so callers don't hardcode
   string literals. Add `AIBackend AIBackendConfig` field to the main `Config`
   struct near `Embedding`/`MetadataScoring`. Wire it into both the
   viper-load path and the hardcoded-defaults path (the two locations you
   found in Background) with defaults: `LocalBaseURL:
   "http://172.16.3.22:11434/v1"`, `LocalEmbeddingModel: "bge-m3"`,
   `LocalLLMModel: "qwen2.5:7b-instruct"`, both modes empty string (resolved
   by migration/effective-mode logic, not hardcoded to a mode at rest).

2. **Effective-mode resolution helper** — add a small helper (e.g.
   `func (c *Config) EffectiveEmbeddingMode() string` /
   `EffectiveLLMMode() string`) that returns the configured mode if
   non-empty, else applies the exact fallback-derivation rule from the design
   doc (`EmbeddingMode` empty → `local` when `Embedding.BaseURL != ""` (and
   copy it into `LocalBaseURL` if that field is still empty), else `openai`
   when `OpenAIAPIKey != "" && Embedding.Enabled`, else `disabled`; `LLMMode`
   empty → `openai` when `OpenAIAPIKey != "" && (EnableAIParsing ||
   MetadataScoring.LLMEnabled)`, else `disabled`). This lets migration be
   optional/defensive — even if the blob migration step is skipped for any
   reason, effective-mode resolution never crashes on an empty mode.

3. **Blob migration** — in `internal/config/persistence.go`, add
   `migrateAIBackendBlob(blob string) (string, bool)` mirroring
   `migrateEmbeddingBlob`'s shape exactly (unmarshal to `map[string]any`,
   check for absence of the new `ai_backend` nested key combined with
   presence of legacy flat signal fields, apply the same derivation rule as
   step 2, write the nested `ai_backend` object, return `(blob, false)` if
   already migrated or no legacy signal present). Chain it into the blob-load
   path immediately after the existing `migrateMetadataScoringBlob` call,
   following the exact `slog.Info` + `saveRawBlob` + reassign `blobStr`
   pattern used by the three existing migrations.

4. **Config API shim** — update `docs/reference/config-api-shape.md`: document
   the new `ai_backend` nested object in the `GET /config` / `PUT /config`
   shape, and note that the legacy `embedding.base_url` / `openai_api_key` /
   `enable_ai_parsing` fields remain readable and are still accepted on `PUT`
   for one release (write-through: setting a legacy field also updates the
   derived `ai_backend` mode via the same effective-mode helper from step 2 on
   next load). Bump that file's version header.

5. **`internal/ai/openai_parser.go`** — add
   `NewOpenAIParserWithBaseURL(cfg *config.Config, apiKey, baseURL, model
   string, enabled bool) *OpenAIParser` that mirrors `NewOpenAIParser` but
   takes an explicit `baseURL` and `model` instead of relying on
   `os.Getenv("OPENAI_BASE_URL")` and `defaultModel`. Keep `NewOpenAIParser`
   as a thin wrapper that calls the new constructor with
   `os.Getenv("OPENAI_BASE_URL")` and `defaultModel` for backward
   compatibility with any direct callers you don't touch in this task.

6. **`internal/ai/embedding_client.go`** — change `localOllamaOK bool` to
   `localOllamaOK atomic.Bool` (import `sync/atomic`); update
   `SetOllamaAvailable` to `c.localOllamaOK.Store(ok)` and all reads (e.g. the
   batch-path check near line ~193) to `c.localOllamaOK.Load()`. Add a new
   exported prober, e.g. `func ProbeOllamaAvailable(ctx context.Context,
   baseURL string, timeout time.Duration) bool` that does `GET
   {baseURL}/api/tags` and returns true on 2xx within `timeout` (default 2s).
   Do not wire a background poller/goroutine in this task if that would
   require new lifecycle plumbing beyond `server.go` — a single re-probe
   inline before `EmbedBatch` calls when `!localOllamaOK.Load()` (cheap
   TTL-style recheck) is sufficient scope for this task; note in the PR
   description if you defer a full background TTL-cache poller as a
   follow-up.

7. **`internal/server/server.go`** — replace the one-shot
   `LookPath`-or-trust check (the block you verified in Background,
   ~line 614-624) with a call to the new `ProbeOllamaAvailable` against
   `cfg.AIBackend.LocalBaseURL` (falling back to `cfg.Embedding.BaseURL` if
   `LocalBaseURL` is empty, for the migration-not-yet-applied case), still
   calling `server.embedClient.SetOllamaAvailable(...)` with the probe result.

8. **`internal/ai/register.go`** — update the `embedclient` and `llmparser`
   `Build` funcs to key off `cfg.EffectiveEmbeddingMode()` /
   `cfg.EffectiveLLMMode()` instead of raw `OpenAIAPIKey`/`BaseURL` presence
   checks (building on top of whatever TASK-06 already changed here — do not
   revert TASK-06's keyless-construction fix):
   - `EffectiveEmbeddingMode() == AIBackendModeDisabled` → nil client (no
     warning needed, this is deliberate).
   - `== AIBackendModeLocal` → `NewEmbeddingClientWithOptions("ollama",
     cfg.AIBackend.LocalEmbeddingModel, cfg.AIBackend.LocalBaseURL)` (dummy
     key "ollama", Ollama ignores it).
   - `== AIBackendModeOpenAI` or `AIBackendModeOpenAIFallbackLocal` → real key
     required, current OpenAI construction path.
   - Same triage for `llmparser`, using the new
     `NewOpenAIParserWithBaseURL` from step 5 with `cfg.AIBackend.LocalBaseURL`
     / `cfg.AIBackend.LocalLLMModel` in local mode.
   - `metadatallmscorer` (TOGGLE-6, search `register.go` near where
     `MetadataScoring.EmbeddingEnabled` is checked at ~line 80): make it
     return nil when `EffectiveLLMMode() == AIBackendModeDisabled`, matching
     how `metadatascorer` already checks `EmbeddingEnabled` at build.

9. **`EmbedBooksAsync` hard-gate (TOGGLE-2)** — in `internal/dedup/engine.go`,
   add a check at the top of `EmbedBooksAsync` that returns a typed error
   (define `ErrBatchUnsupported` in the same package or `internal/ai` — pick
   whichever avoids an import cycle, verify with `go build` — err on
   `internal/ai` since that's where the client mode concept lives) when
   `EffectiveEmbeddingMode() != AIBackendModeOpenAI`. In
   `internal/plugins/dedup/embed_scan.go`, when `async=true` is requested but
   the effective mode isn't `openai`, log a warning and downgrade to the sync
   path instead of calling `EmbedBooksAsync`. While you're in
   `EmbedBooksAsync`'s already-embedded skip check, add the missing
   `embeddingModelMatches` call so it matches `prepBookEmbed`'s skip logic
   (this closes the second half of TOGGLE-2 — stale-dimension vectors
   currently get silently skipped in the async path).

10. **Tests** — add/extend `internal/config/persistence_test.go` (or the
    file holding the other blob-migration tests) covering
    `migrateAIBackendBlob`: legacy `embedding.base_url` set → `EmbeddingMode
    == "local"`; legacy `openai_api_key` set + no base_url → `"openai"`;
    neither → `"disabled"`; idempotent on already-migrated blob (second call
    returns `changed == false`). Add `internal/ai` tests for
    `NewOpenAIParserWithBaseURL` (verify it sets the base URL, doesn't touch
    `OPENAI_BASE_URL` env) and for the `register.go` mode-gated construction
    (nil client in disabled mode, dummy-key construction in local mode). Add
    an `internal/dedup` test asserting `EmbedBooksAsync` returns
    `ErrBatchUnsupported` when effective mode is `local`/`disabled`.

11. Bump file headers (version + `last-edited`) on every file touched:
    `internal/config/config.go`, `internal/config/persistence.go`,
    `internal/ai/register.go`, `internal/ai/embedding_client.go`,
    `internal/ai/openai_parser.go`, `internal/ai/retry.go` (only if touched —
    if you left it untouched per step-by-step scope, do not bump it),
    `internal/server/server.go`, `internal/dedup/engine.go`,
    `internal/plugins/dedup/embed_scan.go`,
    `docs/reference/config-api-shape.md`, plus any new/edited `_test.go`
    files.

## How to test

```bash
go build ./...
go test ./internal/config/... ./internal/ai/... ./internal/dedup/... ./internal/plugins/dedup/... -count=1
go vet ./internal/config/... ./internal/ai/... ./internal/dedup/... ./internal/plugins/dedup/... ./internal/server/...
```

If `make ci` is faster to reason about for the full gate, use it instead —
check `grep -E '^[a-z-]+:' Makefile` first and prefer `make ci` if it covers
the same packages with mocks/staticcheck.

## Acceptance criteria

- [ ] `AIBackendConfig` exists in `internal/config/config.go` with
      `EmbeddingMode`, `LLMMode`, `LocalBaseURL`, `LocalEmbeddingModel`,
      `LocalLLMModel`, wired into both the viper-load and hardcoded-defaults
      paths, with local defaults `bge-m3` / `qwen2.5:7b-instruct` /
      `http://172.16.3.22:11434/v1`.
- [ ] `EffectiveEmbeddingMode()`/`EffectiveLLMMode()` (or equivalent) resolve
      an empty mode from legacy fields per the design doc's derivation rule,
      with tests covering all three derivation branches plus idempotency.
- [ ] `migrateAIBackendBlob` follows the existing
      `migrateEmbeddingBlob`/`migrateDedupBlob`/`migrateMetadataScoringBlob`
      idempotent shape and is chained into the blob-load path in
      `persistence.go` immediately after the existing three migrations.
- [ ] `NewOpenAIParserWithBaseURL` exists in `internal/ai/openai_parser.go`
      taking an explicit base URL and model (never `OPENAI_BASE_URL` env);
      `NewOpenAIParser` still works unchanged for existing callers.
- [ ] `localOllamaOK` is an `atomic.Bool`, not a plain `bool`; `Store`/`Load`
      used consistently at all read/write sites.
- [ ] `server.go`'s startup Ollama-availability check is replaced with an
      HTTP probe against `{LocalBaseURL}/api/tags` (or the equivalent
      fallback field), not just LookPath-or-trust.
- [ ] `register.go`'s `embedclient`/`llmparser`/`metadatallmscorer` Build
      funcs key off `EffectiveEmbeddingMode()`/`EffectiveLLMMode()`, not raw
      key/URL presence, and construct with a dummy key in local mode.
- [ ] `EmbedBooksAsync` returns a typed `ErrBatchUnsupported` (or equivalent)
      when the effective embedding backend isn't `openai`; its
      already-embedded skip check now also calls `embeddingModelMatches`;
      `embed_scan.go`'s async path downgrades to sync with a log line under
      non-openai modes.
- [ ] `docs/reference/config-api-shape.md` documents the new `ai_backend`
      nested config object and legacy-field write-through behavior.
- [ ] `go build ./...`, targeted `go test`, and `go vet` are all green.
- [ ] File headers bumped on every changed file (test files included).

## Commit message

```
feat(config): add AIBackendConfig mode toggle core (embedding+LLM, per-config LLM base URL)

Implements the backend-mode toggle core from docs/consultancy/03-matching-and-backends.md:
nested AIBackendConfig with independent EmbeddingMode/LLMMode enums, startup blob
migration from legacy flat fields, per-config LLM base URL via
NewOpenAIParserWithBaseURL (closing TOGGLE-3), atomic Ollama-availability flag with
an HTTP probe replacing the one-shot LookPath-or-trust startup check (TOGGLE-5),
mode-gated client registration building on TASK-06's keyless construction, and a
hard gate on EmbedBooksAsync/batch paths to openai-only modes with the missing
embeddingModelMatches check restored on the async skip path (TOGGLE-2).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-10-backend-mode-toggle-core
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `AIBackendConfig` already exists in `internal/config/config.go` with all
five fields and `migrateAIBackendBlob` is already chained in
`persistence.go`, re-verify with:
```bash
grep -n "type AIBackendConfig\|EffectiveEmbeddingMode\|EffectiveLLMMode" internal/config/config.go
grep -n "migrateAIBackendBlob" internal/config/persistence.go
grep -n "NewOpenAIParserWithBaseURL" internal/ai/openai_parser.go
grep -n "localOllamaOK atomic.Bool\|localOllamaOK  *atomic" internal/ai/embedding_client.go
grep -n "ErrBatchUnsupported" internal/dedup/engine.go
```
If all five checks find matches, this task is already done — do not
re-implement; instead diff against the acceptance criteria above to find any
partial gaps (e.g. migration exists but register.go still checks raw
`OpenAIAPIKey`) and close only those gaps. Rollback = revert the commit; the
legacy flat config fields (`Embedding.BaseURL`, `OpenAIAPIKey`,
`EnableAIParsing`, `MetadataScoring.LLMEnabled`) are untouched and remain the
source of truth if the migration/effective-mode helper is reverted, so a
revert is safe and non-destructive to existing configs.
