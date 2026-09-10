<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-322-no-persisted-author-books-secondary-index-pre-me.md -->
<!-- version: 1.6.0 -->
<!-- guid: b4d27625-ba16-4728-8193-03277e49df68 -->
<!-- last-edited: 2026-09-10 -->

# TASK-322 — No persisted author->books secondary index; pre-memdb-warmup fallback does two full-keyspace scans for a single-author lookup (SQ-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SQ-02` (audit_schema_queries.json)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SQ-02` (audit_schema_queries.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-322" -b agent/database-322-no-persisted-author-books-secondary-inde origin/main
cd "$REPO/.worktrees/database-322"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either build and maintain a real `book:author:<authorID>:<bookID>` (and `book_authors_junction:<authorID>:<bookID>` for co-authors) secondary index on CreateBook/UpdateBook/SetBookAuthors so the pre-memdb path can prefix-scan instead of full-table-scan, or explicitly serve GetBooksByAuthorIDCore from a 503/degraded response during the documented warmup window instead of paying the full-scan cost per request.

Why it matters: This fallback is not a rare cold-start-only edge case: memory/project notes record memdb warmup as consistently ~130s after every restart, a recurring window this project has been bitten by before (`memdb warmup is ASYNC ~130s after every restart`). Any hit to GET /authors/:id/books (an interactive Authors-page endpoint) inside that window costs two full-library unmarshal passes instead of an O(k) prefix scan, and the cost repeats per request (no caching) for the whole warmup window after every restart/deploy.

## Background (verify before editing)

- getBooksByAuthorIDFull (pebble_store.go:2297-2339), the fallback used whenever `!p.UseMemDB || p.mem() == nil` (line 2230-2234, GetBooksByAuthorIDCore), calls bookIDsInAuthorJunction (line 2254-2279) which iterates the ENTIRE `book_authors:` keyspace (every book's junction row) json.Unmarshal-ing each one just to find rows for ONE authorID, then getBooksByAuthorIDFull itself iterates the ENTIRE `book:` keyspace unmarshalling every book row a second time (lines 2304-2336). No `book:author:<authorID>:<bookID>` secondary index exists in the code despite one being documented in docs/database-architecture.md and docs/AI-REFERENCE.md's key schema (`book:author:<author_id>:<ulid> -> ULID (index)`) -- grep for the literal key `book:author:` in pebble_store.go turns up zero writers, only comments describing keys to *skip* during other scans.
- Anchor: `internal/database/pebble_store.go:2254` (audit `SQ-02`, confidence high, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/database/pebble_store.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '2224,2345p' internal/database/pebble_store.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/pebble_store.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_322.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_322.md`.

## Commit message

```
fix(database): No persisted author->books secondary index; pre-memdb-warmup fallback  (SQ-02)

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
