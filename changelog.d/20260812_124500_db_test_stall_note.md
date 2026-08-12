### Internal

- Recorded an intermittent hang in the `internal/database` short-test suite that fails the
  coverage gate with `panic: test timed out after 25m0s`. Same commit passed on re-run, and
  the package passes locally and on `main`, so it is a stall rather than a regression — but
  the ceiling raised in #2270 (10m → 25m) has now been hit at both heights, so the timeout is
  not the fix. Filed with the evidence and next steps in `todo.d/`.
