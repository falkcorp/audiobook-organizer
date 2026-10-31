<!-- file: docs/decisions/2026-10-30-throttle-panel.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5a83c17-2f64-4b09-9d71-0c8b4f6a2e93 -->
<!-- last-edited: 2026-10-30 -->

# Throttle panel with Reset now

## Decision log

- Build the panel and the button together (coordinator, 2026-10-30). The panel was
  the open `TODO.md` item "Throttle panel in the UI" and did not exist in `web/src`,
  so the Reset now button had nothing to attach to. Rejected: waiting for a separate
  panel task.
- Placement: a card on System > Maintenance, under Manual Fixes, which already
  hosts op-starting cards with the same optimistic-bell and error-Alert pattern.
  Rejected: the metadata settings tab (configuration, not operations) and the
  review workspace (per-book, not provider-wide).
- Start path: `startThrottleReset` in `api.ts` uses `triggerOp` + `wrapTrigger`
  (POST `/operations/v2`, `def_id: maintenance.throttle-reset`), the ops-v3
  start seam, so the bell shows the run. It always sends `dry_run:false`
  (the op previews by default) and omits `provider` for Reset all. Rejected:
  `runMaintenanceJob`, which targets the legacy `/maintenance/jobs` dispatcher.
- Running state: the panel polls the started op with `pollOperationV2` and keeps
  every reset button disabled until it settles, then refetches the list. A failed
  or interrupted run shows `error_message` in the panel's Alert.
- No confirm dialog: releasing a hold is reversible (the next failure re-holds)
  and the surrounding cards use none for it. Rejected: a preview-then-confirm
  flow like Manual Fixes, since the list on screen is already the preview.
- Poll: 4 hours (the ask in the TODO item), plus fetch on mount and a Refresh
  button. `seconds_remaining` is rendered as served, with no client clock math.
- Gating: buttons are disabled without `scan.trigger`, the permission the
  `POST /operations/v2` route enforces (advisory; the server still decides).
