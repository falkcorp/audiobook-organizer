- [ ] **F8-HEALTH-REASONS** Give `health.Result` more than one reason —
      `core.secrets` can hold `secrets_undecryptable`,
      `derive_key_retire_blocked` and `secrets_keyring_not_exported` at once,
      but a Result carries one `Reason`, so F3-03 reports the most urgent by
      precedence and lists the rest only in `Detail`. A UI or alert keyed on a
      token misses the other two. Done means: a Result can carry every active
      token, the secrets reporter sets them all, and 05-health says how the
      component status combines them (seams.d/F3-03.md, "Health").
      - Owner: 05-health's owner (F8).
