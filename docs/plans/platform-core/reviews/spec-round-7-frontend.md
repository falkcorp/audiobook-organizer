<!-- file: docs/plans/platform-core/reviews/spec-round-7-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: f996218c-5de7-4605-af26-8d32420cd362 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: FRONTEND SHELL lens (ID prefix FE7)

Scope: (A) regression check of every round-6 FE finding (FE6-1..FE6-8, all accepted, "reviewer's fix applied
as written"), against 07 v1.7.0, 05-http-api v1.7.0, 05-realtime v1.5.0, 06 v1.7.1, 09 v1.6.1, 12 v1.6.0,
13 v1.6.0, 03 v1.9.1, 04 v1.6.1, 05-updater v1.7.1, 05-storage v1.7.1 and 05-authn v1.7.1; (B) new defects
introduced by the revision, mostly cross-file. Code anchors are `path:line @748fd787a`. Spec anchors are
`file:line` in the current checkout. D1–D9, R3-*, R4-*, R5-*, R6-* and the FE4-1/FE4-2 dispositions (meta-only
UI build ID, no ldflags, no boot check; path-form invites until S9) are taken as given. Nothing here asks for
the removed dual-format machinery, a linked UI build ID or a boot-time build-ID comparison.

## Part A: regression check of round 6

| Round 6 | Status | Note |
|---|---|---|
| FE6-1 (MAJOR) library-baked build-ID define | **landed** | `checkBuild(response, { buildId, shell })` with `buildId` read once from the page's `mediacore-build` meta (07:628-643, 05-http-api:950-963, 03:669, 09:826, 09 UI-0 :911). The preset's `define` is `MEDIACORE_CORE_VERSION` only (07:1202-1207). The e2e cell `stale-build.spec.ts` runs a production `dist/` build and the image (07:1348, 09 UI-0 done :914). Residue: 09 S0 still says Vite writes the ID "into its `define`" (FE7-3). The one remaining define has the mirror-image problem under `vite serve`/vitest (FE7-4). How a detection reaches the UI, and who builds the pre-shell and module clients, is unspecified (FE7-5). |
| FE6-2 | landed | `MediacoreOptions { appId, themeStorageKey, defaultMode }` (07:1180-1188), the `mediacore-theme` meta and the boot script (07:990-997), `ShellConfig.theme` without `defaultMode` (07:405-409), and e2e `theme-boot.spec.ts` (07:1366). Residue: a second app-ID source, no key validation, and no missing-meta fallback (FE7-8). 09 S9 was not updated (FE7-9). |
| FE6-3 | landed | L0 leaf `buildid` (03:274, R4 :513, 04:843, 06:996-1023, 05-storage:991-995, 05-updater:283-297). Residue: the 09 S0 Moves row omits it (FE7-14). |
| FE6-4 | landed | One five-field list in 09:825, 09:1111, 05-updater:397-403, 06:1012-1022 and 06:1804. `commit` comes from `vcs.revision` (`+dirty`). goreleaser's dead `-X` lines are deleted (`.goreleaser.yml:33-34`, `main.go:20-21` verified). The P10 fallback covers only `""` (09:1146), not `+dirty` (FE7-2). |
| FE6-5 | **half** | The "S4 r2–S8" wording landed in 09:100, :151 and :944. The test name did not converge: 07:1362 and 05-authn:1529 say 09 S4 uses `TestLegacySPA_InviteFragmentReader`, but 09:948 names "`TestLegacyInviteLink_EndToEnd`'s fragment cell" (FE7-10). |
| FE6-6 | landed | `LegacyShellKey`, a seven-member union (07:399-402), and `Partial<Record<LegacyShellKey,string>>` (07:421). The test row is updated (07:1379). 09 S9 still says "eight" (FE7-9). |
| FE6-7 | landed | Non-empty, `^[A-Za-z0-9._-]{1,64}$`, else fail (07:931-935, 03:669, 09:826). The charset admits the reserved value `dev` (FE7-6). |
| FE6-8 | landed | No single-argument or `{ shell }`-only `checkBuild` remains outside history entries (07:1564, :1582 are history). |
| "Build sites" list (MOD6-5 in 07 §7.3.2) | landed | Checked against `748fd787a`. The seven building sites in 07:135-138 are `release-prod.yml:58`, `prerelease.yml:51`, `Dockerfile:21`, `Dockerfile.build-cgo:19`, `binary-smoke.yml:55`, `e2e.yml:184` and `Makefile:200`. They are exactly the sites that run `npm run build`, and the 12 `npm ci` sites of 09:74 match `git grep`. New defect on these paths: an untracked `core/web/node_modules` (FE7-1). |

## Part B: findings

| ID | Severity | Title |
|---|---|---|
| FE7-1 | MAJOR | No `.gitignore` covers `core/web/node_modules`. From UI-0 every build tree is dirty: every binary's `commit` gets `+dirty`, the "clean checkout" build-ID branch never fires, and goreleaser's dirty-tree validation may fail releases |
| FE7-2 | MINOR | P10 and the R-test use `--version-json`'s `commit` as `PREV_REF`, but `<sha>+dirty` is not a git ref, and the fallback covers only `""` |
| FE7-3 | MINOR | 09 S0 says Vite writes the UI build ID "into its `define` and the meta". 07 says the meta "and nowhere else". The S0→UI-0 `apiFetch` source is ambiguous |
| FE7-4 | MINOR | `MEDIACORE_CORE_VERSION` is defined only for the shell's library build. Under `vite serve`/vitest the app compiles shell `src`, so the define is `undefined` and the core-version row blocks every dev session |
| FE7-5 | MINOR | `checkBuild` has no output contract. ABO's pre-shell core client (S4–S8) and module clients have no specified `buildId`/`shell` source. "`apiFetch` passes … to `createCoreClient`" conflates two calls |
| FE7-6 | MINOR | `BUILD_ID=dev` passes the FE6-7 regex, which contradicts "`vite build` never writes `dev`" and silently disables the reload rule |
| FE7-7 | MINOR | `ui.FromFS` claims the `mediacore-build` meta marks a shell build, but ABO's pre-shell Vite config writes the same meta from S0 |
| FE7-8 | MINOR | The preset's `appId` is a second app-ID source next to `ShellConfig.app.id`, with no check. `themeStorageKey` is unvalidated but lands in a space-separated meta and a JS literal. `createShell` has no fallback when the theme meta is missing |
| FE7-9 | MINOR | 09 S9 still says "all eight keys" go through `legacyStorageKeys`. S9 Moves and Done-when omit the theme meta, the deletion of ABO's own boot script, and `theme-boot.spec.ts` |
| FE7-10 | MINOR | FE6-5 half-fixed: the fragment-cell test still has two names, and 07 and 05-authn misreport which name 09 uses |
| FE7-11 | MINOR | 07 says shell and ui-kit need no `@mui/icons-material` peer. 13 makes it a ui-kit peer, and 23 of 35 shell- and ui-kit-bound files import it |
| FE7-12 | MINOR | ui-kit is "presentational", but `ServerFileBrowser`/`PathLinks` import ABO's API and `PathLinks` imports the shell's toast. 07 and 12 also give different workspace build orders |
| FE7-13 | MINOR | `ui_build_id` means two things: `--version-json` gives the embedded bundle's meta or `dev`, while `/modules` gives `""` without a shell bundle. 05-updater merges them |
| FE7-14 | MINOR | 09 S0 Moves omits `buildid` (04 §8.1 S0 and 09's own Endpoints row include it). S0 Done-when lacks `TestVersionJSONFields` |

Counts: **0 BLOCKER, 1 MAJOR, 13 MINOR** (14 findings).

### FE7-1: MAJOR. Untracked `core/web/node_modules` makes every build tree dirty from UI-0

- **Where:**
  - 07 §7.3.1/§7.3.2 (`07-ui-shell.md:68-74`, `:112-120`): `npm ci` in `core/web`.
  - 07 §7.6.3 item 2 (`:936-937`): "in a clean git checkout, `<short sha>-<commit unix time>`, so a rebuild of
    one clean commit gets one ID".
  - 06 `BinaryID` comment (`06-app-sdk.md:1016-1018`): `commit` is `vcs.revision`, "suffixed `+dirty` when
    `vcs.modified` is set".
  - 09 S0 "Docker" row (`09-abo-on-core.md:819`): it adds `**/node_modules` and
    `core/examples/*/web/node_modules` to **`.dockerignore`**, but no file adds them to any `.gitignore`.
- **Evidence:**
  - **Nothing ignores the new directory.** The root `.gitignore @748fd787a` (380 lines) has no `node_modules`
    pattern. Only `web/.gitignore:1` ignores `node_modules`, and only below `web/`. No `core/web/.gitignore`
    is specified anywhere (`grep gitignore docs/specs/platform-core/*.md` finds none). `dist/`
    (`.gitignore:59`, unanchored) does cover `core/web/packages/*/dist`.
  - **Every build path creates it.** From UI-0, `make web-deps` → `build_web.sh deps` runs `npm ci` in
    `core/web` on every path: `make build`, both goreleaser `pre-build-script`s, Docker (no `.git`, so not
    affected), `binary-smoke.yml` and `e2e.yml`. That leaves an untracked `core/web/node_modules/` (and any
    non-hoisted `core/examples/hello/web/node_modules` from S9).
  - **`commit` is always `+dirty`.** Go's VCS stamping sets `vcs.modified=true` whenever `git status
    --porcelain` prints anything, untracked entries included. So from UI-0 every goreleaser and `make`
    binary reports `commit: "<sha>+dirty"`, and the field stops distinguishing a real dirty deploy from a
    clean one.
  - **Item 2 never fires.** If the Vite config's "clean" check counts untracked files, as `git status
    --porcelain` does, item 2 of the UI-build-ID rule never applies. Every build falls to the random item 3,
    and two `make build`s of one clean commit offer each other's tabs a reload. The spec does not say which
    "clean" test is meant.
  - **Releases may fail (unverified).** goreleaser refuses to release from a dirty tree unless validation is
    skipped. Whether `reusable-release.yml` skips it is unverifiable from this repository (MOD6-5's
    disposition), so this half is conditional. If validation is on, every RC and production release from
    UI-0 fails, on the very path the UI-0 dry-run RC done-check (`09:914`) is meant to cover.
- **Fix:**
  - **Ignore the outputs.** The UI-0 PR that creates `core/web` adds `core/web/.gitignore` (`node_modules/`,
    `dist/`, `*.tsbuildinfo`, `coverage/`, `test-results/`). It travels with `core/web` into the split repo's
    `web/` (12), so the root `.gitignore` is not enough. The S9 PR adds the same file to
    `core/examples/hello/web/`.
  - **Define "clean".** Item 2 becomes "otherwise, when `git status --porcelain --untracked-files=no` is
    empty, …", so a stray untracked file cannot change the ID rule. `vcs.modified` keeps Go's definition.
  - **Add a done-check.** UI-0 Done-when (09) and 07 §7.19 step 2 gain: after `make web-deps && make
    web-build`, `git status --porcelain` is empty; the dry-run RC asserts the same after the rewritten
    `pre-build-script`.

### FE7-2: MINOR. `PREV_REF` from a `+dirty` commit is not a git ref

- **Where:** 09 P10 (`09-abo-on-core.md:1146`): "its `commit` is the R-test's `PREV_REF` for the next flip;
  when `commit` is `""` …, its `version` (a tag) or a manual `PREV_REF`". R-test (`:1233`): "the operator
  overrides it with the commit P10 prints". 06:1016-1018 and 05-updater:399-400 define the `+dirty` suffix.
- **Evidence:**
  - **The deploy path is the dirty path.** Deploys build main from the operator's tree (`Makefile:9`
    already expects `--dirty`), and FE7-1 makes every build dirty from UI-0.
  - **`git checkout <sha>+dirty` fails.** The R-test then either errors or silently falls back to the default
    tag, rehearsing against the wrong `prev`.
  - **`version` is not always a tag.** A Docker build's `version` is `dev` (`Dockerfile:71`
    `ARG APP_VERSION=dev`), and a `make` build's is `git describe` output (`v…-<n>-g<hex>-dirty`).
- **Fix:**
  - **Strip the suffix.** P10 strips a trailing `+dirty` and records "dirty: the rehearsal builds the
    committed tree, not the deployed one" in the rollback inventory.
  - **State the fallback order:** `commit` without the suffix; else the `g<hex>` part of a describe-form
    `version`; else a manual `PREV_REF`. An R-test whose `PREV_REF` does not resolve fails loudly instead of
    defaulting.
  - **Add a test:** `scripts/tests/` covers the `+dirty` and `version=dev` cases.

### FE7-3: MINOR. 09 S0 still puts the UI build ID into a `define`

- **Where:** 09 S0 "Build ID and reload check" (`09-abo-on-core.md:826`): "Vite writes it into its `define` and
  the `<meta name="mediacore-build">` tag … The SPA's `apiFetch` offers a reload when `X-App-Build` differs
  from its build ID". 07 §7.6.3 (`07-ui-shell.md:945-948`): "writes it into `index.html` as `<meta …>`, and
  **nowhere else** … The client reads the meta once at page load". 07 §7.5.2 (`:637-639`).
- **Evidence:**
  - **Two sources for one stretch.** From S0 to UI-0 the 09 text implies `apiFetch` compares against ABO's
    own define, and from UI-0 against the meta.
  - **Not broken today:** ABO's define is evaluated in the same `vite build` as the meta. It is the drift
    FE6-1 closed, though: a second source that an implementation can evaluate twice. Under item 3, a rule
    evaluated twice yields two random IDs and a permanent reload offer.
  - **03 does not settle it.** 03:669 bans a build-ID define only in `core/web`, so the three files do not
    say one thing.
- **Fix:** In 09:826, replace "Vite writes it into its `define` and the `<meta …>` tag" with "the Vite config
  writes it into the `<meta name="mediacore-build">` tag only (07 §7.6.3)". Replace "differs from its build
  ID" with "differs from the build ID it read once from that meta at page load, from S0". Add "from S0" to
  07:637.

### FE7-4: MINOR. `MEDIACORE_CORE_VERSION` is undefined wherever shell sources are compiled by the app

- **Where:** 07 §7.5.2 (`07-ui-shell.md:605-609`): the version is "baked when the shell package itself is
  built in library mode". 07 §7.10 (`:1202-1207`): "`define`: `MEDIACORE_CORE_VERSION` only, … baked when the
  shell package is built in library mode". 07 §7.3.1 (`:76-80`): under `vite serve` and vitest, the
  `development` condition resolves shell **`src`**.
- **Evidence:**
  - **The app's Vite compiles the shell sources.** In `make web-dev` and in ABO's vitest (`renderInShell`),
    `import.meta.env.MEDIACORE_CORE_VERSION` inside shell `src` is replaced by the **app's** Vite, which per
    the text defines nothing for it.
  - **Vite leaves an undefined name undefined.** A non-`VITE_` `import.meta.env` name with no define is
    `undefined`. `major(undefined)` then hits the core-version row: a blocking "UI and server versions do not
    match" page with no module mounted, in every dev session and in every shell-mounting unit test.
  - **No test can see it.** Production is unaffected (`dist/` holds the literal), so the e2e and image cells
    pass. This is FE6-1's src/dist split in the opposite direction.
- **Fix:**
  - **Define it in app mode too.** In 07 §7.10, the preset defines `MEDIACORE_CORE_VERSION` in **both** modes:
    in library mode from the package's own `package.json`, and in app mode from the resolved
    `@mediacore/shell/package.json` `version`, which `check_core_version.py` already pins to
    `version.Version`. In app production builds the value is redundant with `dist/`'s literal and identical.
  - **Add a case** to `intersect.test.ts`: the core-version row reads a defined version under vitest.

### FE7-5: MINOR. `checkBuild`'s output and its other callers are unspecified

- **Where:**
  - 07 §7.5.2 (`07-ui-shell.md:628-650`): "`checkBuild` offers a reload when …"; "`createCoreClient({ buildId,
    shell })` … `createShell` passes …, ABO's `apiFetch` `{ buildId: <meta>, shell: false }`".
  - 07 §7.8 (`:1026-1033`).
  - 05-http-api §7 (`05-http-api.md:956-960`), with the same wording.
  - 07 §7.8 / 05-http-api §7: books' generated client "on the same runtime".
- **Evidence:**
  - **"Offers" has no defined effect.** Nothing says whether `checkBuild` returns a value, throws, or emits an
    event. Yet two different UIs must react to the same detection: ABO's pre-shell banner (S0–S8) and the
    shell banner (S9+). The runtime's own middleware (native calls) and `apiFetch` (legacy calls) must both
    reach whichever banner is mounted.
  - **The quoted sentence conflates two calls.** `apiFetch` never calls `createCoreClient`; it calls
    `checkBuild` per response.
  - **Two clients have no stated inputs.** The S4–S8 pre-shell bundle needs its own `createCoreClient` for
    the generated operations (07 §7.19 step 3), and the text gives it no `buildId`/`shell`. Neither does
    books' generated module client (S9+).
  - **The likely failure is silent.** If these options are optional, an omitted `buildId` reads as `""`,
    which "never triggers", so the check is disabled without a sound.
- **Fix:**
  - **Export one reader:** `readPageBuildId(): string` from `@mediacore/api-client`, the only meta reader
    ("" when absent).
  - **Define the effect:** `checkBuild(response, opts): StaleReason | null` plus
    `onStaleBuild(listener): Unsubscribe`, which fires once per page load per reason. The shell banner and
    ABO's pre-shell banner both subscribe.
  - **Make the options required:** `createCoreClient(opts: { buildId: string; shell: boolean })`.
  - **Name the module path:** module clients come from `createModuleClient<Paths>(coreClient)`, which
    inherits both values.
  - **Name ABO's pre-shell client:** one `createCoreClient({ buildId: readPageBuildId(), shell: false })` in
    `web/src/api/core.ts` from UI-0.
  - **Reword** "ABO's `apiFetch` `{…}`" as "ABO's `apiFetch` calls `checkBuild(response, { buildId:
    readPageBuildId(), shell: false })`".
  - **Extend `buildId.test.ts`:** a stale response on a native call and one on a legacy call each notify the
    same listener.

### FE7-6: MINOR. `BUILD_ID=dev` is accepted

- **Where:** 07 §7.6.3 item 1 (`07-ui-shell.md:931-935`), 03:669, 09:826. "`vite build` never does [write
  `dev`]" (`07:949-950`; 09:826 "`vite build` never writes `dev`").
- **Evidence:**
  - **`dev` passes the regex.** `dev` matches `^[A-Za-z0-9._-]{1,64}$`, so an operator or a CI variable
    setting `BUILD_ID=dev` produces a production bundle whose meta and `X-App-Build` are `dev`.
  - **That disables the rule.** "A `buildId` of `""` or `"dev"` never triggers it", so the reload rule is
    silently off for that artefact.
  - **The test does not cover the path.** `TestBuildID_HeaderEqualsEmbeddedMeta` asserts "not `dev`" only on
    the paths it builds.
- **Fix:** Item 1 adds: "`dev` (any case) is reserved and fails `vite build`". Add the case to
  `buildId.test.ts`.

### FE7-7: MINOR. The build meta no longer marks a shell bundle

- **Where:** 07 §7.6.1 `FromFS` comment (`07-ui-shell.md:704-706`): "It fails if index.html or the meta tag
  is missing: the meta tag is written by the shell preset, so only shell builds are Bundles". 07 §7.6.3
  (`:925-927`) and 09:826: ABO's own `vite.config.ts` writes the same `mediacore-build` meta from **S0**.
- **Evidence:**
  - **The stated reason is false.** From S0 every ABO legacy bundle carries the meta, so `FromFS` accepts a
    pre-shell SPA as a shell `Bundle`.
  - **The consequence.** Nothing passes one today: ABO passes the zero `Bundle` until S9. But the
    fail-closed claim ("only shell builds are Bundles", §7.13 row "Missing/invalid … build meta") rests on
    a discriminator that does not discriminate, and a premature `UIBundle()` would mount `ui.Handler` over
    a legacy SPA without an error.
- **Fix:** `FromFS` also requires `<meta name="mediacore-theme">`, which only the preset writes (07 §7.7).
  Reword the comment to "the build meta (any npm build) **and** the theme meta (only the shell preset)". Add
  a case to `TestBundle_MissingMeta` with a legacy-style `index.html` (build meta only).

### FE7-8: MINOR. Preset `appId` and theme-key hygiene

- **Where:** 07 §7.10 (`07-ui-shell.md:1180-1188`): `appId: string; // the app's ID, for storageKey()
  defaults`. 07 §7.4.2 (`:404`): `ShellConfig.app.id`, and `storageKey()` = `${appId}.${scope}.${name}`
  (`:441`). 07 §7.7 (`:990-997`): `<meta name="mediacore-theme" content="<key> <mode>">`. 07 §7.9.2:
  `BroadcastChannel('<appId>.realtime')`.
- **Evidence:**
  - **Three app IDs, unchecked.** The build-time `appId` (preset), the runtime `app.id` (`createShell`) and
    the server's `app.id` (`/modules`, `06-app-sdk.md:774`) are three sources and nothing compares them. If
    they differ, the default theme key lands in a different namespace from every other `storageKey()`
    preference.
  - **The key is unvalidated.** `themeStorageKey` is written into a space-separated `content` attribute and
    into an inline JS string literal (the boot script). A key containing a space, a quote or `</script>`
    breaks the meta parse or the script.
  - **No missing-meta behaviour.** Nothing says what `createShell` does without the theme meta, which is the
    case in `renderInShell`/vitest (jsdom) and in any hand-written `index.html`.
- **Fix:**
  - **Pick one app-ID source.** The preset writes `<meta name="mediacore-app" content="<appId>">`.
    `createShell` refuses to mount (console error and blocking page) when `config.app.id` differs from it,
    and admins get a `ui-diagnostics` kind when it differs from the server's `app.id`. Alternatively, drop
    `app.id` from `ShellConfig` and read the meta.
  - **Validate the key.** The preset validates `themeStorageKey` against `^[A-Za-z0-9._:-]{1,128}$` and
    `appId` against 03 §3.6's `[a-z][a-z0-9]{1,15}`, else it fails the config.
  - **Specify the fallback:** without the theme meta, `createShell` uses `storageKey('shell','themeMode')`
    and `'dark'`; `/testing` sets them explicitly.

### FE7-9: MINOR. 09 S9 is stale against FE6-2/FE6-6

- **Where:**
  - **09 S9 UI routes** (`09-abo-on-core.md:1020`): "the shell keeps the legacy names of the keys it reads
    through `createShell({ legacyStorageKeys })` — **all eight keys** of shell-bound files …". 07
    (`07-ui-shell.md:413-420`, `:999-1001`, §7.11 row, `:1379`) says seven through `legacyStorageKeys`, and
    `app-theme-mode` through the preset.
  - **09 S9 Moves** (`:1019`): "the preset writes the build-ID meta tag", with no theme meta or boot script.
    07 §7.19 step 4 (`07:1412-1413`) has both.
  - **09 S9 Done when** (`:1022`): no `theme-boot.spec.ts` (07:1366).
- **Evidence:**
  - **The two files disagree.** An implementer following 09 passes `themeMode` through `legacyStorageKeys`,
    which FE6-6's union now rejects at compile time.
  - **ABO's own script survives.** Nothing says ABO's hand-written inline script in `web/index.html:13-30`
    is deleted when the preset starts injecting one. Two boot scripts both run and are both CSP-hashed, so
    the old one silently remains the effective source if the two ever differ.
- **Fix:**
  - **UI routes:** "the seven keys through `createShell({ legacyStorageKeys })` (typed by `LegacyShellKey`)
    and `app-theme-mode` through the preset's `themeStorageKey`".
  - **Moves** adds: "the preset writes the theme meta and boot script from `mediacore({ appId: 'abo',
    themeStorageKey: 'app-theme-mode', defaultMode: 'dark' })`; the inline script in `web/index.html` is
    deleted in the same PR".
  - **Done when** adds `theme-boot.spec.ts`, and asserts that the built `index.html` has exactly one inline
    script.

### FE7-10: MINOR. FE6-5 half-fixed: the fragment-cell test still has two names

- **Where:**
  - 07:1362: `TestLegacySPA_InviteFragmentReader` "(the fragment cell …, under its own name, **which 09 S4
    and 05-authn §10 use**)".
  - 05-authn:1529: "the name 09 S4's done-check uses".
  - 09 S4 Done when (`09-abo-on-core.md:948`): "`TestLegacyInviteLink_EndToEnd`'s fragment cell (…)".
  - 09 history (`:1597`): "one test name (`TestLegacyInviteLink_EndToEnd` fragment cell)".
  - 07 history (`:1600`): the name `TestLegacySPA_InviteFragmentReader`.
- **Evidence:** `grep InviteFragmentReader 09-abo-on-core.md` finds nothing. 09 picked option (a) of FE6-5's
  fix, 07 and 05-authn picked option (b), and both claim the other file agrees.
- **Fix:** Replace 09:948's "`TestLegacyInviteLink_EndToEnd`'s fragment cell" with
  "`TestLegacySPA_InviteFragmentReader`". Correct 09's history line to match.

### FE7-11: MINOR. The icons peer: 07 says none, 13 says ui-kit, and the code imports icons in the moved files

- **Where:** 07 §7.3.1 Peers (`07-ui-shell.md:79-83`): "the shell imports nothing from `@mui/icons-material`
  and needs no icons peer (FE-26)". 13 §13.4 (`13-toolchain.md:171`): "`@mui/material / icons-material` …
  `^9.5.0` peer (`@mediacore/ui-kit`)".
- **Evidence:**
  - **The move list imports icons.** Of the 35 non-test files 07 §7.11 sends to shell or ui-kit (the
    shell-bound pages `Login`, `AcceptInvite`, `Setup`, `NotFound`, `Users`, `ActivityLog`, `System`,
    `Settings`, `Dashboard` and `Diagnostics`; the `layout`, `common`, `system` and `toast` component folders;
    `WelcomeWizard`, `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner` and
    `OperationTrackerProvider`), 23 import `@mui/icons-material @748fd787a`.
    Examples: `components/layout/TopBar.tsx`, `UserMenu.tsx`, `Sidebar.tsx`, `OperationsIndicator.tsx`,
    `pages/ActivityLog.tsx`, `pages/Users.tsx`, `components/common/ConfigurableTable.tsx:18`,
    `ServerFileBrowser.tsx:28-36`, `FilterTagBar.tsx:7` and `PathLinks.tsx:19`.
  - **FE-26's choice covers only the contributed icons.** Typing `NavItem.icon` as
    `ComponentType<SvgIconProps>` handles icons that modules contribute, not the shell's own.
  - **Without a declared dependency the build fails.** `core/web` does not install `@mui/icons-material`,
    so the S9 move fails, or each file has its icons rewritten, which nobody has planned.
- **Fix:** Make `@mui/icons-material ^9.5.0` a **peer** of both `@mediacore/ui-kit` and `@mediacore/shell`
  (apps already depend on it: `web/package.json:29`). Change 07:82-83 to "contributed icons are typed as
  `ComponentType<SvgIconProps>`; the shell's own icons come from the `@mui/icons-material` peer". Align 13's
  row to "peer of ui-kit and shell".

### FE7-12: MINOR. ui-kit-bound components reach the ABO API and the shell; the build orders differ

- **Where:**
  - 07 §7.11 (`07-ui-shell.md:1243`): "`components/common/*` (ConfigurableTable, ServerFileBrowser,
    PathLinks, BulkConfirmDialog, FilterTagBar) … ui-kit (presentational)"; `:1237` "uses ui-kit
    `ServerFileBrowser`".
  - Build order in 07 §7.3.2 (`:113-115`): `-w @mediacore/api-client -w @mediacore/ui-kit -w @mediacore/shell`.
  - Build order in 12 SC8 and §12.5 (`12-repo-split.md:259-261`, `:432`): `ui-kit`, `api-client`, `shell`.
- **Evidence:**
  - **Neither component is presentational.** `ServerFileBrowser.tsx:37,97` calls `api.browseFilesystem`
    (ABO `services/api`). `PathLinks.tsx:22-23` imports `getConfig` from ABO's API and `useToast` from
    `components/toast/ToastProvider`, which is shell-bound: a ui-kit → shell edge, while shell imports
    ui-kit.
  - **The browse route moves to core.** 05-library:739 makes the browse route core (`core:library.browse`).
    If ui-kit's `ServerFileBrowser` calls it through `@mediacore/api-client`, then ui-kit depends on
    api-client and 12's order (ui-kit first) builds against a missing `dist/`. If it does not, 07 should say
    so.
  - **The import rule only catches it at move time.** The boundary rule (07:96-101) catches ABO imports
    then, but it does not decide the shape.
- **Fix:**
  - **Pin ui-kit's graph:** ui-kit imports neither `@mediacore/api-client` nor `@mediacore/shell`, enforced by
    a `core/web` ESLint rule.
  - **Turn the calls into props.** `ServerFileBrowser` takes `browse: (path: string) => Promise<BrowseResult>`
    (the shell and books pass the generated core call). `PathLinks` takes `aliases` and `onCopied` props.
  - **Keep one order list,** owned by 07 §7.3.2, and have 12 cite it rather than restate it.

### FE7-13: MINOR. `ui_build_id` names two different values

- **Where:**
  - `--version-json` (06:1015): "`ui_build_id` is the embedded bundle's meta (… `dev` without one)". That is
    the legacy bundle's ID from S0 to S8.
  - `/api/core/v1/modules` (07:598-599, 05-http-api:846-848): "`""` when no shell bundle is served (zero
    `Bundle`, or a legacy mount owns `/`)".
  - 05-updater:290-293: "the UI bundle's build ID (07 §7.6.3; X-App-Build, ui_build_id), which is `dev` for
    every API-only build and every app without an embedded shell bundle".
- **Evidence:**
  - **One name, two meanings.** On an S0–S8 binary with an embedded legacy SPA, `--version-json` prints a
    real ID while `/modules` reports `""`. Case 2 of the stale rule depends on `/modules`' `""`.
  - **05-updater's sentence is wrong for both.** It is false for `/modules` (`""`, not `dev`) and for
    `--version-json` (a real ID when a legacy bundle is embedded).
  - **An operator or script cannot compare them.** One reading P10's output against `/modules` would misread
    the difference as a fault.
- **Fix:**
  - **06:1015:** "`ui_build_id` is the value `X-App-Build` reports: the embedded bundle's meta, legacy or
    shell, or `dev` without one; it is not `/modules`' `ui_build_id`, which is the shell bundle's only".
  - **05-updater:290-293:** "… (`X-App-Build`), which is `dev` for every API-only build".
  - **07 §7.5.1:** add the same one-line contrast.

### FE7-14: MINOR. 09 S0 omits the `buildid` leaf from Moves and `TestVersionJSONFields` from Done-when

- **Where:** 09 S0 Moves (`09-abo-on-core.md:817`): "04 §8.1 row S0: `core/` with `go.mod` …, the `archtest`
  engine and `storage/keys`". The 09 S0 Endpoints row (`:825`) says `buildid.Binary` "lands in this stage".
  04:843 lists `buildid` in S0. 06:1804 defines `TestVersionJSONFields` as an S0 test. 09 S0 Done when
  (`:830`) does not name it.
- **Fix:** In 09:817, add "`buildid` (L0 leaf: `buildid.Binary`, `buildid.Commit`; FE6-3)". In 09:830 add
  `TestVersionJSONFields`, and that the guard's `build` fields written at S0 equal `buildid.Binary(version)`
  (`TestS0LedgerKeysRoundTrip` already reads them).

## Not attacked (fixed decisions)

D1–D9, R3-1..R3-5, R4-1..R4-6, R5-1..R5-7 and R6-1..R6-7 are taken as given, as are the FE4-1 disposition
(meta-only UI build ID, no ldflags, no boot check) and the FE4-2/FE5-4 disposition (path-form invites until
S9, fragment reader from S4 r2). FE7-1 and FE7-2 work inside R5-7's binary ID and FE6-4's `commit` field.
FE7-3 to FE7-8 keep FE6-1's single source, the page's meta, and FE6-2's preset-owned theme key.
