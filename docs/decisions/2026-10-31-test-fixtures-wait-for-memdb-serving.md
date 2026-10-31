<!-- file: docs/decisions/2026-10-31-test-fixtures-wait-for-memdb-serving.md -->
<!-- version: 1.0.0 -->
<!-- guid: 72f1304e-ddd9-4cab-ac10-0f6c1cde6e81 -->
<!-- last-edited: 2026-10-31 -->

# Test fixtures wait for memdb to serve

## Decision log

- Chose: fix the fixtures, not the store. Fixtures that need a serving memdb use
  `dbtest.NewStore` (waits, now bounded, now asserts memdb is published).
  Production fail-closed lookups are untouched.
- Rejected: making `NewPebbleStore` wait for warmup under `testing.Testing()`.
  It would change the constructor's behaviour in production code by test
  detection, and it would break the tests that exercise the warmup window on
  purpose (write-loss buffering, cold-store refusals).
- Rejected: per-test `WaitForWarmup()` calls or sleeps. They are what each
  fixture was missing; one shared constructor is the single place to get it right.
- Chose: an exported `SetWarmupStartHookForTest` seam (precedent:
  `DeleteRetiredShellForTest`) over an env var, so the late-warmup window is a
  deterministic in-test condition and cross-package tests can use it.
