<!-- file: docs/plans/platform-core/reviews/spec-round-8-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b694307-70d4-4887-a977-073ab460c240 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: FRONTEND SHELL lens (ID prefix FE8)

Scope: (A) a regression check of the round-7 FE findings (FE7-1 MAJOR, FE7-2..FE7-14 MINOR, all
accepted "as written") against 07 v1.8.0, 05-http-api v1.8.1, 05-realtime v1.5.0, 06 v1.8.1, 09 v1.7.0,
12 v1.7.1, 13 v1.7.0, 03 v1.10.1 and 05-updater v1.8.1; (B) new defects the round-7 revision introduced,
and cross-file consistency against `web/` at `748fd787a`. Spec anchors are `file:line` in the current
checkout, and code anchors are `path:line @748fd787a`.

Taken as given: D1–D9, R3-*..R7-*, FE4-1 (meta-only UI build ID, no ldflags, no boot check), FE4-2/FE5-4
(path-form invites until S9) and FE6-1 (runtime `buildId`). Nothing here asks for removed machinery.

## Part A: regression check of round 7

| Round 7 | Status | Note |
|---|---|---|
| FE7-1 (MAJOR) `.gitignore`, "clean" | **landed, residue** | `core/web/.gitignore` is in 07:61 and :74-82, 09 UI-0 :1007, 12 SC4 :183-186 and 06:1028. Hello's file is in 07:1492 and 09 S9 :1115. "Clean" means `--untracked-files=no` (07:1001-1003, 03:739). The done-check is in 07:81-82, :1482-1484 and :1460, and in 09 UI-0 Done-when. Residue: core's other build outputs are still unignored, 12's SC4 asserts `git status` in a directory that is not a git checkout, and 09 S0 quotes the rule without the definition (FE8-4). |
| FE7-2 `PREV_REF` | landed | 09 P10 :1242 strips `+dirty` and gives the fallback order; the R-test (:1333) fails loudly; 06:1033-1036 agrees. |
| FE7-3 define in 09 S0 | landed | 09:921 says "the meta tag only" and "read once from that meta at page load, from S0"; 07:688-691 agrees. |
| FE7-4 `MEDIACORE_CORE_VERSION` | **landed, but rests on a broken premise** | 07:633-642 and :1281-1284 define it in both modes, and `intersect.test.ts` has the case. The library-mode half assumes the shell package is built with `mediacore(options)`, which as specified cannot load or configure a library build (FE8-1). |
| FE7-5 `checkBuild` contract | **landed, introduces two defects** | 07:666-698, 05-http-api:953-973. The synchronous signature cannot evaluate case 2 (FE8-2). `onStaleBuild` has no replay, and no stage builds the "pre-shell banner" (FE8-3). |
| FE7-6 `BUILD_ID=dev` | landed | 07:995-997, 03:739, 09:921, test row 07:1430. |
| FE7-7 shell bundle meta | landed | 07:759-770, :1343-1346, :1369, `TestBundle_MissingMeta`. New ordering hazard inside the S9 PR series (FE8-8). |
| FE7-8 app ID and theme key | **landed, residue** | 07:423-426, :1254, :1261-1267, :1059-1066, `createShell.meta.test.tsx`. Missing-meta behaviour for `mediacore-app` is unspecified, and `renderInShell` has no option for the app ID it "sets explicitly" (FE8-7). |
| FE7-9 09 S9 stale | landed, residue | 09 S9 UI routes say seven keys plus `themeStorageKey`, Moves deletes `web/index.html:13-30`, and Done-when has `theme-boot.spec.ts`. 09 S9 omits the `mediacore-app` meta and its test (FE8-8). |
| FE7-10 test name | landed | 09:1044 and :1327 use `TestLegacySPA_InviteFragmentReader`; 07:1445 and 05-authn:1609 agree. |
| FE7-11 icons peer | landed | 07:91-100 and 13 §13.4 (peer of ui-kit **and** shell). Re-checked: the shell-bound set imports only `react`, `@mui/material`, `@mui/icons-material`, `react-router-dom` and `zustand` @748fd787a. |
| FE7-12 ui-kit graph, build order | **half** | The order list is single (07:132-137, 12 SC8 :259-267, §12.5 step 6). The ui-kit cut covers one of `ServerFileBrowser`'s three ABO calls and none of `PathLinks`' ABO utilities (FE8-5). ui-kit's own build imports the shell's preset (FE8-1). |
| FE7-13 `ui_build_id` | landed | 06:1023-1026, 07:623-627, 05-http-api:849-852, 05-updater:308-313, 03:325. |
| FE7-14 09 S0 | landed | 09 S0 Moves names `buildid`; Done-when names `TestVersionJSONFields`. |

## Part B: findings

| ID | Severity | Title |
|---|---|---|
| FE8-1 | MAJOR | `mediacore(options)` cannot build the core packages: it needs an `appId`, writes app-only HTML, and lives in the shell's unbuilt `dist/`. The FE7-4 library-mode define and the FE7-12 graph rest on it |
| FE8-2 | MAJOR | `checkBuild(response, opts): StaleReason \| null` is synchronous and header-only, but case 2 needs the `/modules` JSON body, so the S9→S8 rollback reload (FE3-3) cannot be implemented to the stated contract |
| FE8-3 | MINOR | `onStaleBuild` fires once per page load with no replay, so a banner that subscribes after the first stale response never shows. No stage builds ABO's "pre-shell banner", and from S0 to UI-0 there is no `onStaleBuild` to subscribe to |
| FE8-4 | MINOR | FE7-1 residue: core's other outputs (hello's binary, `core/.ci/readiness/`, Playwright's report) are not ignored, SC4(a) asserts `git status` in a `git archive` extract, and 09 S0 quotes the "clean" rule without its definition |
| FE8-5 | MINOR | FE7-12 half-fixed: `ServerFileBrowser` still calls ABO's include/exclude API, `PathLinks` still needs ABO's `pathAliases`/`formatPath` (which import `services/api`), and ui-kit's `useKeyboardShortcuts` hard-codes `/library` |
| FE8-6 | MINOR | §7.11 sends `MaintenanceTab` and `ThrottlePanel` to the shell. §7.8 and 05-http-api §7 call them books pages that keep legacy `ops.ts` calls after S9. The "16 non-test importers" of `operationPolling.ts` are 5 |
| FE8-7 | MINOR | FE7-8 residue: no behaviour when the `mediacore-app` meta is missing, no `RenderInShellOptions` field for the app ID or theme key that `/testing` "sets explicitly", and the `appId` pattern is unanchored |
| FE8-8 | MINOR | 09 S9 omits the `mediacore-app` meta and `createShell.meta.test.tsx`. Neither file orders `UIBundle()` after the preset adoption, and since FE7-7 that order is required or the embed build panics |
| FE8-9 | MINOR | 07 and 05-realtime disagree on the stream control vocabulary: 07 uses a "`resync` frame" and close reasons `credential`/`error`, while 05-realtime has `core.stream.reset.v1` and `credential_revoked`/`permission_revoked` |
| FE8-10 | MINOR | Name residue: 05-http-api gives `opsgen`'s output as `web/src/services/ops.ts` (it is `web/src/generated/ops.ts`), and the no-argument `createCoreClient()` survives in 05-http-api:951 and 07:1096 after FE7-5 made its options required |

Counts: **0 BLOCKER, 2 MAJOR, 8 MINOR** (10 findings).

### FE8-1: MAJOR. The preset cannot build the core packages it claims to build

- **Where:**
  - 07 §7.10 (`07-ui-shell.md:1248-1250`): `mediacore(options)` returns "the Vite plugin list and config
    every app **and every core package** uses".
  - 07:1271-1272: "the same preset builds the core packages in library mode (§7.3.1)".
  - 07:1254: `appId: string` is **required** (FE7-8).
  - 07:1281-1284: the library-mode half of `MEDIACORE_CORE_VERSION` is "from the package's own
    `package.json`" (FE7-4).
  - 07:62-64: the preset ships as `@mediacore/shell/vite`.
  - 07:85-86: every package's `exports` has `"development": "./src/index.ts"` and `"default"` → `dist/`.
  - 07:132-137: build order `api-client`, `ui-kit`, `shell`.
  - 07:114-116: "ui-kit imports neither `@mediacore/api-client` nor `@mediacore/shell`".
- **Evidence:**
  - **The configuration does not fit a library.** A library build has no `index.html`, yet the preset
    writes the build meta, the `mediacore-app` meta, the theme meta and the boot script. It also demands
    an `appId`, so `api-client`, `ui-kit` and `shell` would each have to invent one. 03 §3.6 reserves app
    IDs for apps.
  - **The preset cannot be loaded when it is needed.** Vite loads `vite.config.ts` through Node module
    resolution, not through its own `resolve.conditions`, so `@mediacore/shell/vite` resolves through
    `"default"` to the shell's `dist/`. That fails three times:
    - at UI-0 the shell package does not exist (07:63, created at S9), yet `api-client` is built at UI-0;
    - `ui-kit` is built before `shell` (the one order list), so its config imports an unbuilt `dist/`;
    - `shell` itself would import its own unbuilt `dist/` (a bootstrap cycle).
  - **ui-kit gains a hidden build-time edge.** Its config imports `@mediacore/shell`, which contradicts
    the FE7-12 graph rule in spirit and the order list in practice.
  - **FE7-4 inherits the gap.** Its library-mode define is only as real as the shell's own build running
    this preset. Nothing else in the spec defines it for the library build, so `dist/` would carry
    `undefined` and every production tab would hit the core-version row.
- **Fix:**
  - **Split the preset.** `@mediacore/shell/vite` exports two functions:
    - `mediacore(options)` for apps: the HTML metas, the boot script, `appId`, and the app-mode define;
    - `mediacoreLibrary({ name })` for the three packages: the React Compiler, `dedupe`, library mode,
      and, for `shell` only, the library-mode `MEDIACORE_CORE_VERSION`. It writes no HTML and needs no
      `appId`.
  - **Make the preset loadable before any build.** The `./vite` export points at an **unbuilt** plain-ESM
    file, `"./vite": "./vite/index.mjs"`, which is shipped as-is, has no TypeScript, and is outside
    `dist/`. Alternatively, keep `core/web/build/preset.mjs` as a workspace-internal file that the three
    package configs import by relative path, with `@mediacore/shell/vite` re-exporting it for apps.
  - **Define UI-0's config.** State that `api-client`'s UI-0 Vite config uses `mediacoreLibrary` (or its
    own minimal config) before `shell` exists.
  - **Exempt the preset file from the graph rule.** ui-kit's package code still imports neither
    `api-client` nor `shell`; its build config may import only the preset file. The ESLint rule's scope
    is `packages/*/src`.
  - **Add a test.** `check-singletons.mjs` or a new `presets.test.mjs` builds the three packages from a
    clean `core/web` (no `dist/`) in the 07 §7.3.2 order and asserts that `shell/dist` contains the
    `MEDIACORE_CORE_VERSION` literal.

### FE8-2: MAJOR. Case 2 cannot be evaluated by the stated `checkBuild` signature

- **Where:**
  - 07 §7.5.2 (`07-ui-shell.md:670`): `checkBuild(response: Response, opts): StaleReason | null`, which
    "returns the case it detected" (:676).
  - Case 2 (:705-709): "the response is `/api/core/v1/modules`, `app.ui_build_id` is `""` and
    `X-App-Build` is non-empty …".
  - 05-http-api §7 (`05-http-api.md:954-955`, :970-972): the same synchronous contract.
  - 09 S9 Rollback (`09-abo-on-core.md:1117`): the S9→S8 reload rests on case 2.
- **Evidence:**
  - **Case 2 needs the body.** `app.ui_build_id` is in the JSON body. Reading it from a `Response` is
    asynchronous (`response.json()` or a `clone()` first), and it consumes the stream that the generated
    client also needs. A synchronous `StaleReason | null` cannot return `'legacy-spa'`.
  - **The likely implementations both fail.** One returns `null` for case 2, which silently drops the
    FE3-3/OPS3-11 guarantee; the R-test row "S9 tab, roll back to S8, reload offered" (07:1433) would go
    red with no spec-conformant way to turn it green. The other reads the body inside `checkBuild` and
    breaks either the signature or the caller's body.
  - **Introduced by FE7-5's contract.** The round-6 text had no return value, so this did not arise.
- **Fix (pick one and state it in 07 §7.5.2 and 05-http-api §7):**
  - **(a) Header-only.** httpapi stage 5 also writes `X-App-UI: shell|legacy|none` on every response
    (`shell` when `ui.Handler` owns `/`, `legacy` when a `ServesSPA` mount does), so case 2 becomes
    "`shell: true` and `X-App-UI: legacy`". `checkBuild` stays synchronous and needs no special-casing
    of `/modules`. Add the header to `TestPerimeter_XAppBuildOnEveryResponse`.
  - **(b) Keep the body rule.** Split the call: `checkBuild(response, opts)` handles cases 1 and 3, and
    `checkModules(body: ModulesResponse, response, opts): StaleReason | null` is called by the shell's
    `/modules` fetch after it parses the body. Both notify `onStaleBuild`.
  - **Test.** Extend `buildId.test.ts`: the case-2 path detects `legacy-spa` without consuming the body
    the caller reads.

### FE8-3: MINOR. `onStaleBuild` loses early detections, and the pre-shell banner has no owner

- **Where:**
  - 07:676-679: `onStaleBuild` fires "once per page load per reason"; "the shell banner (S9+) and ABO's
    pre-shell banner (S0–S8) both subscribe".
  - 05-http-api:970-972: the same.
  - 09 S0 "Build ID and reload check" (`09-abo-on-core.md:921`): "the SPA's `apiFetch` offers a reload",
    with no component named. `grep banner 09-abo-on-core.md` finds no reload banner in any stage.
- **Evidence:**
  - **There is no replay.** The first API calls of a page load are the auth probe (`AuthContext`,
    `web/src/contexts/AuthContext.tsx:90-91` @748fd787a) and, in the shell, `/auth/status` and `/me`
    (07 §7.12). Both run before a layout-mounted banner's effect subscribes. If that first stale response
    fires the one notification, a later subscriber never hears it, and the banner never appears for that
    page load.
  - **The S0–UI-0 stretch has nothing to subscribe to.** `@mediacore/api-client` does not exist before
    UI-0, so the "pre-shell banner (S0–S8)" cannot subscribe to `onStaleBuild` before UI-0. HEAD has no
    reload UI, and no stage creates one.
- **Fix:**
  - **Replay on subscribe.** `onStaleBuild` calls a new listener immediately for every reason already
    detected on this page load (state kept in the runtime). Add `getStaleReasons(): readonly
    StaleReason[]`. The "once" applies per listener per reason.
  - **Assign the banner.** 09 S0 Moves adds ABO's reload banner (`web/src/components/StaleBuildBanner.tsx`,
    mounted above the routes, fed by `apiFetch`'s own rule). From UI-0 the same component subscribes to
    `onStaleBuild`, and at S9 it is replaced by the shell banner.
  - **Test.** `buildId.test.ts` adds a listener that subscribes after a stale response and is still
    notified once.

### FE8-4: MINOR. FE7-1 residue: only `core/web` is covered, and SC4's check cannot run

- **Where:**
  - 07:74-82 and 12 SC4(a) (`12-repo-split.md:183-193`): "the run leaves `git status --porcelain` empty
    there", run after `git archive HEAD core | tar -x -C "$TMP"`.
  - 12:312 and :712: `readiness` writes to `core/.ci/readiness/`, described as "gitignored".
  - 06:2572: the hello target runs `go build -tags embed_frontend ./examples/hello/cmd/hello` with no
    `-o`.
  - 09 S0 :921: "in a clean checkout", claimed as a quote of 07's rule but without the
    `--untracked-files=no` definition.
- **Evidence:**
  - **No file ignores these outputs.** The root `.gitignore @748fd787a` has no `.ci/readiness`, `hello`
    or `**/playwright-report` pattern (only `/playwright-report/` and `web/tests/e2e/playwright-report/`,
    lines 278-283). `core/web/.gitignore` lists `test-results/` but not `playwright-report/` or
    `blob-report/`, which `core/web/e2e` writes. No spec file adds a `core/.gitignore`.
  - **A local core run dirties the tree.** After `make readiness`, a hello build or a core e2e run,
    every later ABO build has `vcs.modified` set (`commit` gets `+dirty`), which is the FE7-1 failure
    mode. After the split none of these is ignored at all, because the root file does not travel.
  - **SC4's check cannot run.** `$TMP` from `git archive | tar` is not a git work tree, so `git status`
    there errors instead of passing.
- **Fix:**
  - **Add `core/.gitignore`** in the S0 PR that creates `core/`: `/.ci/readiness/`, `/hello` (or give
    the hello target `-o $(CORE_BIN)/hello` under an ignored directory), `**/playwright-report/` and
    `**/blob-report/`. Add the two Playwright entries to `core/web/.gitignore` too.
  - **Make SC4 checkable.** SC4(a) creates a checkout instead of an archive (`git worktree add --detach
    "$TMP" HEAD` and then works in `$TMP/core`), or runs `git init && git add -A && git commit` in
    `$TMP/core` before the run, and then asserts `git status --porcelain` is empty.
  - **09 S0 :921:** replace "in a clean checkout" with "when `git status --porcelain
    --untracked-files=no` is empty (07 §7.6.3)".

### FE8-5: MINOR. FE7-12 half-fixed: the ui-kit set still reaches ABO code and routes

- **Where:**
  - 07 §7.11 (`07-ui-shell.md:1325`): `ServerFileBrowser` "takes `browse: …` instead"; `PathLinks`
    "takes `aliases` and `onCopied` props instead"; "ui-kit then imports neither …".
  - 07:1326: hooks `useAsyncAction`, `useKeyboardShortcuts`, `useDebouncedSearch`, `useRowSelection` →
    ui-kit.
- **Evidence (@748fd787a):**
  - **`ServerFileBrowser` makes two more ABO calls.** `ServerFileBrowser.tsx:176,182` call
    `api.includeFilesystemPath` and `api.excludeFilesystemPath` (books' scan exclusion), and its state is
    typed with `api.FileSystemItem` and `api.FilesystemBrowseResult` (:74, :77). A `browse` prop alone
    leaves two ABO calls and the ABO types in a ui-kit file.
  - **`PathLinks` keeps ABO imports after the cut.** `PathLinks.tsx:20-21` still imports `renderPath`
    (`utils/pathAliases.ts`, which imports `PathAlias` from `services/api`) and `PathVar`
    (`utils/formatPath.ts`, which imports `getConfig` from `services/api` and derives the books-specific
    `libroot`/`books` variables). Its only consumers are books review components (`FolderFilesChip`,
    `BookInfoPanel` and the `*Spine` components).
  - **`useKeyboardShortcuts` names a books route.** `useKeyboardShortcuts.ts:16,75` navigates to
    `/library` (the "g l" shortcut, "Go to Library"), which becomes `/books/library` at S9, so a ui-kit
    hook would hard-code a books route.
  - **The rule catches this only at the S9 move.** The ESLint rule rejects these at the S9 move PR,
    but the spec says the cut is done.
- **Fix:**
  - **`PathLinks` is books UI.** Move it to `web/src/modules/books/components/PathLinks.tsx` with
    `pathAliases`/`formatPath`, and remove it from the ui-kit list.
  - **Pass all three file-browser operations as props.** `ServerFileBrowser` takes
    `{ browse, include?, exclude? }`; without `include`/`exclude` the context menu hides. ui-kit declares
    its own `BrowseEntry`/`BrowseResult` types, structurally assignable from the generated core types.
    A type import from `@mediacore/api-client` would also break the rule.
  - **Contributed shortcuts.** `useKeyboardShortcuts` takes its shortcut table (`SHORTCUTS`, :14-19) from
    the shell, which builds it from shell routes plus a new `shortcuts?` contribution (or books' first
    nav entry for "g l"). Add the slot to §7.4.2 or say the shortcut is dropped.

### FE8-6: MINOR. `MaintenanceTab` and `ThrottlePanel` are classified twice, oppositely

- **Where:**
  - 07 §7.11 (`07-ui-shell.md:1315`): `pages/System.tsx` "(+ `components/system` Storage, Quota,
    SystemInfo, **Maintenance, ThrottlePanel**)" → **shell** `/system`.
  - 07 §7.8 (`:1122-1125`): "`utils/operationPolling.ts:7` with its **16** non-test importers, among them
    **books pages that keep legacy calls after S9** (… `MaintenanceTab.tsx`, `ThrottlePanel.tsx`)".
  - 05-http-api §7 (`05-http-api.md:991-994`): the same list.
- **Evidence (@748fd787a):**
  - **The tabs are audiobook-specific.** `MaintenanceTab.tsx` calls about 20 audiobook API functions:
    `getAcoustIDStats`, `backfillFileHashes`, `backfillMetadataHashes`, `findMetadataHashDuplicates`,
    `scanDuplicateFiles`, `ChapterGroupsParams` and others (`import * as api`, :36).
    `ThrottlePanel.tsx` calls `getProviderThrottles`/`startThrottleReset`, imports
    `useHasPermission` from ABO's `hooks/useLibraryPermission` (:21) and hard-codes
    `THROTTLE_RESET_PERMISSION = 'scan.trigger'` (:24, one of the legacy constants 07 §7.9.1 replaces).
  - **The count is wrong.** `operationPolling.ts` has **5** non-test importers (`OperationsIndicator`,
    `ITunesImport`, `ActivityLog`, `Library`, `useOperationsStore`; `git grep "operationPolling'"`).
    Neither `MaintenanceTab` nor `ThrottlePanel` imports it or `generated/ops`. They reach `ops.ts` only
    through `services/api`, as do 111 files.
  - **One reading must be wrong.** Either the files move to the shell, where the ESLint boundary rule
    rejects them, or they stay books, in which case §7.11 is wrong.
- **Fix:**
  - **07 §7.11:** `components/system` Storage, Quota, SystemInfo → shell. `MaintenanceTab` is split:
    the maintenance-window and registered-task controls → shell (over the native tasks routes); the
    hash, AcoustID, chapter and duplicate jobs → a books `widgets` entry (placement `system`).
    `ThrottlePanel` → a books widget, unless provider throttles have a native core route, in which case
    name it.
  - **07 §7.8 and 05-http-api §7:** "`ops.ts` is imported by `services/api.ts` and
    `utils/operationPolling.ts` (5 non-test importers @748fd787a); `opsgen` retires when both are gone".
    Drop the "16" and the page list.

### FE8-7: MINOR. FE7-8 residue: the app-ID check has no missing-meta rule or test hook

- **Where:**
  - 07:423-426, :1264-1267: `createShell` "refuses to mount … when `config.app.id` differs from" the
    `mediacore-app` meta.
  - 07:1061-1063: missing-meta defaults, for the **theme** meta only.
  - 07 §7.4.4 (`:560-562`): `/testing` "sets … the app ID explicitly". `RenderInShellOptions` (:538-546)
    has no app-ID, theme-key or default-mode field.
  - 07:1262: `appId` must match `[a-z][a-z0-9]{1,15}`, unanchored, while `themeStorageKey` uses `^…$`.
- **Evidence:**
  - **Missing meta has no defined result.** Without the meta (jsdom, a hand-written `index.html`, a
    shell built by a non-preset config), "differs" is undefined. Comparing against `""` blocks every
    such mount; skipping silently reopens the three-source drift FE7-8 closed. `storageKey()` and the
    default theme key (`<appId>.shell.themeMode`) need the app ID in either case.
  - **Tests cannot choose the app ID.** ABO's tests that read `abo.*` keys cannot select it through any
    documented option.
  - **The `appId` pattern is unanchored,** so `ABO1` passes it as a substring match.
- **Fix:**
  - **Missing meta:** `createShell` uses `config.app.id` and logs a console warning; production cannot
    hit this, because `ui.FromFS` refuses a bundle without the preset's theme meta.
  - **Better:** `FromFS` also requires and exposes `mediacore-app`, and the kernel refuses at P8 when it
    differs from `app.Options.AppID` (`ProblemUIAppIDMismatch`). The server then guarantees the
    equality, and the client check becomes defence in depth. This is an app-ID check, not the build-ID
    check FE4-1 removed.
  - **`/testing`:** `RenderInShellOptions` gains `appId?: string` (default `'test'`), `themeStorageKey?`
    and `defaultMode?`.
  - **Pattern:** write `^[a-z][a-z0-9]{1,15}$` in 07:1254 and :1262.

### FE8-8: MINOR. 09 S9 omits FE7-8, and neither file orders `UIBundle()` after the preset

- **Where:**
  - 07 §7.19 step 4 (`07-ui-shell.md:1501-1506`): the preset writes "the UI build-ID meta tag, the
    `mediacore-app` meta, and the theme meta and boot script".
  - 09 S9 Moves (`09-abo-on-core.md:1115`): build meta, theme meta and boot script only.
  - 09 S9 Done-when (:1118): no `createShell.meta.test.tsx`, no `TestBundle_MissingMeta`.
  - 07:759-770 (FE7-7): `FromFS` fails without the theme meta; 07:1343-1346: `MustFromFS` runs in the
    `embed_frontend` file.
- **Evidence:**
  - **09 omits the app meta.** An implementer following 09 builds ABO without `mediacore-app`, and
    `createShell` then refuses to mount (07:1264).
  - **The series has an order constraint nobody states.** Step 4 is "one PR series" whose PRs each
    reach `main` and the RC train (OPS7-6). Since FE7-7, a PR that switches `main.go` to
    `app.Options{UI: UIBundle()}` before ABO's `vite.config.ts` uses the preset embeds a legacy
    `index.html` with no theme meta. `MustFromFS` then panics on every `embed_frontend` build, and
    `make build`, the image job and the release are red until the preset PR lands. Before FE7-7 this
    order silently served the legacy SPA through `ui.Handler`; now it fails closed, but 07 and 09 still
    state no order.
- **Fix:**
  - **09 S9 Moves:** add "and the `mediacore-app` meta (`appId: 'abo'`, equal to `createShell`'s
    `app.id`; FE7-8)".
  - **09 S9 Done-when:** add `createShell.meta.test.tsx` and `TestBundle_MissingMeta`.
  - **07 §7.19 step 4 and 09 S9:** "ABO's `vite.config.ts` adopts `mediacore(…)` in the same PR as, or
    a PR before, the one that passes `UIBundle()` to `app.Options.UI`; the image job of that PR boots
    the embedded bundle".

### FE8-9: MINOR. Realtime control vocabulary differs between 07 and 05-realtime

- **Where:**
  - 07:1241: "A `resync` frame calls every `useResync` handler and re-runs §7.5.2"; also 07:631,
    :1339 and :1368.
  - 07:555: `FakeRealtime.close(reason: 'shutdown' | 'credential' | 'error')`.
  - 05-realtime §4.2 (`05-realtime.md:209-213`): the native control frames are `core.stream.reset.v1`
    (`restart|gap|overflow`) and `core.stream.closed.v1`
    (`shutdown|credential_revoked|permission_revoked`). §4.8 item 6 (:336-339) defines the client
    reaction. The only `resync` in 05-realtime is stream C's legacy `event: resync` (:42, :377).
- **Evidence:**
  - **The native stream has no `resync` frame.** A shell written from 07 waits for a frame the native
    stream never sends.
  - **The fake cannot express the real reasons.** `FakeRealtime` cannot emit `credential_revoked` or
    `permission_revoked`, and it can emit an `error` reason that does not exist, so shell tests cannot
    cover 05-realtime §4.6.
- **Fix:**
  - **07:1241:** "On `core.stream.reset.v1` (any reason) and after a reconnect that could not resume,
    the shell calls every `useResync` handler and re-runs §7.5.2 (05-realtime §4.8 item 6)". Use the
    same wording at :631, :1339 and :1368.
  - **`FakeRealtime`:** `reset(reason: 'restart' | 'gap' | 'overflow'): void` (replacing `resync()`) and
    `close(reason: 'shutdown' | 'credential_revoked' | 'permission_revoked'): void`. A transport error
    becomes a separate `fail(status?: number)`.

### FE8-10: MINOR. Name residue against `web/` and against FE7-5

- **Where and evidence:**
  - **Wrong `opsgen` path.** 05-http-api §7 (`05-http-api.md:986`): "`opsgen` (`tools/cmd/opsgen` →
    `web/src/services/ops.ts`)". The output is `web/src/generated/ops.ts`
    (`tools/cmd/opsgen/main.go:6,28` @748fd787a), which is what 07 §7.11 and `operationPolling.ts:7`
    use.
  - **A no-argument form survives FE7-5.** 05-http-api:951 says "`createCoreClient()` built on
    `openapi-fetch`", and 07:1096 says "`createCoreClient()`, whose middleware …". FE7-5 made both
    options required (07:672), so the no-argument form contradicts the signature in the same paragraph,
    as `checkBuild(response)` did before FE6-8.
- **Fix:** 05-http-api:986 → `web/src/generated/ops.ts`. 05-http-api:951 and 07:1096 →
  `createCoreClient({ buildId, shell })`.

## Not attacked (fixed decisions)

The following are taken as given:
- D1–D9;
- R3-1..R3-5, R4-1..R4-6, R5-1..R5-7, R6-1..R6-7 and R7-1..R7-8;
- the FE4-1 disposition (one meta-sourced UI build ID, no ldflags, no boot-time build-ID comparison);
- FE4-2/FE5-4 (path-form invites until S9, the fragment reader from S4 r2);
- FE6-1 (a runtime `buildId`, never a library define).

How the findings stay inside those decisions:
- FE8-1 and FE8-8 keep FE6-2's preset-owned theme key and FE7-7's fail-closed bundle check.
- FE8-2(a) adds a header beside `X-App-Build` and keeps the meta as the one build-ID source.
- FE8-7's optional P8 check compares app IDs, not build IDs.
