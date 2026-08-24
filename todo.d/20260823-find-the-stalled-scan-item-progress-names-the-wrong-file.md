- [ ] **SCAN-STALL-ITEM** Find what wedges `library.scan` at ~14912 — and fix the
      progress reporting that currently names the wrong file.

      **The reported filename is a completed book, not the stuck one.** In
      `ProcessBooksParallel` the progress send is inside a *deferred* closure:

      ```go
      go func(idx int) {
          defer wg.Done()
          semaphore <- struct{}{}          // acquire
          defer func() {
              <-semaphore                  // release
              progressCh <- books[idx].FilePath   // scanner.go:844
          }()
      ```

      That defer runs when the worker's body *returns*, so a book only names
      itself once it has finished. The wedged worker never returns and therefore
      never sends. Whatever appears in `"Processed: N/M books (X)"` is simply the
      last of the other ~9 workers to complete. `Past Life Hero Book 3.m4b` is a
      book that scanned fine.

      **Measured evidence — re-pulled 2026-08-24 over a 7-day window.** An
      earlier version of this entry used a 9-row population; the correct one is
      **21 rows**. The instrument was the problem, see below.

      The stall count is **not** a single value. It steps *down* over the week
      while the denominator grows:

      | pin | rows | window | denominator |
      |---|---|---|---|
      | **16416** | 7 | Aug 18 08:07 – Aug 20 04:17 | 40084 → 40088 |
      | **14916** | 3 | Aug 20 10:17 – Aug 21 21:08 | 40088 → 40089 |
      | **14912** | 4 | Aug 22 09:08 – Aug 23 20:48 | 40090 → 40109 |

      That shape is important and it argues against a single poisoned file. A
      fixed bad input at sorted position N would hold N, or drift *up* as books
      sort in ahead of it. This drifts **down** — it stalls progressively
      earlier while the library grows.

      **Not one `library.scan` in 7 days reached completion.** 20 of 21 rows end
      `interrupted_quiesced`, 1 ends `canceled`. There is no `completed` row in
      the window at all.

      The named item varies across at least five titles at these pinned counts —
      `Imagining Elsewhere.m4b` (5×), `Past Life Hero Book 3.m4b` (5×),
      `Ryan DeBruyn - Endarkened Spire` (2×), `Noelle Stevenson - Nimona.mp3`,
      `Orson Scott Card ... Shadow of the Hegemon` — exactly as the defer above
      predicts.

      **The instrument lied, and it is worth knowing how.**
      `GET /api/v1/operations/timeline` reads **only** `since` (default **15m**).
      `def_id` and `limit` are not parameters; Gin drops unknown query keys
      silently. Verified with a bogus value on 2026-08-24 rather than by reading
      the handler:

      ```
      since=168h                        -> 148 rows
      since=168h&def_id=library.scan    -> 148 rows
      since=168h&def_id=TOTAL_NONSENSE  -> 148 rows   <-- inert
      since=168h&limit=5                -> 148 rows   <-- inert
      (no params)                       ->   1 row    <-- the 15m default
      ```

      So a query written as `?def_id=library.scan&limit=200` silently asks for
      *the last 15 minutes of everything*. See
      [[20260824-operations-timeline-ignores-def-id-and-limit]]. Also note the
      payload nests two deep — `{"data":{"operations":[…]}}` — so a parser
      reading top-level `operations` gets 1.

      Re-pull with `since=168h` and filter client-side. 148 < the 200 row cap,
      so that window is a real count; `since=240h` and `336h` both hit the cap
      and truncate the **old** end, leaving anything before Aug 17 unmeasured.

      Rows in other phases must not be folded in with the `"Processed:"` rows.
      Four are `"Reading tags"` at N/N with wildly different denominators —
      49280, 132260, 22400, 61380 — because that phase counts *files* with a
      growing denominator, so N/N means "still discovering", not "finished".
      Two more are `"AI parsing batch"` (3/6 and 1/18), a different op shape
      entirely.

      **What to do, in order:**
      1. Report the item being *started*, not only the one completed. Sending the
         path on acquire (or keeping an in-flight set on the handle) makes the
         stuck item name itself. Without this, no run can identify it — this is
         the blocker, not a nice-to-have.
      2. Populate `current_phase` / `current_item` on the op row. They are `None`
         on all 9 rows, so the phase has to be guessed from prose today.
      3. Only then chase the file. The candidate set is the ~10 books in flight
         around sorted index 14912, inside the 500-book chunk containing it.

      **Do not assume #2830 fixed this.** The 120s `ProcessFileWithTimeout` bound
      converts a single wedged *file* into a normal scan failure. It does nothing
      if the stall is a pool/semaphore/deadlock bound rather than one poisoned
      input — and a count pinned to two adjacent values (14912/14916) across
      three days is at least as consistent with the latter. Confirm which before
      closing.
