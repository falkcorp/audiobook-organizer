- Window backfill now reports **why** files were deferred on every heartbeat, not
  only in the run's closing message. A remote-only pass over the full library is a
  multi-hour job, and the number that says which files can *never* succeed
  (`not_under_libroot`, which no worker may be offered and which every re-run
  defers again) previously arrived only once the run had finished — long after the
  point where it would have changed what the operator ran. A permanent cause and a
  retryable one were indistinguishable in the bare total.
