<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: d1112fe2-8659-4591-81b6-b47537cdf23d -->
<!-- last-edited: 2026-08-21 -->

# Workstream — missing-file-lane (todo-completion)

31 tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-089 | L5494 | Log a warning when GetAllSeriesBookCounts() itself errors in LibrarySe | P2 | S | Haiku-class | 1 |
| TASK-090 | L5722 | Give Change Log row 'Compare snapshot' keyboard/a11y affordance | P2 | S | Sonnet-class | 1 |
| TASK-091 | L5736 | Remove dead expanded state in TagComparison | P2 | S | Haiku-class | 1 |
| TASK-092 | L5742 | Delete the unreachable Bulk Fetch Metadata dialog and its handler | P2 | M | Sonnet-class | 1 |
| TASK-093 | L5758 | Audit remaining setupMockApi startsWith() catch-alls for shadowed spec | P2 | S | Haiku-class | 1 |
| TASK-094 | L6252 | Restore version-group count and current-version marker on Book Detail | P2 | S | Sonnet-class | 1 |
| TASK-198 | L6394 | Diagnose and fix scan-import-organize.spec.ts's 7 stuck-on-'Add Import | P2 | M | Sonnet-class | 2 |
| TASK-095 | L6701 | Instrument sort_by usage to inform the enabled_sort_indexes decision | P2 | S | Haiku-class | 2 |
| TASK-096 | L7435 | Require every mutating operation to declare and enforce dry_run suppor | P1 | L | Opus-class | 2 |
| TASK-097 | TODO-MUI-3 | Remove the now-redundant react-is override from web/package.json | P2 | S | Haiku-class | 1 |
| TASK-098 | L7736 | Echo which filters the server actually applied in the /audiobooks list | P2 | S | Sonnet-class | 3 |
| TASK-199 | L7819 | Render Library sub-nav items (In Progress/Finished) in collapsed-sideb | P2 | M | Sonnet-class | 1 |
| TASK-099 | L8044 | Fail/warn CI when the RC ordinal for a version hits 10 | P2 | S | Sonnet-class | 1 |
| TASK-100 | L8177 | Validate the two unvalidated client-side navigation sinks (Login.tsx f | P2 | S | Sonnet-class | 1 |
| TASK-101 | L8245 | Pin a regression test: the regroup recommender must not default to dup | P2 | S | Sonnet-class | 1 |
| TASK-102 | L8273 | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the fro | P2 | L | Opus-class | 2 |
| TASK-200 | L8316 | Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2 | P1 | L | Opus-class | 1 |
| TASK-201 | L8316 | Wire per-file intro classification into the regroup-shattered-books cl | P1 | M | Opus-class | 3 |
| TASK-202 | L8316 | Wire per-file intro classification into First Aid as a tier-2 signal b | P1 | M | Opus-class | 4 |
| TASK-103 | L8433 | Build a report-only op categorizing the transcribe_status vs IntroTran | P2 | M | Sonnet-class | 1 |
| TASK-104 | L8551 | Build the version-group acoustic audit op (tier 2 of First Aid) | P1 | L | Opus-class | 1 |
| TASK-105 | L8611 | Build chapters backfill from a near-exact-acoustic-match duplicate (or | P1 | L | Opus-class | 2 |
| TASK-106 | L8646 | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan,  | P2 | L | Opus-class | 1 |
| TASK-107 | L8646 | Export a playlist back to .m3u | P2 | S | Sonnet-class | 1 |
| TASK-108 | L8675 | Add the review/rating half of app-to-server reading-state sync (readin | P2 | M | Sonnet-class | 1 |
| TASK-109 | L8707 | Parse Deluge torrent release names into structured candidate metadata  | P2 | L | Opus-class | 1 |
| TASK-110 | L8738 | Audit book/file grouping against Deluge torrent file-list membership ( | P2 | L | Opus-class | 2 |
| TASK-111 | L8837 | Build the pre-apply snapshot tool for the 138 pending multidisc holds | P1 | M | Opus-class | 1 |
| TASK-112 | L8890 | Build the First Aid orchestrator + frontend trigger button (dry-run by | P1 | L | Opus-class | 1 |
| TASK-113 | L8890 | Missing-input triggering: enqueue the producer op when a waiting_deps  | P2 | M | Opus-class | 1 |
| TASK-114 | L8943 | Never delete — re-associate: combine debris books into a template matc | P1 | L | Opus-class | 1 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; go build ./... && go vet ./... && go test ./internal/deluge/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/scheduler/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/playlist/... ./internal/scanner/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/transcribe/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/audiobooks/... -count=1 ; npm --prefix web run lint && npm --prefix web test
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

- `.github/workflows/prerelease.yml`: TASK-191, TASK-099 → serialize by wave (TASK-191=w2, TASK-099=w1)
- `internal/operations/registry/registry.go`: TASK-096, TASK-115 → serialize by wave (TASK-096=w2, TASK-115=w1)
- `internal/plugins/maintenance/intro_migrate_single_file.go`: TASK-197, TASK-200 → serialize by wave (TASK-197=w2, TASK-200=w1)
- `internal/plugins/maintenance/intro_transcribe.go`: TASK-197, TASK-200 → serialize by wave (TASK-197=w2, TASK-200=w1)
- `internal/plugins/maintenance/missing_file_audit.go`: TASK-195, TASK-197, TASK-202 → serialize by wave (TASK-195=w1, TASK-197=w2, TASK-202=w4)
- `internal/plugins/maintenance/regroup_shattered_ai.go`: TASK-197, TASK-201 → serialize by wave (TASK-197=w2, TASK-201=w3)
- `internal/plugins/maintenance/regroup_shattered_ai_test.go`: TASK-101, TASK-201 → serialize by wave (TASK-101=w1, TASK-201=w3)
- `internal/scanner/scanner.go`: TASK-021, TASK-181, TASK-106 → serialize by wave (TASK-021=w7, TASK-181=w2, TASK-106=w1)
- `internal/server/batch_apply_op.go`: TASK-096, TASK-135 → serialize by wave (TASK-096=w2, TASK-135=w1)
- `internal/server/duplicates_ops.go`: TASK-043, TASK-096 → serialize by wave (TASK-043=w1, TASK-096=w2)
- `internal/server/handlers/abs/browse.go`: TASK-089, TASK-144, TASK-212 → serialize by wave (TASK-089=w1, TASK-144=w2, TASK-212=w3)
- `internal/server/handlers/abs/library_fake_test.go`: TASK-089, TASK-147 → serialize by wave (TASK-089=w1, TASK-147=w3)
- `internal/server/handlers/audiobooks/handler.go`: TASK-005, TASK-037, TASK-095, TASK-098 → serialize by wave (TASK-005=w1, TASK-037=w6, TASK-095=w2, TASK-098=w3)
- `internal/server/reconcile_ops.go`: TASK-096, TASK-136 → serialize by wave (TASK-096=w2, TASK-136=w1)
- `web/package-lock.json`: TASK-097, TASK-102 → serialize by wave (TASK-097=w1, TASK-102=w2)
- `web/package.json`: TASK-097, TASK-102 → serialize by wave (TASK-097=w1, TASK-102=w2)
- `web/src/components/bookdetail/BookDetailVersionGroup.tsx`: TASK-094, TASK-169 → serialize by wave (TASK-094=w1, TASK-169=w6)
- `web/src/pages/BookDetail.tsx`: TASK-037, TASK-100, TASK-165 → serialize by wave (TASK-037=w6, TASK-100=w1, TASK-165=w8)
- `web/src/pages/Library.tsx`: TASK-092, TASK-161, TASK-164, TASK-165, TASK-166, TASK-167, TASK-168, TASK-169 → serialize by wave (TASK-092=w1, TASK-161=w2, TASK-164=w7, TASK-165=w8, TASK-166=w3, TASK-167=w4, TASK-168=w5, TASK-169=w6)
- `web/src/pages/Settings.tsx`: TASK-198, TASK-158 → serialize by wave (TASK-198=w2, TASK-158=w1)

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
| 1 | TASK-089, TASK-090, TASK-091, TASK-092, TASK-093, TASK-094, TASK-097, TASK-199, TASK-099, TASK-100, TASK-101, TASK-200, TASK-103, TASK-104, TASK-106, TASK-107, TASK-108, TASK-109, TASK-111, TASK-112, TASK-113, TASK-114 | none | disjoint files within the wave (computed collision matrix) |
| 2 | TASK-198, TASK-095, TASK-096, TASK-102, TASK-105, TASK-110 | wave 1 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 3 | TASK-098, TASK-201 | wave 2 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |
| 4 | TASK-202 | wave 3 merged + siblings rebased | disjoint files within the wave (computed collision matrix) |

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-2026-08-21.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
