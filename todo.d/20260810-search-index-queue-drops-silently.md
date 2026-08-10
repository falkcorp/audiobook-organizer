<!-- file: todo.d/20260810-search-index-queue-drops-silently.md -->
<!-- version: 2.0.0 -->
<!-- guid: 9e51d7a3-2c48-4b16-8f70-a3d19c6528b4 -->
<!-- last-edited: 2026-08-09 -->

- [x] **🔴 The search index silently drops updates when its queue fills — 56,537 dropped
      in seven days.** Measured on prod 2026-08-10 from `journalctl`. This was a
      **blocking prerequisite** for pushing filters/sort into Bleve (design doc option
      A1), and it changed the ordering of that plan.

      **✅ FIXED — reconciliation shipped.** Owner chose a dirty-set drained on a ticker,
      persisted to Pebble, with an adaptive batch size. Steps 1, 2 and 4 below are done;
      step 3 (filter/sort pushdown) is now unblocked.

      Implementation: `internal/database/pebble_store_search_dirty.go` (durable set,
      `idx:sidx:dirty:{id}`, mirroring the existing `idx:upl:dirty:` playlist idiom) and
      `internal/server/search_reconciler.go` (ticker + adaptive drain).

      ## 🔑 The root cause was a false comment, not just a missing feature

      Three separate comments — `indexed_store.go:14`, `indexed_store.go:100` and
      `server.go:225` — asserted that "a startup reindex will heal any gaps". **It does
      not.** `buildSearchIndexIfEmpty` opens with `if count > 0 { return }`, so it runs
      only when the index has ZERO documents. On a populated library it has never run.

      The drop was therefore designed as safe *under a guarantee that was never true*.
      That is why all three comments were corrected in place, with the old claim quoted
      and refuted, rather than quietly rewritten: the next person to read the old
      reasoning must not re-derive the same wrong conclusion.

      ## Two things the implementation measured rather than assumed

      1. **`pebble.Sync` on the mark was a latency bug.** The first version synced every
         mark; a test writing 2,500 IDs took **13.9s** (~180/sec). Drops arrive in bursts
         on the write path while `enqueueIndex` holds `indexQueueMu.RLock`, so that would
         have added ~5ms to every write during exactly the overload the drop relieves.
         Switched to `NoSync` (still WAL-backed, survives process crash): the same test
         now takes **0.13s** — 107× faster.
      2. **A 1%-per-tick adaptive drain was too slow to matter.** At 1%, a 56,537 backlog
         drains ~565/tick — indistinguishable from the fixed 500 floor, ~50 minutes total,
         and it decays so the tail is slowest. Implemented at 10% clamped to
         [500, 5000]: the same backlog clears in ~11 ticks (~5.5 min).

      ## The measurement

      ```
      level=WARN msg="search index queue full, dropped (delete)" bookID=01KXXVGZ90PS78ZWJZJY62EFCJ del=false
      ```

      | window | dropped index operations |
      |---|---|
      | last 7 days | **56,537** |
      | days affected | Aug 03 and Aug 07 only |
      | since the Aug 09 10:33 restart | 0 |

      **The zero is not reassuring.** The queue is empty because the process restarted and
      no bulk operation has run since. Both affected days were bulk-operation days; the
      next scan, merge wave or dedup run refills it and drops again.

      ## The mechanism

      `internal/server/indexed_store.go:113-122` — a non-blocking send onto a 1024-deep
      channel, with `default:` as the overflow branch:

      ```go
      select {
      case s.indexQueue <- indexRequest{bookID: bookID, delete: del}:
      default:
          atomic.AddInt32(&s.indexWorkerBusy, -1)
          slog.Warn("search index queue full, dropped (delete)", "bookID", bookID, "del", del)
      }
      ```

      Dropping under pressure is a defensible choice — the alternative is blocking a write
      path on the indexer. **What is not defensible is that nothing reconciles afterwards.**
      A dropped update is lost permanently; there is no retry, no dirty-set, and no
      periodic re-sync. The index diverges from the database and stays diverged until
      something happens to rewrite that book.

      Note the log message says `(delete)` while `del=false` — the label is wrong for the
      upsert case, which makes the warning harder to interpret than it needs to be.

      ## Why this blocks A1

      Today a dropped update means **stale relevance** — a book ranks oddly or misses a
      match. Bad, tolerable, invisible.

      After A1 pushes filters and sort into the Bleve query, a dropped update means
      **wrong rows**. A book whose `library_state` changed to `organized` but whose index
      entry still says `imported` will be *absent from the Organized filter and present in
      Imported*. The user sees a library that is missing books, with no error.

      **This is the difference between an index that is a relevance dependency and one that
      is a correctness dependency** — exactly the risk flagged as open item 3 in
      `docs/design/2026-08-09-search-backend-options.md`, now with a measured failure rate
      attached.

      ## What to do, in order

      1. **Make the drop visible.** A counter and a metric, not just a WARN that scrolls
         past 56,537 times. Right now the only way to know is to grep journald.
      2. **Reconcile.** Any of: a dirty-set of book IDs that failed to enqueue, drained on
         a ticker; a periodic full re-index; or a generation counter per book compared
         against the index on read. A dirty-set is the cheapest and matches the existing
         "cached aggregates + dirty flag" idiom in this codebase.
      3. **Then and only then**, push filters/sort into the index.
      4. Fix the `(delete)` label while touching this.

      **Do not size the queue bigger and call it fixed.** 1024 → 100,000 moves the
      threshold; it does not add reconciliation. The bulk days dropped 56K operations,
      which no reasonable buffer absorbs.

      ## Also settles an open question

      Open item 4 of the design doc asked whether the index is complete. **It is not**, and
      now there is a mechanism and a number rather than a suspicion. The `.api-token` is
      still stale, but this answer did not need it.
