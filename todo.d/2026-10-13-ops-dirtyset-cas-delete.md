- [ ] **OPS-DIRTYSET-CAS** 02 PR 14: add the store primitive the v3 Batch
      runner's `ops.DirtySet` reader needs, `CompareAndDeleteRaw(key, expect)`
      on `*PebbleStore`, together with the marker that writes
      `<prefix><subjectID>` -> 8-byte big-endian epoch under the same lock.
      05-PR5 shipped the reader side (`internal/operations/registry`
      `dirtySetStore`); a run over a DirtySet refuses to start until the store
      has it.
