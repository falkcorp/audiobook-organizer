- [ ] **CORE-APIKEY-EXPIRY-WARN** Under core identity the API-key expiry warning is gone:
      books' `warnExpiringAPIKeys` (`internal/server/apikey_expiry_sweep.go`) returns at once
      because its `k:` rows are drained, and core has no equivalent, so nothing now logs that
      a key is about to expire. Add a core-side warn-only sweep (log keys within the warn
      window of their expiry, and keys with no expiry once per process). It is advisory and
      never revokes or refuses a key, so it is not a widening. Due before R2.
