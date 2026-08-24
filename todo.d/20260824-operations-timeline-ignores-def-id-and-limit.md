- [ ] **TIMELINE-FILTER-INERT** `GET /api/v1/operations/timeline` silently
      ignores `def_id` and `limit`. Either honour them or reject unknown query
      keys — the current behaviour returns a plausible wrong answer.

      The handler (`internal/server/handlers/operations_v2.go:145-159`) reads
      **only** `since`, defaulting to **15m**, and passes a hardcoded 200 row
      cap. `def_id` and `limit` are not parameters at all; Gin drops unknown
      query keys without complaint.

      **Measured with a bogus value, 2026-08-24** — a nonsense `def_id` returns
      the identical row set, which is what makes it inert rather than merely
      broken:

      ```
      since=168h                        -> 148 rows
      since=168h&def_id=library.scan    -> 148 rows
      since=168h&def_id=TOTAL_NONSENSE  -> 148 rows
      since=168h&limit=5                -> 148 rows
      (no params)                       ->   1 row     <- the 15m default
      ```

      **Why this is worth fixing rather than documenting.** A query written the
      natural way — `?def_id=X&limit=200` — reads as "200 rows of op X" and
      actually asks for the last quarter hour of everything. On a quiet system
      that returns one unrelated row, which looks exactly like *"this op has
      never run."* It has already produced three wrong conclusions in two days:

      1. A `library.scan` population recorded as 9 rows when the real 7-day
         count is **21**, with a stall pin that turned out to move (16416 →
         14916 → 14912) rather than hold — see
         [[20260823-find-the-stalled-scan-item-progress-names-the-wrong-file]].
      2. A `maintenance.window` failure count recorded as 3 nights when it was
         **7 for 7**, in a document that shipped with the undercount.
      3. A wrong mechanism diagnosis (a "broken `def_id` filter") that was
         briefly confirmed off a second, unrelated parser bug.

      **Two further traps for whoever fixes this.** The payload nests two deep,
      `{"data":{"operations":[…]}}`, so a parser reading top-level `operations`
      with a `len()` fallback returns 1. And the 200 cap truncates the **old**
      end: `since=240h` and `336h` both return the same 8 rows, so a window that
      hits the cap cannot support any "it never happened before X" claim.

      Rejecting unknown query keys with a 400 is arguably the better fix than
      implementing the filters, since it converts every existing wrong query
      into a loud failure instead of a plausible one. Related:
      [[feedback_operations_timeline_hardcodes_limit_200]],
      [[feedback_verify_the_instrument_with_a_bogus_value]].
