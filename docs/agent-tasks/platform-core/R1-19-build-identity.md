<!-- file: docs/agent-tasks/platform-core/R1-19-build-identity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f6947ba-4d93-4c9b-92ad-bc8d3abdecb9 -->
<!-- last-edited: 2026-11-05 -->

# R1-19 — Build identity: --version-json and --print-upgrade-floor, app.BinaryID, the UI build ID in <meta name="mediacore-build">, StaleBuildBanner and apiFetch's build rule, goreleaser's dead flags, the deploy post-check over ssh

| Field | Value |
|---|---|
| ID | `R1-19` |
| Tier | **T2** (Sonnet / high): server and SPA halves of one build-ID rule; both are given exactly |
| Release / wave | R1 / W17 |
| Depends on | R1-17, R1-29, R1-04 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.4 R1 Content: endpoints and build identity; 07 §7.6.3; 05-health §5H.3.3; 09 Out-of-scope defects item 5 |
| Branch | `agent/platform-core-r1-19-build-identity` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `main.go`
- `version_json_test.go`
- `core/app/binaryid.go`
- `web/vite.config.ts`
- `web/index.html`
- `web/src/components/StaleBuildBanner.tsx`
- `web/src/components/StaleBuildBanner.test.tsx`
- `web/src/utils/apiFetch.ts`
- `web/src/App.tsx`
- `.goreleaser.yml`
- `Makefile.local.example`
- `.github/workflows/docker-image.yml`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-19" -b agent/platform-core-r1-19-build-identity origin/main
cd "$REPO/.worktrees/platform-core-r1-19"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

The two halves of R1's one build-ID rule (09 §9.4 R1 "Content: endpoints and build identity"; 03 §3.6 "Build
identity"; 07 §7.6.3 "One UI build ID, one source"; 05-health §5H.3.3; 09 "Out-of-scope defects found" item 5):

**Server side**
- `--version-json` fast exit printing exactly `{app, version, commit, ui_build_id, binary_id}` (06 §6.4.5's one
  field list, which 05-updater's step-3 self-test uses verbatim): `app` the app ID (`Options.AppID`), `version` the
  release version (`main.version`), `commit` = `buildid.Commit()` (`vcs.revision`, `+dirty` when modified, `""` when
  absent), `ui_build_id` the embedded bundle's `<meta name="mediacore-build">` content or `"dev"` without a bundle,
  `binary_id` = `app.BinaryID` = `buildid.Binary(version)`.
- `--print-upgrade-floor`: prints the floor; empty until R3 (09 §9.4 R1).
- `core/app/binaryid.go`: `func BinaryID(opts Options) (string, error)` delegating to `buildid.Binary(opts.Version)`
  and adding nothing (06 §6.4.5).
- `X-App-Build` (emitted by stage 5 since R1-17) carries the UI build ID read from the embedded bundle's meta tag;
  no Go link step receives it (09 §9.4 R1).
- `.goreleaser.yml`: delete the dead `-X main.commit={{.Commit}}` and `-X main.date={{.Date}}` (lines 33–34 at
  `f7ac5b11e`); keep `-X main.version`.
- Deploy post-check: the `deploy` template in `Makefile.local.example` reads the running version with
  `ssh $(DEPLOY_HOST) $(DEPLOY_BIN) --version-json` and compares its `version` field, never `/health` or an HTTP
  route (HEAD's post-check calls `/api/v1/system/version`, which no route serves: `Makefile.local.example:103`;
  09 out-of-scope defect 5).
- The `docker-image` job turns on the build-ID probe R1-04 wired: set `PROBE_BUILD_ID=1` in `docker-image.yml`.

**SPA side**
- `web/vite.config.ts` generates the UI build ID by 07 §7.6.3's rule (03 §3.6): `BUILD_ID` when non-empty (must
  match `^[A-Za-z0-9._-]{1,64}$` and not be `dev` in any case, else `vite build` fails); else, in a clean checkout
  (`git status --porcelain --untracked-files=no` empty), `<short sha>-<commit unix time>`; else
  `<UTC timestamp>-<8 random hex>`. It writes the ID **only** into `index.html`'s
  `<meta name="mediacore-build" content="...">` (no `define`; FE6-1).
- `web/index.html` carries the meta placeholder the config fills.
- `web/src/components/StaleBuildBanner.tsx` (+ test): ABO's reload banner ("After an upgrade, open browser tabs
  offer to reload."), fed by `apiFetch`'s build-ID rule (07 §7.5.2's stale-build rule: compare `X-App-Build` of a
  response with the page's meta value; `dev` never triggers); mounted once in `web/src/App.tsx`.
- `web/src/utils/apiFetch.ts`: reads the page build ID once at load and applies the rule on every response.

## Tests this task owns (spec names)

- `TestVersionJSONFields` (06 §6.10): exactly the five fields; `commit` with `+dirty` for a modified tree and `""`
  without VCS data; passes 05-updater's self-test shape. In `version_json_test.go` at the root.
- `TestBuildID_HeaderEqualsEmbeddedMeta` (06 §6.10; 05-http-api §12): a server built with an embedded bundle answers
  `X-App-Build` equal to the bundle's meta content; the image job (R1-04's probe) asserts the same on the real image.
- `TestBinaryIDDistinguishesBuilds` (06 §6.10), the part R1 can prove: `app.BinaryID(opts)` equals
  `buildid.Binary(opts.Version)`; two binaries with different code have different IDs.
- vitest: `StaleBuildBanner.test.tsx` (banner hidden when IDs match or the page ID is `dev`; shown on mismatch);
  a vite-config unit test of the ID rule (each branch, and that `dev` and a bad `BUILD_ID` fail the build).

## Dependency check

R1-17, R1-29, R1-04 merged: `test -d core/httpapi/perimeter && ! grep -q react-router-dom web/package.json && test -f .github/workflows/docker-image.yml && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
sed -n 30,34p .goreleaser.yml                     # expect ldflags with -X main.version / main.commit / main.date
grep -n 'api/v1/system/version' Makefile.local.example    # expect 1 (line 103)
grep -n '^var version' main.go                    # expect 21: var version = "dev"
grep -n 'mediacore-build' web/index.html web/vite.config.ts | wc -l   # expect 0
test ! -e web/src/components/StaleBuildBanner.tsx && echo NEW          # expect NEW
grep -n 'PROBE_BUILD_ID' .github/workflows/docker-image.yml            # expect the R1-04 wiring, off
```

## Steps

1. Tests first (Go and vitest). 2. `core/app/binaryid.go`; the two fast exits in `main.go` (before any store or
config open; they need no credential). 3. Vite config, `index.html`, `apiFetch`, banner, `App.tsx`.
4. goreleaser, `Makefile.local.example`, `docker-image.yml` (`PROBE_BUILD_ID=1`). 5. Header bumps; fragment
`changelog.d/<today>_build_identity.md` with the R1 release-note lines: "After an upgrade, open browser tabs offer to
reload." and "`--version-json`, `--print-storage-levels` and `--print-upgrade-floor` print machine-readable build and
store facts." (`--print-storage-levels` is R1-23's; leave its words for R1-23 if it has not merged, and say so).
Commit(s): `feat(build): --version-json, --print-upgrade-floor and app.BinaryID`, `feat(web): UI build ID meta and the stale-build banner`.

## Code contract

`func BinaryID(opts Options) (string, error)` in `core/app`; `--version-json` prints exactly `{app, version, commit, ui_build_id, binary_id}`; `--print-upgrade-floor` prints nothing until R3.

## Acceptance

```bash
make build && ./audiobook-organizer --version-json | python3 -c 'import json,sys;d=json.load(sys.stdin);print(sorted(d))'   # expect ['app', 'binary_id', 'commit', 'ui_build_id', 'version']
./audiobook-organizer --print-upgrade-floor | wc -c                     # expect 0 or 1 (empty)
go test . -run TestVersionJSONFields -count=1 -v                        # expect PASS
go test ./... -run TestBuildID_HeaderEqualsEmbeddedMeta -count=1        # expect ok where the test lives
grep -c 'main.commit\|main.date' .goreleaser.yml                        # expect 0
npm test --prefix web -- --run StaleBuildBanner                         # expect pass
make ci && make -C core ci                                              # expect exit 0
```

## Out of scope

`--print-storage-levels` (R1-23); `@mediacore/api-client`'s `checkBuild`/`onStaleBuild` (R1-30, which then feeds
the same banner); the shell's banner (F9).

## Rollback

Revert the PR; the meta tag and header become absent and the banner never shows.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
