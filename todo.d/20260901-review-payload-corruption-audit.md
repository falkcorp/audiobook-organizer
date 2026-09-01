- [ ] **Add an op that reports undecodable review-hold payloads.** `ListReviewItems`'
      search path discovers them — an undecodable payload falls back to a raw-text
      match — but it deliberately does NOT count them, because that count would be
      a blind instrument: `reviewSearchMatches` returns on the first column hit, so
      a corrupt payload on a row that matched by summary is never decoded, and the
      total therefore varies with the search term rather than with the data. See the
      comment at the search pass in `internal/database/review_store.go`. Corruption
      is a property of the whole queue and needs a pass that decodes every payload
      once, unbiased by a needle: report count, kinds affected, and sample IDs.
