### Concurrency

- [ ] **`internal/scanner/scanner.go` spawns one goroutine per directory and per
      book before any of them can be admitted — the semaphore bounds the work,
      not the fan-out.** Both loops have this shape:

  ```go
  semaphore := make(chan struct{}, workers)
  for _, dir := range dirs {           // and again: for idx := range books
      wg.Go(func() {
          semaphore <- struct{}{}      // ACQUIRE INSIDE the goroutine
          defer func() { <-semaphore }()
          ...
      })
  }
  ```

  `scanner.go:965` (per directory) and `scanner.go:1166` (per book). Every
  iteration creates a goroutine immediately; each then blocks on the channel.
  So `workers` caps how many run at once but nothing caps how many *exist*.

  This is the exact shape `CLAUDE.md`'s concurrency rule names: *"Never fan out
  unbounded goroutines over an unbounded collection."* The library is ~61,000
  books, and a goroutine's minimum stack is 8 KB, so a full scan can park on the
  order of hundreds of MB in goroutines that are doing nothing but waiting for a
  slot — plus the scheduler cost of tracking them all.

  **Fix — acquire before spawning**, so the loop itself blocks:

  ```go
  for _, dir := range dirs {
      semaphore <- struct{}{}          // acquire BEFORE the spawn
      wg.Go(func() {
          defer func() { <-semaphore }()
          ...
      })
  }
  ```

  or replace the pair with `errgroup.Group` + `SetLimit(workers)`, which has
  this behaviour built in and is what the standards now recommend.

  **Care required at `scanner.go:1166`** — its deferred release also does
  `progressCh <- books[idx].FilePath`, and `close(progressCh)` happens after
  `wg.Wait()`. Moving the acquire must not change that ordering, or the send
  races the close and panics. Verify, don't assume.

  Found while converting `Add(1)`/`Done()` pairs to `wg.Go` (PR #2991). The
  conversion neither introduced nor changed this — it is pre-existing, and the
  `wg.Go` above is already the converted form.
