<!-- file: docs/decisions/2026-10-30-admin-routes-auth-off.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d1e5b94-3a60-4c28-9f7b-e24a8c06d153 -->
<!-- last-edited: 2026-10-30 -->

# Admin-role routes with authentication off

Task: TODO-MLIST-REQUIREADMIN-AUTHOFF. `servermiddleware.RequireAdmin` answers 401 to
every request when `EnableAuth=false` (no request carries a user), while `s.perm`
passes everything in that mode. The master-list routes were given a pass-through
(`Server.adminRole()`); the other `RequireAdmin` users still answered 401.

## Decision log

- Chosen rule: with authentication off, read-only admin introspection opens through
  `adminRole()`; every route that deletes or rewrites data, and every credential
  route, keeps `RequireAdmin()` and answers 401. Reason: an unauthenticated caller on
  the LAN must not be able to wipe the store or reach a credential surface because
  the operator turned login off for a dev run.
- Chosen: the guest service token reads (`GET /admin/guest-service-tokens` and
  `/plan`) are classified as credential surface, not introspection, and stay at 401.
  They name guests and token ids, make the server call Cloudflare and read the real
  books policy; `credGuardMiddleware` is already a no-op with auth off, so opening
  them would leave nothing in front of them.
- Chosen: the three retired 410 stubs (`/maintenance/wipe`, `/cache/invalidate`,
  `/admin/recompact-digests`) keep 401. They are wipe, flush and rewrite verbs behind
  a stub; their comments and the existing gone tests promise 401 / 403 / 410 by role.
  Opening a stub would only print "gone" and gains nothing.
- Chosen: the debug API's `PATCH` (preview included, it is the same route as apply)
  and undo stay at 401; its four `GET` routes open. `admindebug.Register` takes the
  read gate as a parameter and applies `RequireAdmin()` to the writes itself, so no
  caller can open them by mistake.
- Chosen: `adminRole()` moved from `wire_master_list_routes.go` to
  `wire_admin_role.go` (several callers now), with the rule written next to it.
- Chosen: classification is enforced by recording, not by inspecting the router. gin's
  `RouteInfo` does not expose a route's middleware chain, and probing every route would
  run every ungated handler. Routes are registered through `adminRoute` / `adminGroup`,
  which add a per-route gate (before any `credRoute` guard, as the old group-wide `Use`
  did) and record `METHOD /path` with a kind. `admin_routes_test.go` requires the
  recorded set to equal two reviewed allowlists (with a reason per entry), requires each
  to be a live route, proves with a viewer session that every recorded route is behind
  an admin gate, and scans non-test source so `RequireAdmin()` cannot be wired anywhere
  but `wire_admin_role.go` and `admindebug`.
- Rejected: changing `RequireAdmin` to pass through with auth off. It also guards the
  destructive routes (the TODO says not to).
- Rejected: opening everything admin-gated with auth off, "like `s.perm`". `s.perm`
  guards library data the operator already exposed by turning login off; a store wipe
  is a different class.
- Rejected: a second middleware for the destructive group with a clearer 401 message.
  The current "authentication required" is what clients and tests already expect.
- Not changed, adjacent: the master-list `POST` upload and `DELETE` routes were opened
  with auth off by the earlier PR that added `adminRole()`. They are deletes of
  operator-uploaded lists, not of library data, and that PR's tests pin the choice, so
  they stay as they are and are not recorded in `adminRoutes`. Revisit if the owner
  wants the stricter reading (the delete would move to `RequireAdmin`).

## Route table

Every call site of `RequireAdmin()` found under `internal/`, before this change.

| Route | Kind | Before | After (auth off) |
|---|---|---|---|
| `GET /cache/stats/keys` | read-only (lists keys of one cache) | 401 | open (`adminRole`) |
| `GET /admin/debug/books/:id` | read-only | 401 | open |
| `GET /admin/debug/book-files/:id` | read-only | 401 | open |
| `GET /admin/debug/lookup` | read-only | 401 | open |
| `GET /admin/debug/edits` | read-only | 401 | open |
| `PATCH /admin/debug/books/:id` | destructive (rewrites a row) | 401 | 401 |
| `PATCH /admin/debug/book-files/:id` | destructive | 401 | 401 |
| `POST /admin/debug/edits/:edit_id/undo` | destructive | 401 | 401 |
| `POST /system/reset` | destructive (wipes the store) | 401 | 401 |
| `POST /system/factory-reset` | destructive | 401 | 401 |
| `POST /maintenance/wipe` | destructive (retired 410 stub) | 401 | 401 |
| `POST /cache/invalidate` | destructive (retired 410 stub, flush) | 401 | 401 |
| `POST /admin/recompact-digests` | destructive (retired 410 stub, rewrite) | 401 | 401 |
| `GET /admin/guest-service-tokens` | credential surface | 401 | 401 |
| `GET /admin/guest-service-tokens/plan` | credential surface | 401 | 401 |
| `POST /admin/guest-service-tokens/:userId/revoke` | destructive credential change | 401 | 401 |

Master-list routes (`POST/GET /master-lists`, `GET /master-lists/elsewhere`,
`DELETE /master-lists/:id`) already used `adminRole()` and are unchanged.

All paths are under `/api/v1`. With authentication on, every row is unchanged: 401
anonymous, 403 for a non-admin, handler for an admin.
