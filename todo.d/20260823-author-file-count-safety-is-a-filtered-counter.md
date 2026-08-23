- [ ] **AUTHOR-FILE-SAFETY: `purge-empty-authors`' "safety that matters" is itself a
      filtered display counter, so it cannot hold back a single case the ref guard
      exists for.** `author_purge_empty.go` labels `require_zero_files` "🔴 THIS IS THE
      SAFETY THAT MATTERS" and defaults it ON, to protect the 822 authors whose
      zero-book count looks more like a broken link than an empty author. It reads
      `GetAllAuthorFileCounts`, and BOTH implementations
      (`memdb_reads.go` ~L299-344, `pebble_store_authors.go` ~L658-731) scan only the
      primary-version index, skip soft-deleted books, and map books to authors via the
      legacy `Book.AuthorID` field only — never the junction. So `fileCounts[id]` is
      unconditionally 0 for a junction-only co-author, and for any legacy author whose
      books are all trashed or all non-primary. Those are exactly the three populations
      `author_bookref.go` documents as the bug. The candidate selector, the ref gate and
      the file safety all read the same lossy memdb, so they are correlated, not
      independent. The same function also still carries the three defects fixed in the
      ref scan by #2787: an undecodable book row is silently skipped, `iter.Error()` is
      never checked, and a `GetBookFilesForIDsCore` error is swallowed. Found by review
      on #2787; deliberately not fixed there to keep that PR's diff reviewable.
