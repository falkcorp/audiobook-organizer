<!-- file: docs/agent-tasks/platform-core/R1-07-move-lifecycle-and-log.md -->
<!-- version: 1.1.1 -->
<!-- guid: 10fc0787-73a0-42ba-9753-7796bb4c7ace -->
<!-- last-edited: 2026-11-05 -->

# R1-07 — Move internal/lifecycle to core/lifecycle and internal/logging (with internal/config's log_retention.go) to core/log

| Field | Value |
|---|---|
| ID | `R1-07` |
| Tier | **T1** (Sonnet / medium): import-path rewrite of two leaf packages and their importers; every commit must build |
| Release / wave | R1 / W06 |
| Depends on | R1-02 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §8.1 R1, §8.2 (mediacore/lifecycle, mediacore/log rows), §2.2 (mechanical rewrites); 09 §9.2.10 (move window); 09 §9.4 R1 Content: runtime |
| Branch | `agent/platform-core-r1-07-move-lifecycle-and-log` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/lifecycle/`
- `core/log/`
- `internal/lifecycle/`
- `internal/logging/`
- `internal/config/`: `config.go`, `log_retention.go`, `log_retention_test.go`, `persistence.go`
- `internal/aiscan/`: `pipeline.go`, `pipeline_review3_test.go`, `pipeline_review_test.go`
- `internal/dedup/`: `auto_resolve.go`, `book_runtime.go`, `drain_stale.go`, `engine.go`
- `internal/httputil/respond.go`
- `internal/importer/service.go`
- `internal/metabatch/upgrade.go`
- `internal/metafetch/`: `book_runtime.go`, `file_pipeline.go`, `service.go`
- `internal/operations/registry/registry.go`
- `internal/plugins/dedup/`: `build_candidate_index.go`, `check_book.go`, `emb_reencode.go`, `embed_async.go`, `lsh_index_build.go`, `register.go`
- `internal/plugins/maintenance/`: `activity_filter_index_backfill.go`, `cleanup.go`, `compact_activity_log.go`, `deps.go`, `intro_transcribe.go`, `optimize.go`, `recompact_activity_digests.go`
- `internal/scanner/`: `chapter_consolidation.go`, `oversized_dir.go`, `scanner.go`, `shattered_coalesce.go`
- `internal/scheduler/`: `batch_poller_ctx_test.go`, `purge_old_logs_task_test.go`, `scheduler.go`, `tasks.go`
- `internal/server/`: `ai_scan_apply_op.go`, `batch_apply_one.go`, `batch_apply_refusal.go`, `csp_report.go`, `duplicates_helpers.go`, `duplicates_ops.go`, `entities_ops.go`, `library_ai_parse_op.go`, `library_core_ops.go`, `metadata_bulk_fetch_log.go`, `metadata_ops.go`, `series_rename_ops.go`, `server_maintenance_deps.go`, `server_search.go`
- `internal/server/handlers/ai.go`
- `internal/server/handlers/dedup/manual_candidates.go`
- `internal/server/handlers/operations/`: `handler.go`, `pause.go`
- `internal/server/middleware/`: `cfbounce.go`, `log_redact.go`, `trace_redact.go`
- `tools/cmd/sdkguard/internal-deps.txt`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

The 56 importer files above (beside `log_retention.go` and `log_retention_test.go`) import `internal/lifecycle` or
`internal/logging`, or name a retention identifier outside `internal/config`, at the plan's base `f7ac5b11e` (no code changed between it and HEAD). Two more files in `internal/config` (`config.go` at `:2786`
and `persistence.go` at `:1095`) name a retention identifier and change in this PR; both are in the list (step 4).
Any further importer your base's `git grep` finds is your file too.

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-07" -b agent/platform-core-r1-07-move-lifecycle-and-log origin/main
cd "$REPO/.worktrees/platform-core-r1-07"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Move two leaf packages into core, import paths only (04 §8.1 R1, §8.2 rows `mediacore/lifecycle` and
`mediacore/log`, §2.2 "mechanical rewrites"; 09 §9.4 R1 "Content: runtime"; 09 §9.2.10 move window):

| old ==> new | what moves |
|---|---|
| `internal/lifecycle` ==> `github.com/falkcorp/mediacore/lifecycle` (`core/lifecycle/`) | the whole package (`shutdown.go`, `shutdown_test.go`), package name stays `lifecycle` |
| `internal/logging` ==> `github.com/falkcorp/mediacore/log` (`core/log/`) | the whole package (8 files), package name becomes `log` |
| `internal/config` (part) ==> `github.com/falkcorp/mediacore/log` | from `internal/config/log_retention.go` only `DefaultLogRetentionDays`, `LogRetentionKeepForever`, `ResolveLogRetentionDays` and their doc comments (04 §8.6 row `internal/config/log_retention.go`: "only the retention section and its resolver move to `log`") |

What stays in `internal/config/log_retention.go`: `normalizeLogRetentionDays`, `logRetentionZeroMigratedKey`
(an ABO setting key, which `TestCoreHoldsNoAppKeyNames` would reject in core) and `migrateLogRetentionBlob`
(04 §8.6, same row). No behaviour change. Every ABO importer imports the new `log` package under the alias
`logging`, so no call site changes except the three retention names. The alias is the plan's rule for every ABO
file from this PR on: R1-18 and later log work keep `logging "github.com/falkcorp/mediacore/log"`, and nothing
collides with the standard library `log`.

## Dependency check

R1-02 merged: `test -f .github/workflows/extraction-gates.yml && test -f core/go.mod && echo ok` prints `ok`.
Otherwise STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
M=github.com/falkcorp/audiobook-organizer
ls internal/lifecycle                      # expect: shutdown.go shutdown_test.go
ls internal/logging                        # expect 8 files: integration_test.go opcontext.go redact_path.go redact_path_test.go sanitize.go sanitize_test.go structured.go structured_test.go
git grep -l "\"$M/internal/lifecycle\"" -- '*.go' | wc -l    # expect 6 (internal/aiscan 3, internal/operations/registry 1, internal/scheduler 2)
git grep -l "\"$M/internal/logging\"" -- '*.go' | wc -l      # expect 47 (dedup 4, httputil 1, importer 1, metabatch 1, metafetch 3, plugins/dedup 6, plugins/maintenance 6, scanner 4, server 14, server/handlers 1, server/handlers/dedup 1, server/handlers/operations 2, server/middleware 3)
git grep -n 'DefaultLogRetentionDays\|LogRetentionKeepForever\|ResolveLogRetentionDays' -- '*.go' ':!internal/config/log_retention.go' ':!internal/config/log_retention_test.go' | wc -l
#   expect 11: code at internal/config/config.go:2786, internal/config/persistence.go:1095,
#   internal/scheduler/tasks.go:1601, internal/server/server_maintenance_deps.go:516 and the test
#   internal/scheduler/purge_old_logs_task_test.go:37; comments at config.go:1483,1486,1487,3967,
#   internal/scheduler/tasks.go:1600 and internal/plugins/maintenance/deps.go:926
grep -n '"internal/lifecycle"\|"internal/logging"' internal/arch/layering_test.go
#   expect the layerOf keys (85 and 90 at f7ac5b11e) and one table entry (520); R1-01 redesigned the file, so
#   line numbers differ at your base, but each key must still appear
grep -c 'internal/lifecycle' tools/cmd/sdkguard/internal-deps.txt     # expect 1 (see step 7)
grep -c '"internal/lifecycle"\|"internal/logging"' scripts/ci/short_test_weights.json   # expect 0: no weight entry to move
```

The importer counts are the work list. If they differ at your base, use the `git grep -l` output at your base
(a predecessor or a feature PR changed an importer); do not hand-pick.

## Steps (one commit that builds; `check_commits_build.py` builds every commit)

1. Create the directories and move the files with history:

   ```bash
   mkdir -p core/lifecycle core/log
   git mv internal/lifecycle/shutdown.go internal/lifecycle/shutdown_test.go core/lifecycle/
   for f in integration_test.go opcontext.go redact_path.go redact_path_test.go sanitize.go sanitize_test.go structured.go structured_test.go; do
     git mv internal/logging/$f core/log/$f
   done
   sed -i 's/^package logging$/package log/' core/log/*.go
   ```

   Every moved file imports only the standard library (checked at `f7ac5b11e`). Set each moved file's
   `// file:` line to its new path (`core/lifecycle/...`, `core/log/...`), bump its patch version and
   `last-edited`, keep its `guid`.
2. Split the retention names into core: create `core/log/retention.go` (new header, `package log`) holding
   lines 13–48 of `internal/config/log_retention.go` verbatim: the doc comments and declarations of
   `DefaultLogRetentionDays`, `LogRetentionKeepForever` and `ResolveLogRetentionDays`. Delete those lines from
   `internal/config/log_retention.go`. In what remains (`normalizeLogRetentionDays`), write
   `logging.DefaultLogRetentionDays` and `logging.LogRetentionKeepForever`, and add the import
   `logging "github.com/falkcorp/mediacore/log"`.
3. Move the resolver test: cut `TestResolveLogRetentionDays` (lines 21–32 of
   `internal/config/log_retention_test.go`) into a new `core/log/retention_test.go` (`package log`, imports
   `testing`). In the remaining `internal/config/log_retention_test.go`, prefix every
   `DefaultLogRetentionDays`/`LogRetentionKeepForever` with `logging.` and add the same aliased import.
4. Rewrite importers (mechanical, every file of the re-verify lists):

   ```bash
   M=github.com/falkcorp/audiobook-organizer
   git grep -l "\"$M/internal/lifecycle\"" -- '*.go' | xargs sed -i "s#\"$M/internal/lifecycle\"#\"github.com/falkcorp/mediacore/lifecycle\"#"
   git grep -l "\"$M/internal/logging\"" -- '*.go' | xargs sed -i "s#^\(\s*\)\"$M/internal/logging\"#\1logging \"github.com/falkcorp/mediacore/log\"#"
   ```

   Then the retention callers: in `internal/config/config.go:2786`, `internal/config/persistence.go:1095`,
   `internal/scheduler/tasks.go:1601`, `internal/server/server_maintenance_deps.go:516` and
   `internal/scheduler/purge_old_logs_task_test.go:37`, replace `config.ResolveLogRetentionDays`,
   `config.DefaultLogRetentionDays`, `config.LogRetentionKeepForever` (inside `internal/config`: the bare
   names) with `logging.<Name>`, and add `logging "github.com/falkcorp/mediacore/log"` to the file's imports
   where it is not already there. Update the two comments that name `config.ResolveLogRetentionDays`
   (`internal/scheduler/tasks.go:1600`, `internal/plugins/maintenance/deps.go:926`) to `logging.ResolveLogRetentionDays`.
   Run `gofmt -w` on every changed file (import blocks re-sort) and bump each file's header.
5. Root `go.mod`: no edit (the `require` + `replace` of `github.com/falkcorp/mediacore` exist since R1-01).
   Run `go mod tidy` at the root and `go -C core mod tidy`; both must leave `go.mod`/`go.sum` unchanged (core's
   two packages import only the standard library). If either changes, STOP and report.
6. `internal/arch/layering_test.go`: delete the `"internal/lifecycle"` and `"internal/logging"` keys from
   `layerOf` and from any other table in the file that lists them (one entry per key at `f7ac5b11e`: line 520's
   map). Add nothing: imports of `github.com/falkcorp/mediacore/...` are classified by R1-01's mapping (core L0
   → −1). Bump the header.
7. `make sdkguard` reads `tools/cmd/sdkguard/internal-deps.txt`, which lists `internal/lifecycle` in
   `pkg/plugin/sdk`'s closure; after the move it is no longer an ABO path. Run
   `go run ./tools/cmd/sdkguard/main.go -update`, bump that file's `# version:` line, and confirm the only
   diff is the removed `internal/lifecycle` line (the file is in this row's list).
8. Shard weights: `scripts/ci/short_test_weights.json` has no entry for either package (re-verify), so it is
   unchanged; run `python3 scripts/ci/short_test_shards.py --check` and expect exit 0.
9. Core guard control (03 §3.5.1 item 2, "once core has leaves"): this PR creates no in-core import edge
   (`lifecycle` and `log` import only the standard library), so the core guard's control stays as R1-01 left it.
10. `go -C core fix ./...` (04 §2.2: a move PR applies `go fix` to the files it moves); commit what it changes
    in `core/lifecycle` and `core/log` together with the move.
11. Headerless fragment `changelog.d/<today>_core_lifecycle_log.md` (`### Changed`: "Internal: the shutdown
    and logging helper packages moved into the core module."). Commit:
    `refactor(core): move internal/lifecycle to core/lifecycle and internal/logging to core/log`.
    The PR description lists the three `old ==> new` lines of the table above.

## Tests

No new behaviour, so no new test: the moved tests (`go -C core test ./lifecycle/ ./log/`) and every
importer's tests are the proof. If a core guard test at your base (R1-01's `archtest` checks) fails on the
moved test files' `slog.SetDefault` calls (`core/log/integration_test.go:21,24,71,74`,
`core/log/sanitize_test.go:56-57`), STOP and report: the fix (a local logger instead of the default) is a
behaviour decision for the lead, not part of an import-path move.

## Code contract

No exported symbol changes except the three retention names' package (`logging.DefaultLogRetentionDays`, `logging.LogRetentionKeepForever`, `logging.ResolveLogRetentionDays(v int) int` in `github.com/falkcorp/mediacore/log`).

## Acceptance

```bash
M=github.com/falkcorp/audiobook-organizer
test ! -e internal/lifecycle && test ! -e internal/logging && echo MOVED                       # expect MOVED
git grep -n "\"$M/internal/lifecycle\"\|\"$M/internal/logging\"" -- '*.go' | wc -l               # expect 0
git grep -n 'logRetentionZeroMigratedKey\|log_retention_zero_migrated' -- core | wc -l           # expect 0
git grep -n 'func ResolveLogRetentionDays' -- core/log/retention.go | wc -l                       # expect 1
go build ./... && go -C core build ./...                                                          # expect exit 0
go vet ./... && go -C core vet ./...                                                              # expect exit 0
go -C core test ./lifecycle/ ./log/ -count=1                                                      # expect ok ok
go test ./internal/config/ ./internal/scheduler/ ./internal/aiscan/ ./internal/arch/ -short -count=1   # expect ok for each
make sdkguard                                                                                      # expect "SDK guard passed"
make ci && make -C core ci                                                                         # expect exit 0 for both
```

## Out of scope

Any identifier rename beyond the three retention names' package qualifier. The logger cut of 05-logging
(R1-18). `internal/logger` (stays ABO). New core API.

## Rollback

Revert the commit (the moves revert with it). No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
