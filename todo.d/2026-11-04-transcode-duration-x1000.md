- [ ] **TRANSCODE-DURATION-X1000** `internal/transcode/transcode.go` (~line 255) computes the progress
      total as `int64(f.Duration) * 1_000` under a comment saying book files store milliseconds. They store
      seconds, so the "microseconds" total is 1000x too small and transcode progress runs past 100% early.
      Multiply by `1_000_000` (the `book.Duration` fallback just below already does), fix the comment, and
      add a test that a book of known seconds yields the right total.
