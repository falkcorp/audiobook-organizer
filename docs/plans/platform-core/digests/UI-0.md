<!-- file: docs/plans/platform-core/digests/UI-0.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c899d46-07d7-45e2-ab18-2a5a7d85885f -->
<!-- last-edited: 2026-11-04 -->

# Normative digest: UI-0, UI groundwork (release `UI0`)

Rules an implementer must follow. Sources are the spec files under `docs/specs/platform-core/`; a bare `§` is that
file's section. Counts are informative (measured at the evidence commit) and are regenerated at the stage's base commit.

## Goal

- Ships: (1) react-router-dom 7 to react-router 8 in ABO's `web/` as an import swap with the declarative
  `<BrowserRouter>` unchanged; (2) the npm workspace `core/web` holding `@mediacore/api-client` (fetch runtime plus
  generator) and nothing else; (3) a typed client generated from `core/openapi/core.json`, consumed by ABO through
  `file:`; (4) the stale-build rule exported as `checkBuild` and called by ABO's `apiFetch`; (5) the Docker frontend
  stage and CI entry points that build `core/web` before `web/`. (09 UI-0; 07 §7.19 steps 1-3)
- Why: the shell needs react-router 8, and S4-S6 must call native routes through the generated client, not
  hand-written code rewritten twice. (09 UI-0 Goal; 07 §7.8)
- Data, config keys, endpoints, permissions: none. Rollback: binary swap, because the UI is embedded per build.
- Release ID `UI0`. Trailer on every commit of every UI-0 PR: `Stage-Release: UI-0 1` (09 §9.2.1 "The trailer").
- No flip PR. UI-0 leaves `shipped_stage:` unchanged and is never a marker value. Its first PR sets `release:` to `UI0`.
- `UI0` soaks in parallel, off the serial chain: it is never another release's predecessor. The release that follows it
  waits for the row of the release `release:` named before `UI0`. (09 §9.2.1 "Soaks")

## Prerequisites

- Merged and soaked: `S2r3` at the earliest (the client is generated from the `core/openapi/core.json` that the S2
  hello skeleton emits; 09 §9.3). UI-0 lands directly after one of `S2r3`, `S2br1`, `S2br2`, `S3`, and always before `S4r1`.
- The `Stage-Release: UI-0 1` trailer needs the soak row of `S2r3` at the earliest, otherwise of the release `release:`
  names when the PR merges (check (b) of `scripts/ci/check_soak.py`, 09 §9.2.1).
- S0 delivered: `scripts/ci/build_web.sh`, `make web-deps`, the `.dockerignore` lines, the `ci.yml` classifier
  (`web=true` for `core/web/*` and `scripts/ci/build_web.sh`), Dependabot npm `/core/web`, and the `core/` module (09 S0
  "Web build entry points"; 07 §7.3.2). The Docker frontend-stage change lands in the UI-0 PR that creates `core/web`,
  because a `COPY` of `core/web/package.json` fails before that file exists.
- Owner checkpoint first: read `reusable-release.yml` at the SHA in `.github/ghcommon-ref.txt` and record what
  `frontend-enabled` does (see Owner checkpoints). The UI-0 PR may not merge before this record exists.
- Preconditions in the same PR series (09 UI-0 "Preconditions"; 07 §7.3.2): `ci.yml:127`, `frontend-ci.yml:64` and
  `nightly.yml:31` set `run-frontend: false`, with ABO-local frontend jobs that call `make web-deps`.

## Code moves and new packages

No Go package is created or moved. 03's Go layers (03 §3.5 R4) do not apply; the npm twin of rule R1 is the ESLint
import boundary below (07 §7.3.1). Rows follow 04 §8.1 row UI-0 and 07 §7.19.

| What | Where | Layer / note |
|---|---|---|
| react-router-dom 7 to react-router 8, import swap only | 52 source and 70 test files under `web/`, chunk regex `web/vite.config.ts:81-82`, `web/package.json` | ABO web; data router and test harness rewrite wait for S9 |
| `core/web` workspace root | `core/web/package.json` `{ "private": true, "workspaces": ["packages/*"] }`, own `package-lock.json` | npm; the `../examples/hello/web` member is added at S9 |
| `@mediacore/api-client` | `core/web/packages/api-client/` (runtime, generator, generated types, own minimal Vite library config, no preset) | npm; `ui-kit` and `shell` are created at S9 |
| `core/web/.gitignore` | `node_modules/`, `dist/`, `*.tsbuildinfo`, `coverage/`, `test-results/`, `playwright-report/`, `blob-report/` | travels into the split repo |
| ABO consumption | `web/package.json`: `"@mediacore/api-client": "file:../core/web/packages/api-client"`; `web/src/api/core.ts` creates the one pre-shell client | ABO web |
| Stale-build wiring | `web/src/utils/apiFetch.ts` calls `checkBuild`; `web/src/components/StaleBuildBanner.tsx` subscribes through `onStaleBuild` | ABO web |
| Build entry | `scripts/ci/build_web.sh deps` builds `core/web` first, then `npm ci` in `web/`; `make generate-api-client` | POSIX `sh`, under 20 lines |
| Docker frontend stage | `Dockerfile`, `Dockerfile.build-cgo` | steps below |
| CI and release callers | `ci.yml`, `frontend-ci.yml`, `nightly.yml`, `release-prod.yml`, `prerelease.yml` | see rules |
| ESLint import boundary | `core/web` `make ci` | no specifier may resolve outside `core/web` |

## Contracts

TypeScript, quoted from 07 §7.5.2 (owner text; 05-http-api §7 lists the same runtime middleware). UI-0 must export the
first five. `checkModules` (the shell's `/modules` fetch) and `createModuleClient` (books' client) have S9 consumers in
07 §7.8 and §7.5.2, so they are not UI-0 requirements.

```ts
// @mediacore/api-client
export type StaleReason = 'header-mismatch' | 'legacy-spa' | 'pre-s0-server'; // cases 1, 2, 3
export function readPageBuildId(): string; // the only reader of <meta name="mediacore-build">; "" when absent
export function checkBuild(response: Response, opts: { buildId: string; shell: boolean }): StaleReason | null; // headers only: cases 1, 3
export function onStaleBuild(listener: (reason: StaleReason) => void): () => void; // replays detected reasons; returns unsubscribe
export function getStaleReasons(): readonly StaleReason[];
export function createCoreClient(opts: { buildId: string; shell: boolean }): Client; // both required
export function checkModules(body: ModulesResponse, response: Response, opts: { buildId: string; shell: boolean }): StaleReason | null; // S9
export function createModuleClient<Paths>(core: Client): ModuleClient<Paths>; // S9+
```

Generation (07 §7.8; 05-http-api §7): types by `openapi-typescript` 7.13.0 and calls by `openapi-fetch` 0.17.0, read from
`core/openapi/core.json`. `make generate-api-client` failing is a CI failure; there is no fallback generator. Each
generated operation whose `x-mediacore-permission` is `public` is marked `skipAuthRedirect`. Errors are RFC 9457
`Problem`; the runtime keeps `apiFetch`'s deadline (`ApiTimeoutError`), SSO bounce detection (`ApiAuthRedirectError`),
`credentials: 'include'` and `X-Request-ID` surfacing.

## Behaviour rules

1. The router change is an import swap only. `<BrowserRouter>` stays; the data router and the `renderWithProviders`
   rewrite are S9 work. Nothing else in `web/` changes behaviour. (07 §7.19 step 1)
2. One stale-build implementation. `checkBuild` is synchronous, reads headers only and is never deleted. Both inputs are
   runtime values passed by the caller: `buildId` is read once at page load from `<meta name="mediacore-build">` and is
   never a define baked into the library build; ABO's pre-shell calls pass `shell: false`. ABO's `apiFetch` calls
   `checkBuild(response, { buildId: readPageBuildId(), shell: false })` for every legacy response. `onStaleBuild` replays
   reasons already detected to a late subscriber. (07 §7.5.2, §7.8)
3. Build order is an explicit list: `npm run build -w @mediacore/api-client` (until S9 the list is that one package).
   Never `--workspaces`. `build_web.sh` leaves a caller's `BUILD_ID` in the environment and generates none; `npm run
   build` in `web/` stamps the UI build ID itself. (07 §7.3.2)
4. Docker frontend stage, in order: (1) copy `core/web/package.json`, its lockfile and every member `package.json`, run
   `npm ci` in `core/web`; (2) copy the members' sources and run the ordered build; (3) copy `web/package.json` and its
   lockfile, run `npm ci` in `web/`; (4) copy `web/` and build. Only this stage declares `ARG BUILD_ID`; the Go stage
   takes none. (07 §7.3.2)
5. Every site that runs `npm ci` or `npm install` in `web/` (the twelve sites of 09 §9.1, plus the CLAUDE.md worktree
   recipe) calls `make web-deps`, or the script directly where `make` is unavailable. Caches list
   `core/web/package-lock.json`. (07 §7.3.2; 09 §9.1)
6. Release callers: both `pre-build-script`s become `sh scripts/ci/build_web.sh deps && cd web && npm run build`. If
   `reusable-release.yml`'s frontend half installs or builds `web/` itself, either set `frontend-enabled: false` and state
   where Node comes from for `pre-build-script`, or add a pre-install input to that workflow in the same PR as the
   `.github/ghcommon-ref.txt` bump (owner decides). (09 UI-0 "Preconditions")
7. A build leaves no untracked file: after `make web-deps && make web-build`, and after the rewritten
   `pre-build-script`, `git status --porcelain` is empty. The UI build ID's clean-checkout test is `git status
   --porcelain --untracked-files=no`. (07 §7.3.1, §7.6.3)
8. ESLint: `no-restricted-imports` plus `import/no-relative-packages` in `core/web` reject any specifier resolving
   outside `core/web` (no `../../web/...`, no `@abo/*`). It runs in `make ci` from UI-0. (07 §7.3.1)
9. The JSON field naming of native routes is the single rule of 05-http-api §4.3; the generated types follow it.
10. UI-0 changes no stored state and no boot path. R-test cells a and b are mandatory because it moves UI calls, and
    `compat-rehearsal` runs on the PR that changes `release:` (09 §9.2.1, §9.3). All owners stay at storage level 0.
11. Every PR of UI-0 passes `make ci`. A PR that adds a core route regenerates `core/openapi/core.json` in that PR (none
    at UI-0). (09 §9.3; 07 §7.8)

## Rollback costs

None. Binary swap; no row of 09 §9.2.1's table applies (09 §9.3 stage table, UI-0 row; 09 UI-0 Rollback). The previous
release's tabs and the new bundle follow the stale-build rule, which offers a reload.

## Done-when

Commands: `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts`; `make web-deps && make
web-build`; `make generate-api-client`; `make -C core ci`; `make test-all`; `make test-e2e`; `make ci`.

- the grep above finds nothing (09 UI-0; 07 §7.19 step 1);
- vitest and Playwright e2e green (09 UI-0);
- the generator produces a typed client from `core.json` in `core/web` CI (09 UI-0);
- every S0 build entry point builds `core/web` first: the image job is green with `core/web` present (09 UI-0);
- a dry-run RC build through `prerelease.yml` is green and leaves `git status --porcelain` empty after the rewritten
  `pre-build-script`; the same holds after `make web-deps && make web-build` locally (09 UI-0; 07 §7.18 "git status");
- the production-`dist/` e2e cell of `checkBuild` against the image: e2e `stale-build.spec.ts` shows no reload offered
  when the server's `X-App-Build` equals the page's meta and the reload offered when it differs (09 UI-0; 07 §7.18);
- also named by 07 §7.18 for this stage: `buildId.test.ts` (api-client runtime and ABO `apiFetch`),
  `runtime.errors.test.ts` (api-client), the ESLint import-boundary rule;
- R-test cells a and b (09 UI-0; 09 §9.10);
- the stage-wide checks of 09 §9.3: boot-budget A/B by `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir>
  --runs 5` appended to the stage record, and the R-test;
- the stage record `docs/plans/platform-core/stage-ledger/UI-0.md` holds the reads of the owner checkpoints and the ticks;
- soak row `UI0` in `ci/soak.txt` before the `S4r1` flip (09 §9.2.1 check (a)).

## Owner checkpoints

NOT AGENT WORK. No agent may complete these.

1. Read `reusable-release.yml` at the SHA in `.github/ghcommon-ref.txt`, record what `frontend-enabled` does, and decide
   between `frontend-enabled: false` and a pre-install input with a ref bump (09 §9.2.1 list of checkpoints; 09 UI-0).
2. Deploy a main build carrying UI-0 to the reference host; `scripts/soak_record.py --deployed` runs after the deploy.
3. After 14 days, run `scripts/soak_record.py --release UI0` from the operator's checkout and merge the row PR. For
   `UI0` a main binary of a later release before `S4r1` also counts toward `<since>`. Pass criteria: zero `restore_*`
   and boot errors, every owner at level 0 in the build and the store, a clean health read.
4. Run the boot-budget A/B on the scrubbed staging copy (`scripts/staging/provision.sh`, production archive).
5. Tick the stage record's done-check list before the row PR.

## Footnotes

Spec sections: 09 UI-0, §9.1, §9.2.1 ("Soaks", trailer, check_soak), §9.3, §9.4.1, §9.10; 07 §7.3.1, §7.3.2, §7.5.2,
§7.8, §7.18, §7.19; 04 §8.1; 03 §3.5; 05-http-api §4.3, §7.

Review IDs behind non-obvious rules:
- Router grep and the 122-file count: FE2-19. `core/web` holds only `api-client`: FE2-4. Client before identity moves:
  FE-12. Import boundary from UI-0: FE-16.
- Release callers and `frontend-enabled` unverified: MOD6-5. CI callers cannot call `build_web.sh`: MOD4-10.
- `core/web/.gitignore` and `git status` checks: FE7-1. Docker stage order and `file:` deps resolving to `dist/`: MOD3-13,
  MOD2-16. Explicit build order: MOD2-17, FE2-14.
- `checkBuild` signature and caller-supplied `buildId`: FE4-9, FE6-1, FE6-8, FE7-5; replay on subscribe: FE8-3;
  `api-client` needs no preset: FE8-1. Generated from `core.json`: MOD3-4.
- `UI0` ID, unordered soak and `Stage-Release: UI-0 1`: R11-4, FIN12-5, OPS6-8, OPS7-8.
