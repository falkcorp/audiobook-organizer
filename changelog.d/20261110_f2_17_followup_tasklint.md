### Changed

#### tasklint: a reviewed list of kind constructors, and kinds built and thrown away are reported (F2-17 follow-up, dark)

- `//tasklint:constructor` is allowed only on functions named in
  `Options.Constructors`. Core's list has one entry,
  `datamigrate.DerivedBuildTask`, and an entry that no longer matches is
  reported.
- `unregistered-kind` now follows what a definition is built from. A function
  counts only through what it returns, so a kind built inside a reached
  function (a constructor's own body included) and then discarded is reported.
- Registration counts only `Registrar.Tasks` and `Registrar.DerivedIndex`'s
  build, by name.

No runtime behaviour changes.
