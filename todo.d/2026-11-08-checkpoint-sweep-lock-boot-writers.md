- [ ] **PLATFORM-CORE-R1-24-BOOT-WRITERS-SWEEP-LOCK** The boot's checkpoint
      writers (P4a's `takeCheckpoint`, its garbage sweep, P4b's
      `completeCheckpoint`) do not take the sweep lock
      (`.mediacore-checkpoints/.sweep.lock`) that the hourly sweeper and
      `storage prune-checkpoints` share. A `prune-checkpoints --include-open
      --confirm` run while another process is booting can therefore delete
      the open checkpoint the boot just recorded, or race its
      `.migration-checkpoint` pointer write. `PruneCheckpoints` deletes only
      records (never garbage or scratch), so it cannot remove a copy whose
      record does not exist yet, and the next sweep drops a dangling pointer,
      but the boot loses its rollback. Fix: take the lock across P4a and
      P4b (with a bounded wait, failing the boot rather than racing), and add
      a test that runs prune against a boot paused between the record and
      the pointer. Found by R1-24.
