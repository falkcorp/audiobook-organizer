<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-094-restore-version-group-count-and-current-version-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 31630b9f-033f-40a5-8577-1ff345774d79 -->
<!-- last-edited: 2026-09-02 -->

# TASK-094 — Restore version-group count and current-version marker on Book Detail (TODO.md L6252)

> **Status 2026-09-02:** ✅ DONE — PR #2770 merged 2026-08-23 (a7eeb6b13).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** Touches two related components; needs the version count plumbed to the header chip label. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 6252 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The version-group summary lost its count and its" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-094-restore-version-group-count-and-current-version-" -b agent/missing-file-lane-094-restore-version-group-count-and-current-version- origin/main
cd "$REPO/.worktrees/missing-file-lane-094-restore-version-group-count-and-current-version-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Restore visible group size and 'you are here' signal: (a) extend BookDetailHeader.tsx's 'Version Group Linked' chip label to include the count, e.g. `Version Group Linked (N)` where N = versions.length/allVersions.length; (b) add a visible '(Current)' text/badge next to the version row in BookDetailVersionGroup.tsx where isCurrent is true (~L299).

## Background (verify before editing)

- allVersions is already computed as `versions.length > 0 ? versions : [book]` in BookDetailVersionGroup.tsx (~L176), a ready source for the count.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'Part of version group\|(Current)' web/src   # 0 hits — no count or current-marker text exists anywhere in web/src
  grep -n 'Version Group Linked' web/src/components/bookdetail/BookDetailHeader.tsx   # 1 hit ~L198 — the header chip only says 'Version Group Linked', no count
  grep -n 'const isCurrent = version.id === book.id' web/src/components/bookdetail/BookDetailVersionGroup.tsx   # 1 hit ~L299 — isCurrent is already computed per version row
  ```

### Reuse — don't invent

- Use `isCurrent (existing per-row boolean)` in `web/src/components/bookdetail/BookDetailVersionGroup.tsx` (verify: `grep -n 'const isCurrent' web/src/components/bookdetail/BookDetailVersionGroup.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. In BookDetailHeader.tsx, thread a `versionCount` (or `versions.length`) prop into the component (check current props at the top of the file) and change the Chip label (~L198) from `label="Version Group Linked"` to a template string including the count, e.g. `label={`Version Group Linked (${versionCount})`}`.
2. In BookDetailVersionGroup.tsx, in the groupVersions.map(...) render (~L296+), where `isCurrent` is computed (~L299), render a small Chip or inline text '(Current)' next to that version's title when isCurrent is true.
3. Confirm the count source is consistent between the header chip and the version list (both should reflect allVersions.length, not just linked-but-not-current).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_094.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with version_group_id set but only 1 resolvable version (others deleted) — count should reflect what's actually resolvable, not a stale group size.

## Tests

- Add/extend a Vitest test for BookDetailHeader.tsx asserting the chip label includes the version count for a book with version_group_id set and N linked versions.
- Add/extend a Vitest test for BookDetailVersionGroup.tsx asserting the current version's row renders a visible '(Current)' marker and non-current rows do not.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web test -- BookDetailHeader passes
- [ ] npm --prefix web test -- BookDetailVersionGroup passes
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_094.md`.

## Commit message

```
feat(missing-file-lane): Restore version-group count and current-version marker on Bo (TODO L6252)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -rn 'Part of version group\|(Current)' web/src` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a straightforward restoration, not a product question — no owner decision blocks it, unlike the sibling L5972/L5904 items.
