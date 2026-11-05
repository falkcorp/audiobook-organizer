<!-- file: docs/agent-tasks/platform-core/F2-15-cutover-suite-harness-restore-drill.md -->
<!-- version: 1.1.4 -->
<!-- guid: d41c117c-6cdf-49d0-b491-b6dd9fd211bf -->
<!-- last-edited: 2026-11-05 -->

# F2-15 — R2 cutover-suite harness in `extraction-gates`: per-family count, ID and reference checks, the byte-identity check outside generated sub-prefix lists, and the restore drill with the pre-freeze release's binary

| Field | Value |
|---|---|
| ID | F2-15 |
| Release / flip | R2, foundation PRs (F2); tests, scripts and CI only |
| Wave | W27 |
| Tier | **T3** (Opus / high): the cross-flip checks every R2 PR runs, the restore drill included; they must pass vacuously at F2 and bite from F3-07, the first build that registers a cutover step |
| Depends on | F2-13, F2-06, F2-14 (this row extends its `production-shaped-store` job) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-15-cutover-suite
git -C "$REPO" fetch origin --tags
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Goal

Write the four gate tests that 09 §9.4 R2 "Verification" runs on every R2 PR through `extraction-gates` (09 §9.2.1: "from
the F3 PR on, the R2 cutover suite"), as a harness that works for **whatever cutover steps the build registers**: zero at
F2 (each test passes with the expected "nothing moved" outcome and says so), M2/M7/M8 from F3-07, one more step per flip
after that. Plus the generator of the shared-family sub-prefix lists, the fetcher of the pre-freeze release's binary, the
**flip-upgrade cell table** that each later flip extends (design rule 9), and the `extraction-gates` job.

| Test (names fixed by 05-storage §5S.3.13 and §5S.10) | Proves |
|---|---|
| `TestCutoverOnR0Corpus` | on F2-12's R0-written corpus with F2-13's overlay, every registered step completes; per-family counts hold (read = imported + quarantined + deleted-by-rule); the quarantine report equals exactly the overlay's quarantine cases (09 §9.3); 14 E1. The overlay holds no unconvertible identity row (F2-13); the blocker corpus is not run here (F4-06's `TestCutoverStopsOnUnconvertibleIdentity` runs it) |
| `TestCutoverPreservesIDsAndReferences` | every user, role, key, import-root, instance and run ID is kept; every reference resolves or names a quarantined ID counted in `orphaned_refs` (14 E2; 09 §9.2.3 "IDs and references") |
| `TestCutoverLeavesOtherFamiliesByteIdentical` | every books key range outside the steps' `Source` and outside 05-storage §5S.9.1's "Rewritten in place by" column is byte-identical before and after (14 E3) |
| `TestRollbackRestoreBootsPreviousBuild` | the restore drill: after an R2 boot, the checkpoint is restored offline and the **pre-freeze release's** binary boots the result with every pre-R2 row intact and gives the corpus' pre-migration answers; a write made after the upgrade is gone; the same through `ROLLBACK_ARCHIVE` from a `legacy-abo/0` archive (05-storage §5S.10; 09 §9.3 "restore drill") |

## Spec to read first

09 §9.2.1 (`extraction-gates` row; the pre-freeze release definition), §9.2.3 ("Shared families", "IDs and references", the
checkpoint and its 7 days), §9.3 item 1 and the restore-drill sentence, §9.4 R2 Verification; 05-storage §5S.3.12 (the
checkpoint, `storage restore-offline --checkpoint` / `--archive`, `make rollback-restore`), §5S.3.13 (`Verify` rules, "The
shared families", "Tests"), §5S.9.1 (the family table, rows 1–9 and 18, and the "Rewritten in place by" column), §5S.10
(`TestRollbackRestoreBootsPreviousBuild`); 14 E1–E3.

## Base state

```bash
python3 -c "import json;m=json.load(open('internal/books/testdata/r2corpus/manifest.json'));print(len(m['stage1'])>0,len(m['overlay'])>0,len(m['blockers'])>0)"   # True True True (F2-12, F2-13), else STOP
grep -rn 'type DataMigration struct' core/storage/datamigrate/   # 1 hit (F2-06), else STOP
git ls-files | grep -E '(^|/)keyfamilies\.go$'                    # 1 path (internal/database/ at f7ac5b11e; internal/books/store/ after R1-26)
grep -c 'production-shaped-store' .github/workflows/extraction-gates.yml  # >=1 (F2-14's job), else STOP
grep -n 'rollback-restore' Makefile | head -3                     # >=1 (R1-27's recipe), else STOP
grep -rn 'restore-offline' core/app/ | head -2                    # >=1 (R1-25's offline restore), else STOP
test ! -e internal/books/cutover_suite_test.go && echo absent     # "absent"
```

## Files

Create: `internal/books/cutover_suite_test.go`, `internal/books/restore_drill_test.go`, `scripts/ci/gen_shared_family_lists.py`,
`internal/books/testdata/shared_families.golden`, `scripts/ci/fetch_prefreeze_binary.sh`. Modify:
`.github/workflows/extraction-gates.yml` (add job `r2-cutover-suite`, name coined here; extend F2-14's
`production-shaped-store` job to run the same suite with `R2_SHAPED_STORE`; keep its name and env names). Create the
generator's test `scripts/tests/test_gen_shared_family_lists.py`.

## Design rules

1. **Step-agnostic.** The suite enumerates the cutover steps the build under test registers (through the books module's
   `Cutover` and the kernel's DAG, the way F2-06's runner sees them); it hard-codes no step name. With zero steps:
   `TestCutoverOnR0Corpus` asserts the corpus boots and no family changed; the others pass with an explicit
   `t.Log("no cutover step registered")`.
2. **Before/after on a copy.** Every test copies the committed corpus to a temp dir; the committed bytes are never opened
   for write.
3. **Per-family checks** use the step reports (`VerifyReport`, counts per outcome) and an independent key scan of source
   and target ranges; never trust the step's own numbers alone.
4. **Byte identity.** Hash every books key range outside (a) the registered steps' `Source` ranges and (b) the families and
   exact keys `shared_families.golden` lists as moved or rewritten in place (05-storage §5S.9.1: M3's expiry flag and merge
   journals, M4's activity families, M6's `rootscan:`), before and after; require equality.
5. **`gen_shared_family_lists.py`** generates `shared_families.golden` from `keyfamilies.go` (the `idx:` and `setting:`
   sub-prefixes and exact keys of §5S.9.1 rows 1–9 and 18) and checks it against a key scan of the committed corpus (the
   census): a prefix in the corpus that the lists neither keep nor move fails the script. CI runs it with `--check` and
   fails on a diff (the golden is generated, never hand-edited).
6. **Pre-freeze binary.** `fetch_prefreeze_binary.sh <tag>` downloads that tag's linux/amd64 release asset into a cache dir
   and prints its path (under 20 lines; CLAUDE.md rule 4). The tag comes from `ci/prefreeze_release.txt` when that file exists
   (F3-08 creates it when it turns the R2 freeze on, before F3-07 merges); until F3-08 records it, the pre-freeze tag
   resolves to the latest published production release, which is what 09 §9.2.1 defines it to be until F3 merges
   (`gh release list --exclude-pre-releases --exclude-drafts --limit 1`; `release-prod.yml` can leave a draft, which
   cannot be fetched). **The file's reader rule** (one rule, shared with F3-07 and F3-08): F3-08 writes a four-line `#`
   header, a `#` comment and one tag line, so the tag is the single line that is neither blank nor starts with `#`:
   `grep -v '^#' ci/prefreeze_release.txt | grep -v '^[[:space:]]*$'`. Never `cat` the file. Give the script a
   `--resolve <file>` mode (name coined here) that prints exactly that line and exits non-zero unless there is exactly
   one; the CI job and F3-07/F3-08's re-verify lines call it. In `restore_drill_test.go` add
   `TestPrefreezeTagResolver` (name coined here): it writes a fixture to `t.TempDir()` in F3-08's exact shape (the four
   header lines, the `#` comment, a blank line, `v0.1.0`) and expects `--resolve` to print `v0.1.0`; a fixture with no tag
   line, and one with two, must fail. The drill test reads the path from `R2_PREFREEZE_BIN` and skips with
   a message when unset locally; in CI it is required.
7. **Restore drill.** Boot the build under test on a corpus copy (which takes the pre-migration checkpoint when any step runs);
   write one marker row after the upgrade; stop; restore the checkpoint offline with the build's `storage restore-offline
   --checkpoint <name>` (or the `make rollback-restore` recipe's underlying steps, the same ones R1-27 tests); boot the
   pre-freeze binary on the result; assert it starts, the marker row is gone, every pre-R2 row is intact, and F2-12's
   answers (effective config, schedules, search, ABS) are unchanged. Repeat through `ROLLBACK_ARCHIVE` with a `legacy-abo/0`
   archive made from the corpus. At F2, if the boot takes no checkpoint (no step runs), the checkpoint half logs that and
   the archive half still runs.
8. **CI job** `r2-cutover-suite`: every PR that touches a non-doc file (the `extraction-gates` trigger), `timeout-minutes: 30`,
   actions pinned to SHAs (copy those the file uses), fetches the pre-freeze binary, runs
   `go test ./internal/books/ -run 'TestCutoverOnR0Corpus|TestCutoverPreservesIDsAndReferences|TestCutoverLeavesOtherFamiliesByteIdentical|TestRollbackRestoreBootsPreviousBuild' -count=1`
   and `python3 scripts/ci/gen_shared_family_lists.py --check`, plus `TestCutoverFlipUpgrade` (rule 9) with
   `R2_PREVFLIP_BIN` set when the table holds a cell. Add the job to `ci/gates.txt` if R1-02's file lists
   `extraction-gates` jobs individually.
9. **Flip-upgrade cells** (03 §3.8 "Each later flip that adds a step raises M7's target levels … and registers a new M8";
   05-storage §5S.3.5, §5S.3.13 "M8: the rewrite", IMPL-13; the mechanism is stated in F3-04 and F3-06: each flip adds
   a level step per raised owner at its next contiguous version and replaces M8). In
   `cutover_suite_test.go` add `TestCutoverFlipUpgrade` (name coined here) over a table (name coined here,
   `flipUpgradeCells`) that this row leaves **empty**; each flip from F3-07 on adds exactly one cell naming the commit its
   previous `main` is built from (for F3-07, the last F2 `main`, which registers no cutover step; from F4-08, the
   previous flip's merge commit). A cell: the previous flip's `main` (binary path in `R2_PREVFLIP_BIN`, env name coined here; the
   job builds it from the cell's commit) migrates a corpus copy; this build then boots it and must (a) raise the
   levels of each owner this flip raises (books; core when core's level changes) to this build's declared `Spec.Level`
   before the flip's first newly registered step runs, through the flip's new level steps, with the previous build's
   level steps still applied and unchanged, (b) run exactly one M8
   (one new `c/storage/dm/` record with this build's M8 ID and exactly one new `KindRewrite` journal), and (c) leave a
   store on which the previous `main`'s binary exits 78 with nothing written. With an empty table the test logs
   "no flip-upgrade cell" and passes; its `selftest/` case drives the cell runner with in-process stand-ins for
   the two builds (the fake step of "TDD" at two levels) to prove each of (a)–(c) fails when broken. Locally the test skips with a message when
   `R2_PREVFLIP_BIN` is unset; in CI it is required whenever the table holds a cell.

## TDD

Write the four tests first against the F2 build (they must pass with zero steps), then a temporary local cutover step in
the test file only (a `t.Helper` test module with one `Cutover` step that moves one overlay family through a fake importer),
proving each test fails when that step drops a row, changes an ID, touches a byte outside its source, or breaks the drill.
Keep that fake step and its negative cases in the test file, behind a subtest name `selftest/` (coined here).

## Acceptance

```bash
python3 scripts/ci/gen_shared_family_lists.py --check                                    # exit 0, no diff
python3 -m pytest scripts/tests/test_gen_shared_family_lists.py -q                        # passed
go test ./internal/books/ -run TestPrefreezeTagResolver -count=1                         # ok (header fixture resolves to its tag)
R2_PREFREEZE_BIN="$(bash scripts/ci/fetch_prefreeze_binary.sh "$(gh release list --exclude-pre-releases --exclude-drafts --limit 1 --json tagName -q '.[0].tagName')")" \
  go test ./internal/books/ -run 'TestCutoverOnR0Corpus|TestCutoverPreservesIDsAndReferences|TestCutoverLeavesOtherFamiliesByteIdentical|TestRollbackRestoreBootsPreviousBuild' -count=1 -v | grep -c '^--- PASS'   # 4
go test ./internal/books/ -run 'TestCutoverOnR0Corpus/selftest|TestCutoverFlipUpgrade' -count=1   # ok (negative cases fail as designed; empty cell table logs and passes)
grep -c 'r2-cutover-suite' .github/workflows/extraction-gates.yml                         # >=1
make ci                                                                                   # exit 0
```

## STOP conditions

- The pre-freeze release has no linux/amd64 asset.
- `keyfamilies.go` and the corpus scan disagree (a prefix in the corpus that no list classifies): report the prefix; do not add
  it to the golden by hand.
- A test can only pass at F2 by hard-coding "no steps": the harness must enumerate steps, not special-case F2.

## Out of scope

Any cutover step (F3-06 onward); `TestCutoverRefusedByOlderBuilds` and `TestDryRunMatchesLiveRun` (F3-04, F3-07);
the owners' oracles (config F3-06, reach F4-07, schedule F5-14, search F7); the production-shaped store generator (F2-14).

## Rollback

Revert the PR; the job and tests disappear.

## Repo rules

File headers on every new file, both workflow edits and the golden (as a leading comment line if the golden format allows,
else a `.golden.header` note in the PR body; the generator writes the header); conventional commit
(`test(r2): cutover-suite harness and restore drill in extraction-gates`) with `Co-Authored-By: Claude <noreply@anthropic.com>`;
`git push` for workflow changes (CLAUDE.md); `gh pr merge <n> --rebase`; `changelog.d/` fragment.
