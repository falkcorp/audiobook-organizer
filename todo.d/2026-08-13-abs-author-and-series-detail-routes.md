### ABS author and series detail routes are unimplemented and redirect into a 404

Found by enumerating the paths the app ACTUALLY requested in the server log, rather
than by reading our own route table — the table can say which routes exist, never
which absent routes are being asked for.

```
  6 /api/v1/authors/:num      <- the redirect target
  3 /api/authors/:num         <- what the app asked for
  1 /api/series
```

The 1:2 ratio is the redirect signature (each request logs the 301 and the target).
Probed against production:

| route | result |
|---|---|
| `GET /api/authors/:id` | 301 → `/api/v1/authors/:id` → **404 "endpoint not found"** |
| `GET /api/series/:id` | 301 → `/api/v1/series/:id` → **404** |
| `GET /api/series` | 301 → `/api/v1/series` → app-API shape (`{"data":{"items":[…]}}`) |

There is no `GET /authors/:id` in `wire_entities_routes.go` at all — only
`/authors/:id/books`, `/authors/:id/aliases`, `DELETE /authors/:id` and friends. So
the ABS author page asks for an author and gets a 404, which the ABS contract tells
clients to treat as "unsupported, degrade gracefully" — it renders empty, silently.
Same failure the playlist detail route had.

**This is NOT a quick prefix reservation.** The app API has live routes at
`GET /series/:id/books`, `PATCH /series/:id`, `PUT /series/:id/name`,
`POST /series/:id/split`, `DELETE /series/:id`, and the author namespace is denser
still. Reserving `/api/series/` or `/api/authors/` wholesale is exactly the defect
that took out 46 live app routes twice (#2332 → #2335) and again, more narrowly, in
the playlist reservation. Use `absCollisionDetailRoutes`, which matches on method
plus exactly one segment.

Work:

1. `GET /api/authors/:id` — ABS author DTO with its `libraryItems`. The id is ours
   (it comes from our own `/libraries/:id/authors` list), so no id-mapping problem.
2. `GET /api/series/:id` — ditto; the per-series book build already exists and is
   cached (`seriesBooksCached`).
3. Decide what bare `GET /api/series` should do. It currently redirects to an
   app-API shape an ABS client cannot parse — the "looks implemented, behaves
   broken" case. Either serve it or reserve it as an honest 404.
4. Each addition needs a `TestPlaylistReservationDoesNotSwallowAppSubRoutes`-style
   companion listing that namespace's app routes, or the next widening repeats this.
