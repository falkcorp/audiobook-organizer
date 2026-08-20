### Fixed

- **Three maintenance-plugin guard tests had been skipped since 2026-05-12 and are
  now enabled.** They asserted that every operation declares a resume policy, declares
  its capabilities, and has a unique namespaced ID — each skipped with "requires full
  ServerDeps stub", which was not accurate: the registry interface has two methods and
  the operation definitions do not touch dependencies when they are constructed, so the
  whole table can be checked with a small fake.

  The cost was real. A new operation shipped without a resume policy, the package tests
  passed, and the failure appeared only when **the server refused to start** — the
  registry rejects an unspecified resume policy at boot, so it took down the binary
  smoke test and the end-to-end suite instead of failing in a unit test a second after
  the code was written.

  Enabling the capability check also surfaced `maintenance.auto-match-transcribed`,
  which applies metadata candidates to books but declared no capabilities at all, so
  permission enforcement had nothing to gate it on. It now declares library read and
  write.
