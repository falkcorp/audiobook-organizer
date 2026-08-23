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
