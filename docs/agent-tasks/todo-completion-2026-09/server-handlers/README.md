<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 31c19e91-d1a5-43fb-b02e-6b3960064fa4 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — server-handlers (todo-completion-2026-09)

19 tasks: 9 carried forward from the 2026-08-21 package (ids kept), 10 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-142](TASK-142-expose-unmergeauto-through-an-admin-undo-merge-e.md) | carried | correctness | P2 | M | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke) | grep -rn 'UnmergeAuto' --include=*.go across the whole repo returns only internal/database |
| [TASK-143](TASK-143-n-3-stop-advertising-delete-update-permissions-t.md) | carried | hygiene | P2 | S | N-3: stop advertising Delete/Update permissions the library surface cannot honor | internal/server/handlers/abs/dto.go:302 Delete: true and :305 Update: true remain inside d |
| [TASK-147](TASK-147-align-abs-conformance-fixtures-with-the-oracle-s.md) | carried | hygiene | P2 | M | Align ABS conformance fixtures with the oracle so CompareValues stays green perm | internal/server/handlers/abs/abs_test.go:474 and library_fake_test.go:1349 both still call |
| [TASK-148](TASK-148-re-capture-the-series-abs-fixture-against-a-popu.md) | carried | hygiene | P2 | S | Re-capture the series ABS fixture against a populated library (it currently cont | testdata/abs-fixtures/get_api_libraries_id_series.json still has response.body.results ==  |
| [TASK-149](TASK-149-detect-multi-file-books-whose-synthesized-chapte.md) | carried | correctness | P2 | M | Detect multi-file books whose synthesized chapter timeline stops short of Book.D | git log --oneline d2fcef16a..HEAD -- internal/server/handlers/abs/mapper.go shows 2 commit |
| [TASK-150](TASK-150-audit-apply-shaped-endpoints-for-missing-tag-fil.md) | carried | correctness | P2 | M | Audit apply-shaped endpoints for missing tag/file-I/O writeback | docs/audits/2026-08-21-apply-endpoint-fileio-audit.md still does not exist. All four handl |
| [TASK-154](TASK-154-implement-post-api-session-local-all-batch-local.md) | carried | correctness | P2 | M | Implement POST /api/session/local-all (batch local-session sync, accept both bod | internal/server/handlers/abs/handler.go has only the single-session route at line 602; gre |
| [TASK-157](TASK-157-parallelize-the-per-candidate-synchronous-label-.md) | carried | perf | P2 | M | Parallelize the per-candidate synchronous label/breakdown refresh in DismissDedu | internal/server/handlers/dedup/handler.go:1248 func DismissDedupCluster, sequential `for _ |
| [TASK-214](TASK-214-cap-get-api-v1-audiobooks-metadata-cache-review-.md) | carried | perf | P2 | S | Cap GET /api/v1/audiobooks/metadata/cache/review to a default page size, add all | PARTIALLY CHANGED SINCE 09-02: commit 9ef923ba7 'fix(metadata): the cached listing honours |
| [TASK-306](TASK-306-post-backup-restore-caller-requested-checksum-ve.md) | new-finding | data-loss | P2 | S | POST /backup/restore: caller-requested checksum verification is silently skipped | internal/server/handlers/system/handler.go:697 |
| [TASK-308](TASK-308-sse-handler-unconditionally-overrides-the-app-s.md) | new-finding | security | P3 | S | SSE handler unconditionally overrides the app's restrictive CORS policy with Acc | internal/realtime/events.go:221 |
| [TASK-318](TASK-318-publisheddecades-filter-list-is-built-from-only.md) | new-finding | correctness | P2 | S | publishedDecades filter list is built from only the first 5,000 books in ULID/cr | internal/server/handlers/abs/browse.go:1959 |
| [TASK-319](TASK-319-delete-operations-history-deletes-from-the-dead.md) | new-finding | correctness | P2 | S | DELETE /operations/history deletes from the dead v1 `operation:` keyspace; repor | internal/server/handlers/operations/handler.go:244 |
| [TASK-321](TASK-321-search-index-bulk-backfill-is-a-sequential-per-b.md) | new-finding | perf | P1 | M | Search-index bulk backfill is a sequential per-book N+1 (author/series/tags) wit | internal/server/server_search.go:63 |
| [TASK-328](TASK-328-ipratelimiter-sweeps-the-entire-ip-map-under-one.md) | new-finding | perf | P3 | S | IPRateLimiter sweeps the entire IP map under one mutex on every request | internal/server/middleware/ratelimit.go:47 |
| [TASK-336](TASK-336-get-operations-timeline-silently-ignores-its-que.md) | new-todo | data-loss | P1 | M | `GET /operations/timeline` silently ignores its query filters | TODO.md lines 1192 |
| [TASK-338](TASK-338-terminal-ops-never-get-completed-at-so-they-ling.md) | new-todo | data-loss | P1 | M | Terminal ops never get `completed_at`, so they linger as zombies | TODO.md lines 1366, 1461 |
| [TASK-343](TASK-343-re-calibrate-the-absolute-title-distance-gates-f.md) | new-todo | data-loss | P1 | M | Re-calibrate the absolute title-distance gates for non-Latin scripts | TODO.md lines 2872, 2887, 2901 |
| [TASK-361](TASK-361-2026-06-22-security-sweep-the-items-still-open-a.md) | new-todo | security | P1 | M | 2026-06-22 security-sweep: the items still open after the status pass | TODO.md lines 10906, 10908 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
