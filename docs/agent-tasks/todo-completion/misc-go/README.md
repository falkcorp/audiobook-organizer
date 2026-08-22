<!-- file: docs/agent-tasks/todo-completion/misc-go/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: a0637008-5e7e-434c-b936-e5d52c1b309d -->
<!-- last-edited: 2026-08-21 -->

# Workstream — misc-go (todo-completion)

9 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-197 | L697 | Audit every registry.RunItems caller's custom Label closure for the po | P2 | L | Sonnet-class | 2 |
| TASK-186 | VG-DOUBLE-PRIMARY | Measure the real double-primary rate library-wide, then build the demo | P1 | M | Opus-class | 6 |
| TASK-082 | SEC-CODEQL-BACKLOG | Fix the go/zipslip finding on the backup-restore extraction path | P1 | M | Opus-class | 1 |
| TASK-083 | SEC-CODEQL-BACKLOG | Fix or verify the 4 still-open go/path-injection findings (1 of the or | P1 | M | Opus-class | 1 |
| TASK-084 | SEC-CODEQL-BACKLOG | Add CodeQL-specific lgtm suppressions for the 3 already-justified go/d | P2 | S | Sonnet-class | 1 |
| TASK-085 | L3433 | Add search-index metrics (docs total, dirty backlog) to /metrics — the | P2 | M | Sonnet-class | 1 |
| TASK-086 | L3790 | Collapse internal whitespace in util.NormalizeAuthor so double-spaced  | P1 | S | Sonnet-class | 1 |
| TASK-087 | ARCH-8 | Replace serviceregistry.Get[T]'s panicking string-key lookups with typ | P2 | M | Sonnet-class | 1 |
| TASK-088 | L4698 | Route acoustid lsh_backfill's lshIndexChecker lookup through database. | P2 | S | Haiku-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/organizer/... ./internal/plugins/maintenance/... ./internal/reconcile/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/backup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/fileops/... ./internal/metadata/... ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/mtls/... ./tools/cmd/merge-split-books/... ./tools/cmd/reconcile-paths/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... ./internal/plugins/dedup/... ./internal/plugins/maintenance/... ./internal/plugins/metafetch/... -count=1 ; go build ./... && go vet ./... && go test ./internal/serviceregistry/... -count=1 ; go build ./... && go vet ./... && go test ./internal/util/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `internal/audiobooks/service_filtering.go`: TASK-002, TASK-190, TASK-005, TASK-186 → serialize by wave (TASK-002=w4, TASK-190=w2, TASK-005=w1, TASK-186=w6)
- `internal/audiobooks/service_query.go`: TASK-001, TASK-002, TASK-190, TASK-003, TASK-005, TASK-186 → serialize by wave (TASK-001=w3, TASK-002=w4, TASK-190=w2, TASK-003=w5, TASK-005=w1, TASK-186=w6)
- `internal/database/pebble_store.go`: TASK-029, TASK-039, TASK-186 → serialize by wave (TASK-029=w2, TASK-039=w3, TASK-186=w6)
- `internal/metrics/metrics.go`: TASK-085, TASK-203, TASK-130, TASK-131 → serialize by wave (TASK-085=w1, TASK-203=w4, TASK-130=w2, TASK-131=w3)
- `internal/organizer/service.go`: TASK-186, TASK-121, TASK-203 → serialize by wave (TASK-186=w6, TASK-121=w1, TASK-203=w4)
- `internal/plugins/acoustid/backfill.go`: TASK-021, TASK-197 → serialize by wave (TASK-021=w7, TASK-197=w2)
- `internal/plugins/acoustid/lsh_backfill.go`: TASK-197, TASK-088 → serialize by wave (TASK-197=w2, TASK-088=w1)
- `internal/plugins/maintenance/chapters_backfill.go`: TASK-066, TASK-197 → serialize by wave (TASK-066=w1, TASK-197=w2)
- `internal/plugins/maintenance/intro_migrate_single_file.go`: TASK-197, TASK-200 → serialize by wave (TASK-197=w2, TASK-200=w1)
- `internal/plugins/maintenance/intro_transcribe.go`: TASK-197, TASK-200 → serialize by wave (TASK-197=w2, TASK-200=w1)
- `internal/plugins/maintenance/missing_file_audit.go`: TASK-195, TASK-197, TASK-202 → serialize by wave (TASK-195=w1, TASK-197=w2, TASK-202=w4)
- `internal/plugins/maintenance/missing_file_repoint.go`: TASK-067, TASK-197 → serialize by wave (TASK-067=w3, TASK-197=w2)
- `internal/plugins/maintenance/regroup_shattered_ai.go`: TASK-197, TASK-201 → serialize by wave (TASK-197=w2, TASK-201=w3)
- `internal/server/handlers/filesystem.go`: TASK-083, TASK-213 → serialize by wave (TASK-083=w1, TASK-213=w2)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-082, TASK-083, TASK-084, TASK-085, TASK-086, TASK-087, TASK-088 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-197 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 6 | TASK-186 | wave 5 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
