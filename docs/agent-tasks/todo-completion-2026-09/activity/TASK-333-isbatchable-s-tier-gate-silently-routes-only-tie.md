<!-- file: docs/agent-tasks/todo-completion-2026-09/activity/TASK-333-isbatchable-s-tier-gate-silently-routes-only-tie.md -->
<!-- version: 1.6.0 -->
<!-- guid: 88ddf15e-a84e-478e-846c-9adfeef798bb -->
<!-- last-edited: 2026-09-10 -->

# TASK-333 — isBatchable's Tier gate silently routes only tier=debug high-volume entries through the batcher; a caller that emits the same Type at tier=change (e.g. after the writer.go tier-upgrade rule for warn/error) falls back to one full ActivityEntry per line instead of being coalesced (DA-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DA-04` (audit_dedup_activity.json)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · activity subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `DA-04` (audit_dedup_activity.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/activity-333" -b agent/activity-333-isbatchable-s-tier-gate-silently-routes origin/main
cd "$REPO/.worktrees/activity-333"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either widen isBatchable's gate to route change-tier entries of a batchable Type through the batcher too (dropping the debug-only restriction), or make the restriction and its rationale explicit in a comment so a future reader does not assume warn/error entries of a registered batch type are already coalesced.

Why it matters: This is a narrow, low-blast-radius gap (it only matters for warn/error-level lines from these five Types during a large op), and only degrades to the pre-batching behavior (still durable, just less coalesced and noisier in the channel-full warning path) rather than losing data -- worth a look during any future batcher-scope change but not urgent on its own.

## Background (verify before editing)

- isBatchable (writer.go:176-185) requires `e.Tier == "debug"` in addition to the Type allow-list. sendEntry (writer.go:190-251) derives Tier from parsed.Level BEFORE checking isBatchable: warn/error lines always get tier="change" (writer.go:203-209), so a warn- or error-level log line whose Type happens to match one of the batchable types ("embedded-tag-load", "tag-scan", "metadata-apply", "path-repair", "isbn-enrich") is never batched even during a high-volume run -- it always takes the non-batchable branch and is written/dropped one entry at a time (with the channel-full warning firing on every drop, since parsed.Level != "debug" there).
- Anchor: `internal/activity/writer.go:176` (audit `DA-04`, confidence medium, severity low).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/activity/writer.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '170,215p' internal/activity/writer.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '245,257p' internal/activity/writer.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/activity/writer.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_activity_333.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/activity/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_activity_333.md`.

## Commit message

```
fix(activity): isBatchable's Tier gate silently routes only tier=debug high-volume en (DA-04)

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
