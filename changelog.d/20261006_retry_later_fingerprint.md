### Fixed

- **Repairs: a transient hold no longer masks a real change.** A re-plan held
  on a transient condition (the version twin fixer's hold on an ISBN/ASIN
  index that is not built yet) is reported `retry_later` only when nothing
  else about the row changed since the trial. The fixer now also reports the
  fingerprint the row would have with the hold cleared, and the engine
  compares that one with the plan's; when the twin's record was refreshed (or
  any other input moved) while the index was unbuilt, the row is
  `changed_since_plan` and the trial has to be re-run. The version twin
  fixer's row fingerprint itself is unchanged, so plans made before this
  deploy stay valid; other fixers are unaffected.
- **Repairs: correction to the previous `retry_later` entry.** Only an
  approved planned row stays selectable and is retried when the re-plan holds
  it. A row that was already held `retry_later` when the trial ran is
  `not_applicable` until the trial is re-run.
- **Repairs lane:** the "Retry later" banner hint no longer repeats "were not
  written", which the row's outcome chip already says.
