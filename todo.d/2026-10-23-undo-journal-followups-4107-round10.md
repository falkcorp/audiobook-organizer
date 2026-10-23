- [ ] **TODO-UNDO-JOURNAL-4107** undo journal follow-ups from #4107 round 10 —
      non-blocking items from the round-10 and round-11 reviews of #4107 (N1, N2,
      N4 and N6 from round 10, N3 from round 11), filed instead of fixed in that PR:
      - [ ] N1 — the journal-clock seed tests overstate what they cover. Only
        the present-key and missing-key cases open the store through
        `NewPebbleStore`; the undecodable-row, malformed-key and list-entry
        seed tests call `seedJournalClock` on a bare `&PebbleStore{db}`, and
        the staging of more than 2,000 missing list entries (more than one
        chunk) has no test. Drive those cases through a real open and add a
        test with more than 2,000 missing entries.
      - [ ] N2 — the orphan simulation counts against the old graph filter
        (the marker history's ignored list), not against
        `RevertResult.OrphanMarkers`, and its orphans sit in operations that
        are never reverted, so the post-pass in `internal/audiobooks/revert.go`
        that reports and stamps orphans is covered by one end-to-end test only.
        Make the simulation assert on `OrphanMarkers` and revert the orphans'
        own operations in some histories.
      - [ ] N4 — an orphan marker whose own operation is never reverted is
        reported by every later revert of that list, forever. Either stamp a
        standing orphan that has no row in the same batch that writes the list,
        or document how the owner clears it.
      - [ ] N6 — `orphansIgnored` is an error value that is returned on success
        when the row's own error is nil (`revert.go`, around the
        `withOrphans` and `orphansIgnored` definitions). It is safe only
        because `RevertOperation` is the sole caller of `revertChangeIn`. Make
        the orphan markers a field of the row's result instead of an error.
      - [ ] N3 — the open-time scan fails the whole store open on a transient
        read error that is not "not found" (a `Get` of a list entry or of the
        completion keys). Consider a retry with backoff at boot before giving up.
