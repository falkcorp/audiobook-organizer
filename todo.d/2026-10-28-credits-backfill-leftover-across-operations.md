- [ ] **CREDITS-LEFTOVER-ACROSS-OPERATIONS** `maintenance.credits-backfill`: a record
      left by an apply that crashed and is NEVER resumed is still taken for a
      preexisting record by any other operation. The leftover seeding reads only the
      resumed operation's own journal rows (`created_narrators` of its
      `credits_backfill` rows and its `narrator_create_intent` rows), and each
      `limit` cohort runs under its own operation id, so a split book in a later
      operation can link to the record although no creating write ever credited it.
      Close it by letting an apply also read the intent rows of other
      `credits_backfill` operations whose last attempt did not complete (judged
      against that operation's first row, unlinked, fail closed), or by sweeping
      them from a maintenance op. Not a regression; found in the follow-up to PR
      #4216.
