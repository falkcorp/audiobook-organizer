<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-143-n-3-stop-advertising-delete-update-permissions-t.md -->
<!-- version: 1.0.0 -->
<!-- guid: 208434ce-2cc2-46a1-84e2-ef239ff72f54 -->
<!-- last-edited: 2026-08-21 -->

# TASK-143 — N-3: stop advertising Delete/Update permissions the library surface cannot honor (ABS-N3)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** Small, localized DTO change, but requires judgment about what value is truthful (false vs. omit) and checking no client hard-requires true regardless of capability — worth a careful read of the ABS client contract doc, not pure mechanics. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 53 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔌 **ABS coverage gaps N-1 … N-10** (audit:" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-143-n-3-stop-advertising-delete-update-permissions-t" -b agent/server-handlers-143-n-3-stop-advertising-delete-update-permissions-t origin/main
cd "$REPO/.worktrees/server-handlers-143-n-3-stop-advertising-delete-update-permissions-t"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Set userPermissions.Delete and .Update to false in defaultPermissions() (internal/server/handlers/abs/dto.go) so Absorb/AudioBooth do not render edit/delete affordances this server cannot service, matching the honesty principle already applied elsewhere on this surface (e.g. N-1's socket.io fix, PlayMethod always 0).

## Background (verify before editing)

- The comment directly above defaultPermissions() (dto.go, just before L283) already says 'update/delete/download must be present and true or the clients disable working features' — this claim needs to be RE-VERIFIED against docs/reference/abs-target-client-contract.md before flipping the values, since if it is accurate, flipping breaks a currently-working feature (this is exactly the kind of stale in-code justification CLAUDE.md's 'verify the claim before believing it' rule warns about).
- The correct fix depends on what update/delete are gating client-side: if they gate ONLY server-management actions (which we do not implement) it is safe to set false; if they also gate something we DO support (e.g. per-item progress/bookmark deletion, which /api/me DOES implement per userdata.go's UserDataLibraryStore), setting Update/Delete false wholesale would break a working feature — this needs the client-contract doc check as step 1, not a blind flip.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "Delete: *true\|Update: *true" internal/server/handlers/abs/dto.go   # 2 hits inside defaultPermissions() — defaultPermissions sets Delete and Update true
  grep -n "type LibraryStore interface" -A 8 internal/server/handlers/abs/handler.go   # 6 embedded *Reader interfaces only — LibraryStore has no write methods
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read docs/reference/abs-target-client-contract.md for what `update`/`delete` on the user-permissions object gate client-side (search for 'permissions' or 'Delete' in that doc).
2. If update/delete gate ONLY unimplemented server-management actions: in internal/server/handlers/abs/dto.go's defaultPermissions(), change `Delete: true` to `Delete: false` and `Update: true` to `Update: false`, updating the adjacent comment to explain why (cite this TODO / the audit finding).
3. If update/delete ALSO gate something implemented (e.g. bookmark/progress deletion), instead leave them true but add a comment citing the specific implemented capability that requires it, and downgrade this TODO sub-item to not_a_task/false-positive in a follow-up docs note rather than silently closing it.
4. Bump the file version header per repo convention.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_143.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A client that treats permissions.delete=false as 'hide the button' vs 'grey it out and 403 on click' — either way false is more honest than true when no route backs it; do not overthink beyond checking the contract doc.

## Tests

- internal/server/handlers/abs/abs_test.go: TestUserPermissions_DeleteUpdateReflectCapability — assert the /api/me user object's permissions.delete/update match whatever step 2/3 decided, with a comment citing the client-contract doc line that justifies it.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n 'Delete:\|Update:' internal/server/handlers/abs/dto.go` shows the corrected values with a comment citing this decision.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_143.md`.

## Commit message

```
refactor(server-handlers): N-3: stop advertising Delete/Update permissions the library  (ABS-N3)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Read the client-contract doc FIRST (step 1) — the existing in-code comment claiming true is required may itself be stale, per the worked example in this repo's CLAUDE.md about not trusting an unre-verified justification comment.
