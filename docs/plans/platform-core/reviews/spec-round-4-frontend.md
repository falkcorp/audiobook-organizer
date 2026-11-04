<!-- file: docs/plans/platform-core/reviews/spec-round-4-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8272f53-48b3-49a3-8d63-9584de645079 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: FRONTEND SHELL lens (ID prefix FE4)

Scope: (A) regression check of every round-3 FE BLOCKER and MAJOR (FE3-1..FE3-7) against 07 v1.4.0,
05-http-api v1.4.1, 05-realtime v1.3.0, 06 v1.4.1, 09 v1.3.1, 12 v1.3.0, 13 v1.4.0, 03 v1.6.1,
04 v1.3.0, 05-authn v1.4.1, 05-rbac v1.4.0 and 05-tasks v1.4.1; (B) new cross-file contradictions
introduced by the revision. Code anchors are `path:line @748fd787a`. Spec anchors are `file:line` in the
current checkout. R3-1..R3-5, the round-1 and round-2 resolutions and D1–D9 are taken as given.

## Part A: regression check of round 3

| Round 3 | Status | Note |
|---|---|---|
| FE3-1 (BLOCKER) `NonSPA` from `LegacyPrefixes` | **landed** | 03 §3.6 row, 05-http-api §3.5 and §4.10, 06 `ReservedIDs`/`LegacyPrefixes`/P8/`TestReservedSegmentsDisjointFromNonSPA`/`TestABOManifestBootsAtS9`, and 07 §7.4.3/§7.6.1 all now say `NonSPA` = `TopSegments` minus `SPAShared` (`login`, `invite`). Checked against `spa_fallback.go:72-83`: the eight segments are reproduced and `login` stays a page. |
| FE3-2 (MAJOR) build ID carrier and build paths | **half** | 05-http-api adopted "meta-only, no ldflags". 06, 07 and 09 adopted "ldflags into `app.Options.BuildID` plus a P8 equality check". These are two incompatible designs (FE4-1). The missing-header rule cannot fire (FE4-3). |
| FE3-3 (MAJOR) reload after an S9 rollback | **landed** | One rule in the API runtime, never deleted (07 §7.5.2, §7.6.3; 09 S9 Rollback, §9.10 cell b; R-test row). Rule 1 alone already covers the S9→S8 case, because the S8 header differs from the S9 define. Residue: two implementations from UI-0 to S9 (FE4-9). |
| FE3-4 (MAJOR) ABO page middleware and Access host into `ui.Handler` | **landed in the API**, wiring gap | `ui.AppOptions` and `app.Options.UIOptions`, merged by the kernel at P8 (06 §6.4.5, P8 row; 07 §7.6.1, §7.6.3; 09 S9 Moves). However, the three funcs ABO passes need objects that exist only inside `app.New` (FE4-5). |
| FE3-5 (MAJOR) legacy invite and temp-login links | **half** | The temp-login half landed (legacy `/login/temp` route, `TestLegacyTempLoginLink_EndToEnd`, §9.6 rows 31/34). The invite half did not: the legacy SPA never renders `AcceptInvite` for `/invite#token=` (FE4-2). |
| FE3-6 (MAJOR) Activity page S5–S9 client | **landed** | Hand-written legacy calls, `opsgen` kept until S9, `WithLegacyShape` gone (05-http-api :264, 05-tasks :954). Residues: `opsgen`'s consumer set and its input package (FE4-7). |
| FE3-7 (MAJOR) `/api/v1/auth/me` alias shape | **landed** | `permissions` = legacy atoms, `canonical_permissions` added, `TestMeAlias_LegacyShape`, R-test cell (05-rbac §6.3, :1055; 09 S4; 07 §7.9.1). |

The round-3 MINORs were spot-checked. FE3-8, FE3-9, FE3-10, FE3-11, FE3-13, FE3-14, FE3-15, FE3-16,
FE3-19 and FE3-20 landed, with residues in FE4-6 and FE4-11. FE3-12 landed, with a test-name residue
in FE4-10. FE3-17 landed, but its recording hook has the FE4-5 problem. FE3-18 contradicts 07 (FE4-4).

## Part B: findings

| ID | Severity | Title |
|---|---|---|
| FE4-1 | MAJOR | Two incompatible build-ID designs: 05-http-api says meta-only with no ldflags; 06, 07 and 09 say ldflags into `app.Options.BuildID` with a boot-fatal P8 check |
| FE4-2 | MAJOR | FE3-5 half-fixed: the legacy SPA cannot open `/invite#token=`, so from S4 r2 to S9 every new invite lands the invitee on `/login` |
| FE4-3 | MINOR | Stale-build case 3 (missing header plus `X-Request-ID`) can never fire: HEAD writes neither header, and every build from S2 writes both |
| FE4-4 | MINOR | `localStorage`: 07 says "read in place, never moved"; 09 S9 and C9 say "copied until C9, then copies dropped"; and the shell-classified `storageKeys.ts` is mostly books keys |
| FE4-5 | MINOR | `app.Options.UIOptions` is built in `main` before `app.New`, but the `ui_route` compat handle, CF recovery state and the config façade exist only after the kernel's phases |
| FE4-6 | MINOR | `internal/modulehttp` (L6, S4) has no L6 type for the module UI lists: `ui.ModuleManifest`/`UIIDs` land at S9, and `module.UIDescriptor` is L7 |
| FE4-7 | MINOR | `opsgen`: "its only consumer" is false (16 files reach `ops.ts`), its input package moves at S5, and 05-http-api §7 says it stays indefinitely |
| FE4-8 | MINOR | 07 §7.19 puts the `/books/...` URL move (step 5) after shell adoption (step 4), but 09 S9 and C9 start `ui_route` recording at step 4 |
| FE4-9 | MINOR | "One rule, one place" has two implementations from UI-0 to S9 (`apiFetch` and the api-client runtime); 05-http-api §7's middleware list omits the rule |
| FE4-10 | MINOR | The S5 bridge test has two names, and 05-tasks' description ("a native run") contradicts the `LegacyDefID`-only rule |
| FE4-11 | MINOR | The new root-file rule serves `/index.html` as a plain file: no `PageMiddleware` (CF recovery), `no-cache` instead of `no-store` |

Counts: **0 BLOCKER, 2 MAJOR, 9 MINOR** (11 findings).

### FE4-1: MAJOR. Two incompatible build-ID designs (FE3-2 half-fixed; collision of FE3-2 and MOD3-14)

- **Where:**
  - **Meta-only design:** 05-http-api §4.2 stage 5 (`05-http-api.md:613`: "**One source, the embedded
    bundle (FE3-2):** only `build_web.sh` generates the ID … there is no ldflags value. Stage 5 writes,
    from S9, `app.Options.UI.BuildID()` … before S9 the mount's `LegacyMount.BuildID` … otherwise
    `dev`"); §8.2 `LegacyMount.BuildID` comment (`:984`); history (`:1318`).
  - **ldflags design:** 06 §6.4.5 `Options.BuildID` (`06-app-sdk.md:722-737`), P8 row (`:1203`),
    failure row (`:1304`), `ProblemBuildID` (`:1010`), test (`:1383`). 06's own cross-reference row
    (`:2021`) claims that 05-http-api says "stage 5 writes `X-App-Build` from `app.Options.BuildID`",
    which is false. Also 07 §7.6.3 "One build ID, one source" (`07-ui-shell.md:788-812`) and §7.17/§7.18
    (`:1059-1071`), and 09 S0 "Build ID and reload check" (`09-abo-on-core.md:565`) and Docker row (`:558`).
- **Evidence:**
  - **Contradiction.** The two designs disagree on what stage 5 writes (the embedded meta, or the
    linked value), on whether ldflags carry the ID, and on whether P8 has a check. An implementer
    cannot satisfy both texts. Round-3 dispositions accepted both FE3-2 ("no ldflags") and MOD3-14
    ("ARG in both stages, ldflags") "as written", and the files split along that line.
  - **The ldflags design has failure modes the meta-only design cannot have:**
    - **Boot failure for a cosmetic defect.** P8 `ProblemBuildID` is fail-closed, so a pipeline slip
      turns a reload banner into an outage on deploy.
    - **Make variable expansion.** "Once per `make build`" holds only if the Makefile defines
      `BUILD_ID` with `:=` and `export`s it. A recursively expanded `BUILD_ID = $(shell date …)` is
      re-evaluated for the `web-build` recipe (`Makefile:200 @748fd787a`, `npm run build`) and again
      for the `go build` recipe (`:156`), so every `make build` fails P8.
    - **The release path cannot carry the env var.** `release-prod.yml:44-58 @748fd787a` calls the
      pinned external `falkcorp/github-common/.github/workflows/reusable-release.yml@861d4e8…`, with
      `pre-build-script: 'cd web && npm ci && npm run build'` as an *input* of that workflow (it is not
      a goreleaser feature). A caller cannot export an env var into a called reusable workflow's jobs.
      goreleaser's `{{ .Env.BUILD_ID }}` then fails template evaluation when the variable is unset,
      which breaks the production release. Making it work needs a change in another repository, which
      the spec does not list.
    - **Stale text.** "the Makefile's three ldflags sets" (07 `:799`): `GO_LDFLAGS` is defined once
      (`Makefile:76`) and used by four recipes (`:156,162,168,177`). `make docker` (`Makefile:822`)
      passes only `APP_VERSION`.
  - **The meta-only design propagates nothing.** The server reads what its own bundle carries. It
    needs no P8 check, because a mismatch is impossible by construction. The Docker frontend stage and
    goreleaser's pre-build script can each let `build_web.sh` generate an ID locally.
- **Fix:** Adopt 05-http-api's meta-only design in every file, as FE3-2 recommended.
  - **06:** delete `Options.BuildID`, `ProblemBuildID`, the P8 build-ID check and its failure row, and
    the ldflags half of `TestBuildID_HeaderEqualsEmbeddedMeta`. Fix the `:2021` cross-reference row.
  - **07 §7.6.3, §7.17/§7.18 and 09 S0:** "`build_web.sh` uses `$BUILD_ID` if set, else generates
    `<UTC timestamp>-<8 random hex>` itself, before Vite runs. No Go link step receives it. Before S2,
    ABO's `main` reads the meta from `WebFS` and writes `X-App-Build` itself."
  - **MOD3-14:** reduce it to `ARG BUILD_ID` in the Docker **frontend** stage only (optional, for
    reproducible IDs).
  - **goreleaser:** keep it unchanged.
  - If the owner prefers to keep ldflags, write it the other way in 05-http-api. In that case also
    specify `BUILD_ID := …` plus `export` in the Makefile, add the change to `reusable-release.yml` as a
    cross-repo prerequisite, use `{{ index .Env "BUILD_ID" }}` with a non-empty assertion, add
    `--build-arg BUILD_ID` to `make docker`, and downgrade `ProblemBuildID` to a boot WARN plus a
    `/readyz` detail, not a refusal to boot.

### FE4-2: MAJOR. FE3-5 half-fixed: the legacy SPA cannot open `/invite#token=`, so from S4 r2 to S9 every new invite lands the invitee on `/login`

- **Where:** 09 S4 "Invite and temp-login links" (`09-abo-on-core.md:673`: "`AcceptInvite` reads
  `#token=` as well as the path parameter"); 07 §7.9.1 last bullet (`07-ui-shell.md:920-929`), §7.4.3
  (`:409-420`); 05-authn §4.13 (`:906`); test rows `TestLegacyInviteLink_302NoToken` (07 `:1153`) and
  `TestLegacyInviteRedirect_SPASharedSegment` (05-http-api `:1191`).
- **Evidence:**
  - **Where the fragment link goes.** From S4 core mints `/invite#token=…`, and the legacy 302 also
    targets `/invite#token=…`. Both arrive with `location.pathname === "/invite"`.
  - **What the legacy SPA does with it.** Its public invite branch is entered only when
    `location.pathname.startsWith('/invite/')` (trailing slash), and its only route is
    `/invite/:token` (`web/src/App.tsx:201-206 @748fd787a`). For `/invite`:
    - An unauthenticated invitee falls into the unauthenticated branch, whose `*` navigates to
      `/login` and drops the fragment (`App.tsx:249-250`).
    - A signed-in user hits the authenticated `*` route and gets `NotFound` (`App.tsx:447`).
  - **What the spec changes.** It only changes what `AcceptInvite` reads. `AcceptInvite` is never
    mounted for `/invite`, so the change has no effect.
  - **What the tests cover.** The 302 tests assert only the redirect and that `GET /invite` "serves the
    SPA page". No test opens the link end to end on S4, unlike the temp-login half.
- **Fix:** In 09 S4 and 07 §7.9.1, state that the S4 build changes the guard to
  `pathname === '/invite' || pathname.startsWith('/invite/')` and adds
  `<Route path="/invite" element={<AcceptInvite />} />` beside `/invite/:token`. `AcceptInvite`
  prefers `#token=`, removes it with `history.replaceState`, and calls
  `POST /api/core/v1/auth/invites/lookup|accept`. Add `TestLegacyInviteLink_EndToEnd` to the S4
  done-check, next to `TestLegacyTempLoginLink_EndToEnd`: create an invite on S4 r2, open the link
  signed out, accept it, and end up signed in. Also run it signed in, where it must show the invite
  page, not `NotFound`.

### FE4-3: MINOR. Stale-build case 3 can never fire

- **Where:** 07 §7.5.2 item 3 (`07-ui-shell.md:562-564`: "`X-App-Build` is missing on a 2xx or 4xx
  response that carries `X-Request-ID` … a pre-S0 server"); 09 S0 (`:565`); 05-http-api stage 5
  (`:613`); `buildId.test.ts` row (07 `:1141`).
- **Evidence:** HEAD writes no `X-Request-ID` (05-http-api's own inventory, `:124`: "0 non-test files
  mention `X-Request-ID`"). The header first appears with stage 1 at S2, and from S2 stage 5 writes
  `X-App-Build` on every response. A response that lacks `X-App-Build` but carries `X-Request-ID`
  therefore never exists. The case the rule names (an S0+ tab after a rollback to a pre-S0 server) gets
  no prompt. The guard I proposed in round 3 was wrong. HEAD does mark every one of its responses:
  `securityHeadersMiddleware` sets `Reporting-Endpoints: csp="/api/v1/csp-report"` globally
  (`internal/server/server_middleware.go:102-109 @748fd787a`). From S2, stage 5 writes
  `Reporting-Endpoints` with every CSP. A proxy's 502 and an Access interstitial carry neither.
- **Fix:** Replace the discriminator with "carries a `Reporting-Endpoints` header naming a `csp`
  group". Change 07 §7.5.2 item 3, 09 S0, 05-http-api stage 5 and `buildId.test.ts` to match.
  Alternatively, delete case 3 and state that a tab opened on S0+ gets no prompt after a rollback to
  pre-S0 (low cost).

### FE4-4: MINOR. `localStorage`: three statements, and the shell-classified key file is mostly books keys

- **Where:** 07 §7.7 (`07-ui-shell.md:833-835`: "read in place, never moved or renamed, during the
  extraction"; history `:1324`); 09 S9 UI routes (`09-abo-on-core.md:744`: "**copied, not moved**,
  until C9"); 09 C9 (`:810`: "the legacy `localStorage` copies dropped"); 07 §7.11 row (`:1034`:
  `lib/storageKeys.ts` → shell); 07 §7.4 `storageKey(scope, name)` = `${appId}.${scope}.${name}`
  (`:386`).
- **Evidence:**
  - **Copying breaks rollback.** Under "copy", S9 writes the new key. After a rollback, S8 reads the
    old key, so every preference changed on S9 silently reverts. C9 then deletes the old keys inside
    the extraction, which a rollback past C9 would lose.
  - **The key file is mostly books keys.** `web/src/lib/storageKeys.ts:7-30 @748fd787a` has 21 keys,
    13 of them audiobook-specific (`itunes_import_settings`, four `library*`, six `metadata-review-*`,
    two `dedup-*`). Moving the file to the shell puts audiobook names into core (R1).
  - **The Activity keys would reset.** The two keys the shell's Activity page reads
    (`activity-source-prefs`, `activity-ops-pinned`) would become `abo.shell.*` under `storageKey`,
    which resets them at S9. That contradicts "every preference kept".
- **Fix:**
  - **09 S9 and C9:** use 07's rule ("read in place, never moved or renamed; no copies"), and delete
    "legacy `localStorage` copies dropped" from C9.
  - **07 §7.11:** split `storageKeys.ts`. The books keys go to `web/src/modules/books/storageKeys.ts`
    with their literal values. The shell keys keep their literal legacy values through a
    `createShell({ legacyStorageKeys: { themeMode, activitySourcePrefs, activityOpsPinned } })` map,
    and `storageKey()` is used only for keys that do not exist today.

### FE4-5: MINOR. `UIOptions` is a compile-time value in `main`, but what it must reach exists only after `app.New`'s phases

- **Where:** 06 §6.4.5 `UIOptions` (`06-app-sdk.md:772-783`: "its funcs close over whatever the app's
  main can reach … `ui_route` recording … re-read per response from its config façade's atomic
  pointer"); 07 §7.6.1 `AppOptions` (`07-ui-shell.md:619-643`); 09 §9.2.9 `compat.Ledger.Register` →
  `*Handle` (built by the kernel "before listeners open").
- **Evidence:** `main` builds `app.Options` before `app.New`. What the three funcs need appears later:
  - The `ui_route` recorder needs a `compat.Handle`. Only the kernel's `Ledger.Register`, called
    inside `app.New`, hands one out.
  - CF service-token recovery needs ABO's CF config and token store, which today come from server
    state (`internal/server/cf_recovery.go:36-60 @748fd787a`).
  - The team-host pointer is filled by books' config façade.

  None of these exists when `main` builds `app.Options`. Without a stated late-binding rule, an
  implementer either reaches for package globals (R5) or calls a nil handle on the first page load.
- **Fix:**
  - **`ui_route`:** make it core-generic. `UIDescriptor.LegacyRedirects` is already in the manifest,
    so the kernel registers one `compat.Entry{Surface: "ui_route"}` per redirect at P7, and
    `ui.Handler` calls `Handle.Use` when a full page load matches one. Remove `ui_route` from ABO's
    `PageMiddleware` in 06, 07 and 09.
  - **CF recovery and `ConnectSrcFrom`:** state that ABO's funcs read an `atomic.Pointer` that books
    fills in its Start hook. Until it is filled they are a no-op or return an empty list, and P8 only
    wraps them.
  - **Test:** a page load during boot (before books Start) serves `index.html` with no panic.

### FE4-6: MINOR. `internal/modulehttp` at S4 has no L6 type for the module UI lists

- **Where:** 06 §6.4.6 (`06-app-sdk.md:944-948`: `ModulesView` is "the catalog snapshot …", and
  `modulehttp` "imports nothing above L6"); 07 §7.5.1 (S4) and §7.6.1 (`ui.ModuleManifest`, `UIIDs`,
  `Redirect` in `ui`, "the rest of the package lands at S9", `:690`); 06 §6.4.10 S9 row
  (`Description.UIManifests`); 03 R4.
- **Evidence:** `/api/core/v1/modules` reports `modules[].ui` from S4 (07 §7.5.1 JSON). The only L6
  types for those lists are `ui.ModuleManifest`/`UIIDs`, which land at S9 (only `ReservedSegments` is
  at S2). `module.UIDescriptor` is L7, so `modulehttp` (L6) cannot import it. As written, the S4
  package has no type to return. This is the FE3-8 class again.
- **Fix:** Land `ui.ModuleManifest`, `UIIDs` and `Redirect` as dependency-free leaf types with
  `ReservedSegments` (at S2, or at the latest at S4). State this in 04 §8.1, 06 §6.4.10 and 07 §7.6.1.
  Alternatively, define `ModulesView` over `modulehttp`-local DTOs.

### FE4-7: MINOR. `opsgen` residues

- **Where:** 07 §7.8 (`07-ui-shell.md:876-878`: "until S9, when its only consumer moves to native
  routes"), §7.11 row (`:1044`); 04 `opsgen` row (`04-package-mapping.md:282`) and S5 row (`:770`:
  `internal/operations/state` → `mediacore/tasks`); 05-http-api §7 (`05-http-api.md:937`: "tygo … and
  opsgen stay for the types of routes that remain on the legacy mount, which may be indefinitely").
- **Evidence:**
  - **More than one consumer.** `ops.ts` is imported by `web/src/services/api.ts:14-15,590` and by
    `web/src/utils/operationPolling.ts:7 @748fd787a`. `operationPolling` has 16 non-test importers,
    including books pages that keep legacy calls after S9 under step 6: `Library.tsx`, the review lanes,
    `ITunesImport.tsx`, `stagedMetadataApply.ts`, `MaintenanceTab.tsx` and `ThrottlePanel.tsx`.
  - **The input package moves early.** `opsgen` reads `internal/operations/state` (04 `:282`), which
    moves to `mediacore/tasks` at S5, while `opsgen` stays in ABO until S9.
  - **The retirement stage disagrees.** 05-http-api keeps `opsgen` indefinitely; 04 and 07 retire it
    at S9.
- **Fix:**
  - **07 and 04:** `opsgen` retires when the last `ops.ts` importer is native. Either the S9 PR
    rewires `operationPolling` and `api.ts`' operation types for all 16 importers (list them), or
    `opsgen` stays until step 6 finishes.
  - **S5:** `opsgen` is repointed to `mediacore/tasks`' `State` constants, and its freshness gate
    proves `ops.ts` byte-identical.
  - **05-http-api §7:** align with that rule.

### FE4-8: MINOR. Step order: the URL move comes after the shell adoption, but `ui_route` recording starts with the adoption

- **Where:** 07 §7.19 step 4 (shell adoption with `UIOptions`, including `ui_route` recording) and
  step 5 ("ABO URLs move to `/books/...` with redirects"); 09 S9 Moves (`:743`: "… `ui.Handler` mounts
  from this build. Then step 5 …"), UI routes (`:744`), C9 (`:810`, "R(S9) + W … zero on
  `ui_route`").
- **Evidence:** If step 4 ships before step 5, `/library` and the other legacy page paths are still
  the real pages, so `PageMiddleware` records every ordinary page load as legacy use. The C9 clock
  then starts at a release in which nothing has moved yet. 09's S9 rollback text ("Bookmarks to
  `/books/...` 404 …") and its Done-when (G3 intersection) assume step 5 has already happened.
- **Fix:** State either that steps 4 and 5 ship in one release, or that `ui_route` entries are
  registered (and the C9 clock R starts) only in the step-5 release. Use the same wording in 07 §7.19
  and 09 S9.

### FE4-9: MINOR. One stale-build rule, two implementations, UI-0 to S9

- **Where:** 07 §7.5.2 (`:553-556`: "one rule, one place … `apiFetch` from S0, and
  `@mediacore/api-client`'s runtime from the moment `apiFetch` moves into it"), §7.8 (`:857-861`:
  "carried over from ABO's `apiFetch`"), §7.11 row (`:1039`, `apiFetch.ts` moves at S9);
  05-http-api §7 middleware list (`05-http-api.md:928-931`, no stale-build item); 05-http-api
  `:1079` ("calls to legacy routes keep `apiFetch`").
- **Evidence:** From UI-0, S4–S8 native calls go through the api-client runtime, while legacy calls
  still go through ABO's `apiFetch`. Both must run the rule, so it exists twice for about five stages.
  05-http-api, which owns the runtime's middleware list, does not list the rule at all.
- **Fix:** From UI-0, the api-client runtime exports `checkBuild(response)`, and ABO's `apiFetch`
  calls it, so there is one implementation. Add "the stale-build rule of 07 §7.5.2" to 05-http-api §7's
  middleware list.

### FE4-10: MINOR. The S5 bridge test has two names

- **Where:** 05-tasks test table (`05-tasks.md:1293`: `TestLegacyStreamBridge_FeedsABC`, "streams A,
  B and C show live progress for a native run"); 05-realtime (`05-realtime.md:502`) and 09 S5
  Done-when (`:693`): `TestS5Bridge_LegacyStreamsShowCoreRuns`, "under its `LegacyDefID`".
- **Evidence:** It is one test with two names. "A native run" reads as a run without a legacy ID,
  which by 05-tasks' own step 3 (`:1056-1059`) reaches no stream.
- **Fix:** Use `TestS5Bridge_LegacyStreamsShowCoreRuns` in 05-tasks, worded "a run of a kind with a
  `LegacyDefID`, executed by the core executor".

### FE4-11: MINOR. The root-file rule serves `/index.html` around `PageMiddleware`

- **Where:** 07 §7.6.1 `Handler` comment (`07-ui-shell.md:672-679`: "any other existing regular file
  of the bundle's fsys … `no-cache` … checked before the extension rule").
- **Evidence:** `index.html` is itself a regular file at the dist root, so `GET /index.html` is served
  `no-cache` with no `PageMiddleware`. HEAD runs its SPA middleware (CF recovery) ahead of every
  `NoRoute` file (`internal/server/static_embed.go:59-66 @748fd787a`).
- **Fix:** Add "except `index.html`, which is always served by the index rule (`no-store`, full CSP,
  through `PageMiddleware`)". Add `/index.html` to `TestHandler_IndexNoStore`.

## Not attacked (fixed decisions)

D1–D9, R3-1..R3-5 (`TopSegments`/`SPAShared` included), cross-cutting resolutions 2 and 9 and
R2-1..R2-8 are taken as given. The findings challenge only whether the written frontend design works
and agrees across files.
