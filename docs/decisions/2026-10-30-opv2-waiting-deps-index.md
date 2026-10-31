<!-- file: docs/decisions/2026-10-30-opv2-waiting-deps-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: d5712067-374a-4338-a5cd-ec78abe70a11 -->
<!-- last-edited: 2026-10-30 -->

# opv2:wait: waiting-deps index (STARTUP-GC-WAIT-INDEX)

## Decision log

Chosen without asking, per the 2026-10-30 autonomy instruction. The owner can
overturn any of these.

1. **One writer, not a list of writers.** The brief said to find every writer
   that moves an op into or out of waiting_deps. Every opv2:op: row writer
   already stages its index delta through `stageOpRow` (an AST ratchet,
   TestOpsV2RowWritesGoThroughStageOpRow, enforces it), so the wait delta
   (`stageOpWait`) is called from `stageOpRow` and covers insert, status
   updates, resume reset, delete, the opv3 mirror and the hollow sweep with no
   new call sites. Writers that delete a row of unknown state (undecodable
   rows: Insert over one, DeleteOperationV2, SweepHollowOperationsV2) also
   delete `opv2WaitKey(id)` by id, next to their existing `opv2OpenKey(id)`.
   Rejected: a separate staging call at each writer, which would be a second
   ratchet to maintain.

2. **Trust and rebuild share the timeline index's.** The wait family is built
   and repaired in the SAME passes as `ReconcileOpsV2TimelineIndex` (pass 1
   collects waiting ids from the row walk it already does; pass 2 adds one
   key-only walk of `opv2:wait:`; pass 3 fixes under opsMu), and
   `ListWaitingDepsOps` is gated on the same per-boot
   `OpsV2TimelineIndexTrusted` flag. No second full row walk at boot, no new
   server_lifecycle wiring, no second trust flag. Cost: the
   `opsv2_timeline_index_trusted` gauge and the reconcile log line now cover
   both indexes, and a failed reconcile leaves both on the scan. Rejected: a
   separate `ReconcileWaitIndex` (another full row walk at every boot and
   another flag) and a persisted "built" sentinel (unsafe across a rollback to
   a pre-index binary; the reason the timeline index has no sentinel either).

3. **Scan fallback until trusted.** Before the first reconcile completes
   (the registry's `rebuildIndex` can run before it) the read is the old full
   scan, kept as `listWaitingDepsOpsScan`, which is also the oracle for the
   equivalence test. The signature and ordering (op id order) of
   `ListWaitingDepsOps` are unchanged, so no caller changed.

4. **Reader heal is best effort and re-checks under the lock.** A key whose row
   is missing, hollow or has another status is skipped and queued for
   `healStaleWaitKeys`, which takes opsMu, re-reads the live row (the key came
   from a snapshot and the op may have been parked again) and deletes in one
   batch. A heal failure is logged and never fails the read. A row that is
   present but does not decode keeps its key (its status is unknown; the scan
   skips such a row too), matching how the reconcile decodes.

5. **Key family registered.** `opv2:wait:` is in `keyFamilies` (census) and
   in `opv2RawRefusedPrefixes` so SetRaw/DeleteRaw/ApplyRawBatch cannot write
   it, like the open/done families.

## Not done

- The reader still decodes every waiting row each call; there are few of them.
  Caching the decoded rows in the scheduler is a different change.
