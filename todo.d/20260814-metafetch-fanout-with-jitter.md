- [ ] **CORRECTION to `20260814-matcher-writeback-background-job.md`: the
      blocking half of the matcher is the metadata FETCH, not the file
      write.** Owner (2026-08-14): the write side is already backgrounded;
      the fetch side is effectively a singleton and must (1) be dispatched
      as a background operation visible in the ops system, and (2) fan out
      across books — WITH staggered start delays/jitter per request so the
      fan-out doesn't flood the metadata providers all at once.
      Evidence so far: `metafetch.chainMu` is NOT the singleton — it only
      guards cached-chain construction, and the chain is documented safe
      for concurrent worker pools (per-source rate limiter + circuit
      breaker carry their own mutexes). Look instead at (a) the bulk
      dialog's one-book-at-a-time interactive flow, and (b) whatever the
      matcher's search endpoint serializes server-side. Design notes:
      bounded worker pool per the concurrency mandate sized for
      NETWORK-bound work (small fixed concurrency, e.g. 3-4), plus
      per-worker start jitter (e.g. 250-500 ms spread) layered UNDER the
      existing per-source limiters so providers see a ramp, not a burst;
      progress per book surfaces through the op reporter so the UI polls
      the op instead of holding a request open (kills the false sign-out
      symptom at the root).
