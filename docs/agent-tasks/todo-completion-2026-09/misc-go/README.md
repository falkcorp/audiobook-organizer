<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8338487b-0b0a-4176-a527-6ac7a23fe139 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — misc-go (todo-completion-2026-09)

12 tasks: 4 carried forward from the 2026-08-21 package (ids kept), 8 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-083](TASK-083-fix-or-verify-the-4-still-open-go-path-injection.md) | carried | security | P1 | M | Fix or verify the 4 still-open go/path-injection findings | PR #2781 confirmed MERGED, but PR #2781's own body states the outcome explicitly: 'this re |
| [TASK-086](TASK-086-collapse-internal-whitespace-in-util-normalizeau.md) | carried | correctness | P2 | S | Collapse internal whitespace in util.NormalizeAuthor so double-spaced names dedu | internal/util/normalize.go:26 func NormalizeAuthor(s string) string { return strings.ToLow |
| [TASK-186](TASK-186-measure-the-real-double-primary-rate-library-wid.md) | carried | correctness | P2 | M | Measure the real double-primary rate library-wide, then build the demote-extras  | grep -rln 'primaries > 1/MultiPrimary/DoublePrimary/double_primary' --include='*.go' inter |
| [TASK-197](TASK-197-audit-every-registry-runitems-caller-s-custom-la.md) | carried | correctness | P2 | L | Audit every registry.RunItems caller's custom Label closure for the post-fn re-r | grep -rl 'Label:\s*func' internal/plugins / wc -l -> 35 (was 31 at brief-authoring time, 3 |
| [TASK-335](TASK-335-activity-log-reset-feature-reauth-gate.md) | new-todo | data-loss | P1 | M | Activity-log reset feature + reauth gate | TODO.md lines 1045, 1049 |
| [TASK-342](TASK-342-library-scan-killed-by-the-watchdog-while-its-ow.md) | new-todo | data-loss | P1 | M | Library scan killed by the watchdog while its own auto-backup ran | TODO.md lines 2304 |
| [TASK-345](TASK-345-move-database-backups-off-the-database-s-own-fil.md) | new-todo | data-loss | P1 | S | Move database backups off the database's own filesystem | TODO.md lines 3395 |
| [TASK-346](TASK-346-prod-has-chapter-consolidation-threshold-min-0-w.md) | new-todo | data-loss | P1 | M | Prod has `chapter_consolidation_threshold_min = 0`, which disables multi-file gr | TODO.md lines 3589 |
| [TASK-348](TASK-348-the-unknown-author-repair-is-two-populations-and.md) | new-todo | data-loss | P1 | S | The "Unknown Author" repair is two populations, and only one is cheap | TODO.md lines 3966 |
| [TASK-351](TASK-351-repair-the-book-rows-that-were-written-one-per-t.md) | new-todo | data-loss | P1 | M | Repair the book rows that were written one-per-track | TODO.md lines 4388 |
| [TASK-353](TASK-353-sec-backup-abspath.md) | new-todo | security | P1 | S | SEC-BACKUP-ABSPATH | TODO.md lines 4726 |
| [TASK-363](TASK-363-missing-op-now-built-run-pending-no-book-had-sto.md) | new-todo | security | P1 | S | MISSING (op now built, run pending): no book had stored chapters — `maintenance. | TODO.md lines 16927, 16949, 16960, 16966 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
