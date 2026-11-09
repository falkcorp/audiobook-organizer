- [ ] **PLATFORM-CORE-R1-25-OFFLINE-SWAP-EXCLUSION** `storage
      restore-offline` in place and `storage reset` check that no process
      holds the store (`kernel.refuseHeldStore`, a probe of Pebble's
      `<db>/LOCK`), and the in-place restore then holds only the checkpoint
      sweeper's lock (`.sweep.lock`) through its swap. A serve boot's P0a
      takes neither, so a service started (by systemd, say) while the
      offline restore is between its renames runs P0a on the same in-flight
      intent at the same time. Fix: have P0a take one lock the offline
      commands hold from the held-store check through the swap's last
      rename (the sweep lock, or a dedicated swap lock under the state
      directory), and add a test that starts a boot while an offline
      restore is held between two renames.
