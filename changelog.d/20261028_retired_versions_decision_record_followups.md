### Fixed

- Retired-versions decision records, review follow-ups. A label-only record
  (applied, undone, approve, skip) no longer empties the shared cache of the
  owner's rejects, so an apply over many rows reads the decision log once, not
  once per row; a re-plan of a book that left the trash no longer loads the
  rejects at all. Finishing the tail of a retire that was cut off after its rows
  committed now writes the `applied` record the cut-off run missed. A revert
  writes `undone` only for a retire that actually moved rows into the version,
  not for a version a cut-off run created and never filled.
