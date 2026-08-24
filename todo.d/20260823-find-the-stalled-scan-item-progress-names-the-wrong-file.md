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

      **Measured evidence** (all 9 prod `library.scan` rows, 2026-08-21..23,
      pulled by a parallel session): the numerator pins at **14912** (7 rows) or
      **14916** (2 rows) while the denominator drifts 40109 → 40089. The named
      item **varies across at least three titles** at the same pinned count —
      `Past Life Hero Book 3.m4b` (5×), `Noelle Stevenson - Nimona.mp3` (1×),
      `Orson Scott Card ... Shadow of the Hegemon` (1×). A varying name at a
      fixed count is exactly what the defer above predicts.

      Status is `interrupted_quiesced` on 8 rows and `canceled` on 1 — **not**
      `abandoned`. `resume_count` is 0 on every row, and `current_phase` /
      `current_item` are `None` on every row, so the prose in `progress_message`
      is currently the only phase signal there is.

      Two rows are a different shape and must not be folded in: `01M0KQ1J` at
      49280/49280 `"Reading tags"` (different phase, different denominator) and
      `01M0QCBV` at 3/6 `"AI parsing batch 3/6"` (the 6-item scan, not the walk).

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
