<!-- file: docs/agent-tasks/platform-core/F4-05-core-identity-settings-routes.md -->
<!-- version: 1.0.0 -->
<!-- guid: cbdb5377-2585-4a6d-bc8d-b935127d7e1e -->
<!-- last-edited: 2026-11-05 -->

# F4-05 — Core identity and settings routes, dark: `internal/authhttp` (with the OIDC start and callback and the app hand-off), `internal/uihttp`, `internal/modulehttp`, `internal/storagehttp` (factory reset), the native settings, role and audit routes

| Field | Value |
|---|---|
| ID | F4-05 |
| Release / flip | R2, identity flip F4 (dark PR: the routes are built and mounted on hello; ABO mounts them at F4-08) |
| Tier | **T3** (Opus, high): credential routes with interactive and owner guards |
| Wave | W27 (F3-07 is in this wave and merges first, alone; rebase after it) |
| Depends on | F4-02, F4-01, F4-03, F3-01 |
| Owns gate test | `TestOIDC_AppHandoffMintsABSCode` |
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

Confirm F4-02, F4-01, F4-03 and F3-01 are merged. If not, stop.

## Goal

Create the L6 route packages, each imported only by `kernel` (03 §3.3), registering on `Router.CoreGroup()`
(`/api/core/v1`) except the two OIDC paths on `Router.CoreFixedGroup(opts.OIDCCallbackBase)`:

| Package | Routes (spec) |
|---|---|
| `core/internal/authhttp` | every row of 05-authn §4.13's table (auth status, setup, bootstrap, login/logout, providers, OIDC start at `/api/core/v1/auth/oidc/{provider}/start`, HEAD's start `GET /api/v1/auth/oauth/{provider}/start` and callback `GET /api/v1/auth/oauth/{provider}/callback` on the fixed group, `/me*`, `/me/password`, `/me/sessions*`, `/me/api-keys*`, `/api-keys*`, `/users*`, invites, temp-login), with the native-app hand-off of §4.7 |
| `core/internal/rbachttp` | the roles CRUD and `POST /roles/{name}/accept-seed` (05-rbac §4.7–§4.8) |
| `core/internal/audithttp` | the audit query and export (05-rbac §7.6) |
| `core/internal/confighttp` | `GET /api/core/v1/settings/schema`, `GET /api/core/v1/settings`, `PATCH /api/core/v1/settings` (05-config §1.3.4) |
| `core/internal/storagehttp` | `POST /api/core/v1/system/factory-reset` with `storagehttp.Stager` (05-storage §5S.3.15; 05-rbac §6.2: `core:admin` + `Guards.Interactive` + `Guards.Owner`) |
| `core/internal/modulehttp` | `GET /api/core/v1/modules` over the kernel's read-only `ModulesView` (05-http-api §5 item 2) |
| `core/internal/uihttp` | `ui/diagnostics` and `ui/csp-report` (05-http-api §5 item 2) |

Dark for ABO: nothing here is mounted in ABO's router before F4-08.

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
  `library_files_confirm_required`).
- 05-rbac §6.2 (admin-gated routes), §12 (`TestFactoryResetCoreRouteRecordsHookArgs`,
  `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`, `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` is
  F6's).

## Files

`core/internal/authhttp/`, `core/internal/uihttp/`, `core/internal/modulehttp/`, `core/internal/storagehttp/`,
`core/internal/confighttp/`, `core/internal/rbachttp/`, `core/internal/audithttp/` (all created).

## Re-verify at BASE

```bash
ls -d core/internal/authhttp core/internal/uihttp core/internal/modulehttp core/internal/storagehttp core/internal/confighttp core/internal/rbachttp core/internal/audithttp 2>/dev/null | wc -l   # 0
grep -rn 'func (r \*Router) CoreGroup' core/httpapi/ | wc -l          # 1 (R1-13)
grep -rn 'func (r \*Router) CoreFixedGroup' core/httpapi/ | wc -l     # 1 (see STOP 1)
grep -rn 'func CoreExactPatterns' core/httpapi/ | wc -l               # 1 (see STOP 1)
grep -rn 'type ResetRequest struct' core/storage/ | wc -l             # 1 (R1-25)
grep -rn 'func (s \*Store) Apply' core/config/ | wc -l                # 1 (F3-01)
grep -rn 'func (s \*Service) Middleware' core/auth/ | wc -l           # 1 (F4-02)
grep -rn 'package oidc' core/auth/oidc/ | wc -l                       # >= 1 (F4-03)
# HEAD's routes these replace at F4-08 (HEAD f7ac5b11e; informational)
grep -c 'oauth' internal/server/wire_auth_routes.go                   # >= 1 (callback route at :44 @748fd787a)
```

## STOP conditions

1. `Router.CoreFixedGroup` or `CoreExactPatterns()` is missing in `core/httpapi` at BASE: they belong to `httpapi`
   (05-http-api §3.5), which this row does not list. Report.
2. Any route carries a one-time token in its path, or returns a token, hash or mask in a body.
3. A route would be mounted in ABO's router before F4-08.

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
6. `modulehttp`, `uihttp`: as 05-http-api §5 item 2.
7. Mount them for hello in the kernel's tests (the kernel's real mount for ABO is F4-08's).
8. Headers, one `changelog.d/` fragment, Acceptance.

## Tests first

- `TestOIDC_AppHandoffMintsABSCode` (gate test; 05-authn §10): one cell per control of §4.7's hand-off — both
  `redirect_uri` (or `callback`) and `code_challenge` present → the callback calls `AppHandoff.Mint`, redirects to the app
  scheme with the code and the app's state, and no web session record exists; only one of them → ordinary web sign-in;
  an unlisted, prefix-extended or userinfo-bearing `redirect_uri` → inline 400 with no `Location` header;
  `code_challenge_method` `plain` or missing → 400; the code redeems once at the ABS route `/auth/openid/callback` and is
  refused after the user's epoch is bumped. Here `AppHandoff` is a test double with HEAD's allowlist; the redemption cell
  needs books' real hand-off and ABS route (F4-08; see Planner notes).
- `TestAuthOff_CredentialMetadataNeedsUser`, `TestInviteLink_FragmentEndToEnd` / `TestTempLoginLink_FragmentEndToEnd`
  (the server half), `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser`,
  `TestInviteAcceptRechecksCanAssign`, `TestFactoryResetCoreRouteRecordsHookArgs`,
  `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`, `TestSeedRole_EditRefused` (the route half),
  `TestModuleGroup_IsAuthenticated` over the new packages, `TestBuild_RejectsCamelCaseJSON` over their DTOs, and for the
  settings routes `TestSettingsRoutes_StatusCodes` (name coined here: 400/409/403/422/200 exactly as 05-config §1.3.4,
  secrets never in a body).

## Invariants

- Every route runs the whole core chain (stages 0–13); the only core routes outside `/api/core/v1` are the two exact
  OIDC patterns.
- Tokens travel only in request bodies; responses carry no secret, hash or mask.
- Auth-off: credential, key, session, audit and backup metadata routes answer 401; settings keep their auth-off reach.

## Acceptance

```bash
(cd core && go vet ./internal/authhttp/... ./internal/uihttp/... ./internal/modulehttp/... ./internal/storagehttp/... ./internal/confighttp/... ./internal/rbachttp/... ./internal/audithttp/...)   # exit 0
(cd core && go test -race -count=1 ./internal/... ./kernel/...)                         # ok
(cd core && go test -count=1 -run 'TestOIDC_AppHandoffMintsABSCode' ./...)              # PASS (core cells)
(cd core && go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... | grep -E ' [^ ]*internal/(auth|ui|module|storage|config|rbac|audit)http( |$)' | awk '{print $1}' | grep -v '/kernel$')   # no output: only kernel imports them
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

Mounting for ABO, deleting the gin identity and settings routes, the SPA, the `server-bootstrap` skill and `scripts/`,
books' real `AppHandoff` (F4-08).

## Rollback

Revert the PR; ABO mounts none of it.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase` after F3-07 has
merged and you have rebased; never squash, never `--admin`.

```
feat(core): native identity, role, audit, settings, reset, module and ui routes (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

- **`TestOIDC_AppHandoffMintsABSCode` is an ABO test** (05-authn §10 marks it "ABO, fake IdP, from F4"): its redemption
  cell needs books' `OIDCAppHandoff` over HEAD's code store and the ABS `/auth/openid/callback` route, which only F4-08
  wires. This row owns the core cells; F4-08 should own or finish the ABO cells.
- **`CoreFixedGroup` / `CoreExactPatterns`** are `core/httpapi` members "from F4" (05-http-api §3.5); no row's file list
  includes `core/httpapi/` at F4 (STOP 1).
- **Kernel mounting** of these packages is in no row's files (`core/kernel/` is absent from F4-05 and F4-08).
