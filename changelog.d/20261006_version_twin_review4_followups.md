### Fixed

- **Repairs lane: an apply whose rows were all "retry later" no longer shows
  a green success banner.** The banner now uses the same severity as the apply
  toast, and says that "Retry later" rows were not written and can be applied
  again in a few minutes.
- **Repairs: a re-plan that holds on a transient condition is reported
  `retry_later`, not `changed_since_plan`.** A row a fixer marks
  `retry_later` (the version twin fixer's hold on an ISBN/ASIN index that is
  not built yet) stays selectable and is retried on resume, instead of being
  treated as settled.
- **Version twin fixer: every pair of known runtimes is compared.** A twin and
  a record whose runtimes disagree with each other (each close to the
  primary's) are no longer runtime evidence, so their ASIN is not copied; with
  the primary carrying the record's narrator the row is held as a conflict.
  "Agree" means within max(1%, 60 s), and the evidence text now says so.
