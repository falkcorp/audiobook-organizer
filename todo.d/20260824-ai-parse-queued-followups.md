## AI filename parsing is queued now — what to verify after deploy

`library.ai-parse` landed on 2026-08-24. The scan no longer parses filenames
inline; it queues batches of 200 candidates that run one at a time behind their
own ConcurrencyKey. Two things need eyes on real data, and neither can be
checked before a deploy.

- [ ] **Confirm a real scan actually queues.** After deploying, run a scan and
      check `GET /api/v1/operations/timeline` for `library.ai-parse` rows. The
      scan log also prints `queued N book(s) for background AI filename parsing`
      per batch. If instead the log says `failed to queue AI parsing (...)` the
      hook fell back to inline and the scan is blocking again — the fallback is
      deliberate (work is never dropped) but it is a regression to the old
      behaviour and the warning is the only signal.

- [ ] **Confirm queued results land on the version-group PRIMARY.** Pick a book
      that auto-organize copied during the same scan and whose Series the AI
      filled. The Series must be on the organized/primary row, not the
      `organized_source` row still sitting at the import path.
      `saveAIFieldsToPrimary` resolves the group and writes only the fields still
      empty there. This is the case unit tests cannot see: they all stub the
      saver, which is exactly how the bug got as far as it did.

- [ ] **Watch the params row size once.** A batch carries only the seven fields
      the AI phase reads (`aiParseCandidate` strips SegmentFiles/SegmentHashes),
      so 200 books should be tens of KB, not MB. Worth one look at a real op row
      to confirm nothing re-widened it.
