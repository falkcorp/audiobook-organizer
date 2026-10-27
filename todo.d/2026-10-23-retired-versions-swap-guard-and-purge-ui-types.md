- [ ] **TODO-RETIRED-SWAP-GUARD-AND-PURGE-UI-TYPES** two leftovers from the
      retired-version guards change (4). First, `versions.RunVersionSwap` in
      `internal/versions/swap.go` has no retired check: a retired `toVer` would
      be flipped to `swapping_in` after `fromVer` is already `swapping_out`. It
      has no production caller today, so it is not reachable; add the same
      `refuseRetired` guard (both versions, before the first status write)
      with a live-control test before anything calls it. Second, the web client
      does not know the new purge reason, so a trashed book that holds only retired versions (it used to arrive as
      `skipped_owns_files` and the `OWNS_FILES` code) now degrades:
      `web/src/utils/purgeSummary.ts` leaves `skipped_holds_retired_versions`
      out of `keptReasonsOfResult` and `decidedCount` (the book is neither a
      kept reason nor decided), and `web/src/utils/deleteBookError.ts`
      `isOwnsFilesRefusal` is false for the `HOLDS_RETIRED_VERSIONS` code, so a
      delete, purge-now or discard refusal shows the generic error. The result
      type in `web/src/services/api.ts` lacks `skipped_holds_retired_versions`
      and `skipped_holds_retired_versions_ids`. The fix is small: count the new
      field with `skipped_owns_files` (or give it a reason of its own), treat
      the new code like `OWNS_FILES`, and add the two fields to `api.ts`. It
      cannot bite until something retires a row (nothing does yet), and must
      land before the first prod retire.
