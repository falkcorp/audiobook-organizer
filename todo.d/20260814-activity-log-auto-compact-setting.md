- [ ] **Activity log: auto-compact after 7 days, user-configurable.** Owner
      request (2026-08-14): the activity log grows into a mess — compact
      (prune/roll up) entries older than a retention window automatically,
      defaulting to 7 days, with the retention period exposed as a setting on
      the activity log screen itself (not buried in general settings). Notes:
      `maintenance.cleanup-activity-log` already exists with a midnight-daily
      schedule — the work is wiring a `activity_log_retention_days` config
      key (default 7, 0 = never) into it rather than a new job, plus the
      settings control on the ActivityLog page and a line in the log header
      showing the active retention ("entries older than 7 days are compacted
      automatically"). Follow the config rules: absent key keeps the shipped
      default (#2350 class), and the stored-zeros-shadow-defaults design
      (D111 fragment) applies to the 0=never sentinel.
