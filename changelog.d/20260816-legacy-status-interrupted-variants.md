### Fixed

- Operations: a v2 operation that ended in `interrupted_quiesced` or
  `interrupted_restart` left its legacy operations row stuck at `pending`
  forever. The status mapper enumerated only three of the interrupted variants
  while `interruptedStatus` mints `interrupted_quiesced` for every resume policy
  except `ResumeDrop` — three of the four legal policies. Unmapped statuses
  returned early without writing or logging anything, which looked exactly like
  an operation that had no legacy row at all. It now matches on the `interrupted`
  prefix, so a new variant maps correctly without a code change here.
