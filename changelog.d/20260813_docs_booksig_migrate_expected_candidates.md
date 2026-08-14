### Fixed

- Made the `maintenance.booksig-sidecar-migrate` prod procedure's pre-apply
  instrument check compare against an explicit expected candidate count
  (~27,000, from 580 MB ÷ ~22 KB) instead of "low tens of thousands", and
  recorded that a full apply may need more than one pass because memdb's
  `ListBookIDs` skips soft-deleted books. The completion signal is
  "candidates ≈ 0 on re-run", not "the apply reported no errors".
