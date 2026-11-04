<!-- file: docs/agent-tasks/platform-core/PC-20-01-s10-flip-final-deletions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9036a2c8-d515-4658-8250-1d49ebfd929e -->
<!-- last-edited: 2026-11-04 -->

# PC-20-01 — S10 flip: delete internal/serviceregistry, pkg/plugin/sdk, --db-type and the SQLite no-op flag

| Field | Value |
|---|---|
| ID | `PC-20-01` |
| Tier | T1 Sonnet/medium — deletions of code with no remaining user; every step is a delete checked by the build |
| Wave | W20 — S10, release `S10` |
| Depends on | `OWNER-22` (current `S9` soak row) |
| Priority / effort | P1 / S |
| Spec | 09 S10 ("Flip PR", deletions, "Not deleted"); 09 §9.6 row 19; 09 §9.2.1 "Soaks", "The trailer", required-checks table |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §1 (S10 release and trailer), §5 rule 19, §7 S10 |
| `Stage-Release:` | `S10 flip` (09 S10: the flip PR is the PR that deletes `internal/serviceregistry`, the first S10 deletion; it sets `shipped_stage:` and `release:` to `S10`) |

## Gate

- This PR changes `release:`, so `check_soak.py` check (a) refuses it until `ci/soak.txt` holds a current `S9` row
  (OWNER-22). Open it and make it green any time; it merges only after that row.
- As a flip PR it also runs `compat-rehearsal` (R-test, 09 §9.10) and `unit-lifecycle` (09 §9.2.1 required-checks
  table; plan §(g) step 6). S10 has no rollback row: rollback is a binary swap (09 §9.2.1; digest §6).
- S10 sets **no** upgrade floor of its own: it removes nothing an older store needs (09 S10; 09 §9.2.2).

## Goal

Delete what has run its window and has no remaining user: the packages `internal/serviceregistry` and
`pkg/plugin/sdk` (`internal/plugin` already went at S7), the CLI flag `--db-type` and the hidden no-op flag
`--enable-sqlite3-i-know-the-risks` (09 §9.6 row 19: "W after S2 … deleted S10"), and set the marker to `S10`.

## Background (cited)

- 09 S10: "Delete after their windows: removed flags `--db-type` and the SQLite no-op flag (`cmd/root.go:489,508`);
  `internal/serviceregistry` and `pkg/plugin/sdk` (`internal/plugin` went at S7); the alias code of every surface
  contract that has run" — the alias code is PC-20-02's. "Not deleted: the legacy mount, the `/api/*` → `/api/v1/*`
  redirect, ABS paths, `/health`, `/metrics`, permanent env names, books' legacy store implementations …, the
  `config.AppConfig` and logger façades."
- At HEAD (9b1545c1a) the flag lines match the spec's 748fd787a anchors: `--db-type` registration at `cmd/root.go:489`,
  the SQLite flag registration block at `:497-516` (constant at `:66`, message at `:70`), `databaseType` at `:37`, the
  `"db-type": "database_type"` map entry at `:58`. HEAD's test `TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning`
  (`cmd/root_test.go:28`) pins the no-op flag and is deleted with it.
- `make ci` runs `sdkguard` (`Makefile:789`), which runs `tools/cmd/sdkguard/main.go` over `pkg/plugin/sdk`; CI's job
  runs `make sdkguard` (`.github/workflows/ci.yml:459` at HEAD). Deleting the SDK deletes its guard.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>
TASK=pc-20-01-s10-flip
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list                       # expected: this path listed, not the primary checkout
npm ci --prefix web                     # or `make web-deps` once PC-01-06 has merged
git rev-parse --short HEAD              # record: the base commit
```

Do not run `go work init` (CLAUDE.md).

## Files

| Action | Path |
|---|---|
| delete | `internal/serviceregistry/` (11 files at HEAD) |
| delete | `pkg/plugin/sdk/` (12 files at HEAD) and the then-empty `pkg/plugin/` |
| modify | `cmd/root.go`, `cmd/root_test.go` |
| modify | `ci/stage.txt` (`shipped_stage: S10`, `release: S10`) |
| delete | `tools/cmd/sdkguard/` (`main.go`, `main_test.go`, `internal-deps.txt`) |
| modify | `Makefile` (the `sdkguard` target, its help line, its `.PHONY` entry and its place in `ci:`) |
| modify | `.github/workflows/ci.yml` (the `make sdkguard` step and the comments that describe it; push with `git push`) |
| modify | `scripts/deadcode-baseline.txt` (the one line naming a deleted package) |
| modify | `docs/configuration.md` (the `--db-type` row) |
| modify | the Go file holding the `database_type` error message that mentions `--db-type` (HEAD: `internal/database/store.go:1841`; after the S2b re-home it is under `internal/books/store/`) |
| create | `changelog.d/<date>_s10_flip.md` |

## Re-verify at your base commit

```bash
M=github.com/falkcorp/audiobook-organizer
# R1 no importer remains outside the packages themselves (HEAD: 46 and 212; the spec's S10 premise is 0).
grep -rl "\"$M/internal/serviceregistry\"" --include='*.go' . | grep -v '^./internal/serviceregistry/' | wc -l   # expected: 0
grep -rl "\"$M/pkg/plugin/sdk\"" --include='*.go' . | grep -v '^./pkg/plugin/sdk/' | grep -v '^./tools/cmd/sdkguard/' | wc -l   # expected: 0
test ! -e internal/plugin && echo GONE                                                            # expected: GONE (deleted at S7, PC-16-06)
# R2 the flags (HEAD values shown).
grep -c 'db-type' cmd/root.go                                                                      # HEAD: 2
grep -c 'removedSQLiteFlag' cmd/root.go                                                            # HEAD: 7
grep -n '^var databaseType string' cmd/root.go                                                     # HEAD: line 37
grep -n 'func TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning' cmd/root_test.go               # HEAD: line 28
grep -rn 'databaseType' --include='*.go' cmd | grep -v '^cmd/root.go' | wc -l                      # expected: 0
# R3 the guard that depends on the SDK.
grep -c 'sdkguard' Makefile                                                                        # HEAD: 7 (lines 86, 124, 545, 546, 548, 789, and the comment at 609)
grep -c 'sdkguard' .github/workflows/ci.yml                                                        # HEAD: 8 (the step at 459 and comments)
# R4 the marker (PC-01-07 created it; S9's flip set it).
grep -E '^(shipped_stage|release): ' ci/stage.txt                                                  # expected: shipped_stage: S9 / release: S9
# R5 shipped artifacts never pass the deleted flags.
grep -rn -e '--db-type' -e 'enable-sqlite3' deploy Dockerfile* docker-compose.yml scripts 2>/dev/null | wc -l   # expected: 0 (HEAD: 0)
```

If R1 is not 0, STOP and report the importers: 09 S10 deletes these packages only once nothing uses them, and this
task does not migrate callers. If R4 shows anything but `S9`, STOP.

## Steps

1. Run the re-verify block; record the base commit and R2/R3 values.
2. `git rm -r internal/serviceregistry pkg/plugin/sdk tools/cmd/sdkguard`; remove `pkg/plugin/` if it is empty.
3. `cmd/root.go`: delete `var databaseType string`; the `"db-type": "database_type",` entry of
   `persistentFlagConfigKeys`; the `StringVar(&databaseType, "db-type", …)` line; the `removedSQLiteFlag` and
   `removedSQLiteFlagMessage` constants with their comments; the registration block that adds, deprecates and hides
   `removedSQLiteFlag`. Bump the header.
4. `cmd/root_test.go`: delete `TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning` and any helper only it used. Bump
   the header.
5. `Makefile`: remove `sdkguard` from `.PHONY` and from `ci:`, delete the target and its help line, and reword the
   comment near HEAD's line 609 that cites it. `ci.yml`: delete the step that runs `make sdkguard` and reword the comment
   blocks so they no longer describe a deleted gate. Bump both
   headers. `scripts/deadcode-baseline.txt`: drop the line naming a deleted package.
6. Drop the `(or drop --db-type %s)` clause (and its argument) from the `database_type` error message; update
   `docs/configuration.md`'s flag table.
7. `ci/stage.txt`: set `shipped_stage: S10` and `release: S10` (both lines, one PR, 09 §9.2.1).
8. `go build ./... && go vet ./...`, then "Acceptance".
9. PR description: the release note "`--db-type` and `--enable-sqlite3-i-know-the-risks` are removed; a command line
   that still passes either now fails with `unknown flag`; remove it." (09 §9.5 asks the build's notes to list removed
   surfaces; these flags are surfaces of 09 §9.6 row 19).

## Code contract

No new code. After this PR: `rootCmd.PersistentFlags().Lookup("db-type") == nil` and
`rootCmd.PersistentFlags().Lookup("enable-sqlite3-i-know-the-risks") == nil`.

## Tests to write first (TDD)

- In `cmd/root_test.go`, replace the deleted test with `TestRemovedFlagsAreGone` (name coined here): both lookups
  above return nil, and `rootCmd.SetArgs([]string{"serve", "--db-type", "pebble"})` followed by `Execute()` returns an
  error containing `unknown flag`. It fails before step 3 and passes after.

## Acceptance (commands and expected output)

```bash
test ! -e internal/serviceregistry && test ! -e pkg/plugin && test ! -e tools/cmd/sdkguard && echo DELETED   # expected: DELETED
grep -rn -e 'db-type' -e 'enable-sqlite3' -e 'removedSQLiteFlag' cmd/ --include='*.go' | grep -v 'TestRemovedFlagsAreGone' | wc -l   # expected: 0
grep -c 'sdkguard' Makefile .github/workflows/ci.yml                                         # expected: Makefile:0 and ci.yml:0
grep -E '^(shipped_stage|release): ' ci/stage.txt                                            # expected: shipped_stage: S10 / release: S10
go build ./... && go vet ./... && echo BUILD-OK                                              # expected: BUILD-OK ("build passes with the packages deleted")
go test ./cmd/... -run '^TestRemovedFlagsAreGone$' -count=1 -v 2>&1 | grep -c -- '--- PASS'   # expected: 1
make ci; echo "make ci exit=$?"                                                               # expected: make ci exit=0
```

## Out of scope

- The alias code of the contracts (PC-20-02) and anything 09 S10 lists under "Not deleted".
- `internal/config/removed_keys.go`'s message for the removed `enable_sqlite` **config key**: a config surface, not the
  CLI flag.
- `config.AppConfig` and the logger façades (optional ratchets).

## Rollback

Before a production tag contains it: a revert PR, which `check_soak.py` admits with no soak row (an exact inverse diff,
09 §9.8 revert row). After release: `make rollback` (binary swap; S10 changes no stored state and sets no floor).

## Repo rules

- **File headers:** bump every changed file's header; keep guids; `changelog.d/` fragment without header.
- **Commits:** conventional (`refactor(s10): …`), trailer `Stage-Release: S10 flip` on **every** commit of the PR,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`. Every commit builds (rebase merges).
- **PR:** required checks green on an up-to-date branch (`extraction-gates`, `docker-image`, `vet-darwin`,
  `vet-integration`, `compat-rehearsal`, `unit-lifecycle`); `gh pr merge <n> --rebase`; never squash, never `--admin`
  past a required check. Workflow changes go by `git push`, never the MCP contents API.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.
- Concurrency (CLAUDE.md): no loop is added.
- No hostnames, IPs, paths or secrets in commits and PRs.

## Planner notes

- **Files missing from the inventory row:** `cmd/root_test.go` (pins the no-op flag), `tools/cmd/sdkguard/`, `Makefile`
  and `.github/workflows/ci.yml` (`make ci` and CI run `sdkguard` over `pkg/plugin/sdk`), `scripts/deadcode-baseline.txt`,
  `docs/configuration.md`, and the `database_type` error message that names `--db-type`.
- **Importers:** HEAD has 46 importers of `internal/serviceregistry` and 212 of `pkg/plugin/sdk`. 09 S10 assumes none are
  left by then, but no inventory row is charged with removing all of them; the brief STOPs if any remain.
