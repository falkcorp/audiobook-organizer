<!-- file: docs/agent-tasks/platform-core/PC-01-06-web-build-entry-point.md -->
<!-- version: 1.0.0 -->
<!-- guid: c85f4b0c-60b8-4b71-a30b-6311b491ce0c -->
<!-- last-edited: 2026-11-04 -->

# PC-01-06 — Web build entry point `scripts/ci/build_web.sh` behind `make web-deps` at every non-release `npm ci` site; reusable-workflow callers set `run-frontend: false`

| Field | Value |
|---|---|
| ID | PC-01-06 |
| Tier | **T1 (Sonnet/medium)**: ten one-line site swaps, three reusable-workflow caller changes and one small script; the site list and every edit are given |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-05 (both edit `Dockerfile`, `Dockerfile.build-cgo`, `Makefile`; serial chain) |
| Priority / effort | P2 / M |
| Branch | `build/pc-01-06-web-deps` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §6.6 |
| Owning spec | `09-abo-on-core.md` S0 "Web build entry points" and §9.1 row "`npm ci` runs in 12 places" (the one site list); `07-ui-shell.md` §7.3.2 |

At S0 `core/web` does not exist yet, so `build_web.sh deps` only runs `npm ci` in `web/`; the script and the call
sites exist now so UI-0 changes one file instead of twelve (09 S0; 07 §7.3.2).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" show origin/main:scripts/ci/check_embed_context.py | head -1     # PC-01-05 merged; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-06" -b build/pc-01-06-web-deps origin/main
cd "$REPO/../audiobook-organizer-pc-01-06"
npm ci --prefix web
```

## Exact files

Create: `scripts/ci/build_web.sh`, `scripts/tests/test_build_web.py`.
Modify: `Makefile`, `Dockerfile`, `Dockerfile.build-cgo`, `.github/workflows/ci.yml`, `.github/workflows/e2e.yml`,
`.github/workflows/binary-smoke.yml`, `.github/workflows/security.yml`, `.github/workflows/vulnerability-scan.yml`,
`.github/workflows/frontend-ci.yml`, `.github/workflows/nightly.yml`, `.woodpecker/checks-build.yaml`,
`scripts/ci_remote.py`, `.github/dependabot.yml`, `CLAUDE.md`, and `scripts/tests/test_ci_classifier.py` (see Planner
notes); one `changelog.d/` fragment.
**Do not touch** `.github/workflows/prerelease.yml` or `.github/workflows/release-prod.yml` (their `pre-build-script`s
are PC-08-03's, after the owner reads `reusable-release.yml`).

## Re-verify: the 12 sites of 09 §9.1 (expected at `ac4368bdb` plus PC-01-01..05; line numbers may have moved, the text must not)

```bash
git grep -n 'npm ci' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile | grep -v ':[0-9]*:[[:space:]]*#' | wc -l   # 12
git grep -n 'npm ci' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile | grep -v ':[0-9]*:[[:space:]]*#'
```

Expected lines (text exact; the filter drops comment lines such as the `web-install` comment block):

| # | Site | Text now |
|---|---|---|
| 1 | `Dockerfile` (frontend stage) | `RUN npm ci --prefer-offline --no-audit` |
| 2 | `Dockerfile.build-cgo` (frontend stage) | `RUN npm ci --prefer-offline --no-audit` |
| 3 | `.github/workflows/ci.yml` (knip-report job) | `run: npm ci --prefix web --no-audit --no-fund` |
| 4 | `.github/workflows/e2e.yml` | `run: cd web && npm ci` |
| 5 | `.github/workflows/binary-smoke.yml` | `run: cd web && npm ci && npm run build` |
| 6 | `.github/workflows/security.yml` | `run: npm ci` (step has `working-directory: web`) |
| 7 | `.github/workflows/vulnerability-scan.yml` | `run: cd web && npm ci --ignore-scripts` |
| 8 | `.woodpecker/checks-build.yaml` | `- npm ci --prefix web --no-audit --no-fund` |
| 9 | `scripts/ci_remote.py` (`MAKE_LEGS` web-test) | `"npm ci --prefix web --no-audit --no-fund && make --no-print-directory web-test",` |
| 10 | `Makefile` (`web-install`) | `@cd $(WEB_DIR) && npm ci --no-audit --no-fund` |
| 11 | `.github/workflows/prerelease.yml` | `pre-build-script: 'cd web && npm ci && npm run build'` (leave) |
| 12 | `.github/workflows/release-prod.yml` | `pre-build-script: 'cd web && npm ci && npm run build'` (leave) |

Also: `grep -n '^npm ci --prefix web' CLAUDE.md | wc -l` → 1. `grep -n 'run-frontend:' .github/workflows/ci.yml | wc -l`
→ 1. Any other result: STOP and report.

## Steps

1. **Create `scripts/ci/build_web.sh`** exactly (POSIX `sh`: the frontend Docker stage is `node:26-alpine`, no bash;
   under 20 lines; 07 §7.3.2). The extra-argument pass-through is fixed here (see Planner notes):
   ```sh
   #!/bin/sh
   # file: scripts/ci/build_web.sh
   # version: 1.0.0
   # guid: <fresh uuid>
   # last-edited: <today>
   # The one web-dependency entry point (07 §7.3.2, 09 S0): npm ci in core/web and its
   # packages built in order (when core/web exists), then npm ci in web/. It builds nothing
   # in web/ and never sets BUILD_ID. Usage: build_web.sh deps [extra npm ci args]
   set -eu
   [ "${1:-}" = deps ] || { echo "usage: $0 deps [npm ci args]" >&2; exit 2; }
   shift
   cd "$(dirname "$0")/../.."
   if [ -f core/web/package.json ]; then
       (cd core/web && npm ci --no-audit --no-fund "$@" && npm run build -w @mediacore/api-client)
   fi
   (cd web && npm ci --no-audit --no-fund "$@")
   ```
   `chmod +x scripts/ci/build_web.sh`.
2. **`Makefile`** (find the targets by name, not line):
   - add, above `web-install`:
     ```make
     ## web-deps: Install web dependencies through scripts/ci/build_web.sh (the one entry point, 07 §7.3.2).
     ## A stamp target: a no-op while both lockfiles and core/web's package sources are unchanged.
     WEB_DEPS_STAMP := $(WEB_DIR)/node_modules/.web-deps.stamp
     CORE_WEB_INPUTS := $(wildcard core/web/package.json core/web/package-lock.json) $(shell find core/web/packages -path '*/src/*' -type f 2>/dev/null)
     .PHONY: web-deps
     web-deps: $(WEB_DEPS_STAMP)
     $(WEB_DEPS_STAMP): $(WEB_DIR)/package.json $(WEB_DIR)/package-lock.json scripts/ci/build_web.sh $(CORE_WEB_INPUTS)
     	@sh scripts/ci/build_web.sh deps
     	@touch $@
     ```
   - `web-install:` becomes `web-install: web-deps` and its recipe becomes the single line
     `@echo "✅ Dependencies installed"` (keep its comment block, adding "through web-deps").
   - `web-typecheck:`, `web-test:`, `web-lint:` gain the prerequisite `web-deps`; `web-build: web-install` stays (it now
     reaches `web-deps` through `web-install`); `web-dev:` gains `web-deps` (at S0 the install half is all there is;
     UI-0 splits it, 07 §7.3.2).
3. **Dockerfiles** (both): replace the frontend stage's
   ```
   WORKDIR /build/web
   COPY web/package*.json ./
   RUN npm ci --prefer-offline --no-audit
   COPY web/ ./
   RUN npm run build
   ```
   with
   ```
   WORKDIR /build
   COPY scripts/ci/build_web.sh scripts/ci/build_web.sh
   COPY web/package*.json web/
   RUN sh scripts/ci/build_web.sh deps --prefer-offline
   COPY web/ web/
   WORKDIR /build/web
   RUN npm run build
   ```
   The output stays `/build/web/dist`, so the `COPY --from=frontend-builder /build/web/dist ./web/dist` line and
   `scripts/ci/embed_build_outputs.txt` are unchanged.
4. **Workflows** (sites 3–7):
   - `ci.yml` site 3: `run: make web-deps`.
   - `e2e.yml` site 4: `run: make web-deps`. Its `pull_request.paths` list gains `'core/web/**'`, `'core/go.mod'`,
     `'core/go.sum'`.
   - `binary-smoke.yml` site 5: `run: make web-deps && cd web && npm run build`.
   - `security.yml` site 6: delete that step's `working-directory: web` line and set `run: make web-deps`.
   - `vulnerability-scan.yml` site 7: `run: sh scripts/ci/build_web.sh deps --ignore-scripts`.
   - `frontend-ci.yml`: both `paths:` lists gain `'core/web/**'`, `'core/go.mod'`, `'core/go.sum'`.
5. **Woodpecker and ci_remote** (sites 8–9): `.woodpecker/checks-build.yaml` site 8 becomes
   `- make --no-print-directory web-deps`; `scripts/ci_remote.py` site 9 becomes
   `"make --no-print-directory web-deps && make --no-print-directory web-test",`.
6. **The three reusable-workflow callers** (09 S0 "Precondition of UI-0"; 07 §7.3.2). First verify the input exists in
   each pinned reusable workflow:
   ```bash
   for wf in ci.yml frontend-ci.yml nightly.yml; do
     ref=$(grep -o 'falkcorp/github-common/.github/workflows/[a-z-]*\.yml@[0-9a-f]*' .github/workflows/$wf | head -1)
     path=${ref#falkcorp/github-common/}; file=${path%@*}; sha=${path#*@}
     echo "$wf -> $file @ $sha: $(gh api "repos/falkcorp/github-common/contents/$file?ref=$sha" -q .content | base64 -d | grep -c 'run-frontend:')"
   done   # expect each count >= 1
   ```
   If a count is 0, do **not** edit that caller; STOP for it and report "reusable workflow <file> at <sha> has no
   `run-frontend` input". Otherwise:
   - `ci.yml` job `ci`: `run-frontend: ${{ needs.changes.outputs.web == 'true' }}` becomes `run-frontend: false`.
   - `frontend-ci.yml` job `frontend` and `nightly.yml` job `full-ci`: add `run-frontend: false` under `with:`.
   - Add one ABO-local job to each of the three workflows (name `web-checks`, coined here), replacing the frontend half:
     `runs-on: ubuntu-latest`, `timeout-minutes: 20`, `permissions: contents: read`; in `ci.yml` add
     `needs: changes` and `if: ${{ needs.changes.outputs.web == 'true' }}`; steps: `actions/checkout` and
     `actions/setup-node` at the SHAs those files already use, `node-version: '26'`, `cache: 'npm'`,
     `cache-dependency-path: web/package-lock.json`; then `run: make web-deps`, `run: make web-lint`,
     `run: make web-typecheck`, `run: make web-test`, `run: make web-build`.
7. **`ci.yml` classifier**: in the `changes` job, `case "$f" in web/*|.github/workflows/*) web=true ;; esac` becomes
   `case "$f" in web/*|core/web/*|scripts/ci/build_web.sh|.github/workflows/*) web=true ;; esac`. Add the cases
   `core/web/package.json` and `scripts/ci/build_web.sh` (each `web=true`) to `scripts/tests/test_ci_classifier.py`.
8. **Dependabot**: the npm entry's `directory: /web` becomes `directories: ["/web", "/core/web"]` (09 S0); keep its
   comment and `ignore` rules.
9. **CLAUDE.md**: the worktree recipe line `npm ci --prefix web   # Playwright must come from the worktree, not an orphan`
   becomes `make web-deps          # Playwright must come from the worktree, not an orphan` (09 S0: "CLAUDE.md's
   worktree recipe becomes `make web-deps`"). Bump CLAUDE.md's header.
10. **`scripts/tests/test_build_web.py`** (stdlib `unittest`): runs `sh scripts/ci/build_web.sh` with a fake `npm` on
    `PATH` (a temp script that appends its cwd and argv to a log file) over a temp copy containing `web/package.json`:
    - no `core/web`: the log holds exactly one call, `npm ci --no-audit --no-fund` in `web/`;
    - with a `core/web/package.json`: `npm ci …` in `core/web`, then `npm run build -w @mediacore/api-client`, then
      `npm ci …` in `web/`, in that order;
    - `deps --ignore-scripts`: both `npm ci` calls carry `--ignore-scripts`;
    - no argument or another first argument: exit 2, no npm call;
    - a caller's `BUILD_ID` in the environment is passed through unchanged and never set by the script;
    - `grep -c bash scripts/ci/build_web.sh` is 0 and the file has at most 20 lines.
11. Headers: bump every changed file's header; the fragment has none.

## Acceptance

```bash
git grep -n 'npm ci' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile | grep -v ':[0-9]*:[[:space:]]*#' | wc -l   # 2 (prerelease.yml, release-prod.yml)
git grep -n 'build_web.sh\|web-deps' -- Dockerfile Dockerfile.build-cgo .github/workflows .woodpecker scripts/ci_remote.py Makefile | wc -l   # >= 10
python3 -m unittest discover -s scripts -p 'test_build_web.py' -v 2>&1 | tail -1         # OK
python3 -m unittest discover -s scripts -p 'test_ci_classifier.py' -v 2>&1 | tail -1     # OK
rm -rf web/node_modules && make web-deps && test -f web/node_modules/.web-deps.stamp && echo stamped   # stamped
make web-deps                                                                            # no npm output (up to date)
docker build -f Dockerfile -t abo-webdeps .                                              # exit 0
python3 scripts/check_toolchain_versions.py | tail -1                                    # Toolchain versions consistent
make ci                                                                                  # last line: ✅ All CI checks passed!
```

On the PR: `web-checks` runs in all three workflows (frontend-ci and nightly on their triggers) and the reusable
workflows' frontend halves report skipped.

## Out of scope

The two release callers (PC-08-03); `core/web` itself and the Docker frontend stage's `core/web` copies (UI-0,
PC-08-02); `cache-dependency-path` entries for `core/web/package-lock.json` (once it exists, UI-0).

## Rollback

Revert the PR. Every site returns to its own `npm ci`.

## Repo rules

Headers on every changed file; push workflow files with `git push`. Conventional commit `build(web): one entry point
for web dependencies (scripts/ci/build_web.sh, make web-deps)`, ending `Stage-Release: S0 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`; never squash, never `--admin`.

## Planner notes

- `vulnerability-scan.yml` installs with `--ignore-scripts`; 09 S0 requires every site to call `build_web.sh` and the
  spec's script takes no flags, so this brief fixes a pass-through of extra `npm ci` arguments after `deps` (and uses it
  for the Docker stages' `--prefer-offline`).
- The `web=true` classifier cases are this row's done-check, but their test file `scripts/tests/test_ci_classifier.py`
  belongs to PC-01-04; this brief adds two cases to it (PC-01-04 merges first).
- 09 S0 puts the three `run-frontend: false` changes in S0, while 07 §7.3.2 calls them a precondition of UI-0; both
  agree they must land before UI-0. The ABO-local replacement job is not named by the spec; `web-checks` is coined, and
  whether `web-typecheck` belonged to the reusable frontend half is not verifiable here (the reusable workflow is
  external).
- Dependabot npm `directories` gains `/core/web` before that directory exists (as 09 S0 says); Dependabot may log a
  missing-manifest error for it until UI-0.
