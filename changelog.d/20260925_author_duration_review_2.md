### Fixed

#### author-strip-merge never merges into a row the same run removes

A row that `delete_title_as_author` deletes (for example "Arcane Chef 2") could
also be the merge target of its numbered twin ("01 Arcane Chef 2"). With the
delete first, the merge wrote the deleted row's ID into the twin's books,
leaving dangling AuthorIDs. With the merge first, the delete unlinked the
merged-in books, which the title-as-author gate never judged. Planning now
drops any merge whose target is itself deleted or merged away in the same run,
before `limit` applies, so the dry run and the apply show the same plan. The
dropped merges are counted as `merge-target-removed` in the summary.

#### Book duration copy detection: a shared hash needs agreeing sizes

Legacy `book_files.file_hash` values hash only the first 1 MB, so distinct
tracks with an identical opening shared a hash and were collapsed into one
counted track. A shared hash no longer makes two rows copies when both sizes
are known and differ. Durations do not veto a hash match, because the two rows'
durations can come from different measurements that drift on VBR files.

#### Book duration copy keeper prefers the book's own-folder row

The row a copy cluster counts now ranks "inside the book's own folder" above
"has a measured duration". Before, a measured iTunes twin was kept over the
unmeasured library row, so `zero_rows_only` skipped the whole book as iTunes
and ABS listed the iTunes file.
