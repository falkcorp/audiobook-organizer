- [ ] **OR-WITH-PER-USER-ANDED** A query that ORs an indexed term with a per-user
      term (`title:Alpha OR read_status:finished`) is evaluated as an AND.
      `translateOr` in `internal/search/bleve_translator.go` drops the per-user
      child (it returns no Bleve query for it) and appends it to the per-user filter
      list, which `MatchPerUserFilters` ANDs over the candidates of the remaining
      Bleve query, so the result is `title:Alpha AND read_status:finished`. Found
      while fixing per-user value alternation in 07-F5a (#4071), which only changed
      `field:(a|b)`. It affects the library search, smart playlists and the hourly
      dynamic-collection refresh. Fix properly: either represent OR over per-user
      terms in the filter tree (evaluate the whole boolean expression in Go when it
      mixes per-user and indexed terms, using Bleve only to fetch candidates) or
      refuse the mix with a clear error at translation time.
