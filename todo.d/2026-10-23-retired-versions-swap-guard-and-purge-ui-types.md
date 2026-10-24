- [ ] **TODO-RETIRED-SWAP-GUARD-AND-PURGE-UI-TYPES** two leftovers from the
      retired-version guards change (4). First, `versions.RunVersionSwap` in
      `internal/versions/swap.go` has no retired check: a retired `toVer` would
      be flipped to `swapping_in` after `fromVer` is already `swapping_out`. It
      has no production caller today, so it is not reachable; add the same
      `refuseRetired` guard (both versions, before the first status write)
      with a live-control test before anything calls it. Second, the web client
      type for the purge-trashed-books result in `web/src/services/api.ts`
      lacks `skipped_holds_retired_versions` and
      `skipped_holds_retired_versions_ids`, and no screen shows the new
      reason or the `HOLDS_RETIRED_VERSIONS` code yet; that belongs with the
      retired-versions UI change (PR 9).
