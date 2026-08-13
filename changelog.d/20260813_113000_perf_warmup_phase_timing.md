### Changed

- Startup now reports how long each stage of building the in-memory index took,
  instead of one combined number. The index takes about two minutes to build on
  a production-sized library, during which the library is slow and — until the
  fix earlier today — could return wrong results; the combined number said it
  was slow but not which stage to blame. The time spent committing the index is
  reported separately, because that would point at a different fix than the
  scans would.
