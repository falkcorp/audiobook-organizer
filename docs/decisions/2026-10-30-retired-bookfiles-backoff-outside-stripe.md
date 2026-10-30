<!-- file: docs/decisions/2026-10-30-retired-bookfiles-backoff-outside-stripe.md -->
<!-- version: 1.0.0 -->
<!-- guid: 073c641c-f5ab-4b49-a922-ee39058a0a3b -->
<!-- last-edited: 2026-10-30 -->

# Retired-row writers release the version stripe across the restage backoff

Closes TODO-BF-CAS-RETIRE-BACKOFF-UNDER-LOCK.

## Decision log

Chosen: `restageBookFileBatch` gained a sibling, `restageBookFileBatchGuarded`
(`internal/database/bookfile_cas.go`), that takes a per-round `guard`. The guard
runs after the backoff sleep and returns the round's release func. The three
retired-row writers go through one shared helper,
`PebbleStore.restageUnderVersionLock` (`internal/database/retired_book_files.go`),
whose guard takes `lockBookVersion` and then runs a `check` closure under it. A
guard error is a refusal that ends the call (it is never retried, even if it
wraps `errBookFileRowVanished`).

What the per-round `check` re-reads, because the stripe is no longer held across
the gap:

- RetireBookFiles: `requireRetiredVersionOf` and `requireHolderNotSoftDeleted`
  (previously run once before the loop).
- RehomeRetiredVersion / RestoreRetiredVersionHolder: the `checkDest` closure.
- UnretireBookFiles: nothing version-level was checked before, so no check.

The rows themselves need no new re-read path. Retire and unretire read them
through the row cache and the commit compares the bytes
(`bookFileUnchanged` in `commitBookFileBatchVerified`, plus the `indexCAS`
note/verify hook for single-owner index keys); the rehome attempt already
re-reads its rows every round.

Rejected:

- Re-checking only the rows and leaving the version-level preconditions as a
  once-only check: wrong once the stripe is released, because a DeleteBookVersion,
  UpdateBookVersion or a competing rehome can change the version or its holder in
  the gap, and the commit's byte compare covers rows, not the version row.
- Sleeping inside `restageBookFileBatch` with an unlock/relock callback: the
  sleep is a package variable tests replace, and a callback threaded through the
  existing signature would change every other caller (moves, deletes, sweeps)
  for no benefit.
- Releasing the stripe only for the sleep but skipping `check` on re-take
  (smaller diff): it would let a retire write rows under a version that had been
  rehomed during the sleep.

Behavior change worth knowing: a call that previously saw the version stripe held
continuously now sees it taken once per staging round. Lock order is unchanged
(version stripe outermost).

Touched `pebble_store_book_lock.go` only for the `lockBookVersion` doc comment;
`pebble_store_bookfiles.go` was not touched.
