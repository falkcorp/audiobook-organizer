- [ ] **BULK-FETCH-NO-GATE** `POST /metadata/bulk-fetch`
      (`internal/server/handlers/metadata/handler.go`, the loop around the
      `IndexFunc` pick of the first non-review-only `searchResp.Results` row)
      applies the search's top candidate to each book with no `applygate`, no
      score floor, no sequence guard and no identity check; only the
      review-only source rule, field locks and `onlyMissing` limit it. Route it
      through `applygate.Evaluate` the way the cached batch apply does, or retire
      the endpoint in favour of the gated batch apply. Found in the round-4
      review of 02-PR9a (#3900); that PR does not change this pick.
