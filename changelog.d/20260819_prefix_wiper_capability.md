### Changed

- The six `/maintenance/wipe` helpers (`wipeBookFiles`, `wipeSegments`,
  `wipeBooks`, `wipeAuthors`, `wipeSeries`, `wipeExternalIDs`) now resolve their
  raw key-space operations through a named `prefixWiper` capability interface
  instead of `database.AsPebbleStore`. No behaviour change — `AsPebbleStore`
  already walked the decorator chain, so both spellings resolve identically
  today. What changes is that `internal/server` no longer names
  `*database.PebbleStore` for this, which is a prerequisite for ever splitting
  that type. `WipeByPrefixes` and `CountByPrefix` were compile-probed and are
  both absent from `database.Store`, so they share the same reachability and the
  composite carries no hidden weak member.
