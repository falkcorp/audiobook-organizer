### Fixed

- **The filter Drawer could stall mid-close and leave an invisible full-viewport
  backdrop that swallowed every click until reload — and the 2026-08-10 "fix"
  for it had only narrowed the race, not closed it.** `web/src/theme.ts` now
  sets `MuiDrawer.defaultProps.slotProps.transition = { exit: false }`, which
  makes react-transition-group take its synchronous branch in `performExit`
  (`safeSetState({status: EXITED})` from inside `componentDidUpdate`) instead of
  deferring completion to a `setTimeout`. Instrumented traces show the deferred
  update is the thing that gets lost: the timer fires, RTG calls `setState` on a
  mounted instance, and React never applies it — the same instance re-renders
  4 ms later still reporting `exiting`, and is still reporting `exiting` 300 ms
  later. `onExited` therefore never runs, MUI's `Modal` never unmounts, and its
  `position: fixed; inset: 0` backdrop keeps eating clicks while looking
  perfectly closed.

  Measured on chromium at `workers=1`, n=10 per cell. Before, on the MUI 6.5.0
  branch: an Escape with no preceding CDP round-trip stalled **10/10**. After:
  **0/10**, and the real gate (`scan-import-organize.spec.ts` "complete
  workflow") went from failing to **10/10 passing**.

  The previously shipped `transitionDuration: { exit: 0 }` is retained for the
  enter animation but is *not* the fix; on `main` (MUI 5.18.0), which already
  carried it, the same defect still reproduced 9/10 when the Escape was preceded
  by a round-trip. MUI v6 did not introduce this bug — it moved the timing
  window onto the common path. The comment in `theme.ts` that credited `exit: 0`
  has been corrected in place with the measurements that disprove it.

  Still unexplained, and labelled as such in-code: why React silently drops a
  `setState` issued from a `setTimeout` on a mounted class component inside a
  portal. Duplicate React copies, StrictMode, `flushSync`/`startTransition`,
  uncaught exceptions and unmounting are all ruled out.
