<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-06-keyless-local-registration.md -->
<!-- version: 1.0.0 -->
<!-- guid: a6ea4580-aeb8-4e62-84c5-dd36cde278b2 -->
<!-- last-edited: 2026-07-03 -->

# TASK-06 — Keyless local-backend registration (drop `OpenAIAPIKey` requirement)

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none · **Wave:** 1

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-06-keyless-local-registration" -b agent/cr-06-keyless-local-registration origin/main
cd "$REPO/.worktrees/cr-06-keyless-local-registration"
git rebase origin/main
```

## Goal

Close TOGGLE-1 / MATCH-8 (`docs/consultancy/03-matching-and-backends.md`): the
`embedclient` and `llmparser` service registrations in `internal/ai/register.go`
currently refuse to construct their client at all when `cfg.OpenAIAPIKey` is
empty — even when an explicit local OpenAI-compatible endpoint (e.g. Ollama) is
configured and needs no key. If an operator removes a now-useless,
quota-exhausted OpenAI key, the local backend silently goes dark (nil client →
nil `metadatascorer` → silent F1 downgrade, no warning) even though the
endpoint is fully reachable. Decouple "should we build this client" from "do we
have a real OpenAI key" by keying off whether an explicit base URL is
configured, passing a dummy bearer token when no real key is present. Do **not**
weaken the real-OpenAI path: when no base URL is configured, a non-empty key is
still mandatory.

Out of scope (do not attempt): the full backend-mode toggle (TOGGLE-3), a
dedicated LLM base-URL config field (`LocalBaseURL`/`LocalLLMModel` — that
config surface does not exist yet), and 429/insufficient_quota error
classification (TOGGLE-4). This task only removes the artificial key
requirement for construction; it does not add new config fields.

## Background (verify before editing)

- `internal/ai/register.go` registers `embedclient` and `llmparser` via
  `serviceregistry.Register`. As of this writing:
  - `embedclient`'s `Build` func returns `(*EmbeddingClient)(nil), nil` when
    `cfg.OpenAIAPIKey == "" || !cfg.Embedding.Enabled` — this happens *before*
    the existing `baseURL := cfg.Embedding.BaseURL` resolution that already
    exists a few lines later (which falls back to `os.Getenv("OPENAI_BASE_URL")`
    when the config field is empty). So a configured `Embedding.BaseURL`
    pointing at local Ollama is never even consulted if the key is empty.
  - `llmparser`'s `Build` func returns `(*OpenAIParser)(nil), nil` when
    `cfg.OpenAIAPIKey == ""`, with no base-URL awareness of its own —
    `NewOpenAIParser` (`internal/ai/openai_parser.go`) itself only consults the
    process-wide `os.Getenv("OPENAI_BASE_URL")` env for its base URL (there is
    no per-config LLM base-URL field yet — that's TOGGLE-3, a separate,
    larger task).
  - `NewEmbeddingClientWithOptions(apiKey, model, baseURL string) *EmbeddingClient`
    (`internal/ai/embedding_client.go`) takes `apiKey` as a plain string and
    only adds `option.WithAPIKey(apiKey)` — it does not itself validate the key
    is non-empty, so passing a dummy value (e.g. `"ollama"`) is safe and Ollama
    ignores the `Authorization` header entirely.
  - `NewOpenAIParser(cfg *config.Config, apiKey string, enabled bool) *OpenAIParser`
    (`internal/ai/openai_parser.go`) returns a disabled parser when
    `apiKey == ""` (`!enabled || apiKey == ""`) — passing a non-empty dummy key
    when a base URL is present makes it proceed to construct a real client
    against that base URL.
  - `internal/server/server.go` has a separate, already-correct availability
    trust fix (PR #1739/#1740, comment "Gate embedding client on Ollama
    availability..."): it treats an explicit `config.AppConfig.Embedding.BaseURL`
    as proof the endpoint exists, independent of the `exec.LookPath` binary
    probe. That logic runs on `server.embedClient` **after** it's constructed —
    it is orthogonal to this task's construction-time gate and must NOT be
    duplicated or altered; just confirm it still compiles/behaves the same once
    `embedClient` is non-nil in more cases than before.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "OpenAIAPIKey == \"\"\|func NewEmbeddingClientWithOptions\|func NewOpenAIParser\b" \
    internal/ai/register.go internal/ai/embedding_client.go internal/ai/openai_parser.go
  ```
  Confirm the server-side availability gate is unchanged in shape:
  ```bash
  grep -n "Gate embedding client on Ollama availability\|ollamaOK :=\|SetOllamaAvailable(ollamaOK)" \
    internal/server/server.go
  ```

## Step-by-step

1. In `internal/ai/register.go`, add a small unexported helper (place it above
   the `init()` func, in the same file, so both registrations share one code
   path):
   ```go
   // resolveAIEndpointKey decides whether an OpenAI-compatible client should be
   // constructed and what API key to hand it. When a real apiKey is configured
   // it's always used. When apiKey is empty but an explicit baseURL is
   // configured (a local OpenAI-compatible backend, e.g. Ollama, which ignores
   // the Authorization header), a dummy bearer is substituted so construction
   // proceeds. When neither is set, construction is skipped — the real-OpenAI
   // path (no baseURL) still requires a real key.
   func resolveAIEndpointKey(apiKey, baseURL string) (resolvedKey string, ok bool) {
       if apiKey != "" {
           return apiKey, true
       }
       if baseURL != "" {
           return "ollama", true
       }
       return "", false
   }
   ```
2. In the `embedclient` `Build` func:
   - Move the `baseURL` resolution (`cfg.Embedding.BaseURL`, falling back to
     `os.Getenv("OPENAI_BASE_URL")`) so it happens *before* the key check.
   - Replace the early-return condition `cfg.OpenAIAPIKey == "" || !cfg.Embedding.Enabled`
     with: return nil when `!cfg.Embedding.Enabled`, then call
     `resolvedKey, ok := resolveAIEndpointKey(cfg.OpenAIAPIKey, baseURL)` and
     return `(*EmbeddingClient)(nil), nil` when `!ok`.
   - Pass `resolvedKey` (not `cfg.OpenAIAPIKey`) into
     `NewEmbeddingClientWithOptions(resolvedKey, cfg.Embedding.Model, baseURL)`.
3. In the `llmparser` `Build` func:
   - Resolve `baseURL := os.Getenv("OPENAI_BASE_URL")` (the only base-URL
     signal `NewOpenAIParser` currently understands — do not invent a new
     config field, that's TOGGLE-3).
   - Replace `if cfg.OpenAIAPIKey == "" { return (*OpenAIParser)(nil), nil }`
     with `resolvedKey, ok := resolveAIEndpointKey(cfg.OpenAIAPIKey, baseURL)`
     and return nil when `!ok`.
   - Pass `resolvedKey` into `NewOpenAIParser(cfg, resolvedKey, cfg.EnableAIParsing)`.
4. Add a log line at `slog.Warn` (or the package's existing logging
   convention — grep for `slog.` usage elsewhere in `internal/ai/register.go`
   or sibling files first) when a dummy key is substituted, so operators can
   see in logs that a tier is running in local/keyless mode rather than
   silently. If the file has no existing logging import, add `log/slog` and
   keep the message factual (no PII, no key value).
5. Do NOT change `internal/server/server.go` — re-verify its availability gate
   still compiles and its semantics are unaffected (it only reacts to
   `server.embedClient != nil`, which is now true in one more case: keyless +
   explicit base URL — which is exactly the case it already special-cases via
   `config.AppConfig.Embedding.BaseURL != ""`).
6. Add unit tests in `internal/ai/register_test.go` (new file) covering the 4
   combinations of `resolveAIEndpointKey`:
   - `apiKey != "", baseURL == ""` → real key returned, `ok == true`.
   - `apiKey != "", baseURL != ""` → real key returned (key takes priority),
     `ok == true`.
   - `apiKey == "", baseURL != ""` → dummy key returned, `ok == true`.
   - `apiKey == "", baseURL == ""` → `ok == false` (construction skipped).
   Test `resolveAIEndpointKey` directly (it's a pure function) rather than
   driving the full `serviceregistry.Container` — that keeps the test fast and
   avoids container-wiring boilerplate unrelated to this fix.
7. Bump the file header (version bump + `last-edited` date) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/ai/... -run 'TestResolveAIEndpointKey|TestRegister' -count=1 -v
go test ./internal/ai/... -count=1
go vet ./internal/ai/...
```

## Acceptance criteria

- [ ] `embedclient` constructs a client when `Embedding.Enabled` and either a
      real `OpenAIAPIKey` OR an explicit `Embedding.BaseURL` (or
      `OPENAI_BASE_URL` env) is set — not both required.
- [ ] `embedclient` still returns nil when `Embedding.Enabled` is false,
      regardless of key/base-URL.
- [ ] `embedclient` still returns nil when neither a key nor a base URL is
      configured (the "nothing configured" case is unchanged).
- [ ] `llmparser` constructs a parser when either a real `OpenAIAPIKey` OR
      `OPENAI_BASE_URL` env is set — not both required.
- [ ] Real-OpenAI path (no base URL configured) still requires a non-empty key
      — this task does not weaken that check.
- [ ] `resolveAIEndpointKey` unit tests cover all 4 key/base-URL combinations
      and pass.
- [ ] `internal/server/server.go`'s Ollama-availability trust logic
      (`ollamaOK := ... || config.AppConfig.Embedding.BaseURL != ""`) is
      untouched and still compiles.
- [ ] `go build ./...`, `go test ./internal/ai/...`, and `go vet ./internal/ai/...`
      are green.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(ai): allow keyless registration of embedclient/llmparser for local backends

embedclient and llmparser both gated construction on a non-empty
OpenAIAPIKey even when an explicit base URL pointed at a local
OpenAI-compatible backend (e.g. Ollama), which needs no key. Removing a
quota-dead OpenAI key silently disabled local embeddings/LLM with no
warning. Add resolveAIEndpointKey to substitute a dummy bearer when a
base URL is configured but no key is set, while still requiring a real
key on the no-base-URL (real OpenAI) path.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-06-keyless-local-registration
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `embedclient`'s `Build` func already resolves `baseURL` before checking the
key, and both `embedclient` and `llmparser` already accept a dummy/placeholder
key when a base URL is present, this task is done — verify with:
```bash
grep -n "resolveAIEndpointKey\|OpenAIAPIKey == \"\"" internal/ai/register.go
```
If `resolveAIEndpointKey` (or an equivalent helper) already exists and is
wired into both `Build` funcs, skip this task. Rollback = revert the commit;
the pre-existing behavior (key-required for both registrations) is fully
restored since this change is additive/gating-only and does not alter
`NewEmbeddingClientWithOptions`, `NewOpenAIParser`, or `server.go`.
