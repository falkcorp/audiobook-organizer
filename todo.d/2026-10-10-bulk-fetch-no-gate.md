- [ ] **BULK-FETCH-NO-GATE** `POST /metadata/bulk-fetch`
      (`internal/server/handlers/metadata/handler.go`, `bulkFetchMetadataImpl`)
      applies the first non-review-only, non-rejected candidate of a fresh
      search with no certainty gate at all: no score floor, no sequence,
      identity, ASIN or owner-manual-only check. Since 2026-10-10 it skips
      owner-rejected candidates, but every other unattended apply runs
      `applygate`. Route its pick through the gate (with the book's live
      authors, canonical runtime and `applygate.LoadRejections`), report a
      refusal per book, and decide with the owner whether the endpoint should
      stay at all (commit 5c9b88dfc kept the web caller,
      `startBulkMetadataFetch`, until the owner decides the bulk-fetch
      feature).
- [ ] **UNREJECT-FROM-REVIEW** A rejected candidate can only be un-rejected
      through `POST /metadata/batch-unreject-candidates`, which needs the
      operation id the rejection was made in, and no web UI calls either
      candidate-reject endpoint (the review lane's Reject is the book-level
      "no match"). The review rail now marks owner-rejected candidates
      (`owner_rejected`), and no apply button applies them; add an
      un-reject action on that row that works from the book and candidate
      alone (delete the matching `rejected_candidate:` keys, re-rank the
      cached row) so the owner can override without an op id.
