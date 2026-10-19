### Removed

#### Review > Metadata no longer has a full-index mode, and `review_metadata_server_query` is gone

The Metadata lane always asks the server for one filtered page (`view=page`) plus the chip
counts, and fetches full rows for the rows on screen with `ids=`. The browser-side path that
loaded every reviewable row and filtered, counted, grouped and paged them in memory is
deleted, along with the setting that chose between the two ways, so a Review tab can no
longer hold the whole library. Select all N matching, the title filter, the provider and
confidence filters, the runtime, language, transcription and multi-book switches and every
chip view are answered by the server, as they have been while the setting was on.

`GET /api/v1/audiobooks/metadata/cache/review?view=index` now answers `410 Gone` with a JSON
body (`code` `VIEW_INDEX_GONE`, `use` `view=page`) and is recorded in
`review_index_request_seconds` under `view="gone"`, so a client that still sends it shows up
in the metric. The error text tells an old browser tab to reload the page. `view=page`, `ids=`
and the default listing keep their shapes; `view=page` rows now carry `owner_rejected` (the
row's Rejected badge reads it; the page says `owner_rejections_unavailable` when the rejections
could not be read) and the response and every `ids=all` list carry `bulk_apply_max_items`, so
the lane learns the apply cap without the settings permission.

The setting `review_metadata_server_query` is removed. A stored config or settings row that
still contains it loads without error; startup logs that it is ignored, a config-file save
drops it, and a `PUT /config` carrying it is refused with the removal message like the other
retired settings.

The precondition for this change (the setting on for a full release) was waived by the owner
on 2026-10-19 ("Go ahead now"). The setting has been on in production since 2026-10-18 16:05
with no review errors, 02-PR18 (`941d9521c`) shipped in v0.222.2, and no full release has gone
out since the setting was turned on.

### Changed

#### Review latency metric: `view` labels are `page`, `ids_all`, `full`, `gone` and `selection`

`review_index_request_seconds{view}` records every `view=page` request under `view="page"`
(whatever it answers, a refusal included) and an id list (`ids=all`) under `view="ids_all"`,
next to `full` (the listing), `gone` (the retired `view=index`) and `selection` (the selection
check below). `view="index"` is no longer
written. The overnight dashboard's "Review page p95" panel (the 3s target) reads `view="page"`,
and its latency, request-rate and slow-request panels include page traffic.

#### Selecting books across chips, and a filter change

When a filter changes, or a chip is left, the lane drops the selected books the new view hides.
It asks the server with `POST /api/v1/audiobooks/metadata/cache/review/selection` (the page's
filter in the query string, the selected ids in the body, at most the bulk apply cap of them,
`422` past it), which answers which of those books the filter lists, which it does not, and
which it does not hold at all; the lane no longer fetches the whole library's id list twice.
`bucket=any` is accepted only there; `view=page` refuses it. A Title change drops the selected
books whose title no longer matches in any chip. Leaving a chip checks the selection against
the filters the chip had paused. If the check cannot be made the lane says so: outside a chip
the selection is cleared, inside a chip it is kept but applying it is held until a check
succeeds, the selection is cleared or the filters change.

#### A split bulk apply is held to the cap as a whole

Select all N matching sends its apply in chunks of 500. Each chunk names its batch
(`batch_id`, an unguessable random id), the batch total (`batch_total`) and its own position
(`batch_index`) on `POST .../batch-apply-cached`. The server keeps one ledger entry per user and
batch id and refuses a chunk when the declared total is over `bulk_apply_max_items`
(`BULK_APPLY_CAP_EXCEEDED`), when the total changes mid-batch (`BATCH_TOTAL_MISMATCH`), or when
the chunks add up to more than the declared total (`BATCH_EXCEEDED`; the message says earlier
chunks were already queued, except on the first chunk, where nothing was). A resent chunk
(same `batch_index`, same books) is neither counted nor queued again: the server answers
`202` with the op id of its first send, and the same index with different books is a `422`
`BATCH_CHUNK_MISMATCH`. A request is validated before it is recorded, so refused requests
occupy nothing, and a full ledger evicts the least recently touched batch (the user's own
first) instead of refusing anyone. A user holds at most 16 live batches; the ledger forgets an idle batch after 30
minutes.

This is an accident guard, not a quota. It enforces the total a cooperating client declares: a
client that omits `batch_id` or sends a fresh one per request gets only the per-request check,
and there is no per-user rolling window. A request with no batch id is checked against the cap
as one request, as before. `batch_id`, `batch_total` and `batch_index` go together, and a
batched request with no `book_ids` is a `400`.

The selection check now asks about a changed Title over both buckets even when the view leaves a
chip inside the debounce. A check that cannot be made never drops the selection: it is kept and
bulk apply stays held, with a message saying how to clear the hold, whether the server refused the
Title pattern, answered with an error, was busy, or timed out (each check has a 15 second
deadline). A busy server and a timeout are asked again at most 5 times. While the first snapshot
after a restart is still building, the endpoint answers `503` `REVIEW_SNAPSHOT_BUILDING` with
`Retry-After` at once instead of waiting, and the lane keeps asking for up to 5 minutes outside
that budget; a failed build that is backing off answers `503` `REVIEW_SNAPSHOT_BACKOFF` with the
remaining wait.

A resent batch chunk must now match the first send in everything the apply does (books, `mode`,
`write_back` and pins), not only its books, or it is refused with `BATCH_CHUNK_MISMATCH`. A
batched chunk that repeats a `book_id` is a `400`.

While a selection is being checked against new filters, or its check failed, every command that
acts on the selected books is held, not only apply and reject: "Write details into audio files",
"Search again" (button and menu), "Skip selected" and "Reject selected" are disabled with the
reason, and refuse again if invoked anyway. A snapshot-building wait that was left over from
another view no longer makes a later visit to the same filters give up at once.
