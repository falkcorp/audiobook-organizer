### Fixed

- **`itunes.regroup` check-failed follow-ups (review of #3757).**
  - The end-of-run error now keys on the groups skipped for a failed
    owner-manual check, not on every live book whose check failed. The
    snapshot checks the whole library, so one bad credit row on a book in no
    heal group used to fail every apply although nothing was withheld.
    The Warn log still gives the book count.
  - The dry run and the apply now end with the same status for the same plan:
    a dry run that would skip a group for a failed check returns the error
    too, where it used to return success.
  - The snapshot's owner-manual file index is size-hinted with the book count.
  - Correction to the #3757 entry: "15 MB instead of 751 MB" is cumulative
    allocation per build (`-benchmem` B/op), not resident memory. With the
    size hint the 300k-row benchmark now allocates about 10 MB (five files
    per book) and 28 MB (one file per book, a new case) against 751 MB and
    294 MB for the old per-row `BookFile` map. The benchmark now resets its
    timer after setup.
