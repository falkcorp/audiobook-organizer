<!-- file: PLAN.md -->
<!-- version: 1.2.0 -->
<!-- guid: e9651b18-d6ed-4dcb-bb13-1e1f0d1e98f7 -->
<!-- last-edited: 2026-08-27 -->

# Runtime mismatch review filter

## Goal

Let metadata review users hide rows whose runtime does not differ, without
hiding rows that lack enough duration data to determine a difference. Preserve
the existing shift-click inclusive selection behavior and add regression proof
for both library views.

## Affected files

- `web/src/...metadata-review...` — add the filter state and apply the shared
  runtime-difference helper when rendering rows.
- `web/src/...QueueRail...` — expose the filter control with its explanatory
  tooltip.
- Focused metadata-review tests — first prove the requested visibility cases,
  then keep them green through implementation.
- Library grid/list selection tests — prove Shift-click selects the complete
  inclusive range in each existing view.

## Steps

1. Locate the existing metadata review, QueueRail, runtime helper, and selection
   tests; record exact files before changing behavior.
2. Add failing metadata-filter and Shift-click range regression tests.
3. Implement the opt-in metadata filter using the shared helper and add the
   QueueRail control/tooltip.
4. Run focused web tests and the frontend build; commit each reviewable change.
5. Push the branch without opening or merging a pull request.

## Test strategy

- `npm test -- --run <focused metadata review and library-selection suites>`
- `npm run build`

## Rollback

Revert the feature commits. The filter is UI-only and defaults off, so rollback
restores prior visibility with no data migration.
