- [ ] **FILTER-UNCHANGED-TAGS-REWRITES-UNVERIFIABLE-KEYS** The metafetch write-back
      (`FilterUnchangedTags` in `internal/metafetch/service_writeback.go`) keeps a
      key in the write whenever the file's value differs from the wanted one. A key
      the container cannot hold (for example a `track` that is not an integer on an
      MP4 file, or a value TagLib cuts) never reads back as wanted, so with
      `verify_after_write` on every run re-writes the whole file, fails verification
      on that key (`tagger.ErrVerifyAfterWrite`), and records another observed
      event, forever. Make the filter skip such a key, or back it off, once a
      verification failure names it: for example remember the failing keys per
      `book_file` row (the verification event already carries the key and the
      value read) and leave them out of the next write until the wanted value
      changes. Found reviewing verify_after_write (#3968); not fixed there.
