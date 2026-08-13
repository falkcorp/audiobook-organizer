### ABS API — sweep every endpoint for accepted-but-ignored query parameters

Three separate bugs on 2026-08-13 were the same defect wearing different hats: an
endpoint accepted a query parameter and read it with nothing, then answered `200`
with a wrong-but-plausible body.

- `GET /api/libraries/:id/items` — `filter` ignored, so every filtered request
  returned the whole library. This is what made series show "random books".
- `GET /api/libraries/:id/series` — `page`, `limit`, `sort` ignored. `limit=100`
  and `limit=500` both returned all 14,625 rows.
- (fixed earlier) the same surface's `sort` on items, noted in `absItemFilter`.

All three were invisible to the 28-fixture conformance oracle because **no fixture
carries a query parameter at all** — the corpus bounds what it can prove, and a
parameter that never appears in a capture can never be asserted on.

Work to do:

1. Enumerate every ABS route from `absRouteList()` and, for each, diff the query
   parameters upstream ABS 2.36.0 documents against the ones the handler actually
   reads. `c.Query(` / `c.GetQuery(` grep is the starting point, not the answer —
   the failure mode is a parameter read by *nobody*, which greps as absence.
2. For each unread parameter decide explicitly: honour it, or return an empty /
   error response. Never silently ignore — an ignored filter is strictly worse
   than an unimplemented one, because the wrong answer looks like a right answer.
3. Consider a test that drives each route with a parameter set and asserts the
   response *changes*. A parameter that provably makes no difference is the bug.
4. Re-capture the oracle fixtures with query parameters present, so the
   conformance suite can see this class at all.
