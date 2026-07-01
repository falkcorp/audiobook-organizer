<!-- file: docs/agent-tasks/ai-responses-migration/TASK-01-metadata-llm-review.md -->
<!-- version: 1.0.0 -->
<!-- guid: d495d0f5-aaba-450b-bf3c-85cf563e436d -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — Migrate metadata_llm_review.go single call to /v1/responses (AI-RESP-A)

> ⚠️ **DEFERRED / OPTIONAL — DO NOT START WITHOUT EXPLICIT GO-AHEAD.** This whole
> workstream (AI-RESP-A/B/D/E/F) is on hold pending a team decision to migrate
> `internal/ai` off Chat Completions. This brief exists so the work is ready to
> run the moment it is greenlit. If you were handed this file without an
> explicit "go ahead and migrate" instruction, STOP and ask before touching code.

**Priority:** P3 (deferred) · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ar-metadata-llm-review" -b agent/ar-metadata-llm-review origin/main
cd "$REPO/.worktrees/ar-metadata-llm-review"
git rebase origin/main
```

## Goal

Migrate the single Chat Completions call in `internal/ai/metadata_llm_review.go`
to the OpenAI `/v1/responses` API, preserving the existing response parsing and
behavior exactly. This task is the **pattern-setter** for the rest of the
workstream (AI-RESP-B/D/E copy whatever approach you land on here), so keep the
migration clean and well-commented.

## Background (verify before editing)

- The call site is around line 145 of `internal/ai/metadata_llm_review.go`,
  using `p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...})`.
  Re-verify with:
  ```bash
  grep -n "Chat.Completions.New\|ChatCompletionNewParams" internal/ai/metadata_llm_review.go
  ```
- Confirm there is currently **zero** `/v1/responses` usage anywhere in
  `internal/ai/` or `internal/ai/aijobs/` (this task introduces the first one):
  ```bash
  grep -rn "client.Responses\|ResponseNewParams\|/v1/responses" internal/ai/ internal/ai/aijobs/
  ```
- **Before writing any migration code**, check what the vendored OpenAI Go SDK
  actually exposes for the Responses API — do not assume the shape matches Chat
  Completions:
  ```bash
  go doc github.com/openai/openai-go/v2/responses 2>/dev/null || \
    find $(go env GOMODCACHE) -maxdepth 2 -iname "openai-go*" 2>/dev/null
  grep -rn "package responses" $(go env GOMODCACHE)/github.com/openai/openai-go*/responses/*.go 2>/dev/null | head -5
  ```
  Look for the client's `Responses.New(...)` method, its params struct (likely
  `responses.ResponseNewParams`), and how the response text/JSON payload is
  returned (likely `resp.OutputText()` or iterating `resp.Output`). If the SDK
  version vendored in `go.mod` does not have a `responses` package at all,
  STOP — bump the SDK version first in a separate prep step, or document the
  blocker and do not proceed with a half-migration.
- Read the full function containing the call site to understand what input
  (system/user messages, JSON schema / response_format) is being sent and how
  the output is parsed (JSON unmarshal, field extraction, error handling).

## Step-by-step

1. Read the current call site fully, including the messages/prompt construction
   above it and the response-parsing code below it.
2. Replace `p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...})`
   with the equivalent `p.client.Responses.New(ctx, responses.ResponseNewParams{...})`
   call (exact package/type names per what you found in the Background step).
   Map the existing system/user prompt content into the Responses API's input
   shape (typically a single `Input` string/union, or an `Instructions` +
   `Input` split — check the SDK types).
3. If the original call used a structured/JSON response format
   (`response_format` / JSON schema), find and use the Responses API's
   equivalent (e.g. `Text.Format` with a JSON schema) so parsing stays
   equivalent.
4. Update the response-parsing code to read from the Responses API result
   (e.g. `resp.OutputText()`) instead of
   `completion.Choices[0].Message.Content`. Keep the downstream JSON
   unmarshal/field-extraction logic unchanged — only the extraction of the raw
   text/JSON string from the SDK response type should change.
5. Preserve all existing error handling (nil-response checks, empty-choice
   checks) — adapt the specific conditions to whatever the Responses API
   returns for empty/error responses, but keep the same fail-safe behavior.
6. Update or add a Go test in `internal/ai/metadata_llm_review_test.go` (or the
   nearest existing test file for this function) covering the new call. If
   tests use a mock HTTP transport or a fake OpenAI client, update the fixture
   to return a Responses-API-shaped payload instead of a Chat-Completions-shaped
   one. Re-verify the test file name with:
   ```bash
   ls internal/ai/*metadata_llm_review*test*.go
   ```
7. Bump the file header (version bump + `last-edited`) on every `.go` file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./internal/ai/... ./internal/ai/aijobs/...
go test ./internal/ai/... ./internal/ai/aijobs/... -run TestMetadataLLMReview -v
go test ./internal/ai/...
```

## Acceptance criteria

- [ ] The single Chat Completions call in `metadata_llm_review.go` now calls the `/v1/responses` API via the OpenAI SDK's `Responses.New` (or equivalent).
- [ ] Response parsing produces the same downstream data structure as before (no behavior change visible to callers).
- [ ] Existing/updated test in `internal/ai/` covers the migrated call and passes.
- [ ] `go build ./internal/ai/... ./internal/ai/aijobs/...` and `go test ./internal/ai/...` both pass.
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(ai): migrate metadata_llm_review.go to /v1/responses API (AI-RESP-A)

Replace the single Chat Completions call in metadata_llm_review.go with the
equivalent /v1/responses call, preserving parsing and error-handling behavior.
This establishes the migration pattern for the rest of the Responses API
workstream (AI-RESP-B/D/E).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ar-metadata-llm-review
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `metadata_llm_review.go` already calls `client.Responses.New` instead of
`client.Chat.Completions.New`, this task is done — verify with
`grep -n "Responses.New\|Chat.Completions.New" internal/ai/metadata_llm_review.go`.
Rollback = revert the commit.
