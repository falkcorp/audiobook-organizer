<!-- file: docs/agent-tasks/ai-responses-migration/TASK-02-openai-parser.md -->
<!-- version: 1.0.0 -->
<!-- guid: d8ae2062-d520-4565-bdb1-d03508cb3653 -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Migrate openai_parser.go single-shot calls (6 sites) (AI-RESP-B)

> ⚠️ **DEFERRED / OPTIONAL — DO NOT START WITHOUT EXPLICIT GO-AHEAD.** This whole
> workstream (AI-RESP-A/B/D/E/F) is on hold pending a team decision to migrate
> `internal/ai` off Chat Completions. If you were handed this file without an
> explicit "go ahead and migrate" instruction, STOP and ask before touching code.

**Priority:** P3 (deferred) · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** TASK-01 (AI-RESP-A) merged to `main`

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
# Confirm TASK-01 (AI-RESP-A) is merged before starting:
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-A" | head -1
git -C "$REPO" worktree add "$REPO/.worktrees/ar-openai-parser" -b agent/ar-openai-parser origin/main
cd "$REPO/.worktrees/ar-openai-parser"
git rebase origin/main
```

If the `git log --grep` check above finds nothing, TASK-01 has not merged yet —
stop and wait; do not proceed on an unmerged base, since this task copies the
migration pattern TASK-01 establishes.

## Goal

Migrate the six single-shot Chat Completions call sites in
`internal/ai/openai_parser.go` to the `/v1/responses` API, following the exact
pattern established by TASK-01 (AI-RESP-A) in `metadata_llm_review.go`.
Preserve behavior and parsing for every call site; update tests.

## Background (verify before editing)

- Six call sites use `p.client.Chat.Completions.New(...)`, originally reported
  at lines 167, 272, 356, 416, 562, 684. Line numbers drift — re-verify with:
  ```bash
  grep -n "Chat.Completions.New" internal/ai/openai_parser.go
  ```
- Look at how TASK-01 migrated `internal/ai/metadata_llm_review.go` — read that
  file's final diff/commit (search `git log --grep="AI-RESP-A"`) or the current
  file content to see the exact `Responses.New` call shape, params struct, and
  response-text extraction pattern to replicate:
  ```bash
  git log --oneline --grep="AI-RESP-A"
  grep -n "client.Responses.New\|responses.ResponseNewParams\|OutputText" internal/ai/metadata_llm_review.go
  ```
- Each of the 6 call sites in `openai_parser.go` may have slightly different
  input shapes (different prompts, some with JSON schema response formats,
  some without). Read each site individually before migrating — do not
  blindly copy-paste one migration across all six without checking each one's
  specific request/response shape.

## Step-by-step

1. List all 6 call sites with `grep -n "Chat.Completions.New" internal/ai/openai_parser.go` and read each one in full (the request construction above it and the response parsing below it).
2. For each site, in order, replace the `Chat.Completions.New` call with the `Responses.New` equivalent, mapping messages/prompt content and any `response_format`/JSON-schema settings to their Responses API equivalents, exactly as TASK-01 did.
3. Update each site's response-parsing code to extract from the Responses API result type instead of `completion.Choices[0].Message.Content`, preserving all downstream JSON/field parsing unchanged.
4. Preserve error handling (nil/empty-response checks) at each site, adapted to the Responses API's shape.
5. After migrating each site, run `go build ./internal/ai/...` to catch type errors incrementally rather than migrating all 6 blind and debugging at the end.
6. Update the corresponding tests in `internal/ai/openai_parser_test.go` (verify the exact file name with `ls internal/ai/*openai_parser*test*.go`) — update mock/fixture responses for each of the 6 test cases to be shaped like Responses API payloads.
7. Bump the file header (version bump + `last-edited`) on every `.go` file you touch.

## How to test

```bash
go build ./internal/ai/... ./internal/ai/aijobs/...
go test ./internal/ai/... -run TestOpenAIParser -v
go test ./internal/ai/...
```

## Acceptance criteria

- [ ] All 6 Chat Completions call sites in `openai_parser.go` now use the `/v1/responses` API.
- [ ] Each site's response parsing produces the same downstream data as before.
- [ ] `grep -n "Chat.Completions.New" internal/ai/openai_parser.go` returns zero matches.
- [ ] Tests updated for all 6 sites and passing.
- [ ] `go build ./internal/ai/... ./internal/ai/aijobs/...` and `go test ./internal/ai/...` both pass.
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(ai): migrate openai_parser.go single-shot calls to /v1/responses (AI-RESP-B)

Migrate all 6 Chat Completions call sites in openai_parser.go to the
/v1/responses API, following the AI-RESP-A pattern. Preserves parsing and
error-handling behavior at each site; tests updated.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ar-openai-parser
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "Chat.Completions.New" internal/ai/openai_parser.go` returns
nothing, this task is done. Rollback = revert the commit.
