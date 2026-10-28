- [ ] **TODO-RETIRED-SWAP-GUARD** a leftover from the retired-version guards
      change (4): `versions.RunVersionSwap` in `internal/versions/swap.go` has no
      retired check: a retired `toVer` would be flipped to `swapping_in` after
      `fromVer` is already `swapping_out`. It has no production caller today, so
      it is not reachable; add the same `refuseRetired` guard (both versions,
      before the first status write) with a live-control test before anything
      calls it.
