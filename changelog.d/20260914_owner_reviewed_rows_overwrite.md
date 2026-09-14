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

### Removed

- `metafetch.Service.PreviewMetadataCandidate`, which had no caller outside
  tests; use `PreviewMetadataCandidateWithOptions`.
