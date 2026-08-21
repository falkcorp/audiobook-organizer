<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-111-build-the-pre-apply-snapshot-tool-for-the-138-pe.md -->
<!-- version: 1.0.0 -->
<!-- guid: da370c7c-56c8-4358-a984-1eb61ebb9575 -->
<!-- last-edited: 2026-08-21 -->

# TASK-111 — Build the pre-apply snapshot tool for the 138 pending multidisc holds (TODO.md L8837)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** a read-only report generator over existing review-hold data with an existing pickPrimary helper to call directly — mechanical once the review-hold accessor is found · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8837 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Canary the multidisc applies behind a before/aft" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-111-build-the-pre-apply-snapshot-tool-for-the-138-pe" -b agent/missing-file-lane-111-build-the-pre-apply-snapshot-tool-for-the-138-pe origin/main
cd "$REPO/.worktrees/missing-file-lane-111-build-the-pre-apply-snapshot-tool-for-the-138-pe"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a report-only tool that, for every pending regroup.multidisc review hold, writes a TSV/JSON snapshot to disk BEFORE any apply: every member book ID, title, duration, file path, and which ID pickPrimary would select — so a hard-delete apply has a pre-image to diff against, since the apply path cannot be reconstructed post-hoc.

## Background (verify before editing)

- regroup_apply.go's ApplyMultidisc/ApplyDuplicateOf hard-delete absorbed rows via the combiner.
- pickPrimary(ids []string) string at L391 already implements the exact 'smallest ULID wins' selection this snapshot needs to predict and record.
- The item states this snapshot practice already caught 41 of 43 'confident' candidates elsewhere in this codebase that would have merged distinct novels — not theoretical, an established practice for this exact apply path.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln 'snapshot' internal/plugins/maintenance/regroup*.go   # 0 hits — no snapshot tooling exists for multidisc holds today
  grep -n 'func pickPrimary' internal/plugins/maintenance/regroup_apply.go   # 1 hit ~L391 — pickPrimary (smallest ULID) already exists to predict which member a merge would keep
  ```

### Reuse — don't invent

- Use `pickPrimary` in `internal/plugins/maintenance/regroup_apply.go` (verify: `grep -n 'func pickPrimary' internal/plugins/maintenance/regroup_apply.go`) — do NOT write a parallel helper.

## Step-by-step

1. Find the accessor that lists pending regroup.multidisc review holds and their members (grep -rn 'regroup.multidisc\|ReviewKind' internal/database/review_store.go).
2. Add a report-only function/op that, for each hold, loads every member book's ID/title/duration/file path and calls pickPrimary on the member ID list to record the predicted-primary.
3. Write one TSV row per member per hold (hold_id, book_id, title, duration, file_path, predicted_primary bool).
4. Make this callable standalone (no apply-flag needed — it never writes to the library) so it can run safely even while review_apply_enabled is off in prod.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_111.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A hold whose member book was deleted/changed since the hold was created should still snapshot whatever current data exists, flagging the discrepancy rather than erroring out and skipping the whole hold.

## Tests

- TestMultidiscSnapshot_RecordsAllMembersAndPredictedPrimary — fixture hold with known members asserts every member appears with the correct predicted-primary flag matching pickPrimary's own output.

Anti-over-suppression test: `N/A — report-only, no filter/skip logic to over-suppress.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run MultidiscSnapshot passes.
- [ ] Running the snapshot against the current pending regroup.multidisc holds produces one TSV row per member across all holds.
- [ ] Anti-over-suppression test: `N/A — report-only, no filter/skip logic to over-suppress.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_111.md`.

## Commit message

```
feat(missing-file-lane): Build the pre-apply snapshot tool for the 138 pending multid (TODO L8837)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run MultidiscSnapshot passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is the prerequisite for the actual canary APPLY, which is PARKED per decision 8 (review_apply_enabled: verify prod state and record only; no flip) — see the sibling parked object, same todo_line part 2. Build and land this snapshot tool regardless of when/whether the flag gets flipped; it has standing audit value on its own.
