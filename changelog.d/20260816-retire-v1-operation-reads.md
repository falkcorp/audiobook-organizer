### Fixed

- **Operation status and logs no longer read a table that never says "done".** The
  legacy `operations` table does not transition rows out of `pending` — read against
  production on 2026-08-16 it reported **183 of 200 rows pending**, some six days
  old, while the v2 record for the same window showed 179 completed, 13
  interrupted_dropped, 6 canceled, 1 failed. `GET /operations/:id/status` and
  `/operations/:id/logs` tried v2 first and *fell back* to that table, so anything
  that fell through got a confidently wrong answer. Both now read v2 only.

### Changed

- **Retired `GET /api/v1/operations`, `/operations/:id/status` and
  `/operations/:id/logs`.** Replaced by `GET /operations/timeline` and
  `GET /operations/v2/:id`. The list route had no caller at all.
- `GET /api/v1/operations/v2/:id` now accepts **`?limit=`** (capped at 5000, default
  50) so it covers the retired `?tail=`, and the diagnostics log provider is served
  from v2 rather than the legacy handler.
