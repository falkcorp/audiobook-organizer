<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-042-forward-fix-demote-pre-existing-version-group-me.md -->
<!-- version: 1.0.0 -->
<!-- guid: e7888b36-2df8-4397-9212-d96ec1be66cf -->
<!-- last-edited: 2026-08-21 -->

# TASK-042 — Forward fix: demote pre-existing version-group members when a merge reuses their group ID (VG-DOUBLE-PRIMARY)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Why:** Correctness-critical write-path fix on the merge path; the change itself is a bounded query+demote loop but must not break the existing single-merge-call primary election. · **Depends on:** none · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2373 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**VG-DOUBLE-PRIMARY**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-042-forward-fix-demote-pre-existing-version-group-me" -b agent/dedup-042-forward-fix-demote-pre-existing-version-group-me origin/main
cd "$REPO/.worktrees/dedup-042-forward-fix-demote-pre-existing-version-group-me"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In internal/merge/service.go's version-group primary-election logic (~L210-240), when reusing an existing versionGroupID, first load EVERY current member of that group (via GetBooksByVersionGroup or equivalent), demote any of them that are not the newly-elected primary (including ones not present in the current call's `books` argument), and only then write the winner as primary — so a group can never end up with two `is_primary_version=true` rows after a merge.

## Background (verify before editing)

- internal/merge/service.go:221-227: version group ID resolution reuses an existing group's ID if any of the current call's books already has one.
- internal/merge/service.go:232-240: the primary/non-primary flag is written ONLY for books in the current call's argument slice — any book that was ALREADY a member of that version group from a PRIOR merge and is not part of THIS call is left untouched, including its (possibly still `true`) IsPrimaryVersion flag.
- Measured on prod 2026-08-11 (per the item): 10 of 15 sampled non-primary-adjacent groups had two members both flagged `is_primary_version=true`, reproduced via the plain (non-search, non-cache) list path — confirmed independent of the separate 24h list-cache staleness bug.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'versionGroupID = \*b.VersionGroupID' internal/merge/service.go   # 1 hit ~L224 — group ID is reused from any book already carrying one
  grep -n 'for i, book := range books' internal/merge/service.go   # 1 hit ~L232, followed by `book.IsPrimaryVersion = &isPrimary` with no lookup of sibling books sharing versionGroupID — the primary-flag loop only touches the `books` argument, never queries for other existing group members
  ```

### Reuse — don't invent

- Use `store.GetBooksByVersionGroup (existing accessor to find ALL current members of a group before writing)` in `internal/database` (verify: `grep -rn 'func.*GetBooksByVersionGroup' internal/database/*.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/merge/service.go, immediately after resolving `versionGroupID` (~L227) and before the primary-election loop, call the store's GetBooksByVersionGroup(versionGroupID) (or equivalent existing accessor — verify its exact signature first) to load every CURRENT member of that group, not just the ones in this call's `books` argument.
2. Build the full candidate set for primary election: the union of (a) books passed into this call and (b) any additional pre-existing members returned by GetBooksByVersionGroup that are not already in (a).
3. Run the existing bestIdx selection logic (or an equivalent 'exactly one winner' rule) over this FULL set, not just the call's argument slice — decide with the coordinator/owner whether an out-of-call pre-existing primary should be allowed to remain the winner if it's genuinely still the best candidate, or whether the newly-merged book should always win; the safest minimal fix is: demote every existing member that is not the currently-elected winner, regardless of which call they came from.
4. Write `IsPrimaryVersion=false` to every non-winning member found in step 1 that is NOT already in the current call's `books` slice (those already get `false` from the existing loop) via `ms.db.UpdateBook`, mirroring the existing per-book UpdateBook call pattern at L235-238 for the ones already handled.
5. Add a code comment at this fix site explaining the invariant: 'a version group must never have more than one is_primary_version=true member; when reusing an existing group ID, ALL current members must be re-evaluated, not just the ones in this call.'

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_042.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A version group with a pre-existing member NOT included in the current merge call's `books` — this is exactly the case this fix must catch; write the test around it specifically, not just the case where all group members are in the call.
- Concurrent merges touching the same version group ID racing on the read-then-write of group membership — out of scope for this specific fix per the item, but worth a TODO comment noting the race exists if not otherwise handled by existing locking.

## Tests

- internal/merge/service_test.go (or wherever merge tests live) — new test: seed 2 books already in a version group with book A as primary; call MergeBooks with a THIRD book that also gets assigned to that same group; assert exactly ONE of the 3 ends up with IsPrimaryVersion=true after the call, and it is NOT necessarily book A if the new candidate is better per BookIsBetter.
- Invariant test (per the item's own explicit request): 'add an invariant test that a group can never have more than one primary, and run it against the existing data as a diagnostic before writing the repair' — write a store-level query/test that scans all version groups and asserts primary count == 1 for each, and run it against a seeded fixture reproducing the reported 10/15 double-primary shape to confirm it FAILS before the fix and PASSES after.

Anti-over-suppression test: `N/A — this is a correctness fix ensuring an invariant, not a filter; the invariant test above IS the safeguard against silently continuing to allow two primaries.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/merge/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The invariant test (group primary count == 1) passes against a fixture reproducing the previously-reported double-primary shape, after the fix.
- [ ] `go test ./internal/merge/...` passes.
- [ ] Anti-over-suppression test: `N/A — this is a correctness fix ensuring an invariant, not a filter; the invariant test above IS the safeguard against silently continuing to allow two primaries.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/merge/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_042.md`.

## Commit message

```
fix(dedup): Forward fix: demote pre-existing version-group members when  (VG-DOUBLE-PRIMARY)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: directly on the merge/version-group write path, the canonical prod-data-correctness class per CLAUDE.md's definition. This is a hard prerequisite for L2025 (REVIEW-COMBINE-FIRST, this same scope) per that item's own explicit 'Blocked-ish' warning.
