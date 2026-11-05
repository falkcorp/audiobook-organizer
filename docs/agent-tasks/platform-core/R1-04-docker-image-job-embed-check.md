<!-- file: docs/agent-tasks/platform-core/R1-04-docker-image-job-embed-check.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4ec57f2c-fd43-44f4-ac1f-57531f066721 -->
<!-- last-edited: 2026-11-05 -->

# R1-04 — docker-image PR job, both Dockerfiles copying core/go.mod and core/go.sum, .dockerignore lines, and the embed-context check

| Field | Value |
|---|---|
| ID | `R1-04` |
| Tier | **T2** (Sonnet / high): build-context rules (dockerignore patterns, embed inputs) are easy to get subtly wrong |
| Release / wave | R1 / W10 |
| Depends on | R1-03 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.4 R1 Content: build and CI (docker-image job, embed check); 09 §9.1 (Dockerfiles, .dockerignore); 12 §12.3.11 (embed-check) |
| Branch | `agent/platform-core-r1-04-docker-image-job-embed-check` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `.github/workflows/docker-image.yml`
- `scripts/ci/check_embed_context.py`
- `scripts/ci/embed_build_outputs.txt`
- `scripts/tests/test_check_embed_context.py`
- `scripts/ci/image_probe.sh`
- `Dockerfile`
- `Dockerfile.build-cgo`
- `.dockerignore`
- `Makefile`
- `core/Makefile`
- `core/scripts/ci/check_embed_outputs.py`
- `ci/gates.txt`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-04" -b agent/platform-core-r1-04-docker-image-job-embed-check origin/main
cd "$REPO/.worktrees/platform-core-r1-04"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Make the Docker build know about `core/` and prove it on every PR (09 §9.4 R1 "Content: build and CI";
09 §9.1 facts on both Dockerfiles and `.dockerignore`; 03 §3.2 item 4 "Docker"; 12 §12.3.11 `embed-check`):

1. **Both Dockerfiles** copy `core/go.mod core/go.sum ./core/` beside `go.mod go.sum` before
   `go mod download && go mod verify` (`Dockerfile:54-55`, `Dockerfile.build-cgo:52-53` at `f7ac5b11e`, same as
   `748fd787a`). The empty committed `core/go.sum` (R1-01) makes the literal `COPY` always succeed.
2. **`.dockerignore`** adds `**/node_modules`, `core/web/packages/*/dist` and `core/examples/*/web/node_modules`
   and keeps every existing exclusion, `docs` (`:11`) and root `*.md` (`:30`) included (09 §9.4 R1; R4-4).
3. **The embed-context check** `scripts/ci/check_embed_context.py` (03 §3.2 item 4, the full rule): `go list -e
   -json` over the root module and over core for every (GOOS, tag set) of 03 §3.5.1 item 4's one list; every
   package's `EmbedFiles` (and embed patterns of packages that report "no matching files found") is normalised
   (strip `all:`, join with the package dir relative to its module root) and fails when it matches a
   `.dockerignore` pattern (moby semantics) and is not listed in `scripts/ci/embed_build_outputs.txt` (one line
   today: `web/dist`) as produced in-image; the list is cross-checked with the Dockerfiles' `COPY --from=` lines
   **into a `go build` stage** under that stage's `WORKDIR` (a listed path with no such line, or such a line with no
   listed path, fails; the runtime-stage binary copies `Dockerfile:90` and `Dockerfile.build-cgo:75` are outside
   the comparison). A "no matching files found" error is ignored only for a listed output. Runs in root `make ci`
   on every PR (target `embed-check`). Core's half: `core/scripts/ci/check_embed_outputs.py` over core's own list
   `core/scripts/ci/embed_build_outputs.txt` (empty until hello's UI lands), no Dockerfile cross-check, behind
   `make -C core embed-check` (12 §12.3.11).
4. **The `docker-image` PR job** (`.github/workflows/docker-image.yml`, new; 09 §9.2.1 `docker-image` row):
   builds the full image (layer-cached) and `Dockerfile.build-cgo` with `--output type=local`; run 1 starts the
   image on a seeded volume, waits for `"ready":true` on `/health`, and runs the probe script
   `scripts/ci/image_probe.sh`; run 2 starts it with `--db /config/x.pebble` on a named volume, asserts
   `.encryption_key` sits beside the database (`/config/.encryption_key`), recreates the container on the same
   volume and asserts the key file survives byte-identical (09 §9.4 R1, OPS5-7, OPS6-10; from F3 also
   `secrets.keyring`). Trigger: every PR that touches a non-doc file (via the `changes` classifier).
   The level probe (`--print-storage-levels` inside the container) and the build-ID probe (`X-App-Build` equals the
   embedded `<meta name="mediacore-build">`, `TestBuildID_HeaderEqualsEmbeddedMeta`) are **wired here and off**:
   `image_probe.sh` runs them only when `PROBE_STORAGE_LEVELS=1` / `PROBE_BUILD_ID=1` (names coined here), which
   R1-23 and R1-19 set in `docker-image.yml` when their flags exist.

## Dependency check

R1-03 merged (`test -f core/.errcheck-baseline && echo ok`). Otherwise STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
grep -n '^COPY go.mod go.sum ./' Dockerfile Dockerfile.build-cgo       # expect Dockerfile:54, Dockerfile.build-cgo:52
grep -n '^COPY --from=frontend-builder /build/web/dist ./web/dist' Dockerfile Dockerfile.build-cgo   # expect :68 and :65
grep -n '^COPY --from=go-builder' Dockerfile Dockerfile.build-cgo      # expect Dockerfile:90, Dockerfile.build-cgo:75 (runtime copies)
grep -n -x 'docs\|\*\.md\|web/dist\|node_modules\|testdata' .dockerignore   # expect 5 lines (docs, node_modules, web/dist, testdata, *.md)
git grep -n '//go:embed' -- '*.go'     # expect 5: web_embed.go:11 (web/dist), internal/server/op_ledger_embed.go:22,
                                       #   internal/authority/authoritybuild/seed.go:26, internal/transcribe/batch.go:22, canary.go:38
grep -n 'docker' .github/workflows/prerelease.yml | head -3            # expect docker-enabled: false (no image on prereleases, 09 §9.1)
test ! -e .github/workflows/docker-image.yml && echo NEW               # expect NEW
```

## Steps

1. Tests first: `scripts/tests/test_check_embed_context.py` with fixtures: (a) the real tree passes; (b) a fixture
   package with `//go:embed docs/x.json` fails naming the file and the `.dockerignore` line; (c) a fixture with no
   built `web/dist` passes (listed output); (d) an `all:` pattern in a non-root package is normalised (03 §3.2
   item 4, MOD8-11); (e) a listed output with no `COPY --from` into a go-build stage fails, and a runtime-stage
   copy is ignored (MOD7-5). Write `check_embed_context.py` until they pass; then `check_embed_outputs.py`
   (core half) with its own small test inside the same test file.
2. Dockerfiles: change both `COPY go.mod go.sum ./` lines to

   ```dockerfile
   COPY go.mod go.sum ./
   COPY core/go.mod core/go.sum ./core/
   ```

   Nothing else in either Dockerfile (the frontend stage changes in R1-05 and R1-30).
3. `.dockerignore`: append the three lines with a comment citing 09 §9.4 R1.
4. Makefile: root `embed-check` target (`python3 scripts/ci/check_embed_context.py`) added to `ci:`; core
   `embed-check` target in `core/Makefile`'s `ci`. Register `embed-context` (`check`, `own-job`) in `ci/gates.txt`.
5. `scripts/ci/image_probe.sh` (POSIX `sh`): arguments `<container> <base-url>`; waits up to
   `READY_TIMEOUT` (default 300) for `"ready":true` on `/health`; if `PROBE_STORAGE_LEVELS=1`, runs
   `docker exec <container> /app/audiobook-organizer --print-storage-levels` and checks it exits 0 and prints
   JSON; if `PROBE_BUILD_ID=1`, compares the `X-App-Build` response header of `/` with the `content` of
   `<meta name="mediacore-build">` in the served HTML. Exit non-zero with a one-line reason on any failure.
6. `.github/workflows/docker-image.yml`: `changes`-gated, `timeout-minutes: 30`, Buildx with GitHub cache,
   actions pinned to SHAs. Run 1 seeds the volume by starting the image once on `/data/audiobooks.pebble` until
   ready and stopping it, then starts it again on the same volume and runs `image_probe.sh` (the second start
   exercises a non-empty store). Run 2 as Goal item 4. Build `Dockerfile.build-cgo` with
   `--output type=local,dest=dist/` and assert the binary exists. Dump `docker logs` on failure.
7. Header bumps; fragment `changelog.d/<today>_docker_image_job.md` (`### Added`: "CI: every PR builds and runs the
   Docker image; the image build copies the core module."). Commit:
   `ci(docker): PR image job, core module in both Dockerfiles, embed-context check`.

## Tests this task owns

- `scripts/ci/check_embed_context.py`: passes on HEAD, fails on a fixture that embeds a `docs/` path
  (09 §9.4 R1 Verification).
- The `docker-image` job, both runs (09 §9.4 R1 Verification).

## Code contract

Script contracts: `scripts/ci/check_embed_context.py` (no arguments; exit 0/1 with one line per offending embed input), `core/scripts/ci/check_embed_outputs.py` (same, core only), `scripts/ci/image_probe.sh <container> <base-url>` with `READY_TIMEOUT`, `PROBE_STORAGE_LEVELS`, `PROBE_BUILD_ID`.

## Acceptance

```bash
python3 -m pytest scripts/tests/test_check_embed_context.py -q          # expect all passed
python3 scripts/ci/check_embed_context.py                               # expect exit 0 on the tree
make -C core embed-check                                                # expect exit 0
docker build -t abo-r104 . && docker build -f Dockerfile.build-cgo --output type=local,dest=/tmp/r104 .   # expect both succeed
make ci && make -C core ci                                              # expect exit 0
```

On the PR: the `docker-image` check is green with both runs' log lines ("ready", "encryption key beside the
database and kept across recreate"); record them in the PR description.

## STOP conditions

- The image build needs `docs/` in the context (it must not; `docs` stays excluded).
- A placeholder `web/dist` would be needed anywhere (it never is; 03 §3.5.1 item 4).

## Out of scope

The frontend stages (R1-05, R1-30); the probes' flags themselves (R1-19 `--version-json`/build ID, R1-23
`--print-storage-levels`); prerelease Docker builds (stay off).

## Rollback

Revert the PR. The image built from `main` is unchanged in behaviour.

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

- Missing file in the row: core's own embed list `core/scripts/ci/embed_build_outputs.txt` (03 §3.2 item 4 and 12
  §12.3.11 name it; `check_embed_outputs.py` reads it) is created here, empty until hello's UI. Add it to the row.
- The two probes this job wires but cannot yet run are gated by `PROBE_STORAGE_LEVELS` and `PROBE_BUILD_ID`
  (names coined here); R1-23 and R1-19 already list `docker-image.yml`, but R1-23 does not depend on R1-04 (it is
  ordered after it only by wave, W14 after W10). Add R1-04 to R1-23's depends-on.
