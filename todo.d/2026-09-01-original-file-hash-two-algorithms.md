- [ ] **TODO-ORIGHASH-SPLIT** `book_files.original_file_hash` has the same
      two-algorithms-one-column disease that `fix/file-hash-column-algorithm` fixed for
      `file_hash`, and it is still live. `fileops.WriteTagsSafe` writes a **whole-file**
      SHA-256 to it (`internal/fileops/write_tags_safe.go`, via `UpdateBookFileHashes`);
      `SetBookFileHash` back-fills it with the **chunked** `filehash.BookFileHash` when
      empty (`internal/database/pebble_store_bookfiles.go`). Both are 64 hex chars, so a
      row gives no clue which it holds. The column is consumed as identity —
      `GetDuplicateFilesByHash` groups `book_files` by it and a `book_file_orig_hash:`
      secondary index exists over it — so duplicates silently fail to group, the same
      failure mode as the `file_hash` split. Decide what the column MEANS first: it is
      named "original", so a tag-independent digest (`AudioMD5`) may be the right answer
      rather than either SHA. Do not unify the writers before answering that.
