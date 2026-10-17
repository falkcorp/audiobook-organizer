### Added

#### `/api/v3/ops/*`: operations v3 definitions, runs, exact counts, timeline and SSE

New read endpoints for the v3 run model, beside the unchanged `/api/v1/operations*`
routes: `GET /api/v3/ops/defs` (effects, schedule and next fire, readiness, last
finished run, params schema), `GET /api/v3/ops/runs` and `/runs/{id}`
(`progress.chunks` and `progress.workers` included; `state` is always the stored
state, so a row matches the filter that listed it, and a run whose goroutine
outlived the abandon grace has `zombie=true` and `display_state=stopping`), `GET /api/v3/ops/runs/{id}/rows`
(paged failed and skipped items, with `truncated` when the per-run cap dropped
some), `GET /api/v3/ops/counts?group_by=state|def|outcome` and
`GET /api/v3/ops/timeline`. Counts and `total` are exact and every count carries
an `href` whose list has exactly that many rows; a timeline cut short by its scan
cap says `complete=false`. `GET /api/v3/ops/events` streams `event: run` with a
heartbeat every 15 seconds, `Last-Event-ID` resume from a bounded ring and an
`event: resync` for a client too slow to keep up (the registry is never blocked).
Event ids are `<boot id>-<sequence>`, so an id from before a restart resyncs
instead of resuming at the wrong place; a run whose terminal event the registry
feed dropped is still published once it settles; streams end as soon as the
server starts shutting down; at most 64 streams are open at once (then 503).

`POST /api/v3/ops/defs/{id}/runs` (also `POST /api/v3/ops/runs` with the
definition in the body) needs scan.trigger, as `POST /api/v1/operations/v2`
does, plus the definition's own permission (settings.manage when it declares
none), validates params against its JSON Schema before anything
is queued (400 with field paths), answers 409 with the reason while the
definition is not ready, and returns 202 `{run_id}`. A writing definition
previews by default. One that has no preview mode (the operations recorded
`no-mode`, which ignore `dry_run` and always write; `has_preview=false` in
`/defs`) is never queued as a "preview": a preview or a request with no mode is
a 409, and only an explicit `mode: "live"` starts it. `POST
.../runs/{id}/cancel` needs being the run's starter, or scan.trigger plus the
definition's permission; an interrupted or awaiting-decision run is not
executing, so cancelling it is a 409. `POST
.../runs/{id}/approve` needs the approver permission and a verified auth method
(a session, an API token or a verified Cloudflare Access sign-in); a request
authenticated only by a forwarded header is refused. No
approval backend exists in the runner yet, so a fully authorised approve answers
501. Reads need library.view.

The schema check is a small in-tree checker for the keywords a params struct
produces (no new dependency); a schema using a keyword it cannot check, or a
supported keyword with a malformed argument, is refused rather than passed, and
a number too large to compare with a bound is a violation.
