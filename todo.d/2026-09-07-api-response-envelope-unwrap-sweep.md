## Sweep `api.ts` for missing `{data:…}` envelope unwraps (2026-09-07)

PR #3092 fixed ONE instance of this: `batchFetchCandidates` did a bare
`return response.json()`, returning the Go `{"data":{…}}` envelope
(`internal/httputil/respond.go:98-99`) instead of the inner object. Callers read
`resp.operation_id`, which was permanently `undefined`, so the
"already being fetched" toast fired on **every successful fetch**, and
`withOptimisticOperation` silently deleted the notification-bell placeholder it
had just inserted (it reconciles on `result.operation_id ?? result.id`).

The audit for that PR found the defect is **systemic, not a one-off**:

- [ ] **Fix the 18 confirmed functions.** Each was verified against its actual Go
  handler during the #3092 audit (not merely grepped). They return the envelope
  un-unwrapped, so every field read off the result is `undefined`.
- [ ] **Verify the 11 bare-return candidates**: `patchAudiobookRating`,
  `revertToSnapshot`, `pruneBookVersions`, `runTask`,
  `getMaintenanceWindowStatus`, `findMetadataHashDuplicates`,
  `backfillFileHashes`, `backfillMetadataHashes`, `runMaintenanceJob`,
  `getTools`, `installTool`.
- [ ] **Triage the ~30 second-shape candidates.** `searchBooks` (~`api.ts:1113`)
  is a SUSPECT only — it was deliberately not upgraded to a finding.

**Trap for whoever does this — do not blanket-apply the unwrap.**
`RespondWithList` responds **flat** (`{items, count, …}`, no `.data` key), so a
bare return is *correct* against it. None of the 18 confirmed use it, but every
remaining candidate must be checked **per Go handler**, never assumed from the
call shape.

**Also add the structural guard**, or this returns: the reason the bug survived
was that the declared TS return type claimed a flat shape (a lie) AND the test
fixture mocked that same flat shape, so type checker, test, and buggy code all
agreed with each other. Honest return types are what make a wrong fixture a
compile error.

Separately noted while fixing, pre-existing and NOT fixed: the batch-fetch
handler sends `total_books` on the started path but `book_count` only on the
"nothing to do" paths, so `handleFetchAllUnmatched` always prints "unmatched".
