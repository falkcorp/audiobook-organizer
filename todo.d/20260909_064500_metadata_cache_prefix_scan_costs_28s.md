- [ ] **Find out why scanning the `metadata_cache:` prefix takes ~28s.**
      `PebbleStore.ListMetadataCacheKeys` is a constant ~28s on production
      regardless of what the caller asked for, and it is the floor under BOTH
      `GET /audiobooks/metadata/cached` and `GET /audiobooks/metadata/cache/review`.
      Paging (PR for `fix/metadata-cached-limit-and-batch`) cut the response body,
      not this.

      **Measured on production 2026-09-09** (a live instance with apply jobs
      running, so treat the rates as indicative, not exact — successive calls
      drifted 22s → 27s → 29s → 30s upward):
      - `cached?limit=3|5|100` — all returned every row, ~20–24s. Constant in `limit`.
      - `cache/review?limit=5` = 29.3s, `?limit=200` = 30.3s. So 195 extra FULL
        entry decodes cost ~1.0s (~5ms each), and the ~28s constant is the scan.
      - The review endpoint does strictly MORE per-row work yet is no faster than
        the listing that did 40,549 `GetBookByID` point reads. **The per-book reads
        were never the cost** — which is why the batch read in that PR is justified
        by correctness (filter-before-paginate needs every row's status), not speed.
      - 40,549 entries, 134,407 candidates total, mean 3.31 per entry; 11,624
        entries (28.7%) hold zero candidates.

      **Hypothesis already tested and REFUTED — do not retry it.** Decoding the
      stored value into `MetadataCandidateCache` just to read `len(Candidates)`
      looks wasteful (`json.RawMessage.UnmarshalJSON` is `append(...)`, so it
      copies every candidate's bytes to then discard them). Replacing it with a
      counting decode that allocates nothing benchmarked **1.6x SLOWER**
      (10 candidates x 4KB: 73µs full decode vs 98µs counting; allocations
      16 → 1). A custom `UnmarshalJSON` makes encoding/json scan the array to
      find its extent and *then* hand it over to be scanned again — two passes,
      where the full decode does one pass plus a memmove. Allocation count is not
      cost.

      **Cause of the remaining ~28s is UNIDENTIFIED.** Candidates:
      1. LSM read amplification over the `metadata_cache:` prefix — the iterator
         merging across many overlapping SSTables. Plausible given the store is
         overdue for compaction, but unverified.
      2. GC pressure from the per-request garbage against a large live heap. The
         benchmark above ran in a small-heap process, where allocation is at its
         cheapest, so it does NOT rule this out for production.

      Note the entry-size figure (~3.1 KB, from 937 bytes x 3.31 candidates) is
      derived from a *rendered* candidate in the review response, not measured at
      the store. If the stored provider payload is larger, every rate above moves
      with it. Getting a real per-prefix byte count (Pebble exposes
      `EstimateDiskUsage` over a key range) would settle both the size question
      and hypothesis 1 cheaply, and should come before any redesign.
