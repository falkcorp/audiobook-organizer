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
- [ ] **UNREJECT-STILL-REJECTED-UI** `POST /metadata/unreject-candidate`
      returns `still_rejected: true` when another rejection still refuses
      the candidate (another source's rejection sharing its ISBN, or a
      legacy title-wide key). The single and bulk metadata search windows
      (`MetadataSearchDialog.tsx`, `BulkMetadataSearchDialog.tsx`) ignore it
      and clear the "Rejected by you" chip on every Undo, so the row looks
      pickable until the next search. Keep the chip (and say why) when
      `still_rejected` is true, and clear every other row whose ASIN the
      server un-rejected with it (review round 4, N2).
