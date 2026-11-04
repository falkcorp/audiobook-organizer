<!-- file: docs/agent-tasks/platform-core/PC-01-07-soak-marker-trailer-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 36ce2444-c28a-4051-b521-893bd442d62a -->
<!-- last-edited: 2026-11-04 -->

# PC-01-07 — Soak, marker and trailer gate: `ci/stage.txt`, `check_soak.py`, `soak_record.py`, `commit-msg` hook, `.gitmessage`, PR template and sweep stamp

| Field | Value |
|---|---|
| ID | PC-01-07 |
| Tier | **T3 (Opus/high)**: the program's main release interlock; the grammar, the ordering (with `UI0` off the chain and the H releases), the bot table, merge commits and the exact-revert recomputation have many edge cases, and a gate that passes wrongly lets unsoaked content auto-promote |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-04 (`extraction-gates.yml` exists; this PR adds its `check_soak` job) |
| Priority / effort | P1 / L |
| Branch | `ci/pc-01-07-soak-gate` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §4.6, §6.7 |
| Owning spec | `09-abo-on-core.md` §9.2.1 "One flip PR per stage release", "Soaks and owner checkpoints", "The trailer", "The soak ledger", the `check_soak.py` refusals (a)–(d), the bot table, the revert rule; S0 "Gates" and "Done when"; §9.10 rows `test_check_soak.py`/`test_soak_record.py`; `03-architecture.md` §3.2 item 6, §3.5.1 item 4 |

**Owner boundary.** This PR creates `ci/stage.txt` and the tooling only. It does **not** create `ci/soak.txt`,
`ci/soak_health_allow.txt` or `ci/prev_ref.txt` (owner bookkeeping, OWNER-02); `check_soak.py` treats a missing ledger as
empty and a missing allow-list as empty. No agent may run `scripts/soak_record.py --release`, edit those three files, or
merge a PR that changes them (09 §9.2.1).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" show origin/main:.github/workflows/extraction-gates.yml | grep -c 'timeout-minutes'   # >= 1: PC-01-04 merged; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-07" -b ci/pc-01-07-soak-gate origin/main
cd "$REPO/../audiobook-organizer-pc-01-07"
npm ci --prefix web    # or make web-deps if PC-01-06 has merged
```

## Exact files

Create: `ci/stage.txt`, `scripts/ci/check_soak.py`, `scripts/tests/test_check_soak.py`, `scripts/soak_record.py`,
`scripts/tests/test_soak_record.py`, `.gitmessage`, `scripts/git-hooks/commit-msg`.
Modify: `scripts/setup-git-hooks.sh`, `.github/PULL_REQUEST_TEMPLATE.md`, `.gitignore`,
`.github/workflows/ratchet-lower.yml`, `.github/workflows/extraction-gates.yml`,
`.claude/skills/parallel-sweep-impl/SKILL.md`, and instead of the absent `docs/superpowers/specs/parallel-sweep.md`
(see Planner notes) `.claude/skills/parallel-sweep-impl/references/child-prompt.md` and
`.claude/commands/parallel-sweep.md`; one `changelog.d/` fragment.

## Re-verify (expected at `ac4368bdb` plus PC-01-01..04)

```bash
test -e ci/stage.txt && echo EXISTS || echo absent                                   # absent
ls ci/soak.txt ci/soak_health_allow.txt ci/prev_ref.txt 2>/dev/null | wc -l          # 0
grep -n 'git commit -m "chore(ci): lower ratchet baselines"' .github/workflows/ratchet-lower.yml | wc -l   # 1 (~:119)
grep -n 'HOOK_FILE="$HOOK_DIR/pre-commit"' scripts/setup-git-hooks.sh | wc -l      # 1 (installs into --git-common-dir)
grep -n '^\.soak/' .gitignore | wc -l                                                # 0
git cat-file -t 1c2244869                                                            # commit (HEAD's grouped Dependabot commit, a test fixture)
git show --name-only --format= 1c2244869 | sort                                      # Dockerfile, Dockerfile.build-cgo, go.mod, go.sum, web/package-lock.json, web/package.json
test -e docs/superpowers/specs/parallel-sweep.md && echo EXISTS || echo absent      # absent
```

## Normative rules (09 §9.2.1; quoted precisely, implement them, do not paraphrase them into something weaker)

- **`ci/stage.txt`**: two keys, `shipped_stage:` and `release:`; this PR writes `shipped_stage: pre-S0` and
  `release: S0`. Comment lines start with `#` (a 4-line `#` header). **No `levels:` line, ever.** `shipped_stage:`
  values in order: `pre-S0 < S2r1 < S2r2 < S2r3 < S2b < S3 < S4 < S5 < S6 < S7 < S8 < S9 < S10`, then `H1`, `H2`, `H3`,
  `H2H3` as landed. UI-0, T8 and S4 r1 are never marker values.
- **Release grammar**: `^(S0|S1|S2r[1-3]|S2br[12]|UI0|S3|S4r[12]|S5r[1-9][0-9]?|floor|S6|S7|S8|S9|S10|H1|H2|H3|H2H3)$`,
  ordered `S0 < S1 < S2r1 < S2r2 < S2r3 < S2br1 < S2br2 < S3 < S4r1 < S4r2 < S5r1 < S5r2 < … < floor < S6 < S7 < S8 < S9
  < S10`; `floor` follows any `S5r<n>` and precedes `S6`; `UI0` is off the chain (follows `S2r3`, precedes `S4r1`, is
  never another release's predecessor; the flip to `S4r1` needs a current `UI0` row too); H IDs follow `S10` in landing
  order, `H2H3` counts as both, each lands once; moving `release:` to a landed `UI0`/H ID or from an H ID to an S ID is
  backwards.
- **Trailer**: `Stage-Release: <stage> <n|flip>`, `<stage>` in `S0 S1 S2 S2b UI-0 S3 S4 S5 floor S6 S7 S8 S9 S10 H1 H2
  H3 H2H3`; `<n>` the release number within the stage (`S2b 2` = `S2br2`, `S5 3` = `S5r3`; single-release stages use
  `1`); `flip` names the release the flip PR sets; `Stage-Release: none` for feature work. A flip PR carries
  `<stage> flip` instead of `<stage> <n>`.
- **Ledger row** (`ci/soak.txt`): `<release> <landing commit> <binary_id> <commit> <since UTC> <levels> <evidence SHA-256>`;
  the landing commit is the latest first-parent commit on `main` that moved `release:` forward to `<release>`; a row is
  current only if its landing commit is the current one for its ID.
- **`check_soak.py` refuses** (in `extraction-gates`, every non-doc PR):
  (a) a PR that changes `release:` unless `ci/soak.txt` holds a current row for the predecessor (the release `release:`
  named before the PR, or before `UI0` when that is `UI0`) whose `<since>` is at least 14 days old and whose `commit` is
  an ancestor of the PR's base with a `ci/stage.txt` naming that predecessor; the `S4r1` flip also needs a current `UI0`
  row;
  (b) a PR any of whose commits carries a `Stage-Release:` trailer whose named release's predecessor has no such row
  (for `UI-0 1` the predecessor is `S2r3`'s row at the earliest; for an H trailer `S10`'s or the previous H's), and a
  `flip` trailer on a PR that changes no `shipped_stage:`, or a `shipped_stage:` change without one;
  (c) an ID or trailer outside its grammar, a backwards value (except an admitted revert), a `shipped_stage:` change
  without a `release:` change, a `shipped_stage:` value whose stage table names no flip PR, a `levels:` line, and a PR
  that changes `ci/soak.txt` or `ci/soak_health_allow.txt` together with any file outside those two;
  (d) a PR that touches a non-doc file, other than one that changes only owner bookkeeping (`ci/soak.txt`,
  `ci/soak_health_allow.txt`, `ci/prev_ref.txt`), with a commit that carries no `Stage-Release:` line. Merge commits have
  no exemption.
- **The one creation case**: the PR that first commits `ci/stage.txt` (no file on base → `release: S0`) is accepted.
- **Bot commits**: an author in the fixed list `dependabot[bot]`, `renovate[bot]`, `github-actions[bot]` counts as
  `Stage-Release: none` only when every path the PR touches is admitted by the fixed ecosystem table: `gomod` →
  `go.mod`, `go.sum` in `/` and `/core`; `npm` → `package.json`, `package-lock.json` in `/web`; `docker` → `Dockerfile*`
  (`Dockerfile`, `Dockerfile.build-cgo`); `github-actions` → `.github/`; `gitsubmodule` → the gitlinks `.gitmodules`
  lists (`.standards`). Any other path: refused like an untrailered commit.
- **Reverts**: a PR passes with no soak row, though it moves the marker back, only when every commit is a `git revert`
  of one named commit whose `Stage-Release:` value is not `none`, carries no trailer of its own, touches nothing else,
  and the PR's tree equals the base with those reverts applied (`git revert --no-commit <sha>…` recomputed and trees
  compared; the message decides nothing). Check (c) still applies except its backwards clause. The revert of an
  `<H> flip` commit is admitted only while no production tag (`v<major>.<minor>.<patch>`, no pre-release suffix) contains
  it (`git tag --contains`).
- **`soak_record.py`** (the owner's command, run from the operator's checkout over ssh; `DEPLOY_HOST`, `DEPLOY_BIN`,
  `DEPLOY_STATE_DIR`): `--deployed` appends `{at, binary_id, commit, release}` read from the new binary's
  `--version-json` to `.soak/deploys.log`; `--release <id>` computes `<since>` (the oldest recorded deploy from which
  every recorded deploy ran a binary of `<id>` at or after its landing commit; a `make rollback` or another release's
  binary restarts it; `UI0`'s and the H releases' exceptions as 09 §9.2.1 states), cross-checks the unit's journal (the
  `boot binary_id=<id> release=<release:>` and `boot levels=<vector>` lines; refuses "journal does not cover the soak"
  when the oldest retained entry is newer than `<since>` or a logged boot since ran another binary), refuses a `commit`
  that is `""` or ends in `+dirty`, verifies the binary (commit on `main`, at or after the landing commit, its
  `ci/stage.txt` names the release), applies the pass criteria (no `restore_*` or boot errors since `<since>`, no unit
  exit other than 0 or 75; every owner at the release's levels in both `--print-storage-levels` and
  `<db>.storage-levels`, all 0 for S0–S10 and `UI0`, and for an H release the fixed H table; one health read with no
  Critical component Down and no token outside `ci/soak_health_allow.txt`; the claim-violation rule for `S2br1`/`S2br2`
  and the decode-census rule for `S4r1`), writes `.soak/<release>.json` and prints the row only when all pass. It never
  writes `ci/soak.txt` itself. Its H owner table is PC-21-01's to fill; at S0 it holds no entries and an H release
  refuses with "H owner table not defined".
- **Trailer machinery**: `.gitmessage` (a commit template with a `Stage-Release: ` line to fill); `scripts/git-hooks/commit-msg`
  (installed by `scripts/setup-git-hooks.sh` into `--git-common-dir`'s `hooks/`, like the pre-commit hook) refuses a
  commit whose staged paths include a non-doc file and whose message has no `Stage-Release:` line matching the trailer
  grammar or `none`; it never inserts a value. The PR template gains a "Stage-Release:" line. The `/parallel-sweep`
  task spec gains a required `stage_release` value per task, and the child prompt tells every child commit to carry it
  (the sweep merges with `--admin`, so review alone is no check there). `ratchet-lower.yml`'s commit adds
  `-m "Stage-Release: none"`. `.gitignore` gains `.soak/`.
- **`extraction-gates.yml`**: a `check-soak` job (`timeout-minutes` ≤ 30) running `scripts/ci/check_soak.py --base
  <base sha> --head <head sha>` on every non-doc PR and on bookkeeping-only PRs; it joins the aggregator's `needs`.

## TDD: tests to write first (every case the spec lists; names coined here, file names are the spec's)

`scripts/tests/test_check_soak.py` (stdlib `unittest`; each case builds a throwaway git repo in
`tempfile.mkdtemp()` with a `ci/stage.txt`, an optional `ci/soak.txt` and crafted commits, and calls the checker's
entry function with an injectable clock and tag list):

- creation of `ci/stage.txt` (`pre-S0`, `S0`) accepted; a `release:` change with no predecessor row refused; with a row
  13 days old refused; 14 days old accepted; a row whose landing commit is not current refused; a row whose `commit` is
  not an ancestor of the base refused;
- an S6 flip PR before the `floor` row refused; an S7 live PR refused until the post-flip `S6` row exists; an S1 content
  PR before the `S0` row refused; an `S6 1` content PR before the `floor` row refused;
- a non-doc PR with an untrailered commit refused; an untrailered merge commit refused; a docs-only PR with no trailer
  accepted; a bookkeeping-only PR with no trailer accepted;
- IDs and trailers outside the grammar refused; a backwards `release:` refused; a `shipped_stage:` change without
  `release:` refused; a `flip` trailer with no `shipped_stage:` change refused and the reverse; a `levels:` line refused;
  a `ci/soak.txt` change with any other file refused (also `ci/soak_health_allow.txt`);
- an exact revert of a trailered commit admitted with no row; a revert PR with an extra commit, a revert commit touching
  another file, and a forged revert whose tree differs refused; a re-landed release with only its old row refused;
- bots: HEAD's grouped Dependabot commit `1c2244869`'s six paths admitted; a `.standards`-only Dependabot PR admitted; a
  Dependabot gomod PR in `/core` admitted; a Dependabot commit that also touches `internal/` refused; the root-tidy
  commit with its `none` trailer admitted; an unknown bot login refused;
- `UI0`: a `UI-0 1` trailer admitted after `S2r3`'s row; the release after `UI0` admitted on the row before `UI0`; the
  `S4r1` flip refused without a current `UI0` row;
- H: an `H1 flip` PR setting `shipped_stage:` and `release:` admitted after `S10`'s row; the exact revert of an `H1 flip`
  admitted before a production tag contains it and refused after (`-rc.N` tags do not count).

`scripts/tests/test_soak_record.py` (fake ssh and fake binary on `PATH`, as PC-02-04's harness will be; build them here
under `tempfile`): "journal does not cover the soak" when the oldest journal entry is newer than `<since>`; a
`make rollback` deploy restarting `<since>`; a redeploy inside the release keeping it; a claim record whose only entry
predates `<since>` passing (S2br1); a `+dirty` or empty commit refused; a level above 0 on an S release refused; a health
token outside the allow-list refused; `--deployed` appends one JSON line with the four fields.

Hook test (in `test_check_soak.py` or its own case): the `commit-msg` hook refuses a non-doc commit without the line,
accepts `Stage-Release: none`, accepts `Stage-Release: S0 1`, refuses `Stage-Release: S11 1`, and ignores a docs-only
commit.

## Invariants

- I1: no `release:` change and no stage-trailered commit merges without the predecessor's current ≥ 14-day row.
- I2: every commit of a non-doc PR carries a trailer, except a bot commit inside the admitted ecosystem paths.
- I3: only an exact inverse of trailered commits bypasses the soak; an H flip only until a production tag contains it.
- I4: bookkeeping files change alone, and no PR writes levels.
- I5: on this PR itself, `check_soak.py` passes: creation case for `ci/stage.txt`, every commit carries `Stage-Release: S0 1`.

## STOP conditions

- 09 §9.2.1 says something these rules do not capture, or two of its sentences conflict for a case you must code: STOP
  and report the case; do not choose.
- A required input (a production tag list, the stage tables' flip rows) cannot be read in CI without a credential: STOP.

## Acceptance

```bash
python3 -m unittest discover -s scripts -p 'test_check_soak.py' -v 2>&1 | tail -1    # OK
python3 -m unittest discover -s scripts -p 'test_soak_record.py' -v 2>&1 | tail -1   # OK
python3 scripts/ci/check_soak.py --base origin/main --head HEAD; echo "exit=$?"      # accepts (creation case), exit=0
grep -v '^#' ci/stage.txt                                                            # shipped_stage: pre-S0 / release: S0
grep -c '^levels:' ci/stage.txt                                                      # 0
bash scripts/setup-git-hooks.sh && test -x "$(git rev-parse --path-format=absolute --git-common-dir)/hooks/commit-msg" && echo hook-installed
git log origin/main..HEAD --format=%B | grep -c '^Stage-Release: S0 1$'             # equals the PR's commit count
make ci                                                                              # last line: ✅ All CI checks passed!
```

Never run `scripts/soak_record.py --release` against a real host; its tests use fakes only.

## Out of scope

`ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt` (OWNER-02); the `deploy` recipe's and `make rollback`'s
call of `soak_record.py --deployed` and the fake-ssh harness for `make rollback` (PC-02-04); `TestBootLogsBinaryID` and
the boot log lines (PC-02-02); the H owner table (PC-21-01).

## Rollback

Revert the PR (its own revert is admitted by the rule it adds only once it is on `main`; before that, revert normally).
No runtime change.

## Repo rules

Headers on every new/changed file (`ci/stage.txt` with a `#` header; the hook and scripts with their language's
comment header). Push workflow files with `git push`. Conventional commit `ci: soak, marker and trailer gate
(ci/stage.txt, check_soak.py, soak_record.py)`, ending `Stage-Release: S0 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`; never squash, never `--admin`.

## Planner notes

- `docs/superpowers/specs/parallel-sweep.md` (in the inventory row, and linked from CLAUDE.md) does not exist at
  `ac4368bdb`; the sweep's task spec and child prompt live in `.claude/commands/parallel-sweep.md` and
  `.claude/skills/parallel-sweep-impl/references/child-prompt.md`, which this brief edits instead.
- PC-01-04's `compat-rehearsal` trigger must not fire on this PR's creation of `ci/stage.txt` (no `ci/prev_ref.txt`
  exists until OWNER-02); PC-01-04's brief fixes that. If it fires anyway, STOP: do not create `ci/prev_ref.txt`.
- This row and PC-01-05 are file-disjoint and may run in parallel after PC-01-04; PC-01-06 does not touch
  `extraction-gates.yml`.
