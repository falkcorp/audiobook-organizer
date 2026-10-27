- [ ] **TODO-WHISPER-MIDREQUEST-IDENTITY** Detect a whisper worker that is swapped
      for another machine in the middle of a request — a pool probes a port again
      after every cooldown wait and uses it only if the worker that answers is
      the one the dispatch froze for it (D60/D62, PR #4201), but nothing checks
      again after that re-probe. A tunnel that comes back from another Mac while
      a request is in flight (the request timeout is minutes) hands its answer,
      or its timeout, to the old machine's name: the answer is stored with the
      frozen identity, and a timeout strike or a condemnation names a machine
      that never saw the file. `pollWorkerLiveness` in
      `internal/transcribe/liveness.go` already reads `/health` every
      `livenessInterval` for the whole request and throws away `machine_id`.
      Proposal: compare the
      `machine_id` it reads (through the same `resolveMachine` rules the gate
      uses, so a worker that missed its id and answers as `u:` is not a swap)
      with the identity frozen for the port, and cancel the request with a new
      cause on a mismatch. The outcome must be neither stuck (it must not count
      toward the ops' stuck streak) nor an outage (it must not bench the
      endpoint process-wide): the jobs are deferred without a strike, the port
      is dropped from the dispatch like a port that changed hands, and the next
      dispatch's gate keys it. Decide what happens to results the swapped worker
      has already returned for the request before the probe saw the swap (they
      cannot be told apart from the old machine's): withhold the whole request's
      answers and strikes, as a worker fault does, rather than store them under
      the frozen identity. Done means a test with a worker whose `machine_id`
      changes while its request is running, asserting no result and no strike is
      stored under the old identity and the dispatch continues on the other ports.
