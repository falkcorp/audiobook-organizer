<!-- file: docs/agent-tasks/todo-completion/web/TASK-164-let-the-owner-combine-merge-duplicate-books-from.md -->
<!-- version: 1.0.0 -->
<!-- guid: f874f4b4-bceb-40b4-9983-a74571b29c31 -->
<!-- last-edited: 2026-08-21 -->

# TASK-164 — Let the owner combine/merge duplicate books from the metadata chooser, before applying metadata — BLOCKED on two data-correctness bugs landing first (REVIEW-COMBINE-FIRST)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · web subagent · **Why:** New cross-surface UI feature (reach existing combine/merge dialogs from the metadata chooser without losing the chooser's in-progress state) requiring state-management design across two existing components, plus explicit sequencing against two live data-correctness bugs. · **Depends on:** TASK-042, TASK-186 · **Wave:** 7 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2025 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**REVIEW-COMBINE-FIRST**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-164-let-the-owner-combine-merge-duplicate-books-from" -b agent/web-164-let-the-owner-combine-merge-duplicate-books-from origin/main
cd "$REPO/.worktrees/web-164-let-the-owner-combine-merge-duplicate-books-from"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add an entry point from the metadata-chooser/review surface to trigger the EXISTING Combine-into-One-Book and Merge-as-Versions flows before the owner applies metadata to a book row, so a book split across several rows (the item cites 199 books exploded into 6,060 single-file folders as a concrete scale example) can be consolidated first rather than having the same metadata applied to fragments and reconciled afterward. Preserve the chooser's in-progress selection state (candidate match, any manual overrides) across the combine/merge action so the owner isn't forced to restart their metadata search after consolidating.

## Background (verify before editing)

- Both backend capabilities already exist and are wired (Combine: hard-deletes absorbed shells via POST /api/v1/audiobooks/combine; Merge as Versions: soft-deletes losers, demotes to non-primary) — the missing piece is purely reaching them from the metadata-chooser surface without losing chooser state, this is a UI wiring + state-preservation task, not new backend capability.
- Documentation check already done by the item (2026-08-11): the universal review queue (docs/plans/2026-07-13-review-queue-and-regroup.md, review_apply_enabled defaults OFF — see owner decision #8 in this scope's decision list, 'verify prod state and record it only, no flip') is regroup-only and does NOT cover user-initiated combine, dedup review, or metadata review; the bulk-metadata-review and dedup-label-review-panel plans were both archived without shipping — there is no existing 'one home' for this to slot into, it must be designed fresh.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'Combine into One Book' web/src/components/BatchToolbar.tsx   # 1 hit at L128 — Combine into One Book UI entry point exists
  grep -n 'api.combineBooks' web/src/pages/Library.tsx   # 1 hit ~L1306 — combine call site in Library.tsx
  grep -n 'audiobooks/combine' internal/server/wire_dedup_routes.go   # 1 hit at L75, `duplicatesH.CombineBooks` — backend combine route
  ```

### Reuse — don't invent

- Use `api.combineBooks + POST /api/v1/audiobooks/combine (existing combine flow, backend complete)` in `internal/server/wire_dedup_routes.go` (verify: `grep -n 'audiobooks/combine' internal/server/wire_dedup_routes.go`) — do NOT write a parallel helper.
- Use `'Merge as Versions' flow (existing soft-delete merge, backend complete)` in `web/src/pages/Library.tsx` (verify: `grep -n 'handleMergeAsVersions' web/src/pages/Library.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. DO NOT START until L2373 (VG-DOUBLE-PRIMARY, this same scope) part 1 (the forward fix demoting pre-existing group members on merge) has landed — building this on top of the current merge bug would actively make the double-primary problem worse per the item's own warning.
2. Design the entry point: likely a button/menu action in the metadata chooser (find the chooser component — grep for 'MetadataChooser' or similar under web/src/components) that opens the SAME dialog/flow already used by BatchToolbar's 'Combine into One Book' (web/src/components/BatchToolbar.tsx:128) and Library.tsx's Merge-as-Versions action, rather than building parallel dialogs.
3. Preserve chooser state across the action: identify what state the chooser currently holds when the owner is mid-review (candidate match selection, any manual title/author/narrator overrides) and ensure triggering a combine/merge does not discard it — likely requires lifting relevant chooser state up or passing a callback/promise that resumes the chooser once the combine/merge completes and the review surface re-fetches the now-consolidated book.
4. After a combine/merge triggered from the chooser, re-target the chooser at the resulting single (primary) book ID rather than a now-deleted/demoted one, so the metadata application that follows applies to the correct row.
5. This item's dependency #2 (metadata apply from review screen not reaching files) is noted by the item as already fixed on branch fix/review-apply-writes-tags — confirm that fix has landed on main before relying on it (`git log --oneline --all --grep 'review-apply-writes-tags' -i` or similar) rather than assuming from the item text alone.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_164.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the combine/merge fails partway (backend error), the chooser must not silently proceed as if consolidation succeeded — surface the error and let the owner retry or abandon.

## Tests

- web/src/pages/Library.test.tsx or a new component test — trigger combine-from-chooser on a mocked multi-row book, assert the chooser's in-progress candidate selection survives and re-targets the consolidated book ID.
- E2E: a book split across 2 rows, open the metadata chooser on one, trigger combine, confirm only ONE row remains and the chooser is still open/usable against it.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Manual: from the metadata chooser on a multi-row book, combine or merge without leaving the chooser or losing the current candidate selection, and the subsequent metadata apply lands on the single consolidated book.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_164.md`.

## Commit message

```
feat(web): Let the owner combine/merge duplicate books from the metadat (REVIEW-COMBINE-FIRST)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`Manual: from the metadata chooser on a multi-row book, combine or merge without leaving the chooser or losing the current candidate selection, and the subsequent metadata apply lands on the single consolidated book.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: this sits directly on the version-group primary-election bug (L2373) and the apply-writes-tags fix — both are prod-data-correctness paths. Explicitly sequence AFTER L2373 part 1 lands, per the item's own 'Blocked-ish — read first' framing.
