- [ ] **TODO-RETIRED-REJECT-ROUTE** the Repairs reject route does not yet write a
      `repairs:retired-versions` reject (entity = the shell,
      `decisions.RetiredVersionsSubjectKey`), and that op kind has no
      reject-reason taxonomy of its own (it gets the common and `repairs:`
      ones). Two things the route must know. (1) Reopen is not unreject: the
      retired-versions fixer asks `SubjectRejected` with material hash "" (a
      sticky ask), so ANY live reject of the shell blocks it, including one that
      was reopened. A reject route for op kind `repairs:retired-versions` must
      offer unreject to release a shell, not only reopen. (2) A revert puts the
      rows back under the shell and leaves nothing behind (it deletes the retired
      version), so the shell is plannable again; the `undone` record is a label
      and never a veto. Any future writer of a `decision:` key outside
      `decisions.Store.Record` would leave the `RowCache` stale;
      `TestDecisionKeysHaveOneWriter` fails if one appears.
