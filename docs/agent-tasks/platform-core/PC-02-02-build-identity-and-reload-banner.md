<!-- file: docs/agent-tasks/platform-core/PC-02-02-build-identity-and-reload-banner.md -->
<!-- version: 1.0.0 -->
<!-- guid: d0755afd-7e5e-4781-b38a-2e72bccbfb71 -->
<!-- last-edited: 2026-11-04 -->

# PC-02-02 — Build identity: `--version-json`, `/health` version, `X-App-Build` from the embedded meta, boot log line, native-prefix redirect exclusion, and the SPA's build ID, reload banner and `apiFetch` rule

| Field | Value |
|---|---|
| ID | PC-02-02 |
| Tier | **T2 Sonnet/high.** Server and SPA halves of one rule; both are given exactly below, but a slip in either half makes every tab reload forever or never. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-02-01 (adds `database.SetBinaryID`, the `boot levels=` line and the fast-exit block in `main.go`) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §1 items 3–6, §4.1, §4.5, §4.6 last bullet, §5 rules 2–5 |
| Spec | 09 S0 rows "Endpoints", "Build ID and reload check", "Moves" (`StaleBuildBanner`); 07 §7.6.3 (the one build-ID rule); 05-http-api §4.2 stage 5; 09 §9.6 row 20; 06 test table row `TestVersionJSONFields` |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-02" -b platform-core/pc-02-02-build-identity origin/main
cd "$REPO/../audiobook-organizer-pc-02-02"
git rebase origin/main
git worktree list
make web-deps
```

Precondition: PC-02-01 is merged: `grep -n 'print-storage-levels' main.go` prints at least 1 line and
`grep -n 'func SetBinaryID' internal/database/storage_guard.go` prints 1 line. Otherwise STOP.

## Exact files

Create: `stage_embed.go`, `version_json_test.go`, `boot_log_test.go`, `internal/server/build_header_test.go`,
`internal/server/redirect_native_test.go`, `web/src/components/StaleBuildBanner.tsx`,
`web/src/components/StaleBuildBanner.test.tsx`, `web/src/utils/buildId.ts`, `web/src/utils/buildId.test.ts`.

Modify: `main.go`, `internal/server/server_lifecycle.go`, `internal/server/server_middleware.go`,
`internal/server/handlers/system/handler.go`, `.goreleaser.yml`, `.github/workflows/docker-image.yml`,
`web/vite.config.ts`, `web/index.html`, `web/src/utils/apiFetch.ts`, `web/src/App.tsx`.

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n '// Redirect /api/\* to /api/v1/\* for v1 compatibility' internal/server/server_lifecycle.go  # 1 hit (line 1790)
grep -n 'c.Redirect(http.StatusMovedPermanently, newPath)' internal/server/server_lifecycle.go       # 1 hit (line 1814)
grep -n 'func TestCollidingNamespacesStillRedirect' internal/server/wire_abs_routes_test.go           # 1 hit (line 267)
grep -n 'c.Header("Reporting-Endpoints"' internal/server/server_middleware.go                       # 1 hit (line 109)
grep -n 'cspReportGroup = "csp"' internal/server/csp_report.go                                      # 1 hit
grep -n 'resp\["ready"\] = ready' internal/server/handlers/system/handler.go                       # 1 hit (line 268)
grep -n 'appVersion func() string' internal/server/handlers/system/handler.go                       # 2 hits (field, New param)
grep -n -- '-X main.commit={{.Commit}}\|-X main.date={{.Date}}' .goreleaser.yml                     # 2 hits (lines 33-34)
grep -n 'assertNotAuthRedirect(url, response);' web/src/utils/apiFetch.ts                           # 2 hits
grep -n '<meta name="mediacore-build"' web/index.html                                               # 0 hits
grep -n '{!requiresLogin && <WelcomeWizard' web/src/App.tsx                                         # 1 hit
grep -n '//go:embed web/dist' web_embed.go                                                          # 1 hit
grep -n '^release: S0$' ci/stage.txt                                                                # 1 hit (created by PC-01-07)
```

The spec cites `server_lifecycle.go:1798` and `server_middleware.go:102-109` at `748fd787a`; at `51a2c2acd` the
redirect condition is lines 1799–1811 and the header block lines 102–110. A zero-hit edit target: STOP.

## Code contract

- `--version-json` (09 S0 "Endpoints"; 05-updater §5U.4 step 3): one JSON object, members in exactly this order,
  then a newline, exit 0, before cobra:
  ```go
  type versionJSON struct {
  	App       string `json:"app"`         // "abo" (06 §6.4 AppID example; ABO's app ID)
  	Version   string `json:"version"`     // main.version
  	Commit    string `json:"commit"`      // vcs.revision, + "+dirty" when vcs.modified == "true"; "" when absent
  	UIBuildID string `json:"ui_build_id"` // meta of the embedded bundle; "dev" when nothing is embedded
  	BinaryID  string `json:"binary_id"`   // buildid.Binary(version)
  }
  ```
  A `buildid.Binary` error prints the error to stderr and exits 1.
- `stage_embed.go` (package `main`): `//go:embed ci/stage.txt` into `var stageTxt string` and
  `func embeddedRelease() string` returning the value of the `release:` line (trimmed), `""` when absent.
- `main.go` adds `func uiBuildIDFromFS(fsys fs.FS) string` (name coined here): reads `web/dist/index.html`, returns the
  `content` of `<meta name="mediacore-build" content="...">`, or `"dev"` when the file or tag is missing.
- `internal/server`: `func SetUIBuildID(id string)` (name coined here), stored like `appVersion`; the middleware
  writes `X-App-Build: <id>` on every response, `dev` when unset.
- Boot log (09 §9.2.1 "Soaks"): in `run()`, after the fast exits and the child-mode check, before `executeCmd()`, one
  INFO record whose message is exactly `boot binary_id=<id> release=<embeddedRelease()>` (the whole text in the
  message, so every handler prints it verbatim). The second line `boot levels=books=0,core=0` comes from PC-02-01.
- Redirect exclusion regex, compiled once:
  `var nativeAPIPrefix = regexp.MustCompile(`^/api/(core|[a-z][a-z0-9]{1,15})/v[0-9]+(/|$)`)`, added to the redirect
  condition as `!nativeAPIPrefix.MatchString(path) &&` before `!absReservedPath(path)`.
- `/health`: `resp["version"] = h.appVersion()` when `h.appVersion != nil` (09 §9.6 row 20, a permanent field). Update
  the handler comment that says the response never carries a version.
- `web/src/utils/buildId.ts` (names coined here):
  ```ts
  export const BUILD_ID_RE = /^[A-Za-z0-9._-]{1,64}$/;
  export interface BuildIdInputs { env?: string; gitClean: boolean; shortSha?: string; commitUnix?: number; now: Date; randomHex8: string }
  export function computeBuildId(i: BuildIdInputs): string;     // 07 §7.6.3, below
  export function readPageBuildId(doc?: Document): string;      // meta content, read once; "dev" when absent
  export type StaleReason = 'header-differs' | 'pre-s0-server';
  export function checkBuild(response: Response): void;         // the reload rule, below
  export function onStaleBuild(fn: (r: StaleReason) => void): () => void; // replays reasons already seen
  ```
  `computeBuildId`: when `env` is non-empty it must match `BUILD_ID_RE` and must not equal `dev` in any case, else
  throw; an empty `env` falls through. Else when `gitClean` (empty `git status --porcelain --untracked-files=no`),
  `${shortSha}-${commitUnix}`. Else `${UTC timestamp}-${randomHex8}`, timestamp formatted `YYYYMMDDTHHMMSSZ`
  (format coined here). Never returns `dev`.
  `checkBuild`: let `h = response.headers.get('X-App-Build')`, `page = readPageBuildId()` (cached). If `page === 'dev'`
  do nothing. If `h !== null`: report `header-differs` when `h !== page && h !== 'dev'`. If `h === null` and the
  status is 2xx or 4xx and `Reporting-Endpoints` contains a `csp=` group, report `pre-s0-server`. A 502 or any
  response without both headers never counts.
- `web/vite.config.ts`: a plugin with `transformIndexHtml` that replaces `%MEDIACORE_BUILD%` in `index.html` with
  `computeBuildId(...)` evaluated once per `vite build` (git read through `child_process.execSync`, a failing git call
  counts as "not clean"). No `define`, no env var exposed to the bundle. `vite serve` writes `dev`.
- `web/index.html`: `<meta name="mediacore-build" content="%MEDIACORE_BUILD%" />` in `<head>`.

## TDD tests to write first

Spec-named: `TestVersionJSONFields` (`version_json_test.go`: exact member list and order; `commit` cases: revision,
`+dirty`, absent → `""`; `app == "abo"`; output decodes and `version` equals the injected version),
`TestBuildID_HeaderEqualsEmbeddedMeta` (`internal/server/build_header_test.go`: a server built with a fake FS whose
`index.html` meta is `abc-123` answers `X-App-Build: abc-123` on `/health`, on an SPA route and on a 404; with no
FS the header is `dev`), `TestBootLogsBinaryID` (`boot_log_test.go`: with `executeCmd` stubbed and the default
logger pointed at a buffer, `run()` logs `boot binary_id=<buildid.Binary(version)> release=S0` before `executeCmd`
runs; `database.InitializeStore("pebble", t.TempDir())` then logs `boot levels=books=0,core=0`),
`TestNativePrefixesNotRedirected` (`redirect_native_test.go`: `/api/core/v1/ping`, `/api/books/v2/x`, `/api/tv/v1`
answer 404, not 301; `/api/audiobooks` still 301s to `/api/v1/audiobooks`), `TestCollidingNamespacesStillRedirect`
(existing, unchanged, must stay green).

New: `TestHealthReportsVersion` (in `build_header_test.go`); `buildId.test.ts` (the 07 §7.6.3 cases: valid env,
env with a space throws, 65-char env throws, `dev`/`DEV`/`Dev` throw, empty env falls through, clean tree →
`<sha>-<unix>`, dirty tree → timestamp + 8 hex, never `dev`; `checkBuild` cases: differing header reports,
equal header and `dev` header do not, missing header with `Reporting-Endpoints: csp="…"` on 200 and 404 reports,
on 502 does not, page ID `dev` never reports); `StaleBuildBanner.test.tsx` (hidden by default, visible after a
reported reason, the Reload button calls `window.location.reload`).

## Steps

1. Re-verify. Write the failing Go tests, then `stage_embed.go`, `uiBuildIDFromFS`, `--version-json`, the boot line
   in `main.go` (keep PC-02-01's lines).
2. `internal/server`: `SetUIBuildID`, header in `securityHeadersMiddleware` (it runs on every response), the regex
   exclusion, then `/health` version.
3. `.goreleaser.yml`: delete the two lines `-X main.commit={{.Commit}}` and `-X main.date={{.Date}}` (09 S0 "Endpoints").
4. Frontend: `buildId.ts` and its tests; the vite plugin; the meta tag; in `apiFetch.ts` call `checkBuild(response)`
   right after each of the two `assertNotAuthRedirect(url, response);` lines; `StaleBuildBanner.tsx` (an MUI
   `Alert` with text "A new version is available." and a "Reload" button, subscribed through `onStaleBuild`);
   mount `<StaleBuildBanner />` in `App.tsx` on the line before `{!requiresLogin && <WelcomeWizard`.
5. `docker-image.yml`: after the ready wait, fetch `/` and the response headers from the container; assert the
   `X-App-Build` value equals the meta `content` and is neither empty nor `dev`.
6. Header every file; add a `changelog.d/` fragment with the release-note lines 3–6 of 09 S0 "User-visible changes".

## Acceptance

```bash
go test . ./internal/server/ -run 'TestVersionJSONFields|TestBootLogsBinaryID|TestBuildID_HeaderEqualsEmbeddedMeta|TestNativePrefixesNotRedirected|TestCollidingNamespacesStillRedirect|TestHealthReportsVersion' -count=1
                                                    # ok  github.com/falkcorp/audiobook-organizer
                                                    # ok  github.com/falkcorp/audiobook-organizer/internal/server
go build -o /tmp/abo . && /tmp/abo --version-json | python3 -c 'import json,sys;print(list(json.load(sys.stdin)))'
                                                    # ['app', 'version', 'commit', 'ui_build_id', 'binary_id']
grep -c 'main.commit\|main.date' .goreleaser.yml    # 0
(cd web && npx vitest run src/utils/buildId.test.ts src/components/StaleBuildBanner.test.tsx)   # all tests pass
(cd web && npm run build) && grep -o 'name="mediacore-build" content="[^"]*"' web/dist/index.html
                                                    # a value that is neither empty nor dev
make ci; echo "exit=$?"                             # exit=0
```

On a running server: `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` prints `404`.
The `docker-image` check is green with the header-equals-meta step.

## Out of scope

`@mediacore/api-client` (UI-0); the shell banner (S9); `app.BinaryID` (S2b); `X-App-Build` from the core perimeter
(S2); the updater's self-test (S8).

## Rollback

Revert the PR. The header, the field and the flag are additive; an older SPA ignores the header.

## Repo rules

- Worktree only; never `go work init`. Headers on every new or changed file; one `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- `gh pr merge <n> --rebase` after the 09 §9.2.1 required checks pass on an up-to-date branch; never squash or `--admin`.
- Concurrency (CLAUDE.md): no library-scale loop is added.
