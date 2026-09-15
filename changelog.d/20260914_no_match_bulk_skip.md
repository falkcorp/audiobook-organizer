### Fixed

- Bulk metadata paths now honour a "no match" mark. An automatic apply
  (`ApplyOptions.FillOnly`) on a book whose `MetadataReviewStatus` is
  `no_match` returns `metafetch.ErrMarkedNoMatch` and writes nothing. Batch
  apply candidates, the cached batch apply, metadata upgrade and
  auto-match-transcribed all go through that one check.
  `FetchMetadataForBookByTitle`, which the production-company resolvers call,
  refuses these books too. The cached batch apply and batch-apply-candidates
  report them as skipped (`marked_no_match`), not failed. The bulk-apply
  preview leaves them out. Batch candidate fetch, bulk-fetch-metadata and
  metadata upgrade skip them before searching, so they use no provider quota.
  A person-picked apply (the single-book dialog, or approving a review-lane
  row) still goes through. It records the match, which clears the mark.
