<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-368-react-router-ghsa-qwww-vcr4-c8h2-accepted-not-re.md -->
<!-- version: 1.0.0 -->
<!-- guid: e9b37c4b-0970-5904-adc7-9aacc5fbaf14 -->
<!-- last-edited: 2026-09-10 -->

# TASK-368 — react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not re-litigate (TODO.md:15004)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Missing-file lane — follow-ups after the report-only change (#2614)” (L12249), items at lines 15004
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: security — 11964 legit (OperationDef.Permissions enforced by nothing, and enforcement code is about to be deleted); 15004 is a minor accepted-risk dependency bump now unblocked · Both items are real security findings but the brief's section title matches neither item's actual location — substantive mismatch.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — web/package.json: react ^19.2.8, react-router-dom ^7.18.3 — the item's own trigger ('revisit when the app moves to React 19') has fired; re-evaluating the v8 upgrade is what the item asks.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · web subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Missing-file lane — follow-ups after the report-only change (#2614)” (L12249), items at lines 15004. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-368" -b agent/web-368-react-router-ghsa-qwww-vcr4-c8h2-accepte origin/main
cd "$REPO/.worktrees/web-368"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Missing-file lane — follow-ups after the report-only change (#2614)” (TODO.md line 12249; items at lines 15004 as of HEAD 42d187168):
  - L15004: **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not re-litigate.** The v6 → v7.18.2 upgrade (2026-08-06) closed three advisories and opened this one. It is **expected**, it was a deliberate trade, and th

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L15004 — **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not re-litigate.** The v6 → v7.18.2 upgrade (2026-08-06) closed three advisories and opened this one. It is **expected**, it was a deliberate trade, and the analysis is recorded here so the next person to see the alert does not redo it — evidence: web/package.json shows react now at ^19.2.8 (up from 18.3.1 at filing) while react-router-dom is still ^7.18.3 — the item's own 'revisit when the app moves to React 19' trigger has now been met, so the react-router v8.3.0 upgrade (closing GHSA-qwww-vcr4-c8h2) is unblocked and worth re-evaluating.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable" TODO.md   # item L15004 still exists (line numbers drift; text is the anchor)
  sed -n '12249p' TODO.md   # the enclosing heading: Missing-file lane — follow-ups after the report-only change (#2614)
  test -e web/package.json   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_368.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
cd web && npm ci && npm run build && npm test -- --run
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_368.md`.

## Commit message

```
fix(web): react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not re- (TODO.md:15004)

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
