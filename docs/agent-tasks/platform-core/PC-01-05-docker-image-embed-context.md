<!-- file: docs/agent-tasks/platform-core/PC-01-05-docker-image-embed-context.md -->
<!-- version: 1.0.0 -->
<!-- guid: 41b4a453-a4c5-4378-9633-15ce60377fb8 -->
<!-- last-edited: 2026-11-04 -->

# PC-01-05 — `docker-image` PR job, Dockerfile module copies, `.dockerignore` and the embed-context check

| Field | Value |
|---|---|
| ID | PC-01-05 |
| Tier | **T2 (Sonnet/high)**: build-context rules (moby `.dockerignore` patterns, `COPY --from` destinations, `go list` embed errors) are easy to get subtly wrong; the checks are fully specified |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-04 (the `changes` classification the job reuses; `ci/gates.txt` rows) |
| Priority / effort | P1 / M |
| Branch | `ci/pc-01-05-docker-image` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §6.4, §6.5 |
| Owning spec | `09-abo-on-core.md` S0 "Docker" and "Gates" (embed-context paragraph), §9.1 rows "Both Dockerfiles…" and "Prereleases skip Docker"; `03-architecture.md` §3.2 item 4 ("Docker"); §9.2.1 table row `docker-image` |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" show origin/main:.github/workflows/extraction-gates.yml | head -1    # PC-01-04 merged; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-05" -b ci/pc-01-05-docker-image origin/main
cd "$REPO/../audiobook-organizer-pc-01-05"
npm ci --prefix web
```

Never run `go work init` in the checkout or the worktree.

## Exact files

Create: `.github/workflows/docker-image.yml`, `scripts/ci/check_embed_context.py`, `scripts/ci/embed_build_outputs.txt`,
`scripts/tests/test_check_embed_context.py`, `scripts/ci/image_probe.sh`.
Modify: `Dockerfile`, `Dockerfile.build-cgo`, `.dockerignore`, `Makefile`, `README.md`, and `ci/gates.txt` (one row;
see Planner notes); one `changelog.d/` fragment.

## Re-verify (expected at `ac4368bdb` plus PC-01-01..04)

```bash
grep -n '^COPY go.mod go.sum ./$' Dockerfile Dockerfile.build-cgo | wc -l                  # 2 (Dockerfile:54, Dockerfile.build-cgo:52)
grep -n 'go mod download && go mod verify' Dockerfile Dockerfile.build-cgo | wc -l          # 2
grep -n 'COPY --from=frontend-builder /build/web/dist ./web/dist' Dockerfile Dockerfile.build-cgo | wc -l   # 2 (:68, :65)
grep -n '^WORKDIR /build$' Dockerfile Dockerfile.build-cgo | wc -l                          # 2 (the go-builder stages)
grep -n 'COPY --from=go-builder' Dockerfile Dockerfile.build-cgo | wc -l                    # 2 (runtime-stage binary copies, out of scope)
grep -n '^docs$\|^web/dist$\|^\*\.md$\|^node_modules$\|^testdata$' .dockerignore | wc -l   # 5 (:11, :20, :30, :18, :21)
grep -rn '^//go:embed' --include=*.go . | grep -v _test.go | grep -v node_modules | wc -l   # 5 (web_embed.go:11 web/dist; transcribe x2; server op_ledger_embed.go; authority seed)
grep -n 'mkdir -p /data && chown audiobook:audiobook /data' Dockerfile | wc -l              # 1 (:93; the image prepares only /data)
```

## What to build

### 1. Dockerfiles (09 S0 "Docker"; 03 §3.2 item 4)

In both, directly after `COPY go.mod go.sum ./` add `COPY core/go.mod core/go.sum ./core/`, before
`RUN go mod download && go mod verify`; `COPY . .` stays. The Go stage takes no `BUILD_ID` and no link step receives
one. The frontend stage is unchanged in this PR (its `core/web` copy is UI-0's; its `build_web.sh` call is PC-01-06's).

### 2. `.dockerignore`

Add `**/node_modules`, `core/web/packages/*/dist`, `core/examples/*/web/node_modules`; keep every existing exclusion,
`docs` included (R4-4). Add the 4-line header's missing `last-edited` field.

### 3. `scripts/ci/check_embed_context.py` + `scripts/ci/embed_build_outputs.txt` (03 §3.2 item 4 is the rule's one text)

- `embed_build_outputs.txt`: one path per line, `#` comments; at S0 exactly `web/dist`.
- The script runs `go list -e -json` over the root module and over `core` for every (GOOS, tag set) of 03 §3.5.1
  item 4's list, takes each package's `EmbedPatterns`/`EmbedFiles`, normalises a pattern (strip `all:`, join with the
  package directory relative to its module root), and **fails** when an embed input matches a `.dockerignore` pattern
  (moby semantics: patterns are cleaned and root-relative, `**` matches any number of directories, a pattern that
  matches a directory excludes everything under it, `!` re-includes, the last matching pattern wins) unless it is on the
  list. A `pattern …: no matching files found` package error is ignored only for a listed path; for any other path it
  fails.
- It cross-checks the list against the Dockerfiles: every listed path must be the destination of a `COPY --from=…`
  line **into a stage that runs `go build`** (a `go-builder` stage) whose destination lies under that stage's
  `WORKDIR`, and every such line must name a listed path. Runtime-stage copies of the binary (`Dockerfile:90`,
  `Dockerfile.build-cgo:75`) are out of scope.
- Root `Makefile`: target `check-embed-context` running it, added to `ci:` (it runs on every PR, 09 §9.2.1 table row 1).
- Tests (`test_check_embed_context.py`, stdlib `unittest`, fixture trees in `tempfile.mkdtemp()`): passes on HEAD with
  `web/dist` listed; **fails** on a fixture embedding a `docs/` path; **fails** on a fixture whose `go-builder`
  `COPY --from` destination is missing from the list; **passes** a fixture with a runtime-stage binary copy; an
  `all:web/dist` pattern in a non-root package matches its normalised entry; a listed output with "no matching files"
  passes and an unlisted one fails; a `!`-re-included path passes.

### 4. `docker-image.yml` (09 §9.2.1 table row `docker-image`; S0 "Docker")

A required check named `docker-image`, on every PR that touches a non-doc file (the same `changes` classification as
`extraction-gates`, skip through job-level `if:`), with layer caching (`docker/build-push-action` with `cache-from`/
`cache-to: type=gha`, or the existing setup the repo uses; pin actions to SHAs). It:

1. builds the **full** image from `Dockerfile` and `Dockerfile.build-cgo` with `--output type=local`;
2. runs `scripts/ci/image_probe.sh`:
   - **default run**: start the image on a named `/data` volume, wait for `/health` to report `"ready":true` (the
     probe's timeout comes from a variable, never a literal toolchain or version); create the first admin through
     `POST /api/v1/auth/setup`, sign in, set a secret through `PUT /api/v1/config` (e.g. `openai_api_key`), recreate
     the container on the same volume, and assert that `.encryption_key` sits beside the database on `/data` and that
     the secret still reads as set (masked, non-empty) after the recreate;
   - **second run on `/config`**: prepare the volume as a user must (the image creates and chowns only `/data`):
     `docker run --rm --user 0 --entrypoint chown -v cfg:/config <abo-image> 1000:1000 /config`; start with an
     overridden command `serve --host 0.0.0.0 --db /config/x.pebble`; seed, boot, recreate; assert `.encryption_key`
     beside the database on `/config` and the secret readable after the recreate.
   - Leave two marked extension points (comments naming the rows): `--print-storage-levels` inside the container
     (PC-02-01 adds the flag and the assertion) and `X-App-Build` equals the embedded `<meta name="mediacore-build">`
     (`TestBuildID_HeaderEqualsEmbeddedMeta`, PC-02-02). From S3 the probe also asserts `secrets.keyring` (PC-09-*).
3. `timeout-minutes` ≤ 30; `permissions: contents: read`.

No credential the probe creates leaves the job; it uses throwaway values generated in the job.

### 5. `README.md` compose notes

Add the `/config` volume preparation (the one-line `chown` command above, with an explanation that the image runs as
uid 1000 and creates only `/data`), next to the existing Docker/compose instructions (09 S0 "Deploy": the README's
compose notes gain the `/config` preparation).

## Invariants

- I1: every `//go:embed` input of both modules is inside the Docker build context or is a listed in-image output.
- I2: the root list and the `go-builder` `COPY --from` destinations agree exactly.
- I3: `docker build` of both Dockerfiles succeeds with the nested module (`COPY core/go.mod core/go.sum` never fails,
  because `core/go.sum` exists, PC-01-02).
- I4: a secret written through the API survives a container recreate on `/data` and on a user-prepared `/config`.

## Acceptance

```bash
python3 scripts/ci/check_embed_context.py; echo "exit=$?"                                   # exit=0
python3 -m unittest discover -s scripts -p 'test_check_embed_context.py' -v 2>&1 | tail -1  # OK
docker build -f Dockerfile -t abo-pr .                                                      # exit 0
docker build -f Dockerfile.build-cgo --output type=local,dest=/tmp/abo-cgo-out .             # exit 0; binary in /tmp/abo-cgo-out
ABO_IMAGE=abo-pr sh scripts/ci/image_probe.sh                                               # exit 0; prints both runs' assertions as passed
make ci                                                                                     # last line: ✅ All CI checks passed!
```

On the PR: `docker-image` green; paste the run link.

## Out of scope

The frontend stage's `build_web.sh` call (PC-01-06) and its `core/web` copies (UI-0); level and build-ID assertions
(PC-02-01, PC-02-02); `prerelease.yml` Docker builds (none, 09 S0 "Gates"); recording which Dockerfile
`reusable-release.yml` builds (an owner step, OWNER-02).

## Rollback

Revert the PR (09 §9.4.1: revert PR). No runtime change. Reverting PC-01-02 later also requires reverting this
PR's `COPY core/go.mod core/go.sum` lines, which need `core/` to exist.

## Repo rules

Headers on every new/changed file; push workflow files with `git push`; actions pinned to SHAs. Conventional commit
`ci: docker-image PR job and the embed-context check`, ending `Stage-Release: S0 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`; never squash, never `--admin`.

## Planner notes

- The new required check `docker-image` must be registered in `ci/gates.txt` (09 S0: "each registered in
  `ci/gates.txt`"); the row does not list that file. PC-01-04 merges before this row, so there is no collision.
- `.dockerignore` at HEAD has a 3-field header (no `last-edited`); this PR completes it.
