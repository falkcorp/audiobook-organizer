- [ ] **ABS series list returns hollow entries, and the app renders no series at all.**
      Measured 2026-08-16 against production, using the client's exact query
      (`?page=0&limit=50&sort=name`, the params AudioBooth actually sends):
      - 50 results returned, `total=15528` — the server side is healthy and
        pagination works.
      - **27 of 50** have an empty `books: []`.
      - **9 of those 27 are self-contradictory**: `numBooks >= 1` with
        `books: []` and `totalDuration: 0` (e.g. "Salem's Lot (read by Ron
        McLarty)" reports `numBooks=1`). The remaining 18 report `numBooks=0`.

      **Unexplained, and deliberately not assumed:** 23 of the 50 entries are
      well-formed and have books, yet the app's Series tab shows "No Series
      Found". The hollow entries are a real bug worth fixing, but they do NOT
      by themselves explain an empty render — a client that skipped bad entries
      would still show 23. Do not let the hollow-series finding stand in as the
      cause of the empty screen without evidence; the likely-but-unverified
      hypothesis is that the client aborts parsing the whole list on the first
      malformed entry rather than skipping it.

      Native API is healthy, so the defect is in the ABS compat layer.
