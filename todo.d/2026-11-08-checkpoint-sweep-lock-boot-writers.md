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
      but the boot loses its rollback.
      The pointer race also happens on a plain `prune-checkpoints`, without
      `--include-open`. Prune's `deleteCheckpoint` reads
      `.migration-checkpoint`, sees it names the completed checkpoint it is
      deleting, and then removes the file. If another process's boot writes
      a new pointer between that read and the remove (P4a for its new open
      checkpoint, or P4b's completion), prune removes the boot's new
      pointer. The boot's checkpoint and record survive, but
      `make rollback-restore` no longer finds its rollback.
      Fix: take the lock across P4a and P4b (with a bounded wait, failing
      the boot rather than racing). Add a test that runs prune against a boot
      paused between the record and the pointer, and one that pauses prune
      between its pointer read and remove while a boot writes a new pointer.
      Found by R1-24.
