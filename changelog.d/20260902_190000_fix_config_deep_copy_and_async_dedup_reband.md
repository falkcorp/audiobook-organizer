### Fixed

#### A rejected config save no longer poisons the running config

`PUT /api/v1/config` unmarshalled the request body straight into the live
`AppConfig`, keeping only `prior = *c` as its undo. Both are shallow: a Go
struct assignment shares every map and slice with the original, and
`json.Unmarshal` merges into an existing map in place. So a save carrying a
typo'd `dedup.signals.confidence` kind wrote that key into the live map
*before* validation could reject it, the 400's rollback restored the very same
map, and from then on **every** later config save failed validation on a key
the operator never saved — with any unguarded `SaveConfigToDatabase` caller
(the scheduler admin endpoints, the settings handler) able to persist the
poisoned map into the settings blob, where it also blocks startup. The payload
is now applied to a deep copy (`Config.Clone`, reflection-based so the
`json:"-"` fields a JSON round-trip would zero survive), validated there, and
assigned over the live config only once it passes; every rollback restores a
deep copy too. A guard test walks the `Config` type and fails if an unexported
map/slice/pointer field is ever added, since reflection cannot deep-copy one.

#### A dedup score-ladder change no longer stalls the save, and reports honestly when the re-band cannot finish

Changing `dedup.signals` re-bands every stored duplicate candidate, because
auto-resolve reads the *stored* band. That re-band used to run inline inside
the HTTP save, on a background context, over the whole pending backlog (27,439
rows in production), with one fsync per changed row and nothing stopping a
`dedup.full-scan` from writing the same rows at the same time. It is now a
queued `dedup.rescore` operation that shares the full scan's concurrency key,
so the two are serialized; the save returns immediately with the operation's
id, and the ladder itself is applied to the live engine synchronously. The
re-band writes in batches with a single sync at the end, under the same lock
the scan takes.

When the re-band cannot be started or cannot finish, the configuration stays
saved and live — it is valid, and rolling it back left the engine, memory and
the database disagreeing three ways — and the error says so instead of the old,
impossible "dedup engine rejected the new score ladder". Five operator messages
that told you to "run dedup.rescore", an operation that did not exist, now name
the endpoint that does: `POST /api/v1/dedup/rescore {"apply":true}`.

Making the re-band a queued operation exposed a second way it could silently
not happen: the dispatcher collapses an enqueue onto an already-running
operation when the parameters are byte-identical, and every ladder change
queued the same parameters. A second ladder change arriving while the first
re-band was still running was therefore handed the running operation's id and
queued nothing — and that pass had already read its ladder when it started, so
it would finish the whole backlog under the *old* ladder while the save
reported success. The queued parameters now carry a fingerprint of the ladder
that triggered them, so two different ladders always queue two re-bands (the
second waits for the first) and re-queuing the same ladder still collapses.

A re-band also now reports how many rows the store confirmed it wrote, rather
than callers inferring it by subtracting the write failures from the changed
count. A run cancelled between two batches abandons whatever it had buffered,
so that subtraction credited rows the store never saw.

**After deploying**, run `POST /api/v1/dedup/rescore {"apply":true}` once.
27,123 of the 27,439 pending candidates are exact-layer rows, which a scan
never re-bands (they are protected on upsert), so a ladder change reaches them
only through a rescore. Run it while no dedup scan is in flight: unlike the
re-band the config save now queues, this endpoint does the work inline in the
request and does not take the scan's concurrency key.
