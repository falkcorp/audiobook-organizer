### Fixed

#### Activity log writes no longer crash the server when the database closes mid-flush

The Pebble activity store borrows the main database handle, so closing the
activity service never closed anything, and the main store could close while a
deferred activity flush was still committing. Pebble panicked with
`pebble: closed` and took the process down (seen in CI on #3416 and #3418).
Every PebbleActivityStore read and write now recovers that panic and returns an
error wrapping `pebble.ErrClosed`; the deferred flush logs how many entries
were lost. `TestNewServer_DoesNotWriteActivitySynchronously` now drains the
activity service before closing its database.
