### Fixed

- **Test flake: `TestMetadataCandidateFetch_ResumeSkipsCheckpointedBooks` no
  longer fails with "checkpoint owes 0 of 60".** The test cancelled the fetch
  from a progress callback that runs after the recorder releases its lock, so
  under CPU load the cancelling worker could be descheduled while the other 15
  workers fetched every remaining book. Later callbacks now wait until the
  cancel has landed, and the pool is pinned to 8 workers, so the interrupt
  always lands between 30 and 37 of 60 books. Production code was not at fault:
  the checkpoint it wrote correctly owed nothing because every book had
  finished. Measured under CPU stress: 8 failures in 4,000 runs before, 0 in
  4,000 after.
