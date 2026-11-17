- [ ] **WHISPER-IDCAPABLE** Go `gatedEndpoint.idCapable()` (`internal/transcribe/workerid.go`)
      is `Probed && Health.Backend == backendMLX`, so only the MLX worker is ever
      treated as able to report a machine id. `whisper_server.py` now reports
      `machine_id` in `/health` like the Mac worker (shared `whisper_machine_id.py`,
      sent only when the worker has a stable id) and `backend: "faster-whisper"`, but
      the Go side still treats it as unable to identify itself. Make `idCapable`
      follow whether `/health` carried a well-formed `machine_id` rather than the
      backend name, and fix the stale comments that say only the MLX worker has a
      machine id (`workerid.go`, `canary.go` near line 314, `stuck_test.go`,
      `canary_resolution_test.go`).
