<!-- file: docs/agent-tasks/ai-responses-migration/TASK-03-batches-api.md -->
<!-- version: 1.0.0 -->
<!-- guid: b70e3e1a-29cc-46e3-bf72-d45e1a4c1887 -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — Migrate Batches API (openai_batch.go) — verify allowlist first (AI-RESP-D)

> ⚠️ **DEFERRED / OPTIONAL — DO NOT START WITHOUT EXPLICIT GO-AHEAD.** This whole
> workstream (AI-RESP-A/B/D/E/F) is on hold pending a team decision. If you were
> handed this file without an explicit "go ahead and migrate" instruction, STOP
> and ask before touching code.
>
> **This task is additionally conditional on external verification.** Do NOT
> migrate anything until you have confirmed the OpenAI Batches API actually
> supports `/v1/responses` as a batch endpoint. If it does not, stop after the
> verification step and document the blocker — do not force a migration onto
> an unsupported endpoint.

**Priority:** P3 (deferred) · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** TASK-01 (AI-RESP-A) merged; conditional on OpenAI Batches API supporting `/v1/responses`

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-A" | head -1   # confirm TASK-01 merged
git -C "$REPO" worktree add "$REPO/.worktrees/ar-batches-api" -b agent/ar-batches-api origin/main
cd "$REPO/.worktrees/ar-batches-api"
git rebase origin/main
```

## Goal

Migrate `internal/ai/openai_batch.go`'s Batches API usage from targeting
`/v1/chat/completions` to targeting `/v1/responses`, **but only if** the OpenAI
Batches API supports `/v1/responses` as a valid batch endpoint. If it does not
(at the time you run this task), stop after verifying, document the blocker in
the PR description (or as a comment in this file's place if no code changes are
made), and do not proceed with a partial/broken migration.

## Background (verify before editing)

- Current batch endpoint usage, originally reported at lines 76, 184, 216, 348,
  378. Re-verify with:
  ```bash
  grep -n "BatchNewParamsEndpointV1ChatCompletions\|/v1/chat/completions\|Endpoint:" internal/ai/openai_batch.go
  ```
- **Verification step (do this before writing any migration code):** check
  whether the vendored OpenAI Go SDK defines a Responses-API batch endpoint
  constant analogous to `BatchNewParamsEndpointV1ChatCompletions`:
  ```bash
  grep -rn "BatchNewParamsEndpoint" $(go env GOMODCACHE)/github.com/openai/openai-go*/*.go 2>/dev/null
  ```
  If you find a constant like `BatchNewParamsEndpointV1Responses` (or similar),
  the SDK supports it — proceed with migration. If you find only
  `BatchNewParamsEndpointV1ChatCompletions` and no Responses-API equivalent,
  the SDK/API does not yet support Responses-API batches — **stop here**.
  Also check OpenAI's own batch API documentation if you have web access, since
  SDK support and API support can lag each other.

## Step-by-step (only if verification confirms support)

1. Confirm support per the Background verification step above. If unsupported, skip to "If blocked" below and do not modify code.
2. Replace each `Endpoint: openai.BatchNewParamsEndpointV1ChatCompletions` with the Responses-API equivalent constant found in the SDK.
3. Replace each `URL: "/v1/chat/completions"` string (used in the per-line JSONL batch request bodies) with `"/v1/responses"`.
4. Update the per-line request body construction to match the Responses API's request shape (input/instructions instead of messages), following the pattern from TASK-01 (AI-RESP-A).
5. Update the batch-result parsing code to read Responses-API-shaped result bodies instead of Chat-Completions-shaped ones.
6. Update tests in `internal/ai/openai_batch_test.go` (verify name with `ls internal/ai/*openai_batch*test*.go`) to use Responses-API-shaped fixtures.
7. Bump the file header on every `.go` file you touch.

## If blocked (Responses API batch support not yet available)

1. Do not modify `openai_batch.go`.
2. Note the blocker clearly: open a PR-free note, or if you must produce output, add a single-line `// AI-RESP-D: blocked — OpenAI Batches API does not yet support /v1/responses as of <date>. Re-check before attempting migration.` comment near the top of `openai_batch.go`, bump its file header, and commit just that comment with a `chore(ai):` commit documenting the blocker instead of a `feat(ai):` migration commit.
3. Skip the "How to test" / migration acceptance criteria below; only the blocker-documentation commit applies.

## How to test (only if migration proceeded)

```bash
go build ./internal/ai/... ./internal/ai/aijobs/...
go test ./internal/ai/... -run TestOpenAIBatch -v
go test ./internal/ai/...
```

## Acceptance criteria

- [ ] Verified whether OpenAI Batches API supports `/v1/responses` (documented either way).
- [ ] If supported: all `BatchNewParamsEndpointV1ChatCompletions` / `/v1/chat/completions` references in `openai_batch.go` migrated to the Responses API equivalent, tests updated and passing.
- [ ] If NOT supported: no functional code changed; blocker documented in a comment + commit.
- [ ] `go build ./internal/ai/...` passes either way.
- [ ] File headers bumped on every changed file.

## Commit message

If migrated:
```
feat(ai): migrate Batches API to /v1/responses endpoint (AI-RESP-D)

Migrate openai_batch.go's batch endpoint and per-line request bodies from
/v1/chat/completions to /v1/responses, now that the OpenAI SDK/API supports
Responses-API batches. Tests updated.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

If blocked:
```
chore(ai): document Batches API /v1/responses migration blocker (AI-RESP-D)

OpenAI Batches API does not yet support /v1/responses as a batch endpoint.
No functional change; documents the blocker for re-evaluation.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ar-batches-api
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "BatchNewParamsEndpointV1ChatCompletions\|/v1/chat/completions" internal/ai/openai_batch.go`
returns nothing (migrated) or the blocker comment already exists (documented),
this task is done. Rollback = revert the commit.
