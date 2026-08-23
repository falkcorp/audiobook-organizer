- [ ] **OPS-V2-DISPATCH-RACE — `dispatchCycle` can start a brand-new run after `Shutdown()` has been entered.**
      `internal/operations/registry/dispatcher.go:36` reads `r.shuttingDown` once at
      the top of the cycle, then does a `ListQueuedOperationsV2()` store round-trip and
      a dispatch loop. `Shutdown` (`registry.go:1026`) flips the flag at its top, but a
      cycle already past line 36 keeps going and dispatches. The window is a whole store
      list wide, not an instruction.
      **Measured 2026-08-23** on CI run 32655184277 (PR #2788, tip `6da3e9dcb`), log
      ordering: `enqueued op ...RD6WVXGN` → `registry: shutting down` → `dispatched op
      ...RD6WVXGN` → `run finished status=completed`. The op started *after* shutdown
      began, on a worker slot freed by shutdown cancelling the previous run.
      **Cost:** every run started this way is immediately cancelled and recorded
      `interrupted_*`, so it manufactures exactly the spurious backlog the v2 resume
      sweep lane exists to clean up (26/28 of the current prod `interrupted_quiesced`
      rows are `library.scan`). It also stretches drain time by up to one run's startup.
      **Fix shape:** re-check `shuttingDown` immediately before each dispatch inside the
      loop to shrink the window, or — the correct fix — take the dispatch decision and
      the flag under the same mutex so the check and the act cannot interleave.
      Not fixed in #2788: out of that PR's scope (maintenance resume policies + watchdog
      gate + high-water mark). Candidate for the resume-sweep lane (#2793) or its own PR.
      Worked around in `resume_shutdown_roundtrip_test.go` by planting the queued row
      after `Shutdown` returns; see the FIXTURE NOTE there.
