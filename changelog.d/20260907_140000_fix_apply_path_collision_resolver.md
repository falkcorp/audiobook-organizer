### Fixed

- **Applying metadata no longer fails forever on a book whose organized path is
  already taken.** `metadata.batch-apply-cached` wrote the metadata to the
  database and then failed its file write-back with
  `rename files: ... link <tmp-rename-nonce> <dest>: file already exists`, on
  every run, for the same books — because nothing ever resolved a target that a
  *different* file already owned. The rename step only ever noticed two files of
  the same book colliding with each other.

  The refusal itself was correct and is unchanged: `finalizeExclusive` is the
  shared primitive that stops six callers from silently overwriting another
  book's audio, and its documented contract puts the obligation on the caller.
  What was missing was the caller-side resolver, and that is what this adds. A
  pre-flight pass now runs **before any file is parked at a temp path** — the
  ordering is the guarantee, because a parked temp can be rolled back and a
  destroyed occupant cannot — and decides each occupied target on an identity
  ladder that goes cheapest-first: same inode, then a size mismatch, then the
  two stored file hashes, and only as a last resort a live digest (bounded by a
  process-wide semaphore, so a library-scale run cannot put every core on
  hashing multi-GB `.m4b` files).

  Nothing is destroyed on any branch. When the occupant holds the same bytes it
  wins — it is already at the organized path — and our copy is **moved into a
  quarantine tree under `.failed/_collisions`, never unlinked**, with the
  `book_file` row **repointed at the kept file, never deleted**. When the two
  differ, the rename falls back to the same `_copyN` ladder organize has always
  used, so a genuine name clash resolves identically whichever path reaches it.
  Every one of those pre-flight actions is journalled and undone if a later file
  in the same book fails.

- **One unresolvable book no longer breaks every later run.** A rename that
  failed used to skip its checkpoint deliberately, so the same doomed rename was
  re-attempted on every apply, forever, and the book never advanced. A book
  blocked by a collision that cannot be resolved is now recorded as durably
  failed and skipped — and comes back on its own the moment the blocking file
  goes away, changes, or the book starts targeting a different path. Transient
  failures (a stranded temp waiting for an operator, a NAS blip) keep the old
  retry-next-run behaviour, so an outage cannot turn into a library-wide skip
  list. A new maintenance operation, **Clear apply rename-failure records**
  (`maintenance.clear-apply-rename-failures`, dry-run by default), exists so a
  misclassification is never permanent.
