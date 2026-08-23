- [ ] **OPS-V2-RESUME-BLIND** `resumeAfterStartup` cannot see any interrupted v2
      operation, so `ResumePolicy` is only consulted after a hard kill. This is
      pre-existing and affects **every** v2 op, not just maintenance.

      Mechanism, measured 2026-08-23:
      `Registry.resumeAfterStartup` (`internal/operations/registry/resume.go:34`)
      takes its candidate rows from `store.ListActiveOperationsV2()`, which scans
      the `opv2:act:` index (`internal/database/pebble_store_ops_v2.go:361`) and is
      documented as exactly the `queued|running` set. `UpdateOperationV2Status`
      **deletes** that index key for any status that is not `running` or `queued`
      (`pebble_store_ops_v2.go:277`) — deliberately, so a terminal row leaves the
      active set and stops poisoning `EnqueueOp`'s ConcurrencyKey dedupe.

      Every shutdown path writes such a status. A clean drain cancels the run and
      it finishes `canceled`; the shutdown-timeout branch writes
      `interrupted_quiesced` (`registry.go:1075`); worker abandonment writes the
      same (`worker.go:370`, whose comment says outright that the point is to make
      "the row leave the opv2 active index"). All three delete the key, so the next
      startup's sweep sees nothing. Only a SIGKILL — where no shutdown path runs
      and the row is left `running` — leaves a row the sweep can act on.

      There is **no** `ListInterruptedOperationsV2`: the only v2 listings are
      `ListQueuedOperationsV2`, `ListActiveOperationsV2` and
      `ListOperationsV2Since`.

      This is the exact v2 twin of a v1 bug already fixed. See the comment on
      `isResumableOpStatus` (`internal/database/pebble_store_operations.go:461`),
      which matches the `interrupted` **prefix** precisely so the v1 sweep stops
      being "blind to exactly the rows it exists to resume — a library.scan killed
      by a deploy on 2026-08-17 sat at interrupted_quiesced and never came back."
      The v1 sweep scans rows by status and so could be fixed that way; the v2
      sweep reads an index, so it needs a listing that returns interrupted rows.

      Why this surfaced now: the v1 sweep had been masking it for maintenance jobs.
      PR #2784 retired the v1 op minter and deleted that branch, and PR #2788 then
      corrected six jobs' declared policies — but a correct policy is still only
      consulted on the hard-kill path. Do not fix this inside a maintenance PR: 19
      ops declare `ResumeRequeue` (dedup, acoustid, itunes among them), so making
      the sweep see interrupted rows changes startup behaviour for all of them on a
      path that has never been exercised. Needs its own change, its own tests, and
      a decision about whether a `canceled` op should be resumable at all.
