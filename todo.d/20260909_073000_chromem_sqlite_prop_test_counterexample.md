- [ ] **`TestProp_ChromemMatchesSqlite` has a reproducible counterexample — chromem returns an EMPTY set where sqlite returns a match.** Found 2026-09-09 by a routine full-package `go test ./internal/server/ -count=1` on an unrelated branch; `rapid` hit it on a random seed after 0 shrink steps. Failure: `sqlite→chromem overlap too low: 0 of 1 matched (chromem set=0)` at `internal/server/dedup_engine_prop_test.go:387`.

  **Reproduced on pristine `origin/main` (0adfb5ebf)** with no other changes, so it is not branch-specific and not a flake in the usual sense — once `rapid` writes its failfile the case replays deterministically, 3/3.

  The drawn vectors are dominated by **denormals and signed zeros** — `1.5e-38`, `-1.3e-34`, `-4.2e-35`, `-3.0e-23`, `-0` and exact `0` — across 14 vectors of dimension 8 plus the query. That points at a numerical edge case rather than a logic bug: most likely a normalization step dividing by a magnitude that underflows to zero, so every embedding degenerates and chromem's result set comes back empty while the sqlite path still ranks something.

  Worth deciding explicitly which behaviour is correct before "fixing" it: agreeing on garbage may be the wrong goal, and rejecting a degenerate vector outright may be better than making the two backends match on it.

  Reproduce: the seed lives in the `rapid` failfile `internal/server/testdata/rapid/TestProp_ChromemMatchesSqlite/TestProp_ChromemMatchesSqlite-20260909032244-5371.fail`. That directory is untracked and was deliberately NOT committed (it would turn CI permanently red on an unrelated PR); a copy is preserved in this session's scratchpad. To regenerate from scratch, just run `go test ./internal/server/ -run TestProp_ChromemMatchesSqlite -count=1` repeatedly until `rapid` rediscovers it, then keep the failfile it writes.

  Note the trap this exposes: **`rapid` failfiles make a random failure look like a deterministic one.** The test passed on `main` only because `main` had no failfile — not because the bug was absent. Never conclude "passes on main, so my branch broke it" without checking `testdata/rapid/` for a file your own run just wrote.
