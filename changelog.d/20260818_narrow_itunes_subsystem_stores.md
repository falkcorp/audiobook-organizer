### Changed

- Narrowed four of the iTunes service's per-subsystem store interfaces to the
  methods a compiler probe measured them using: `WriteBackStore` 102 transitive
  methods → 5, `pathReconcilerStore` 108 → 4, `provisionerStore` 51 → 5, and
  `playlistSyncStore` 9 → 4. Each previously embedded whole `database.*`
  surfaces. No behaviour change — no function bodies were edited, and the
  `interfacebloat` count is deliberately unchanged, because all four declared
  fewer than 8 entries before and after. The width they carried was never
  visible to the linter.
