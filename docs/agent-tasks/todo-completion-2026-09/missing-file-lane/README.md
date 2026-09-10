<!-- file: docs/agent-tasks/todo-completion-2026-09/missing-file-lane/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: cbe9bf6c-a04e-43ee-9265-e2f7560496f2 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — missing-file-lane (todo-completion-2026-09)

15 tasks: 15 carried forward from the 2026-08-21 package (ids kept), 0 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-095](TASK-095-instrument-sort-by-usage-to-inform-the-enabled-s.md) | carried | hygiene | P2 | S | Instrument sort_by usage to inform the enabled_sort_indexes decision | Re-verified at HEAD 42d187168, identical to the 09-02 grep results: `grep -rn 'slog.*sort_ |
| [TASK-096](TASK-096-require-every-mutating-operation-to-declare-and-.md) | carried | data-loss | P1 | L | Require every mutating operation to declare and enforce dry_run support at the r | Re-verified at HEAD: `grep -c 'DryRun' internal/operations/registry/types.go` = 0 (Operati |
| [TASK-098](TASK-098-echo-which-filters-the-server-actually-applied-i.md) | carried | hygiene | P2 | S | Echo which filters the server actually applied in the /audiobooks list response | Re-verified at HEAD, identical to 09-02: `grep -n 'applied_filters' internal/server/handle |
| [TASK-102](TASK-102-typescript-6-0-3-7-0-2-migration-the-one-remaini.md) | carried | hygiene | P2 | L | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the frontend-fram | web/package.json:62 still reads "typescript": "^6.0.3". All 6 peer packages confirmed at t |
| [TASK-103](TASK-103-build-a-report-only-op-categorizing-the-transcri.md) | carried | hygiene | P2 | M | Build a report-only op categorizing the transcribe_status vs IntroTranscription  | internal/plugins/maintenance/intro_transcribe.go: IntroTranscription is actively reference |
| [TASK-106](TASK-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp.md) | carried | correctness | P2 | L | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving  | internal/scanner/scanner.go:2202 func parseM3UFile still exists and is still grouping-only |
| [TASK-108](TASK-108-add-the-review-rating-half-of-app-to-server-read.md) | carried | correctness | P2 | M | Add the review/rating half of app-to-server reading-state sync (reading status h | internal/server/handlers/abs/progress.go: IsFinished still has 7 hits (L58, 292, 305, 316, |
| [TASK-109](TASK-109-parse-deluge-torrent-release-names-into-structur.md) | carried | data-loss | P1 | L | Parse Deluge torrent release names into structured candidate metadata (author/se | internal/deluge/discovery.go:334 func ParseTorrentNameCandidates still returns only normal |
| [TASK-110](TASK-110-audit-book-file-grouping-against-deluge-torrent-.md) | carried | data-loss | P1 | L | Audit book/file grouping against Deluge torrent file-list membership (read-only, | grep -rn 'audit/grouping' internal/deluge/*.go -> 0 hits. internal/plugins/deluge/ contain |
| [TASK-111](TASK-111-build-the-pre-apply-snapshot-tool-for-the-138-pe.md) | carried | hygiene | P2 | M | Build the pre-apply snapshot tool for the 138 pending multidisc holds (TODO.md L | grep -rl 'snapshot' internal/plugins/maintenance/regroup*.go -> 0 hits, confirmed no snaps |
| [TASK-112](TASK-112-build-the-first-aid-orchestrator-frontend-trigge.md) | carried | correctness | P2 | L | Build the First Aid orchestrator + frontend trigger button (dry-run by default,  | grep -rl 'FirstAid/first-aid/first_aid' internal --include='*.go' -> 0 hits, confirmed no  |
| [TASK-113](TASK-113-missing-input-triggering-enqueue-the-producer-op.md) | carried | correctness | P2 | M | Missing-input triggering: enqueue the producer op when a waiting_deps requiremen | grep -n 'ReqOpCompleted/waiting_deps' internal/operations/registry/deps.go internal/operat |
| [TASK-114](TASK-114-never-delete-re-associate-combine-debris-books-i.md) | carried | data-loss | P1 | L | Never delete — re-associate: combine debris books into a template match by durat | grep -rl 'Successors/combine_by_template/CombineByTemplate' internal --include='*.go' -> 0 |
| [TASK-200](TASK-200-build-the-tiered-per-file-intro-transcription-ba.md) | carried | correctness | P2 | L | Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2/3) (TODO. | internal/transcribe/classify.go:356 func ClassifyIntro confirmed present, unchanged. grep  |
| [TASK-201](TASK-201-wire-per-file-intro-classification-into-the-regr.md) | carried | correctness | P2 | M | Wire per-file intro classification into the regroup-shattered-books classifier,  | grep -c ClassifyIntro internal/plugins/maintenance/regroup_shattered_ai.go -> 0, confirmed |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
