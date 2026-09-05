- [ ] **After the download-fix set (#3075/#3076/#3077) is deployed, run the two repair ops** to clean up books already broken by the stale `book_file` pointer. Order and both report-only first:
  1. `maintenance.missing-file-repoint` — report-only, review, then apply. Repoints books whose file pointer was orphaned when an apply moved the file.
  2. `maintenance.merge-same-path-dupes` — report-only, review, then apply. Collapses the phantom duplicate book pairs the stale pointers created (same exact file, requires matching stored hash).
  - Deploy is the user's call (it resumes the weekly scan). Do NOT start a library scan to trigger this.
