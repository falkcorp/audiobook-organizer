<!-- file: docs/agent-tasks/ai-responses-migration/TASK-05-cleanup-chatcompletions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 10f64d67-d1d0-4259-a21e-91934967f04a -->
<!-- last-edited: 2026-07-01 -->

# TASK-05 — Delete remaining Chat Completions call sites in internal/ai/ (AI-RESP-F)

> ⚠️ **DEFERRED / OPTIONAL — DO NOT START WITHOUT EXPLICIT GO-AHEAD.** This whole
> workstream (AI-RESP-A/B/D/E/F) is on hold pending a team decision. If you were
> handed this file without an explicit "go ahead and migrate" instruction, STOP
> and ask before touching code.
>
> **This task must run LAST**, only after TASK-01 (AI-RESP-A), TASK-02
> (AI-RESP-B), TASK-03 (AI-RESP-D), and TASK-04 (AI-RESP-E) are all merged to
> `main`. Do not start it early — it deletes code that the other tasks may
> still depend on until they've landed.

**Priority:** P3 (deferred) · **Effort:** S · **Recommended subagent:** Haiku · go-backend subagent · **Depends on:** TASK-01/02/03/04 (AI-RESP-A/B/D/E) all merged

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
# Confirm all four prerequisite tasks are merged before starting:
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-A" | head -1
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-B" | head -1
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-D" | head -1
git -C "$REPO" log origin/main --oneline --grep="AI-RESP-E" | head -1
git -C "$REPO" worktree add "$REPO/.worktrees/ar-cleanup-chatcompletions" -b agent/ar-cleanup-chatcompletions origin/main
cd "$REPO/.worktrees/ar-cleanup-chatcompletions"
git rebase origin/main
```

If any of the four `git log --grep` checks above finds nothing, STOP — that
prerequisite task has not merged. Do not proceed; wait and re-check later.

## Goal

After TASK-01/02/03/04 (AI-RESP-A/B/D/E) have migrated their respective call
sites, sweep `internal/ai/` for any remaining unused Chat Completions call
sites, helper functions, types, or imports and delete them. This is pure
cleanup — no new migrations, no behavior changes. **Do not touch
`internal/ai/embedding_client.go`** — it intentionally stays on
`/v1/embeddings` (AI-RESP-C is a permanent do-not-migrate marker; Chat
Completions is not used there anyway, so this file should not appear in your
sweep results, but call it out explicitly if it does).

## Background (verify before editing)

- First, confirm the four prerequisite tasks actually landed (see START HERE
  checks above — do not skip these).
- Sweep the whole `internal/ai/` package for any remaining Chat Completions
  usage:
  ```bash
  grep -rln "Chat.Completions\|ChatCompletionNewParams\|chat/completions" internal/ai/ internal/ai/aijobs/
  ```
- For every file that shows up, confirm the specific matched code is genuinely
  dead (no remaining call sites reference it, and it's not `embedding_client.go`
  or a file with a legitimate ongoing reason to reference Chat Completions).
  Cross-check with:
  ```bash
  grep -n "Chat.Completions\|ChatCompletionNewParams\|chat/completions" <each-file>
  ```
- If `internal/ai/embedding_client.go` appears in the sweep, read it carefully —
  if it's genuinely using Chat Completions (not just `/v1/embeddings`), flag
  this in the PR description as a discrepancy from the AI-RESP-C marker rather
  than silently deleting/migrating it. If it only uses `/v1/embeddings`
  (expected), leave the file untouched entirely.

## Step-by-step

1. Run the sweep grep above to get the full list of remaining Chat Completions references in `internal/ai/` (excluding `embedding_client.go`).
2. For each file/site found, confirm it is dead code (unreferenced now that A/B/D/E migrated their call sites) — use `grep -rn "<helperFuncName>"` across the package to check for remaining callers before deleting a helper function or type.
3. Delete the dead Chat Completions call sites, unused helper functions/types built specifically for Chat Completions request/response shapes, and any now-unused imports (`openai.ChatCompletionNewParams`-adjacent types, etc.).
4. Run `go vet ./internal/ai/... ./internal/ai/aijobs/...` and `goimports`/`gofmt` (or your repo's standard formatter) to catch any leftover unused imports after deletion.
5. Do NOT modify `internal/ai/embedding_client.go` under any circumstances — verify with `git diff --stat` before committing that this file does not appear in your diff.
6. Bump the file header on every `.go` file you touch (deletions still count as edits).

## How to test

```bash
go build ./internal/ai/... ./internal/ai/aijobs/...
go vet ./internal/ai/... ./internal/ai/aijobs/...
go test ./internal/ai/... ./internal/ai/aijobs/...
grep -rln "Chat.Completions\|ChatCompletionNewParams\|chat/completions" internal/ai/ internal/ai/aijobs/   # should print nothing except embedding_client.go if it's a false-positive match on unrelated text
```

## Acceptance criteria

- [ ] All confirmed-dead Chat Completions call sites, helpers, types, and imports in `internal/ai/` (excluding `embedding_client.go`) are deleted.
- [ ] `internal/ai/embedding_client.go` is untouched (`git diff --stat` confirms it does not appear).
- [ ] `go build ./internal/ai/... ./internal/ai/aijobs/...` and `go test ./internal/ai/... ./internal/ai/aijobs/...` both pass.
- [ ] `go vet` is clean.
- [ ] File headers bumped on every changed file.

## Commit message

```
chore(ai): remove dead Chat Completions call sites after Responses API migration (AI-RESP-F)

Delete unused Chat Completions call sites, helpers, and types left over after
AI-RESP-A/B/D/E migrated internal/ai to the /v1/responses API. Leaves
embedding_client.go untouched (AI-RESP-C, /v1/embeddings stays as-is).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ar-cleanup-chatcompletions
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -rln "Chat.Completions\|ChatCompletionNewParams\|chat/completions" internal/ai/ internal/ai/aijobs/`
returns nothing (or only unrelated false-positive text matches, not actual API
calls), this task is done. Rollback = revert the commit.
