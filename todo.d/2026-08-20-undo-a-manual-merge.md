- [ ] **MERGE-UNDO** Make a review-initiated merge reversible. The machinery is
      half-built and entirely unwired: `Engine.UnmergeAuto`
      (`internal/dedup/auto_resolve.go:450`) reverts both books to their
      pre-merge `book_ver` snapshots and has **no production caller at all** —
      it is reachable only from tests. Three gaps stand between that and a
      working undo, and none of them is the hard part of the other two:
      - Only the auto-resolve path journals. `PutAutoMergeJournalEntry` is
        called from `auto_resolve.go` alone, so a merge dispatched from the
        review lane records no pre-merge snapshot timestamps and there is
        nothing for `UnmergeAuto` to revert *to*.
      - `UnmergeAuto` declares its own scope limit: it restores the BOOK RECORD
        only. It does not reverse the external-ID reassignment (loser→winner)
        that `MergeBooks` performed, nor the enqueued iTunes write-back
        removals. Its comment names the missing follow-on explicitly.
      - No endpoint or op exposes it, so there is no way to invoke it.
      Deferred deliberately on 2026-08-20 when the dupes lane was made faster to
      triage: the user chose to ship throughput first and treat undo as its own
      task, since it is backend work with a correctness surface (external-ID
      restoration) that does not belong inside a keyboard-shortcut change. The
      speedup did not make merges less reversible — they were never reversible
      from that screen — but it does raise the rate at which they happen, which
      is the reason this is written down rather than left implicit.
