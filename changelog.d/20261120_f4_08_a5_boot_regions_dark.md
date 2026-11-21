### Added

- core: the identity boot's store commits carry crash-matrix labels (F4-08 A5, dark). The P4d step 2 re-apply (tail and drained chunks, the pending head, the restore line, the unapplied queue and its dequeue, the kept admins' restriction, the withheld head, `MarkUnappliedClosed`, `served_boot`), the restore hooks' run ID, `auth.BootWrites`' four commits and `rbac.CommitSeeds` each run inside a `bootseam` region. A region is a no-op in production. The writer table lists them as rows marked Unbound until F4-08 wires the boot.
