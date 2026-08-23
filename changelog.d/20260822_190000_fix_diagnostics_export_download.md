### Fixed

#### Diagnostics export never downloaded

Requesting a diagnostics export appeared to hang forever. The progress bar sat
at "generating" and the ZIP never downloaded, even though the export itself had
been written to disk successfully within a minute or two.

The export endpoint minted its own operation id, started the real work under a
*different* id, and returned the first one to the browser:

    opID := ulid.Make().String()
    _, err := store.CreateOperation(opID, "diagnostics_export", nil)
    ...
    if _, enqErr := h.registry.EnqueueOp(ctx, "diagnostics.export", params); enqErr != nil {

The id from `EnqueueOp` — the one belonging to the operation that actually runs
— was discarded. The browser polled the id it was given, which named a row the
progress endpoint had stopped serving when the v1 operation routes were retired,
so the poll never once reported completion and the download step it gates never
ran.

This is the same defect fixed for the eight duplicate-detection actions
(#2736), in a second place. The export now returns the id of the operation that
runs, and the download endpoint reads that operation's stored result.

#### Large diagnostics exports were being cancelled part-way

Separately from the above, an export that took more than five minutes was
killed while it ran. The export reported no progress at all between "starting"
and "finished", and the operations watchdog cancels anything that goes quiet
for five minutes — so on a large library the export was terminated every time,
while the half-built ZIP went on being written to a file nothing would serve.

Exports now report progress as each part of the archive is written, and stop
promptly when cancelled.

#### A failed export now says so

If an export failed, was cancelled, or was killed by the watchdog, the download
endpoint reported it as still in progress. The page waited forever and never
showed the reason, which was recorded but never read. Failures are now reported
as failures, with the reason.

The AI-diagnostics submission on the same page shares the broken poller and is
**not** fixed here — it is not yet a registered operation at all, and converting
it is tracked separately.

### Changed

#### OpenLibrary download and import report enqueue failures

Both endpoints used to answer `202 Accepted` with `"message": "download
started"` even when the work could not be queued, falling back to running it in
a detached goroutine under an id that tracked nothing — no progress, no
cancellation, no resume, and no record afterwards that it had run. They now
report the failure instead.
