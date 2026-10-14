- [ ] **STARTUP-GC-WAIT-INDEX** 06-P13 follow-up: `ListWaitingDepsOps`
      (`pebble_store_ops_v2.go`) scans and decodes every `opv2:op:` row because
      there is no status index. Add an `opv2:wait:` index written by `stageOpRow`
      and read by `DepsScheduler.SweepTick`, `reevaluateSubject` and
      `rebuildIndex`; this removes about 7 GB of allocation per 25 minutes on the
      production heap. Brief it after 05-PR4 merges (same file, same writer).
- [ ] **STARTUP-GC-MEMDB-SHRINK** 06-P13 follow-up: shrink the memdb live heap,
      which sets the cost of every GC cycle. Strip `BookFile.RawTags` (447 MB of
      maps plus a share of 862 MB of strings; `signal_coverage.go` needs
      presence only), replace the 2.1 KB `BookFile` shallow copies (670 MB) with a
      narrow row type, and review the 7 `book_file` indexes (radix nodes are 55
      percent of the live objects). Needs an owner decision first.
- [ ] **STARTUP-GC-DEDUP-CPU** 06-P13 follow-up: dedup CPU. Parallelize
      `groupMetadataBucket` over disjoint buckets, score each pair once with
      precomputed normalized forms instead of twice from scratch, and make
      Jaro-Winkler and Levenshtein allocation-free (`matcher/fuzzy.go`,
      `dedup/author.go`).
