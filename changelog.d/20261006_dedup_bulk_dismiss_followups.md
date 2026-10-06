### Fixed

- **Undoing a bulk dismiss can no longer lose an earlier verdict or skip a
  dismissed pair.** Follow-ups to the review of #3783.
  - A bulk dismiss now writes an undo record for every pair it dismisses: the
    label it replaced, or an explicit "no earlier label" marker
    (`SaveLabelBeforeBulk`). If the record cannot be written, the not_dup
    capture is skipped, the dismiss is rolled back and the pair is reported as
    failed. A pair only appears in `rejected_ids` once its record is saved.
  - The revert works from that record rather than from the label reason, so a
    pair whose best-effort not_dup capture failed can still be undone. A pair
    whose label was re-decided since the bulk dismiss is left alone. Records
    written by #3783 are still read: the old bare-label record, and a bulk
    label with no record behind it.
  - A bulk-dismiss label is never saved as the "earlier verdict". An existing
    record is kept as it is; with no record, the save is refused.
  - If the status goes back to pending but the label restore fails, the pair
    is reported as failed and its record is kept. Sending the id again
    finishes the restore.
  - A book that cannot be read (GetBookByID error, e.g. a corrupt row or a
    failed signature hydration) is no longer treated as a deleted book. The
    candidate list, bulk-count, bulk-link and bulk-reject now refuse the
    request instead of silently dropping the pair from the count and the
    action.
  - "Select all N matching" and the "N selected (every page)" count now use
    the server's bulk count, the number the confirmation dialog shows, in the
    review lane's dupes panel and in the acoustic dedup tab. The banner reads
    "counting…" until that count arrives and labels the list total as
    approximate if the count fails. The dialog still recounts at confirm
    time.
