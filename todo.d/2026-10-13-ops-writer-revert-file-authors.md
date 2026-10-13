- [ ] **OPS-WRITER-REVERT** Teach the operation revert to reverse the
      record-only change types the v3 Writer writes: `book_file_modify`
      (`ModifyBookFile`, one row per changed field) and `book_authors_modify`
      (`ModifyBookAuthors`, the whole credit list before and after). Until then
      the revert refuses them; `book_file_repoint_location` rows from
      `RepointBookFile` already revert.
