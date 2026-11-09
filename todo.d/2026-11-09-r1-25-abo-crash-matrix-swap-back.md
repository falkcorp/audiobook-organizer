- [ ] **PLATFORM-CORE-R1-25-ABO-SWAP-BACK-FIXTURE** ABO's boot crash matrix
      lists `swap.back.mark`, `swap.back.mv`, `swap.back.sidecars` and
      `swap.journal.swapped_back` as unreached, because books' restore
      stager opens the extracted store with this build's guard before it
      stages, so no ABO-staged restore is refused after the swap. Add a
      fixture that stages, through `crashmatrix.Stage` and not the stager,
      an archive restore whose store this build refuses at open (core keys
      without an install marker, as the `foreign-keys` fixture), drop the
      four labels from `aboUnreachedLabels`, and the matrix then breaks
      ABO's swap-back writers too (hello's matrix breaks them today).
