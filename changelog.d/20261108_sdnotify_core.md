### Changed

- Internal: systemd readiness notifications are sent by the core runtime. The
  server no longer sends `READY=1`, `STATUS=` or `STOPPING=1` itself, and a
  startup step that asks systemd for more time now also extends the core
  runtime's own 60 second start limit for that step.
