- [ ] **03-PR12-STUBS** Remove the 410 stubs for /audiobooks/duplicates* one
      release after 03 PR 12 — the seven `duplicatesGone` registrations in
      `internal/server/wire_dedup_routes.go` and their test
      `internal/server/wire_dedup_routes_gone_test.go` (01 P81-style stub
      cleanup). Also decide, with 04, whether the `dedup.book-scan` op stays:
      its groups go only to the `book-dedup-scan` cache entry (plus one
      activity line with the counts), and no route reads that entry now.
