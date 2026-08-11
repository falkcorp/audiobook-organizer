### Fixed

- The version-group index backfill had **never run in production**. `Server.Start`
  resolved it with a bare type assertion on `s.Store()`, but the store is wrapped
  by the `indexedStore` search decorator, which embeds the `database.Store`
  *interface* and so does not promote `BackfillVersionGroupIndex`. The assertion
  missed on every boot where the Bleve index opened — which is every boot in
  production. Caught 2026-08-10 23:07:40 by the warning added hours earlier in the
  same area; before that it was completely silent. This is the likely origin of the
  under-reporting version-group index, which was never built rather than corrupted.
  Now resolved with `database.AsCapability`, which walks the `Unwrap` chain.
