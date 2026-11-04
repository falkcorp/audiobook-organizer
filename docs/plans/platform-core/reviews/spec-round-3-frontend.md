<!-- file: docs/plans/platform-core/reviews/spec-round-3-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: b5e64dd4-aec4-4072-9d62-3b7cb62721b1 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: FRONTEND SHELL lens (ID prefix FE3)

Scope: (A) regression check of every round-1 and round-2 FE BLOCKER/MAJOR against 07 v1.3.0,
05-http-api v1.3.1, 05-realtime v1.2.0, 06 v1.3.1, 09 v1.2.1, 12 v1.2.0, 13 v1.3.0, 03 v1.5.1,
04 v1.2.1, 05-authn v1.3.1; (B) new cross-file inconsistencies introduced by the revision. Code
anchors are `path:line @748fd787a` (`git show 748fd787a:<path>`); spec anchors are `file:line` in the
current checkout. The round-1 and round-2 resolutions (cross-cutting 2 and 9, R2-1..R2-8) and D1–D9
are taken as given.

## Part A: regression check

Round 1 (FE-1..FE-19, the BLOCKERs and MAJORs): all landed as recorded in round 2, except the
composition half of **FE-3** (app SPA middleware) and the S9 half of **FE-4** (the Access host in
`connect-src`): `ui.Options` has the fields, but nothing carries them from the app to the
kernel-built `ui.Handler` (FE3-4).

| Round 2 | Status | Note |
|---|---|---|
| FE2-1 legacy CSP per request | **landed for S2–S8** (05-http-api stage 5, §8.2 `DefaultCSP func(*http.Request) string`, `CSPReport`; 07 §7.6.2) | regresses at S9: FE3-4 |
| FE2-2 plain-HTTP election fallback | landed (07 §7.9.2, 05-realtime §4.8 item 7, `tabs.spec.ts` non-loopback) | 07 omits 05-realtime's last-resort mode: FE3-15 |
| FE2-3 Activity page stream source per stage | **half** | the S5–S9 client is not implementable as worded, and three files describe three different bridges: FE3-6, FE3-12 |
| FE2-4 `renderInShell` after providers move | landed (07 §7.4.4, §7.19 step 4; 04 §8.1 S9; 09 UI-0/S9) | — |
| FE2-5 one build ID, `X-App-Build` | **half** | no carrier into core, and one value cannot span the four build paths: FE3-2; the S9 deletion leaves rollback tabs without a prompt: FE3-3 |
| FE2-6 `NonSPA` covers probes | landed for S9 (05-http-api §4.10, 07 §7.6.1) | S8 window still answers HTML: FE3-13 |
| FE2-7 no `/logout` page, P8 disjointness | landed | 06's and 07's definition of the `NonSPA` input makes the check fire on ABO's own `/login`: FE3-1 |
| FE2-8 invite/temp-login fragment links | **half** | nothing serves the 302s before S9, and 09/07 omit the legacy `/login/temp` page that 05-authn requires: FE3-5 |
| FE2-9 `core.json` producer | landed (06 §6.4.10, 04 §8.1 S2, 09 S2/UI-0) | — |
| FE2-10 `UIBundle()` pattern | landed (07 §7.6.3, §7.12; 09 S9) | — |
| FE2-11 reserved-ID owners, split compat inputs | **half**: 03 and 05-http-api agree; 06 and 07 use a different `NonSPA` input | FE3-1 |
| FE2-12 build ID before bundling | landed | its plumbing: FE3-2 |
| FE2-13 v0 rule, package versions | landed (07 §7.5.2, 12 §12.4.3) | — |
| FE2-14 ordered `-w` list | landed (07 §7.3.2, 09 S0, 12 SC8 and §12.5 step 6) | — |
| FE2-15 stamp target | landed | glob nit in FE3-20 |
| FE2-16 absorbed-page redirects, `ui_route` | landed | Go-side list source: FE3-17 |
| FE2-17 ESLint AST rule | landed (07 §7.9.1, §7.18; 09 S4) | — |
| FE2-18 route names | landed for auth/rbac/tasks | settings path and stage still wrong: FE3-11 |
| FE2-19 122 files, UI-0 scope | landed (13 E4, 14 K4, 07, 09) | — |
| FE2-20 manual redirect, one hook signature | landed | — |
| FE2-21 leader subscription set | landed (`exclude=`, 05-realtime §4.1) | — |
| FE2-22 no GitHub Packages | landed (12 §12.5, §12.6) | — |
| FE2-23 CSRF wording, `X-Forwarded-Host` | landed | dev-proxy case: FE3-19 |
| FE2-24 stale text | landed, except `WithLegacyShape` (named in 07, 09; defined nowhere) | FE3-6 |
| FE2-25 S9 preconditions, `/modules` at S4 | landed | `/modules` route package unnamed and its layering: FE3-16 |

## Part B: findings

### FE3-1: BLOCKER. 06 and 07 make every first segment of `Manifest.LegacyPrefixes` a `NonSPA` segment, so ABO's ABS `/login` becomes non-SPA and the FE2-7 boot check refuses ABO at S9 (FE2-11 half-fixed)

- **Where:** 06 `module.ReservedIDs` comment (`06-app-sdk.md:144-156`: "They are also NonSPA segments"), `Manifest.LegacyPrefixes` (`:268-277`: "its first segment is a reserved module ID and a NonSPA segment"), test `TestReservedSegmentsDisjointFromNonSPA` (`:1149`: "a legacy prefix whose first segment is a shell route segment fails P8"), cross-ref row (`:1701`); 07 §7.6.1 `Options.NonSPA` comment (`07-ui-shell.md:585-597`: "every compat top-level segment (the first segments of Manifest.LegacyPrefixes)"). Against: 05-http-api §3.5 (`05-http-api.md:500-515`), §4.10 (`:744-753`), §8.2 `TopSegments`/`SPAShared` (`:910-911`); 03 §3.6 Module ID row (`03-architecture.md:401`).
- **Evidence:** ABS registers `POST /login` at the top level (`internal/server/handlers/abs/handler.go:674 @748fd787a`), and today's code deliberately keeps `login` out of the non-SPA list because `GET /login` is the SPA page (`internal/server/spa_fallback.go:72-75 @748fd787a`). 06 says only paths listed in `LegacyPrefixes` may be passed to `MountLegacy`, so ABO's manifest must list `/login`. Under 06/07, `login` is then a `NonSPA` segment. At S9, `login` is also a shell segment (`ui.ReservedSegments`), so the P8 check (`ProblemSPASegmentCollision`) refuses to boot, and 06's own test asserts exactly that outcome. If the check were skipped, `GET /login` would answer a problem+json 404. 05-http-api and 03 avoid this: `NonSPA` comes from `LegacyMount.TopSegments`, minus `SPAShared` (`login`). The same trap applies to the `invite` segment if the S4 legacy 302 route of FE3-5 is a gin route.
- **Fix:** Use 05-http-api's model in 06 and 07. In 06 `ReservedIDs` and `LegacyPrefixes`, write that `LegacyPrefixes` first segments are **reserved IDs only**, and that `NonSPA` = `LegacyMount.TopSegments`, never including `SPAShared`. Change `TestReservedSegmentsDisjointFromNonSPA` to "a `TopSegments` entry that is a shell segment fails P8; a `SPAShared` segment (`login`) that is a shell segment passes". Add `SPAShared` to 06's cross-ref row. In 07 §7.6.1, replace "the first segments of Manifest.LegacyPrefixes" with "every mount's `LegacyMount.TopSegments` (not `SPAShared`)". Add a kernel test that boots ABO's real S9 manifest.

### FE3-2: MAJOR. The single build ID has no carrier into core, and "one value per `make build`" cannot span ABO's four build paths, so every Docker and goreleaser build shows a permanent reload offer (FE2-5 half-fixed)

- **Where:** 09 S0 "Build ID and reload check" (`09-abo-on-core.md:537`); 05-http-api stage 5 (`05-http-api.md:570`); 07 §7.6.3 "One build ID, one source" (`07-ui-shell.md:728-743`), §7.10 `define`.
- **Evidence:**
  - **No carrier into core.** Stage 5 is core `httpapi`, but neither `app.Options` (06 §6.4.5) nor `httpapi.Deps` (`05-http-api.md:452-459`) has a build-ID field. "Passed to `go build -ldflags`" names an ABO `main` variable, which core cannot read.
  - **One value cannot span the build paths.** 07 has `build_web.sh` derive the ID and "pass it to `go build -ldflags`", but that script runs no `go build`. The Dockerfile builds the frontend in one stage (`Dockerfile:13-21 @748fd787a`) and links Go in another, with its own `-ldflags ... -X main.version=${APP_VERSION}` (`Dockerfile` go-builder stage). goreleaser links four targets with `-X main.date={{.Date}}` (`.goreleaser.yml:30-34 @748fd787a`). The Makefile has two more ldflags sets (`Makefile:76,156,177`). A "git commit plus build timestamp" generated separately in each invocation therefore never equals the one Vite embedded, so production bundles see a mismatch on every response, and reloading does not clear it. The S9 shell banner compares `ui_build_id` (read from the meta tag) with the define, while `X-App-Build` comes from ldflags. These are two server-side sources for the "one" ID.
  - **False positives.** "A missing header counts as a mismatch" also fires on responses that do not come from the app, such as a reverse proxy's 502 during the restart of an upgrade or an Access interstitial.
- **Fix:** Make the embedded bundle the single source. The server reads the ID from the `<meta name="mediacore-build">` of the `index.html` it embeds: `ui.Bundle.BuildID()` from S9; before S9, a new `LegacyMount.BuildID string` that ABO fills from its embedded `web/dist/index.html`. Stage 5 writes that value: from S9 `app.Options.UI.BuildID()`, otherwise the mount's `BuildID`, otherwise `"dev"`. Only `build_web.sh` generates the ID, before Vite runs; no ldflags. State this in 09 S0, 05-http-api stage 5 and §8.2, and 07 §7.6.3. Count a missing header as a mismatch only on a 2xx/4xx response that carries `X-Request-ID` (the perimeter's stage 1), never on a 5xx without it. Add `TestBuildID_HeaderEqualsEmbeddedMeta` for a `make build` and a Docker build.

### FE3-3: MAJOR. Rolling back from S9 leaves open shell tabs with no reload prompt: the S0 header check is deleted at S9, and the shell banner skips `ui_build_id == ""`, which is exactly what the S8 server reports

- **Where:** 09 S9 Rollback (`09-abo-on-core.md:711`: "the S0 build check offers a reload to tabs left open"); §9.10 R-test cell (b) ("new tabs, S0 reload prompt"); 07 §7.5.2 (`07-ui-shell.md:528-533`), §7.5.1 (`:502-503`: `""` when a legacy mount owns `/`), §7.6.3 (`:741-742`: "the S0 check is deleted"), §7.19 step 4.
- **Evidence:** After a rollback from S9 to S8, the tabs left open run S9 shell code, which no longer has the S0 check. The S8 server answers `/api/core/v1/modules` (shipped since S4) with `ui_build_id: ""`, and the banner fires only when both IDs are non-empty. Those tabs keep running the shell against a legacy-SPA server: `/books/...` URLs exist only client-side, and a reload of any of them lands on the S8 SPA's NotFound. The rollback text promises a mechanism that S9 deletes, and cell (b) at S9 cannot pass.
- **Fix:** Do not delete the header comparison at S9; move it into `@mediacore/api-client`'s middleware (07 §7.8 list), so that it runs on every API response in both the pre-shell and shell bundles. Define one rule: offer a reload when the response's `X-App-Build` (or `/modules.app.ui_build_id`, whichever is present) differs from the bundle's define, **or** when `ui_build_id` is `""` while `X-App-Build` is non-empty and not `dev`. Correct 09 S9 Rollback and §9.10 cell (b), and add an S9 R-test row: "S9 tab, roll back to S8, reload offered".

### FE3-4: MAJOR. At S9 nothing carries ABO's `PageMiddleware` (CF service-token recovery, `ui_route`) or its Access host (`ConnectSrcFrom`) into the kernel-built `ui.Handler`, so FE-3 and FE2-1 regress at shell adoption

- **Where:** 07 §7.6.1 `Options.PageMiddleware`, `ConnectSrcFrom`, `ExtraDirectives` (`07-ui-shell.md:599-611`), §7.6.3 row 2 (`:701`: "ABO passes `PageMiddleware`"); 06 `app.Options` (`06-app-sdk.md:612-660`: only `UI ui.Bundle`), §6.4.10 S9 row (`:940`); 06 P8 (`:976`: the kernel mounts `ui.Handler`); 09 S9 Moves and UI routes (`09-abo-on-core.md:709-710`).
- **Evidence:** `grep -n "PageMiddleware\|ConnectSrcFrom\|ExtraDirectives"` across the spec finds them only in 07 and in 09's `ui_route` sentence. The kernel builds `ui.Options` (`NonSPA` comes from `Router.NonSPA()`), and an app can reach it only through `app.Options.UI`, which is a `Bundle`. At S9:
  - CF service-token recovery, which today runs on SPA page loads (`internal/server/cf_recovery.go:36-60`, `static_embed.go:59-64 @748fd787a`), stops.
  - The Access team host leaves `connect-src`, which is the defect FE2-1 fixed for S2–S8, now at S9: `apiFetch` can no longer recognise an Access expiry redirect (`csp.go:34-46`).
  - The `ui_route` usage gate (FE2-16) has no hook.
  - `core.ui.csp.connect_src` is a static operator key, not ABO's per-response re-read of its own config.
- **Fix:** Add `app.Options.UIOptions func(deps) ui.AppOptions` (S9 row of 06 §6.4.10), with `AppOptions{PageMiddleware, ConnectSrcFrom, ExtraDirectives}`. Alternatively use a struct literal with a `ConnectSrcFrom func() []string`, as `Authenticators` does for identity glue. The kernel merges it into the `ui.Options` it builds in P8. Name ABO's values in 09 S9 Moves: CF recovery from `internal/cfaccess`, `ui_route` recording, and the team host from `modules.books.identity.*`. Extend 07's port of `static_embed_recovery_test.go` to boot through `app.New`, not a hand-built `ui.Handler`.

### FE3-5: MAJOR. FE2-8 half-fixed: from S4 to S9 nothing serves the legacy-link 302s, and 09 S4 and 07 omit the legacy `/login/temp` page that 05-authn requires, so admin reset and temp-login links break from S4 r3 to S9

- **Where:** 09 S4 "Invite and temp-login links" (`09-abo-on-core.md:639`), S4 UI row, Done-when; 07 §7.4.3 (`07-ui-shell.md:393-401`: the 302 is "answered by `ui.Handler`"), §7.9.1 last bullet (`:839-843`: only `AcceptInvite` changes); 05-authn §4.13 "Legacy links" (`05-authn.md:833-841`: "adds a `/login/temp` page that reads the fragment"); 05-http-api §3.5 (`ErrPrefixCollision` for a top-level legacy path outside `TopSegments`/`SPAShared`).
- **Evidence:**
  - **The 302s.** `ui.Handler` is not mounted before S9 (07 §7.6.3 row 1). Between S4 and S9, `GET /invite/<token>` reaches gin's `NoRoute`, which serves the SPA, so no 302 occurs. If ABO adds a gin route for it, `invite` must be declared, and 05-http-api offers only `TopSegments` (which would make `/invite` non-SPA and fail P8 at S9) or `SPAShared`. No file says which.
  - **The temp-login page.** Today `GET /auth/temp-login?token=` consumes the token server-side and redirects to `/` (`internal/server/auth_temp_login.go:156-224 @748fd787a`). The password reset flow mints the same link (`auth_reset_password.go:97`). From S4 that link 302s to `/login/temp#token=`. The legacy SPA has no such route. Its unauthenticated branch sends `*` to `/login` (`web/src/App.tsx:249-250 @748fd787a`), which drops the fragment. 05-authn fixes this with a new legacy page, but 09's S4 checklist, its done-check and 07 §7.9.1 list only `AcceptInvite`.
  - **The R-test cell.** "invite created on cur opens … in prev UI" contradicts S4 Rollback: "Invites issued on r3 are void on r1".
- **Fix:**
  - **09 S4 and 07 §7.9.1:** add "the same build adds a `/login/temp` route to the legacy SPA's unauthenticated and authenticated branches, reading `#token=` and calling `POST /api/core/v1/auth/temp-login/consume`". Add `TestLegacyTempLoginLink_EndToEnd` (admin reset on S4 r3 → link → logged in) to the S4 done-check.
  - **The 302s:** from S4 to S9 they are books legacy manifest routes `GET /invite/{token}` (Public, `RateAuth`, `invite` in `LegacyMount.SPAShared`) and the existing `GET /auth/temp-login` rewritten to 302. Either keep them as the only implementation, or delete the gin `/invite/{token}` route in the S9 PR when `ui.Handler` takes over. State the choice in 07 §7.4.3 and 05-http-api §8.2.
  - **The R-test cell:** reword it to "an invite link created on cur opens in cur's UI; prev-release UI tabs (cell a) still accept a legacy `/invite/<token>` link".

### FE3-6: MAJOR. The S5–S9 Activity page "calls the task aliases in the legacy shape through the generated core client", but aliases have no typed legacy schema, `WithLegacyShape` is defined nowhere, and `opsgen` (the source of the legacy `ops.ts` enums) is retired at S5

- **Where:** 07 §7.11 ActivityLog row (`07-ui-shell.md:929`), §7.9.2 (`:879-881`, "`WithLegacyShape`, 05-tasks"), §7.8 last bullet (`:802-803`: tygo and `opsgen` deleted once routes are native); 09 S5 UI and Endpoints rows (`09-abo-on-core.md:654,657`); 04 §8.2 (`04-package-mapping.md:716`: `tools/cmd/opsgen` → `internal/openapi` at S5); 05-http-api §3.1 `LegacyPath.Shape func(v any) any` (`05-http-api.md:224-238`), §7.
- **Evidence:**
  - **No such API.** `grep -n WithLegacyShape` matches only 07 and 09. 05-tasks and 05-http-api define no such function. The alias mechanism is `LegacyPath.Shape func(any) any`, which is untyped, so `Router.OpenAPI` can document an alias only with the **native** response schema (or none).
  - **The generated client cannot do this.** A client generated from `core.json` therefore cannot call an alias "in the legacy shape" with correct types.
  - **The enums go stale.** `useOperationsStore` compares legacy `type`/`status` strings (`web/src/stores/useOperationsStore.ts:14-31 @748fd787a`) that come from `web/src/generated/ops.ts`, which `opsgen` writes. 04 retires `opsgen` at S5, and 07 deletes it "once routes are native" (S5). The legacy enum source therefore disappears four stages before its only consumer moves.
- **Fix:**
  - **Client (pick one, write it in 07 §7.11, §7.9.2 and 09 S5):** (a) **recommended:** from S5 to S9, `useOperationsStore` and `ActivityLog` keep their hand-written `api.ts` operations calls on the legacy paths, and `ops.ts` is frozen (committed, its freshness gate removed) or `opsgen` is kept until S9; the "rewire to the generated core client" happens at S9 against native routes. (b) Give `LegacyPath` a typed `Shape` (`LegacyResp reflect.Type`) so OpenAPI documents the legacy schema, and put aliases into `core.json`.
  - **Wording:** replace every `WithLegacyShape` with `LegacyPath.Shape` (09 §9.2.4, S5; 09 §9.6 row 32).
  - **04:** the `opsgen` row reads "retired at S9".

### FE3-7: MAJOR. The `/api/v1/auth/me` alias returns canonical names in `permissions`, which breaks the previous-release UI (R-test cell a) and contradicts the alias rule "canonical handler, legacy wire shape"

- **Where:** 09 S4 Permissions row (`09-abo-on-core.md:640`); 05-rbac §6.3 "Compatibility window" (`05-rbac.md:821-822`); 09 §9.2.4 alias rule (`09-abo-on-core.md:331-333`); 07 §7.9.1 (the S4 UI reads `/api/core/v1/me` and `/permissions`).
- **Evidence:** Previous-release bundles check legacy atoms against `user.permissions` (`web/src/hooks/useLibraryPermission.ts:9,11 @748fd787a`, `useCanOrganizeLibrary.ts:9`, `useCollections.ts:24`, `ThrottlePanel.tsx:24`), and they fail closed. After S4 r3, the alias puts `books:library.delete`-style names into `permissions`, so every old tab hides delete, restore, organise and throttle controls until the user reloads. The new field `legacy_permissions` is one that the old bundle never reads. The canonical list on the alias serves no first-party reader: the S4 UI reads native `/api/core/v1/me` in the same build (07 §7.9.1). External scripts reading `permissions` break the same way.
- **Fix:** In 05-rbac §6.3 and 09 S4, the alias keeps `permissions` = reverse-mapped legacy atoms (legacy shape) and adds `canonical_permissions` during W. Native `/api/core/v1/me` returns canonical names only. Add an S4 R-test cell (a) assertion: "a previous-release tab still shows the delete button for a user holding `library.delete`".

### FE3-8: MINOR. Package `ui` lands at S9, but `module.ReservedIDs()` (S2) is defined over `ui.ReservedSegments()`, and the "equals 03's row" test cannot pass before S9

- **Where:** 06 `ReservedIDs` (`06-app-sdk.md:144-156`), §6.4.10 (S2 row: `module`; S9 row); 04 §8.1 S9 row (`04-package-mapping.md:688`: `ui`); 03 §3.6 (`03-architecture.md:401`).
- **Evidence:** `module` (L7) cannot call a function in a package that does not exist yet. The shell segments (`login`, `invite`, `settings`, …) are therefore not reserved from S2 to S8. The test that "the static union equals 03's row" fails, or is skipped, for seven stages.
- **Fix:** Land `ui.ReservedSegments()` (a static function, no dependencies) at S2 together with `module`, and keep the rest of `ui` at S9. Say so in 04 §8.1 (S2 row) and 06 §6.4.10.

### FE3-9: MINOR. The P8 shell-segment check has two error names and two tests

- **Where:** 06 `ProblemSPASegmentCollision` and `TestReservedSegmentsDisjointFromNonSPA` (`06-app-sdk.md:820,1061,1149`); 07 §7.4.3 and §7.18 (same names); 05-http-api §4.10 `ErrShellSegment` and `TestKernel_ShellSegmentsDisjointFromNonSPA` (`05-http-api.md:757,1090`).
- **Fix:** Use one name. The check runs in the kernel at P8, so use 06's `ProblemSPASegmentCollision` and one test (06 §6.10). 05-http-api's `Router.NonSPASegments()` only supplies the data.

### FE3-10: MINOR. 04 moves `csp.go` and `csp_report.go` into core `httpapi` at S2, which contradicts 07 and 05-http-api: ABO keeps computing its legacy CSP until S9, and the core report route is `internal/uihttp`

- **Where:** 04 `internal/server` split (`04-package-mapping.md:362`: "mediacore/httpapi (perimeter: peer allow-list, CSP, security headers/CORS) ← … `csp.go`, `csp_report.go` …"); 05-http-api §8.1 S2 (`DefaultCSP` = ABO's `contentSecurityPolicy()`); 07 §7.6.2 (`core/ui` computes the shell policy; reports via `internal/uihttp`; gin's `/api/v1/csp-report` until S9).
- **Evidence:** `contentSecurityPolicy()` hashes ABO's embedded `index.html` and re-reads ABO's Access team host (`internal/server/csp.go:34-46,95-106,129-138 @748fd787a`), which is deployment-specific (G11). If it moves to core at S2, ABO has nothing to pass as `DefaultCSP`. `static_embed.go` (deleted at S9 per 07) is not mapped at all.
- **Fix:** Split the 04 row: `cspBase` and the policy builder go to `core/ui` (S9); `contentSecurityPolicy()` and the team-host pointer stay in ABO until S9, then become ABO's `ConnectSrcFrom` (FE3-4); `csp_report.go` goes to `internal/uihttp` (S4), with the gin handler deleted at S9; `static_embed.go` goes to `ui` (S9).

### FE3-11: MINOR. The shell's settings routes: 07 says S3 supplies the config schema and `KeyState` API, 09 S4 lists `/config/*`, and 05-config owns `/api/core/v1/settings*`

- **Where:** 07 §7.5.1 (`07-ui-shell.md:480-482`), §7.11 Settings row (`:931`); 09 S3 Endpoints ("`GET/PUT /api/v1/config` unchanged"), S4 Endpoints (`09-abo-on-core.md:637`: `/config/*`); 05-config §1.3.4 (`05-config.md:431-435`: `GET /api/core/v1/settings/schema`, `GET`/`PATCH /api/core/v1/settings`).
- **Evidence:** Section permissions are `core:settings.manage`/`<id>:settings.manage` (05-config §1.3.4), so the routes cannot be enforced before S4's rbac. The path `/config/*` does not exist in the owner spec. Generated operation names follow the path, so `<SchemaForm>` would target operations that do not exist (FE2-18 class).
- **Fix:** In 09 S4, replace `/config/*` with `/settings`, `/settings/schema`. In 07 §7.5.1 and §7.11, say "S4 (settings schema and `KeyState`)" instead of S3, and update 09 S9's preconditions sentence to match.

### FE3-12: MINOR. Three files describe three different S5 legacy-stream bridges

- **Where:** 05-tasks §5T.4 step 3 (`05-tasks.md:999-1003`: the **executor** publishes onto `registry.EventHub` through `internal/opsbridge`, streams A/B); 05-realtime §4.9 table (`05-realtime.md:388`: a **bus subscriber registered by books** feeds hub A, `registry.EventHub` (B) **and** the v3 broker (C)); 09 S5 (`09-abo-on-core.md:655`: `EventHub` "feeds `/api/v1/operations/events` and `/api/v3/ops/events`", i.e. B and C); 07 §7.9.2 (`07-ui-shell.md:882-883`: "`core/tasks` publishes … to the legacy `registry.EventHub`", which reads as core importing ABO).
- **Evidence:** The bridges differ in four ways: synchronous executor hook vs asynchronous bus subscriber, which streams are fed (A/B vs A/B/C), whether `EventHub` feeds stream C (it does not: C is the v3 broker, `opsv3/events.go`), and whether core calls ABO code. `TestLegacyHubBridge_OpEventsUntilProjections` checks stream B only. The SPA uses A and B (`web/src/services/eventSourceManager.ts:31`, `api.ts:733 @748fd787a`); C has external consumers.
- **Fix:** Adopt 05-realtime's form everywhere: a books-registered bus subscriber feeding A, B and C. Change 05-tasks §5T.4 step 3, 09 S5 and 07 §7.9.2 to that wording, and extend the test to all three streams.

### FE3-13: MINOR. During S8 the new `/livez` and `/readyz` are not non-SPA: gin's `NoRoute` still owns `/`, with today's eight-segment list

- **Where:** 09 S8 Health row (`09-abo-on-core.md:696`: "`livez` and `readyz` are non-SPA segments"); 07 §7.6.3 row 1 (gin keeps `spaWillNotServe` until S9); 05-http-api §4.10 (`Router.NonSPA()` feeds only `ui.Options`).
- **Evidence:** `spaWillNotServe` uses the static `nonSPASegments` map (`internal/server/spa_fallback.go:74-83 @748fd787a`), which has no `livez` or `readyz`. `/livez/` and `/READYZ` miss the exact `Infra` pattern, fall through to the `ServesSPA` fallback, and get `200 text/html` for the whole of S8. That is the FE2-6 failure, for one stage.
- **Fix:** In 09 S8, add "gin's `nonSPASegments` gains `livez` and `readyz` in the same PR". Better, from S2 the `ServesSPA` fallback consults `Router.NonSPA()` before calling gin (05-http-api §4.10), so one predicate serves both owners.

### FE3-14: MINOR. `ui.Handler` serves only `/assets/*` and `index.html`; root files of `dist/` (Vite's `public/`, e.g. `/favicon.svg`) are unspecified and would 404 under the "extension → 404" rule

- **Where:** 07 §7.6.1 `Handler` comment (`07-ui-shell.md:617-629`).
- **Evidence:** ABO's `web/public/favicon.svg` is referenced by `index.html:9 @748fd787a`. Today gin serves any existing file of the embedded dist (`internal/server/static_embed.go:82-104 @748fd787a`).
- **Fix:** Add a bullet: "any other existing regular file of the bundle's `fsys`: served with `Cache-Control: no-cache`". Add `TestHandler_ServesRootPublicFile`.

### FE3-15: MINOR. 07 omits 05-realtime's last-resort per-visible-tab mode, and §7.14 states an absolute "one connection per browser" that 05-realtime does not guarantee

- **Where:** 07 §7.9.2 bullet 2 (`07-ui-shell.md:861-870`), §7.14 (`:986-990`), §7.18 `realtime.leader.test.ts`; 05-realtime §4.8 item 7 "Last resort" (`05-realtime.md:339-341`).
- **Fix:** Add the last-resort bullet to 07 §7.9.2, say "at most one per visible tab without `BroadcastChannel`" in §7.14, and add a `realtime.leader.test.ts` case with both APIs undefined.

### FE3-16: MINOR. The `/api/core/v1/modules` route package is unnamed, and "`kernel/catalog` supplies the data" is an L6 → L8 import unless the data is injected

- **Where:** 06 §6.4.6 (`06-app-sdk.md:755-762`); 05-http-api §5 item 2 (the capability package list has no catalog package); 03 §3.3 `internal/<capability>http` row and R4 (`03-architecture.md:273,334`).
- **Fix:** Name it (`internal/modulehttp`, L6), add it to 05-http-api §5 and 03 §3.3, and state that the kernel passes it a read-only `ModulesView` interface (catalog snapshot, `restart_required`, `ui_build_id` source), so it imports nothing above L6.

### FE3-17: MINOR. `ui_route` recording needs the legacy-redirect list on the Go side, but its only declared source is the TypeScript `redirects` of books' `ModuleUIInput`

- **Where:** 07 §7.6.1 `PageMiddleware` comment (`07-ui-shell.md:599-604`), §7.4.2 `redirects`; 09 S9 UI routes (`09-abo-on-core.md:710`).
- **Fix:** Generate one list: books declares `LegacyUIRedirects []struct{From, To string}` in Go, `manifest.gen.ts` exports it, the TS `redirects` is built from it, and `PageMiddleware` reads the Go value. The existing freshness check then keeps the two in step.

### FE3-18: MINOR. Stale or contradictory compat records for the UI links and the S9 storage keys

- **Where / evidence:**
  - 09 §9.6 row 31 (`/invite/:token`, "W after S9") vs 09 S4 (`:639`, "aliased for W ≥ max invite TTL" from S4). The `/auth/temp-login?token=` 302 has no §9.6 row.
  - 09 S9 "`localStorage` keys migrated on first load" vs S9 Rollback (binary swap): if migration moves the keys, a rollback to S8 resets every preference. 07 §7.7 says ABO passes its existing key instead.
  - 07 §7.20 O3 ("whether account merge is core") is decided: `internal/usermerge` stays in ABO, and auth exposes `StageUserMerge` (05-authn `:194-195`, 04 `:403`). 07 §7.11's `UserMerge.tsx` row still branches.
- **Fix:** One window for `/invite/{token}` (S4 + max(W, invite TTL)) plus a row for the temp-login 302. "Copied, not moved, until C9" for `localStorage`. Close O3, and make `UserMerge.tsx` a books `userPageExtensions` row action.

### FE3-19: MINOR. "The Vite dev proxy is same-origin from the browser's view" holds only on localhost: over a LAN address, `changeOrigin: true` plus stage 7 refuses every dev POST

- **Where:** 07 §7.9.1 cookie/CSRF bullet (`07-ui-shell.md:816-818`), §7.10 dev proxy; 05-http-api stage 7.
- **Evidence:** ABO's proxy sets `changeOrigin: true` (`web/vite.config.ts:39-43 @748fd787a`), which rewrites `Host` to the target. Browsers send `Sec-Fetch-Site` only to trustworthy origins, so a dev server opened at `http://<lan-ip>:5173` (phone testing) yields `Origin` ≠ `Host` and a 403 `cross_origin_rejected` on every POST.
- **Fix:** The preset's proxy also rewrites `Origin` to the target origin (dev only). Write this in 07 §7.10 instead of "nothing extra".

### FE3-20: MINOR. Small residues

- 07 §7.3.1 (`07-ui-shell.md:59,92`): "`core/web` … the post-split repo root" contradicts 12, where the split repository is `core/` and `core/web` becomes `web/`. With `core/web` as the root, `../examples/hello/web` would point outside the repository. Say "the split repository's npm root (`web/`)".
- 07 §7.3.2 stamp prerequisite `core/web/packages/*/src/**`: GNU make has no `**`, so the stamp would miss nested sources. Use `$(shell find core/web/packages -path '*/src/*' -type f)`.
- 13 §13.4 has no rows for `openapi-typescript` 7.13.0 and `openapi-fetch` 0.17.0, although 07 says 13 owns frontend versions and their 14-day age (FE-21); 07 says peer `react-router ^8`, 13 says `^8.5.1`. Add the two rows (with the `overrides` note under E1) and use 13's range in 07.

## Not attacked (fixed decisions)

D6 (core shell plus app UI contributions), D8, D9, cross-cutting 2 (legacy mount owns `/` until shell
adoption) and 9 (one web entry point), and R2-1..R2-8 are taken as given. The findings challenge only
whether the written design works and agrees across files.

Counts: **1 BLOCKER, 6 MAJOR, 13 MINOR** (20 findings).
