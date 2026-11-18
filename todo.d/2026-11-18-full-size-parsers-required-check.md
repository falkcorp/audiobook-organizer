- [ ] **FULL-SIZE-REQUIRED** Make the `Full-Size Tests (applygate, seqnum)` check
      required — once the job has reported on a queue run, add that name to the
      main ruleset (the owner's step; the ruleset is outside git), and register
      it in `ci/gates.txt` as an `own-job` `check` row, the way `vet-darwin` and
      `vet-integration` are, so `scripts/tests/test_run_gates.py` holds the
      registration. It runs `go test ./internal/applygate ./internal/seqnum` at
      full size without `-race`; until it is required, a red run shows on the PR
      but does not stop the merge queue.
