<!-- file: docs/agent-tasks/platform-core/R3-01-r3-dead-package-deletions.md -->
<!-- version: 1.1.2 -->
<!-- guid: c637a7a0-9253-4a38-bf4b-34bc36514047 -->
<!-- last-edited: 2026-11-05 -->

# R3-01 — R3 deletions: serviceregistry, pkg/plugin/sdk with sdkguard, the no-op --db-type and SQLite flags, bookfiles, writeback; internal/models merged into internal/dedup/unified

| Field | Value |
|---|---|
| ID | R3-01 |
| Release | R3 (cleanup and the upgrade floor) |
| Tier | **T2** (Sonnet / high): deletions of packages with no caller left, plus one small type merge; multi-file but fully specified |
| Wave | W46 (beside R3-03; file-disjoint: this row owns `internal/books/services.go` in W44, R3-03 owns `internal/books/module.go`) |
| Depends on | OWNER-05 (R2 released and deployed), R3-05 (books' `ServiceDefs` list emptied, so `internal/serviceregistry` has no registrant left) |
| Spec | 09 §9.4 R3 "Content"; 04 §3.6 (the delete rows), §5 (`internal/models`), §6 (`pkg/plugin/sdk`, `tools/cmd/sdkguard`); 03 §3.8 R3; 06 §6.11.1 steps 3–6, §6.11.3 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. R3 work starts only after the R2 release is
cut and deployed (OWNER-05, 09 §9.3 "Order"). If the tracking issue does not show OWNER-05 recorded, STOP.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-r3-01-deletions" -b agent/platform-core-r3-01-deletions origin/main
cd "$REPO/.worktrees/pc-r3-01-deletions"
git rebase origin/main
```

## Goal

Delete what R3's content list names and nothing else (09 §9.4 R3; 04 §3.6, §5, §6):

1. `internal/serviceregistry` (04 §3.6: the process-global factory map; 06 §6.11.1 step 4 says "when the list is
   empty, delete `internal/serviceregistry` … in R3 at the latest"). R1-34 replaced the `init()` registrations with
   one explicit `ServiceDefs()` list (step 3), the flips F3-07, F5-18, F7-09 and F8-04 deleted their generic services'
   entries, and R3-05 emptied the rest, so the only caller left is books' empty list in `internal/books/services.go`.
2. `pkg/plugin/sdk` (04 §6: deleted, N4) and `tools/cmd/sdkguard`, the ratchet that guarded it (04 §6
   "Consequences": `archtest` replaces it), with its `make sdkguard` target and its CI step.
3. The no-op `--db-type` flag and the removed SQLite flag `--enable-sqlite3-i-know-the-risks`, both in
   `cmd/root.go` (09 §9.4 R3 cites `cmd/root.go:489,508` at the evidence commit).
4. `internal/bookfiles` and `internal/writeback`: dead, no importer (04 §3.6).
5. `internal/models` merged away (04 §5): the dedup-score family (`Signal`, `SignalKind`, `UnifiedDedupScore`, the
   provenance helpers) becomes the definitions in `internal/dedup/unified` (which today re-exports them as aliases);
   `Audiobook` moves into `internal/itunes`, its only user; every other type in `internal/models` has zero users
   and is deleted.

The precondition is "no caller left": every earlier flip removed its callers. This brief verifies that first and
stops if it is false.

## Files

| Action | Path |
|---|---|
| delete | `internal/serviceregistry/` |
| delete | `pkg/plugin/sdk/` |
| delete | `tools/cmd/sdkguard/` |
| delete | `internal/bookfiles/` |
| delete | `internal/writeback/` |
| delete | `internal/models/` (after the merge) |
| modify | `internal/dedup/unified/` (aliases become definitions; header bump) |
| modify | `internal/itunes/` (`Audiobook` type added; `import.go` re-pointed) |
| modify | `internal/plugins/dedup/` (re-point `models.` to `unified.`) |
| modify | `internal/books/services.go` (drop the empty `ServiceDefs` list and the `NewContainerFrom` call; whatever books still builds is a constructor since R3-05) |
| modify | `internal/config/register.go` (any `serviceregistry` remnant removed, or the file deleted if nothing else remains in it) |
| modify | `cmd/root.go` (the two flags) |
| modify | `Makefile` (the `sdkguard` target, its `.PHONY` entry, its help line and its place in `ci:`) |
| modify | `internal/arch/layering_test.go` (drop the `layerOf` keys and any allowance naming a deleted package) |
| modify | `scripts/ci/short_test_weights.json` (only if it names a deleted package) |
| modify | `.github/workflows/ci.yml` (the `SDK internal-dependency ratchet` step that runs `make sdkguard`) |
| modify | `cmd/root_test.go` (the test of the removed SQLite flag; add `TestRemovedFlagsUnknown`), `cmd/commands_test.go`, `cmd/child_mode.go`, `cmd/child_mode_test.go` (only where they reference the removed flags, the `databaseType` CLI variable or a SQLite path the flags selected; leave `config.AppConfig.DatabaseType` and the `database_type` key alone, and if a file needs no edit at your base, leave it and say so in the PR), `docs/configuration.md` (the `--db-type` row) |
| modify | the embedding store and its tests, which import `internal/models` for `ScoreBreakdown`: `internal/books/store/embedding_store.go`, `internal/books/store/embedding_store_manual_test.go`, `internal/books/store/embedding_store_provenance_test.go`, `internal/books/store/candidate_score_batch_test.go`, and `internal/dedup/rescore_writeback_test.go` (paths after R1-26's rename of `internal/database`) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

Counts in brackets are what the evidence checkout (HEAD `f7ac5b11e`, spec anchor `748fd787a`) shows; at your base
the earlier flips have removed callers, so the **expected** value is the one stated first.

```bash
M=github.com/falkcorp/audiobook-organizer

# 1. serviceregistry importers outside the package. Expected: only internal/books/services.go (its empty list),
#    and internal/config/register.go if a remnant is left there. [evidence checkout: 46 files in 27 packages;
#    R1-34, the flips and R3-05 removed the rest]
grep -rl --include='*.go' "\"$M/internal/serviceregistry\"" . | grep -v '^./internal/serviceregistry/'

# 2. pkg/plugin/sdk importers outside the package. Expected: only tools/cmd/sdkguard files (or 0).
#    [evidence checkout: 212 files]
grep -rl --include='*.go' "\"$M/pkg/plugin/sdk\"" . | grep -v '^./pkg/plugin/sdk/'

# 3. bookfiles and writeback importers. Expected: 0 lines. [evidence checkout: 0]
grep -rl --include='*.go' -e "\"$M/internal/bookfiles\"" -e "\"$M/internal/writeback\"" . \
  | grep -v -e '^./internal/bookfiles/' -e '^./internal/writeback/'

# 4. The two flags. Expected: 1 line each. [evidence checkout: cmd/root.go:489 and :508]
grep -n '"db-type", "pebble"' cmd/root.go
grep -n 'PersistentFlags().Bool(removedSQLiteFlag' cmd/root.go

# 5. internal/models importers. Expected: files under internal/dedup/unified, internal/itunes,
#    internal/plugins/dedup and the embedding store's package only.
#    [evidence checkout: 11 files: internal/database/embedding_store.go + 3 tests (internal/books/store/ after R1-26),
#     internal/itunes/import.go,
#     internal/dedup/unified/score.go, internal/dedup/rescore_writeback_test.go, internal/plugins/dedup x4]
grep -rl --include='*.go' "\"$M/internal/models\"" . | grep -v '^./internal/models/'

# 6. sdkguard wiring. Expected: Makefile lines for the target, .PHONY, help and ci:, and one ci.yml step.
grep -n 'sdkguard' Makefile .github/workflows/ci.yml

# 7. layerOf entries. Expected: one key per deleted package that still has one.
grep -n -e '"internal/serviceregistry"' -e '"pkg/plugin/sdk"' -e '"internal/bookfiles"' \
  -e '"internal/writeback"' -e '"internal/models"' -e '"tools/cmd/sdkguard"' internal/arch/layering_test.go
```

**STOP and report** (do not convert callers yourself) if:

- grep 1 lists any file other than `internal/books/services.go` and `internal/config/register.go`, or
  `internal/books/services.go` still lists a `ServiceDef`. Converting live registrants is R3-05's work (06 §6.11.1
  step 4), not a deletion.
- grep 2 lists any file outside `tools/cmd/sdkguard/`. `PageBooks` and `Progress` must already have moved
  (04 §6: to `internal/books/contract` and a `tasks` helper).
- grep 3 lists any file.
- grep 4 finds neither flag (already removed: run the acceptance block instead) or finds them somewhere other than
  `cmd/root.go`.
- grep 5 lists a package not named above.

## Steps

1. Run the re-verify block; record each result in the PR description.
2. **serviceregistry.** In `internal/books/services.go` delete the empty `ServiceDefs` list and its
   `NewContainerFrom` call (books' remaining services are constructors since R3-05). In `internal/config/register.go`
   remove any `serviceregistry` remnant and the import; if the file then holds only the accessor documented as wrapping `serviceregistry.Get`, delete that
   accessor too after checking (`grep -rn '<accessor name>' --include='*.go' .`) that nothing calls it; delete the
   file if it is empty. Then `git rm -r internal/serviceregistry`.
3. **pkg/plugin/sdk and sdkguard.** `git rm -r pkg/plugin/sdk tools/cmd/sdkguard`. In `Makefile` delete the
   `sdkguard:` target and its `## sdkguard:` comment line, the word `sdkguard` from the `.PHONY` list, the
   `make sdkguard` help line, and the word `sdkguard` from the `ci:` prerequisites. In `.github/workflows/ci.yml`
   delete the step named `SDK internal-dependency ratchet` and the comment block directly above it; reword the
   comment that says "`make ci` gained bench-check alongside sdkguard" so it no longer names sdkguard (keep its
   meaning: the step runs the same make target a contributor runs).
4. **Flags.** In `cmd/root.go`:
   - delete the `"db-type": "database_type",` entry of `persistentFlagConfigKeys`;
   - delete the `rootCmd.PersistentFlags().StringVar(&databaseType, "db-type", ...)` line, and the
     `var databaseType string` declaration if `grep -n 'databaseType' cmd/*.go` then finds no other use;
   - delete the `removedSQLiteFlag` and `removedSQLiteFlagMessage` constants with their comments, and the block
     that registers, deprecates and hides the flag.
   Do **not** touch `config.AppConfig.DatabaseType` or the `database_type` config key: they are read elsewhere
   (backup file naming, child mode) and are out of scope.
   In `cmd/root_test.go` delete the test of the removed SQLite flag (the test that looks up `removedSQLiteFlag`).
   In `cmd/commands_test.go`, `cmd/child_mode.go` and `cmd/child_mode_test.go` change only what the flag removal
   breaks (`go vet ./cmd/...` tells you); at the plan's base they touch `config.AppConfig.DatabaseType`, which stays.
   In `docs/configuration.md` delete the `--db-type` table row (header bump).
5. **bookfiles, writeback.** `git rm -r internal/bookfiles internal/writeback`.
6. **internal/models** (04 §5):
   - Move the dedup-score family from `internal/models/dedup_score.go` into `internal/dedup/unified` as
     definitions: in `internal/dedup/unified/score.go` replace each alias (`type SignalKind = models.SignalKind`,
     `type Signal = models.Signal`, `type UnifiedDedupScore = models.UnifiedDedupScore`, and each constant
     defined as `models.SignalKind…`) with the real definition, and move the provenance helpers
     (`MergeProvenance`, `CarryProvenance`, the `IsProvenanceOnly` method and any other exported function in
     `dedup_score.go`) into a new file `internal/dedup/unified/provenance.go`. Keep every exported name that the
     aliases exposed. If a `models` name collides with an existing `unified` name, STOP and report.
   - Move `Audiobook` (`internal/models/audiobook.go`) into `internal/itunes` (new file
     `internal/itunes/audiobook.go`), with its test, and change `import.go` to use it unqualified.
   - Re-point every remaining importer from grep 5 (`models.X` → `unified.X`), including the embedding store's
     file and its tests, and `internal/plugins/dedup`.
   - Delete `internal/models` once `grep -rl "\"$M/internal/models\"" --include='*.go' .` returns nothing. The other
     types in it (`Author`, `Series`, the request/response DTOs, `FileSystemItem`, `BrowseRequest`,
     `ExclusionRequest`, `SystemStatus`, `LogEntry`) have zero users (04 §5); confirm each with one grep before
     deleting it with the package.
   - Serialized field names (`json:"sb"`, `json:"score_breakdown"`) must not change: the stored dedup rows decode
     the same bytes afterwards.
7. **Layering and shards.** Delete the `layerOf` key and any pair allowance that names a deleted package in
   `internal/arch/layering_test.go`; if `internal/dedup/unified`'s computed layer changes, update its key with the
   value the test prints. Remove deleted packages from `scripts/ci/short_test_weights.json` if present.
8. `go build ./... && (cd core && go build ./...)`, `go vet ./cmd/... ./internal/dedup/... ./internal/itunes/...
   ./internal/plugins/dedup/... ./internal/arch/...`, then the acceptance block.
9. Bump the file header (version, `last-edited`) of every modified file; new files get a fresh header.
10. Add a `changelog.d/` fragment.

## Code contract

No new API. After the merge, `internal/dedup/unified` exports (as definitions, same names and method sets as the
aliases exposed at your base): `SignalKind` with its constants, `Signal`, `UnifiedDedupScore` (with
`IsProvenanceOnly`), `MergeProvenance`, `CarryProvenance`. `internal/itunes` exports `Audiobook` with the fields
`internal/models.Audiobook` had. The removed flags no longer parse: `serve --db-type x` and
`serve --enable-sqlite3-i-know-the-risks` fail with cobra's "unknown flag" (D10: no compatibility shim).

## Tests (first)

Write or move these before the deletions, then make them pass:

1. `TestUnifiedDedupScoreJSONRoundTrip` (name coined here) in `internal/dedup/unified`: marshal a fully populated
   `UnifiedDedupScore` at your base (before the move, through the alias), commit the bytes as a golden string in
   the test, and assert that after the move the same bytes unmarshal and re-marshal identically. This proves the
   stored `ScoreBreakdown` rows still decode.
2. Move `internal/models/audiobook_test.go` to `internal/itunes/audiobook_test.go` (package `itunes`), unchanged in
   its assertions.
3. `TestRemovedFlagsUnknown` (name coined here) in `cmd/root_test.go`: `rootCmd.PersistentFlags().Lookup("db-type")`
   and `Lookup("enable-sqlite3-i-know-the-risks")` are both `nil`.

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# Deleted directories are gone. Expected output: nothing
ls -d internal/serviceregistry pkg/plugin/sdk tools/cmd/sdkguard internal/bookfiles internal/writeback internal/models 2>/dev/null

# No importer of any deleted package. Expected output: 0
grep -rl --include='*.go' -e "\"$M/internal/serviceregistry\"" -e "\"$M/pkg/plugin/sdk\"" \
  -e "\"$M/internal/bookfiles\"" -e "\"$M/internal/writeback\"" -e "\"$M/internal/models\"" . | wc -l

# Flags gone. Expected output: 0
grep -c -e '"db-type"' -e 'removedSQLiteFlag' cmd/root.go

# sdkguard gone from the gates. Expected output: 0
grep -c 'sdkguard' Makefile .github/workflows/ci.yml | awk -F: '{s+=$2} END {print s}'

# Both modules build. Expected: exit 0, no output
go build ./... && (cd core && go build ./...)

# Changed packages. Expected: ok lines, no FAIL
go test -count=1 ./cmd/... ./internal/dedup/... ./internal/itunes/... ./internal/plugins/dedup/... ./internal/config/... ./internal/arch/... ./internal/books/...

# The workflow parses. Expected output: ok
python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('ok')"

# Full local gate. Expected last line: ✅ All CI checks passed!
make ci
```

## Out of scope

- `config.AppConfig.DatabaseType`, the `database_type` key and its readers.
- `tools/cmd/oplint` (it becomes `archtest/tasklint`, 04 §6; not this row).
- R3-02's deletions (legacy steps, cutover, opsgen, `ops.ts`, `internal/operations/state`) and R3-03's start hook.
- Converting any live `serviceregistry` registrant or `pkg/plugin/sdk` caller (STOP instead).
- The R3 release note (R3-04 writes it, the removed flags included).

## Rollback

Revert the commit. Nothing stored changes: the dedup rows keep their bytes (test 1), and the flags are CLI only.
`make rollback` to R2 is unaffected.

## Repo rules

- Worktree off fresh `origin/main`; never edit the primary checkout.
- Every modified file's header bumped; new files get the 4-line header with a fresh guid.
- Conventional commit, for example `refactor(cleanup): delete serviceregistry, plugin sdk, dead packages and no-op flags (R3)`,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr create`, `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check. No release or
  flip trailer (09 §9.2.1).
- The PR description lists the deleted package paths (09 §9.2.10: move/delete PRs list their paths for held
  branches).
- CLAUDE.md "Fix it right": if a STOP condition fires, report it with its real size; do not shrink the task to fit.

