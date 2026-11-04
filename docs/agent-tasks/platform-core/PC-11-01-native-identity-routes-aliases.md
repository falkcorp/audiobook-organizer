<!-- file: docs/agent-tasks/platform-core/PC-11-01-native-identity-routes-aliases.md -->
<!-- version: 1.0.0 -->
<!-- guid: 145a9060-f4b9-4dd9-84fa-a040d4770302 -->
<!-- last-edited: 2026-11-04 -->

# PC-11-01 — Native identity routes (unmounted until the flip): authhttp, users, roles, api-keys, audit, permissions, settings, modules and uihttp; the alias table with selectors

**ID:** PC-11-01 · **Wave:** W11 (S4 r2) · **Priority:** P1 · **Effort:** L
**Tier:** T3 Opus/high — many routes with legacy-shape aliases and field selectors; an alias that retargets an action
or decodes a field loosely changes who may do what.
**Depends on:** OWNER-11 (the `S4r1` soak row and the decode census with no undecodable row outside the P1 branch-B
list). Merges only after that row is in `ci/soak.txt`.
**Stage-Release trailer:** `Stage-Release: S4 2` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Behaviour rules 7, 8, 12, 13; Contracts).
**Owning spec:** 09 S4 "Endpoints", "Alias targets", "Permissions"; 05-authn §4.13 (the route table, the legacy key
list, `me/password` not aliased, invites re-checked on accept, legacy links); 05-rbac §4.7 (roles CRUD), §6.3
(`/api/v1/auth/me` legacy shape, `canonical_permissions`); 05-config §1.3.4 (settings API semantics); 07 §7.5.1
(`/ui/diagnostics`, `/ui/csp-report`); 05-http-api §3.1 (`Alias`, `LegacyPath`), §4.6 (aliases never retarget),
§7 (OpenAPI and `core.json`); 04 §8.6 (`user_merge.go` methods move onto an ABO-owned merge handler type first).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-11-01" -b agent/pc-11-01-native-identity-routes origin/main
cd "$REPO/../audiobook-organizer-pc-11-01"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Goal

Write and test, **without mounting** (the kernel registers them only in the flip PR, PC-11-02):

- `core/internal/authhttp`: every route of 05-authn §4.13 under `/api/core/v1` (`/auth/*`, `/me`, `/me/password`,
  `/me/sessions`, `/me/api-keys`, `/api-keys` (admin list, `core:users.manage`), `/api-keys/{id}`, `/users/*`,
  invites lookup/accept, `/auth/temp-login/consume`), with each route's `Perm` and `Guards` exactly as the table says;
- `core/internal/rbachttp`: `/roles/*` (05-rbac §4.7) and `/permissions`;
- `core/internal/audithttp`: `/audit` (05-rbac §7.6);
- `core/internal/confighttp`: `/settings`, `/settings/schema` (05-config §1.3.4);
- `core/internal/modulehttp`: `/modules`;
- `core/internal/uihttp`: `/ui/diagnostics`, `/ui/csp-report` (07 §7.5.1);
- the alias table: `/api/v1/auth/*` (22 at the evidence commit), `/api/v1/users/*` (15), `/api/v1/me`, `/api/v1/setup`,
  `/api/v1/config` (2), `/api/v1/preferences` (3), each a `httpapi.Alias` with its ledger row; field selectors per 09 S4
  "Alias targets"; every unmapped legacy field → 400 `legacy_field_unsupported`; `PUT /api/v1/auth/me/password` **not**
  aliased;
- `core/openapi/core.json` regenerated with these routes (every regeneration lands in the PR that adds the route);
- ABO: the merge methods of `internal/server/handlers/user_merge.go` move onto an ABO-owned merge handler type that
  checks `Can(core:admin)` key-scoped from the flip (05-rbac §4.6; the move only, no behaviour change before the flip);
- ledger rows for every alias in `docs/compat-surfaces.md`, embedded copy regenerated.

## Exact files

Create: `core/internal/authhttp/`, `core/internal/rbachttp/`, `core/internal/audithttp/`, `core/internal/confighttp/`,
`core/internal/modulehttp/`, `core/internal/uihttp/`.
Modify: `core/openapi/core.json`, `internal/server/handlers/user_merge.go`, `docs/compat-surfaces.md`,
`internal/books/compat-surfaces.md` (regenerated, never hand-edited).

## Re-verify (expected at the plan checkout `f07f516be`; generated counts at base go in the stage ledger)

```bash
grep -n 'authGroup := api.Group("/auth")' internal/server/wire_auth_routes.go          # 1 hit (:31)
grep -cE '\.(GET|POST|PUT|PATCH|DELETE)\(' internal/server/wire_auth_routes.go        # 16 in this file; the other /api/v1/auth/* routes are wired elsewhere (credential routes) — 22 in all at 748fd787a (09 S4), counted from the router dump below
grep -n 'func mergeActor\|adminRole' internal/server/handlers/user_merge.go            # mergeActor at :62-75, adminRole test :67
grep -n '"/csp-report"\|csp-report' -r internal/server/*.go | grep -v _test | head -3  # the legacy CSP report route stays gin until S9
grep -n 'apikeys.go' -r docs/specs/platform-core/05-authn.md | head -2                 # HEAD anchors :258-303, :265, :323, :353, :402
grep -n 'all=true\|"all"' internal/server/handlers/apikeys.go                          # the selector HEAD reads (:265)
grep -n 'func Alias(g \*Group, legacy LegacyPath)' -r core/httpapi/                    # 1 hit
test -f core/openapi/core.json && echo ok                                              # PC-04-02
grep -n 'type Service struct' core/auth/*.go core/rbac/*.go                            # 2 hits
for d in authhttp rbachttp audithttp confighttp modulehttp uihttp; do test -d core/internal/$d && echo "EXISTS $d"; done   # nothing printed
```

Generate the exact alias inventory at your base: the gin routes under `/api/v1/auth`, `/api/v1/users`, `/api/v1/me`,
`/api/v1/setup`, `/api/v1/config`, `/api/v1/preferences` from `(*gin.Engine).Routes()` (PC-10-06's dump helper).
Expected at the evidence commit: 22, 15, 1, 1, 2, 3. A route with no native target in 05-authn §4.13, 05-rbac §4.7 or
05-config §1.3.4 stays a gin route (the "unmapped one" of 09 S4); list each such route in the PR. If a count moved, record
both values; do not STOP for a count alone.

## Steps

1. Tests first (below), red.
2. Each `internal/<capability>http` package is L6, imported only by `kernel` (03 §3.3), exports one
   `Register(g *httpapi.Group, deps ...)`-style function (signature per the existing `internal/*http` packages at your
   base — follow their shape) and is **not** called from `kernel` in this PR.
3. Routes call `auth.Service` / `rbac.Service` / `audit` / `config.Store` methods only; dominance, `CanAssign`, the
   last-admin rule and key-scope limits are the services' (05-rbac §4.7 rule 5). Invites re-run `rbac.CanAssign` on
   accept (05-authn §4.13 "Invites are re-checked on accept").
4. Aliases (05-http-api §3.1, §4.6, 09 S4 "Alias targets"):
   - `POST /api/v1/auth/api-keys` → `POST /api/core/v1/me/api-keys`, or with body `user_id` →
     `POST /api/core/v1/users/{id}/api-keys`; `Selector: ["user_id"]`;
   - `GET /api/v1/auth/api-keys` → `GET /api/core/v1/me/api-keys`, or with `all=true` **and** a principal holding
     `core:users.manage` → `GET /api/core/v1/api-keys`; any other caller, `all=true` included, gets its own list;
     `Selector: ["all"]`;
   - `GET`/`PATCH`/`DELETE /api/v1/auth/api-keys/:id` → `/api/core/v1/api-keys/{id}` (one target);
   - every other alias has exactly one target; `LegacyError: true` where HEAD answered `{error, code, status}`;
     `Shape` adapts to HEAD's wire shape where the legacy SPA reads it;
   - `/api/v1/auth/me` (alias): `permissions` = the legacy atoms the principal's checker satisfies (reverse-mapped),
     plus `canonical_permissions`; `/api/core/v1/me` returns canonical names only (05-rbac §6.3).
5. `user_merge.go`: move the merge methods onto an ABO-owned type (name coined here: `MergeHandler`) in the same
   package; keep `mergeActor`'s HEAD check until the flip (PC-11-02 switches it). Pure move.
6. Regenerate `core/openapi/core.json` with the generator PC-04-02 added (`grep -n 'openapi' core/Makefile`); aliases
   appear as deprecated entries with the target's native schema (05-http-api §3.1 `Shape`).
7. Ledger rows: one per alias (surface `http`), plus the `rbac_atom` input surface for legacy atoms accepted on API
   input during W (09 S4 "Permissions"), in PC-07-06's format; regenerate the embedded copy (`make compat-ledger`).
8. Headers, `changelog.d/` fragment, acceptance.

## Code contract

Routes and guards: 05-authn §4.13 table verbatim (Perm, Guards, `RateAuth`). `httpapi.LegacyPath{Pattern, Ledger,
Targets, Request, Selector, LegacyError, Shape}` (05-http-api §3.1).

## TDD — names fixed by 09 S4 and 05-rbac §12

`TestAlias_UnknownLegacyFieldRefused` (an unmapped field → 400 `legacy_field_unsupported`, nothing done),
`TestAlias_CreateKeyForOtherUserKeepsTarget` (`user_id` selects the user route; the key belongs to that user; dominance
applies), `TestAlias_ListAllKeysNeedsUsersManage` (`all=true` from a non-`core:users.manage` principal returns the
caller's own list, never 403 and never others' keys), `TestMeAlias_LegacyShape` (legacy atoms in `permissions`,
`canonical_permissions` present; native `/me` canonical only). The row's "core.json contains the stage's native routes":
`TestCoreJSONHasS4Routes` (name coined here) asserts every route of the 05-authn §4.13 table, `/roles/*`,
`/permissions`, `/audit`, `/settings`, `/settings/schema`, `/modules`, `/ui/diagnostics`, `/ui/csp-report` is in
`core.json`. Build the routes in tests on a test `httpapi.Router` with fake services.

## Invariants (STOP conditions)

- Nothing mounts these routes in this PR: `grep -rn 'authhttp\.\|rbachttp\.\|audithttp\.\|confighttp\.\|modulehttp\.\|uihttp\.' core/kernel/ | grep -v _test` prints nothing.
- An alias never retargets an action; `PUT /api/v1/auth/me/password` has no alias.
- No one-time token appears in a core route path (05-authn §4.13: tokens travel in bodies).
- `oasdiff breaking` against the last release's `core.json` reports no breaking change (additive only, 05-http-api §4.6).

## Acceptance

```bash
(cd core && go build ./... && go test ./internal/authhttp/... ./internal/rbachttp/... ./internal/audithttp/... ./internal/confighttp/... ./internal/modulehttp/... ./internal/uihttp/... -race -count=1)   # ok
(cd core && go test ./... -run 'TestAlias_UnknownLegacyFieldRefused|TestAlias_CreateKeyForOtherUserKeepsTarget|TestAlias_ListAllKeysNeedsUsersManage|TestMeAlias_LegacyShape|TestCoreJSONHasS4Routes' -count=1)   # ok
go test ./internal/server/handlers/... -count=1                                       # ok (merge handler move is behaviour-neutral)
go test ./internal/books/... -run 'TestCompatLedgerMatchesCode|TestCompatLedgerCopyFresh' -count=1   # ok
make -C core ci                                                                       # exit 0
make ci                                                                               # exit 0
```

## Out of scope

Mounting routes, deleting the replaced gin routes, `LegacyAuthCore`, the temp-login 302 and the UI switch (PC-11-02); the
factory-reset route (PC-11-03); the SPA readers (PC-11-04).

## Rollback

Revert: nothing is mounted.

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 2` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase` after the gate.

## Planner notes

- The alias registrations need a home: core-owned aliases are registered on core's group (05-http-api §3.1: a ledger
  row naming core as the alias owner), so they sit in the `internal/<capability>http` packages created here; no ABO
  file beyond the ledger is needed. If the coordinator prefers books' manifest to declare them, the row's files change.
