### Changed

- `AcoustIDScan`'s Tier-0 LSH candidate lookup now resolves its store through
  the decorator chain (`database.AsCapability`) behind a named
  `lshCandidateStore` interface, instead of a two-method anonymous assertion
  written inline at the call site. The two methods have opposite reachability —
  `GetBookFileByID` is part of `database.Store`, `LookupAcoustIDCandidates` is
  `*PebbleStore`-only — so the combined assertion silently takes the worse of
  the two and fails through any decorator. The engine holds the bare store
  today, so this is hardening rather than a fix; it is worth doing because the
  call site's nil check is silent, and a nil there drops the whole scan onto the
  O(n) segment walk with no log line and no error.
