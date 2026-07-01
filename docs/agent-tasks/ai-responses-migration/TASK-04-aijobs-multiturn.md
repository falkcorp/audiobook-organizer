<!-- file: docs/agent-tasks/ai-responses-migration/TASK-04-aijobs-multiturn.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2a0de4bf-619a-4ba7-88c9-332b572756de -->
<!-- last-edited: 2026-07-01 -->

# TASK-04 — Migrate aijobs.go multi-turn flows (add last_response_id) (AI-RESP-E)

> ⚠️ **DEFERRED / OPTIONAL — DO NOT START WITHOUT EXPLICIT GO-AHEAD.** This whole
> workstream (AI-RESP-A/B/D/E/F) is on hold pending a team decision. If you were
> handed this file without an explicit "go ahead and migrate" instruction, STOP
> and ask before touching code.

**Priority:** P3 (deferred) · **Effort:** L · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** TASK-01 (AI-RESP-A) merged

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-A" | head -1   # confirm TASK-01 merged
git -C "$REPO" worktree add "$REPO/.worktrees/ar-aijobs-multiturn" -b agent/ar-aijobs-multiturn origin/main
cd "$REPO/.worktrees/ar-aijobs-multiturn"
git rebase origin/main
```

## Goal

Migrate the multi-turn AI job flows in `internal/ai/aijobs/aijobs.go` from
building `/v1/chat/completions` request bodies to `/v1/responses`, and thread a
`last_response_id` (the SDK's `PreviousResponseID`) through job state so
multi-turn conversations use server-side state instead of resending the full
message history on every turn. This is the biggest token-cost win in the
workstream — a correct migration here should measurably shrink per-turn
request payload size for any job with more than one turn.

> **Path correction:** the original workstream spec referred to this file as
> `internal/aijobs/aijobs.go`. It actually lives at
> `internal/ai/aijobs/aijobs.go`. Use the corrected path below; re-verify with
> `find . -iname aijobs.go` if in doubt.

## Background (verify before editing)

- Confirm the file and current state:
  ```bash
  find . -iname "aijobs.go" -not -path "*/.worktrees/*"
  grep -n "/v1/chat/completions\|chat/completions" internal/ai/aijobs/aijobs.go
  ```
  Originally reported: a comment near line 34 (`Body is the raw
  /v1/chat/completions request body...`) and a literal `"url": "/v1/chat/completions"`
  near line 110. Line numbers drift — use the grep above, not these numbers.
- Confirm there is currently no multi-turn state tracking:
  ```bash
  grep -n "last_response_id\|PreviousResponseID\|previous_response_id" internal/ai/aijobs/aijobs.go
  ```
  (Expect zero matches before this task.)
- Read the job-state struct(s) in this file (or a nearby types file — check
  `grep -rln "type.*Job.*struct" internal/ai/aijobs/`) to understand where
  per-job persistent fields live, since you'll be adding a new field there.
- Read how the current code builds the multi-turn message history (likely
  appending prior turns' messages into a growing `messages` array sent on
  every request) to understand what you're replacing.
- Check the SDK for the Responses API's multi-turn support:
  ```bash
  grep -rn "PreviousResponseID\|previous_response_id" $(go env GOMODCACHE)/github.com/openai/openai-go*/responses/*.go 2>/dev/null
  ```
  This confirms the exact field name to set on subsequent turns' request params.

## Step-by-step

1. Add a `LastResponseID string` (or equivalently-named) field to the aijobs job-state struct so it persists across turns of a job. Check how job state is persisted (in-memory map, PebbleDB, etc.) and make sure the new field round-trips through that persistence layer — search `grep -rn "json:\"" internal/ai/aijobs/aijobs.go` for existing struct tags to match the style.
2. Replace the `/v1/chat/completions` request-body construction with a `/v1/responses` request using the OpenAI SDK's `Responses.New` (or equivalent), following the exact pattern TASK-01 established in `metadata_llm_review.go`.
3. On the **first** turn of a job, send the full input/instructions as before (no `PreviousResponseID` set).
4. On the **second and subsequent** turns, set `PreviousResponseID` (or the SDK's equivalent field) to the job's stored `LastResponseID` instead of resending the full prior message history. Send only the new turn's input.
5. After each successful call, store the returned response's ID into the job's `LastResponseID` field for the next turn.
6. Update the response-parsing code to extract from the Responses API result type instead of the Chat Completions one, preserving downstream JSON/field extraction.
7. Update the comment near the old `/v1/chat/completions` mention to describe the new `/v1/responses` shape and the `last_response_id` mechanism.
8. Update `internal/ai/aijobs/aijobs_test.go` — add or update a test that runs a simulated 2+ turn job and asserts the second turn's request does NOT resend the first turn's full content (i.e., asserts `PreviousResponseID` is set and the input is turn-2-only).
9. Bump the file header on every `.go` file you touch.

## How to test

```bash
go build ./internal/ai/... ./internal/ai/aijobs/...
go test ./internal/ai/aijobs/... -v
go test ./internal/ai/...
```

## Acceptance criteria

- [ ] `internal/ai/aijobs/aijobs.go` no longer builds `/v1/chat/completions` request bodies; it uses `/v1/responses`.
- [ ] Job state carries a `LastResponseID` (or equivalent) field that persists across turns.
- [ ] Second and later turns of a multi-turn job set `PreviousResponseID` and send only the new turn's input (not the full prior history).
- [ ] A test in `internal/ai/aijobs/aijobs_test.go` covers the multi-turn `PreviousResponseID` behavior and passes.
- [ ] `go build ./internal/ai/... ./internal/ai/aijobs/...` and `go test ./internal/ai/aijobs/...` both pass.
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(aijobs): migrate multi-turn flows to /v1/responses with last_response_id (AI-RESP-E)

Migrate aijobs.go from building /v1/chat/completions bodies to /v1/responses,
threading last_response_id (PreviousResponseID) through job state so
multi-turn conversations use server-side state instead of resending full
message history on every turn.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ar-aijobs-multiturn
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "PreviousResponseID\|last_response_id" internal/ai/aijobs/aijobs.go`
finds matches and `grep -n "/v1/chat/completions" internal/ai/aijobs/aijobs.go`
finds none, this task is done. Rollback = revert the commit.
