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
- [ ] **TODO-RETIRED-DECISION-RECORD-PROVENANCE** the `applied` and `undone`
      retired-versions records carry no plan op id and no actor, so their source
      is `auto`: `Fixer.Apply` is handed neither (the engine keeps the plan op id
      in `ApplyDeps`/params, the actor on the apply op's row) and
      `RevertService` has neither. Thread them through (the apply op's row
      carries `ActorUserID`; `repairs.ApplyDeps` has the plan op id) so the
      records say who ran the apply or the revert.
- [ ] **TODO-RETIRED-APPLIED-RECORD-GAP** a retire whose process dies, or whose
      decision-log write fails, between the batch commit and `recordApplied`
      leaves the row with no `applied` record for good: the finish-the-tail path
      (`Apply` of a `tail_pending` row) never writes one, and a failed write is
      only logged. Write it there too, from the retired version's id and
      `RetireOpID` (`RecordUnlessSeen` makes a repeat safe), and decide whether
      retires that predate the records get a backfill.
