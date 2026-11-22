### Fixed

- Every blocking step between the process start and the ready signal now asks systemd for more start time
  right before it runs, so a long boot step (a migration, a large ops history, the search index import, the
  store compaction) is no longer killed at the 300 s start timeout, and a wedged step is no longer kept alive.
  The request comes from the code that runs the step (kernel phases, hooks, startup rows, service
  container), never from a timer. Loops that run on worker goroutines report progress and the boot thread
  extends once per unit.
- The M8 store rewrite's compaction no longer counts background compactions as progress: it compacts in
  sub-ranges and extends before each one, so a hung manual compaction stops extending.
