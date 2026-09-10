<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/TASK-373-abs-sync-task-12-p1-data-loss-class-close-the-th.md -->
<!-- version: 1.0.0 -->
<!-- guid: 67aad794-e683-5fa5-8b0f-ef532b56b2c1 -->
<!-- last-edited: 2026-09-10 -->

# TASK-373 — ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's ID-durability claim  (TODO.md:17185)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified” (L17116), items at lines 17185
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: MIXED — 17185 is CODE (hook RepointSyncItem into the 3 remaining call sites); 17307/17329 are DECISION · class: 17185 is legit data-loss (unrepointed sync ID orphaned on a hard-delete path); 17307 and 17329 are blocked on unresolved owner design decisions · Dispatch the 17185 fix now; 17307/17329 cannot be closed by code alone and wait on the owner's design calls.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — RepointSyncItem (pebble_store_syncid.go:268) is called only from merge/sync_follow.go:236; dedup.MergeBooks (hard-delete) is reached only from reconcile/itunes_heal.go:314 (book_dedup.go:388 flags the gap as a follow-up); CombineBooks (merge/service.go:776) has no RepointSyncItem call — the brief's 3-unhooked-path claim holds.
**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified” (L17116), items at lines 17185. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-373" -b agent/dedup-373-abs-sync-task-12-p1-data-loss-class-clos origin/main
cd "$REPO/.worktrees/dedup-373"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified” (TODO.md line 17116; items at lines 17185 as of HEAD 42d187168):
  - L17185: **ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's ID-durability claim is actually true.** Owner decided (2026-07-30) to hook **all three** paths, not just the worst one. Today only `merge.

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L17185 — **ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's ID-durability claim is actually true.** Owner decided (2026-07-30) to hook **all three** paths, not just the worst one. Today only `merge.Service.MergeBooks` repoints sync IDs; these three still orphan a device's liste — evidence: grep 'RepointSyncItem(' across internal/merge, internal/dedup, internal/scanner shows exactly one call site (internal/merge/sync_follow.go:236, the already-hooked MergeBooks path). dedup.MergeBooks (hard-delete path), CombineBooks, and the scanner's untagged-move CreateBook path are all still unhooked, leaving the 3 identity gaps the item names — including the hard-delete path where an unrepointed sync ID is unrecoverable.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**ABS-SYNC TASK-12 (P1, data-loss class): close the three id" TODO.md   # item L17185 still exists (line numbers drift; text is the anchor)
  sed -n '17116p' TODO.md   # the enclosing heading: SEC: origin is reachable from the LAN — "bind loopback" is NOT achieva
  test -e internal/merge   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_dedup_373.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_dedup_373.md`.

## Commit message

```
fix(dedup): ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps  (TODO.md:17185)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
