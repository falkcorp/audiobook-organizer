<!-- file: docs/agent-tasks/todo-completion/server/TASK-207-duplicate-reference-internal-server-pkg-stall-st.md -->
<!-- version: 1.0.0 -->
<!-- guid: 617dab43-fb13-468d-a1f2-6473155a1f01 -->
<!-- last-edited: 2026-08-21 -->

# TASK-207 — (duplicate reference) INTERNAL-SERVER-PKG-STALL structural decision -- see todo_line 10104 (TODO-SRVTIMEOUT)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · server subagent · **Why:** identical to todo_line 10104 -- see that item's why_tier · **Depends on:** TASK-206 · **Wave:** 2

Source: `TODO.md` line 10600 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**INTERNAL-SERVER-PKG-STALL structural decision** " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-207-duplicate-reference-internal-server-pkg-stall-st" -b agent/server-207-duplicate-reference-internal-server-pkg-stall-st origin/main
cd "$REPO/.worktrees/server-207-duplicate-reference-internal-server-pkg-stall-st"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Do not implement this as a second, separate task. This TODO.md line is a duplicate reference to todo_line 10104's item (same TODO-SRVTIMEOUT/INTERNAL-SERVER-PKG-STALL work); the coordinator should check off BOTH TODO.md checkboxes (line ~10600 item 26, and the fuller entry near line 10104) once the single underlying fix (a newTestServer helper + ~60 migrated call sites, per owner decision #6) lands.

## Background (verify before editing)

- See todo_line 10104's background -- identical underlying task, described twice in TODO.md: once in detail (10104) and once as a one-line numbered-list summary (this block, item 26).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  git show 46628240:TODO.md | sed -n '10600,10601p'   # line 26 text: 'INTERNAL-SERVER-PKG-STALL structural decision (H1:849-877) — leak fixed; residual needs an owner decision: raise timeout / split package / migrate ~60 call sites to newTestServer (see DECISIONS-PENDING).' — this block is the same item as todo_line 10104, referenced a second time in TODO.md as part of a numbered summary list
  ```

### Reuse — don't invent

- Use `full goal/steps/tests/acceptance already written under todo_line 10104` in `n/a (this scope file's own todo_line 10104 object)` (verify: `n/a`) — do NOT write a parallel helper.

## Step-by-step

1. See todo_line 10104's steps -- do not duplicate the implementation work; implement once, close both TODO.md checkboxes together.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_207.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- N/A -- see todo_line 10104

## Tests

- See todo_line 10104's tests.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] See todo_line 10104's acceptance criteria -- satisfying those closes this line too.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_207.md`.

## Commit message

```
refactor(server): (duplicate reference) INTERNAL-SERVER-PKG-STALL structural d (TODO-SRVTIMEOUT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Pure duplicate-reference handling per the coordinator's own instruction to split multi-deliverable blocks -- this is the inverse case (one deliverable, two TODO.md locations). Keep this as a thin object so the collision matrix doesn't double-count internal/server/server_test.go as two independent, possibly-conflicting tasks.
