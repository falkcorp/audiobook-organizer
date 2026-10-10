### Fixed

- A metadata candidate the owner rejected (`POST /metadata/batch-reject-candidates`) is no longer applied by any of the nine apply paths a code search found. Before, the rejection was only a key the batch fetch's own pick read, so these applied it:
  1. a pinless `metadata.batch-apply-cached`, and every review-page apply button (row, bulk and the hashless bulk marker);
  2. `batch-apply-candidates` and `metadata.apply-when-scanned` against another op's still-"matched" result;
  3. `maintenance.auto-match-transcribed`;
  4. the nightly metadata upgrade, which overwrote filled fields;
  5. `POST /metadata/bulk-fetch`;
  6. the single-book apply (`POST /audiobooks/:id/apply-metadata` and its queued form);
  7. `FetchMetadataForBook` (the iTunes import enrichment, organize's fetch-first, `POST /audiobooks/:id/fetch-metadata` and its queued form);
  8. `FetchMetadataForBookByTitle` (the production-company resolvers);
  9. the version-twin fixer's applied-twin apply.
- The certainty gate has a hard `owner_rejected` leg (and `owner_rejection_check_failed` when the rejection list cannot be read) that no owner-review pin lifts. The paths that skip the gate check rejections themselves:
  - Transcription auto-match and bulk-fetch.
  - The single-book apply refuses with 409 `owner_rejected`.
  - The two auto-fetch functions read rejections once, before any provider is searched, and fail closed with `ErrOwnerRejectionCheckFailed`. A rejected best record falls through to the source's next record, then to the next source. When only rejected records match they apply nothing and return `ErrOwnerRejectedNotApplied`; the fetch endpoint answers 200 with `owner_rejected: true`.
  - The version-twin fixer holds the row (`owner_rejected`, or `owner_rejection_unknown`) and its write guard refuses it again at the commit.
- A rejection's key now stores the rejected record's identifiers: its ASIN, or its ISBNs when it has no ASIN. When both the rejection and a candidate carry an identifier of the same kind, they match on it, so rejecting one edition no longer refuses another edition with the same source and title. Otherwise matching falls back to source and title (case-insensitive). Legacy keys (value `1`) keep refusing the whole title.
- `POST /metadata/unreject-candidate` un-rejects one candidate for one book, with no operation id, under the same permission as the reject endpoints. Afterwards the candidate matches no rejection; another record of the title rejected by its own identifier stays rejected. The single and bulk metadata search windows mark a rejected candidate "Rejected by you" with an Undo button.
- Rejecting or un-rejecting a candidate re-orders the book's cached row (rejected candidates last, by the merge ranking). So does a search that replaces the row, including the search dialog's fetch, so a refetch cannot put a rejected candidate back first. Both read the rejections under the cache-row lock, so neither can write an order computed from an older rejection set. A one-time background pass re-orders every existing row that holds a rejected candidate (`system:migration:owner_rejected_rerank_v1_done`); it never removes a candidate.
- The review rail marks a row whose candidate the owner rejected (`owner_rejected` on the review list).
