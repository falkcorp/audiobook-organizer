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

- [ ] **Confirm queued results land on the version-group PRIMARY.** This is the
      SECONDARY path and it is the one with no production evidence behind it.
      `saveAIFieldsToPrimary` resolves the row by ID first, which is correct on
      its own for the common case; the group redirect only fires when that row
      turns out to have been demoted to a non-primary member. Pick a book that
      auto-organize COPIED (not renamed in place) during the same scan and whose
      Series the AI filled: the Series must be on the organized/primary row, not
      the `organized_source` row still sitting at the import path. Unit tests
      cannot see this — they all stub the saver, which is how the bug got as far
      as it did.

- [ ] **Decide what to do about organize running before the parse.** Named as a
      known regression in the changelog: auto-organize fires when the scan ends,
      which is now before the queued parsing drains, so a book organized in the
      same scan is filed using pre-AI metadata. `{series}` is the visible one —
      the row gets the series, the file stays in a non-series folder, and nothing
      re-organizes it. Worst on a first import, where every book is a candidate
      because no row exists yet. The fix is for `library.ai-parse` to re-organize
      the books it materially changed (`internal/server` already imports
      organizer, so it can call `OrganizeOneBook` directly) — but that moves
      files on the strength of an op's output and needs a deliberate decision,
      not a drive-by.

- [ ] **Two books from one version group in one batch can lose a field.** The
      saver redirects a demoted row to its group's primary, so two hash-duplicate
      sources in the same batch have two workers doing a concurrent whole-row
      read-modify-write on the same primary: last writer wins. Needs row-level
      serialization. Noted in `ai_batch_phase.go`; narrow enough that it was left
      unfixed deliberately.

- [ ] **Watch the params row size once.** A batch carries only the seven fields
      the AI phase reads (`aiParseCandidate` strips SegmentFiles/SegmentHashes),
      so 200 books should be tens of KB, not MB. Worth one look at a real op row
      to confirm nothing re-widened it.
