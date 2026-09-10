<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/TASK-342-duplicate-placeholder-book-records-priority-3-of.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a84437f-b797-5e89-b89e-0806efd5bea8 -->
<!-- last-edited: 2026-09-10 -->

# TASK-342 — Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 audit cleanup (TODO.md:2088)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Activity SQLite backend — follow-ups after the cutover” (L2006), items at lines 2088
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE (large repair job, needs a folding/merge design) · class: correctness/data-hygiene (duplicate & placeholder book records) with real data-loss risk in any merge/fold repair if not done via repoint · ⛔ standing-ban contact: none directly, but any merge of the 8,235-row dedup set must repoint not delete · Section matches; scope is very large (three separate repair populations) for one brief, flag for splitting.
**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · maintenance subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Activity SQLite backend — follow-ups after the cutover” (L2006), items at lines 2088. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-342" -b agent/maintenance-342-duplicate-placeholder-book-records-prior origin/main
cd "$REPO/.worktrees/maintenance-342"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Activity SQLite backend — follow-ups after the cutover” (TODO.md line 2006; items at lines 2088 as of HEAD 42d187168):
  - L2088: **Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 audit cleanup.** Full-census audit 2026-09-05 (`library-health-audit-2026-09-05.md`). (1) **8,235 redundant primary book rows are true content/metadat

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L2088 — **Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 audit cleanup.** Full-census audit 2026-09-05 (`library-health-audit-2026-09-05.md`). (1) **8,235 redundant primary book rows are true content/metadata duplicates** (same normalized title+author, placeholders excluded; 5,671 disti — evidence: internal/plugins/maintenance/merge_same_path_dupes.go:1-30 (#3076) is deliberately narrow per its own header comment: 'SAME EXACT FILE PATH only ... never same-directory', targeting the 197-path/396-record stale-pointer artifact from the pre-#3075 organizer bug. It does not implement: normalized title+author dedup with hash confirmation (the 8,235-row / 5,671-pair finding), folding per-chapter split books (Skin Game ×51) back into one record, or a placeholder-title ("read by narrator"/"unknown title") re-parse/re-fetch pass. No other op in internal/plugins/maintenance or internal/dedup implements these.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**Duplicate & placeholder book records — PRIORITY 3 of the 2" TODO.md   # item L2088 still exists (line numbers drift; text is the anchor)
  sed -n '2006p' TODO.md   # the enclosing heading: Activity SQLite backend — follow-ups after the cutover
  test -e internal/plugins/maintenance/merge_same_path_dupes.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_maintenance_342.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_maintenance_342.md`.

## Commit message

```
fix(maintenance): Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 au (TODO.md:2088)

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
