### Added

#### `/api/v3/ops/*`: operations v3 definitions, runs, exact counts, timeline and SSE

New read endpoints for the v3 run model, beside the unchanged `/api/v1/operations*`
routes: `GET /api/v3/ops/defs` (effects, schedule and next fire, readiness, last
finished run, params schema), `GET /api/v3/ops/runs` and `/runs/{id}`
(`progress.chunks` and `progress.workers` included, a stopping run with a live
goroutine reads `state=stopping`, `zombie=true`), `GET /api/v3/ops/runs/{id}/rows`
(paged failed and skipped items, with `truncated` when the per-run cap dropped
some), `GET /api/v3/ops/counts?group_by=state|def|outcome` and
`GET /api/v3/ops/timeline`. Counts and `total` are exact and every count carries
an `href` whose list has exactly that many rows; a timeline cut short by its scan
cap says `complete=false`. `GET /api/v3/ops/events` streams `event: run` with a
heartbeat every 15 seconds, `Last-Event-ID` resume from a bounded ring and an
`event: resync` for a client too slow to keep up (the registry is never blocked).

`POST /api/v3/ops/defs/{id}/runs` (also `POST /api/v3/ops/runs` with the
definition in the body) checks the definition's own permission (settings.manage
when it declares none), validates params against its JSON Schema before anything
is queued (400 with field paths), answers 409 with the reason while the
definition is not ready, and returns 202 `{run_id}`. `POST .../runs/{id}/cancel`
needs the definition's permission or being the run's starter. `POST
.../runs/{id}/approve` needs the approver permission and a verified auth method;
a request authenticated only by a forwarded header is refused with 403. No
approval backend exists in the runner yet, so a fully authorised approve answers
501. Reads need library.view.

The schema check is a small in-tree checker for the keywords a params struct
produces (no new dependency); a schema using a keyword it cannot check is refused
rather than passed.
