<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-091-remove-dead-expanded-state-in-tagcomparison.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f6ab571-3e7b-4608-ad24-22cbfd956a3a -->
<!-- last-edited: 2026-08-21 -->

# TASK-091 — Remove dead expanded state in TagComparison (TODO.md L5736)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · missing-file-lane subagent · **Why:** Mechanical dead-state removal with no remaining consumers. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 5736 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Dead `expanded` state in `TagComparison`.** `web" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-091-remove-dead-expanded-state-in-tagcomparison" -b agent/missing-file-lane-091-remove-dead-expanded-state-in-tagcomparison origin/main
cd "$REPO/.worktrees/missing-file-lane-091-remove-dead-expanded-state-in-tagcomparison"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Delete the always-true expanded state and its Collapse wrapper in web/src/components/TagComparison.tsx since no toggle UI or test depends on it, per the item's first option ('drop the state and the Collapse'). The alternative (wire up a real toggle) is not chosen because the e2e testid that used to assert it was intentionally deleted 2026-08-09, indicating the toggle was deliberately dropped rather than merely lost.

## Background (verify before editing)

- setExpanded is called in one other place (~L109, inside the snapshot-select useEffect) purely to force it back to true — dead once the state itself is deleted.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'setExpanded(' web/src/components/TagComparison.tsx   # 2 hits, both setExpanded(true) — expanded is initialized true and never set false
  grep -n '<Collapse in={expanded}>' web/src/components/TagComparison.tsx   # 1 hit ~L276 — Collapse wraps the table using expanded
  grep -rn 'tag-comparison-toggle' web/   # 0 hits — no e2e test still depends on a toggle
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In web/src/components/TagComparison.tsx remove the `const [expanded, setExpanded] = useState(true);` line (~L75).
2. Remove the `setExpanded(true);` call inside the snapshot-select useEffect (~L109) — the surrounding effect body (`setCompareId('')`) stays.
3. Replace `<Collapse in={expanded}>` (~L276) and its matching closing `</Collapse>` (~L533) with just the inner content (remove the Collapse wrapper); remove the now-unused `Collapse` import from '@mui/material' (~L10) if nothing else in the file uses it — check with `grep -n Collapse web/src/components/TagComparison.tsx` after the edit.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_091.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Confirm no other component imports TagComparison expecting an `expanded` controlled prop (grep -rn 'expanded=' for TagComparison usages) — none found; it is fully internal state.

## Tests

- Update/add a Vitest test in web/src/components/TagComparison.test.tsx confirming the metadata table renders immediately without needing any expand interaction (i.e. it is present in the DOM on mount, no data-testid="tag-comparison-toggle" is queried).

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web run lint && npm --prefix web run build succeed (no unused-import errors)
- [ ] npm --prefix web test -- TagComparison passes
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_091.md`.

## Commit message

```
refactor(missing-file-lane): Remove dead expanded state in TagComparison (TODO L5736)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Low risk, self-contained single-file cleanup.
