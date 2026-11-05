<!-- file: docs/agent-tasks/platform-core/R1-06-core-version-checkpoints-readiness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f616e9b-4602-4e36-8503-c2d539986fac -->
<!-- last-edited: 2026-11-05 -->

# R1-06 — Core version checks, checkpoints and split readiness: check_core_version.py, the core-checkpoint job, the readiness target and its SC scripts with the nightly fold-in job

| Field | Value |
|---|---|
| ID | `R1-06` |
| Tier | **T2** (Sonnet / high): report-only scripts over fixed commands; no runtime code |
| Release / wave | R1 / W20 |
| Depends on | R1-05, R1-13, R1-30 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 12 §12.3 (SC1-SC10 commands), §12.3.11 (readiness target), §12.4.1 (core-checkpoint job), §12.4.3; 09 §9.4 R1 CI classification (check_core_version.py, core-checkpoint) |
| Branch | `agent/platform-core-r1-06-core-version-checkpoints-readiness` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/scripts/ci/check_core_version.py`
- `core/scripts/ci/core_api_gate.py`
- `core/scripts/ci/core_coverage_by_package.py`
- `core/scripts/ci/coredoc_check.py`
- `core/tools/`
- `core/Makefile`
- `.github/workflows/release-prod.yml`
- `.github/workflows/split-readiness.yml`
- `scripts/ci/core_consumer_smoke.py`
- `scripts/ci/web_build_against_tarballs.py`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-06" -b agent/platform-core-r1-06-core-version-checkpoints-readiness origin/main
cd "$REPO/.worktrees/platform-core-r1-06"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Report-only readiness and the two version checks R1's classification table assigns here (09 §9.4 R1 classification
rows `check_core_version.py` and `core-checkpoint`; 12 §12.3 SC1–SC10 commands, §12.3.11 the `readiness` target,
§12.4.1 the `core-checkpoint` job, §12.4.3 the version constant and `MinCoreVersion`). No runtime code.

1. **`core/scripts/ci/check_core_version.py`** (core only, in `make -C core ci` as `version-check`): `version.Version`
   ≥ the newest `refs/core-checkpoints/v0.*` (fetched explicitly; none exists yet, which passes), hello's
   `MinCoreVersion` ≤ `version.Version`, and npm lockstep: every `@mediacore/*` package version equals
   `version.Version` (12 §12.4.2 "npm lockstep", §12.4.3). Its core-only checks run from an export of `core/`.
2. **`core-checkpoint`** job in `.github/workflows/release-prod.yml` (12 §12.4.1, exactly): a separate job with
   `permissions: contents: write` and `continue-on-error: true`, which nothing `needs`; if
   `git diff <newest checkpoint>..HEAD -- core/` is non-empty and `version.Version` > the newest checkpoint, it runs
   `git update-ref refs/core-checkpoints/v0.Y.Z <commit>` and pushes with the explicit refspec; if `core/` changed and
   the constant was not bumped, it records nothing and opens or updates one issue labelled `core-version-bump-needed`.
   It also runs when `prerelease.yml` auto-promotes an RC (12 §12.4.1; the R1 freeze stops that until R1-31).
3. **`make -C core readiness`** (report-only; 12 §12.3.11): `apidiff-gate coverage coredoc-check vulncheck`
   (SC1, SC5's thresholds, SC6, SC9) with their scripts `core/scripts/ci/core_api_gate.py`,
   `core/scripts/ci/core_coverage_by_package.py`, `core/scripts/ci/coredoc_check.py`, and the `core/tools/` modules
   (`tools/apidiff`, `tools/govulncheck`, `tools/staticcheck` pinned like the root's; `check_module_skew.py` compares
   the staticcheck tools modules). It never fails a merge and never writes outside `core/`: a JSON and Markdown
   report with date and commit to `$(READINESS_OUT)` (default `core/.ci/readiness/`, ignored by `core/.gitignore`).
   Thresholds follow OWNER-01's recorded Q-G answer (row notes); if that answer is missing, use 12 SC5's stated
   numbers and say so in the PR.
4. **Root-side SC scripts** (pre-split they need ABO's `web/` or a temp module; 12 §12.3.11): SC3
   `scripts/ci/core_consumer_smoke.py` and SC8 `scripts/ci/web_build_against_tarballs.py`, per 12 §12.3's SC3 and SC8
   commands, run from the nightly job below.
5. **`.github/workflows/split-readiness.yml`** (the root-side nightly fold-in job): runs `make -C core readiness`
   and SC3/SC8, uploads the artifact, and folds the report into `docs/plans/platform-core/split-readiness.md`
   **through a PR** (never a direct push); dropped at cutover step 6 (12 §12.8.2).

## Tests this task owns (09 §9.4 R1 classification)

- `check_core_version.py` (unit tests beside it under `core/scripts/ci/`: no checkpoint passes; a checkpoint
  above the constant fails; hello's `MinCoreVersion` above the constant fails; an npm package off lockstep fails).
- `core-checkpoint` (`release-prod.yml`): a workflow-level test is not possible in CI; prove the job's script with a
  local test that runs it against a throwaway git repo (records the ref when bumped; opens no ref and prints the issue
  text when not).

## Dependency check

R1-05, R1-13, R1-30 merged: `test -d core/web && test -f core/openapi/core.json && test -f scripts/ci/build_web.sh && echo ok`.

## Re-verify

```bash
grep -n '^## \|^### SC' docs/specs/platform-core/12-repo-split.md | sed -n 1,20p    # SC1..SC10 sections, the command source
grep -n 'core-checkpoint\|continue-on-error' .github/workflows/release-prod.yml      # expect 0 before
git ls-remote origin 'refs/core-checkpoints/*' | wc -l                               # expect 0 (no checkpoint yet)
grep -n 'readiness' core/Makefile core/.gitignore                                    # R1-01 created the ignore line
```

## Steps

Tests first; then the scripts; the `core/Makefile` targets (`version-check` into `ci`; `readiness` separate); the
`core/tools/` modules (each its own `go.mod`, with the `go` directive equal to the root's; R1-01's toolchain guard
reads `core/tools/*/go.mod`); the two workflows (actions pinned to SHAs; `split-readiness.yml` opens its PR with the
PAT, as `auto-revert.yml` pushes, so the PR's checks run). Header bumps; fragment
`changelog.d/<today>_core_readiness.md` (`### Added`: "CI: core version checks, checkpoint recording and a nightly
split-readiness report."). Commit: `ci(core): version check, core-checkpoint job and the readiness report`.

## Code contract

`core/scripts/ci/check_core_version.py` (no arguments; exit 0/1); `make -C core readiness` writes only under `$(READINESS_OUT)` and always exits 0 unless a script crashes.

## Acceptance

```bash
python3 core/scripts/ci/check_core_version.py && python3 -m pytest core/scripts/ci -q   # expect exit 0, all passed
make -C core readiness; echo "exit $?"; ls core/.ci/readiness/                         # expect exit 0 and a .json and .md report
test -z "$(git status --porcelain)" && echo CLEAN                                       # expect CLEAN (readiness wrote only ignored files)
python3 scripts/ci/check_module_skew.py                                                 # expect exit 0 (tools modules compared)
make ci && make -C core ci                                                              # expect exit 0
```

## Out of scope

Recording a checkpoint (the first production release after R1 does it); bumping `version.Version` (the "core
version" PR, 12 §12.4.1).

## Rollback

Revert the PR; nothing at runtime depends on it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
