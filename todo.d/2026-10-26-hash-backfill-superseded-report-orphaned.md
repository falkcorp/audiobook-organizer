- [ ] **TODO-HASH-BACKFILL-SUPERSEDED-REPORT-ORPHANED** The boot resume sweep's
      `supersedeStaleQuiesced` (internal/operations/registry/resume.go) drops an
      `interrupted_quiesced` row when a newer queued or running row of the same
      def exists, or a newer quiesced one. For `backfill-file-hashes`, which now
      pauses and continues under one operation id, that row may be a half-finished
      operation whose per-run report
      (`{root}/.reports/backfill-file-hashes-<opID>.tsv`) is the ONLY record of its
      progress. Superseding it orphans that report and the newer operation starts
      from row zero, redoing hours of hashing (in a dry run nothing is stored, so
      nothing else remembers it). Decide whether such a row should be resumed
      rather than superseded (the job is one-run-at-a-time by ConcurrencyKey, so
      two never run together), or whether the newer run should adopt the older
      report. Found in review of the in-place continue; pre-existing, not changed
      there.
