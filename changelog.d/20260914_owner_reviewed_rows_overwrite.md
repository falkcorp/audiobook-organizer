### Changed

- Batch metadata apply: a review-lane row the owner approved is hand-picked, so
  it may now overwrite filled descriptive fields like the single-book apply
  (owner decision 2026-09-14). Its apply and rename preflight share one options
  value, and the bulk-apply preview shows the overwrite for a row only an owner
  review can land. Unreviewed batch rows, batch-apply-candidates, auto-fetch
  and the upgrade job stay fill-only.
