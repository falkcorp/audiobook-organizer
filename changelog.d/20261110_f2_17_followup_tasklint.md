### Changed

#### tasklint: a reviewed list of kind constructors, and kinds built and thrown away are reported (F2-17 follow-up, dark)

- `//tasklint:constructor` is allowed only on functions named in
  `Options.Constructors`. Core's list has one entry,
  `datamigrate.DerivedBuildTask`, and an entry that no longer matches is
  reported.
- `unregistered-kind` reports a kind that is plainly thrown away, even inside
  a function a registration reaches or a constructor's own body: a call left
  as a bare statement, a result assigned to `_`, or a local whose only read is
  `_ = x`. Reaching a function still covers its whole body, so a list filled
  by index into a made slice, through an adder closure, or from a table of
  rows passes.
- Registration counts only `Registrar.Tasks` and `Registrar.DerivedIndex`'s
  build, by name.

No runtime behaviour changes.
