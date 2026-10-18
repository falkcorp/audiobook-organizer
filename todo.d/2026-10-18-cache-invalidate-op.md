- [ ] **CACHE-INVALIDATE-OP** There is no way to flush the in-memory caches
      without a process restart. `POST /api/v1/cache/invalidate` was retired in
      01-P81g (it answers 410 Gone for one release) and nothing replaces it. A
      restart costs about ten minutes of unusable library while the in-memory
      store warms back up. Generation keying on the library-list caches does not
      cover batch writes, direct memory-store writes or index-only edits, so
      stale entries are still possible. If the owner wants the lever back, add a
      registered `maintenance.*` op that empties one named cache or all of them
      and reports what was dropped per cache. Needs a preview mode and an
      admin-only permission.
