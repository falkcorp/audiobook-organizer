- [ ] **SERIES-NORMALIZE-OP-DRY-RUN** `dedup.series-normalize` lost its only
      preview when 01-P72 deleted `GET /series/normalize/preview` and its
      builder. The op moves files and has no dry-run mode (it is absent from
      `write_op_modes.golden`). Make preview/dry-run a mode of the op itself,
      built on `computeSeriesNormalizeActions`, so an operator can see the
      planned renames before approving a run.
