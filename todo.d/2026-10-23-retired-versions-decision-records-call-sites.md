- [ ] **TODO-RETIRED-DECISION-RECORD-CALL-SITES** the retired-versions Apply
      must call `recordRetiredVersionApplied` (internal/plugins/maintenance,
      `retired_versions_vetoes.go`) once per row after the row's retire commits,
      and the revert path must call `recordRetiredVersionUndone` (or
      `decisions.RecordRetiredUndone`) once per reverted row. PR 8 ships the
      helpers, tested, but wires neither: the Apply is PR 7 and the revert is
      PR 5, both in flight on their own branches. Also: the Repairs reject route
      does not yet write a `repairs:retired-versions` reject (entity = the
      shell, `decisions.RetiredVersionsSubjectKey`), and that op kind has no
      reject-reason taxonomy of its own (it gets the common and `repairs:` ones).
