### Fixed

- **ABS surface: guard the `/api` → `/api/v1` redirect against endpoints we have not built
  yet.** Every existing check on this surface derives from `absRouteList()` — from what we
  *implement* — so an endpoint the real client requests but we have not written is absent
  from that list and nothing checks it. If its sub-tree is not reserved, the call 301s into
  the app API and answers `200` in the app's shape: implemented-looking and broken, the exact
  failure mode that produced #2332, #2333 and #2335. The new guard derives instead from the
  golden fixtures' recorded `request.path`, which is the only in-repo record of what the
  client *asks for*, so a newly captured endpoint is covered the moment its fixture lands.
  All 28 captured paths are reserved today; this is a ratchet, not a bug report. Confirmed
  by removing a reserved prefix and watching it fail on the exact fixture and path.
