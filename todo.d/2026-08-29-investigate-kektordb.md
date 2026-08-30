### Investigate kektordb as a vector-store option

<https://github.com/sanonone/kektordb> — evaluate it as a replacement or
supplement for the current embedding store, with a fork in mind if the shape is
close but not exact.

**Lowest priority.** This is an investigation, not a commitment, and it should
not displace any in-flight storage work.

What the investigation has to answer before anyone writes code:

- What does it actually persist, and does an index survive a restart? The
  current pain is HNSW snapshot staleness, so "rebuilds from scratch at boot"
  would be trading one problem for the same problem.
- Does it reclaim space on delete? PebbleDB does not until compaction, and that
  property is what makes the 30 GB production database hard to shrink. A vector
  store with the same behaviour buys nothing on that axis.
- Licence, release cadence, and single-maintainer risk — a fork is only cheap if
  the upstream is small. Measure the source size before assuming it is.
- Benchmark against the incumbent on OUR shape: ~61k books, real embedding
  dimension, recall at the operating point dedup actually uses. A synthetic
  benchmark will not settle it.

Decide explicitly between "adopt", "fork", and "no" — and if the answer is no,
record why, so the next person does not re-evaluate it from zero.
