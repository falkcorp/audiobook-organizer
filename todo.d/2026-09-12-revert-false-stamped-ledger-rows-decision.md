- [ ] **REVERT-FALSE-STAMPS (owner decision)** Before #3312, `RevertOperation` marked every change
      row of an operation reverted even when its restore failed or it had no reversal
      (`RevertOperationChanges(op)` mark-all, `internal/audiobooks/revert.go` on main). Any
      production op that was reverted that way has ledger rows with `reverted_at` set but nothing
      restored. #3312 skips already-marked rows one at a time and counts them as "already
      reverted", so those false stamps are never retried, and a false stamp cannot be told apart
      from a real undo in the stored data. Decide whether to audit which reverted ops carry
      failed or record-only rows (for example, cross-check against the revert-time log lines) and
      clear their `reverted_at` so a retry can pick them up, or accept them as lost.
