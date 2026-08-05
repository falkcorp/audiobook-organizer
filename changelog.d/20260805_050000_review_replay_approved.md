<!-- file: changelog.d/20260805_050000_review_replay_approved.md -->
<!-- version: 1.0.0 -->
<!-- guid: 847a0a62-8ea1-4714-a331-929c4048bd3e -->
<!-- last-edited: 2026-08-05 -->

### Added

- **`POST /api/v1/review/replay-approved`** — re-runs the apply handler for review
  items already marked `approved`.

  🔴 **Approved decisions were being silently discarded.** `approveOne` applies an
  item only *inside* the approve request, and only when `review_apply_enabled` is on.
  With the switch off it records `status="approved"` and returns a note — and nothing
  ever read that state back. Before this endpoint, `ReviewStatusApproved` appeared in
  exactly two places in the entire codebase: the constant, and the single line that
  sets it.

  So a human could work through hundreds of holds in review-only mode and lose every
  decision:

  - flipping `review_apply_enabled` later does **not** revisit them, because apply
    only ever happens inside approve; and
  - the regroup scan reports them as `already-decided` and **skips** the folder, so
    they are never even re-offered.

  With ~928 holds currently queued, that is a large amount of human judgement with
  nowhere to land. This makes the queue safe to work through *before* apply is
  enabled.

  Dry-run by default, reporting what would replay and which items have no handler
  (779 of the current queue are `regroup.ambiguous`, which has none by design).

  **Refuses to execute while the global switch is off**, with a 409, rather than
  quietly doing nothing — silently re-marking items without doing the work is the
  exact failure this exists to fix.

  A failing apply leaves the item `approved` so a later replay retries it; marking it
  applied after a failure would strand the work just as before.

  5 tests, including the full round-trip — approve with apply off, flip the switch,
  replay, and assert the handler actually ran and the status became `applied`.
