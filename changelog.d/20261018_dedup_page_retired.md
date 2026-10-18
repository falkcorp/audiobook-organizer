- The old Dedup page is retired. Its jobs now live in the Review page: the
  Duplicates lane (layer filters, bulk keep-older and keep-newer, Clusters,
  CSV export, Labels and Suspicious views), the new Authors & series lane
  (author and series duplicates, AI scans) and the Repairs lane (empty-series
  pruning, missing-file matches, split-book clusters).
- `/dedup`, `/dedup/labels`, `/authors/dedup` and `/books/dedup` redirect into
  `/review`; a `?tab=` value picks the matching lane, and `?book=` and `?band=`
  are kept.
- The seven `/api/v1/audiobooks/duplicates*` routes (five routes and their two
  deprecated aliases) answer 410 Gone for one release and are then removed.
- `docs/AI-REFERENCE.md` now lists the Review page and its five lanes in place
  of the old dedup page; the two old port-inventory docs carry a superseded
  banner.
