<!-- file: docs/agent-tasks/platform-core/F7-10-integrations-instance-routes-dark.md -->
<!-- version: 1.0.6 -->
<!-- guid: 5632beb8-202d-46fa-8ed4-53a698d32866 -->
<!-- last-edited: 2026-11-05 -->

# F7-10 — Integrations instance routes, dark: the adapters list, the `{kind}/instances` routes, the unsaved-body and per-instance test routes, health and `DELETE …/hold`, in a capability route package

| Field | Value |
|---|---|
| ID | F7-10 |
| Release | R2, flip label F7 (dark PR; mounted at F7-09) |
| Tier | **T3** (Opus / high): instance routes whose test path must never send a stored secret to a new origin |
| Wave | W29 (beside F2-05, F2-11, F4-03, F4-06, F5-16, F7-04; file-disjoint) |
| Depends on | F7-01 (the integrations `Registry`), F4-01 (`rbac`), F4-02 (`auth`: the principal, its method, ProtectAdmin's interactive check) |
| Owns gate tests | none of 09's gate list; the route tests below (names coined here) |
| Spec | 05-metadata M1.3 (`Registry`, the HTTP surface table, "When the routes land, and what they replace"), M1.5 (error semantics: the statuses), M1.10 (the test-connection rows this surface replaces); 05-http-api §3 (`Route`, `Handle`, `CoreGroup`), §8.1 F7 row; 03 §3.3 (`internal/<capability>http`), §3.5 R4 (L6); 05-rbac §7.1 (`core.integration.*`) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** nothing mounts the package in ABO;
ABO keeps its class-`i` `GET`/`PUT /api/v1/config` fields and HEAD's test-connection handlers until F7-09, which mounts
these routes and deletes those handlers in one PR (05-metadata M1.3 "When the routes land"). Before F7 none of these
routes is mounted.

**No stage 13 yet.** Route permission (05-http-api §4.2 stage 13) and the bound principal (stage 12) arrive at F4-08
(W34), after this row's W28, and `coretest`'s `As`, `WithUser` and `AssertPermission` fail "needs core authentication"
until then (R1-37). So the permission and 401 cells are tables whose cells call `t.Skip("needs stage 13: F4-08")`;
F7-09, which mounts the package, un-skips them. The handler logic (I1–I5, ProtectAdmin's 403 from `Registry`, audit,
no secret in responses) is tested now with a principal built with `authtest`'s helpers and bound to the request
context with `auth.WithPrincipal`, through an `httpapi.Router` built for the test with `Register`. Add no permission
check inside a handler: enforcement stays the router's.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-10-integrationhttp" -b agent/platform-core-f7-10-integrationhttp origin/main
cd "$REPO/.worktrees/pc-f7-10-integrationhttp"
git rebase origin/main
```

No `go work init` (CLAUDE.md). Run core commands from inside `core/`.

## Goal

Create `core/internal/integrationhttp` (package name coined here; 03 §3.3 `internal/<capability>http`, L6, imported
only by `kernel`) registering on `httpapi.Router.CoreGroup()` the M1.3 surface, each route a thin adapter over F7-01's
`integrations.Registry`:

| Route (05-metadata M1.3) | Permission | Registry call |
|---|---|---|
| `GET /api/core/v1/integrations/adapters?kind=` (Meta + settings schema) | `core:integrations.read` | `Adapters(kind)` |
| `GET /api/core/v1/integrations/{kind}/instances` | `core:integrations.read` | `Instances(kind)` |
| `POST /api/core/v1/integrations/{kind}/instances` | `core:integrations.manage` | `Put` (new instance) |
| `GET /api/core/v1/integrations/{kind}/instances/{id}` | `core:integrations.read` | `Config(ref)` |
| `PUT /api/core/v1/integrations/{kind}/instances/{id}` | `core:integrations.manage` | `Put` |
| `DELETE /api/core/v1/integrations/{kind}/instances/{id}` | `core:integrations.manage` | `Delete(ref)` |
| `POST /api/core/v1/integrations/{kind}/test` (unsaved body) | `core:integrations.manage` | `Test` with no stored instance: only `secretsIn` is used |
| `POST /api/core/v1/integrations/{kind}/instances/{id}/test` (unsaved body for that instance) | `core:integrations.manage` | `Test` with the instance's ref: a stored secret fills an omitted field only while no destination origin changes |
| `GET /api/core/v1/integrations/{kind}/instances/{id}/health` | `core:integrations.read` | `Health(ref)` |
| `DELETE /api/core/v1/integrations/{kind}/instances/{id}/hold` | `core:integrations.manage` | releases the instance's active hold, as `providerhttp.Registry.ReleaseHold` (M2.4: "its one-instance form, calling the same `ReleaseHold`") |

The security rules live in `Registry` (F7-01), not here: origin binding, the secret-clearing on a destination change,
ProtectAdmin for `Local`, `Locked` fields, `Test` returning no body. This package must pass the request through
unchanged so those rules apply, and must add none of its own reach: it never resolves a `secrets.Ref`, never fills a
secret into a body, and never retries a refused `Put` or `Test` with stored values.

## Invariants

- **I1. No stored secret to a new origin.** An instance-scoped test whose body changes a destination origin and omits
  the secret sends no stored secret (05-metadata M1.3 `Test`; M1.5 row "Instance-scoped `Test` whose body changes a
  destination origin"). The route passes the body and the instance ref to `Registry.Test` and nothing else.
- **I2. No secret value leaves the server.** Every response carries `InstanceConfig` as `Registry` returns it, whose
  secret fields hold `secrets.Ref`s only (M1.3), and `TestResult` carries only `OK`, `Class` and `Status` (a failed test
  is a 200 with no remote text: M1.5 "`Test` with remote failure").
- **I3. Echoed masks are not re-supply.** The request's secret fields go to `secretsIn` as received; an empty, echoed
  or mask-shaped value is left for `Put`'s re-supply check to reject (M1.3 `Put`, SEC3-4); the route never turns a
  missing field into the stored value.
- **I4. Statuses as M1.5 fixes them:** 422 (settings fail schema; nothing written), 409 (`ErrFieldLocked`, or a `Put`
  version conflict), 403 (ProtectAdmin refused), 503 (secrets backend unavailable on `Put`), 404 (unknown kind or
  instance).
- **I5. Each audit action once.** `core.integration.create`, `update`, `change_url`, `write_secret`, `delete`, `test`
  (05-rbac §7.1): record in the route only an action `Registry` does not already record (re-verify below), so none is
  written twice or not at all.

## Files

| Action | Path |
|---|---|
| create | `core/internal/integrationhttp/` (`routes.go`, `routes_test.go`, `hold.go`) |
| create | one `changelog.d/` fragment (headerless) |

Touch nothing else, in particular not `core/integrations/` (F7-01) or `core/integrations/providerhttp/` (F2-11, F7-02).

## Re-verify before editing (expected results at your base)

```bash
# The package does not exist. Expected: 0
ls -d core/internal/integrationhttp 2>/dev/null | wc -l
# F7-01's Registry with the M1.3 methods. Expected: 7 (Adapters, Instances, Config, Put, Delete, Test, Health)
grep -cE '^func \(r \*Registry\) (Adapters|Instances|Config|Put|Delete|Test|Health)\(' core/integrations/registry.go
# Which core.integration.* actions Registry already records. Read the hits; the route records only the rest (I5)
grep -rn '"core.integration\.' core/integrations/*.go | grep -v _test
# The health view carries the active hold (M1.3: Health.Hold *Hold). Expected: >= 1
grep -n 'Hold ' core/integrations/integrations.go
# httpapi's core group and guards (R1-13). Expected: 1 and >= 1
grep -rn 'func (r \*Router) CoreGroup' core/httpapi/ | wc -l
grep -rn 'func Handle\[' core/httpapi/ | wc -l
# HEAD's handlers this surface replaces at F7-09 (plan base ac4368bdb; not edited here). Expected: 1 and 1
grep -c 'func (s \*Server) handleDelugeTestConnection' internal/server/deluge_integration.go
grep -c '"/ai/test-connection"' internal/server/wire_media_routes.go
# Whether providerhttp's ReleaseHold has landed (F7-02, a later wave). Expected at this row's base: 0
grep -rn 'func (r \*Registry) ReleaseHold' core/integrations/providerhttp/ | wc -l
```

## STOP conditions

1. `Registry` lacks one of the seven M1.3 methods, or their signatures differ from M1.3: report; do not edit F7-01's
   files.
2. A route would need to read a stored secret value, or to build the `secretsIn` map from anything but the request body.
3. The instance-scoped test could reach `Registry.Test` with a stored secret merged into the body by this package.
4. A route registers with a zero `Perm` or outside `CoreGroup()`.
5. Anything in ABO imports or mounts `integrationhttp` in this PR.

## Steps

1. Read 05-metadata M1.3 (`Registry`, the HTTP table, "When the routes land"), M1.5 and M2.4 (`ReleaseHold`).
2. Write the tests below first, with `integrationstest` fakes, principals bound with `auth.WithPrincipal` (see "No
   stage 13 yet") and a test `httpapi.Router`.
3. `hold.go`: the one-method consumer interface the hold route calls, matching providerhttp's method exactly
   (`type HoldReleaser interface { ReleaseHold(ctx context.Context, scope string) error }`, interface name coined here;
   04 §4.6: a consumer declares the narrow interface it calls). The route reads `Registry.Health(ref)`; when its
   `Hold` is set it calls `ReleaseHold(ctx, hold.Scope)`; with no active hold it answers 204 and calls nothing (choice
   made here, tested). F7-09 hands it providerhttp's `*Registry`; this PR's tests use a fake.
4. `routes.go`: `func Register(g *httpapi.Group, reg *integrations.Registry, holds HoldReleaser, audit audit.Logger)`
   (name coined here) with `httpapi.Handle` and one `Route` per row of the Goal table (`Name`s lowerCamel and unique;
   `Lenient` false). Bind `{kind}` and `{id}` from the path, `?kind=` from the query; map the Registry's sentinels to
   the I4 statuses.
5. Headers, fragment, acceptance.

## Tests (write first; names coined here unless marked)

Permission and 401 cells: each test is a table whose every cell calls `t.Skip("needs stage 13: F4-08")` in this PR;
F7-09 un-skips them when it mounts the package.

- `TestIntegrationHTTP_ReadNeedsIntegrationsRead` and `TestIntegrationHTTP_WritesNeedIntegrationsManage`: every route
  refuses a principal without its permission (403) and answers 401 with no principal, including the two test routes
  and the hold route (a hold-route refusal calls no `ReleaseHold`).

Handler logic, running in this PR (a principal from `authtest`'s helpers bound with `auth.WithPrincipal`):

- `TestIntegrationHTTP_InstanceTestChangedOriginSendsNoStoredSecret`: the route-level twin of F7-01's
  `TestRegistryTest_ChangedURLUsesNoStoredSecret` (spec name): an instance with a stored secret; a body that changes
  the URL origin and omits the secret → the fake server sees no secret; the same body with the origin unchanged → the
  stored secret is used; an echoed mask → none is sent.
- `TestIntegrationHTTP_UnscopedTestUsesOnlyBody`: `POST /{kind}/test` with no secret in the body sends none.
- `TestIntegrationHTTP_NoSecretInResponses`: list, get, put and test responses contain no secret value and no remote
  error text.
- `TestIntegrationHTTP_StatusesFollowM1_5`: 422, 409 (locked field; version conflict), 403 (a bound non-admin or
  non-interactive principal sets `Local`: `Registry`'s ProtectAdmin check, not a route guard, so this cell runs now),
  503 (secrets backend down), 404.
- `TestIntegrationHTTP_HoldReleaseCallsReleaseHold`: an active hold → `ReleaseHold` called once with the hold's scope;
  no hold → 204, no call (the 403 cell is in `…WritesNeedIntegrationsManage`).
- `TestIntegrationHTTP_AuditOncePerAction`: each state-changing route leaves exactly one record of its
  `core.integration.*` action.

## Acceptance (commands and expected output)

```bash
# Package tests with the race detector. Expected: ok
(cd core && go test -count=1 -race ./internal/integrationhttp/...)
# The skipped permission tables are in place for F7-09. Expected: >= 2
grep -c 'needs stage 13: F4-08' core/internal/integrationhttp/routes_test.go
# Vet. Expected: exit 0
(cd core && go vet ./internal/integrationhttp/...)
# Dark: nothing imports it yet. Expected: 0
grep -rn 'internal/integrationhttp' --include=*.go . core | grep -v '^core/internal/integrationhttp' | wc -l
# Layering. Expected: ok
(cd core && go test -count=1 -run 'TestCoreImportsOnlyCore|TestCoreHoldsNoAppKeyNames' ./archtest/...)
# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make -C core ci
make ci
```

## Out of scope

- Mounting the routes, regenerating `core/openapi/core.json` and the client, deleting the class-`i` `/api/v1/config`
  handlers and HEAD's test-connection handlers, and switching the SPA (all F7-09).
- `providerhttp.Registry.ReleaseHold` itself and the `core.integrations.release-holds` kind (F7-02, F2-11).
- The deferred facade writer's routes (05-metadata M1.3 "Deferred-build members").

## Rollback

Revert the PR; nothing imports the package and nothing is stored.

## Repo rules

- Worktree off fresh `origin/main`; file headers on every new file (fragments headerless).
- Conventional commit, for example `feat(core/integrationhttp): instance, test, health and hold routes (dark)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; no release or flip trailer; `gh pr merge <n> --rebase`;
  never squash; never `--admin`.
- No package-level mutable state (03 R5).

## Planner notes

- **The hold route's backend lands later.** `DELETE …/instances/{id}/hold` calls `providerhttp.Registry.ReleaseHold`
  (05-metadata M2.4), which F7-02 builds in W30, after this row's W28, and the row does not depend on F7-02. The brief
  declares a one-method consumer interface (`HoldReleaser`) so the route lands dark against a fake; F7-09 must hand it
  providerhttp's registry when it mounts the routes. If the planner prefers the real dependency, the row needs F7-02
  and moves to W31.
