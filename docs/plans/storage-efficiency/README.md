<!-- file: docs/plans/storage-efficiency/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: dbffee11-8cb8-403f-86a9-d887ac6f38dc -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency, Release A: task briefs

Design: `docs/design/2026-10-03-storage-efficiency-design.md` (v1.3).
Plan: `docs/plans/2026-10-03-storage-efficiency-plan.md` (v1.3), table
"Release A".
Evidence: `.claude/notes/db-optimization-eval-2026-10-03.md` in the primary
checkout (R1-R7, F1-F13).

The A1-A7 briefs were written against `d1f069fac` and revised on 2026-10-03
to match plan v1.3; the A4, A8 and A9 anchors were re-run on `543827ef7`
(A8's on `falkcorp/infra-docs` at `5602791`). Each brief is self-contained:
an executor reads only its own brief, and re-runs its anchor greps first.

## Briefs

| Task | Brief | Branch | Model | Depends |
|---|---|---|---|---|
| A1 | [TASK-A1.md](TASK-A1.md): Pebble metrics on `/metrics`, incl. per-level write amplification | `feat/storage-a1-pebble-metrics` | sonnet | #3704 merged (P-1) |
| A2 | [TASK-A2.md](TASK-A2.md): key-family registry and `GET /diagnostics/db-census` | `feat/storage-a2-db-census` | opus | none |
| A3 | [TASK-A3.md](TASK-A3.md): db-health and `/cache/stats` read the census; safe prefix bound | `perf/storage-a3-db-health-census` | sonnet | A2 merged, #3704 merged |
| A4 | [TASK-A4.md](TASK-A4.md): `storage_format` stamp, open/init split, pinned Pebble format, `make rollback` guard | `feat/storage-a4-format-stamp` | opus | #3704 merged |
| A5 | [TASK-A5.md](TASK-A5.md): timeline indexes through `stageOpRow`, stamp-gated reconcile, `GetOpLogsV2` tail read | `perf/storage-a5-timeline-index` | opus | #3704 merged |
| A6 | [TASK-A6.md](TASK-A6.md): progress-log throttle | `perf/storage-a6-progress-log-throttle` | sonnet | none |
| A7 | [TASK-A7.md](TASK-A7.md): store-open settings from environment, shared cache, AI-scan and OpenLibrary format pin, `MemoryMax` | `feat/storage-a7-pebble-env-settings` | sonnet | A1, A3, A4 merged |
| A8 | [TASK-A8.md](TASK-A8.md): rebuild the rehearsal sandbox on a ZFS clone (repo `falkcorp/infra-docs`) | `feat/sandbox-zfs-clone` in `infra-docs` | main session | A2 merged |
| A9 | [TASK-A9.md](TASK-A9.md): `scripts/deploy-cutover.sh` and its Python body | `feat/storage-a9-deploy-cutover` | sonnet | A4 merged, A8 done |

PR #3698 (also in P-1) merged on 2026-10-03; #3704 is the remaining gate.

## Wave order

- **W1:** A2 and A6 now; A1, A4 and A5 as soon as #3704 has merged (it edits
  `pebble_store.go`, `metrics/metrics.go` and `server_lifecycle.go`, which A4,
  A1 and A5 edit). The five W1 file sets are disjoint (matrix below). That
  holds only because of four constraints written into the briefs:
  - A1 registers its collector from `internal/server/server.go`, not from
    the ticker in `server_lifecycle.go`, which A5 edits.
  - A5 adds no field to the `PebbleStore` struct (which would mean editing
    `pebble_store.go`, owned by A4). It reads its sentinel with one point
    `Get` per call. Its ratchet is a Go source-scan test, not a Makefile
    target, because A4 edits the `Makefile`. Its docs note goes into
    `docs/database-pebble-schema.md`, not `docs/system/runbooks.md` (A4).
  - A2 registers the families `opv2:open:` and `opv2:done:` in advance, so A5
    never edits `keyfamilies.go`. A2 does not edit
    `docs/database-pebble-schema.md`, which A5 owns.
  - A4 pins the Pebble format only at the main store and raw diagnostics. The
    AI-scan and OpenLibrary pins are in A7, so A4 never edits
    `internal/openlibrary/store.go` (A1) or `ai_scan_store.go` (A3, A7).
- **W2:** A3 (needs A2's census) and A8 (separate repository).
- **W3:** A7 and A9. A3 and A7 both edit `internal/database/pebble_store.go`
  (A3: `ScanPrefix`, `CountPrefix`, `KeyCount`; A7: `newPebbleStore`
  options and `Optimize`) and `internal/database/ai_scan_store.go` (A3:
  `HealthStats`; A7: open options and `Optimize`), so A7 waits for A3. A9
  needs A4's `--print-storage-format` and sidecar, and A8's sandbox for its
  one real deploy. A7 and A9 share no file.

No task in any wave edits `database.Store`, an `iface_*.go` file,
`internal/database/mocks/` or `.interface-width-baseline`. `make ci` runs
`mocks-check`, so two parallel regenerations of `mocks/mock_store.go` would
collide. New store methods are reached through capability interfaces
resolved with `database.AsCapability`.

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
| `internal/testutil/` (one new test file) | | | X | | |
| `cmd/diagnostics.go`, `cmd/diagnostics_test.go` | | | X | | |
| `main.go`, `main_test.go` | | | X | | |
| `Makefile`, `Makefile.local.example` | | | X | | |
| `scripts/storage_format_guard.py`, `scripts/test_storage_format_guard.py` (new) | | | X | | |
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

## Later waves

- **W2.** A3 edits `pebble_store.go`, `ai_scan_store.go`,
  `handlers/diagnostics.go`, `handlers/cache.go`, `web/src/services/api.ts`,
  `web/src/pages/Diagnostics.tsx`, replaces
  `handlers/key_counter_capability_test.go` with
  `handlers/census_capability_test.go`, and adds new test files. A8 edits
  only `scripts/dedup-sandbox/*` in `falkcorp/infra-docs`. Disjoint.
- **W3.** A7 edits `pebble_store.go`, `ai_scan_store.go`,
  `openlibrary/store.go`, `plugins/maintenance/db.go`,
  `deploy/audiobook-organizer.service`, `docs/configuration.md`, and adds
  `internal/database/pebble_settings.go` with its test. A9 adds
  `scripts/deploy-cutover.sh`, `scripts/deploy_cutover.py` and
  `scripts/test_deploy_cutover.py`, and edits `Makefile.local.example`
  (after A4's edit to it has merged). Disjoint.

Each task starts from an `origin/main` that already holds its predecessors.

## Anchors from the design and plan that did not hold on `d1f069fac`

- Design 1(3), "`pebble_store_ops_v2.go:785`": the `DeleteRange` is at `:789`
  (the function starts at `:736`).
- Eval note F6(a), "`diagnostics.go:741-755`": the `ScanPrefix` decode loop is
  at `:744-753`.
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
- The plan's decision rules run before any release B brief is written: the
  census `book_ver:` bytes are compared with design 4.9 (restate it if the
  total is below 12 GB), and any held or undecodable legacy shape gets a
  named B7 handler or an owner acceptance.
