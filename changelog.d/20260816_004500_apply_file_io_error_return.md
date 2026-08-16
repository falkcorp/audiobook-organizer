### Fixed

#### Batch metadata apply no longer reports success when the files never moved

`POST /api/v1/metadata/cached/apply` counted a book as applied whether or not
its audio files were actually renamed. The rename happens inside
`ApplyMetadataFileIO`, which had **no return value** — a failure from the apply
pipeline was swallowed into a `slog.Warn` and was unreachable to all six of its
callers. `applyCachedCandidateForBook` therefore returned `Applied: true`
regardless of what happened on disk, and the batch op's `write_failed` counter
could never be incremented by a rename failure.

`ApplyMetadataFileIO` now returns an `error`, and the outcome the API reports
distinguishes the two cases:

- **`Applied` stays true.** The database change is real and durable, and a
  failed rename does not undo it. Reporting the book as unapplied would send
  someone re-applying work that already succeeded.
- **`WriteBackFailed` is now set** when the file work did not fully land, so the
  batch op counts it separately and logs it naming the book.

A non-nil error means "the file work did not fully land", **not** "nothing
happened": `runApplyPipeline` deliberately persists the `book_file` rows for
every rename that *did* succeed before returning the failure, so a partial
rename is already recorded.

The four callers that run in a background pool or a restart-recovery handler
cannot reach an HTTP response — by the time they run, the request has been
answered — so they log the failure with the book named instead. Tag write-back
still runs after a file-I/O failure exactly as it did before; only the error
that gets reported changed, with the file-I/O error taking precedence because
"rename failed" localises the fault better than the write-back error it tends to
cause.

Follows the target-path builder unification, which is what surfaced this: that
change made the rename correct, and this one makes its failure visible.
