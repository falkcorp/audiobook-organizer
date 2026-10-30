<!-- file: docs/decisions/2026-10-30-abs-handlers-test-hash-cost.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4d1a66e9-f550-401f-ae3f-11e1f9f7c3a6 -->
<!-- last-edited: 2026-10-30 -->

# Lower the hash cost in the ABS handlers tests

## Decision log

- Added `TestMain` plus a proof test (`TestMainLoweredTheHashCost`) in a new
  `internal/server/handlers/abs/main_test.go`, the shape #4261 used for
  `internal/server`. The package is `abs_test`, so the file calls the seam
  through the `absauth` import and satisfies `TestHashCostSeamStaysInTests`.
- Not applied to `internal/server/handlers` (the only other package that
  hashes passwords in tests): `auth_login_timing_test.go` there compares the
  median time of a real-user failed login to an unknown-name one and an
  OAuth-only one, within a 0.5x to 3x band. At m=64,t=1 each derivation takes
  microseconds, so that ratio would become scheduler noise and the test would
  flake. Making that package cheap needs the timing test to keep the production
  cost (a separate package-level decision), so it is left for its own change.
- Not applied to `internal/server/absauth`: its tests pin the production
  parameters.

## Amendment (same day): 8 MiB, not 64 KiB

The first cut used m=64,t=1. CI (run 41109536409, shard 4/4) then failed
`TestLogin_FailedAbsLoginTimingDoesNotRevealWhetherTheNameExists`, which lives
in this very package and has the same shape as the `internal/server/handlers`
timing test described above: at 64 KiB a derivation is microseconds and the
0.5x to 3x band is scheduler noise. The cost is now m=8192,t=1 (8 MiB). Measured
under `-race` with GOMAXPROCS=2 on the Mac agent: about 30 ms per derivation,
medians within 10% of each other across three runs, the timing test at 0.33 s.
That keeps the derivation dominant over HTTP handling while a 32-login throttle
test stays around one second instead of 30 to 60 s at the production cost.
