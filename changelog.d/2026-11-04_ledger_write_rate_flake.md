### Fixed

- `TestLedgerWriteRateBounded` no longer flakes on a loaded CI agent. It measured the wall time at which the fake ledger store was entered, which trails the runner's throttle decision by a scheduling delay that varies per commit; it now compares the runner's own commit timestamps (`Ledger.UpdatedAt`), which is what the throttle guarantees, with no tolerance.
