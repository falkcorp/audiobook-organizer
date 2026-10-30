- [ ] **TODO-WHISPER-RESPONSE-ECHOES-MACHINE-ID** Make `scripts/whisper_mlx_server.py`
      echo its `machine_id` in the body of every `/transcribe-batch`,
      `/transcribe-path-batch` and `/transcribe` response, and have the
      transcribe client compare it with the identity the dispatch froze for the
      port (`frozenWorker.swappedTo`) before it accepts the answer. Today a port
      handed to another machine (a tunnel coming back from another Mac) is
      caught only by a `/health` read: the liveness poll every `livenessInterval`
      while a request runs, and one probe after each successful response
      (`verifyAfterResponse`, `internal/transcribe/liveness.go`). A swap between
      the response being produced and that probe, or one the probe cannot read
      (an unreadable probe withholds the answer, which costs a re-send), is the
      residual; the identity inside the response itself closes it, because the
      answer then names the machine that produced it. Bump the worker's script
      version and `whisper_mlx_server.py`'s documented `/health` contract, keep
      the client tolerant of an older worker that echoes nothing (it falls back
      to the probe), and add a client test with a response whose echoed id
      differs from the frozen one (answers withheld, no strike, port dropped,
      journal entries invalidated as for `WorkerSwappedError`).
