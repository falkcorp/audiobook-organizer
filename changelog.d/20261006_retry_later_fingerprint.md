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
