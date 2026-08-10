<!-- file: todo.d/20260810-search-index-queue-drops-silently.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e51d7a3-2c48-4b16-8f70-a3d19c6528b4 -->
<!-- last-edited: 2026-08-10 -->

- [ ] **🔴 The search index silently drops updates when its queue fills — 56,537 dropped
      in seven days.** Measured on prod 2026-08-10 from `journalctl`. This is a
      **blocking prerequisite** for pushing filters/sort into Bleve (design doc option
      A1), and it changes the ordering of that plan.

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
