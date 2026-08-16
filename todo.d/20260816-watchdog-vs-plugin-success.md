- [ ] **Maintenance window: watchdog cancels it, then the plugin reports success.** Prod
      cancelled the maintenance window at 331s idle, after which the plugin logged
      "completed successfully (100%)". Pre-existing disagreement, but newly consequential
      after #2483: the legacy operations row is now mirrored as `canceled` while the op's
      own log claims success, so the two records actively contradict each other.
