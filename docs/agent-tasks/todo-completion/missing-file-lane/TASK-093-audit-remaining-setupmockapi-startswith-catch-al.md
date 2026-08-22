<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-093-audit-remaining-setupmockapi-startswith-catch-al.md -->
<!-- version: 1.0.0 -->
<!-- guid: feab0b58-0d58-4d90-8328-16a04cf90f49 -->
<!-- last-edited: 2026-08-21 -->

# TASK-093 — Audit remaining setupMockApi startsWith() catch-alls for shadowed specific branches (TODO.md L5758)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · missing-file-lane subagent · **Why:** Read-and-verify ordering audit across one file, no new logic to design. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 5758 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Audit `setupMockApi` for more branches shadowed " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-093-audit-remaining-setupmockapi-startswith-catch-al" -b agent/missing-file-lane-093-audit-remaining-setupmockapi-startswith-catch-al origin/main
cd "$REPO/.worktrees/missing-file-lane-093-audit-remaining-setupmockapi-startswith-catch-al"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 10 remaining `pathname.startsWith(...)` branches in web/tests/e2e/utils/test-helpers.ts's setupMockApi dispatcher, confirm no more-specific `pathname === '...'` branch for a path under that prefix sits below it (which would make the specific branch unreachable and silently fall through to the generic one). Fix ordering for any violation found by moving the specific check above its prefix catch-all, following the existing pattern/comment at ~L1623-1626.

## Background (verify before editing)

- Current startsWith() sites (grep -n '\.startsWith(' web/tests/e2e/utils/test-helpers.ts): L762 /api/v1/backup/, L1571 /api/v1/blocked-hashes/, L1603 /api/v1/audiobooks/ (PUT), L1618 /api/v1/audiobooks/ (DELETE), L1636 /api/v1/audiobooks/ (POST), L1646 /api/v1/metadata/, L1683 /api/v1/ai/, L1715 /api/v1/itunes/import-status/, L1745 /api/v1/version-groups/, L1750 /api/v1/works.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'startsWith' \''(\'\''/api/v1/audiobooks/\'\'')' \'\''and .audiobooks/batch. below below above' web/tests/e2e/utils/test-helpers.ts || grep -n audiobooks/batch web/tests/e2e/utils/test-helpers.ts   # 1 hit ~L1626, preceded by comment ~L1623 — the batch hazard is already fixed with an explanatory comment
  grep -c '\.startsWith(' web/tests/e2e/utils/test-helpers.ts   # 10 — 10 startsWith() prefix catch-alls remain in the dispatcher
  grep -n 'audiobooks/batch' web/tests/e2e/utils/test-helpers.ts   # 2 hits: L1623 comment, L1626 `if (pathname === '/api/v1/audiobooks/batch' && method === 'POST')` — the /audiobooks/batch shadowing hazard is already fixed and carries an explanatory comment
  grep -n '\.startsWith(' web/tests/e2e/utils/test-helpers.ts   # L762, L1571, L1603, L1618, L1636, L1646, L1683, L1715, L1745, L1750 — the 10 sites are at the line numbers the Background lists
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. For each startsWith() line listed above, search the file for any `pathname === '<prefix>...'` (exact-match) branch and note its line number.
2. If an exact-match branch for a path under that prefix exists BELOW the startsWith() branch with the same HTTP method, move the exact-match branch to immediately above its prefix catch-all (mirroring the existing /audiobooks/batch fix and its explanatory comment).
3. If none are found shadowed, no code change is needed for that prefix — just note it as audited.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_093.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A specific branch for a DIFFERENT HTTP method than its prefix catch-all is not shadowed (method is part of the match) — only same-method ordering matters.

## Tests

- No new automated test is strictly required for a pure ordering audit with no findings; if any shadowing IS found and fixed, add/adjust an e2e assertion in the spec exercising that specific endpoint to confirm it now gets its distinct mock response rather than the generic one.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Manual audit table (prefix -> shadowed exact match found: yes/no) attached to the PR description; `npm --prefix web run test:e2e` (or the specific affected spec) still passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_093.md`.

## Commit message

```
refactor(missing-file-lane): Audit remaining setupMockApi startsWith() catch-alls for sha (TODO L5758)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is a confirmatory audit — the two most likely-to-bite cases in the original report are already fixed; expect this to mostly find nothing further, but the task should still be done to close the item honestly rather than assume.
