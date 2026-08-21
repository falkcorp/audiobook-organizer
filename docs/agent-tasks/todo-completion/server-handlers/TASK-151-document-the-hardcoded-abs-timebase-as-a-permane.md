<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-151-document-the-hardcoded-abs-timebase-as-a-permane.md -->
<!-- version: 1.0.0 -->
<!-- guid: f768ebc8-81bb-49b8-9cb5-c3bfba7fdf3c -->
<!-- last-edited: 2026-08-21 -->

# TASK-151 — Document the hardcoded ABS timeBase as a permanent, owner-approved allowance (TODO.md L2589)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Why:** Single-line comment addition at a known anchor; no logic change, no design decision left to make (owner already decided). · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 2589 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`timeBase` is hardcoded `\"1/1000\"` at `internal/" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-151-document-the-hardcoded-abs-timebase-as-a-permane" -b agent/server-handlers-151-document-the-hardcoded-abs-timebase-as-a-permane origin/main
cd "$REPO/.worktrees/server-handlers-151-document-the-hardcoded-abs-timebase-as-a-permane"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a comment immediately above `TimeBase: "1/1000",` at internal/server/handlers/abs/mapper.go:645 explaining that this is a deliberate, owner-approved permanent allowance (not a bug): the real ABS reports ffprobe's actual stream time_base (e.g. 1/14112000), but this codebase does not capture time_base at import, and no known client is known to divide by this value, so the field is set to a fixed placeholder rather than adding an ingest field + backfill for a value nothing consumes. Note that the allowance should be revisited only if a client is found to actually use timeBase.

## Background (verify before editing)

- Owner decision 2026-08-12: allow the hardcoded value with a documented permanent allowance rather than add an ingest field and backfill.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '635,648p' internal/server/handlers/abs/mapper.go   # TimeBase: \"1/1000\", with no preceding // comment block specific to it — TimeBase is hardcoded with no explanatory comment today
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/server/handlers/abs/mapper.go and locate line 645 (`TimeBase: "1/1000",` inside the fileView-to-DTO mapping function).
2. Insert a `//` comment block directly above the TimeBase field assignment (or above the containing composite literal's TimeBase line specifically) stating: this value is a fixed placeholder, real ABS reports ffprobe's stream time_base which this codebase does not capture at import, owner-approved 2026-08-12 as a permanent allowance since no known client divides by it, revisit only if that changes.
3. Bump the file's version header per the repo's file-header standard.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_151.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- N/A — documentation-only change.

## Tests

- N/A — comment-only change; existing ABS conformance tests should continue to pass unchanged since no behavior changes.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -B3 'TimeBase:.*1/1000' internal/server/handlers/abs/mapper.go shows a comment block mentioning 'permanent allowance' or equivalent owner-decision language.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_151.md`.

## Commit message

```
refactor(server-handlers): Document the hardcoded ABS timeBase as a permanent, owner-ap (TODO L2589)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Purely a documentation task; the owner decision already resolved the design question.
