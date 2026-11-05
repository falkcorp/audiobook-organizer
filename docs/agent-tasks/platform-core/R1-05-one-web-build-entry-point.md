<!-- file: docs/agent-tasks/platform-core/R1-05-one-web-build-entry-point.md -->
<!-- version: 1.0.0 -->
<!-- guid: ab971b1f-8614-4ccb-b9ba-e106ed75c3e3 -->
<!-- last-edited: 2026-11-05 -->

# R1-05 — One web build entry point: scripts/ci/build_web.sh behind make web-deps at every npm ci site, the reusable-workflow frontend halves moved into ABO-local jobs, the release pre-build scripts

| Field | Value |
|---|---|
| ID | `R1-05` |
| Tier | **T2** (Sonnet / high): twelve npm ci sites and three reusable-workflow callers; the site list is given |
| Release / wave | R1 / W11 |
| Depends on | R1-04 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.4 R1 Content: build and CI (build_web.sh); 09 §9.1 (the 12 npm ci sites); 07 §7.3.2 |
| Branch | `agent/platform-core-r1-05-one-web-build-entry-point` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `scripts/ci/build_web.sh`
- `scripts/tests/test_build_web.py`
- `Makefile`
- `Dockerfile`
- `Dockerfile.build-cgo`
- `.github/workflows/ci.yml`
- `.github/workflows/e2e.yml`
- `.github/workflows/binary-smoke.yml`
- `.github/workflows/security.yml`
- `.github/workflows/vulnerability-scan.yml`
- `.github/workflows/frontend-ci.yml`
- `.github/workflows/prerelease.yml`
- `.github/workflows/release-prod.yml`
- `.woodpecker/checks-build.yaml`
- `scripts/ci_remote.py`
- `.github/dependabot.yml`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-05" -b agent/platform-core-r1-05-one-web-build-entry-point origin/main
cd "$REPO/.worktrees/platform-core-r1-05"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

One entry point for web dependencies, so that when `web/` starts depending on `core/web` (R1-30) no build site
breaks (09 §9.4 R1 "Content: build and CI", the `build_web.sh` sentence; 09 §9.1 row "`npm ci` runs in 12 places";
07 §7.3.2 "One entry point", "Every build site calls the entry point", "Reusable workflows cannot call it",
"Caches, filters and triggers"):

- `scripts/ci/build_web.sh deps` (POSIX `sh`, under 20 lines; the Docker frontend stage is `node:26-alpine` with no
  `bash`): if `core/web/package.json` exists, `npm ci` in `core/web` then `npm run build -w @mediacore/api-client`
  (the explicit ordered list, today one package, 07 §7.3.2); then `npm ci` in `web/`. It builds nothing in `web/`,
  generates no UI build ID, and leaves a caller's `BUILD_ID` in the environment untouched.
- `make web-deps`: a stamp target writing `web/node_modules/.web-deps.stamp`, depending on `web/package-lock.json`,
  `core/web/package-lock.json` (when present) and `$(shell find core/web/packages -path '*/src/*' -type f 2>/dev/null)`;
  `web-build`, `web-install`, `web-test`, `web-lint`, `web-typecheck` depend on it; `web-dev` depends only on the
  install half.
- **Every one of the 12 `npm ci` sites of 09 §9.1 calls it** (the script directly where `make` is unavailable),
  plus the CLAUDE.md worktree recipe (a doc line; CLAUDE.md is out of this row's files, see Planner notes).
- **The reusable-workflow callers** (07 §7.3.2 lists five): the three CI callers `ci.yml:127`
  (`reusable-ci-minimal.yml`, `run-frontend`), `frontend-ci.yml:64` and `nightly.yml:31` (`reusable-ci.yml`) set
  their frontend half off and run it in ABO-local jobs after `make web-deps`; the two release callers
  (`prerelease.yml:34-51`, `release-prod.yml:45-58`) rewrite `pre-build-script` to
  `sh scripts/ci/build_web.sh deps && cd web && npm run build`.
- Caches, filters and triggers: each `cache-dependency-path` adds `core/web/package-lock.json`; path filters
  (`frontend-ci.yml:11-17`, `e2e.yml:68`) add `core/web/**`; the `changes` classifier sets `web=true` for
  `core/web/*` and `scripts/ci/build_web.sh`; Dependabot's npm entry adds `/core/web` (09 §9.4 R1: "path filters,
  the `changes` classifier and Dependabot gain `core/web` and `/core`").

## Verification step first (BLOCKED to the lead if it cannot be done)

Before editing the release callers, read `reusable-release.yml` of `falkcorp/github-common` at the SHA in
`.github/ghcommon-ref.txt` (`861d4e83ccf918c9354a2d5f5685068f655cdf58` at `f7ac5b11e`) and record in the PR
description what `frontend-enabled` does (07 §7.3.2; 09 §9.4 R1). If its frontend half installs or builds `web/`
itself, set `frontend-enabled: false` in both callers and state in the PR where Node comes from for
`pre-build-script` (or, if the owner prefers, a github-common pre-install input with the ref bump in the same PR).
If you cannot read that file, the task is BLOCKED: report to the lead and stop.

```bash
gh api repos/falkcorp/github-common/contents/.github/workflows/reusable-release.yml?ref=$(cat .github/ghcommon-ref.txt) --jq .content | base64 -d | grep -n -i -A8 'frontend-enabled'
```

## Dependency check

R1-04 merged: `test -f .github/workflows/docker-image.yml && echo ok`.

## Re-verify (the 12 sites; expected at `f7ac5b11e`)

```bash
git grep -n 'npm ci' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile
```

Expected (09 §9.1's list; HEAD lines where they differ from 748fd787a are marked):
`Dockerfile:18`, `Dockerfile.build-cgo:17`, `.github/workflows/prerelease.yml:51`, `release-prod.yml:58`,
`ci.yml:379`, `e2e.yml:167`, `binary-smoke.yml:55`, `security.yml:82`, `vulnerability-scan.yml:62`,
`.woodpecker/checks-build.yaml:85`, `scripts/ci_remote.py:516`, and `Makefile:198` (`web-install`; 09 cites
`Makefile:194`, and at `748fd787a` it ran `npm install`; HEAD's `web-install` runs `npm ci --no-audit --no-fund`).
Plus the comment lines `Makefile:191-192`. If a site is missing or a new one exists, STOP and report.

```bash
grep -n 'run-frontend\|frontend-enabled\|pre-build-script' .github/workflows/ci.yml .github/workflows/prerelease.yml .github/workflows/release-prod.yml   # expect ci.yml:133; prerelease.yml:40,51; release-prod.yml:52,58
grep -n 'reusable-ci.yml' .github/workflows/frontend-ci.yml .github/workflows/nightly.yml   # expect frontend-ci.yml:64, nightly.yml:31
grep -n 'directory: /web' .github/dependabot.yml          # expect 1 (line 70)
grep -n '^web-install:\|^web-build:\|^web-test:\|^web-lint:\|^web-typecheck:\|^web-dev:' Makefile   # expect 6 targets
```

## Steps

1. Test first: `scripts/tests/test_build_web.py` runs `sh scripts/ci/build_web.sh deps` against a temp tree with a
   stub `npm` on `PATH` that logs its argv and cwd, and asserts: without `core/web`, exactly one call
   (`ci` in `web/`); with a `core/web/package.json`, the order `ci` in `core/web`, `run build -w
   @mediacore/api-client` in `core/web`, `ci` in `web/`; a non-zero `npm` exit stops the script with that exit;
   `BUILD_ID` set by the caller is still in the environment of every `npm` call and the script never sets it;
   `sh -n` passes and the file has no bash-only syntax (run it with `dash` if present). This is the gate test this
   task owns (`scripts/ci/build_web.sh`).
2. Write `scripts/ci/build_web.sh` (header as `#` comments; `set -eu`; argument `deps`; unknown argument exits 2).
3. Makefile: add `web-deps` (stamp) and make the five targets depend on it; `web-install` becomes the stamp's
   prerequisite alias (keep its name; its comment says it runs `build_web.sh deps`).
4. Replace each of the 12 sites with `make web-deps` or `sh scripts/ci/build_web.sh deps` (Dockerfiles, Woodpecker
   and `ci_remote.py` call the script; workflows call `make web-deps` when they have `make`). Keep each site's
   flags' intent (for example `vulnerability-scan.yml`'s `--ignore-scripts`: if the script cannot pass it, keep
   that site's `npm ci --ignore-scripts` and add a comment why; list any such exception in the PR).
5. Reusable callers per the Goal and the verification step. For the three CI callers, the ABO-local frontend job
   (as `ci.yml:358-420`'s `knip-report` and the `web` steps already do) runs `make web-deps` then the checks the
   reusable half ran (lint, typecheck, vitest).
6. Caches, filters, classifier, Dependabot per the Goal.
7. Header bumps; fragment `changelog.d/<today>_build_web_entry_point.md` (`### Changed`: "CI: one web dependency
   entry point (`make web-deps`) at every build site."). Commit:
   `ci(web): one web build entry point behind make web-deps at every npm ci site`.

## Code contract

`scripts/ci/build_web.sh deps` (exit 2 on any other argument); `make web-deps` writes `web/node_modules/.web-deps.stamp`.

## Acceptance

```bash
python3 -m pytest scripts/tests/test_build_web.py -q                        # expect all passed
git grep -n 'npm ci' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile | grep -v 'build_web.sh' | grep -v '^\S*:\s*#'
#   expect only the exceptions listed in the PR (none, ideally)
make web-deps && make web-deps | grep -c 'npm'                              # second run is a no-op: expect 0
make web-build && make web-test                                             # expect "Frontend built" and tests passing
docker build -t abo-r105 .                                                  # expect success (frontend stage calls the script)
make ci                                                                     # expect exit 0
```

On the PR, record: the `frontend-enabled` finding; a dry-run RC build through `prerelease.yml` (use the
workflow's dry-run path if it has one; otherwise state that the first post-merge RC is the check and watch it).

## Out of scope

Creating `core/web` (R1-30); the Docker frontend stage's four-step order (R1-30, because a `COPY` of
`core/web/package.json` fails before the file exists, 07 §7.3.2).

## Rollback

Revert the PR; every site returns to its own `npm ci`.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

## Planner notes

- Missing file in the row: 07 §7.3.2 lists `nightly.yml:31` (`reusable-ci.yml`) among the three CI callers whose
  frontend half must move to ABO-local jobs; `.github/workflows/nightly.yml` is not in R1-05's files (R1-03 has it,
  in W09, so no same-wave collision). Add it to R1-05's row.
- The CLAUDE.md worktree recipe `npm ci --prefix web` is one of the sites 07 §7.3.2 names; CLAUDE.md is outside
  every row's files. The lead (or the owner) should change that line to `make web-deps` when R1-30 lands.
- 09 §9.1 cites `Makefile:194` (`web-install`) as an `npm ci` site; at `748fd787a` that target ran `npm install`,
  and HEAD (`f7ac5b11e`) runs `npm ci` at `Makefile:198`. The list stays 12 sites.
