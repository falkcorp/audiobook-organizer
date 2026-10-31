### Removed

- Operations: the retired op IDs `maintenance.author-dedup-scan` and `maintenance.reconcile-scan` no longer resolve to `dedup.author-scan` and `reconcile.scan`. Their soak showed no use and prod stores no run under either ID. `scheduler.dedup-llm-review` keeps its alias because 20 stored runs still carry it.
