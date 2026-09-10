<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-318-publisheddecades-filter-list-is-built-from-only.md -->
<!-- version: 1.7.0 -->
<!-- guid: f392263f-f421-4f97-ae8c-23e0db862601 -->
<!-- last-edited: 2026-09-10 -->

# TASK-318 — publishedDecades filter list is built from only the first 5,000 books in ULID/creation order, permanently omitting decades from the rest of the library (SQ-05)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SQ-05` (audit_schema_queries.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SQ-05` (audit_schema_queries.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-318" -b agent/server-handlers-318-publisheddecades-filter-list-is-built-fr origin/main
cd "$REPO/.worktrees/server-handlers-318"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either scan for the distinct published-year value set the same index-only way GetDistinctLanguages does (cheap, since it only needs the numeric year field, not full Book/BookCore rows), or at minimum warn/flag in the response when the 5,000-row bound was hit, mirroring the activity store's `exhausted` bool + log-on-truncation pattern (pebble_activity_store.go).

Why it matters: This feeds the ABS `/libraries/:id/filterdata` published-decades filter dropdown -- an interactive client-facing endpoint. A user filtering by decade will see a dropdown that is missing valid values for any decade not represented in the oldest 5,000 scanned books, and there is no way for a client to discover the omission (no truncation flag is returned, unlike the activity store's scan-budget pattern which logs+documents the same class of bound). Contrast with the sibling GetDistinctLanguages on the same endpoint (browse.go:1941), which does a full unbounded index-only Pebble scan (pebble_store.go:3570+) and is therefore complete.

## Background (verify before editing)

- filterDataScanLimit = 5000 (browse.go:1959) bounds `h.library.GetAllBooksCore(filterDataScanLimit, 0)` in publishedDecades (browse.go:1968-1995), i.e. always offset 0 -- the SAME first 5,000 rows every call, never a sample of the rest. GetAllBooksCore's memdb path is explicitly documented as unsorted, iterating "in key (ULID) order" (internal/database/memdb_reads.go:655-656), which is creation-time order. On a large library (this codebase's own docs describe 68K+ books) this permanently restricts the decade list to whatever was scanned/imported first, silently omitting any decade that first appears only in book #5,001+.
- Anchor: `internal/server/handlers/abs/browse.go:1959` (audit `SQ-05`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/server/handlers/abs/browse.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '1953,2001p' internal/server/handlers/abs/browse.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '649,662p' internal/database/memdb_reads.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/server/handlers/abs/browse.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_318.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_318.md`.

## Commit message

```
fix(server-handlers): publishedDecades filter list is built from only the first 5,000 books  (SQ-05)

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
