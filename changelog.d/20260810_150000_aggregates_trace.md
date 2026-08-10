<!-- TODO.md evidence only — no code, no behaviour change, so this fragment is
     deliberately a no-op comment. See changelog.d/README.md.

     Traces the "corrected book aggregates are invisible until memdb refreshes"
     entry and records that its stated suspect does not fit the symptom: the op
     uses the batched DeleteBookFilesByIDs (not DeleteBookFile), that path
     already calls DeleteBookFilesFromMemDB plus notifyBookFileChange, and
     total_file_count is derived at read time from memdb rather than stored --
     so RecomputeBookAggregates' early return cannot have staled it.

     NOT a reproduction and NOT a root cause. Redirects the next investigator
     toward memSync's warmup buffering, which is consistent with the observed
     fix being a service restart. -->
