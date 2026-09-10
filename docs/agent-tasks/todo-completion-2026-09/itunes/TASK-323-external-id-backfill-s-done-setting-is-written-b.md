<!-- file: docs/agent-tasks/todo-completion-2026-09/itunes/TASK-323-external-id-backfill-s-done-setting-is-written-b.md -->
<!-- version: 1.7.0 -->
<!-- guid: 7c0c3872-a44d-44f1-acb1-0c7d122ff9ef -->
<!-- last-edited: 2026-09-10 -->

# TASK-323 — External-ID backfill's "done" setting is written but never read -- the full-library backfill unconditionally reruns on every server boot (SQ-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SQ-04` (audit_schema_queries.json)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Haiku-class · itunes subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SQ-04` (audit_schema_queries.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-323" -b agent/itunes-323-external-id-backfill-s-done-setting-is-w origin/main
cd "$REPO/.worktrees/itunes-323"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Read the `external_id_backfill_v4_done` setting at the top of BackfillExternalIDs (or in the server_lifecycle.go caller) and skip the whole function when set, matching the pattern already used correctly by IsBookAggregatesBackfillDone/MarkBookAggregatesBackfillDone in internal/maintenance/jobs/recompute_book_aggregates.go:127-129. Fix the N+1 GetBookFiles call alongside it per the existing PERF-5 TODO, since it is now confirmed to run on every boot rather than once.

Why it matters: Every server restart re-runs a full-library paginated scan (GetAllBooksCore in pages of 10,000) with a known, code-commented N+1 (`files, fErr := store.GetBookFiles(book.ID)` per book, backfill.go:99, tagged `TODO(PERF-5): replace with a batch GetBookFilesByBookIDs call to eliminate the N+1 file read`), followed by a second full-library pass (BackfillITunesTrackPIDs, loading every book into two in-memory maps) plus a full streaming parse of the iTunes XML library -- unconditionally, forever, on every boot, not just the first. CreateExternalIDMapping keys are deterministic (`ext_id:<source>:<externalID>`, pebble_store_externalids.go:28) so repeated runs do not create duplicate rows, but the wasted full-library I/O and CPU on every restart is real and grows with library size.

## Background (verify before editing)

- BackfillExternalIDs's doc comment (backfill.go:29-31) claims "This is idempotent -- it checks the setting 'external_id_backfill_done' and only runs once", and the code at line 56-58 says `// Check if backfill has already been performed... Note: in actual usage, would need to check via store.GetSetting` -- but no such check exists anywhere in the function body; it proceeds straight into the paginated GetAllBooksCore loop unconditionally. The setting is only ever WRITTEN, at line 145 (`store.SetSetting("external_id_backfill_v4_done", "true", "bool", false)`), after success. A repo-wide grep for `external_id_backfill_v4_done` / `external_id_backfill_done` across internal/**/*.go returns exactly one hit: that same write line -- nothing reads it. The caller chain confirms no gate exists either: server_lifecycle.go:856-874 labels this "the one-time, idempotent... backfill" while its own adjacent comment admits "this is the path that actually runs at every boot" (line 867), and server/external_id_backfill.go:45-46 claims "The domain package handles idempotency checks" -- which it does not.
- Anchor: `internal/itunes/backfill.go:56` (audit `SQ-04`, confidence high, severity high).
- Related tracking: PERF-5 TODO already filed in-code for the GetBookFiles N+1 half of this; the missing done-flag gate itself is not tracked anywhere found in TODO.md/todo.d.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/itunes/backfill.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '23,64p' internal/itunes/backfill.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '139,151p' internal/itunes/backfill.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '861,873p' internal/itunes/backfill.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/itunes/backfill.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_itunes_323.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_itunes_323.md`.

## Commit message

```
fix(itunes): External-ID backfill's "done" setting is written but never read -- the (SQ-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
