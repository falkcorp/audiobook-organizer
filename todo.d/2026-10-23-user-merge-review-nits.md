- [ ] **USER-MERGE-REVIEW-NITS** Admin user-merge follow-ups from the #4118
      review: (1) the temp-login token purge (PLAN Apply step 1) is not
      implemented, so a pending temp-login token for the merged-away account
      survives the lock; (2) `refuseForActiveOps` ignores scheduled fixer runs
      whose params carry the absorbed id (decision D5): it only sees queued and
      running operations; (3) the grant token is visible in the op params while
      the run is queued: store only its hash on the op row and compare hashes on
      redeem; (4) `DeactivateUser` on a merged-away account leaves it locked, so
      an undo reactivates it: refuse or record that status change as a conflict;
      (5) `Server.UserMerge()` caches a nil service permanently through its
      `sync.Once` when the store is not ready at first use: retry until it is;
      (6) the locked re-plan under every merge lock runs `warnAboutHistory`,
      which scans every `merge:combine-journal:` value: skip the warnings in the
      re-plan.
      (7) `JSONStringField` and `RewriteJSONField` match field names
      case-sensitively while the struct decoders fold case: unreachable for rows
      written by `json.Marshal`, but a hand-written row with a differently cased
      owner key would be read as unowned: make both match like the decoder, or
      refuse a case-folded duplicate; (8) the owner-family refusals name a row
      by position ("row 3"), which an admin cannot map to a row without
      `AdminDetail`: return the position-to-key map in the admin-only detail.
