### Fixed

#### Starting a metadata fetch no longer claims the books are "already being fetched"

Every successful metadata candidate fetch told the user it had not started. Selecting
books and hitting fetch — from the Library page, from "fetch all unmatched", or from the
stale-row refetch on /review — enqueued the operation on the server exactly as intended,
and then showed an informational toast saying those books were already being fetched in
another operation. The work ran; the UI reported that nothing had happened.

The cause was one missing line in the API client. The backend wraps every success in a
`{"data": {...}}` envelope (`internal/httputil/respond.go`), and nearly every function in
`web/src/services/api.ts` unwraps it. `batchFetchCandidates` did a bare
`return response.json()`, so it handed callers the envelope itself. `resp.operation_id`
was therefore permanently `undefined`, and all three call sites guard on exactly that:

- `web/src/components/review/lanes/useMetadataLane.ts` — "Those books are already being fetched."
- `web/src/pages/Library.tsx` (`handleFetchReview`) — "All selected books are already being fetched."
- `web/src/pages/Library.tsx` (`handleFetchAllUnmatched`) — the "already matched" fallback.

The guard itself was right: the server signals "I declined to start" by sending
`operation_id: ""`, not by omitting the key, so testing it for falsiness is the correct
read. It was the value that never arrived. Fixed in the one place it was wrong, which
corrects all three call sites at once.

The same envelope also broke the notification bell. `withOptimisticOperation` inserts a
placeholder operation before the round-trip and reconciles it against
`result.operation_id ?? result.id`; against the envelope it found neither, so it silently
removed the placeholder it had just inserted. Two of the three call sites go through it,
and both now reconcile to the real operation id.

Two things had kept this invisible and both are corrected, because otherwise the next
regression lands the same way:

- **The declared return type was flat**, promising `{operation_id: string; ...}` while
  returning the envelope, so the type checker had nothing to object to. It now returns a
  named `BatchFetchStartResponse` written against the handler rather than against what
  the callers happened to read — including the detail that the started path sends
  `total_books` while only the two "nothing to do" paths send `book_count`.
- **The component test could not observe the bug.** `ReviewWorkspace.refetchStale.test.tsx`
  mocks the whole api module, so the real function never ran there; its assertions on
  call arguments passed throughout. The regression gate now lives at the api layer, in
  `api.test.ts`, driving the real function against a real enveloped `Response`. The
  component test gains the assertion that would have caught the symptom — that a started
  refetch reports itself as started and does *not* show the "already being fetched" toast
  — plus its counterpart proving the toast still fires when the server genuinely declines,
  so the fix cannot be mistaken for "delete the guard".
