<!-- file: docs/agent-tasks/todo-completion-2026-09/server/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e97012c-d076-4cc0-8cf9-8f006e063622 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — server (todo-completion-2026-09)

13 tasks: 13 carried forward from the 2026-08-21 package (ids kept), 0 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-129](TASK-129-fix-wipeactivity-dry-run-count-saturating-at-2.md) | carried | correctness | P2 | S | Fix wipeActivity dry-run count saturating at 2 | git log --oneline d2fcef16a..HEAD -- internal/server/maintenance_fixups.go shows exactly 1 |
| [TASK-130](TASK-130-register-searchindexdroppedcount-and-a-dirty-bac.md) | carried | hygiene | P2 | S | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metri | internal/metrics/metrics.go:60 only exports search_index_docs_total (confirmed via metrics |
| [TASK-131](TASK-131-fix-audiobook-organizer-books-total-to-report-th.md) | carried | hygiene | P2 | S | Fix audiobook_organizer_books_total to report the true total, not just primary b | internal/metrics/metrics.go:47 still Name:'books_total'; internal/server/server_lifecycle. |
| [TASK-134](TASK-134-add-a-wiring-level-test-proving-the-server-actua.md) | carried | correctness | P2 | M | Add a wiring-level test proving the server actually constructs CancelOperationV2 | No internal/server/wire_handlers_test.go exists (find = 0 results). grep 'pipelineManager= |
| [TASK-136](TASK-136-convert-reconcile-apply-from-resumedrop-to-real-.md) | carried | correctness | P2 | M | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | internal/server/reconcile_ops.go:51 (reconcile.scan) and :98 (reconcile.apply) both still  |
| [TASK-138](TASK-138-exempt-the-abs-router-group-from-the-global-basi.md) | carried | correctness | P2 | S | Exempt the ABS router group from the global BasicAuth() middleware | internal/server/middleware/basicauth.go:19-42 BasicAuth() exempts only /api/health, /api/v |
| [TASK-140](TASK-140-retire-the-unsafe-cleanup-merged-go-handler-as-a.md) | carried | data-loss | P1 | S | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner decision:  | internal/server/itl_cleanup.go:53 still calls itunesservice.SafeWriteITL(itlPath, *ops) un |
| [TASK-205](TASK-205-replace-testserverstartgracefulshutdown-s-fixed-.md) | carried | perf | P2 | S | Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounded readines | internal/server/server_more_test.go:375 still `time.Sleep(6 * time.Second)` verbatim; no s |
| [TASK-206](TASK-206-split-or-speed-up-the-internal-server-test-packa.md) | carried | perf | P2 | L | Split or speed up the internal/server test package -- migrate call sites to a li | grep 'func newTestServer' internal/server/*.go = 0 hits. internal/server now has 175 *_tes |
| [TASK-208](TASK-208-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | P2 | M | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_error | internal/server/itunes_error_test.go still has 11 NewServer( call sites; internal/server/v |
| [TASK-209](TASK-209-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | P2 | M | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_integ | Current NewServer( counts: itunes_integration_test.go=5 (was 5 sites at 08-21/09-02), inde |
| [TASK-210](TASK-210-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | P2 | L | Migrate internal/server test fixtures to setupTestServerWithStore — server_cover | NewServer( counts at HEAD: server_coverage_phase2_test.go=4, deluge_integration_test.go=7, |
| [TASK-211](TASK-211-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | P2 | L | Migrate internal/server test fixtures to setupTestServerWithStore — cover_histor | All 10 files still hold exactly 1 NewServer( call site each at HEAD, matching the 09-02 ba |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
