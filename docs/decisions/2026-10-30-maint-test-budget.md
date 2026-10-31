<!-- file: docs/decisions/2026-10-30-maint-test-budget.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7c1e5d52-3b0a-4f6e-9a47-2d8b6e0c4a19 -->
<!-- last-edited: 2026-10-31 -->

# MAINT-TEST-BUDGET: maintenance package test time

## Measurement (2026-10-30, `nice -n 15 env GOMAXPROCS=2 go test -p 2 -race`, busy CI Mac)

The brief's premise did not reproduce. Whole package, before: 13:05 wall (245 s user CPU, most of it
the -race compile on a loaded host). Top per-test time in that run: 15.2 s
(`TestJunkTitleFixer_IndexAuthorPassIsOrderIndependent`); the four named tests were not in the top 25.
Run alone from a prebuilt -race binary at GOMAXPROCS=2:

| Test | Before |
|---|---|
| TestFragmentFixer_DemoteOnlyFirstOpRevertsToOnePrimaryInEitherOrder | 32.5 s |
| TestFragmentFixer_DeletedSurvivorIsNotAMerge | 13.1 s |
| TestITunesClone_RollbackFailureKeepsStateOnListedBook | 43.4 s |
| TestFragmentFixer_HeldRowActionsClearTheHold | 13.2 s |

The 141-180 s CI numbers are wall time of parallel tests contending for two cores, not their own cost.

## Decision log

- Chosen: in the demote-only test, build one fixture per cut point to probe whether the cut landed
  between demote and soft-delete (order-independent), instead of two. Cut points that miss cost half.
  Assertions unchanged.
- Rejected: parallelising the iTunes clone subtests. `newICFixture` swaps the global
  `config.AppConfig.RootDir`; parallel use would need a production change.
- Rejected: lowering sweep strides in the fragment tests; that removes coverage.
- Left: the other three named tests (already sampled by `fragSweepStride`; the cost is per-cut-point
  fixture seeding of 16 books). A shared seeded store snapshot would be the next step.

## After

Demote-only test, alone, same conditions: 32.5 s -> 16.4 s (passes). The other three are unchanged (see Left). The second whole-package run was not done: the coordinator deadline hit.

## Round 2 (2026-10-31)

Measured alone from a prebuilt -race binary, GOMAXPROCS=2, niced:

| Test | Before | After |
|---|---|---|
| TestITunesClone_RollbackFailureKeepsStateOnListedBook | 11.2 s wall (1.75 s CPU: fsync wait) | 1.55 s |
| TestFragmentFixer_HeldRowActionMatrix (8 subtests) | 24.0 s wall, 34.0 s user | 19.2 s wall, 25.5 s user |
| TestFragmentFixer_DeletedSurvivorIsNotAMerge | 8.4 s | 7.5 s (unchanged, noise) |
| TestFragmentFixer_HeldRowActionsClearTheHold | 5.6 s | 4.5 s (unchanged, noise) |

- Chosen: `newICFixture` opens `NewPebbleStoreInMemory`. The iTunes subtests spent their time on
  fsync, not CPU, so the in-memory store fixes the cost without a seam in production code and
  without touching the global `RootDir` swap. Supersedes the round-1 rejection of a seam: it was
  aimed at the wrong cost.
- Chosen: the action matrix runs a held row's first action on the discovery fixture. Its state
  after `cutAt` and the scenario is what a replay rebuilds. Later actions still replay.
- Rejected: sharing one seeded store across subtests; every case mutates it and Pebble in-memory
  has no cheap clone.
- Left: `DeletedSurvivorIsNotAMerge` needs two fresh fixtures per cut point (the attack mutates the
  state the restore action starts from). 30% of the matrix CPU is `PathResolver.walk` `Lstat`
  calls from `repairs.guardBookPaths` in production code, built per Apply; caching it is a
  production change outside this item.
