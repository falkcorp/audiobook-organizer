### Fixed

- **The book list could serve books you had filtered out.** When the database
  was still warming up after a restart, a request for a filtered page (for
  example "primary versions only") was answered correctly the first time, but
  the *unfiltered* results were saved under the filtered request's name. Every
  later identical request was served those saved results, including books the
  filter was meant to exclude. It affected any excluded book, not just an
  edge case, and cleared itself once warmup finished — so it looked
  intermittent. The saved copy is now only reused when it genuinely answers
  the request that asked for it.
