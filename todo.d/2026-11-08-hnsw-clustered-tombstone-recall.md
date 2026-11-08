- [ ] **HNSW recall with clustered tombstones** (R1-15 review NIT-1). `FindSimilar`
      fetches `min(maxResults*4, 200)` nodes and drops tombstones afterwards, while a
      reconcile keeps up to 10% of a graph as tombstones (`hnswMaxTombstoneShare`). When
      more than `4*maxResults` tombstones sit nearer a query than its live neighbours (a
      group of near-identical versions merged away together), the query returns fewer
      than `maxResults` results, possibly none, although live neighbours exist. This is
      arithmetic from the code, not a measurement. Write a recall@k test (about 200 books,
      20 of them clustered and tombstoned, query at the cluster, compare against brute
      force); if it fails, re-search with a larger k when survivors fall short, or count
      tombstones toward the over-fetch.
