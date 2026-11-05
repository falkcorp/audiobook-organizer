<!-- file: docs/agent-tasks/platform-core/R1-08-merge-library-leaves.md -->
<!-- version: 1.1.1 -->
<!-- guid: d3c5d254-34a3-4d4e-94c4-d6566a7c0634 -->
<!-- last-edited: 2026-11-05 -->

# R1-08 — Merge the library leaves into core/library: safepath, pathvalidation, libroot, boundedcall, diskstats, util/path.go and util/perms.go

| Field | Value |
|---|---|
| ID | `R1-08` |
| Tier | **T1** (Sonnet / medium): six leaves into one package with identifier renames; still mechanical |
| Release / wave | R1 / W10 |
| Depends on | R1-07 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §8.1 R1, §8.2 (mediacore/library row); 05-library §5L.10; 09 §9.2.10 |
| Branch | `agent/platform-core-r1-08-merge-library-leaves` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/library/`
- `internal/security/safepath/`
- `internal/security/pathvalidation/`
- `internal/libroot/`
- `internal/boundedcall/`
- `internal/diskstats/`
- `internal/util/`: `path.go`, `perms.go`, `path_test.go`, `util_coverage_test.go`
- `internal/maintenance/jobs/`: `backfill_file_hashes.go`, `backfill_file_hashes_missing_test.go`, `bulk_deluge_import.go`, `relink_missing_to_itunes.go`
- `internal/backup/backup.go`
- `internal/covers/covers.go`
- `internal/deluge/import.go`
- `internal/fileops/`: `safe_operations.go`, `service.go`
- `internal/reconcile/reconcile.go`
- `internal/server/`: `covers.go`, `openlibrary_service.go`, `diskstats.go`, `itl_paths.go`
- `internal/audiobooks/service_single.go`
- `internal/organizer/collision.go`
- `internal/server/handlers/audiobooks/`: `handler.go`, `handler_files.go`
- `internal/server/handlers/`: `filesystem.go`, `itunes.go`
- `internal/server/handlers/system/handler.go`
- `internal/plugins/maintenance/`: `file_provenance_export.go`, `consolidation_leftovers_fixer.go`, `report_path.go`, `reports_prune.go`, `duration_backfill.go`, `retired_versions_fixer.go`, `tag_backfill.go`
- `internal/repairs/guards.go`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-08" -b agent/platform-core-r1-08-merge-library-leaves origin/main
cd "$REPO/.worktrees/platform-core-r1-08"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Merge six path-safety leaves into one core package, `github.com/falkcorp/mediacore/library` (`core/library/`),
by moving their files verbatim and rewriting importers (04 §8.1 R1: "`library` (path-safety leaves)";
04 §8.2 row `mediacore/library`; 05-library §5L.10 names where each part ends up at F7; 09 §9.2.10 move window).

| old ==> new | files |
|---|---|
| `internal/security/safepath` ==> `mediacore/library` | `safepath.go`, `safepath_test.go` |
| `internal/security/pathvalidation` ==> `mediacore/library` | `pathvalidation.go`, `pathvalidation_test.go` (external test package) |
| `internal/libroot` ==> `mediacore/library` | `libroot.go`, `libroot_test.go`, `export_test.go`, `owner_unix.go`, `owner_other.go` |
| `internal/boundedcall` ==> `mediacore/library` | `boundedcall.go`, `boundedcall_test.go` |
| `internal/diskstats` ==> `mediacore/library` | `diskstats_unix.go`, `diskstats_windows.go` |
| `internal/util` (part) ==> `mediacore/library` | `path.go` (`SafeJoin`, `WithinRoot`), `perms.go` (`DirMode`, `FileMode`, `SecretFileMode`), `path_test.go`, and `TestCoverage_PermConstants` from `util_coverage_test.go` |

`internal/util` keeps every other file. No behaviour change; the merged package needs no identifier rename:
the top-level names of the six sources are disjoint at `f7ac5b11e` (checked: `Join`, `Validate`, `SafePath`;
`SecureJoin`, `SecureJoinResolved`, `CleanAbsolutePath`, `SanitizeFilename`, `IsDangerousRoot`, `Err*`;
`Mounted`, `ReportPathNotify`, `EnsureDirWritable`, `OwnedByCurrentUser`, `ReportDirName`; `Call`,
`ErrTimeout`; `Stats`; `SafeJoin`, `WithinRoot`, `DirMode`, `FileMode`, `SecretFileMode`; no unexported or
test-name clash). If `go -C core build ./library/` reports "redeclared in this block", STOP and report the
name: a rename is then a lead decision.

## Dependency check

R1-07 merged: `test -d core/lifecycle && test -d core/log && echo ok` prints `ok`. Otherwise STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -l -E "\"$M/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)\"" -- '*.go' | wc -l
#   expect 28 files (safepath 8, pathvalidation 10 incl. its own external test, libroot 4, boundedcall 4, diskstats 2;
#   internal/backup/backup.go imports two of them)
git grep -l -E '\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b' -- '*.go' | wc -l
#   expect 6 files: internal/libroot/libroot.go, internal/libroot/libroot_test.go and 4 under internal/maintenance/jobs
git grep -l -E "\"$M/internal/security/safepath\"" -- internal/fileops | wc -l
#   expect 2: internal/fileops/safe_operations.go and service.go import safepath (both in the row)
grep -n -E '"internal/(boundedcall|diskstats|libroot|security/pathvalidation|security/safepath)"' internal/arch/layering_test.go | wc -l
#   expect 5 layerOf keys (plus any table entry R1-01 kept for them)
grep -c -E 'internal/(security|libroot|boundedcall|diskstats)' scripts/ci/short_test_weights.json tools/cmd/sdkguard/internal-deps.txt
#   expect 0 and 0: no weight entry, not in the SDK closure
```

The union of the two `git grep -l` lists (32 files at `f7ac5b11e`, of which 4 are files that move) is the work
list. If it differs at your base, use the output at your base.

## Steps (one commit that builds)

1. Move the files with history:

   ```bash
   mkdir -p core/library
   git mv internal/security/safepath/safepath.go internal/security/safepath/safepath_test.go core/library/
   git mv internal/security/pathvalidation/pathvalidation.go internal/security/pathvalidation/pathvalidation_test.go core/library/
   git mv internal/libroot/libroot.go internal/libroot/libroot_test.go internal/libroot/export_test.go internal/libroot/owner_unix.go internal/libroot/owner_other.go core/library/
   git mv internal/boundedcall/boundedcall.go internal/boundedcall/boundedcall_test.go core/library/
   git mv internal/diskstats/diskstats_unix.go internal/diskstats/diskstats_windows.go core/library/
   git mv internal/util/path.go internal/util/perms.go internal/util/path_test.go core/library/
   sed -i -E 's/^package (safepath|pathvalidation|libroot|boundedcall|diskstats|util)$/package library/' core/library/*.go
   sed -i 's/^package pathvalidation_test$/package library_test/' core/library/pathvalidation_test.go
   ```

   If `core/library/` would receive two files of the same name, STOP (none at `f7ac5b11e`).
2. Inside `core/library/`: in `libroot.go` and `libroot_test.go` drop the qualifier `util.` (now the same
   package) and the import of `internal/util`; in `pathvalidation_test.go` replace the import of
   `internal/security/pathvalidation` with `"github.com/falkcorp/mediacore/library"` and every `pathvalidation.`
   qualifier (28 at `f7ac5b11e`) with `library.`.
3. Move `TestCoverage_PermConstants` (the "Coverage for perms.go" block, `internal/util/util_coverage_test.go`
   lines 65–77 at `f7ac5b11e`) into a new `core/library/perms_test.go` (`package library`, imports `testing`).
4. Rewrite every importer in the work list (outside `core/library/`) with this script, then `gofmt -w` them:

   ```bash
   python3 - $(git grep -l -E '"github.com/falkcorp/audiobook-organizer/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)"|\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b' -- '*.go' ':!core/**') <<'PY'
   import re, sys
   OLD = r'"github.com/falkcorp/audiobook-organizer/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)"'
   NEW = '"github.com/falkcorp/mediacore/library"'
   for f in sys.argv[1:]:
       s = open(f).read()
       s = re.sub(r'(?<![\w.])(safepath|pathvalidation|libroot|boundedcall|diskstats)\.(?=[A-Z])', 'library.', s)
       s = re.sub(r'(?<![\w.])util\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b', r'library.\1', s)
       s = re.sub(r'^\s*' + OLD + r'\s*\n', '', s, flags=re.M)
       if not re.search(r'(?<![\w.])util\.[A-Z]', s):
           s = re.sub(r'^\s*"github.com/falkcorp/audiobook-organizer/internal/util"\s*\n', '', s, flags=re.M)
       if NEW not in s:
           s = re.sub(r'^import \(\n', 'import (\n\t' + NEW + '\n', s, count=1, flags=re.M)
       open(f, 'w').write(s)
   PY
   gofmt -l . | grep -v '^web/' ; gofmt -w $(git diff --name-only -- '*.go')
   ```

   The first substitution also updates comments that name `libroot.Mounted` and similar; that is intended.
   If a file has a single-line `import "x"` form, the script cannot add the import: `go build` then reports
   `undefined: library` for that file; add the import by hand there.
5. Remove the now-empty directories `internal/security/safepath`, `internal/security/pathvalidation`,
   `internal/libroot`, `internal/boundedcall`, `internal/diskstats` (git drops them once empty;
   `internal/security/` keeps its other packages).
6. `internal/arch/layering_test.go`: delete the five `layerOf` keys and any other table entry naming them;
   add nothing (core imports are classified by R1-01's mapping). `"internal/util"` keeps its entry.
7. `go mod tidy` and `go -C core mod tidy`: expect no change (all moved files import only the standard
   library). If either changes, STOP.
8. Core guard control (03 §3.5.1 item 2): `core/library` imports no other core package, so it creates no
   in-core edge; leave the control as it is at your base.
9. `go -C core fix ./library/`; keep its changes in the same commit. Header bump on every moved and edited file
   (moved files: new `file:` path, patch +1, `guid` kept). Shards: `python3 scripts/ci/short_test_shards.py --check`
   must exit 0 (no weight entry names these packages).
10. Headerless fragment `changelog.d/<today>_core_library_leaves.md` (`### Changed`: "Internal: the path-safety
    helpers moved into the core module's library package."). Commit:
    `refactor(core): merge the path-safety leaves into core/library`. The PR description lists the six
    `old ==> new` lines above.

## Tests

The moved tests (`go -C core test ./library/`) and every importer's tests. No new test.

## Code contract

No exported symbol is renamed; every name keeps its signature, now qualified `library.` (`github.com/falkcorp/mediacore/library`).

## Acceptance

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -n -E "\"$M/internal/(security/safepath|security/pathvalidation|libroot|boundedcall|diskstats)\"" -- '*.go' | wc -l   # expect 0
git grep -n -E '\butil\.(SafeJoin|WithinRoot|DirMode|FileMode|SecretFileMode)\b' -- '*.go' | wc -l                              # expect 0
test ! -e internal/util/path.go && test ! -e internal/util/perms.go && echo SPLIT                                            # expect SPLIT
go build ./... && go -C core build ./...                                    # expect exit 0
GOOS=windows go -C core build ./library/ && GOOS=darwin CGO_ENABLED=0 go -C core vet ./library/   # expect exit 0 (diskstats and owner twins)
go -C core test ./library/ -count=1                                         # expect ok
go test ./internal/util/ ./internal/backup/ ./internal/maintenance/jobs/ ./internal/plugins/maintenance/ ./internal/arch/ -short -count=1   # expect ok for each
make ci && make -C core ci                                                  # expect exit 0 for both
```

## Out of scope

`internal/pathutil`, `internal/fileops`, `internal/watcher` (they move at F7, 04 §8.2); `library.Root`,
sentinels, `RootFS` (05-library, F7). Renaming the merged names.

## Rollback

Revert the commit. No stored data changes.

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
