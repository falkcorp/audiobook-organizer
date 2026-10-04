<!-- file: docs/plans/storage-efficiency/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: dbffee11-8cb8-403f-86a9-d887ac6f38dc -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency, Release A: task briefs

Design: `docs/design/2026-10-03-storage-efficiency-design.md` (v1.0).
Plan: `docs/plans/2026-10-03-storage-efficiency-plan.md`, table "Release A".
Evidence: `.claude/notes/db-optimization-eval-2026-10-03.md` in the primary
checkout (R1-R7, F1-F13).

Every brief was written against `d1f069fac`, and every anchor grep in it was
run on that commit. Each brief is self-contained: an executor reads only its
own brief.

## Briefs

| Task | Brief | Branch | Model | Depends |
|---|---|---|---|---|
| A1 | [TASK-A1.md](TASK-A1.md): Pebble metrics on `/metrics` | `feat/storage-a1-pebble-metrics` | sonnet | none |
| A2 | [TASK-A2.md](TASK-A2.md): key-family registry and `GET /diagnostics/db-census` | `feat/storage-a2-db-census` | opus | none |
| A3 | [TASK-A3.md](TASK-A3.md): db-health and `/cache/stats` read the census; safe prefix bound | `perf/storage-a3-db-health-census` | sonnet | A2 merged |
| A4 | [TASK-A4.md](TASK-A4.md): `storage_format` stamp and `make rollback` guard | `feat/storage-a4-format-stamp` | opus | none |
| A5 | [TASK-A5.md](TASK-A5.md): timeline indexes, reconcile, `GetOpLogsV2` tail read | `perf/storage-a5-timeline-index` | opus | none |
| A6 | [TASK-A6.md](TASK-A6.md): progress-log throttle | `perf/storage-a6-progress-log-throttle` | sonnet | none |
| A7 | [TASK-A7.md](TASK-A7.md): store-open settings from environment, shared cache | `feat/storage-a7-pebble-env-settings` | sonnet | A1, A3, A4 merged |

## Wave order

- **W1, in parallel:** A1, A2, A4, A5, A6. Their file sets are disjoint
  (matrix below). That holds only because of three constraints written into
  the briefs:
  - A1 registers its collector from `internal/server/server.go`, not from
    the ticker in `server_lifecycle.go`, which A5 edits.
  - A5 adds no field to the `PebbleStore` struct (which would mean editing
    `pebble_store.go`, owned by A4). It reads its sentinel with one point
    `Get` per call.
  - A2 registers the families `opv2:open:` and `opv2:done:` in advance, so A5
    never edits `keyfamilies.go`. A2 does not edit
    `docs/database-pebble-schema.md`, which A5 owns.
- **W2:** A3 (needs A2's census).
- **W3:** A7. **Collision found:** the plan put A3 and A7 together in W2, but
  both edit `internal/database/pebble_store.go` (A3: `ScanPrefix`,
  `CountPrefix`, `KeyCount`; A7: `newPebbleStore` options and `Optimize`) and
  `internal/database/ai_scan_store.go` (A3: `HealthStats`; A7: open options
  and `Optimize`). A7 therefore moves to its own wave after A3. It also
  depends on A1 and A4 (plan).

No task in any wave edits `database.Store`, an `iface_*.go` file, or
`internal/database/mocks/`. `make ci` runs `mocks-check`, so two parallel
regenerations of `mocks/mock_store.go` would collide. New store methods are
reached through capability interfaces resolved with `database.AsCapability`.

## Same-file collision matrix, W1

Rows are files. A mark means the task creates or edits the file. Every row has
at most one mark, so the W1 tasks touch disjoint files.

| File | A1 | A2 | A4 | A5 | A6 |
|---|---|---|---|---|---|
| `internal/database/pebble_metrics_export.go` (new) | X | | | | |
| `internal/database/pebble_metrics_export_test.go` (new) | X | | | | |
| `internal/metrics/pebble_collector.go` (new) | X | | | | |
| `internal/metrics/pebble_collector_test.go` (new) | X | | | | |
| `internal/metrics/metrics.go` | X | | | | |
| `internal/openlibrary/store.go` | X | | | | |
| `internal/metafetch/openlibrary.go` | X | | | | |
| `internal/server/server.go` | X | | | | |
| `internal/server/pebble_metrics_sources.go` (new) | X | | | | |
| `internal/database/keyfamilies.go` (new) | | X | | | |
| `internal/database/keyfamilies_test.go` (new) | | X | | | |
| `internal/database/census.go` (new) | | X | | | |
| `internal/database/census_test.go` (new) | | X | | | |
| `internal/database/memdb_census.go` (new) | | X | | | |
| `internal/server/handlers/diagnostics.go` | | X | | | |
| `internal/server/handlers/diagnostics_census_test.go` (new) | | X | | | |
| `internal/server/wire_media_routes.go` | | X | | | |
| `internal/database/storage_format.go` (new) | | | X | | |
| `internal/database/storage_format_test.go` (new) | | | X | | |
| `internal/database/pebble_store.go` | | | X | | |
| `main.go`, `main_test.go` | | | X | | |
| `Makefile`, `Makefile.local.example` | | | X | | |
| `scripts/storage_format_guard.sh` (new) | | | X | | |
| `docs/system/runbooks.md`, `docs/system/deploy-and-gpu-ops.md` | | | X | | |
| `internal/database/pebble_store_ops_v2.go` | | | | X | |
| `internal/database/pebble_store_ops_v2_timeline.go` (new) | | | | X | |
| `internal/database/pebble_store_ops_v2_timeline_test.go` (new) | | | | X | |
| `internal/server/server_lifecycle.go` | | | | X | |
| `docs/database-pebble-schema.md` | | | | X | |
| `internal/operations/registry/reporter_db.go` | | | | | X |
| `internal/operations/registry/export_test.go` | | | | | X |
| `internal/operations/registry/reporter_progress_throttle_test.go` (new) | | | | | X |
| `internal/operations/registry/reporter_progress_shape_internal_test.go` (new) | | | | | X |
| `changelog.d/<date>_storage_a<N>_<slug>.md` (new, distinct slug per task) | X | X | X | X | X |

Later waves: A3 edits `pebble_store.go`, `ai_scan_store.go`,
`handlers/diagnostics.go`, `handlers/cache.go`, `web/src/services/api.ts`,
`web/src/pages/Diagnostics.tsx`, replaces
`handlers/key_counter_capability_test.go` with
`handlers/census_capability_test.go`, and adds new test files. A7 edits `pebble_store.go`,
`ai_scan_store.go`, `openlibrary/store.go`, `plugins/maintenance/db.go`,
`deploy/audiobook-organizer.service`, `docs/configuration.md`, and adds
`internal/database/pebble_settings.go` with its test. Each starts from an `origin/main` that
already holds its predecessors.

## Anchors from the design and plan that did not hold on `d1f069fac`

- Design 1(3), "`pebble_store_ops_v2.go:785`": the `DeleteRange` is at `:789`
  (the function starts at `:736`).
- Eval note F6(a), "`diagnostics.go:741-755`": the `ScanPrefix` decode loop is
  at `:744-753`.
- Plan, A4 "`migrations.go`": not needed. The stamp lives in a new
  `storage_format.go`. `db_version` is stored through `UserPreference`
  (`migration_bookkeeping.go:26`), and A4 copies that shape under its own key.
- Plan, A5 "`handlers/operations_v2.go`": no behaviour change is needed. The
  `scan_capped` and `matched` meanings hold because the store keeps "sort the
  whole window, then truncate".
- Plan, A7 "`plugins/maintenance/db.go`": the only change there is the log
  line that names the parallel setting.
- Design 8, "timeline under 100 ms": holds only for windows that hold a few
  thousand rows. R4's 5.46 s was measured with `since=129600m` (90 days). A
  result sorted by `StartedAt` still reads every in-window row, so A5 states
  the window its target applies to.

## Exit for Release A

- The census numbers are recorded as the before-figures: per-family keys and
  bytes; retired books and files; history entries per book; fingerprints and
  transcripts counted.
- The timeline answers in under 100 ms on prod for `since` up to 24 h.
- db-health answers in under 1 s.
