### Fixed

#### Eight `ResumeRestart` ops that never checkpointed now each carry a real checkpoint, an explicit `ResumeDrop`, or a proof

A census on 2026-09-09 found eight registered operations declaring
`ResumePolicy: ResumeRestart` while never calling `reporter.Checkpoint`, so a
restart dispatched them with no saved state and they silently ran from zero —
`ResumeRequeue` behaviour without the idempotency review that policy requires.
Now that resume genuinely works (#3211/#3216), every one of the eight was
reviewed and given exactly one outcome, recorded in a comment beside its
`ResumePolicy`. **Checkpoint added (a):** `metadata.candidate-fetch` and
`library.bulk-write-back` now persist their remaining book ids as a done-set
(never a count or a last-id cursor, because their workers finish out of
order) every 25 books and once more on cancel; `resumeRestart` overlays that
onto the row's params, so the resumed run is handed only the unfinished tail
and skips the books it already fetched or wrote. Before this, bulk-write-back's
only checkpoint was a v1 blob keyed on a per-attempt ULID that nothing ever
read back. `runBulkWriteBack` gained an `onDone` hook for the op to observe
per-book completion; its other callers pass nil. **Downgraded to `ResumeDrop`
(b):** `entities.author-merge` (a re-issued merge for an already-deleted
author is not proven to no-op, and the op-change ledger would get a second set
of rows), `entities.resolve-production-author` (the work list is derived at
run time and a restart re-issues metadata and paid AI cover calls for every
unresolved book against daily quotas), and `maintenance.series-denumber` (a
from-zero restart would apply the NEXT `limit` series on a canary run and
overwrite the rollback report with a plan that no longer lists what the first
attempt merged). **Kept `ResumeRestart` with a proof (c):**
`maintenance.author-conjunction-repair` (selection is by current name and
every write removes its row from the selection, so a restart recomputes only
what is left), `maintenance.isbn-enrichment` (a bounded batch that resumes
from its own persisted sweep cursor — its description falsely claimed
"checkpoints every 100 books" and now says what it actually does), and
`ai.author-scan` (persisted phase rows decide whether to launch, re-attach to
the job OpenAI still holds, or refuse; nothing is billed twice). Two new tests
run each checkpointed op, cancel it partway, and resume it with the merged
params under a fresh op id, asserting the resumed run touches exactly the
books the checkpoint still owed. `internal/operations/registry/resume.go` was
deliberately left untouched (#3220 changes it concurrently); a registry-level
guard that rejects `ResumeRestart` without a checkpoint remains open.
