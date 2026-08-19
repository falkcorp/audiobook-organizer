### Fixed

- Corrected the PebbleStore-surface plan's account of the 10 remaining
  `database.Store` consumer references. The count was right; the label "6 by
  design" was not. `Server.Store()` had been glossed as part of the composition
  root, when it is a public accessor handing the full 398-method type to **216
  call sites using 88 distinct methods** — the largest remaining consumer
  surface, and larger than the `StoreProvider.Store()` hole already tracked
  (111 calls / 39 methods). Re-derived with `go/packages` at full type
  resolution; `types.Info.Selections` follows the `store := s.Store()` idiom
  that grep cannot, which is why earlier grep-shaped counts undercounted the
  production refs as 5 rather than 7.
