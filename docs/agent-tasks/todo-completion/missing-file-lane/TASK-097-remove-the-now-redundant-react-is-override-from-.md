<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-097-remove-the-now-redundant-react-is-override-from-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 431a35d3-af95-4ef6-bfab-b117b0d6f85d -->
<!-- last-edited: 2026-08-21 -->

# TASK-097 — Remove the now-redundant react-is override from web/package.json (TODO-MUI-3)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · missing-file-lane subagent · **Why:** single-line package.json edit plus npm install and a build/test check · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 7603 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**TODO-MUI-3**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-097-remove-the-now-redundant-react-is-override-from-" -b agent/missing-file-lane-097-remove-the-now-redundant-react-is-override-from- origin/main
cd "$REPO/.worktrees/missing-file-lane-097-remove-the-now-redundant-react-is-override-from-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Delete the stale "react-is": "^19.0.0" line from web/package.json's overrides object and reinstall, since react (and MUI) are both already at their post-React-19 target versions and TODO-MUI-3's own text says the override is only needed pre-upgrade.

## Background (verify before editing)

- web/package.json's top-level "overrides" object contains minimatch, brace-expansion, and react-is — only react-is is targeted here.
- TODO-MUI-4's bullet confirms the override should be KEPT only 'if still on React 18' — this repo is on React 19.2.8, so per the plan's own logic the override is now stale.
- react (^19.2.8) and @mui/material (^9.3.1) are both already at their final target versions, so this is safe to do standalone.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'react-is' web/package.json   # 1 hit inside the "overrides" object: "react-is": "^19.0.0" — web/package.json still declares an npm override pinning react-is to ^19.0.0
  grep -n '"react":' web/package.json   # "react": "^19.2.8" — react is already on 19.2.8, the version this override existed to force
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open web/package.json.
2. In the top-level "overrides" object, delete the line `"react-is": "^19.0.0",` and keep minimatch and brace-expansion.
3. Run `cd web && npm install` to regenerate the lockfile without the override.
4. Run `npm run build` and `npm test` inside web/ to confirm nothing regresses now that react-is resolves naturally instead of being forced.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_097.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If some transitive MUI v9 dependency still needs an old react-is pinned and breaks at runtime (prop-type warnings in the console), re-add the override with a comment explaining why rather than silently reverting — but no such dependency was found by this scout.

## Tests

- No new test file needed — rely on the existing web test suite (`npm --prefix web test`) and `npm --prefix web run build` staying green after the override removal.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n 'react-is' web/package.json returns 0 hits after the edit.
- [ ] `cd web && npm run build` exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_097.md`.

## Commit message

```
refactor(missing-file-lane): Remove the now-redundant react-is override from web/package. (TODO-MUI-3)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Low-risk cleanup. package.json has no repo-wide version-header convention (not a .go/.md/.yml doc file), so no header bump applies.
