### Fixed

- **Repairs lane: an apply whose rows were all "retry later" no longer shows
  a green success banner.** The banner now uses the same severity as the apply
  toast, and says that "Retry later" rows can be applied again in a few
  minutes (their outcome chip says nothing was written).
- **Repairs: a re-plan that holds on a transient condition is reported
  `retry_later`, not `changed_since_plan`.** When the re-plan of
  an approved, planned row holds on a transient condition (the version twin
  fixer's hold on an ISBN/ASIN index that is not built yet), that row stays
  selectable and is retried on resume, instead of being treated as settled. A
  row that was already held `retry_later` when the trial ran is
  `not_applicable` until the trial is re-run.
- **Version twin fixer: every pair of known runtimes is compared.** A twin and
  a record whose runtimes disagree with each other (each close to the
  primary's) are no longer runtime evidence, so their ASIN is not copied; with
  the primary carrying the record's narrator the row is held as a conflict.
  "Agree" means within max(1%, 60 s), and the evidence text now says so.
