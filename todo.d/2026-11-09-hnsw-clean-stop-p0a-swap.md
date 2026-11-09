- [ ] **HNSW clean-stop marker: a boot whose P0a swaps the store must not trust the marker it
      took** (from the R1-15 clean-stop PR's port onto R1-23; filed, not built, because nothing
      stages a swap yet).
      - **Why.** Since R1-23 the served boot runs core's pre-boot swap (P0a,
        `core/kernel/swap.go`) whenever a restore or reset is staged in the state directory. The
        swap replaces the database directory and its sidecars and leaves `hnsw/` beside it alone.
        The served boot takes the clean-stop marker in `books.Options`
        (`database.ClaimHNSWCleanStop`), before `app.New` runs P0a, and `Register` hands the claim
        to the store (`AdoptHNSWCleanStopClaim`). On a boot that swaps, the claim's marker
        describes the rows from before the swap, and the snapshot import would trust it for the
        rows after it whenever the counts match. The marker is already gone from disk; only the
        record the claim holds in memory is wrong.
      - **When it matters.** Once something stages a swap: `restoreswap.Stage` has only test
        callers today, and R1-25's reset stager is the first planned. Build this before or with
        it.
      - **Fix.** In `books.Options`, before the claim, treat a staged or in-flight swap as "this
        boot swaps": `restorejournal.PendingPath(stateDir)` exists, or
        `restorejournal.InFlightIDs(vfs.Default, stateDir)` is not empty (fail closed on a probe
        error). Still take the marker, but void the record, the same shape as the migration void
        (`adoptCleanStop`'s `migrated`). Check first that `internal/books` may import
        `core/storage/restorejournal` (archtest). The other way is for the kernel to tell modules
        what P0a did (`restoreswap.Outcome.Boot`), which needs a core API.
      - **Standalone.** `OpenStandalone` has no P0a yet; when R1-25 adds one, its take must come
        before the swap as well, or be voided the same way.
      - **Test.** Stage a restore with `restorejournal.CreateIntent` into the boot's state
        directory, write a vouching marker, boot books, and require the import to reconcile.
