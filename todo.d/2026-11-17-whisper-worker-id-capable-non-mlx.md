- [ ] **WHISPER-IDCAPABLE** Go `idCapable` only recognises the MLX worker —
      `whisper_server.py` now reports `machine_id` and `machine_id_capable` like the
      Mac worker (shared `whisper_machine_id.py`), but the Go side still treats a
      non-MLX backend as unable to identify itself. Make the check follow the
      reported capability rather than the backend name, and fix the stale comments
      that say only the MLX worker has a machine id (`workerid.go`, `canary.go`,
      `stuck_test.go`, `canary_resolution_test.go`).
