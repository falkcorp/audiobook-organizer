- [ ] **SERIES-ID-MISMATCH-COUNT** After the series-object drop history PR
      deploys, run a `maintenance.relink-stale-series` trial on prod and record
      the library-wide `held-series-id-mismatch` count (`by_class`). The list
      endpoints read memdb, which strips the embedded object, so the only API
      count today is the 52 books the swapped title/author dry run held
      (01M42PT0K9KXH6YB0ECR9H3077, all 52 confirmed by single-book GET on
      2026-10-04). The count shrinks with every book write since #3698
      deployed; drops before this PR are only in `book_ver:` snapshots.
