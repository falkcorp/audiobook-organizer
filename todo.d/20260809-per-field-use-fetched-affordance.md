<!-- file: todo.d/20260809-per-field-use-fetched-affordance.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e3d1a47-52bc-4f09-91d6-3ab7c05e2f18 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Per-field "Use File" / "Use Fetched" one-click apply is gone from Book Detail
      — confirm that was intended.** `web/src/pages/BookDetail.tsx:1014-1015` now renders
      exactly two tabs (Info, Files & History). The old Tags/Compare tab listed every
      metadata field as a row with one-click **Use File** and **Use Fetched** buttons,
      each showing its own inline "Applying…" spinner while only that field's write was
      in flight. Neither string appears anywhere in `web/src` today. Fetched values are
      still *surfaced* — `MetadataEditDialog.tsx:188-198` labels a field's source as
      "Fetched" and pre-fills from `fetched_value` — but applying one now means opening
      the dialog and saving the whole form, so there is no way to accept a single fetched
      field. Two e2e tests covering the old flow were deleted on 2026-08-09 rather than
      left permanently skipped. If the loss was unintentional, this is the third
      capability this session's e2e sweep has found missing from Book Detail (the others:
      version management, and the Change Log "Compare snapshot" link — see
      `todo.d/20260809-changelog-row-compare-affordance.md`).

- [ ] **Visual-regression goldens exist only for darwin.**
      `web/tests/e2e/dynamic-ui-interactions.spec.ts-snapshots/` holds
      `scan-button-loading-chromium-darwin.png` and `-webkit-darwin.png` and nothing for
      linux, so `Button loading states visual check` cannot pass on CI runners — it will
      report a missing snapshot. The chromium-darwin golden was regenerated 2026-08-09
      after the spinner was masked; the **webkit-darwin one is now stale** and could not
      be regenerated locally because the webkit browser is not installed on this machine
      (`npx playwright install webkit`). Either commit linux goldens generated in CI, or
      scope this test to a single platform so it stops being a permanent red on the
      nightly e2e workflow.
