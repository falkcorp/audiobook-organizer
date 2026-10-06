- [ ] **SCAN-ID-FOLLOWUPS** Scan identity hold (#3799) review follow-ups, non-blocking:
      (1) a new import matched by content hash or an 80% segment vote copies its
      partner's title/author/series with no proposal (`row()` returns
      `byHash`/`bySegments`, `createdHere` suppresses it) — record a proposal at
      least, and reconsider the segment-vote case; (2) when the org-id relink falls
      through to the by-path save, gaps on the path row are filled from the
      org-id row's held identity; (3) `scan_identity_proposal:<id>` keys are not
      cleared on book delete/merge — `maintenance.scan-proposed-identity` must
      tolerate a missing book, or clear them; (4) AI-parse history rows use
      change type `scan` (source `scan.ai-parse`), so grouping by change type
      counts AI fills as scan writes; (5) re-measure `scannerStore`'s method count
      and fix the caveat comment in `internal/scanner/store.go`.
