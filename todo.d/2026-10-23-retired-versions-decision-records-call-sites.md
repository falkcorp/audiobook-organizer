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

      Two more things the future reject route and revert must know. (1) Reopen
      is not unreject: the retired-versions fixer asks `SubjectRejected` with
      material hash "" (a sticky ask), so ANY live reject of the shell blocks
      it, including one that was reopened. A reject route for op kind
      `repairs:retired-versions` must offer unreject to release a shell, not
      only reopen. (2) A revert (PR 5) puts the rows back under the shell and
      leaves the retired version row behind with no rows; the resolver ignores
      such a version, so that is safe, and the version can stay. Any future
      writer of a `decision:` key outside `decisions.Store.Record` would leave
      the `RowCache` stale; `TestDecisionKeysHaveOneWriter` fails if one appears.
