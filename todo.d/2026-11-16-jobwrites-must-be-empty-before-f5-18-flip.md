- [ ] **JOBWRITES-F5-18** Maintenance job write sets: must be empty before the
      F5-18 flip (the native job kinds are unregistered until then, so none of
      this changes behaviour today). Two oracle gaps in
      `internal/maintenance/jobs/writes_oracle_test.go` and
      `writes_trace_test.go`, and three F5-18 preparation items.
      - Rewrite the SSA walk on `golang.org/x/tools/go/callgraph/vta` (or
        `cha`) over the same program. It follows an interface call into its
        implementations, which removes the hand alias for
        `tagger.LibraryImporter.ImportPath`, and it follows functions passed as
        values and calls that leave the module. Today a store write reached
        through a non-store name (for example `dedup.dedupBookWriter.RevertBookToVersion`)
        is dropped silently, because only names that start with a write verb or
        have a table row are classified.
      - Key `traceInert` by `"<receiver>.<method>"`, as `traceAliases` already is.
        It matches a bare method name on any receiver, so a future write named
        `Move` or `Locked` on another type would be ignored.
      - Make write-set deferral visible before the flip: surface the deferral
        reason on the run and alert when a scheduled kind has waited longer than a
        set number of hours. At the flip the four scheduled kinds that can be held
        back by a manual whole-library job are `books.maintenance.tombstone-cleanup`
        (overlaps 20 jobs, was 2), `books.maintenance.author-split-scan`,
        `books.maintenance.purge-deleted` and `books.maintenance.trash-cleanup`.
      - Retire the legacy author-junction rows (`author_books_legacy:`) the store
        stages inside `ModifyBook`, so `authors` leaves the book row and
        `tombstone-cleanup` goes back to 2 overlapping jobs.
      - Convert the long whole-library jobs (`backfill-file-hashes`,
        `recompute-book-aggregates`, `scan-composer-tags`) to per-batch book
        locking (a per-book resource ID through `RunItems`) instead of a
        whole-kind write set, so a scheduled kind waits for one batch and not for
        a job's 4h timeout.
