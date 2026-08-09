<!-- file: todo.d/20260809-changelog-row-compare-affordance.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b1f7c2e-9a83-4d16-b0e5-7c2a41d8f903 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Change Log rows lost their visible "Compare snapshot" affordance and are
      mouse-only.** `web/src/components/ChangeLog.tsx:135-154` renders each entry as a
      plain `<Box onClick={...}>` that fires `onCompareSnapshot` for `tag_write` /
      `metadata_apply` entries. There is no `role`, no `tabIndex`, no keyboard
      handler, and no label — the old "Compare snapshot" link that used to sit in the
      row was removed. The flow itself still works end-to-end (verified in
      `web/tests/e2e/files-history.spec.ts`: clicking the row does raise
      `snapshot-comparison-banner` in the open format tray), so this is purely a
      discoverability/accessibility gap, not a broken feature. Deciding what replaces
      it is a product call: restore a visible link/button, or keep the row click and
      give it `role="button"` + `tabIndex={0}` + Enter/Space activation + an
      `aria-label`. Note the row already contains a Revert `<Button>` that calls
      `stopPropagation`, so any keyboard handler has to not double-fire there.

- [ ] **Dead `expanded` state in `TagComparison`.** `web/src/components/TagComparison.tsx:69`
      is `useState(true)` and `setExpanded` is never called, so the `<Collapse in={expanded}>`
      at line 249 is always open. Either drop the state and the `Collapse`, or wire up the
      toggle that was evidently intended (the e2e suite still had a `tag-comparison-toggle`
      testid assertion for it until 2026-08-09).
