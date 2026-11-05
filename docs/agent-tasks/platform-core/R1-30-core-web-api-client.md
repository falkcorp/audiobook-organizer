<!-- file: docs/agent-tasks/platform-core/R1-30-core-web-api-client.md -->
<!-- version: 1.1.3 -->
<!-- guid: 646a92e3-e0bd-4a2f-9b2c-edfca1100759 -->
<!-- last-edited: 2026-11-05 -->

# R1-30 — core/web workspace with @mediacore/api-client (runtime and generator) only, consumed through file:, apiFetch's checkBuild and onStaleBuild, the Docker frontend stage building core/web first

| Field | Value |
|---|---|
| ID | `R1-30` |
| Tier | **T2** (Sonnet / high): a new npm workspace and build order across every build site |
| Release / wave | R1 / W21 |
| Depends on | R1-19, R1-13, R1-05 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 07 §7.19 steps 2-3, §7.3.1, §7.3.2; 09 §9.4 R1 Content: UI, Verification (UI steps); 12 §12.4.3 (npm lockstep) |
| Branch | `agent/platform-core-r1-30-core-web-api-client` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/web/`
- `web/`: `package.json`, `package-lock.json`
- `web/src/utils/apiFetch.ts`
- `web/src/components/StaleBuildBanner.tsx`
- `Dockerfile`
- `Makefile`
- `Dockerfile.build-cgo`
- `CLAUDE.md`
- `scripts/ci/build_web.sh`
- `core/Makefile`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-30" -b agent/platform-core-r1-30-core-web-api-client origin/main
cd "$REPO/.worktrees/platform-core-r1-30"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

07 §7.19 step 2 (and the start of step 3's mechanism): create the `core/web` npm workspace with
**`@mediacore/api-client` only** (runtime and generator), generated from `core/openapi/core.json` (R1-13) and consumed
by ABO's `web/` through `file:` (07 §7.3.1 layout, §7.3.2; 09 §9.4 R1 "Content: UI"; 04 key "R1 create npm
@mediacore/api-client"):

- `core/web/package.json` (`"private": true`, `"workspaces": ["packages/*"]`; the `../examples/hello/web` entry is
  added at shell adoption, 07 §7.3.1), its own `package-lock.json`, `core/web/.gitignore` exactly per 07 §7.3.1
  "Ignored outputs" (`node_modules/`, `dist/`, `*.tsbuildinfo`, `coverage/`, `test-results/`, `playwright-report/`,
  `blob-report/`).
- `core/web/packages/api-client/`: the fetch runtime and the generator over `openapi-typescript`/`openapi-fetch`
  (13 §13.4 rows; 13 §13.4.1 E1: the package carries `"overrides": {"openapi-typescript": {"typescript": "$typescript"}}`
  and copies the two-TypeScript alias layout), `exports` with `"development": "./src/index.ts"` and `"default"` at
  `dist/` (07 §7.3.1 "Exports"); `readPageBuildId()` (the only meta reader) and
  `checkBuild(response, { buildId, shell })` with an `onStaleBuild` callback (03 §3.6 "Build identity"; 07 §7.5.2).
- ABO's `web/package.json`: `"@mediacore/api-client": "file:../core/web/packages/api-client"`; `apiFetch` calls
  `checkBuild` (replacing R1-19's local comparison, so there is one rule in one place, 07 §7.5.2) and
  `StaleBuildBanner` subscribes through `onStaleBuild`.
- `scripts/ci/build_web.sh` now finds `core/web` and builds `-w @mediacore/api-client` first (R1-05 already handles
  "when present"; adjust only if needed); `make web-deps` stamp includes `core/web` sources.
- **The Docker frontend stage** builds `core/web` before `web/`, in the four steps of 07 §7.3.2 "Docker frontend
  stage, in order": manifests + `npm ci` in `core/web`; sources + the ordered build; `web/` manifests + `npm ci`;
  `web/` sources + build; the optional `ARG BUILD_ID` only in the frontend stage. `Dockerfile.build-cgo`'s frontend stage
  (`Dockerfile.build-cgo:14-19`) needs the same four steps, or the cgo build breaks once `web/` depends on `core/web`
  (07 §7.3.2 "both Docker stages"); it is in this row and changes in this PR.
- `core/Makefile` (this row owns the file at this wave): `web-test` runs `npm ci` then vitest in `core/web` (no longer
  "not applicable", 12 §12.3.11), and the `openapi-check` target and its place in `make -C core ci` (05-http-api §4.6,
  modelled on `scripts/ci/check_generated_types.sh:1-22`): it runs R1-13's `hello openapi --out <tmp>` and `diff`s the
  result against the committed `core/openapi/core.json`, failing on any difference, so a stale `core.json` fails core
  CI before this row's generator reads it. R1-13 defines the command line and does not edit `core/Makefile`.
- npm lockstep (12 §12.4.3): `@mediacore/api-client`'s version equals core's `version.Version`; `check_core_version.py`
  (R1-06) enforces it later; record the value here.

## Tests this task owns

- The dry-run RC build through `prerelease.yml` green, with `git status --porcelain` empty after it (09 §9.4 R1
  Verification, UI steps; 07 §7.19 step 2 done-check).
- The image job green with `core/web` present (09 §9.4 R1 Verification).
- The generated-client coverage check (name coined here: `test:client-coverage` in `core/web/packages/api-client`):
  every operation in `core/openapi/core.json` has a generated client function, and regenerating from the committed
  `core.json` produces no diff.
- vitest for the runtime: `checkBuild` triggers `onStaleBuild` on a mismatching `X-App-Build`, never for `dev`;
  `readPageBuildId` reads `<meta name="mediacore-build">` once.
- `runtime.errors.test.ts` (07 §7.18, api-client): envelope decoding (the problem shape and the legacy error envelope),
  timeout, bounce detection, and `skipAuthRedirect` taken from `x-mediacore-permission: public`.
- The `openapi-check` freshness gate (above) fails when `core/openapi/core.json` differs from a fresh `hello openapi`
  output: prove it once by editing `core.json` in a scratch copy and running `make -C core openapi-check`.

## Dependency check

R1-19, R1-13, R1-05 merged:
`test -f core/openapi/core.json && test -f scripts/ci/build_web.sh && test -f web/src/components/StaleBuildBanner.tsx && echo ok`.

## Re-verify

```bash
test ! -e core/web && echo NEW                          # expect NEW
grep -n 'mediacore' web/package.json | wc -l            # expect 0
grep -n 'core/web' scripts/ci/build_web.sh | head -3    # R1-05's "when present" branch
sed -n 13,22p Dockerfile                                # the frontend stage to rewrite (node:26-alpine, COPY web/package*.json)
grep -n 'openapi-typescript\|openapi-fetch' docs/specs/platform-core/13-toolchain.md | head -3   # the pinned versions (7.13.0, 0.17.0 per 13 §13.4)
```

## Steps

1. Tests first. 2. The workspace, the package and its build (`npm run build -w @mediacore/api-client` from
`core/web`). 3. ABO `web/` consumption and `apiFetch`/banner rewiring. 4. Docker frontend stage. 5. `core/Makefile`
`web-test`; `.dockerignore` already has the `core/web/packages/*/dist` line (R1-04). 5a. `Dockerfile.build-cgo`'s
frontend stage gets the same four steps as `Dockerfile`. 5b. `CLAUDE.md`'s worktree recipe (section "Worktree
Discipline", the block after "After creating a worktree"): replace `npm ci --prefix web` with `make web-deps` and keep
the comment's point (Playwright comes from the worktree), because a bare `npm ci --prefix web` no longer installs
`core/web` (07 §7.3.2 lists it among the `npm ci` sites); bump CLAUDE.md's header (version patch, `last-edited`). 6. Header bumps (JSON files carry
no header; TS/MJS files do); fragment `changelog.d/<today>_core_web_api_client.md` (`### Added`: "Internal: the core
web workspace with the generated API client."). Commit: `feat(web): core/web workspace with @mediacore/api-client`.

## Code contract

`@mediacore/api-client` exports `readPageBuildId()`, `checkBuild(response, { buildId, shell })` with an `onStaleBuild` subscription, and the generated client, as 07 §7.5.2 and 03 §3.6 name them.

## Acceptance

```bash
make web-deps && make web-build && test -z "$(git status --porcelain)" && echo CLEAN   # expect CLEAN
npm test --prefix web -- --run && (cd core/web && npm test --workspaces --if-present -- --run)   # expect pass
make -C core web-test                                                                    # expect pass
docker build -t abo-r130 . && docker build -f Dockerfile.build-cgo --output type=local,dest="$(mktemp -d)" .   # expect both succeed
grep -c 'npm ci --prefix web' CLAUDE.md                                                  # expect 0
grep -c 'make web-deps' CLAUDE.md                                                        # expect >= 1
make test-e2e && make ci && make -C core ci                                              # expect exit 0
```

On the PR: the dry-run RC (`prerelease.yml` in its dry-run mode, or the method R1-05 recorded) green with a clean tree;
`docker-image` green.

## Out of scope

`@mediacore/ui-kit`, `@mediacore/shell`, hello's web package (shell adoption); switching `api.ts` sections to
generated operations (each R2 flip, 07 §7.19 step 3).

## Rollback

Revert the PR; `web/` returns to R1-19's local stale-build rule.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

