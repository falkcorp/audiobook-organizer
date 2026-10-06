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
  - `/dedup` Acoustic tab: cross-page Keep A / Keep B go to `bulk-link`
    with a new `keep_side` (`a`/`b`), and cross-page Dismiss to
    `bulk-reject`, filtered to pending acoustic candidates. So they get
    bulk-link's review-queue-only guard (hand-pinned, same-path and
    chain-linking pairs refused) and its per-pair recheck, instead of a
    client loop over the unguarded single-pair endpoints. Only pending pairs
    are selectable. A failed load now shows an error instead of the empty
    state, and selected rows with no row behind them count as failures
    instead of "processed".
  - `/dedup` Version Groups, Authors, Series: the old "Select All" button
    (which silently selected every page) is replaced by the header checkbox
    plus banner; merging a selection wider than the page asks first. Authors
    and Series "Merge Selected" now report "Merged X of Y; N failed: ..."
    instead of "Merged N" whatever happened (failed operations were not
    checked, and the refetch erased the error).
  - `/dedup` AI Review and Reconcile: header checkbox and shift ranges;
    applied results are skipped.
- **Bulk link and bulk reject take `expected_total`.** When set, a filter
  that now matches a different count is refused with 409 `FILTER_CHANGED`
  before anything is written; the review lane and the Acoustic tab send the
  count the reviewer confirmed and ask again when it moved.
- **`POST /api/v1/dedup/candidates/bulk-reject/revert`.** Bulk reject returns
  `rejected_ids`; this puts those rows back to pending (only if nobody changed
  them since) and removes the bulk "not a duplicate" labels. The review lane
  and the Acoustic tab offer it as Undo.
- **`POST /api/v1/dedup/candidates/bulk-reject`.** Rejects every pending book
  candidate matching the same filter body as `bulk-link` (both now bound by
  one `bindBulkCandidateFilter`), re-evaluated server-side. Writes go
  through `ReclassifyCandidate(pending -> dismissed)`, so hand-pinned (manual)
  pairs and pairs whose status changed since the list are reported as
  failures and left alone. Capped by `bulk_apply_max_items`; returns
  attempted / rejected / failed / failures.

### Fixed

- **Bulk link is pending-only.** It accepted any status, so Status=Dismissed
  plus "Merge everything matching this filter" linked every pair a person had
  marked not-a-duplicate. The server now refuses any status but pending, and
  the review lane disables the action outside Pending.

- **Version Groups and Series tabs: a single-group merge no longer moves the
  selection and the "keep" choice onto the next group.** Groups were keyed by
  array index, so removing one shifted every later key; a following merge
  could keep a book (or series) from a different group. Groups are now keyed
  by their sorted member ids.
