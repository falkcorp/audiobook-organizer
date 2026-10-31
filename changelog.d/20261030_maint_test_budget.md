### Fixed

- `TestFragmentFixer_DemoteOnlyFirstOpRevertsToOnePrimaryInEitherOrder` builds one fixture, not two, for each cut point that misses the demote-to-soft-delete window (MAINT-TEST-BUDGET, partial).
- `TestITunesClone_RollbackFailureKeepsStateOnListedBook` and the other iTunes clone tests use an in-memory store, so the 20-subtest rollback matrix no longer waits on fsync (11 s to 1.6 s).
- `TestFragmentFixer_HeldRowActionMatrix` runs a held row's first action on the fixture that found the hold instead of rebuilding it.
