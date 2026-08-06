<!-- file: todo.d/20260805_220400_metadata_results_cold_start.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3690b58-1e7a-4f24-a905-62c8f7bd031e -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Warm the metadata-results build at boot** — owner item 6 (2026-08-05).

  The metadata-results build takes **34 s cold**. It was memoised (60 s TTL, PR
  #2142) but is **not warmed at startup**, so the first person to open the match
  UI after a restart eats the full 34 s. Warm it on boot.

  Same cold-path class as authors/narrators failing to load on first paint —
  worth fixing together rather than one at a time, since the pattern (expensive
  aggregate, memoised but never pre-populated) recurs.

  Small and independent of the First Aid track; good candidate to pick up while
  larger work is in flight.
