### Fixed

- **Merging authors reported "Failed to merge" for merges that actually
  succeeded.** The merge endpoint returned the id of a legacy operation record,
  but the progress dialog looks operations up in the newer operations system,
  where that id does not exist. The lookup failed, the error surfaced as a merge
  failure, and the merge itself had already completed — so the same authors could
  be merged again in a second attempt. The endpoint now returns the id the
  progress dialog can actually resolve. Affects both the Authors page and the
  duplicate-authors review tab.

### Changed

- **Author merge and production-author resolution no longer create legacy
  operation records.** Both are now native v2 operations. The undo/provenance
  ledger they write is unaffected — it is keyed per operation and read per book,
  and both sides moved together.
