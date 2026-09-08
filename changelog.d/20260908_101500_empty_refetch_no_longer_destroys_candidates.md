### Fixed

- **A metadata refetch that came back empty no longer destroys the candidates an
  earlier fetch found.** `cacheSearchResponse` wrote the cache unconditionally — its
  own doc comment said "always replaces" — so one provider outage, one rate-limit
  window or one mis-parsed title was enough to overwrite a good entry with an empty
  one *and stamp it fresh on the way out*. Two things kept that invisible. A book's
  review verdict lives on the book, not on the cache entry, so the verdict outlived
  the evidence behind it: production held **212 books carrying a
  matched/no_match/audio_confirmed ruling with zero candidates to justify it**. And
  a zero-candidate row is filed as "unreviewable", so an emptied book did not look
  damaged — it simply left the review queue. Of the 11,372 unreviewable rows, 8,714
  were marked *fresh*, because an empty refetch marks itself current.

  This stops new damage; it does not repair the old. An emptied entry keeps no
  record of what it used to hold, so the damaged rows are no longer distinguishable
  from books the providers genuinely have nothing for. Recovering them means
  refetching, which is a separate job with provider-quota consequences.

  An empty result now preserves the stored candidates when the search inputs are
  unchanged (`SourceHash`, previously stored but documented as diagnostic-only, is
  the discriminator) and records the fruitless look in a new `LastEmptyFetchAt`
  instead. Changed inputs still replace, because candidates for a title the book no
  longer has are worse than none. Dropping a book's cache when its metadata really
  did change is still `InvalidateCachedCandidates`, which is where that decision
  belongs.

- **The metadata review chips report the library instead of a slice of it.** `stale`
  was counted only over the *reviewable* rows, which structurally hid every stale row
  that had no candidates — exactly the rows a refetch would help. The chip read
  **11 stale** while 2,658 stale zero-candidate rows sat unseen in the unreviewable
  bucket, understating the backlog **242x**. Staleness is now counted across every
  non-orphaned row, and is dated from the last time the book was *searched for*
  rather than the last time candidates were *stored* — so a book whose providers came
  back empty this morning is no longer permanently overdue, re-picked by every
  refetch pass forever.

- **Books that have already been ruled on are no longer filed as "unreviewable".**
  They are reported separately as `resolved_no_candidates`: there is nothing for a
  reviewer to do with them, and counting settled work as a backlog is what made the
  unreviewable total unactionable.

- **Decode errors have their own chip.** A stored candidate that will not decode is a
  broken row someone has to repair; a book the providers simply have nothing for is
  normal. Both were folded into one warning-coloured "unreviewable" total, so a real
  corruption problem read identically to a backlog and could only be found by
  hovering. `summary.errors` had been plumbed all the way to the UI type and never
  rendered.

- **`audio_confirmed` counts as a verdict.** `metafetch/service_apply.go` has been
  writing it since the audio-confirmation pass landed, but the doc comment on
  `database.Book.MetadataReviewStatus` still described the vocabulary as
  `null, "no_match", "matched"`, and the review handler's switch trusted the comment.
  Books confirmed against their own transcribed audio — a stronger signal than a human
  eyeballing a title — were reported as still awaiting review.
