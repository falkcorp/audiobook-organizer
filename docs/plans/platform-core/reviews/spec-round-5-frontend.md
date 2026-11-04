<!-- file: docs/plans/platform-core/reviews/spec-round-5-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 07b88c4d-bf1c-4f49-87a2-9d50ed9dda3f -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: FRONTEND SHELL lens (ID prefix FE5)

Scope: (A) regression check of the round-4 FE BLOCKERs and MAJORs (FE4-1, FE4-2) and of the FE4 MINOR fixes
the lead named (FE4-5 `ui.Handler` middleware wiring, FE4-9 reload rule), against 07 v1.5.1, 05-http-api
v1.5.1, 05-realtime v1.4.0, 06 v1.5.1, 09 v1.4.1, 12 v1.4.0, 13 v1.5.0, 03 v1.7.1 and 04 v1.4.1; (B) new
cross-file contradictions. Code anchors are `path:line @748fd787a`. Spec anchors are `file:line` in the current
checkout. D1–D9, R3-1..R3-5, R4-1..R4-6 and the FE4-1/FE4-2 dispositions are taken as given. Nothing here asks
for the removed dual-format machinery or the ldflags build ID back.

## Part A: regression check of round 4

| Round 4 | Status | Note |
|---|---|---|
| FE4-1 (MAJOR) meta-only build ID everywhere | **landed in the API, half in the build** | 06 dropped `Options.BuildID`, `ProblemBuildID` and the P8 check (06:797-802, :1326). 05-http-api stage 5 (:619), 07 §7.6.3 (:844-870) and 09 S0 (:663) all say meta-only with no ldflags. Two residues remain. First, the one generator, `build_web.sh`, is not on the `make build` or goreleaser web-build path (FE5-1). Second, 07 and 09 specify different generators and disagree on the Docker `ARG` (FE5-2). There is also a new cross-file effect: other specs use the bundle ID as the binary's identity (FE5-3). |
| FE4-2 (MAJOR) invite path form until S9 | **landed for S4–S8**, gap at the S9 rollback edge | 05-authn §4.13 (`InviteLinkForm`, :965-969), 07 §7.4.3/§7.9.1 (:425-445, :985-992), 05-http-api §3.5/§8.2 (:541-545, :1001, :1034-1041), 09 S4/S9 (:775, :846), 03 (:522) and tests (`TestLegacyInviteLink_EndToEnd`, `_302NoToken` staged) all agree. Checked against `web/src/App.tsx:201-206` and `internal/server/handlers/invite_delivery.go:115-117`: the server mints the link, so the option is the right control. The S9 build is now both the first writer of fragment links and their only reader. After a rollback to S8 the token is still valid, but the S8 SPA cannot open the link (FE5-4). |
| FE4-5 (MINOR) `ui.Handler` middleware wiring | **landed** | `AppOptions` funcs read an `atomic.Pointer` that books fills in Start (07:656-664, 06:843-858, 09:845). `ui_route` is core-generic: `ui.Options.Redirects`/`OnRedirectUse` (07:699-707), kernel entries at P7 (09:588-591). Tests: 07:1233-1234. One residue: 06's `UIDescriptor` comment still has the app's `PageMiddleware` recording `ui_route` (FE5-11). |
| FE4-9 (MINOR) reload rule, one implementation | **landed in 07 and 05-http-api** | `checkBuild(response)` is exported by the api-client runtime from UI-0 and called by `apiFetch` (07:579-584, 05-http-api:936-940). Residues: 09 still moves the rule at S9, and UI-0 does not list it (FE5-12). The rule's case 2 and its `dev` handling have new holes (FE5-7, FE5-8, FE5-9). |
| FE4-3, FE4-4, FE4-6, FE4-7, FE4-8, FE4-10, FE4-11 | landed, with residues | FE4-3: `Reporting-Endpoints` discriminator in 07:590-596, 09:663, 05-http-api:619. Checked: `internal/server/server_middleware.go:108` sets it globally. FE4-4: covers 3 of the 8 shell-bound keys (FE5-5). FE4-6: S2 leaf in 03:287, 04 §8.1, 06:1255, 07:734-760. FE4-7: the rule landed in 05-http-api §7, 07 §7.8 and 04, but 07 §7.11 still moves `operationPolling` into the shell (FE5-6) and 09 S9 still retires `opsgen` (FE5-12). FE4-8: "may ship in one release" leaves step 4 alone incoherent (FE5-13). FE4-10: 05-tasks:1555. FE4-11: 07:719-723, test 07:1231. |

## Part B: findings

| ID | Severity | Title |
|---|---|---|
| FE5-1 | MAJOR | The one build-ID generator (`build_web.sh`) is not on the `make build` path or the goreleaser path: `make web-build` runs `npm run build` directly, and `web-deps` is `build_web.sh deps` |
| FE5-2 | MINOR | 07 and 09 specify two different generators (git-derived vs timestamp+random) and disagree on the Docker `ARG BUILD_ID`; the git-derived `-dirty` form gives every dirty rebuild of one commit the same ID |
| FE5-3 | MAJOR | 05-updater, 05-storage and 05-backup use "build ID" as the binary's identity, but under FE4-1 it is the UI bundle's meta: every API-only build and every UI-less app is `dev`, so `selfserved/<build id>` lets one binary inherit another's confirmed readiness |
| FE5-4 | MAJOR | S9 starts writing fragment invite links in the same release that first reads them; after an S9→S8 rollback the invites are still valid but the S8 SPA cannot open them, and §9.2.1's "one complete list" and the R-test omit this |
| FE5-5 | MINOR | `legacyStorageKeys` covers 3 of the 8 `localStorage` keys used by shell-bound files: `table_config_*`, `dismissed_announcements`, `welcome_wizard_completed`, `ops-indicator-collapse-v1` and `settings.showAdvanced` reset at S9 and are lost on a rollback |
| FE5-6 | MINOR | 07 §7.11 moves `utils/operationPolling.ts` (with `followOperation`, `trackedOperations`) into the shell, but it imports ABO's `generated/ops` and `services/api`, and books pages keep importing it |
| FE5-7 | MINOR | 03 says `ui_build_id` is the meta of whatever bundle the binary embeds; 07 says it is `""` while a legacy mount owns `/`, and stale-build case 2 depends on the `""` |
| FE5-8 | MINOR | Stale-build case 2 has no shell-only guard, and `checkBuild` runs in the pre-shell bundle from UI-0: a legacy page that calls `/modules` gets a reload offer that reloading cannot clear |
| FE5-9 | MINOR | 05-http-api says clients never treat an `X-App-Build: dev` header as a mismatch; 07 case 1 exempts only a `dev` define, so a `dev` header triggers it |
| FE5-10 | MINOR | From S9 "`ui.Handler` supplies" the Basic-gate asset predicate, but neither `ui` nor `Router.SPA(h http.Handler) error` has a way to pass one |
| FE5-11 | MINOR | 06 `UIDescriptor.LegacyRedirects` comment still says the app's `PageMiddleware` records `ui_route` (pre-FE4-5) |
| FE5-12 | MINOR | 09 S9 Moves residues: it retires `opsgen` and moves the stale-build rule into the api-client at S9; 07 keeps `opsgen` and moves the rule at UI-0, and 09 UI-0 does not list it |
| FE5-13 | MINOR | §7.19 step 4 shipped alone is incoherent: `createShell` mounts module routes under `/<id>/`, so books URLs move at step 4 with no redirects until step 5 |
| FE5-14 | MINOR | 07 §7.9.2 says the S5 bridge forwards "never a `core.*` kind"; 05-realtime and 05-tasks forward core kinds under their `LegacyDefID` |
| FE5-15 | MINOR | 12's cutover step 6 never reverts the Docker frontend stage's `core/web` copies and builds, so step 7's `docker build .` fails after `git rm -r core/` |
| FE5-16 | MINOR | 07 §7.17 / 12 §12.6: the private-repo fallback (GitHub Release tarballs) needs a token on every machine, the same reason GitHub Packages was rejected, and an `X.Y.Z` specifier cannot name a tarball |

Counts: **0 BLOCKER, 3 MAJOR, 13 MINOR** (16 findings).

### FE5-1: MAJOR. The build-ID generator is not on the `make build` or goreleaser web-build path

- **Where:** 07 §7.6.3 (`07-ui-shell.md:845`: "The build ID is generated by `scripts/ci/build_web.sh`, before Vite
  runs, and nowhere else"); 07 §7.3.2 (`:112-118`: `make web-deps` runs `build_web.sh deps`, which runs `npm ci`
  in `core/web`, builds the core packages and runs `npm ci` in `web/`, with no `web/` build; `:122-124`:
  `make web-build` … "depend on it"); 07 §7.10 (`:1074`: `define` is "the per-build value from `build_web.sh`;
  `"dev"` under `vite serve`", with nothing said for `vite build` without it); 09 S0 (`:657`, `:663`); 05-http-api
  stage 5 (`:619`).
- **Evidence:**
  - **`make build` never calls the generating mode.** `make build` depends on `web-build`
    (`Makefile:154 @748fd787a`), whose recipe is `cd web && npm run build` (`Makefile:198-201`), and
    `npm run build` is `tsc … && vite build` (`web/package.json:8`). The spec adds a `web-deps` prerequisite to
    `web-build`, and that prerequisite is the `deps` mode. Environment variables set in one recipe do not reach the
    next, so no ID reaches Vite on the deploy path.
  - **The release path has the same gap.** It runs the `pre-build-script` input
    `cd web && npm ci && npm run build` (`release-prod.yml:58`). 07 `:131` replaces its `npm ci` half with
    `make web-deps`, which again leaves a bare `npm run build`.
  - **Nothing says what `vite build` writes without `BUILD_ID`.** Either outcome is bad:
    - **It writes `dev`.** Every production artefact then serves `X-App-Build: dev`, and the stale-build rule is
      silently off: "`dev` never triggers". `TestBuildID_HeaderEqualsEmbeddedMeta` still passes, because header
      and meta are equal.
    - **It writes no meta.** From S9, `ui.MustFromFS` panics: "It fails if index.html or the meta tag is
      missing" (07:637-641).
- **Fix:**
  - **Generate the ID inside the Vite config.** Use ABO's `vite.config.ts` from S0 and the
    `@mediacore/shell/vite` preset from S9. Under `vite build`, evaluate once
    `process.env.BUILD_ID || <git-derived> || <timestamp>-<random>`, then write it to both the `define` and the
    meta tag. That runs on every build path, `make`, Docker, goreleaser and a bare `npm run build`, with no
    variable to carry.
  - **Narrow `build_web.sh`'s role.** It only passes `BUILD_ID` through when the caller set one.
  - **Make one rule in 07 §7.6.3, 07 §7.10, 09 S0 and 05-http-api stage 5:** "the Vite config generates it"
    instead of "`build_web.sh` generates it".
  - **Extend `TestBuildID_HeaderEqualsEmbeddedMeta`.** For every `embed_frontend` artefact, the meta must be
    neither `""` nor `dev`.

### FE5-2: MINOR. Two generators, two Docker rules, and a non-unique dirty ID

- **Where:** 07 §7.6.3 (`07-ui-shell.md:845-849`: `BUILD_ID`, else `<git rev-parse --short HEAD>-<commit unix
  time>[-dirty]`, "so a rebuild of one commit gets one ID", else timestamp+random) and §7.3.2 (`:162-164`: "The
  stage declares an optional `ARG BUILD_ID`"); test row `:1212` ("a Docker image (with and without `BUILD_ID`)");
  09 S0 (`09-abo-on-core.md:663`: `$BUILD_ID` "and otherwise generates `<UTC timestamp>-<8 random hex>`"; `:656`:
  "No Dockerfile stage takes a `BUILD_ID` argument"; history `:1289`: "no Docker `ARG BUILD_ID`").
- **Evidence:**
  - **The two files disagree.** They give different fallback generators, and opposite answers on whether the
    Docker frontend stage has an `ARG`.
  - **The dirty suffix is constant.** The git-derived form gives every dirty rebuild of one commit the same ID,
    because `-dirty` does not change between edits. A tab on the previous dirty build of a `make deploy` from a
    working tree with edits is then never offered a reload. Its lazy chunks are gone from the new binary, so the
    next lazy route import fails.
  - **The version string has the same problem.** `VERSION` uses `git describe --tags --always --dirty`
    (`Makefile:9`), so version plus build ID cannot tell two dirty binaries apart either (see FE5-3).
- **Fix:**
  - **One text in both files:** `BUILD_ID` if set; else, in a clean checkout,
    `<short sha>-<commit unix time>`; else (dirty, or no `.git`) `<UTC timestamp>-<8 random hex>`.
  - **Docker:** keep the optional `ARG BUILD_ID` in the frontend stage only (07's version). Fix 09:656 and the
    history line to "the Go stage takes none".

### FE5-3: MAJOR. "Build ID" means the UI bundle's meta, but three subsystems use it as the binary's identity

- **Where:** 05-http-api stage 5 (`:619`: "The Go binary carries no build ID of its own … otherwise `dev`");
  05-updater `Self` (`05-updater.md:276-278`: "the build ID is the embedded bundle's … `dev` when nothing is
  embedded"), `IgnoreRestoreStagedBy` (`:243-247`), the marker's `to_build` check (`:396-401`, OPS4-9);
  05-storage self-served record (`05-storage.md:951`: `c/storage/selfserved/<build id>`) and `RequiresSelfServed`
  (`:723`: "until **this build** has been continuously ready"); 05-backup `StagedByBuild`/`Source`
  (`05-backup.md:297`, `:396`: "`version.Version` + build ID; another build rejects the intent"); 09 S0
  `--version-json` (`:662`).
- **Evidence:** Under the FE4-1 disposition the ID exists only in the embedded bundle. Three cases follow:
  - **API-only builds.** Every `make build-api` binary reports `dev`.
  - **Apps with no embedded shell bundle.** `hello` without `ui_embed.go`, and a proof-depth TV or Movies binary
    that ships no UI, report `dev` in every release.
  - **Dirty rebuilds.** Dirty rebuilds of one commit share one ID (FE5-2).

  In 05-backup, `version.Version` is the **core** constant (03 §3.3), not the app release, so it does not
  separate them either. As a result:
  - **`selfserved/dev` is shared.** It is one record across all such binaries. A new release that declares a
    `RaisesLevel` step finds `confirmed_at` already written by a previous, different binary, so
    `RequiresSelfServed` holds on its first boot. The one-way step then runs on an unconfirmed build, which is
    exactly what OPS2-2 forbids (05-storage:1489).
  - **Staged-restore intents are accepted by the wrong binary.** An intent staged by one `dev` binary is accepted
    as "its own" by another (`StagedByBuild` equality).

  The spec names one concept "build ID" but uses it for two jobs: a UI reload discriminator, and a binary
  identity.
- **Fix:**
  - **Split the term.**
    - "UI build ID" (bundle meta) is used only for `X-App-Build`, `ui_build_id` and the reload rule.
    - "Binary ID" is computed once before P−1 as `app.Options.Version` plus the SHA-256 of the running
      executable (`os.Executable`, read once). This needs no ldflags, and `debug.ReadBuildInfo` VCS fields are
      optional extra detail. The Docker context has no `.git`, so the executable hash is the part that always
      works.
  - **Use the binary ID for identity everywhere.** That covers 05-updater `Self.Build` and the marker's
    `to_build`, 05-storage `selfserved/<binary id>` and the served ledger's `build`, 05-backup `StagedByBuild` and
    `Source`, and `--version-json` (print both IDs).
  - **Add a test.** Two `make build-api` binaries built from one commit with different code have different binary
    IDs, and a `RaisesLevel` step on the second one is deferred.

### FE5-4: MAJOR. S9 writes fragment invite links in the same release that first reads them

- **Where:** 09 §9.2.1 (`09-abo-on-core.md:93-98`: "Where a client must understand something before a server
  starts sending it, the client-side reader ships in an **earlier** release than the server change … invite links
  keep their `/invite/<token>` path form until S9"); 09 S9 UI routes (`:846`: "From this build core issues invite
  links in the fragment form"); 05-authn §4.13 (`:967-969`: ABO "drops [`InviteLinkPath`] in the S9 PR that adopts
  the shell"); 09 §9.2.1 cost table row 3 (`:143`: "Invite and temp-login links issued by the new build are void
  on the older one", from S4) and row 9 (`:149`); S9 Rollback (`:847`); R-test (`:1048`: "invites issued on `cur`
  void").
- **Evidence:**
  - **The token stays valid across S9→S8.** S4–S9 builds all run core auth, so a native invite minted on S9 lives
    in books' extension family, which S8 reads. Row 3's "void" is therefore false for this rollback.
  - **The S8 SPA cannot open the link.** S8 serves the legacy SPA, whose invite branch requires
    `pathname.startsWith('/invite/')` (`web/src/App.tsx:201-206 @748fd787a`). A signed-out invitee opening
    `/invite#token=…` falls to `*` → `/login` with the fragment dropped (`App.tsx:249-250`). A signed-in one gets
    `NotFound`.
  - **This is FE4-2's defect again, one stage later.** It contradicts the spec's own reader-before-writer rule.
    No row, no release-note text and no R-test cell covers it: the R-test asserts "void", which is the wrong
    expectation.
- **Fix (either):**
  - **(a) Ship a reader first.** Any release from S4 to S8 (the S4 SPA change is the natural carrier) adds a
    **reader only** to the legacy SPA. Its guard becomes `pathname === '/invite' || pathname.startsWith('/invite/')`,
    and `<Route path="/invite" element={<AcceptInvite />} />` prefers `#token=` and removes it with
    `history.replaceState`. Core keeps minting the path form until S9.
  - **(b) Write later.** Keep `InviteLinkPath` in the S9 release, and drop it in the first release after S9.
    05-authn §4.13 and 09 S9 then say "from S9 + 1", and the books 302 starts with S9 as now.

  Either way, add a §9.2.1 row (or reword row 3 for S9), and add an R-test cell "invite minted on S9, roll back
  to S8, open signed out → invite page".

### FE5-5: MINOR. `legacyStorageKeys` misses five keys used by shell-bound files

- **Where:** 07 §7.4.2 (`07-ui-shell.md:378-382`: `legacyStorageKeys?: … 'themeMode' | 'activitySourcePrefs' |
  'activityOpsPinned'`); §7.7 (`:891-895`: "so a rollback from S9 to S8 keeps every preference"); §7.11
  (`:1105`: "21 keys, 13 of them audiobook-specific"); 09 S9 (`:846`), Done-when R-test row (`:848`: "a preference
  changed on S9 is kept after a rollback to S8").
- **Evidence:**
  - **The key count is wrong.** `web/src/lib/storageKeys.ts:7-29 @748fd787a` has **19** keys: 13 books keys, the 3
    named shell keys, and 3 unassigned ones.
  - **The 3 unassigned keys are used by files 07 moves to the shell or to ui-kit:**
    - `table_config`, the prefix of every `ConfigurableTable` column layout (`ConfigurableTable.tsx:104,129`) →
      ui-kit;
    - `dismissed_announcements` (`AnnouncementBanner.tsx:23,33`) → shell/ui-kit;
    - `welcome_wizard_completed` (`WelcomeWizard.tsx:295`, `App.tsx:93,97`) → shell first-run frame.
  - **Two more shell-bound files use literal keys:** `ops-indicator-collapse-v1` (`OperationsIndicator.tsx:105`,
    moving to the shell `/activity` page) and `settings.showAdvanced` (`useAdvancedSettings.ts:28`, used by the
    shell Settings frame).
  - **They reset at S9.** Under `storageKey(scope, name)` (07:401) these become `abo.shell.*`. Every table layout
    and dismissed banner resets at S9, and S9 edits are lost after a rollback, which contradicts §7.7 and the R-test
    row.
- **Fix:**
  - **Make the map generic.** Replace the three-key union with
    `legacyStorageKeys?: Readonly<Record<string, string>>`. ABO passes all eight:
    `themeMode: 'app-theme-mode'`, `activitySourcePrefs`, `activityOpsPinned`, `tableConfigPrefix: 'table_config'`,
    `dismissedAnnouncements`, `welcomeWizardCompleted`, `opsIndicatorCollapse: 'ops-indicator-collapse-v1'`,
    `showAdvanced: 'settings.showAdvanced'`.
  - **Keep ui-kit free of key names.** `ConfigurableTable` takes its full key, prefix included, from the caller.
  - **Correct "21" to "19"** in 07 §7.11.

### FE5-6: MINOR. `operationPolling` cannot move into the shell while books imports it with legacy enums

- **Where:** 07 §7.11 row (`07-ui-shell.md:1097`: "`utils/{operationPolling,followOperation,trackedOperations}.ts`
  … → shell `/activity`"); vs 05-http-api §7 (`:947-955`) and 07 §7.8 (`:937-944`): `ops.ts` and `opsgen` stay
  "for books' `operationPolling` users".
- **Evidence:**
  - **It imports ABO-only code.** `web/src/utils/operationPolling.ts:6-7 @748fd787a` imports `../services/api` and
    `../generated/ops`, both ABO-only. Moved to `core/web`, it fails the core-web import boundary (07 §7.3.1,
    "no specifier outside `core/web`").
  - **Books still needs it.** Its books importers keep legacy calls after S9: `Library.tsx:55` (`pollOperation`)
    and `ITunesImport.tsx:56`. The row and the FE4-7 rule describe two different S9 PRs.
- **Fix:** Classify `operationPolling.ts` as **books**, at `modules/books/legacy/operationPolling.ts`, unchanged,
  still on `ops.ts`. The shell's Activity page and `TaskTrackerProvider` get their own state helpers over the
  generated `TaskRunView` states. Split the row in 07 §7.11. `followOperation` and `trackedOperations` follow
  whichever side their callers are on (list them).

### FE5-7: MINOR. `ui_build_id` has two definitions

- **Where:** 03 `internal/<capability>http` row (`03-architecture.md:293`: "the `ui_build_id`, which is read from
  the build-ID meta tag of the bundle the binary embeds — its one source"); 07 §7.5.1 (`07-ui-shell.md:553-555`:
  "`""` when no shell bundle is served (zero `Bundle`, or a legacy mount owns `/`)"); 07 §7.5.2 case 2 (`:588-589`).
- **Evidence:** From S0, ABO's legacy bundle carries the meta (09:663). Under 03's wording, `/modules` reports a
  non-empty `ui_build_id` from S4 to S8. Under 07's wording it reports `""`. Case 2 ("`ui_build_id` is `""` …
  the server serves a legacy SPA") only works under 07's.
- **Fix:** In 03:293, say "the served **shell** bundle's ID (`ui.Bundle.BuildID()`), `""` while a legacy mount
  owns `/`; `X-App-Build` carries the legacy mount's ID".

### FE5-8: MINOR. Case 2 has no shell-only guard, and the pre-shell bundle runs it

- **Where:** 07 §7.5.2 (`07-ui-shell.md:579-584`: one `checkBuild` that "the pre-shell bundle and the shell bundle
  run"; case 2 `:588-589`).
- **Evidence:** From UI-0 the pre-shell ABO bundle calls the same `checkBuild`. If any S4–S8 legacy page calls
  `/api/core/v1/modules` (it exists from S4, and `restart_required` is the obvious consumer), case 2 is true
  forever: the server **does** serve a legacy SPA, and that is the bundle running. Reloading loads the same bundle,
  so the offer never clears.
- **Fix:** Case 2 applies only when the bundle is a shell build: `checkBuild(response, { shell: boolean })`, with
  the preset defining `MEDIACORE_SHELL = true`. Add a `buildId.test.ts` case: "a pre-shell bundle seeing
  `ui_build_id: ""` is not offered a reload".

### FE5-9: MINOR. Is a `dev` header a mismatch? Two answers

- **Where:** 05-http-api stage 5 (`05-http-api.md:619`: "`""` or `dev` (API-only and dev builds) means 'unknown',
  which clients never treat as a mismatch"); 07 §7.5.2 case 1 (`07-ui-shell.md:587`: "carries `X-App-Build` and
  the value differs from the define") and `:598` ("A **define** of `""` or `"dev"` never triggers it").
- **Evidence:** Take a tab with a real define against a server answering `dev`, for example after
  `make build` → `make run-api`, or a binary whose meta fell back to `dev` (FE5-1). 07 case 1 offers a reload;
  05-http-api says it never does.
- **Fix:** Pick one rule. Recommended: a `dev` or `""` **header** is not a mismatch (05-http-api's text), so add
  "and the header is neither `""` nor `dev`" to case 1. Add a `buildId.test.ts` case for it.

### FE5-10: MINOR. The S9 Basic-gate asset predicate has no API

- **Where:** 07 §7.6.3 (`07-ui-shell.md:812-817`: "from S9 `ui.Handler` supplies the same predicate for its own
  asset paths when the kernel mounts it with `Router.SPA`"); 05-http-api (`05-http-api.md:496`:
  `func (r *Router) SPA(h http.Handler) error`); 07 §7.6.1 API (no predicate is exported); 05-http-api §8.2
  `LegacyMount.BasicExemptAssets` (`:1003`).
- **Evidence:** `ui.Handler` returns a plain `http.Handler`, and `Router.SPA` takes only that. Stage 8 has no way
  to get the predicate after the legacy mount drops `ServesSPA`. `TestBasicGate_ExemptSetEqualsHEAD` "repeated in
  the S9 PR" cannot be written against the stated API.
- **Fix:** Export `func BasicExemptAssets(path string) bool` from `core/ui` (HEAD's `basicauth.go:40-46` rule).
  Then either change the signature to `Router.SPA(h http.Handler, opts SPAOptions)` with
  `SPAOptions.BasicExemptAssets`, or have the kernel pass it in P8. State it in 05-http-api §4.10, 07 §7.6.1 and
  06's P8 row.

### FE5-11: MINOR. 06's `LegacyRedirects` comment predates FE4-5

- **Where:** 06 §6.4.2 (`06-app-sdk.md:403-408`: "the app's PageMiddleware reads the Go value to record the
  ui_route compat surface (FE3-17)"); vs 06:852-854, 07:699-707, 09:588-591 (the kernel registers the entries and
  `ui.Handler` records).
- **Fix:** Reword it to "the kernel registers one `ui_route` compat entry per row at P7, and `ui.Handler` records a
  full page load that matches one (FE4-5); books fills it in the URL-move release (FE4-8)".

### FE5-12: MINOR. 09 S9 and UI-0 residues on `opsgen` and the stale-build rule

- **Where:** 09 S9 Moves (`09-abo-on-core.md:845`: "the Activity page moves to native REST and stream together
  …, retiring `tools/cmd/opsgen` (FE3-6)"; "the S0 stale-build rule moves into `@mediacore/api-client`'s runtime"
  at S9); 09 UI-0 Moves (`:742`: no `checkBuild`); vs 07 §7.5.2 (`:579-583`: from UI-0) and FE4-7's rule (05-http-api
  `:947-955`, 07 `:1115`, 04 `:879`: `opsgen` stays until its last importer is native).
- **Fix:**
  - **09 S9:** replace "retiring `tools/cmd/opsgen` (FE3-6)" with "the Activity page stops importing `ops.ts`;
    `opsgen` stays (FE4-7)". Replace "moves into" with "keeps running in".
  - **09 UI-0 Moves:** add "`@mediacore/api-client` exports `checkBuild`; ABO's `apiFetch` calls it (07 §7.5.2,
    FE4-9)".
  - **04:** 04 maps `opsgen`'s retirement at S10, but "the last `ops.ts` importer native" may never happen
    (05-http-api :947: legacy types stay "indefinitely"). 04's row should say "stays in ABO until its last importer
    is native; not a stage-bound move".

### FE5-13: MINOR. Step 4 shipped without step 5 breaks every books URL

- **Where:** 07 §7.19 (`07-ui-shell.md:1262-1280`: step 5 "ABO URLs move to `/books/...` …; steps 4 and 5 **may**
  ship in one release"); 07 §7.4.2 (`:404-408`: `createShell` builds the router from "shell routes plus module
  routes under `/<id>/…`"); 09 S9 (`:845`, `:891`).
- **Evidence:** `createShell` (step 4) mounts books' routes under `/books/`. A step-4-only release therefore moves
  `/library` etc. with **no** client redirects (they come with step 5), so every bookmark and typed URL lands on
  the shell's `NotFound`. The text also says URLs move at step 5. Either step 4 needs an unprefixed-route mode
  that the spec does not define, or the two steps cannot ship apart.
- **Fix:** Change "may ship in one release" to "**ship in one release**" in 07 §7.19 and 09 S9/§9.5 row. `ui_route`
  recording and C9's clock still start at that release (FE4-8 unchanged).

### FE5-14: MINOR. Which kinds the S5 bridge forwards: 07 vs 05-realtime

- **Where:** 07 §7.9.2 (`07-ui-shell.md:1047-1048`: "translated back to that ID … **never a `core.*` kind**");
  05-realtime §4.9 (`05-realtime.md:406`: "core kinds HEAD's streams show today keep appearing under their legacy
  IDs"); 05-tasks (`:1243`, `:1504`: every ported def has a `LegacyDefID`).
- **Evidence:** The common reading of 07 drops every core-owned kind, for example backup and maintenance runs moved
  to core at S5. They would vanish from the Activity page for the S5 release, against 05-realtime and the S5
  done-check "streams A, B and C show live progress".
- **Fix:** In 07, write "never under a `core.*` name, and never a kind without a `LegacyDefID`".

### FE5-15: MINOR. The cutover leaves the Docker frontend stage pointing at `core/web`

- **Where:** 12 §12.8 step 6 (`12-repo-split.md:570-577`: "`git rm -r core/` … the Dockerfiles stop copying
  `core/go.mod`"); step 7 (`:580`: `docker build .`); 07 §7.3.2 Docker frontend stage (`07-ui-shell.md:156-164`:
  copy `core/web/package.json`, the lockfile and member manifests, `npm ci`, build); `web-deps` stamp
  prerequisites (`:119-122`: `$(shell find core/web/packages …)`).
- **Evidence:** After `git rm -r core/`, the frontend stage's `COPY core/web/...` fails, so step 7's verification
  build fails. 12 lists only the Go-stage copy.
- **Fix:** In step 6, add: the Docker frontend stage drops its `core/web` copy, `npm ci` and build steps (back to
  `npm ci` + build in `web/`); `web-deps` drops its `core/web` prerequisites; `build_web.sh` already skips
  `core/web` "when present".

### FE5-16: MINOR. The private-repo npm fallback has the same defect as the rejected option

- **Where:** 07 §7.17 table (`07-ui-shell.md:1195-1199`: GitHub Release tarballs, "Install auth: none for a public
  repo", "fallback only if the repository stays private"; GitHub Packages "rejected: token on every machine");
  12 §12.5 step 6 and §12.6 (`12-repo-split.md:414-416`, `:426-433`: exact `"X.Y.Z"` specifiers, "GitHub Release
  tarballs for a private one").
- **Evidence:**
  - **The fallback needs a token.** It applies only to a private repository, and a private repository's release
    assets need an authenticated download, which is the reason GitHub Packages was rejected.
  - **The specifier cannot name a tarball.** An `"X.Y.Z"` specifier cannot resolve to a release tarball; it would
    need a URL specifier.
  - **The tooling cannot follow it.** Dependabot's npm updater and `check_mediacore_lockstep.py` (which reads
    versions) would not track URL specifiers either.
- **Fix:** In 07 §7.17 and 12, state the private case honestly: a token is required (an `.npmrc` with a
  fine-grained read token, the same cost as GitHub Packages), and specifiers become tarball URLs that the lockstep
  check parses for `X.Y.Z`. Or drop the private fallback and make "public core repo" a precondition of the npm
  publish step (14 Q-B already recommends public).

## Not attacked (fixed decisions)

D1–D9, R3-1..R3-5, R4-1..R4-6, the FE4-1 disposition (meta-only UI build ID, no ldflags, no boot check) and the
FE4-2 disposition (path-form invites until the shell) are taken as given. FE5-1, FE5-2 and FE5-3 work inside the
meta-only design. FE5-3 asks for a binary identity that is not the UI build ID, not for a linked build ID.
