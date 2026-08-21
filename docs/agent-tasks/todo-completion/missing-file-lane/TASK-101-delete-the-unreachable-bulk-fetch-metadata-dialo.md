<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-101-delete-the-unreachable-bulk-fetch-metadata-dialo.md -->
<!-- version: 1.0.0 -->
<!-- guid: 835037cb-2a96-4a93-83ee-a8bb2d39089d -->
<!-- last-edited: 2026-08-21 -->

# TASK-101 — Delete the unreachable Bulk Fetch Metadata dialog and its handler (TODO.md L5742)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** Threading removal through a shared props interface across two files without breaking other props on the same interface needs care. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 5742 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Delete the unreachable \"Bulk Fetch Metadata\" dia" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-101-delete-the-unreachable-bulk-fetch-metadata-dialo" -b agent/missing-file-lane-101-delete-the-unreachable-bulk-fetch-metadata-dialo origin/main
cd "$REPO/.worktrees/missing-file-lane-101-delete-the-unreachable-bulk-fetch-metadata-dialo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove the dead Bulk Fetch Metadata dialog (web/src/components/library/LibraryDialogs.tsx:956-1000), its props (bulkFetchDialogOpen, handleCancelBulkFetch, bulkFetchProgress, handleBulkFetchMetadata, and any bulkFetchInProgress/hasSelection props solely feeding it) from LibraryDialogsProps (~L174-178), and the corresponding dead state/handlers in web/src/pages/Library.tsx (bulkFetchDialogOpen at ~L391, bulkFetchProgress at ~L394, handleCancelBulkFetch at ~L1468, and the prop-passing block at ~L2306-2311) — but do NOT remove handleBulkFetchMetadata itself or the working async fetch-then-toast flow, which is the surviving replacement.

## Background (verify before editing)

- The 'Fetch Selected' + 'Review' async flow is the surviving replacement per the item text and confirmed live in handleBulkFetchMetadata (Library.tsx ~L1445-1464).
- Five e2e tests covering the old dialog were already deleted 2026-08-09 per the item; no test coverage will be lost by this removal.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'setBulkFetchDialogOpen(true)' web/src   # 0 hits — the dialog is never opened anywhere in web/src
  grep -n 'bulkFetchDialogOpen' web/src/pages/Library.tsx   # >=2 hits, all false-setting or declaration — state is only ever initialized false and reset false
  grep -n 'api.startBulkMetadataFetch' web/src/pages/Library.tsx   # 1 hit ~L1454 — handleBulkFetchMetadata uses the new async flow, bypassing the dialog
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In web/src/components/library/LibraryDialogs.tsx, delete the `<Dialog open={bulkFetchDialogOpen} ...>...</Dialog>` block (~L956-1000).
2. Remove `bulkFetchDialogOpen`, `handleCancelBulkFetch`, `bulkFetchProgress`, `handleBulkFetchMetadata` (and any bulkFetchInProgress prop feeding only this dialog) from the LibraryDialogsProps interface (~L174-178) and from the component's destructured props (~L320-324).
3. Remove the `getResultLabel` helper if it is used only by this dialog (`grep -n getResultLabel web/src/components/library/LibraryDialogs.tsx` to confirm sole usage before deleting).
4. In web/src/pages/Library.tsx, remove `bulkFetchDialogOpen`/`setBulkFetchDialogOpen` state (~L391), `bulkFetchProgress`/`setBulkFetchProgress` state (~L394), `handleCancelBulkFetch` (~L1468-1473), `bulkFetchCancelRef` if it is unused elsewhere, and the prop-passing lines to LibraryDialogs (~L2306-2311) — but KEEP `handleBulkFetchMetadata` (~L1445) since it is still called from the working 'Fetch Selected' button.
5. Grep for `bulkFetchInProgress` and `BulkActionProgress` type usage after the edit to confirm nothing else references the removed state; delete the BulkActionProgress import/type if it becomes unused.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_101.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Confirm `handleBulkFetchMetadata`'s 'Fetch Selected' button caller (search `onClick={handleBulkFetchMetadata}` or similar in Library.tsx) is untouched — this task removes only the unreachable dialog, not the working button.

## Tests

- Run `npm --prefix web run build` (tsc noUnusedLocals-style errors will surface any leftover references) and `npm --prefix web test -- Library` to confirm no existing test asserted the dialog's presence (if one does, it is itself dead and should be deleted alongside).

Anti-over-suppression test: `N/A — this is a removal task; no filter/guard is being added.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -rn 'bulkFetchDialogOpen' web/src returns 0 hits
- [ ] npm --prefix web run build succeeds
- [ ] npm --prefix web run lint passes
- [ ] Anti-over-suppression test: `N/A — this is a removal task; no filter/guard is being added.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_101.md`.

## Commit message

```
refactor(missing-file-lane): Delete the unreachable Bulk Fetch Metadata dialog and its ha (TODO L5742)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Split from a design/product decision (none needed — the item states this cleanup is 'a separate change... deliberately not bundled with' the e2e repair, i.e. already cleared).
