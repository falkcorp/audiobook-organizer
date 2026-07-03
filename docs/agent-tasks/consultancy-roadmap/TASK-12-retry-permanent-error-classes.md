<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-12-retry-permanent-error-classes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b9347fa-a649-440a-a504-a1f4c6ebdf6e -->
<!-- last-edited: 2026-07-03 -->

# TASK-12 — Permanent-error classification in AI retry paths (MATCH-7 / TOGGLE-4 / BUG-5 / QUAL-6)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-12-retry-permanent-error-classes" -b agent/cr-12-retry-permanent-error-classes origin/main
cd "$REPO/.worktrees/cr-12-retry-permanent-error-classes"
git rebase origin/main
```

## Goal

Both retry implementations in `internal/ai` — the shared `DoWithRetry` helper
and the independent inline loop inside `EmbeddingClient.embedBatchRaw` —
currently retry every error identically with full backoff, including errors
that can never succeed on retry: HTTP 400/401/403/404 and HTTP 429 with an
OpenAI error code of `insufficient_quota` (quota exhaustion is permanent;
plain rate-limit 429s are transient and should still retry). During a live
OpenAI quota outage this burns minutes of wasted backoff per call and gives
the (future, out of scope here) backend-fallback selector no signal to act
on. Add a shared error classifier and make both retry loops short-circuit on
permanent errors, returning a typed `PermanentError` so callers can
distinguish "will never succeed" from "exhausted retries."

Do **not** implement backend fallback, cooldown flags, or a backend-status
endpoint in this task — that is TASK-10's job. This task only adds the
classifier and typed error, and wires both retry loops to consult it.

## Background (verify before editing)

- `DoWithRetry` lives in `internal/ai/retry.go` (currently the whole file is
  ~47 lines). It loops `attempt` from 0 to `maxAttempts-1`, sleeps
  `attempt² × base` between attempts, calls `fn()`, and on any non-nil error
  just does `lastErr = err; continue` — no inspection of `err` at all.
- `EmbeddingClient.embedBatchRaw` in `internal/ai/embedding_client.go` is a
  **second, independent** retry loop (hardcoded 3 attempts, delays
  `1s, 4s`) that calls `c.client.Embeddings.New(...)` (the OpenAI Go SDK v3)
  and on error does `lastErr = fmt.Errorf(...); continue` — same problem,
  duplicated.
- The OpenAI SDK (`github.com/openai/openai-go/v3`, confirmed in `go.mod` as
  `v3.41.0`) exposes API errors as `openai.Error`, which is a type alias:
  `type Error = apierror.Error` (package
  `github.com/openai/openai-go/v3/internal/apierror`). Verify with:
  ```bash
  go doc github.com/openai/openai-go/v3.Error
  ```
  Relevant fields on `*openai.Error` (it implements `error` via a pointer
  receiver, so use `errors.As(err, &apiErr)` with `var apiErr *openai.Error`):
  - `Code string` — the OpenAI error code, e.g. `"insufficient_quota"`,
    `"invalid_api_key"`, `"model_not_found"`.
  - `StatusCode int` — the HTTP status code.
  - `Message string`, `Param string`, `Type string`.
- Classification to implement (per both consultancy findings, which agree):
  - **Permanent** (stop retrying immediately):
    - `StatusCode` is 400, 401, 403, or 404, **or**
    - `StatusCode == 429 && Code == "insufficient_quota"`.
  - **Transient** (keep the existing retry behavior):
    - Any other 429 (plain rate limiting).
    - 5xx status codes.
    - Network/timeout errors that never reach `errors.As` as `*openai.Error`
      at all (e.g. `context.DeadlineExceeded`, connection refused) — treat
      anything that does not classify as `*openai.Error` as transient/unknown
      and retry it, matching current behavior.
- `internal/ai` also has two other `DoWithRetry` callers —
  `internal/ai/openai_parser.go` and `internal/ai/metadata_llm_review.go` —
  that get the permanent-error short-circuit automatically once `DoWithRetry`
  is fixed. Do not modify those two files; confirm they compile unchanged.
- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func DoWithRetry" internal/ai/retry.go
  grep -n "func (c \*EmbeddingClient) embedBatchRaw\|lastErr = fmt.Errorf" internal/ai/embedding_client.go
  grep -rln "DoWithRetry" internal/ai/*.go
  go doc github.com/openai/openai-go/v3.Error
  ```

## Step-by-step

1. In `internal/ai/retry.go`, add:
   - A `PermanentError` type wrapping the underlying error (implements
     `Error() string` and `Unwrap() error` so `errors.Is`/`errors.As` still
     work against the wrapped cause).
   - An unexported (or exported, your call — keep it minimal) classifier,
     e.g. `func isPermanentAIError(err error) bool`, that does the
     `errors.As(err, &apiErr)` check described above against `*openai.Error`
     and returns true only for the permanent cases listed in Background.
     This requires importing `github.com/openai/openai-go/v3` and `errors`
     into `retry.go`.
2. In `DoWithRetry`, after `if err := fn(); err != nil {`, check
   `isPermanentAIError(err)` — if true, wrap `err` in `PermanentError` and
   `return` immediately (no further attempts, no additional backoff sleep).
   Otherwise keep the existing `lastErr = err; continue` behavior unchanged.
3. In `internal/ai/embedding_client.go`, inside `embedBatchRaw`'s loop, after
   `if err != nil {`, check `isPermanentAIError(err)` the same way — if true,
   wrap in `PermanentError` and `return nil, err` immediately instead of
   `continue`-ing into the remaining attempts/delays. Otherwise keep the
   existing `lastErr = ...; continue` behavior unchanged.
4. Do not merge the two loops into one implementation in this task (the
   consultancy "better" suggestion of routing `embedBatchRaw` through
   `DoWithRetry` is a larger refactor — out of scope; leave a `// TODO(#TASK-12-followup):`
   comment at most, do not restructure `embedBatchRaw`'s signature or its
   per-attempt bounded-context pattern).
5. Add `internal/ai/retry_test.go` (new file) with table-driven tests for
   `isPermanentAIError` covering: 400, 401, 403, 404 → permanent; 429 with
   `Code == "insufficient_quota"` → permanent; plain 429 (no code / different
   code) → transient; 500/503 → transient; a plain non-`*openai.Error` error
   (e.g. `errors.New("boom")`, `context.DeadlineExceeded`) → transient. Also
   add a `DoWithRetry`-level test proving a permanent error returned by `fn`
   causes exactly one call to `fn` (no retries) and the returned error
   satisfies `errors.As(..., &PermanentError{})`, versus a transient error
   still retrying up to `maxAttempts`.
6. Add a test (in `internal/ai/embedding_client_test.go` or a new file) that
   spins up an `httptest.Server` returning a canned OpenAI-shaped error body
   (`{"error":{"message":"...","type":"insufficient_quota","code":"insufficient_quota"}}`)
   with HTTP status 429, points `NewEmbeddingClientWithOptions("k", "", server.URL)`
   at it, calls `c.embedBatchRaw(ctx, []string{"x"})` directly, and asserts
   the test server received exactly **1** request (proving no retries) and
   the returned error is a `PermanentError`. Add a second case with a 500
   response asserting the server received all 3 attempts (existing behavior
   preserved for transient errors).
7. Bump the file header (version bump + `last-edited` date) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/ai/... -run 'Retry|Permanent|EmbedBatch' -count=1 -v
go test ./internal/ai/... -count=1
go vet ./internal/ai/...
```

## Acceptance criteria

- [ ] `internal/ai/retry.go` defines `PermanentError` and a classifier that
      identifies 400/401/403/404 and 429-with-`insufficient_quota` as
      permanent via `errors.As` against `*openai.Error`.
- [ ] `DoWithRetry` returns immediately (no further sleep/attempts) on a
      permanent error, wrapped so `errors.As(err, &PermanentError{})` succeeds.
- [ ] `embedBatchRaw` returns immediately (no further sleep/attempts) on a
      permanent error, using the same classifier — no duplicated
      classification logic between the two call sites.
- [ ] Plain rate-limit 429s (no `insufficient_quota` code), 5xx errors, and
      non-`*openai.Error` errors (network/timeout) still retry exactly as
      before (no behavior change for the transient path).
- [ ] `internal/ai/retry_test.go` (new) table-tests the classifier and proves
      `DoWithRetry`'s short-circuit behavior for both permanent and transient
      cases.
- [ ] An `httptest.Server`-backed test proves `embedBatchRaw` makes exactly 1
      HTTP call on a permanent (429/insufficient_quota) response and all 3
      attempts on a transient (5xx) response.
- [ ] `internal/ai/openai_parser.go` and `internal/ai/metadata_llm_review.go`
      compile unchanged (they inherit the fix via `DoWithRetry` with no edits
      needed) — confirm with `go build ./internal/ai/...`.
- [ ] `go test ./internal/ai/...` is green; `go vet ./internal/ai/...` is clean.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(ai): classify permanent vs transient errors in retry paths (MATCH-7/TOGGLE-4/BUG-5/QUAL-6)

DoWithRetry and embedBatchRaw retried every error identically with full
quadratic/fixed backoff, including HTTP 400/401/403/404 and 429
insufficient_quota — none of which can succeed on retry. During the live
OpenAI quota outage this burned minutes per call for a doomed result. Add
a shared classifier consulted by both loops so permanent errors fail fast
as a typed PermanentError, while rate-limit 429s, 5xx, and network errors
keep retrying as before.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-12-retry-permanent-error-classes
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/ai/retry.go` already defines a `PermanentError` type (or
equivalent) and `DoWithRetry` already short-circuits on
400/401/403/404/insufficient_quota-429, this task is done — verify with:

```bash
grep -n "PermanentError\|isPermanentAIError\|insufficient_quota" internal/ai/retry.go internal/ai/embedding_client.go
```

If only one of the two loops has been fixed (e.g. `DoWithRetry` classifies
but `embedBatchRaw` still retries unconditionally), do the remaining half
only — do not duplicate the classifier, import and call the one already
added to `retry.go`. Rollback = revert the commit; both retry loops fall
back to unconditional-retry behavior, which is the pre-existing (working,
just slow-to-fail) state — no data-affecting side effects to unwind.
