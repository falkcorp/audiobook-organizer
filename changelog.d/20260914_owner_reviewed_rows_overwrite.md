### Changed

- Metadata apply: any row the owner approves in the review lane now overwrites
  filled descriptive fields, like the single-book apply, whether the gate
  passed it or the approval lifted a refusal (owner ruling 2026-09-14). Only a
  lifted refusal is recorded as a gate override in the change history. The
  apply, its rename preflight and the bulk-apply preview share one options
  value.
- Paths that may overwrite: the single-book apply and review-lane approved
  rows. Paths that stay fill-only: batch rows with no pin or a non-row pin,
  `/metadata/batch-apply-candidates`, auto-fetch, the upgrade job, and
  `maintenance.auto-match-transcribed`.
- `maintenance.auto-match-transcribed` is now fill-only: it may fill an empty
  title or author and never replaces a filled one. Books with both filled are
  skipped (and no longer counted as eligible in a dry run).
- An automatic (fill-only) metadata apply now records the match (review
  status `matched` or `audio_confirmed`, `metadata_source` and
  `metadata_source_hash`) only when the book ends up holding the candidate's
  title. One that keeps a different title (a locked title, or the auto-match
  filling only an empty author) writes its fields and leaves the review
  status, source and dedup hash alone, so the book stays in the review lane
  instead of reading as verified. An apply a person chose (the single-book
  dialog with any field selection, or a review-lane approval) still records
  the match whatever title the book keeps, so a deselected or locked title
  does not leave it in the review lane.
- `maintenance.auto-match-transcribed` writes nothing when no field is left to
  fill (the match has no value for the empty field, or it is locked): no
  status, version note or source stamp. The book is skipped and not counted
  as eligible. An apply that only adds an author credit counts as a change
  and is committed with its history.

### Removed

- `metafetch.Service.PreviewMetadataCandidate`, which had no caller outside
  tests; use `PreviewMetadataCandidateWithOptions`.
