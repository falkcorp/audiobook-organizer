<!-- TODO.md bookkeeping only — no code, no behaviour change, so this fragment
     is deliberately a no-op comment. See changelog.d/README.md.

     Closes the two async-warmup flaky-test items (TestApplyPIDRepairSameFile,
     TestBackfillSyncIDsJob_ConcurrentRaceSanity). Both entries asked to stay
     open until a green CI streak earned closing them; that streak is now 50
     completed Continuous Integration runs since 587b2fd0 with 0 failures, and
     both tests run under `go test ./... -short -race` with no testing.Short()
     guard, so those runs genuinely executed them.

     The cause is gone rather than quiet: WaitForWarmup in the three test
     helpers (#2131) plus buffered replay of warmup-window writes (587b2fd0,
     memdb_pending.go), covered by a six-shape acceptance suite. -->
