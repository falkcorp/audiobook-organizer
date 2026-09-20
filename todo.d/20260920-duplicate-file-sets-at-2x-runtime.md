- [ ] **10 books hold their entire file set twice (exactly 2.00x runtime).**
      Found via `runtime_mismatch` gate refusals on 2026-09-20. Verified on
      01M07EDG4YNX7E7DEAAZXBE7FQ ("Star Divide"): two file rows,
      217,245,811 B and 217,214,993 B — the same audio with different tags, so
      the hashes differ and nothing deduped them. 01M2TAE4V238P1MFAWMT97CQTY
      ("Apocalypse Tamer") is the same shape at 82 file rows: 41 chapters
      attached twice.
      Both rows are `file_exists: true`, `missing: false`, so the "never delete
      book_file rows — REPOINT" rule does not govern: there is nothing to
      repoint to. This is on-disk duplicate territory. OWNER DECISION on the
      repair; until then these books can never receive metadata, and each one
      costs a gate refusal on every batch.
      Full list: the `runtime_mismatch` refusals at ratio 2.00 in those op logs.
