- [ ] **NO-MATCH-BULK** Make the bulk and batch-candidate metadata paths honour
      a "no match" rejection. Marking a book "no match" sets
      `MetadataReviewStatus = "no_match"` and wipes its fetch-cache rows and
      candidate cache (#3421), but only `FetchMetadataForBook`
      (`internal/metafetch/service_fetch.go`) refuses such books. The bulk fetch
      and batch-candidate paths never check the status, so the next bulk run
      can query providers again and offer or apply a match for a book the user
      rejected. Done means every bulk and batch entry point skips (or reports)
      `no_match` books, with a test per path that fails without the check.
