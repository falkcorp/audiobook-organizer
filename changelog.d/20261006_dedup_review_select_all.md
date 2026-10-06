### Added

- **Dedup review: select the page, select everything matching, shift-click
  ranges (owner request 2026-10-06).** A shared `useRowSelection` hook
  (`web/src/hooks/useRowSelection.ts`) and `SelectAllMatchingBanner` give
  every dedup surface with row checkboxes the same model: a header checkbox
  selects the current page (indeterminate when partial, unchecking clears);
  once the page is full, a Gmail-style banner offers "Select all N matching";
  Shift-click (or Shift+Space on a focused checkbox) sets every row between
  the last-clicked row and this one to the anchor's state, checking or
  unchecking a run. Filter and page-size changes clear the selection during
  render, never one frame late.
  - `/review` Dupes lane: cross-page Merge goes to the filter-scoped
    `bulk-link` endpoint and cross-page Dismiss to the new `bulk-reject`
    endpoint, both with one shared payload builder, after an MUI
    confirmation that states the count (only under the Pending status, where
    the list total is the set acted on) and warns when it is over
    `bulk_apply_max_items`. Row callbacks keep their identity across clicks,
    so the memoised rows do not all re-render on each tick. "Select all matching" is withheld
    (with the reason shown) under Both-unmatched, an unsettled search, or a
    status other than Pending.
  - `/dedup` Acoustic tab: cross-page Keep A / Keep B / Dismiss page through
    the list query (500 per request, refused above 5,000 -- the default
    `bulk_apply_max_items`) with a progress line, then apply. Links run one
    at a time. Only pending pairs are selectable and acted on (the list shows
    every status); decided pairs in a cross-page set are skipped and counted.
    A failed load now shows an error instead of the empty state,
    and selected rows with no row behind them count as failures instead of
    "processed".
  - `/dedup` Version Groups, Authors, Series: the old "Select All" button
    (which silently selected every page) is replaced by the header checkbox
    plus banner; merging a selection wider than the page asks first. Authors
    and Series "Merge Selected" now report "Merged X of Y; N failed: ..."
    instead of "Merged N" whatever happened (failed operations were not
    checked, and the refetch erased the error).
  - `/dedup` AI Review and Reconcile: header checkbox and shift ranges;
    applied results are skipped.
- **`POST /api/v1/dedup/candidates/bulk-reject`.** Rejects every pending book
  candidate matching the same filter body as `bulk-link` (both now bound by
  one `bindBulkCandidateFilter`), re-evaluated server-side. Writes go
  through `ReclassifyCandidate(pending -> dismissed)`, so hand-pinned (manual)
  pairs and pairs whose status changed since the list are reported as
  failures and left alone. Capped by `bulk_apply_max_items`; returns
  attempted / rejected / failed / failures.

### Fixed

- **Version Groups and Series tabs: a single-group merge no longer moves the
  selection and the "keep" choice onto the next group.** Groups were keyed by
  array index, so removing one shifted every later key; a following merge
  could keep a book (or series) from a different group. Groups are now keyed
  by their sorted member ids.
