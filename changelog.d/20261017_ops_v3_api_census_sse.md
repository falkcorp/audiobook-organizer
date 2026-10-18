### Added

#### `/api/v3/ops/*`: operations v3 definitions, runs, exact counts, timeline and SSE

New read endpoints for the v3 run model, beside the unchanged `/api/v1/operations*`
routes: `GET /api/v3/ops/defs` (effects, schedule and next fire, readiness, last
finished run however long ago it finished (null if 256 or more newer runs
are still unfinished), params schema), `GET /api/v3/ops/runs` and `/runs/{id}`
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
server starts shutting down; at most 64 streams are open at once (then 503 with `Retry-After`)
and at most 4 for a signed-in user's browser session plus 4 shared by all of
that user's API keys (then 429 with `Retry-After`), so a script cannot take the
session's places and minting keys buys no more streams; a stream
ends when a write to the client fails.

`POST /api/v3/ops/defs/{id}/runs` (also `POST /api/v3/ops/runs` with the
definition in the body) needs scan.trigger, as `POST /api/v1/operations/v2`
does, plus the definition's own permission (settings.manage when it declares
none), validates params against its JSON Schema before anything
is queued (400 with field paths), answers 409 with the reason while the
definition is not ready, and returns 202 `{run_id}` (or `{"batched": true}`
when a batchable definition added the request to a pending batch). The mode is
written into params under every mode key the operation's own params type
decodes, declared on the operation (`OperationDef.Preview` names that type) and
shown as `preview_keys` in `/defs`: a preview writes `dry_run`/`dryRun` true and
`apply`/`live` false, a live run the opposite, so an operation that writes only
when `apply && !dry_run` is never silently a preview on a live run. An omitted
mode is a preview unless params already state the mode. A mode that
contradicts a mode key in params, two mode keys that disagree, a mode key the
operation does not read (a `dry_run` sent to an `apply` operation), or a key that
differs from a mode key only in case (`APPLY`, which encoding/json would decode
into `apply`) is a 400. `itunes.clone-into-library` lists its three write kinds
in `live_one_of`: a preview sets all three false and a live run must name
exactly one. `maintenance.series-phantom-repair` refuses a live run in report
mode and `maintenance.opchange-book-index-rebuild` refuses `ensure=true` at
start instead of queueing a run that writes nothing or fails. A writing
definition that declares no preview (`has_preview=false`: it ignores every mode
key, or, like `metadata.batch-apply-cached`, previews only through a different
operation) is never queued as a "preview": a preview or a request with no mode
is a 409, and only an explicit `mode: "live"` starts it, with params passed
through untouched. A read-only definition's params are never touched, so a
strictly decoded operation is not handed a `dry_run` it does not declare, but a
mode key in them that contradicts the asked mode is a 400. An operation counts
as writing when it declares a write capability or the rows it writes
(`OperationDef.Writes`); six operations that wrote while declaring only read
capabilities now declare their writes: `itunes.playlist-import`,
`maintenance.cover-text-read` and `maintenance.file-provenance-export` already
previewed by default; `deluge.path-update`, `maintenance.regroup-shattered-ai`
and the full-transcribe path of `maintenance.transcribe-book-intros` now take
`dry_run` and preview when it is omitted (pass `dry_run: false` to run them for
real). `maintenance.transcribe-book-intros` and
`maintenance.repoint-version-primary` also read the `dryRun` spelling, as the
rest of the operations do. Five operations that declare no writes yet write on
every run (`acoustid.scan`, `ai.author-scan`, `dedup.split-book-scan`,
`library.bulk-metadata-fetch` and the paid `diagnostics.ai-analyze`) are
treated by v3 as writing definitions without a preview: only `mode: "live"`
starts them, pending an owner decision on whether each gains a preview mode.
`POST
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

### Fixed

- A start of a scheduled or deduplicated operation is no longer answered with
  the ID of an active run in the other mode. `EnqueueOp` folded a live start of
  `maintenance.author-split-scan` into the cron's queued preview run, so the
  caller was told its live run had started and nothing was written. A
  definition that declares its preview mode now folds a start only into an
  active run of the same mode (its mode keys and `live_one_of` switches
  compared after normalization); otherwise a second run is queued.
- The `maintenance.regroup-shattered-ai` preview no longer counts a group
  whose review hold a person already decided as a hold it would write; it
  reports those as already decided.
- `scripts/transcribe_monitor.py` reads each relaunched run back and raises
  `RELAUNCH_IS_PREVIEW` (an ERROR alert and a banner on stderr) when the
  server queued a preview.
