### Fixed

#### `EmbeddingStore.Close()` could deadlock against in-flight operations

Closing an `EmbeddingStore` that owns its PebbleDB while operations were still running
could hang forever: writers already inside `db.Set` blocked on Pebble's commit-pipeline
mutex and never woke. Pebble documents that calling `Close` concurrently with any other DB
method is unsafe; the store now provides the guarantee Pebble does not.

Every operation that touches the database holds a read lock for its whole duration and
`Close` takes the write lock, so `Close` cannot begin until the last in-flight operation
has returned.

This was reached only by tests — the production constructor `NewEmbeddingStore` sets
`owned: false`, so `Close()` returns immediately and never shuts the DB down. Its cost was
paid in CI, where `TestChaos_MixedReadWriteDuringClose` hung for the full job timeout and
reported only an unexplained "cancelled".

The pre-existing `closed` flag could not have prevented this and was never going to: it is
a check-then-act, and an operation already inside `db.Set` is past the check.

Also bounded the three chaos tests' worker waits. A deadlock is not a panic, so the
`recover()` in each of them could never catch this, and a bare `wg.Wait()` turned a
regression into a silent 30-minute hang with no test named. A regression now fails in 30
seconds and says which invariant broke.
