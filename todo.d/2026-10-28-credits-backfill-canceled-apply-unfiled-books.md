- [ ] **CREDITS-CANCELED-APPLY-UNFILED** `maintenance.credits-backfill`: a canceled
      (or stand-down-lost) apply leaves the cohort books it never reached in no
      class. Phase 1b and phase 3 stop at the first cancel; the books of the cohort
      that were not yet written are scanned and planned but are listed under no
      class, so the result's counts do not add up and a reader cannot tell them
      from books that were never eligible. File them (a `canceled_not_written`
      review class or `limit_reached`-style informational class) at the end of the
      run. Pre-existing; found in the round-6 review of PR #4200. Not fixed there.
