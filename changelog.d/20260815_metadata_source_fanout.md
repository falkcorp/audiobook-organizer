### Changed

- Metadata search now queries sources **concurrently** instead of one after
  another. Previously every source in the priority chain was queried in series,
  and with up to four search attempts per source plus a scoring pass, a single
  book's search was measured at 13s on production.

  Only the I/O half is parallel. The dedupe/scoring merge stays sequential and
  in source order, because the source chain is priority-ordered and the dedupe is
  first-wins — parallelizing the merge would make which source wins a duplicate
  title+author nondeterministic between runs.

  Width is controlled by `metadata_scoring.source_fanout_workers` (default 4).
  This multiplies with `bulk_fetch_workers`: 4 books × 4 sources is 16 provider
  requests in flight, each still gated by that provider's own token bucket in
  `internal/metadata/providerhttp`.
