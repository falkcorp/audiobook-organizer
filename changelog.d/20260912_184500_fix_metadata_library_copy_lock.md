### Fixed

- **An apply's library copy is made in one locked place, after the cover.** A
  protected (iTunes/import) book's library copy was made part-way through the
  apply's file work, and when making it failed once, a later step retried and
  made it with no lock on it, so another version's apply could write the same
  files at the same time. The copy is now made in one step, after the cover
  download (so it carries the new cover instead of the provider's URL), and is
  locked for the whole job.
- **A library copy that can't be used stops the job with a reason.** A copy
  that could not be made, that the file steps would not find, or that could
  not be looked up because the book read failed, now reports a file-side
  failure. The error names the cause: a file row left in the protected tree,
  or a book not linked to the copy's version group. The metadata apply itself
  still succeeds.
