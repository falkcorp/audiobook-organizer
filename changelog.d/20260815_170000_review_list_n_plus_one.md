### Fixed

- **The metadata review listing no longer takes 20–35 seconds.** It is requested
  with `limit=0` ("return all rows"), and it did two sequential `GetBookByID`
  point reads per entry — once to compute status counts and again to build each
  row — plus a serial `GetCachedCandidates` per entry, over the entire pending
  set. Production served it in 21.7s and 35.2s, which was timing the UI out.
  Books are now fetched in one batch call (`GetBooksByIDs`, which preserves input
  order) and reused across both passes, and the cached-candidate reads run
  concurrently with the response order preserved.
