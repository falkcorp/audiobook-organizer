### Fixed

- `database.GetOpsV2` and `server.resolveVGBackfiller` declared their parameter as
  `database.Store` while doing nothing but forwarding the value to `AsCapability`,
  which takes `any`. That imposed all 398 methods on every caller to satisfy a call
  that constrains nothing — the same reasoning already recorded in `GetAIJobs`'s doc
  comment fifteen lines below `GetOpsV2`. Both now take `any`.

  These were the last two *production* declarations pinning a narrowed
  `Server.Store()` at the full interface. The measured required width of
  `internal/server` drops from **398 to 268**, and the callees demanding the whole
  type fall from 4 to 2 — the survivors being the composition root and a test
  helper. This does not narrow the accessor; it removes the floor that made
  narrowing arithmetically impossible.
