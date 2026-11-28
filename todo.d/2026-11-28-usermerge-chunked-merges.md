- [ ] **MERGE-CHUNK** Make identity user merges chunked instead of one capped batch.
      `usermerge.MergeBatchCap` (internal/usermerge/identity.go) was raised from 64 MiB
      to 256 MiB on 2026-11-27 because the F4-08 prod-copy rehearsal (cell 6) measured a
      real account at 128,114,754 bytes; a bigger account still answers 409
      `merge_too_large`. The proper fix stages a merge (and its undo) in bounded chunks
      under the merge journal, keeping the "nothing partial is visible" guarantee the
      one-batch design gives today, and retires the cap. Covers apply, preview and undo
      (undo_identity.go reads the same cap).
