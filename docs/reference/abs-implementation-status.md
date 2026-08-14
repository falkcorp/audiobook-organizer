<!-- file: docs/reference/abs-implementation-status.md -->
<!-- version: 1.0.0 -->
<!-- guid: c2a94e17-5b60-4f83-a1d9-8e35f0b62c74 -->
<!-- last-edited: 2026-08-14 -->

# ABS implementation status — ground truth as of `12c62625` (2026-08-14)

Phase 0 deliverable of `.github/prompts/abs-implementation-completion.prompt.md`.
Every claim below states how it was derived. Where a claim is carried over
rather than re-verified, it says so.

## Method

1. **Runtime router dump** — the ABS handler was constructed with the full
   test harness (library + user-data capabilities) and `Register`ed on a fresh
   gin engine; `router.Routes()` was dumped. **45 routes.** Grep was not used
   for route discovery (gin composes grouped paths; text search is blind to
   them).
2. **`Handler.Register` read** (`internal/server/handlers/abs/handler.go:369`)
   — exactly **one conditional block**: the four bookmark routes register only
   when a bookmark store is present (`:496`). Production wiring
   (`wire_abs_routes.go:359-364`) asserts that store and exits otherwise, so
   the **production surface is 45 + 4 = 49 routes**.
3. **App-API dump** — `setupTestServer` (ABS off, the default deployment):
   407 routes. Used to verify the collision/redirect claims.
4. **Fixture extraction** — `request.method` + `request.path` from all 28
   captures in `testdata/abs-fixtures/`.
5. Reference docs: `abs-upstream-api-reference.md` (223 upstream routes),
   `abs-target-client-contract.md` (§6.1, §6.6, §11).

## Headline findings (what changed vs the 2026-08-11 audit)

| Audit item | Status verified 2026-08-14 |
|---|---|
| N-1 socket.io answered 200 HTML | **FIXED** — `/socket.io/` is in `nonSPAPrefixes` and `/socket.io` in `nonSPAExact` (`spa_fallback.go:57,64`) |
| N-2 conformance harness cannot see a wrong value | **FIXED 2026-08-12** — `assertConformant` passes `CompareValues: true` (`abs_test.go:474`) |
| N-3 | RETRACTED upstream; not re-examined |
| N-4 unimplemented paths 301 into /api/v1 | **FIXED for the no-twin namespaces** — `/api/collections` and `/api/podcasts` 404 honestly (`absUnimplementedNamespaces`); twins (`/api/authors`, `/api/series`, `/api/playlists`, `/api/users`) deliberately keep redirecting (`absAppAPICollisions`); `GET /api/playlists/:id` is claimed ABS-side when enabled |
| N-7 four golden fixtures never loaded | **APPARENTLY CLOSED** — all 28 fixtures are referenced from test files (6 outside `assertConformant` call sites; assertion depth for those 6 not individually read this pass — see below) |
| N-8 `absRouteList()` under-reports | **STILL TRUE, now precise** — the hand list has **47** entries; the real surface is **49**. Missing: `GET /auth/openid`, `GET /auth/openid/callback`. No phantom entries (every hand-list entry is registered). |
| "no fixture carries a query parameter at all" (params-sweep fragment) | **OVERBROAD** — 5 fixtures carry params: `items?limit=10&page=0`, `authors?sort=name&minified=1&limit=100&page=0`, `search?q=odyssey`, `items/:id?expanded=1&include=progress`, `me?populated=1`. The sweep's point stands for everything else. |

## The served surface — all 49 routes, classified

Classes: **VC** = value-conformant (asserted against an oracle fixture with
`CompareValues: true`, allowances bounded) · **HT** = handler-tested (value
assertions in the named test file; no oracle fixture) · **REF** = fixture
referenced in a test file, assertion form not individually read this pass ·
**STUB** = answers with empty/fabricated data · **UT** = registered, test
depth not established this pass.

| Route | Class | Evidence |
|---|---|---|
| GET /ping | VC | abs_test.go |
| GET /status | VC | abs_test.go |
| POST /login | VC | abs_test.go (allowances) |
| POST /auth/refresh | VC | abs_test.go (allowances) |
| POST /logout | UT | registered `handler.go:384` |
| GET /auth/openid | HT | openid_test.go; **absent from absRouteList()** |
| GET /auth/openid/callback | HT | openid_test.go; **absent from absRouteList()** |
| POST /api/authorize | HT | authorize_test.go |
| GET /api/me | VC | abs_test.go (identity allowances) |
| GET /api/me/sessions | VC | abs_test.go |
| DELETE /api/me/sessions/:id | UT | registered `:391` |
| GET /api/libraries | VC | browse_test.go |
| GET /api/libraries/:libraryId | UT | no fixture (list fixture only) |
| GET /api/libraries/:libraryId/items | VC | browse_test.go (allowances) |
| GET /api/libraries/:libraryId/personalized | VC | browse_test.go (allowances) |
| GET /api/libraries/:libraryId/series | VC* | browse_test.go — *fixture captured against an EMPTY library; cannot settle the `books` contract (see gaps) |
| GET /api/libraries/:libraryId/collections | **STUB** | `h.EmptyPage` (`handler.go:403`) — the ONE remaining stub route |
| GET /api/libraries/:libraryId/playlists | HT | playlists_test.go (real data since #2366) |
| GET /api/playlists/:id | HT | playlists_test.go |
| GET /api/libraries/:libraryId/authors | VC | browse_test.go + authors_narrators_test.go (paginated) |
| GET /api/libraries/:libraryId/narrators | VC | browse_test.go |
| GET /api/libraries/:libraryId/filterdata | VC | browse_test.go (allowances) |
| GET /api/libraries/:libraryId/search | VC | browse_test.go (allowances) |
| GET /api/libraries/:libraryId/recent-episodes | UT | registered `:416`; podcast surface — expected empty; not read this pass |
| GET /api/items/:id | VC | browse_test.go (allowances) |
| GET /api/items/:id/cover | UT | no-credential route; 404-when-missing is by design |
| POST /api/items/:id/play | VC | play_test.go (allowances) |
| GET /api/items/:id/file/:ino | UT | registered `:429` |
| GET /api/items/:id/file/:ino/download | UT | registered `:430` |
| POST /api/session/:id/sync | REF | play_test.go |
| POST /api/session/:id/close | REF | play_test.go |
| GET /public/session/:id/track/:index | UT | unauthenticated by design |
| GET /api/me/progress | VC | progress_write_test.go (allowances) |
| GET /api/me/progress/:id | VC | progress_write_test.go (allowances) |
| PATCH /api/me/progress/:id | REF | progress_write_test.go |
| PATCH /api/me/progress/batch/update | REF | progress_write_test.go |
| DELETE /api/me/progress/:id | REF | progress_write_test.go |
| GET/POST /api/me/progress/:id/remove-from-continue-listening | HT | remove_continue_listening_test.go |
| GET/POST /api/me/item/:id/remove-from-continue-listening | HT | remove_continue_listening_test.go |
| GET /api/me/listening-stats | HT | stats_test.go |
| GET /api/me/listening-sessions | HT | stats_test.go |
| GET /api/me/stats/year/:year | HT | stats_test.go |
| GET /api/me/item/listening-sessions/:id | HT | stats_test.go |
| GET /api/me/bookmarks/:id | VC | progress_write_test.go |
| POST /api/me/item/:id/bookmark | VC | progress_write_test.go |
| PATCH /api/me/item/:id/bookmark | VC | progress_write_test.go |
| DELETE /api/me/item/:id/bookmark/:time | REF | progress_write_test.go |

Tallies: **VC 22 · HT 13 · REF 6 · UT 7 · STUB 1.**

The honest residue: the 6 REF routes and 7 UT routes need their assertion
depth established (or added). That is bounded follow-up work, listed in the
gap table below — not a reason to distrust the classification of the rest.

## 404-by-design / 301-by-design (verified by test read, not re-probed)

- **404**: `/api/collections`*, `/api/podcasts` (+ subtrees) — no /api/v1 twin
  exists (pinned by `TestUnimplementedNamespacesHaveNoAppAPITwin`, which walks
  the real router).
- **301 → /api/v1**: `/api/authors`, `/api/series`, `/api/playlists` (bare
  list), `/api/users` — live app-API twins (19/18/9/7 routes).
  `TestCollidingNamespacesStillRedirect` pins it.
- *`/api/collections` will move from 404 to served when collections ship —
  it must then move to the reserved lists, per the comment at
  `wire_abs_routes.go:190`.

## Client demand (the 28 fixtures) — fully served

Every fixture-requested route is on the served surface. No fixture requests a
route we lack. Therefore: **no client-demanded route is absent**; the absent
surface (below) is absent-by-decision or absent-without-observed-demand.

## Absent surface (223 upstream − 49 ours)

Not re-derived here — `abs-upstream-api-reference.md` §1/§3 is the inventory
and `abs-target-client-contract.md` §11 records the by-decision drops (admin,
backups, notifications, emails, tools, RSS, custom providers, podcasts,
users). **Caveat carried from 2026-08-13:** §11 once listed playlists as
"safe to stub" and a real client falsified that; §11 entries bound what the
fixtures prove, not what clients do. Notables with no serve path today:
series/author DETAIL (`/api/series/:id` 301→404; `/api/authors/:id`
301→404; `/api/libraries/:id/series/:seriesId` absent), plural
`/api/sessions*` history, item writes, Socket.IO (`cancel_scan` has no REST
equivalent anywhere upstream).

## Gap list this doc scopes (maps to the 2026-08-14 task breakdown)

1. **Collections** — the only stub route + the only missing entity (B10–B13).
2. Series `numBooks>0` with `books: []` on prod (B20) — NOT visible to the
   conformance suite because the series fixture holds zero series (B41
   re-capture).
3. Series detail via `/api/libraries/:id/series/:seriesId` (B21); author
   detail (B22); series list pagination params (B23); `/series/count`
   discrepancy (B24).
4. REF/UT routes: establish or add value assertions (small, shardable).
5. `absRouteList()` missing the 2 OpenID routes (one-line fix + the guard
   test's claim becomes true again).
6. Ignored-query-params sweep (B30) — fixture-param correction noted above.
7. Phase-3 decisions doc (B50).

## What was NOT verified this pass

- Prod behaviour of any route (this is a code/test-level census; prod probes
  are Wave-A work).
- Assertion depth of the 6 REF + 7 UT routes.
- `recent-episodes` handler semantics.
- The upstream 223-route inventory (trusted from the reference doc, which is
  dated 2026-08-12 and grounded in upstream source).
