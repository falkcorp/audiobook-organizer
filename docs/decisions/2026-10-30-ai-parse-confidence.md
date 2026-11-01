<!-- file: docs/decisions/2026-10-30-ai-parse-confidence.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5ed93222-ecb8-4602-8894-5f95e412e689 -->
<!-- last-edited: 2026-10-30 -->

# AI parse: read the model's confidence, do not gate on it

Decided 2026-10-30 under the owner's autonomy window (take the recommended option, log the
alternatives). Covers two TODO items: the `ai parse summary` that read fully green for a batch
that wrote nothing, and `ParsedMetadata.Confidence` being requested and never read.

## Decision

1. **Discarded rows are their own outcome.** `AIPhaseSummary.BooksParsed` now counts only rows
   the saver actually resolved (a changed row or an attempted-but-unchanged row). A row the saver
   cannot resolve returns `("", nil)`; it is counted in the new `BooksDiscarded`, named in
   `FailureDetails()` (capped at 10), and rendered as `N discarded (book not found / nothing saved)`.
   A save that returns an error is a save failure and is no longer also counted as parsed.
2. **Discard is a warning, not a failure.** `AIPhaseSummary.Degraded()` is true when rows were
   discarded and the run did not fail. The operation registry has only completed/failed (no
   completed-with-warnings status), so the op logs the summary at warn level and still returns
   nil; the inline scan path reports it through `ReportTo`. Progress counts parsed + discarded so
   the bar still reaches 100%.
3. **Keep requesting confidence and start reading it.** The tally (`confidence high N / medium N /
   low N [/ unrated N]`) is in the summary line, each saved row logs its confidence at debug level,
   and the change-history source of an AI-filled field becomes `scan.ai-parse:<confidence>`
   (bare `scan.ai-parse` when the model gave no recognised value). Nothing in the tree matched
   on the old source string.
4. **No save is gated on confidence.**

## Rejected alternatives

- **Drop the field from the prompt and struct.** Saves a few completion tokens per row, but the
  measured data in the TODO entry shows the field predicts correctness (7B series-number accuracy
  100% high / 77% medium / 38% low). Throwing away a signal that is already being paid for, while
  the gate decision is still open, forecloses the option for no gain.
- **Gate saves on `low`** (skip low fields, or route them to review). The numbers favour it (2.6:1
  wrong-to-correct writes avoided) but it changes what gets written to real rows, and it leaves
  open whether a low title means "skip the field" or "skip the row". That is a behaviour change the
  owner has not approved. Reading and recording the confidence now produces the data to make that
  call later (query history rows by source suffix).
- **Fail the operation on any discard, or on a discard ratio threshold.** A row deleted or merged
  between nomination and the batch is legitimate (and prod has ~15.8% book_file rows pointing at no
  file), so failing would train everyone to ignore the status. A ratio threshold was the TODO's
  suggestion; it needs a tuned number, so the first version warns on any discard and the counter is
  there to threshold on later.
- **Add a dedicated confidence column or history field.** `MetadataChangeRecord` has no free-form
  field; a new one is a schema change across every history writer. Suffixing the existing source
  keeps the change local to the AI saver.
