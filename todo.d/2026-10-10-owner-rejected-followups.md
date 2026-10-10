- [ ] **UNREJECT-FROM-REVIEW** The review rail marks a row whose candidate
      the owner rejected (`owner_rejected`), and no apply button applies it,
      but the row has no way back: undoing a rejection takes the book's
      metadata search window ("Rejected by you", Undo, which calls
      `POST /metadata/unreject-candidate`). Add the same Undo to the review
      row (`web/src/components/review/QueueRail.tsx`), calling that endpoint
      with the book and the row's candidate, then refresh the row.
- [ ] **VT-COPY-RERANK** The version-twin metadata fixer copies another copy's
      cached candidates onto the primary
      (`metafetch.Service.CopyCandidateCache`, called from
      `version_twin_metadata_fixer.go`) without re-ranking the copy against the
      receiving book's own rejections, so a candidate the owner rejected for
      that book can sit first in its copied row until the next search or
      reject rewrites it. Every apply still refuses it; re-rank the copied
      row (`metafetch.Service.RerankCachedCandidates` with
      `metabatch.MergeRankerFor` for the receiving book) when it is written.
