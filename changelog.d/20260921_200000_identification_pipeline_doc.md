### Added

- Architecture doc mapping the identification pipeline end to end
  (`docs/architecture/identification-pipeline.md`): a stage spine, a per-file
  decision tree with live exclusion counts, and the signals we collect but never
  score.
- A target-state proposal in the same doc (Part II): a per-file identification
  state machine built as an extension of `database.ScanState`, a continuous
  low-priority driver that advances each file, and the three changes that close
  today's dead ends — an inline duration header read, fingerprinting the iTunes
  tree, and a real full-file fpcalc pass.
