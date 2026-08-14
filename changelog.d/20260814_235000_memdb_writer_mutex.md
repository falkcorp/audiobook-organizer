### Performance

- `UpsertBookToMemDB` no longer holds go-memdb's single global writer mutex
  across three Pebble reads (authors, narrators, and the full book-file prefix
  scan). The reads are hoisted to enqueue time — same snapshot contract as the
  existing struct copy. Benchmarked with realistic 30KB fingerprints: parallel
  UpdateBook 1.35–1.83 ms/op before → 1.21–1.25 ms/op after, with far lower
  variance. This was the system-wide ceiling that made worker pools on
  book-level ops buy less than NumCPU×.
