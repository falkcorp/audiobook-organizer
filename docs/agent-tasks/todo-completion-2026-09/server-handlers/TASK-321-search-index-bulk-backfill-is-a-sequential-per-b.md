<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-321-search-index-bulk-backfill-is-a-sequential-per-b.md -->
<!-- version: 1.7.0 -->
<!-- guid: 4bef8cdb-9afc-4cc8-b54e-5a25c3bc2858 -->
<!-- last-edited: 2026-09-10 -->

# TASK-321 — Search-index bulk backfill is a sequential per-book N+1 (author/series/tags) with no worker pool (SQ-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SQ-01` (audit_schema_queries.json)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SQ-01` (audit_schema_queries.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-321" -b agent/server-handlers-321-search-index-bulk-backfill-is-a-sequenti origin/main
cd "$REPO/.worktrees/server-handlers-321"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Pre-resolve all distinct AuthorID/SeriesID values for the page via GetAuthorsByIDs/GetSeriesByIDs (one call per page of 500, not one per book), and drive the per-book loop through registry.RunItems or an errgroup+SetLimit(runtime.NumCPU()) worker pool per CLAUDE.md's mandated pattern.

Why it matters: This is the exact shape CLAUDE.md's concurrency rule targets (whole-library loop + per-item DB read, no worker pool) and the exact N+1 shape the audit brief targets (batch API exists and is used correctly elsewhere, just not here). It only fires when the search index is genuinely empty (first boot, or after a Bleve mapping-version bump routes around it per server_lifecycle.go:967-983), but at that moment it is a background goroutine doing 3x point-gets x every book in the library (68K+ books observed elsewhere in this codebase's own docs) fully serially -- the same profile as the documented 2026-07-05 dedup.full-scan incident that went silent for 3+ hours at 100% CPU on one core.

## Background (verify before editing)

- buildSearchIndexIfEmpty (server_search.go:44-100) runs `for i := range books { doc := search.BookToDoc(store, &books[i]); s.searchIndex.IndexBook(doc) }` as a plain serial loop, paging GetAllBooksFullFrom at pageSize=500 with no errgroup/worker pool. BookToDoc (internal/search/index_builder.go:82-165) does THREE per-book point reads -- store.GetAuthorByID(*book.AuthorID) (L147), store.GetSeriesByID(*book.SeriesID) (L153), store.GetBookTags(book.ID) (L160) -- instead of resolving author/series once per page via the batch API database.Store.GetAuthorsByIDs (internal/database/pebble_store_authors.go:77), which internal/audiobooks/service_query.go:717 already uses correctly for the equivalent list-enrichment case (`authorsMap, _ = svc.store.GetAuthorsByIDs(authorIDs)`).
- Anchor: `internal/server/server_search.go:63` (audit `SQ-01`, confidence high, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/server/server_search.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '38,106p' internal/server/server_search.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '711,723p' internal/audiobooks/service_query.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '71,83p' internal/database/pebble_store_authors.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '76,88p' internal/search/index_builder.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '159,171p' internal/search/index_builder.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/server/server_search.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_321.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_321.md`.

## Commit message

```
fix(server-handlers): Search-index bulk backfill is a sequential per-book N+1 (author/series (SQ-01)

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
