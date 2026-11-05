<!-- file: docs/agent-tasks/platform-core/F4-05-core-identity-settings-routes.md -->
<!-- version: 1.1.3 -->
<!-- guid: cbdb5377-2585-4a6d-bc8d-b935127d7e1e -->
<!-- last-edited: 2026-11-05 -->

# F4-05 — Core identity and settings routes, dark: `internal/authhttp` (with the OIDC start and callback and the app hand-off), `internal/uihttp`, `internal/modulehttp`, `internal/storagehttp` (factory reset), the native settings, role and audit routes

| Field | Value |
|---|---|
| ID | F4-05 |
| Release / flip | R2, identity flip F4 (dark PR: the routes are built and tested over test routers; the kernel mounts them, for hello and ABO, at F4-08) |
| Tier | **T3** (Opus, high): credential routes with interactive and owner guards |
| Wave | W30 (F2-16 is this wave's one owner of the kernel and module wiring files: edit none of them) |
| Depends on | F4-02, F4-01, F4-03, F3-01, F4-10 |
| Finishes | F2-05's skipped permission cells, `TestApprove_RequiresVerifiedMethodAndPermission` among them |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-05-identity-routes
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-02, F4-01, F4-03, F3-01 and F4-10 are merged. If not, stop.

## Goal

Create the L6 route packages, each imported only by `kernel` (03 §3.3), registering on `Router.CoreGroup()`
(`/api/core/v1`) except the two OIDC paths on `Router.CoreFixedGroup(opts.OIDCCallbackBase)` (F4-10 built it, with
`CoreExactPatterns()` and `Build`'s refusal of a fixed-group route outside the declared list):

| Package | Routes (spec) |
|---|---|
| `core/internal/authhttp` | every row of 05-authn §4.13's table (auth status, setup, bootstrap, login/logout, providers, OIDC start at `/api/core/v1/auth/oidc/{provider}/start`, HEAD's start `GET /api/v1/auth/oauth/{provider}/start` and callback `GET /api/v1/auth/oauth/{provider}/callback` on the fixed group, `/me*`, `/me/password`, `/me/sessions*`, `/me/api-keys*`, `/api-keys*`, `/users*`, invites, temp-login), with the native-app hand-off of §4.7 |
| `core/internal/rbachttp` | the roles CRUD and `POST /roles/{name}/accept-seed` (05-rbac §4.7–§4.8) |
| `core/internal/audithttp` | the audit query and export (05-rbac §7.6) |
| `core/internal/confighttp` | `GET /api/core/v1/settings/schema`, `GET /api/core/v1/settings`, `PATCH /api/core/v1/settings` (05-config §1.3.4) |
| `core/internal/storagehttp` | `POST /api/core/v1/system/factory-reset` with `storagehttp.Stager` (05-storage §5S.3.15; 05-rbac §6.2: `core:admin` + `Guards.Interactive` + `Guards.Owner`) |
| `core/internal/modulehttp` | `GET /api/core/v1/modules` over the kernel's read-only `ModulesView` (05-http-api §5 item 2), with `<ns>_ui_manifest_requests_total{outcome}` (07 §7.16); and the served OpenAPI route `GET /api/core/v1/openapi/{owner}.json` (05-http-api §7: perm `rbac.AnyAuthenticated`, Public when `core.http.openapi.public` is true) over a read-only view of the built documents the kernel passes in (host package chosen here: the spec names none) |
| `core/internal/uihttp` | `ui/diagnostics` and `ui/csp-report` (05-http-api §5 item 2), with `<ns>_ui_diagnostics_total{kind,module}` and `<ns>_ui_csp_reports_total{directive}` (07 §7.16) |

Dark: no kernel mounts these packages before F4-08 (which mounts them for every app); this PR tests each package over
its own test router. It also finishes F2-05's task-route permission cells, which waited for core authentication.

## Spec

- 05-authn §4.7 (OIDC and the hand-off: start parameters `redirect_uri` (alias `callback`), `code_challenge`,
  `code_challenge_method`, `state`; both present selects the app branch; an unlisted `redirect_uri` → inline 400 with no
  `Location`; method other than `S256` → 400; the callback calls `AppHandoff.Mint` before any session exists and issues
  no web session), §4.11 (auth-off reach of credential metadata), §4.13 (the table, link forms `/login/temp#token=…`
  and `/invite#token=…`, tokens only in bodies, invites re-checked on accept), §10 rows for these routes.
- 05-http-api §3.5 (`CoreGroup`, `CoreFixedGroup`, `CoreExactPatterns`), §4.9 (the claim rule's two exact core patterns
  inside books' `/api/v1/` claim), §5 (route packages, the reset route's 422 contract), §3.3 (problem+json), §3.4
  (snake_case).
- 05-config §1.3.4 (status codes: 400 unknown key, 409 locked with `locked_by`, 403 forbidden/protected with `need`, 422
  invalid, 500 storage, 200 with `restart_pending` and `post_commit_error`; `config.Actor` filled from the request's
  auth context: `Interactive` = `auth.Method.MayChangeCredentials()`, `Can` = the checker).
- 05-storage §5S.3.15 (`storage.ResetRequest`, `Stager.StageReset`, 422 `confirm_required` and
  `library_files_confirm_required`; the Tests row's `TestResetRequestFlowsUnchanged`, route half).
- 05-http-api §7 (the served OpenAPI route), §10 (`core.http.openapi.public`), §12 (`TestOpenAPI_UsesConfigOverrides`).
- 07 §7.16 (the three `_ui_` counters above; `_ui_build_info` and `_ui_responses_total` are F9-01's, on `ui.Handler`);
  05-metrics §5 item 5, §12 (`metricstest.AssertSeries`).
- 05-rbac §6.2 (admin-gated routes), §12 (`TestFactoryResetCoreRouteRecordsHookArgs`,
  `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`, `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` is
  F6's).

## Files

`core/internal/authhttp/`, `core/internal/uihttp/`, `core/internal/modulehttp/`, `core/internal/storagehttp/`,
`core/internal/confighttp/`, `core/internal/rbachttp/`, `core/internal/audithttp/` (all created). Also `core/internal/taskhttp/`: finishing F2-05's
cells edits its `_test.go` files only. Not yours:
`core/kernel/`, `core/module/` (F2-16 this wave, F4-08 later).

## Re-verify at BASE

```bash
ls -d core/internal/authhttp core/internal/uihttp core/internal/modulehttp core/internal/storagehttp core/internal/confighttp core/internal/rbachttp core/internal/audithttp 2>/dev/null | wc -l   # 0
grep -rn 'func (r \*Router) CoreGroup' core/httpapi/ | wc -l          # 1 (R1-13)
grep -rn 'func (r \*Router) CoreFixedGroup' core/httpapi/ | wc -l     # 1 (F4-10)
grep -rn 'CoreExactPatterns' core/httpapi/corefixed.go | wc -l        # >= 1 (F4-10)
grep -rln 'needs core auth (F4-05)' core/internal/taskhttp/ | wc -l   # >= 1: F2-05's skipped cells you finish
grep -rn 'type ResetRequest struct' core/storage/ | wc -l             # 1 (R1-25)
grep -rn 'func (s \*Store) Apply' core/config/ | wc -l                # 1 (F3-01)
grep -rn 'func (s \*Service) Middleware' core/auth/ | wc -l           # 1 (F4-02)
grep -rn 'package oidc' core/auth/oidc/ | wc -l                       # >= 1 (F4-03)
# HEAD's routes these replace at F4-08 (HEAD f7ac5b11e; informational)
grep -c 'oauth' internal/server/wire_auth_routes.go                   # >= 1 (callback route at :44 @748fd787a)
```

## STOP conditions

1. `Router.CoreFixedGroup` or `CoreExactPatterns` is missing in `core/httpapi` at BASE (F4-10 not merged): stop; do
   not add them here.
2. Any route carries a one-time token in its path, or returns a token, hash or mask in a body.
3. A route would be mounted by the kernel (for any app) before F4-08, or this PR edits `core/kernel/` or `core/module/`.

## Steps

1. Write the tests below first (hello and `coretest` fixtures; a fake IdP with `httptest`).
2. `authhttp`: every row of §4.13 with its `Perm` and `Guards`; dominance enforced through `auth.Service` methods;
   `PUT /me/password` self only (no target-user field); `GET /api-keys` lists every user's keys with the owner's
   username (`core:users.manage`); invites carry role and creator and re-run `rbac.CanAssign` on accept; the OIDC start
   handler (one handler for both start paths) and the callback with the hand-off; the fixed group only when
   `auth.Options.OIDCCallbackBase` is set (hello: none, so its callback is `/api/core/v1/auth/oidc/{provider}/callback`).
3. `rbachttp`, `audithttp`: routes and errors of 05-rbac (`seed_role_locked` 409; `dominance_required` 403).
4. `confighttp`: the three settings routes over `config.Store` with `config.Actor` from the request.
5. `storagehttp`: the reset route with the storagehttp-local `Stager` interface (never `*storage.Admin`) and the 422
   rules; the kernel passes its staging function.
6. `modulehttp`, `uihttp`: as 05-http-api §5 item 2, plus `modulehttp`'s OpenAPI route (05-http-api §7). The three
   `_ui_` counters add the core `ui` metrics scope: add its `testdata/series.golden`, asserted by
   `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5); F9-01 extends that golden with its two families.
7. Test each package over a test `httpapi.Router` built in its own `_test.go` (with `CoreGroup` and, for `authhttp`,
   `CoreFixedGroup` at a fixture base), with core's real chain and a `coretest` auth fixture. The kernel mount is
   F4-08's.
8. **F2-05's cells** (05-tasks §5T.9 approval row; 05-rbac §12): in `core/internal/taskhttp/`'s tests, remove each
   `t.Skip("needs core auth (F4-05)")` and make the session and key cells of
   `TestApprove_RequiresVerifiedMethodAndPermission` and the per-route permission cells run over core `auth` (session,
   CF Access-shaped interactive extension, module method refused, key naming the scope, key implying it via
   `core:admin` refused, delegated session, forwarded header). Change no production file of `taskhttp`.
9. Headers, one `changelog.d/` fragment, Acceptance.

## Tests first

- `TestOIDCHandoff_StartAndCallbackControls` (name coined here; the core half of 05-authn §4.7's hand-off, with a test
  double `AppHandoff` holding HEAD's allowlist): both `redirect_uri` (or `callback`) and `code_challenge` present → the
  callback calls `AppHandoff.Mint`, redirects to the app scheme with the code and the app's state, and no web session
  record exists; only one of them → ordinary web sign-in; an unlisted, prefix-extended or userinfo-bearing
  `redirect_uri` → inline 400 with no `Location` header; `code_challenge_method` `plain` or missing → 400.
  `TestOIDC_AppHandoffMintsABSCode` itself, with the redemption at the ABS route `/auth/openid/callback` and the refusal
  after an epoch bump, is F4-08's.
- `TestApprove_RequiresVerifiedMethodAndPermission` and F2-05's per-route permission cells (step 8), un-skipped.
- `TestAuthOff_CredentialMetadataNeedsUser`, `TestInviteLink_FragmentEndToEnd` / `TestTempLoginLink_FragmentEndToEnd`
  (the server half), `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser`,
  `TestInviteAcceptRechecksCanAssign`, `TestFactoryResetCoreRouteRecordsHookArgs`,
  `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`, `TestSeedRole_EditRefused` (the route half),
  `TestModuleGroup_IsAuthenticated` over the new packages, `TestBuild_RejectsCamelCaseJSON` over their DTOs, and for the
  settings routes `TestSettingsRoutes_StatusCodes` (name coined here: 400/409/403/422/200 exactly as 05-config §1.3.4,
  secrets never in a body).
- `TestResetRequestFlowsUnchanged`, the route half (05-storage §5S.3.15; R1-25 owns the offline half): the one
  `storage.ResetRequest` reaches the stager unchanged from the route.
- `TestOpenAPI_UsesConfigOverrides` (05-http-api §12): a `config.Duration` field renders as `string/duration` in the
  served document, the same schema as in the settings schema. With it, cells for the served route: an authenticated
  caller gets the owner's document, an anonymous one 401 unless `core.http.openapi.public` is true.
- `metricstest.AssertSeries` over the `ui` scope's `testdata/series.golden` (05-metrics §12).

## Invariants

- Every route runs the whole core chain (stages 0–13); the only core routes outside `/api/core/v1` are the two exact
  OIDC patterns.
- Tokens travel only in request bodies; responses carry no secret, hash or mask.
- Auth-off: credential, key, session, audit and backup metadata routes answer 401; settings keep their auth-off reach.

## Acceptance

```bash
(cd core && go vet ./internal/authhttp/... ./internal/uihttp/... ./internal/modulehttp/... ./internal/storagehttp/... ./internal/confighttp/... ./internal/rbachttp/... ./internal/audithttp/...)   # exit 0
(cd core && go test -race -count=1 ./internal/... ./kernel/...)                         # ok
(cd core && go test -count=1 -run 'TestOIDCHandoff_StartAndCallbackControls' ./internal/authhttp/...)   # PASS
(cd core && go test -count=1 -v -run 'TestApprove_RequiresVerifiedMethodAndPermission' ./internal/taskhttp/... | grep -c SKIP)   # 0
grep -rn 'needs core auth (F4-05)' core/internal/taskhttp/ | wc -l                     # 0
git diff --stat origin/main -- core/kernel core/module | tail -1                        # empty: no kernel or module edit
(cd core && go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... | grep -E ' [^ ]*internal/(auth|ui|module|storage|config|rbac|audit)http( |$)' | awk '{print $1}' | grep -v '/kernel$')   # no output: only kernel imports them
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

Mounting in the kernel (every app), deleting the gin identity and settings routes, the SPA, the `server-bootstrap`
skill and `scripts/`, books' real `AppHandoff` and `TestOIDC_AppHandoffMintsABSCode` (F4-08); `CoreFixedGroup` and
`CoreExactPatterns` (F4-10).

## Rollback

Revert the PR; ABO mounts none of it.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core): native identity, role, audit, settings, reset, module and ui routes (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
