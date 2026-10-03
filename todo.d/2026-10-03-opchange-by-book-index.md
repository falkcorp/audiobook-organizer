- [ ] **OPCHANGE-BY-BOOK** Add a by-book index for operation change rows so
      `GetBookChanges` stops scanning the whole journal. Today every call reads
      and decodes every `opchange:` row (`internal/database/pebble_store_operations.go`,
      `GetBookChanges`) and keeps the ones whose `BookID` matches. Measured
      2026-10-03 on an in-memory store with 300,000 journal rows: 331 ms per
      call. Callers that run once per book multiply that: the fragment fixer's
      resume used to make one call per retired book (300 books → ~99 s under the
      merge lock), and now reads the journal once per re-plan
      (`PebbleStore.ScanOperationChanges`, 391 ms for the same case). Still
      per-book: `retireInto`'s `resumeHandOff` / `owedHandOff`
      (`internal/plugins/maintenance/retire_into.go`) for every already-retired
      book an apply meets again, and `emptiedRowOn`. The prod journal is larger
      than 300k rows. Fix: write an `opchange_by_book:<book>:<op>:<id>` key
      beside each row (and on revert marks), backfill it once, and serve
      `GetBookChanges` from that prefix.
