<!-- file: docs/plans/platform-core/reviews/spec-round-6-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 06cd209d-0392-4108-9742-5e3d5f816f0a -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: FRONTEND SHELL lens (ID prefix FE6)

Scope: (A) regression check of every round-5 FE finding (FE5-1..FE5-16, all accepted; R5-7 binding), against
07 v1.6.1, 05-http-api v1.6.0, 05-realtime v1.5.0, 06 v1.6.1, 09 v1.5.1, 12 v1.5.0, 13 v1.6.0, 03 v1.8.1,
05-updater v1.6.0, 05-storage v1.6.1 and 05-backup v1.5.1; (B) new defects introduced by the revision. Code
anchors are `path:line @748fd787a`. Spec anchors are `file:line` in the current checkout. D1–D9, R3-*, R4-*,
R5-1..R5-7 and the FE4-1/FE4-2 dispositions are taken as given. Nothing here asks for the removed dual-format
machinery, an ldflags UI build ID or a boot-time build-ID check back.

## Part A: regression check of round 5

| Round 5 | Status | Note |
|---|---|---|
| FE5-1 (MAJOR) generator off the `make build`/goreleaser path | **landed** | The Vite config generates the ID under `vite build` on every path (07:876-896, 03:602, 09:760, 05-http-api:626). `build_web.sh` only passes `BUILD_ID` through (07:117-120, 03:169-171, 09:754). The test requires a meta that is neither `""` nor `dev` (07:1265, 05-http-api:1230). New defect: the comparison side of the rule now runs inside a library-mode package, which cannot see the app's define (FE6-1). |
| FE5-3 (MAJOR) bundle ID used as binary identity | **landed from S2b**, gap before | `app.BinaryID` (06:1024-1034) feeds updater `Self.BinaryID` (05-updater:284-294), the storage `build` fields and `selfserved/<binary id>` (05-storage:961-968, :980), and backup `StagedByBuild`/`Source` (05-backup:304, :404). Tests exist (06:1643, 05-updater:660, 05-storage:1925, 05-backup:977). Residue: S0 writes the same `build` fields and prints `binary_id`, but nothing computes it before the kernel exists (FE6-3). `--version-json`'s field list disagrees with the updater's self-test (FE6-4). |
| FE5-4 (MAJOR) S9 writes fragment invites first read at S9 | **landed** | Option (a): the S4 legacy SPA gains the `/invite` reader (07:444-449, 05-authn:1061-1066, 09:99, :148, S4 :872, S9 :943). Fragment test cell (05-authn:1389). R-test row (07:1279, 09:1149). Checked against `web/src/App.tsx:198-206`: the guard runs before the auth gates, so signed-in and signed-out both reach it. Residue: "S4–S8" includes the dark S4 r1, which has neither the reader nor core auth deciding requests. One test also has two names (FE6-5). |
| FE5-2 | landed | One rule in 07:882-891, 03:602, 09:760. The optional `ARG BUILD_ID` is in the frontend stage only (07:166-169, 09:753). New edge: an empty `BUILD_ID`, and no charset check (FE6-7). |
| FE5-5 | landed | Generic map, eight keys, count corrected to 19 (07:382-393, :935-940, :1157). Re-counted against `storageKeys.ts:7-29`: 13 books + 6 shell = 19. The generic `Record` gave up the type check that the old union had (FE6-6). |
| FE5-6 | landed | 07:1147-1148. 05-http-api:961-964 agrees. |
| FE5-7 | landed | 03:304, 05-http-api:844-846, 07:569-570. |
| FE5-8 | landed | 07:605-609, test 07:1264. The signature is still `checkBuild(response)` in three places (FE6-8). |
| FE5-9 | landed | 07:603-604, test 07:1264. |
| FE5-10 | landed | `ui.BasicExemptAssets` (07:754-762), `Router.SPA(h, SPAOptions)` (05-http-api:496, :518-524, :809), 06 P8 (06:1427), tests (07:1292, 05-http-api:1216). No import edge between `ui` and `httpapi` (the kernel passes the func). |
| FE5-11 | landed | 06:425-433. |
| FE5-12 | landed | 09 UI-0 (:841) exports `checkBuild`. 09 S9 (:942) keeps `opsgen`. 04:516 says "not a stage-bound move". |
| FE5-13 | landed | 07:1300-1301, :1335-1340; 09:942, :988. |
| FE5-14 | landed | 07:1093-1097, matching 05-realtime:407, :514. |
| FE5-15 | landed | 12:584-588. |
| FE5-16 | landed | 12:415-421, :436-439; 07:640-641 (lockstep parses tarball URLs). |

## Part B: findings

| ID | Severity | Title |
|---|---|---|
| FE6-1 | MAJOR | `checkBuild` compares against "the bundle's define `MEDIACORE_BUILD_ID`", but from UI-0 it lives in `@mediacore/api-client`, which Vite library mode builds to `dist/` before the app. The app's define never reaches it, so production tabs get a permanent reload offer, or none at all. |
| FE6-2 | MINOR | The preset injects the theme boot script, but `mediacore(options)` has no options: the boot script cannot know ABO's `app-theme-mode` key or its `dark` default, which `createShell` gets only at runtime |
| FE6-3 | MINOR | S0's guard writes binary-ID `build` fields and S0's `--version-json` prints `binary_id`, but `app.BinaryID` is "computed by the kernel before P−1". The kernel does not exist until S2/S2b, so S0–S2 have no defined computation. |
| FE6-4 | MINOR | `--version-json` fields: 09 lists `version, commit, ui_build_id, binary_id`. 05-updater's self-test requires `app`. `commit` has no source without ldflags, but P10 and the R-test build `prev` from it. |
| FE6-5 | MINOR | "An invite minted on S9 stays valid after a rollback to S4–S8" includes S4 r1, which is dark and has no reader. One S4 test also has two names. |
| FE6-6 | MINOR | `legacyStorageKeys?: Readonly<Record<string, string>>` drops the type check: a misspelt shell name silently resets that preference at S9 |
| FE6-7 | MINOR | The UI-build-ID rule takes `BUILD_ID` "when the caller set it": an empty value and an unsafe charset are not covered |
| FE6-8 | MINOR | `checkBuild` has two signatures: `checkBuild(response)` in 05-http-api §7 and 07 §7.5.2/§7.8; `checkBuild(response, { shell })` in the 07 rule and test |

Counts: **0 BLOCKER, 1 MAJOR, 7 MINOR** (8 findings).

### FE6-1: MAJOR. The api-client's `checkBuild` cannot read the app's build-ID define

- **Where:**
  - **07 §7.5.2** (`07-ui-shell.md:596-601`): from UI-0, "`@mediacore/api-client`'s runtime exports it as
    `checkBuild(response)` … Comparing every API response with the bundle's define `MEDIACORE_BUILD_ID`".
  - **07 §7.3.1** (`:73-79`): "Every package builds to `dist/` (JS by Vite library mode …)"; "every production
    build and every published install resolves `dist/`"; `development` (src) is used under `vite serve` and
    vitest only.
  - **07 §7.10** (`:1114`): "the same preset builds the core packages in library mode"; `define` `MEDIACORE_BUILD_ID`
    (`:1123-1125`).
  - **07 §7.6.3** (`:893-896`), **09 S0/UI-0** (`:760`, `:841`), **05-http-api §7** (`:947`).
- **Evidence:**
  - **The package is built before the app, by a different `vite build`.** At UI-0, `build_web.sh deps` builds
    `@mediacore/api-client` to `dist/` (07:113-117). The app's `npm run build` comes later and only resolves that
    `dist/`. After the split the package is built once, in core's publish job (12:415-421).
  - **Vite library mode replaces `import.meta.env.*` statically when it builds the library.** One of two things
    then happens:
    - **The library build defines `MEDIACORE_BUILD_ID`.** `dist/` then carries that build's own ID. In a clean
      checkout this happens to equal the app's `<sha>-<time>`. In the Docker frontend stage (no `.git`) and in any
      dirty tree, each `vite build` draws its own `<timestamp>-<random>` (07:889-891), so the two differ.
    - **It does not define it.** The expression becomes `undefined`, which "a define of `""` or `"dev"` never
      triggers it" (07:615) does not cover.
  - **Either way, case 1 is true on every response.** In the shell that means the non-dismissable banner on every
    page of every Docker image from UI-0 on. Reloading loads the same bundle, so it never clears. After the split,
    every app compares against core's publish-time value.
  - **No test can see it.**
    - `buildId.test.ts` (07:1264) runs under vitest, which resolves the package's `src` through the `development`
      condition, so the baked `dist/` value is never tested.
    - `TestBuildID_HeaderEqualsEmbeddedMeta` compares the server header with the meta and runs no browser.
    - R-test cell (b) runs in CI from a clean checkout, where the values coincide.
  - **`MEDIACORE_SHELL` (case 2's `shell`) has the same problem** if the runtime's own middleware reads it from
    `import.meta.env`, rather than taking it from its caller.
- **Fix:**
  - **Make the build ID a runtime input of `checkBuild`, not a define read inside the library.** Change the
    signature to `checkBuild(response, { buildId, shell })`.
    - `buildId` is read once at page load from the served document's `<meta name="mediacore-build">`, which is
      the very value the server's `X-App-Build` comes from (07 §7.6.3). This matches 07 §7.12's existing
      "load → read build ID".
    - `createCoreClient({ buildId, shell })` stores both for the runtime's own middleware.
    - ABO's `apiFetch` passes `{ buildId: <meta>, shell: false }`. `createShell` passes `{ buildId: <meta>,
      shell: true }`.
  - **Remove `MEDIACORE_BUILD_ID` from the preset's `define` list** (07:1123), or keep it only as the value
    the preset writes into the meta. Say explicitly that no `core/web` package reads `import.meta.env.MEDIACORE_*`
    except `MEDIACORE_CORE_VERSION`. That one is correctly baked at the shell's own build: it is the shell
    package's version (07:576-578).
  - **Edit the matching text** in 07 §7.5.2, §7.6.3 (first bullet), §7.10, 09 S0 "Build ID" row and UI-0, and
    05-http-api §7.
  - **Add an e2e cell** that runs a production `dist/` build (not vitest) against a server whose `X-App-Build`
    equals the page's meta (no reload offered) and differs from it (reload offered). Run it against the Docker
    image in the PR image job.

### FE6-2: MINOR. The preset's theme boot script has no way to learn the app's key or default mode

- **Where:**
  - **07 §7.7** (`07-ui-shell.md:928`): `themeBootScript(storageKey: string)`; `:933-934`: "The pre-mount boot
    script … is injected into `index.html` by the vite preset"; `:935-940`: "ABO passes its existing key in
    `createShell({ legacyStorageKeys })`".
  - **07 §7.10** (`:1108`: `mediacore(options)`, with `options` defined nowhere; `:1128`: "theme boot script").
  - **07 §7.4.2** (`:382-393`).
- **Evidence:**
  - **HEAD's boot script hard-codes both values.** It reads `localStorage.getItem("app-theme-mode")` and falls
    back to `dark` (`web/index.html:13-30 @748fd787a`). Its comment requires those values to match
    `modeStorageKey`/`defaultMode` in `main.tsx:30-33`.
  - **From S9 the preset writes the script at build time,** but the key is a runtime `createShell` option, and
    the preset has no documented option for it.
  - **The result is a first-paint regression for every ABO user.** The script either reads the default
    `storageKey('shell','themeMode')` (`abo.shell.themeMode`, which does not exist) or a missing key. Users who
    chose light see a dark flash until React mounts, and an app with a `light` default sees the reverse. The CSP
    side is fine (`ui.FromFS` hashes whatever script is there).
- **Fix:**
  - **Define the preset options** in 07 §7.10:
    `mediacore({ appId: string; themeStorageKey?: string; defaultMode?: 'light' | 'dark' })`. The defaults are
    `storageKey('shell','themeMode')` and `'dark'`.
  - **Make the boot script and `createShell` share one source.** The preset writes the key and mode into a
    `<meta name="mediacore-theme" …>`, and `createShell` reads the key from there. Otherwise, require
    `legacyStorageKeys.themeMode === themeStorageKey` and have a test assert it.
  - **ABO passes** `themeStorageKey: 'app-theme-mode', defaultMode: 'dark'`.
  - **Add a test:** S9 e2e with a stored `light` asserts that `data-mui-color-scheme` is `light` before the
    first script module runs.

### FE6-3: MINOR. The binary ID has no computation in S0–S2

- **Where:**
  - **Uses from S0:** 05-storage (`05-storage.md:961-967`: every `build` field, including the install marker
    "09 S0's guard writes … with this exact shape", `:976`, and the served ledger, `:979`, holds the binary ID
    "computed once by the kernel before P−1"); 09 S0 Data/keys (`09-abo-on-core.md:757`: the S0 guard writes the
    install marker and the served-ledger entry); 09 S0 Endpoints (`:759`: `--version-json` prints `binary_id`
    "as `app.BinaryID` computes it", from S0, §9.6 row 20 `:1034`).
  - **Definition from S2b only:** 06 (`06-app-sdk.md:1024-1034`: `app.BinaryID(opts Options)`; history `:2548`
    "computed before P−1, S2b").
- **Evidence:**
  - **The function does not exist yet.** At S0 core holds only `archtest` and `storage/keys` (09:751), and
    package `app` lands at S2 (04 §8.1).
  - **S0's writers are ABO code.** Those are the guard in `internal/database` and `main`'s fast exits, and they
    have no `app.Options` to pass.
  - **The S0–S2 values are unspecified.** The `build` values written in S0–S2 are read by the first S2b boot
    (served ledger `last`, install marker). Equality checks across that boundary (for example "was the previous
    boot the same binary") are therefore undefined at the first kernel boot.
- **Fix:**
  - **Move the computation into a dependency-free leaf that lands at S0,** for example
    `core/storage/keys.BinaryID(version string) (string, error)` or a new `core/buildid` leaf in 04's S0 row.
    `app.BinaryID(opts)` delegates to it.
  - **Call it from S0:** ABO's S0 guard and `--version-json` use it with `main.version`.
  - **Update the text:** 05-storage's sentence becomes "computed once per process before the store opens (by
    ABO's guard at S0–S2, by the kernel before P−1 from S2b)". Update 06's history entry to match.

### FE6-4: MINOR. `--version-json` field list and the `commit` source

- **Where:**
  - **09** S0 Endpoints (`09-abo-on-core.md:759`) and §9.6 row 20 (`:1034`): "`--version-json` (version,
    commit, `ui_build_id`, `binary_id`)".
  - **05-updater step 5** (`05-updater.md:393-396`): the new binary "must print `{app: AppID, version: target}`,
    and its `binary_id`".
  - **The R-test and P10** (`09:1149`, `:1069`): "the commit P10 prints (`<bin>.prev --version-json`)".
- **Evidence:**
  - **`app` is missing.** 09's list has no `app` field, so a 09-conformant binary fails the updater's self-test.
  - **`commit` has no source.**
    - The spec forbids new link-time values. `main.go @748fd787a` declares only `var version` (`:20-21`), so
      goreleaser's existing `-X main.commit={{.Commit}}` (`.goreleaser.yml:33`) targets an undeclared variable
      and is silently dropped.
    - The only remaining source is `debug.ReadBuildInfo` `vcs.revision`. It is absent in the Docker build (no
      `.git` in the context), and marked `vcs.modified` for a dirty `make deploy`.
    - P10 and the R-test then have no commit for `prev`.
- **Fix:**
  - **Pin one field list** in 09 S0, §9.6 row 20 and 05-updater step 5:
    `{app, version, commit, ui_build_id, binary_id}`.
  - **Define the fields:** `commit` is `vcs.revision` (suffixed `+dirty` when `vcs.modified`), or `""` when
    absent.
  - **Give P10 a fallback when `commit` is `""`:** `version` (a tag) or a manual `PREV_REF`.
  - **Either declare `var commit` in ABO's `main` or delete goreleaser's dead `-X main.commit`.** The first does
    not conflict with FE4-1, which bans only a linked UI build ID.

### FE6-5: MINOR. "Rollback to S4–S8" includes the dark S4 r1; one test has two names

- **Where:**
  - **09** §9.2.1 expand rule (`09-abo-on-core.md:97-99`: "still opens after a rollback to S4–S8"), row 3
    (`:148`: "stays valid after a rollback to S4–S8, because those builds run core auth and their legacy SPA
    reads the fragment form from S4"), and S4 Invite row (`:872`: "after a rollback to S4–S8").
  - **09 S4 Releases** (`:866`): r1 is dark, gin's auth still decides every request. UI changes come in r2.
  - **Test names:** 09 S4 Done when (`:876`: `TestLegacySPA_InviteFragmentReader`); 07:1278 and 05-authn:1389
    (`TestLegacyInviteLink_EndToEnd`, fragment cell, "S4 r2").
- **Evidence:**
  - **S4 r1 has neither half of the claim.** Its SPA has no fragment reader, which ships with r2's UI changes.
    Its request path is gin's auth, which does not read native invites from books' extension family.
  - **The R-test cell is unaffected.** It covers only S9→S8.
  - **The two test names will drift.** The 09 done-check names a test that 07 and 05-authn do not define, and
    vice versa.
- **Fix:**
  - **Reword the three 09 places** to "a rollback to S4 r2–S8".
  - **Use one test name:** keep `TestLegacyInviteLink_EndToEnd` (fragment cell) in 09:876, or add
    `TestLegacySPA_InviteFragmentReader` to 07 §7.18 and 05-authn §10 as the fragment cell's name.

### FE6-6: MINOR. `legacyStorageKeys` as `Record<string, string>` loses the compile-time check

- **Where:** 07 §7.4.2 (`07-ui-shell.md:382-393`); test `legacyStorageKeys.test.tsx` (`:1293`).
- **Evidence:**
  - **The shell looks values up by its own name for each key.** Those names are `themeMode`,
    `tableConfigPrefix`, `opsIndicatorCollapse` and the others.
  - **A typo compiles.** With `Record<string, string>`, a typo or a rename on either side compiles. The shell
    then falls back to `storageKey(...)`, so that preference silently resets at S9 and an S9 edit is lost on
    rollback, which is exactly FE5-5's failure.
  - **The test catches only ABO's eight names.** It does not catch a shell-side rename, and the next app gets no
    help at all.
- **Fix:**
  - **Export the shell's names as a union** `LegacyShellKey = 'themeMode' | 'activitySourcePrefs' |
    'activityOpsPinned' | 'tableConfigPrefix' | 'dismissedAnnouncements' | 'welcomeWizardCompleted' |
    'opsIndicatorCollapse' | 'showAdvanced'`.
  - **Type the field with it:** `legacyStorageKeys?: Readonly<Partial<Record<LegacyShellKey, string>>>`. A new
    shell preference adds a member. ui-kit still holds no key names.

### FE6-7: MINOR. `BUILD_ID` "when set": empty values and charset

- **Where:** 07 §7.6.3 rule item 1 (`07-ui-shell.md:883-884`), 03:602, 09:760, 07 §7.3.2 Docker `ARG BUILD_ID`
  (`:166-169`).
- **Evidence:**
  - **An empty value is not handled.** `docker build --build-arg BUILD_ID=$BUILD_ID` with an unset CI variable,
    or a workflow expression that expands empty, passes `""`. A `process.env.BUILD_ID ?? …` reading then
    writes `""` into the meta and the header. `TestBuildID_HeaderEqualsEmbeddedMeta` fails only on the paths it
    covers. Any other image silently disables the rule ("`""` never triggers").
  - **The value is not checked.** It lands in an HTML attribute and an HTTP header unvalidated.
- **Fix:** Item 1 becomes "`BUILD_ID` when it is non-empty; it must match `^[A-Za-z0-9._-]{1,64}$`, else
  `vite build` fails". An empty value falls through to items 2–3. Add both cases to `buildId.test.ts`.

### FE6-8: MINOR. `checkBuild` has two signatures

- **Where:**
  - **One argument:** 05-http-api §7 (`05-http-api.md:947`: "the runtime exports `checkBuild(response)`");
    07 §7.5.2 (`07-ui-shell.md:597`); 07 §7.8 (`:967`).
  - **Two arguments:** 07 §7.5.2 (`:601`: `checkBuild(response, { shell })`), test row `:1264`, 07 history
    `:1495`.
- **Fix:**
  - **Use one signature everywhere.** It is `checkBuild(response, { buildId, shell })` if FE6-1 is accepted,
    else `checkBuild(response, { shell })`.
  - **Make `shell` explicit:** "the caller passes it; the runtime never reads `import.meta.env` for it".

## Not attacked (fixed decisions)

The following are taken as given: D1–D9, R3-1..R3-5, R4-1..R4-6, and R5-1..R5-7. R5-7 covers the UI build ID
stamped by the npm build, and a separate binary ID. Also taken as given are the FE4-1 disposition (meta-only UI
build ID, no ldflags, no boot check) and the FE4-2/FE5-4 disposition (path-form invites until S9, with the
fragment reader from S4).

FE6-1 keeps the meta tag as the one source and only moves where the client reads it. FE6-3 and FE6-4 work inside
R5-7's binary ID.
