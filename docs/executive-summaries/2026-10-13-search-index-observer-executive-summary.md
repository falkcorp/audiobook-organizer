<!-- file: docs/executive-summaries/2026-10-13-search-index-observer-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e8a1c57-9d24-4b60-a7f1-5c2b9e4d0a83 -->
<!-- last-edited: 2026-10-13 -->

# Search now follows every book change through one path, not two

Branch `task/07-s3` (07-S3).

## Executive Summary

- **Background.** The search box reads from a separate search index that has
  to be told whenever a book changes. Two different mechanisms used to do
  that telling. One wrapped the library database and noticed only five kinds
  of save. The other sat inside the database and noticed everything else
  (tags, running time recalculations, author and series renames). Because
  they disagreed about which changes counted, a change that only one of them
  knew about could leave search showing an old title or an old author until a
  periodic repair pass happened to notice.
- **What could go wrong.** A change made by a part of the program that did not
  go through the wrapper, for example editing only the credited authors or
  narrators of a book, was not guaranteed to refresh search. The wrapper also
  hid several database features from the code that needed them, which in the
  past silently switched off repair jobs on the live server.
- **The fix.** The wrapper is gone. The database now tells the search index
  about every change it actually commits, and that is the only path. Credit
  edits now refresh search by themselves. Author-list matching for credits
  (used when importing and repairing books) is handed over through a small,
  explicit hookup instead of through the wrapper.
- **What has not changed.** Nothing visible in the app changes. Search results,
  the repair pass, and the setting that turns the author lists on or off behave
  as before. Tests cover each kind of change, many changes at once, and the
  author-list hookup.
